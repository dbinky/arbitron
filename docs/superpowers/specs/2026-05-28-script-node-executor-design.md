# Script Node Executor — Design

**Date:** 2026-05-28
**Status:** Approved (design); pending implementation plan
**Branch:** `feat/script-node-executor`

## Motivation

The `pr-review` smoke test surfaced a gap. The workflow engine has a `script`
node type declared in `domain/workflow/workflow.go` and validated in
`domain/workflow/validation.go` (requires `scriptFile`), and `NodeResult`
already reserves `ScriptStdout`, `ScriptStderr`, and `ScriptExitCode` fields —
but **there is no executor implementation**, so the engine returns "no executor
registered for node type: script" at runtime.

Two concrete needs motivate building it now:

1. **A true failure gate.** During the smoke test, every review persona ran
   under a broken environment (no `bash`), yet the claude-cli subprocess exited
   `0` while politely reporting it was blocked. The instance therefore went
   **green** despite doing no real work. Real subprocess failures *do* propagate
   to a red `failed` instance (confirmed when `10x-r1` later exited non-zero),
   but a *graceful* "I'm blocked" cannot be caught with the existing node types
   because none of them deliberately fail. A `script` node whose non-zero exit
   fails the node gives us a **preflight gate**: verify `bash`, `gh` auth, the
   repo clone, and the PR exist before spending six LLM calls; `exit 1` on a
   bad environment turns the instance red immediately.

2. **General shell steps**, including scripts that call LLM HTTP endpoints
   (not necessarily Claude) and therefore need an API key and a router URL in
   their environment.

## Goals

- Implement the `script` node type end to end: domain port, domain executor,
  adapter, wiring, validation, tests.
- Non-zero exit fails the node (and thus the instance), with the captured
  stderr tail surfaced in the error.
- A controlled environment model that lets a script reach operator-provisioned
  secrets (LLM keys, router URLs) **without** placing those secrets in the
  committed workflow bundle and **without** leaking every engine secret to
  arbitrary scripts.

## Non-Goals

- The `tool_call` node type (also declared-but-unimplemented) — out of scope.
- Shells other than `bash`. Scripts run under `bash`; `bash` is installed in
  the image.
- Auto-push or any change to the `pr-review` personas. Adding the preflight
  node to `pr-review` is a follow-on that consumes this feature; it is noted
  here but specced/implemented separately.
- A general per-node secrets mechanism for `llm_call` nodes.

## Architecture

Follows the established hexagonal pattern, mirroring how `LLMCallExecutor`
delegates subprocess work to the `LLMProvider` port (implemented by the
claude-cli adapter, which itself uses an internal `CommandRunner`).

### Domain port (`domain/engine`)

```go
// ScriptRunner runs a shell script subprocess. Adapters implement it.
type ScriptRunner interface {
    Run(ctx context.Context, req ScriptRequest) (ScriptResult, error)
}

type ScriptRequest struct {
    Script       string            // resolved script body (substitutions applied)
    WorkDir      string            // cwd; the instance workspace
    Env          map[string]string // final env (see Env Model); nil → inherit nothing but base
    TimeoutSecs  int               // 0 → engine default
}

type ScriptResult struct {
    Stdout   string
    Stderr   string // bounded tail
    ExitCode int
}
```

`Run` returns a non-nil `error` only for **execution faults** (spawn failure,
timeout, stdout cap exceeded). A script that runs to completion with a non-zero
exit is **not** a `Run` error — it returns a `ScriptResult` with a non-zero
`ExitCode`. The executor decides what a non-zero exit means (it fails the node).
This keeps the "process ran but the script failed" vs "we couldn't run the
process" distinction explicit.

### Domain executor (`domain/engine/executor_script.go`)

`ScriptExecutor` depends on the existing `FileReader` port and the new
`ScriptRunner` port. `Execute`:

1. Reads `node.Config.ScriptFile` via `FileReader.ReadFile(workflowName, ...)`.
2. Resolves `{{var}}` substitutions in the body with `variable.Resolve`.
3. Resolves the work dir with `WorkDirFromVars(vars)` (same as the LLM executor).
4. Builds the final environment (see Env Model).
5. Calls `ScriptRunner.Run`.
6. On `Run` error → return a wrapped error (node fails).
7. On non-zero `ExitCode` → return an error of the form
   `script %q: exited %d: %s` where `%s` is the stderr tail (node fails,
   instance goes red, reason is visible).
8. On exit 0 → return `&NodeResult{Output: stdout, ScriptStdout, ScriptStderr,
   ScriptExitCode: 0}`. `Output` is the trimmed stdout so a downstream
   `conditional` (`contains`, `eq`, …) can branch on it.

### Adapter (`adapter/script`)

Implements `engine.ScriptRunner` with `os/exec`:

- `exec.CommandContext(ctx, "/bin/bash", scriptPath)` where the resolved body is
  written to a temp file under `WorkDir` (cleaned up after), or piped via
  `bash -s` on stdin. (Temp-file approach chosen: clearer errors, supports
  multi-line scripts and shebangs cleanly.)
- `cmd.Dir = WorkDir`.
- `cmd.Env = sorted "k=v"` from `req.Env` only (no implicit `os.Environ()`).
- stderr captured into a bounded tail buffer (reuse the 64 KB tail-buffer
  approach already in `adapter/claudecli/runner.go`).
- stdout captured with a configurable cap (mirror claude-cli's
  `MaxResponseBytes` DoS bound); exceeding the cap → execution-fault error.
