# Phase 1, Task 1: Port Split — `ChatProvider` + `AgentProvider`

## Goal

Split the single `engine.LLMProvider` port into two capability-scoped ports: a renamed `ChatProvider` (reasoning/chat, no side effects) and a new `AgentProvider` (agentic file editing). Rename the request/response/chunk types to the `Chat*` family and add the `Agent*` mission/result/step types. This is an additive/rename change that unblocks every later task; all existing references must compile against the renamed symbols.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §1.

## Dependencies

None. This is the first task in the sequence; every other task in this phase depends on it.

## Background (current state)

- `domain/engine/ports.go` defines `LLMProvider` (`Call` + `StreamCall`) plus `LLMRequest`, `LLMResponse`, `LLMStreamChunk`.
- Implementations / references that MUST be updated by the rename:
  - `adapter/claudecli/provider.go` — `var _ engine.LLMProvider = (*Provider)(nil)`, `Call`/`StreamCall`, `spec(req engine.LLMRequest)`.
  - `adapter/claudecli/claude.go` — `buildArgs(req engine.LLMRequest, ...)`.
  - `adapter/claudecli/streamstate.go` — produces `engine.LLMStreamChunk` values.
  - `adapter/anthropic/provider.go` — stub implementing `engine.LLMProvider`.
  - `domain/engine/executor_llm.go` — `LLMCallExecutor` holds `llmProvider LLMProvider`, builds `LLMRequest`, calls `.Call`, `processLLMOutput(resp *LLMResponse, ...)`.
  - `domain/engine/executor_interactive.go` — holds `llmProvider LLMProvider`, builds `LLMRequest`, calls `.StreamCall`.
  - `domain/engine/conversation.go` — holds `llmProvider LLMProvider`, builds `LLMRequest`, calls `.StreamCall`.
  - `cmd/arbitron/main.go` — `var llmProvider engine.LLMProvider`.
- Test files referencing these types (e.g. `adapter/claudecli/*_test.go`, `domain/engine/*_test.go`) must also be updated.

**Note on `interactive`:** the design (§3) leaves the `interactive` node behavior untouched, but it still depends on the streaming port. Because `ChatProvider` carries both `Chat` and `StreamChat`, `interactive`/`conversation` are simply rebound onto `ChatProvider.StreamChat` + `ChatRequest` as part of this rename. No behavior change.

## Files to Create/Modify

### Modify: `domain/engine/ports.go`

- Rename interface `LLMProvider` → `ChatProvider`; rename method `Call` → `Chat`, `StreamCall` → `StreamChat`. The two methods now take/return the `Chat*` types:

  ```go
  // ChatProvider performs reasoning/chat with no tool use and no file side
  // effects. Backends: tool-suppressed claude -p (P1); Azure AI Foundry,
  // OpenRouter (P3).
  type ChatProvider interface {
      Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
      StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)
  }
  ```

- Rename `LLMRequest` → `ChatRequest`, `LLMResponse` → `ChatResponse`, `LLMStreamChunk` → `ChatChunk`. Keep existing fields (`Model`, `SystemPrompt`, `Prompt`, `MaxTokens`, `Temperature`, `OutputSchema`, `WorkDir`, `SessionID` on the request; `Output`, `Conversation`, `Model`, `TokensIn`, `TokensOut`, `SessionID` on the response; `Token`, `Done`, `Error`, `TokensIn`, `TokensOut`, `SessionID` on the chunk). Retain the existing `WorkDir`/`SessionID` doc comments — `WorkDir` is still required by claude -p even tool-suppressed, and `SessionID` is retained for the deferred interactive backend.
  - Keep `Conversation any` on `ChatResponse` for now — `LLMCallExecutor` reads `resp.Conversation`. Do not remove it in this task.

- Add the new `AgentProvider` port and its types in the same file:

  ```go
  // AgentProvider runs an agentic mission that mutates files inside the
  // instance workspace. The agentic loop (tool calls, edits, test runs)
  // happens INSIDE the harness, not inside Arbitron. Backends: claude -p with
  // tools (P1); opencode (P3).
  type AgentProvider interface {
      RunAgent(ctx context.Context, mission AgentMission) (*AgentResult, error)
  }

  type AgentMission struct {
      Model     string        // resolved model-id (or provider/model for opencode)
      Prompt    string        // the mission instruction
      WorkDir   string        // = the instance {{workspace}}
      ToolScope []string      // allowed-tools / permission scope
      SessionID string        // optional cross-step resume; see opencode spike
      Timeout   time.Duration
  }

  type AgentResult struct {
      Summary   string      // result/summary text
      Steps     []AgentStep // step events (tool calls, edits) for the trace
      Model     string
      TokensIn  int         // coarse; subprocess accounting
      TokensOut int
      SessionID string
  }

  type AgentStep struct {
      Kind    string // "tool_call" | "edit" | "assistant" | "result"
      Summary string
  }
  ```

  `time` is already imported in `ports.go`. Both ports remain driven ports in `domain/engine`; no domain type imports an adapter.

### Modify: `adapter/claudecli/provider.go`

- Change the interface assertion to `var _ engine.ChatProvider = (*Provider)(nil)`.
- Rename `Call` → `Chat`, `StreamCall` → `StreamChat` on `*Provider`; change their parameter/return types `engine.LLMRequest` → `engine.ChatRequest`, `engine.LLMResponse` → `engine.ChatResponse`, `engine.LLMStreamChunk` → `engine.ChatChunk`.
- Update `spec(req engine.LLMRequest)` → `spec(req engine.ChatRequest)`.
- No logic change. (Task 2 then refactors this Provider into the `ChatAdapter`/`AgentAdapter` pair; this task only renames so the codebase compiles.)

