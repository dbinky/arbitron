# Phase 1 / Task 2 — `adapter/codex` batch agent adapter

**Design:** `docs/superpowers/specs/codex-backend-design-phase-1.md`
**Depends on:** Task 1 (`BackendCodex`) for cross-references; this task builds
the adapter package itself.

## Goal
A new `adapter/codex` package whose `Provider` implements
`engine.AgentProvider`, driving `codex exec` headlessly on the ChatGPT
subscription (no API key), mirroring `adapter/opencode` structure and reusing
the `claudecli.CommandRunner` subprocess seam.

## Files (new, under `adapter/codex/`)
- `args.go` + `args_test.go`
- `env.go` + `env_test.go`
- `parse.go` + `parse_test.go`
- `version.go` + `version_test.go`
- `provider.go` + `provider_test.go`
- reuse `adapter/opencode/fake_runner_test.go` pattern (the shared
  `claudecli.CommandRunner` fake) for subprocess tests.

## Contracts
- `Config{ Binding provider.ResolvedBinding; BinaryPath string; Runner
  claudecli.CommandRunner; Timeout time.Duration; MaxResponseBytes int64 }`.
- `New(Config) *Provider` — nil Runner → `claudecli.NewExecRunner()`; empty
  BinaryPath → resolve from PATH/StandardBinDirs.
- `(*Provider).RunAgent(ctx, engine.AgentMission) (*engine.AgentResult, error)`.

## Flag/constants block (`args.go`) — pin in ONE place
- `execSubcommand = "exec"`
- `yoloFlag = "--yolo"` (bypass approvals + sandbox; v1 posture)
- `modelFlag = "-m"`
- `cdFlag = "--cd"` (set working dir)
- JSON-output flag (e.g. `--json`) for machine-readable events.
> These spellings are pinned against a specific Codex CLI version at
> implementation time; verify with `codex exec --help` before finalizing and
> record the verified version in a comment (mirrors opencode `args.go`).

## TDD steps
1. **args (RED→GREEN):**
   - empty `binding.Model` → `ErrModelRequired`, no argv.
   - happy: argv = `[exec, --yolo, -m, <model>, --cd, <WorkDir>, --json]`;
     prompt is NOT in argv (delivered via `RunSpec.Stdin`).
   - optional per-request `ToolScope` handling deferred (yolo posture is
     fully-open by default; document that ToolScope narrowing is wired in
     Phase 2's shared path).
2. **env (RED→GREEN):**
   - returns `procenv.BaseEnv()` with `HOME` (and `CODEX_HOME` if present in the
     parent env) preserved.
   - asserts the result contains **no** `OPENAI_API_KEY` (security).
   - never calls `binding.RevealKey` (subscription path: no key reveal).
3. **parse (RED→GREEN):**
   - folds a canned JSONL event stream into `AgentResult` (Summary from the
     terminal result event; Steps for tool_call/edit/assistant; best-effort
     token counts).
   - tolerates unknown event kinds without error (forward-compat).
   - malformed JSON line → typed parse error.
4. **version (RED→GREEN):**
   - missing binary → resolve error.
   - memoized (`sync.Once`) compat probe; incompatible version → typed error
     before any mission run.
5. **provider (RED→GREEN):** strict fail-closed ordering via the fake runner:
   - empty WorkDir → `ErrWorkDirRequired` (zero launches).
   - empty model → `ErrModelRequired` (zero launches, not even version probe).
   - happy: WorkDir guard → args → env → version probe → run → parse →
     `AgentResult`.
   - runner error / nonzero exit → typed error naming codex + model.

## Acceptance
- `go test ./adapter/codex/...` passes across all 5 scenario categories
  (happy, input-validation, error-handling, security/no-key, forward-compat).
- No `OPENAI_API_KEY` ever appears in the built env; prompt never in argv.
