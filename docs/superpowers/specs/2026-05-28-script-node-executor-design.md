# Script/Tool-Call Env Hardening + CommandRunner Extraction — Design

**Date:** 2026-05-28
**Status:** Approved (design); pending implementation plan
**Branch:** `feat/script-node-executor`

## Correction to the original premise

An earlier version of this spec assumed the `script` node type was unimplemented
and designed it from scratch. **That was wrong.** The executors already exist and
are wired and tested:

- `adapter/process/executor_script.go` — `ScriptExecutor`: resolves
  `node.Config.ScriptFile` via `FileReader.ResolvePath`, runs it under an
  interpreter (`node.Config.Interpreter`, default `/bin/sh`) with substituted
  `node.Config.Args`, pipes the variable-store snapshot as JSON to **stdin**,
  bounds stdout/stderr (1 MiB `limitedWriter`), enforces `TimeoutSeconds` with a
  process-group kill, and on success parses **stdout as JSON** into `Output`.
- `adapter/process/executor_toolcall.go` — `ToolCallExecutor`: same lifecycle,
  runs `node.Config.Command` + args, no stdin.
- Tests: `executor_script_test.go` (7) and `executor_toolcall_test.go` (8)
  cover success, non-zero exit, timeout, non-JSON stdout, arg substitution,
  stdin vars / no-stdin, and not-found.

Critically, **non-zero exit already fails the node** (the executor returns an
error → the engine marks the instance `failed`/red) and **the stderr tail is
already included in that error**. The original motivating goal — "true red on a
real failure, with a visible reason" — is already satisfied for script/tool
nodes.

## The actual gap

Neither executor sets `cmd.Env`. The subprocess therefore inherits the engine's
**full `os.Environ()`** — every secret the engine started with (`ANTHROPIC_API_KEY`,
database URL, etc.) is exposed to any script or command a workflow runs. The
in-file `SECURITY:` comments acknowledge this as a Phase-1 "trusted authors"
posture.

This is the real, valuable work, and it is also what makes the operator's stated
need safe: a script that calls an LLM HTTP endpoint needs an API key and a router
URL in its environment. Today that "works" only because **everything** leaks;
we want it to work via an **explicit named pass-through** instead.

## Goals

- Replace whole-environment inheritance in both process executors with a
  controlled environment (the three-layer model below), so secrets no longer
  leak wholesale and operators can expose exactly the vars a script needs.
- Honor the standing `TODO(refactor)` at the top of both executor files
  ("Extract a CommandRunner interface the moment this executor is touched"):
  extract the shared process lifecycle **and** the env construction so the two
  executors stop duplicating ~40 lines and the logic becomes unit-testable with
  a fake runner.
- Maintain all existing behavior and tests (interpreter default, stdin-JSON,
  stdout-JSON, timeout, output bounds, exit-code semantics).

## Non-Goals

- Changing the script contract (stdin-JSON in, stdout-JSON out, interpreter,
  args) — unchanged.
- Sandboxing / seccomp / unprivileged-user isolation — still Phase-1 "container
  is the boundary." The env model reduces secret exposure but is not a sandbox.
- Wiring a `preflight` node into `pr-review` — a separate follow-on that consumes
  this work (noted at the end).
- A per-node secrets mechanism for `llm_call` nodes.

## Architecture

### CommandRunner extraction (`adapter/process`)

Introduce one shared type that owns the process lifecycle both executors
currently duplicate (build env, start, wait, kill-on-timeout, bounded capture,
exit-code/stderr handling):

```go
// runner.go (adapter/process)
type commandSpec struct {
    Name        string            // interpreter or command
    Args        []string
    Dir         string            // cwd ("" = inherit)
    Stdin       []byte            // nil = no stdin
    Env         []string          // final "k=v" list (see Env Model)
    TimeoutSecs int               // 0 = no timeout
}

type commandResult struct {
    Stdout   string
    Stderr   string // bounded tail (1 MiB)
    ExitCode int
}

// run starts the process, waits with context/timeout, kills the process group
// on deadline, and returns a commandResult. A non-zero exit is NOT a run error;
// it is reported via commandResult.ExitCode. run errors only on execution
// faults (start failure, timeout, context cancel).
func run(ctx context.Context, spec commandSpec) (commandResult, error)
```

Both `ScriptExecutor.Execute` and `ToolCallExecutor.Execute` are refactored to:
build their `commandSpec` (script: interpreter+scriptPath+args, dir =
script dir, stdin = vars JSON; tool: command+args, no stdin), call `run`, then
apply the **existing** result mapping (non-zero exit → error with stderr tail;
success → parse stdout JSON into `Output`; always set `ScriptStdout/Stderr/
ExitCode`). This keeps the public `engine.NodeExecutor` contract and all current
tests green, while the lifecycle lives in one tested place.

This is adapter-internal; no new domain port is required (the executors are
already adapters implementing `engine.NodeExecutor`). Domain purity is preserved.

### Shared env allowlist (`adapter/procenv`)

The base allowlist + PATH-augmentation logic currently lives in
`adapter/claudecli/claude.go` (`envAllowlist`, `augmentedEnv`, `standardBinDirs`).
Extract these into a new small package `adapter/procenv` that both
`adapter/claudecli` and `adapter/process` import, so there is a single source of
truth. `adapter/claudecli` is refactored to consume it; its behavior is unchanged
and its existing tests continue to pass.

## Env Model (three layers)