### Modify: `adapter/claudecli/claude.go`

- Update `buildArgs(req engine.LLMRequest, policy argsPolicy)` → `buildArgs(req engine.ChatRequest, policy argsPolicy)`. No flag logic change in this task (Task 2 adds `SuppressTools`).

### Modify: `adapter/claudecli/streamstate.go`

- Update any `engine.LLMStreamChunk` references → `engine.ChatChunk` (the streaming state machine that `StreamChat` drives).

### Modify: `adapter/anthropic/provider.go`

- Change `var _ engine.LLMProvider = (*Provider)(nil)` → `var _ engine.ChatProvider = (*Provider)(nil)`.
- Rename methods `Call` → `Chat`, `StreamCall` → `StreamChat`; swap `engine.LLMRequest`/`LLMResponse`/`LLMStreamChunk` → `engine.ChatRequest`/`ChatResponse`/`ChatChunk`. Keep the stub `ErrNotConfigured` behavior.

### Modify: `domain/engine/executor_llm.go`

- `LLMCallExecutor.llmProvider` field type `LLMProvider` → `ChatProvider`. `NewLLMCallExecutor(fr FileReader, provider LLMProvider)` → `provider ChatProvider`.
- `req := LLMRequest{...}` → `req := ChatRequest{...}`; `e.llmProvider.Call(ctx, req)` → `e.llmProvider.Chat(ctx, req)`.
- `processLLMOutput(resp *LLMResponse, ...)` → `processLLMOutput(resp *ChatResponse, ...)`.
- No behavior change in this task. (Task 4 rebinds the model field to a profile alias; leave `node.Config.Model` as-is here.)

### Modify: `domain/engine/executor_interactive.go`

- `llmProvider LLMProvider` field and constructor param → `ChatProvider`.
- `req := LLMRequest{...}` → `ChatRequest{...}`; `e.llmProvider.StreamCall(ctx, req)` → `e.llmProvider.StreamChat(ctx, req)`. No behavior change.

### Modify: `domain/engine/conversation.go`

- `llmProvider LLMProvider` field and constructor param → `ChatProvider`.
- `req LLMRequest` → `req ChatRequest`; `h.llmProvider.StreamCall(...)` → `h.llmProvider.StreamChat(...)`. No behavior change.

### Modify: `cmd/arbitron/main.go`

- `var llmProvider engine.LLMProvider` → `var llmProvider engine.ChatProvider`. (Task 6 reworks this wiring fully; this task only keeps it compiling.)

### Modify: existing tests

- Update `adapter/claudecli/provider_test.go`, `claude_test.go`, `streamstate_test.go`, `adapter/anthropic/*_test.go`, and any `domain/engine/*_test.go` that name `LLMProvider`/`LLMRequest`/`LLMResponse`/`LLMStreamChunk` or call `.Call`/`.StreamCall`, renaming to the new symbols/methods. These are mechanical renames; assertions are unchanged.

## Testing (strict TDD — five scenario categories)

The new `AgentProvider` port has no implementation in this task (claude-cli `AgentAdapter` lands in Task 2), so the testable unit here is the **type/interface surface** plus the preserved behavior of the renamed `ChatProvider` path. Write/adjust tests first, then make the rename.

- **Happy path:** an in-package fake implementing `engine.ChatProvider` is accepted where the old fake `LLMProvider` was; `LLMCallExecutor.Execute` returns the structured output for a normal `Chat` response. A compile-time assertion `var _ AgentProvider = (*fakeAgent)(nil)` in the engine test package confirms a fake can satisfy the new port.
- **Success:** `ChatRequest` round-trips all fields (Model, SystemPrompt, Prompt, MaxTokens, Temperature, OutputSchema, WorkDir, SessionID) into the fake and back; `ChatResponse`/`ChatChunk` fields are populated and read unchanged by the executor (verify token counts + model propagate).
- **Failure:** `LLMCallExecutor.Execute` wraps and returns a non-nil error when the fake `ChatProvider.Chat` returns a domain error (e.g. provider reported failure) — the `llm_call %q: provider call` wrap is preserved.
- **Error:** the renamed claude-cli `Chat` still surfaces a non-nil error for a runner error / missing result event (keep the existing claude-cli provider tests green under the new method names).
- **Edge case:** `AgentMission` with empty `ToolScope` and zero `Timeout`, and an `AgentResult` with a nil `Steps` slice, are valid zero-value-friendly shapes (a table test constructs them and asserts field defaults); `ChatChunk{Done: true}` terminal chunk with empty `Token` is handled by the existing stream consumer.

Constraint: tests live in the same package style as today (`adapter/claudecli`, `domain/engine`) and use fakes/seams only — no real subprocess, no adapter import from `domain/`.

## Acceptance Criteria

- `engine.ChatProvider` (with `Chat`/`StreamChat`) and `engine.AgentProvider` (with `RunAgent`) both exist in `domain/engine/ports.go`.
- `ChatRequest`/`ChatResponse`/`ChatChunk` replace the `LLM*` types; `AgentMission`/`AgentResult`/`AgentStep` are defined.
- claude-cli `Provider` and the anthropic stub satisfy `engine.ChatProvider`; `LLMCallExecutor`, `InteractiveExecutor`, and the conversation handler compile against the renamed port.
- `domain/` imports no adapter package.
- `go build ./...` and `go test ./...` pass.
