# Phase 1, Task 2: claude-cli `ChatAdapter` + `AgentAdapter`

## Goal

Refactor the single claude-cli `Provider` into two thin adapters over the shared `CommandRunner` + stream-json machinery: a **tool-suppressed** `ChatAdapter` (implements `engine.ChatProvider`) and a **tools-enabled** `AgentAdapter` (implements `engine.AgentProvider`). Add a `SuppressTools` vs. `SkipPermissions` distinction to `argsPolicy`/`buildArgs` so the two adapters thread the correct `claude` flags.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §2.

## Dependencies

- **Task 1** (Port split): `engine.ChatProvider`, `engine.AgentProvider`, `ChatRequest`/`ChatResponse`/`ChatChunk`, `AgentMission`/`AgentResult`/`AgentStep` must already exist, and the claude-cli `Provider` must already expose `Chat`/`StreamChat`.

## Background (current state, post-Task-1)

- `adapter/claudecli/provider.go` — `Provider` with `Chat`/`StreamChat`, `spec(req engine.ChatRequest)`, `validateWorkDir`, `withTimeout`, and `Config` (BinaryPath, DefaultModel, WorkspaceRoot, Timeout, AllowedTools, SkipPermissions, MaxResponseBytes, Runner).
- `adapter/claudecli/claude.go` — `argsPolicy{ DefaultModel, SkipPermissions, AllowedTools }` and `buildArgs(req engine.ChatRequest, policy argsPolicy)`. Today `buildArgs` always emits `--print --verbose --output-format stream-json`, emits `--dangerously-skip-permissions` when `SkipPermissions`, emits `--allowedTools <csv>` only when `len(AllowedTools) > 0`, then `--model`, `--append-system-prompt`, `--resume`.
- `adapter/claudecli/runner.go` — `CommandRunner.Run(ctx, RunSpec) (lines, wait, err)`; the existing canned-stream-json fake (`fake_runner_test.go`) is the seam for both adapters.
- `adapter/claudecli/streamjson.go` — `parseEvent`, `streamEvent` (`eventSystem`/`eventAssistant`/`eventUser`/`eventResult`, with `SessionID`, `Text`, `IsError`, `TokensIn`, `TokensOut`).
- `adapter/procenv/procenv.go` — `BaseEnv()` (allowlist, PATH-augmented). Unchanged this phase; the Claude subscription inherits the ambient login, no key injected.

## Design constraints

- Tool-suppressed chat: emit `--allowedTools ""` (empty set) and **omit** `--dangerously-skip-permissions`. With no tools, `claude -p` behaves as a plain completion.
- Tools-enabled agent: emit `--dangerously-skip-permissions` **and** `--allowedTools <ToolScope csv>`; `WorkDir = {{workspace}}`; PATH-augmented env (`procenv.BaseEnv()`) so the agent finds `git`/`gh`.
- Both adapters reuse `CommandRunner`, `parseEvent`, the streaming state machine, and `validateWorkDir`.

## Files to Create/Modify

### Modify: `adapter/claudecli/claude.go`

- Extend `argsPolicy` with a `SuppressTools` knob:

  ```go
  type argsPolicy struct {
      DefaultModel    string
      SkipPermissions bool   // agent: emits --dangerously-skip-permissions
      SuppressTools   bool   // chat: emits --allowedTools "" (empty set)
      AllowedTools    []string
  }
  ```

- Update `buildArgs` flag logic (keep protocol flags `--print --verbose --output-format stream-json` always first):
  - If `policy.SkipPermissions`: append `--dangerously-skip-permissions`.
  - Tool-scope precedence:
    - If `policy.SuppressTools` is true: append `--allowedTools` with an **empty-string** value (`--allowedTools ""`) and ignore `AllowedTools`. (SuppressTools and SkipPermissions are mutually exclusive in practice; SuppressTools wins on the allowed-tools arg.)
    - Else if `len(policy.AllowedTools) > 0`: append `--allowedTools <comma-joined>` (current behavior).
    - Else: emit no `--allowedTools` flag (current behavior).
  - `--model` / `--append-system-prompt` / `--resume` unchanged.