- Timeout: `req.TimeoutSecs` (falling back to the engine default) drives the
  context deadline; on deadline the process is killed and `Run` returns a
  timeout error.

### Shared env allowlist

The base allowlist currently lives in `adapter/claudecli/claude.go`
(`envAllowlist` + `augmentedEnv`). Extract the allowlist set and the
PATH-augmentation helper into a new shared adapter package `adapter/procenv`
that both `adapter/claudecli` and `adapter/script` import, so there is a single
source of truth. `adapter/claudecli` is refactored to consume it (its existing
behavior unchanged, covered by its current tests). Domain purity is preserved —
this is adapter-side only.

## Env Model (three layers)

The final environment handed to a script is the union of:

1. **Base allowlist (always), read from the engine process env:**
   `PATH, HOME, USER, LOGNAME, SHELL, LANG, LC_*, TERM, TMPDIR`. Ensures
   `bash`, `gh`, `git`, `go`, `curl` function. Same safe set as claude-cli.

2. **Operator secret pass-through, from config, by name:** a new config
   setting `script.envPassthrough` (env: `ARBITRON_SCRIPT_ENVPASSTHROUGH`,
   comma-separated). Lists additional engine-env variable **names** that script
   nodes may inherit — e.g. `LLM_API_KEY,LLM_ROUTER_URL,OPENAI_API_KEY`. The
   secret values live in the deploy/container environment, never in the bundle.
   Only names on this list are passed; everything else in the engine env is
   stripped. This is how a script reaches an arbitrary LLM router and key.

3. **Per-node `env` map, from the bundle, literal:** a new optional
   `node.Config.Env` (`map[string]string`, JSON `env`), with `{{var}}`
   substitution. For **non-secret** configuration the author wants visible in
   the workflow (e.g. `LLM_MODEL={{model}}`). Applied last.

**Collision / spoofing rule (validation):** a per-node `env` key MUST NOT match
any name in the base allowlist or in `script.envPassthrough`. This prevents a
committed bundle from shadowing or spoofing a secret/base variable. Violations
are a validation error (`INVALID_NODE_CONFIG`).

**Precedence:** base, then pass-through, then per-node — but because of the
collision rule the sets are disjoint, so precedence never actually overwrites a
secret. (Rule + precedence are both tested.)

## Config additions

`domain/config/config.go` gains a `ScriptConfig` (sibling of `LLMConfig`):

```go
type ScriptConfig struct {
    // EnvPassthrough lists engine-env variable NAMES that script nodes may
    // inherit, in addition to the base allowlist. Values are never stored
    // here — only names. Secrets stay in the deploy environment.
    EnvPassthrough []string
    // DefaultTimeoutSeconds bounds a script node when its node config sets
    // no timeout. 0 means no timeout.
    DefaultTimeoutSeconds int
}
```

Loaded via koanf like the rest of the config. Documented as a security-relevant
setting (parallels the existing `SkipPermissions` warning comments).

## Node config additions

`domain/workflow/workflow.go` `Config` gains:

```go
Env map[string]string `json:"env,omitempty"` // script nodes: non-secret env
```

`ScriptFile` and `TimeoutSeconds` already exist. Validation already requires
`scriptFile`; we add the per-node `env` collision check.

## Security considerations

- A `script` node runs **arbitrary shell** — RCE-equivalent, like the
  `senior-engineer` persona's claude already is. The **container is the security
  boundary**, consistent with `ARBITRON_LLM_SKIPPERMISSIONS=true` in the test
  rig. This must not be enabled in a multi-tenant production without isolation.
- The env model is the mitigation against secret leakage: scripts get only the
  base allowlist plus explicitly named pass-through vars. The default engine
  `os.Environ()` (which carries `ANTHROPIC_API_KEY`, DB URLs, etc.) is never
  passed wholesale — the same principle PR #8's security review established for
  claude-cli.
- stdout cap + stderr tail bound the DoS surface (a hostile script cannot flood
  memory).
- The collision rule stops a committed bundle from spoofing a secret var name.

## Testing (strict TDD — five categories)

- **Happy path:** exit 0; stdout captured to `Output`; `ScriptStdout/ExitCode`
  set; `{{var}}` substitution applied.
- **Success scenarios:** env layering correct (base present; named pass-through
  present; per-node env present; a non-passthrough engine var absent); work dir
  is the workspace; per-node `env` value substitution.
- **Failure scenarios:** non-zero exit → node error containing exit code +
  stderr tail; instance transitions to `failed`.
- **Error scenarios:** `scriptFile` missing/unreadable; runner spawn failure;
  timeout kills the process and errors; stdout exceeds cap.
- **Edge cases:** empty script; very large stdout (cap boundary); per-node `env`
  name colliding with a pass-through/base name rejected at validation; empty
  `envPassthrough`; a pass-through name that is unset in the engine env (absent,
  not empty-string injected).

Coverage spans the domain executor (with a fake `ScriptRunner` + fake
`FileReader`), the adapter (real `os/exec` against trivial scripts), the config
loader, and workflow validation.

## Consumer (follow-on, not in this spec)

Once shipped, add a `preflight` script node as the `pr-review` entrypoint:
checks `bash --version`, `gh auth status`, the repo clone, and `gh pr view
{{pr_number}}`; `exit 1` with a clear message on any failure, so a broken
environment fails fast and red instead of burning six LLM calls.