`adapter/process` builds the final `Env` for a script/tool subprocess as the
union of:

1. **Base allowlist (always), read from the engine process env**, via
   `adapter/procenv`: `PATH, HOME, USER, LOGNAME, SHELL, LANG, LC_*, TERM,
   TMPDIR` (PATH augmented with standard bin dirs). Ensures `bash`, `gh`, `git`,
   `go`, `curl` function.

2. **Operator secret pass-through, from config, by name:** new setting
   `script.envPassthrough` (env: `ARBITRON_SCRIPT_ENVPASSTHROUGH`,
   comma-separated). Lists additional engine-env variable **names** a process
   node may inherit — e.g. `LLM_API_KEY,LLM_ROUTER_URL,OPENAI_API_KEY`. Only the
   named vars are copied from the engine env; everything else is stripped.
   Secret *values* live in the deploy/container environment, never in the bundle.
   A passthrough name that is **unset** in the engine env is simply absent (no
   empty `NAME=` injected).

3. **Per-node `env` map, from the bundle, literal:** new optional
   `node.Config.Env` (`map[string]string`, JSON `env`), values run through
   `variable.Resolve` for `{{var}}` substitution. For **non-secret** config the
   author wants visible in the workflow (e.g. `LLM_MODEL={{model}}`).

**Collision / spoofing rule (validation):** a per-node `env` key MUST NOT match
any base-allowlist name nor any name in `script.envPassthrough`. This stops a
committed bundle from shadowing or spoofing a secret/base variable. Violation →
validation error `INVALID_NODE_CONFIG`.

**Precedence:** base, then pass-through, then per-node. Because of the collision
rule the three sets are disjoint, so per-node never actually overwrites a secret.
The model is applied to both `script` and `tool_call` nodes (both run external
processes; both should be hardened).

## Config additions

`domain/config/config.go` gains a `ScriptConfig` sibling of `LLMConfig`:

```go
type ScriptConfig struct {
    // EnvPassthrough lists engine-env variable NAMES (not values) that script
    // and tool_call nodes may inherit, in addition to the base allowlist.
    // Secrets stay in the deploy environment; only names live here.
    EnvPassthrough []string
}
```

Wired through `adapter/config/loader.go` next to the `llm.*` keys: default
`script.envpassthrough = []string{}`, overridable via
`ARBITRON_SCRIPT_ENVPASSTHROUGH`. Documented as security-relevant (parallel to
the existing `llm.skippermissions` warning comments). The resolved list is
injected into the `adapter/process` executors at construction in `main.go`.

`NewScriptExecutor` / `NewToolCallExecutor` each gain a parameter for the
resolved passthrough name list. The base allowlist and the passthrough values
are read from the live process environment (`os.Environ()`) inside the adapter
at execution time — no engine-env handle needs to be threaded through.

## Node config additions

`domain/workflow/workflow.go` `NodeConfig` gains:

```go
Env map[string]string `json:"env,omitempty"` // script/tool_call: non-secret env
```

`ScriptFile`, `Interpreter`, `Command`, `Args`, `TimeoutSeconds` already exist.
`validateNodeConfigs` gains the per-node `env` collision check for `script` and
`tool_call` nodes.

## Security considerations

- Scripts/commands remain **RCE-equivalent** (Phase-1 trusted authors, container
  is the boundary). This change does **not** add a sandbox; the in-file
  `SECURITY:` notes stay accurate and are updated to mention the env model.
- The env model removes the wholesale secret leak: a subprocess sees only the
  base allowlist + explicitly named pass-through vars, mirroring the principle
  PR #8's review established for claude-cli (no blanket `os.Environ()`).
- The collision rule prevents a committed bundle from spoofing a secret/base
  var name.
- Output bound (1 MiB) and process-group kill-on-timeout are retained.

## Testing (strict TDD — five categories)

New/updated tests live in `adapter/process/*_test.go`, `adapter/procenv`, the
config loader, and workflow validation. Existing 15 process tests must stay
green through the refactor.

- **Happy path:** script exit 0 with base env present; existing behavior
  (stdin-JSON, stdout-JSON, `Output`) unchanged.
- **Success scenarios:** a named pass-through var present in the subprocess env;
  a per-node `env` value present and `{{var}}`-substituted; base vars (PATH/HOME)
  present.
- **Failure scenarios:** non-zero exit still → node error with stderr tail and
  exit code (regression-guard the existing behavior through the refactor).
- **Error scenarios:** scriptFile unresolvable; start failure; timeout kill;
  per-node `env` colliding with a base/passthrough name → validation error.
- **Edge cases:** an engine var **not** on base/passthrough is **absent** from
  the subprocess env (the core leak-prevention assertion); a passthrough name
  unset in the engine env is absent (no empty injection); empty `envPassthrough`;
  empty per-node `env`; `tool_call` gets the same env treatment as `script`.

The CommandRunner extraction is covered by exercising both executors through
their existing tests plus the new env assertions; the env-construction helper in
`adapter/procenv` gets focused unit tests.

## Consumer (follow-on, not in this spec)

Add a `preflight` script node as the `pr-review` entrypoint that checks
`bash --version`, `gh auth status`, the repo clone, and `gh pr view
{{pr_number}}`, exiting non-zero on any failure so a broken environment fails
fast and red. Note: because the script contract requires JSON stdout on success,
the preflight ends by emitting a small JSON object (e.g. `echo '{"ok":true}'`).