- Keep the SECURITY doc comment on `buildArgs`; add a sentence noting `SuppressTools` produces a no-tools chat invocation.

### Modify: `adapter/claudecli/provider.go` → split into two adapters

Refactor `Provider` into two adapter structs that share the same construction inputs. Keep the existing `Config` struct as the shared config source. Prefer adding `ChatAdapter` and `AgentAdapter` types in this file (or split into `chat_adapter.go` / `agent_adapter.go` — implementer's choice; keep `runner.go`/`streamjson.go`/`streamstate.go` shared and untouched except for type names already handled in Task 1).

- **`ChatAdapter`** (implements `engine.ChatProvider`):

  ```go
  var _ engine.ChatProvider = (*ChatAdapter)(nil)

  func NewChatAdapter(cfg Config) *ChatAdapter
  func (a *ChatAdapter) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)
  func (a *ChatAdapter) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)
  ```

  - `spec(req)` builds the `RunSpec` with `buildArgs(req, argsPolicy{ DefaultModel: cfg.DefaultModel, SuppressTools: true })` — **never** `SkipPermissions`, **never** `AllowedTools`.
  - Reuses `validateWorkDir`, `withTimeout`, the result-event parse (`Chat`) and the streaming state machine (`StreamChat`) exactly as today's `Provider.Chat`/`StreamChat`.

- **`AgentAdapter`** (implements `engine.AgentProvider`):

  ```go
  var _ engine.AgentProvider = (*AgentAdapter)(nil)

  func NewAgentAdapter(cfg Config) *AgentAdapter
  func (a *AgentAdapter) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error)
  ```

  - Maps `AgentMission` → `RunSpec`: `buildArgs` with `argsPolicy{ DefaultModel: cfg.DefaultModel, SkipPermissions: true, AllowedTools: mission.ToolScope }`. When `mission.ToolScope` is empty, fall back to `cfg.AllowedTools` (the configured full agent scope).
  - Build a `ChatRequest`-shaped arg input from the mission for `buildArgs` (it takes `engine.ChatRequest`): set `Model: mission.Model`, `SessionID: mission.SessionID`. (Prompt is threaded via `RunSpec.Stdin = mission.Prompt`, matching today's `spec`.) If keeping `buildArgs` on `ChatRequest` is awkward, the implementer may widen `buildArgs` to accept a small internal `argInput` struct with `{Model, SystemPrompt, SessionID}` and adapt both callers — but do NOT change the chat path's emitted flags.
  - `Dir = mission.WorkDir`; `Env = procenv.BaseEnv()`; `MaxResponseBytes = cfg.MaxResponseBytes`.
  - Validate `mission.WorkDir` via the shared `validateWorkDir` (non-empty, under `WorkspaceRoot`).
  - Apply `mission.Timeout` (per-mission) via `withTimeout`-style derivation; if zero, fall back to `cfg.Timeout`.
  - `RunAgent` runs to completion (non-streaming). Drain the runner lines; for each parsed event:
    - `eventAssistant` → append `AgentStep{Kind: "assistant", Summary: <text>}` (and/or `"edit"`/`"tool_call"` as identifiable; for MVP, fold assistant text and user/tool-result events into steps with their `Kind`).
    - `eventUser` → append `AgentStep{Kind: "tool_call"}` (tool-result turns).
    - `eventResult` → capture `Summary = ev.Text`, `TokensIn`/`TokensOut`, `SessionID`; honor `ev.IsError` → return error like today's `Chat`.
  - After draining, call `wait()`; non-nil → error. Missing result event → error. Return `&engine.AgentResult{Summary, Steps, Model: mission.Model, TokensIn, TokensOut, SessionID}`.

- Remove the old combined `Provider` once both adapters exist (or keep it as a thin deprecated shim only if other code still needs it before Task 6 — prefer removing and letting Task 6 wire the new adapters; if removal breaks `main.go`, leave a minimal `Provider` alias until Task 6, but the production path should target the two adapters).

### Modify: `adapter/claudecli/claude_test.go` (and add adapter tests)

- Update the existing `buildArgs` table tests and add cases for `SuppressTools: true` (asserts `--allowedTools` present with empty value, no `--dangerously-skip-permissions`) and the agent combination (`SkipPermissions: true` + non-empty `AllowedTools`).

## Testing (strict TDD — five scenario categories)

Use the canned-stream-json `CommandRunner` fake; no real subprocess. Per the design §7.

### `ChatAdapter`
- **Happy path:** `Chat` with a canned `result` event returns a `ChatResponse` with `Output`, `Model`, token counts, `SessionID`; arg capture shows `--allowedTools ""` present and `--dangerously-skip-permissions` absent.
- **Success:** `StreamChat` emits assistant-text `ChatChunk`s then a terminal `Done` chunk carrying session id + token counts (reusing the streaming state machine).
- **Failure:** a `result` event with `is_error: true` → non-nil error from `Chat` (`claude reported an error`).
- **Error:** runner returns a start error / `wait()` returns a non-zero exit → `Chat`/`StreamChat` surface a non-nil error (chunk with `Error` for the stream path); binary-not-found path surfaces an error.
- **Edge case:** empty/blank stream-json lines and malformed JSON (parse error) handled; empty assistant output yields an empty-but-valid response; `WorkDir` empty → `ErrWorkDirRequired`; `WorkDir` outside root → `ErrWorkDirEscapesRoot`.

### `AgentAdapter`
- **Happy path:** `RunAgent` with a canned transcript (assistant + tool-result + result events) returns an `AgentResult` whose `Steps` reflect the folded events, with `Summary`, tokens, and `SessionID`; arg capture shows `--dangerously-skip-permissions` AND `--allowedTools <scope csv>`.
- **Success:** `mission.ToolScope` empty falls back to `cfg.AllowedTools`; `mission.SessionID` set → `--resume <id>` threaded; `mission.Timeout` applied (derived context cancels).
- **Failure:** `result` event `is_error: true` → non-nil error.
- **Error:** runner/`wait()` error or missing result event → non-nil error; `WorkDir` empty/outside-root → `ErrWorkDirRequired`/`ErrWorkDirEscapesRoot`.
- **Edge case:** transcript with only a `result` event (no intermediate steps) → `AgentResult` with empty `Steps` and a valid `Summary`; oversized output trips `MaxResponseBytes` → `ErrResponseTooLarge` surfaced via `wait()`.

### `buildArgs`
- **Happy/success/failure/error/edge** table cases covering: protocol flags always present; `SuppressTools` → `--allowedTools ""` + no skip-permissions; `SkipPermissions` + non-empty `AllowedTools` → both flags; neither set → no `--allowedTools`; model fallback to `DefaultModel`; `--resume` only when `SessionID` set; `SuppressTools` overriding any incidental `AllowedTools`.

Constraint: all tests run against the `CommandRunner` fake (hex seam). No domain import of the adapter; the adapters import only `domain/engine` (for the port types) + `adapter/procenv`.

## Acceptance Criteria

- `ChatAdapter` implements `engine.ChatProvider` and always invokes claude tool-suppressed (`--allowedTools ""`, no skip-permissions).
- `AgentAdapter` implements `engine.AgentProvider` and invokes claude with `--dangerously-skip-permissions` + `--allowedTools` from `ToolScope`.
- `argsPolicy.SuppressTools` exists and `buildArgs` threads it correctly; existing `SkipPermissions`/`AllowedTools` behavior preserved.
- Both adapters reuse `CommandRunner`, `parseEvent`, `validateWorkDir`, and the streaming state machine.
- `go test ./adapter/claudecli/...` passes; no domain package imports the adapter.
