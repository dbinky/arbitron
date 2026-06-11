# Codex Backend — Design Phase 1: `agent_call` (batch)

**Spec:** `docs/specs/codex-backend-spec.md`
**Phase goal:** A new `codex` agent backend that runs `agent_call` (batch)
nodes on the user's ChatGPT subscription via `codex exec`, with no API key.

## Context

Arbitron dispatches agent nodes on the **backend axis** (`ResolvedBinding.Backend`),
orthogonal to the provider. `adapter/provider/factory.go::AgentProviderFor`
already branches `claude-code → claudecli`, `opencode → adapter/opencode`, and
fails closed on any other backend (`ErrUnknownBackend`). This phase adds a third
arm, `codex`, backed by a new `adapter/codex` package that mirrors the structure
of `adapter/opencode`.

The subprocess seam is the existing `claudecli.CommandRunner`
(`Run(ctx, RunSpec) (lines <-chan string, wait func() error, err error)`),
reused (not redefined) exactly as `adapter/opencode` reuses it, so codex
inherits one fake runner and one stdout-bounding/timeout contract.

## Components

### 1. Backend vocabulary (`domain/workflow/workflow.go`)
- Add `BackendCodex = "codex"`.
- Add `BackendCodex` to the `IsValidAgentBackend` switch.

This is the single source of truth shared by the workflow validator and the
adapter factory's backend dispatch. No other validation change is needed — the
validator already rejects unknown backends fail-closed at load.

### 2. `adapter/codex` package
Mirrors `adapter/opencode` structure, minus the per-provider credential table
(codex is single-provider, subscription-only).

- **`provider.go`** — `Provider` implements `engine.AgentProvider`. `New(Config)`
  resolves the binary and runner (nil → `claudecli.NewExecRunner()`).
  `RunAgent(ctx, mission)` follows the same strict fail-closed ordering as
  opencode:
  1. WorkDir guard (`ErrWorkDirRequired`) — an empty `cmd.Dir` would inherit the
     engine launch dir holding `arbitron.db`/`config.yaml`.
  2. Build args (fail-closed on empty model).
  3. Build env (subscription env; see `env.go`).
  4. Memoized version/compat probe (`version.go`).
  5. Run via the runner with a per-mission timeout (`mission.Timeout` else
     `cfg.Timeout`), bounded by `MaxResponseBytes`.
  6. Parse output → `*engine.AgentResult`.
- **`args.go`** — builds the `codex exec` argv. Flag spellings are pinned in ONE
  constants block (mirroring opencode's `args.go`) so a Codex-version
  re-verification is a one-line edit:
  - `exec` subcommand (non-interactive),
  - `--yolo` (bypass approvals + sandbox) — the v1 posture per spec,
  - `-m <model>` from `binding.Model` (fail-closed `ErrModelRequired` if empty),
  - working dir via `--cd <WorkDir>` (or process cwd = `RunSpec.Dir`),
  - machine-readable JSON output flag,
  - prompt delivered via stdin (`RunSpec.Stdin`), never argv (avoids
    argv/logging exposure of mission text).
  SECURITY: only model/prompt strings reach the subprocess; no secret is ever
  placed in argv.
- **`env.go`** — `codexEnv()` returns `procenv.BaseEnv()` with `HOME` (and
  `CODEX_HOME` if set) preserved so `codex` resolves its own
  `~/.codex/auth.json`. It deliberately sets **no** `OPENAI_API_KEY`. This is the
  defining difference from opencode's `openai` provider, which injects a key.
- **`parse.go`** — folds `codex exec` JSON events into `engine.AgentResult`
  (`Summary`, `Steps` [tool_call/edit/assistant/result], best-effort
  `TokensIn`/`TokensOut`). Tolerant of unknown event kinds (forward-compat).
- **`version.go`** — binary resolution (`""` → PATH/StandardBinDirs lookup) and
  a memoized (`sync.Once`) compat probe that fails closed on a missing or
  incompatible binary before any mission launches.

### 3. Factory arm (`adapter/provider/factory.go`)
- Add a `case workflow.BackendCodex:` arm in `AgentProviderFor` returning
  `codex.New(codex.Config{Binding: b, BinaryPath: ..., Runner: ..., Timeout:
  ..., MaxResponseBytes: ...})`.
- Extend `Deps` with `CodexBinaryPath`, `CodexTimeout`. No `AllowAll` knob —
  `--yolo` is the fixed v1 posture (documented in the spec's risk section).

### 4. Composition root (`cmd/arbitron/main.go`)
- Populate the new `Deps.Codex*` fields from config (binary path override,
  timeout) when constructing `providerFactory`.

## Data flow

`AgentCallExecutor` → `AgentProviderSelector.AgentProviderFor(binding)` →
(backend `codex`) `codex.Provider.RunAgent` → `CommandRunner.Run` →
`codex exec --yolo -m <model> ...` (stdin = prompt, cwd = instance workspace) →
JSON event stream → `parse.go` → `AgentResult` → trace/steps.

## Error handling (all fail-closed)
- Empty WorkDir → `ErrWorkDirRequired` (no launch).
- Empty model → `ErrModelRequired` (no launch).
- Missing/incompatible binary → version-probe error (no mission launch).
- Codex not logged in / nonzero exit → error naming the cause; never a silent
  empty result.

## Testing (strict TDD, 5 scenario categories)
- **happy path:** `RunAgent` over a fake runner emitting a canned JSON event
  stream → expected `AgentResult`.
- **input validation:** empty WorkDir, empty model rejected pre-launch.
- **error handling:** runner error / nonzero exit / unparseable output → typed
  errors; `parse.go` tolerates unknown event kinds.
- **security:** assert the built env contains **no** `OPENAI_API_KEY` and that
  `HOME` is preserved; assert prompt never appears in argv.
- **factory dispatch:** `AgentProviderFor` returns the codex provider for
  `backend: codex` and `ErrUnknownBackend` for typos/case variants.
- **acceptance:** extend `test/acceptance/scenario_03_both_agent_backends_test.go`
  (or a sibling) to a third backend running an `agent_call` mission on codex via
  the fake runner.

## Out of scope (later phases)
Interactive streaming (Phase 2); readiness probe + sample config + docs
(Phase 3).
