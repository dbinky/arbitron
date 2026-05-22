# claude-cli LLM Provider — Design Spec

**Date:** 2026-05-21
**Status:** Design approved; pending implementation plan
**Scope:** Make `claude -p` arbitron's first working LLM provider.

## Motivation

arbitron's `LLMProvider` port has only a stub Anthropic adapter
(`adapter/anthropic/provider.go`) that returns *"full implementation not yet
available"*. No workflow with `llm_call` or `interactive` nodes can run.

Rather than implement the Anthropic Messages API (metered billing, expensive
without a subscription), the first real provider shells out to the `claude`
CLI in headless mode (`claude -p`), using the host's Claude subscription:

- **Cost** — subscription-based, not metered API.
- **Agentic capability** — `claude -p --dangerously-skip-permissions` is a full
  Claude Code agent: it reads/edits files and runs `git`/`gh`. arbitron
  `llm_call` nodes become agentic.
- **Interactivity** — `claude -p` supports multi-turn via `--resume <sessionID>`.

The pattern is proven in RalphFactory (`~/Repos/RalphFactory/internal/executor/`).

## Goals

- A `claude-cli` provider implementing `engine.LLMProvider` (`Call` + `StreamCall`).
- Engine-managed per-instance workspace directories so agents have a real cwd.
- Rework the interactive/conversation layer from Messages-API semantics to
  `claude -p` session resumption.
- Config + wiring to select the provider.

## Non-Goals

- Anthropic API / OpenRouter / Ollama adapters (later).
- Workspace retention/GC beyond delete-on-success (later).
- The pr-review workflow itself (separate, downstream of this spec).
- Sandboxing of agent processes — the existing Phase-1 trust model (workflow
  authors are trusted admins) is unchanged.

## Architecture — five parts

### Part 1 — `adapter/claudecli` package

A new package implementing `engine.LLMProvider`.

**Binary resolution.** `exec.LookPath("claude")`, then `~/.local/bin/claude`,
`/usr/local/bin/claude`, `/opt/homebrew/bin/claude`; bare `claude` as fallback.

**Invocation.**
- Args: `--print --verbose --output-format stream-json
  --dangerously-skip-permissions`; `--model <Model>` when set; `--resume
  <SessionID>` when set; `--append-system-prompt <SystemPrompt>` when set.
- Prompt delivered via **stdin**.
- `cmd.Dir = LLMRequest.WorkDir`.
- PATH augmented so the agent subprocess finds `git`/`gh` even under a minimal
  service PATH.
- Context-based timeout (configurable); kill the process group on timeout/cancel.

**`Call`** — runs to completion, parses the terminal `result` stream-json event
→ `LLMResponse{Output: <result text>, SessionID, Model, TokensIn, TokensOut}`.

**`StreamCall`** — returns `<-chan LLMStreamChunk`. Each `assistant` event → a
chunk with `Token` = assistant text. The terminal `result` event → a final
chunk with `Done: true`, token counts, `SessionID`. Stream/parse failures → a
chunk with `Error`.

**`CommandRunner` seam.** The adapter depends on a `CommandRunner` interface
(start a process with args/stdin/cwd/env, stream stdout lines, wait). The real
implementation wraps `os/exec`; tests inject a fake emitting canned stream-json.
Keeps the adapter unit-testable without a real `claude` binary.

**stream-json parser.** A pure function: input bytes/lines → parsed events.
Event types: `system` (init; carries `session_id`), `assistant` (message
content), `user` (tool results — ignored for output), `result` (terminal;
carries `session_id`, `result`, `is_error`, `usage`). Unit-tested in isolation.

### Part 2 — Port changes (`domain/engine/ports.go`)

| Type | New field | Purpose |
|---|---|---|
| `LLMRequest` | `WorkDir string` | subprocess working directory |
| `LLMRequest` | `SessionID string` | when non-empty → `--resume` |
| `LLMResponse` | `SessionID string` | session id of the new/resumed session |
| `LLMStreamChunk` | `SessionID string` | session id on the terminal chunk |

`MaxTokens`, `Temperature`, `Tools` stay on `LLMRequest` but the `claude-cli`
adapter ignores them (other adapters may use them). All additions are additive —
existing adapters/executors are unaffected.

### Part 3 — Engine-managed per-instance workspace

**Creation.** At the start of `Engine.run()` (`domain/engine/engine.go:209`),
the engine ensures `{config.Workspace.Path}/instances/{instanceID}/` exists.

**`WorkspaceManager` port** (new domain port):

```go
type WorkspaceManager interface {
    EnsureInstanceDir(instanceID uuid.UUID) (string, error)
    RemoveInstanceDir(instanceID uuid.UUID) error
}
```

A filesystem adapter implements it.

**Exposure to nodes.** The resolved path is seeded into the instance
`VariableStore` as a reserved variable `workspace`, so `{{workspace}}` resolves
everywhere: script nodes use it for checkouts; `LLMCallExecutor` and
`InteractiveExecutor` copy it into `LLMRequest.WorkDir`. Workflow validation
gains a rule rejecting a user-declared input named `workspace` (reserved-name
collision guard).

**Cleanup — delete on success.** On a successful terminal state the engine calls
`RemoveInstanceDir`. On failure/cancel the directory is **kept** for debugging.
Retention/GC of failed-run directories is a later concern.

### Part 4 — Interactive rework

Replace the Messages-API model (`complete_session` tool + full message-array
replay) with `claude -p` session resumption.

**First turn — `InteractiveExecutor.Execute`.**
- Build `LLMRequest{Prompt: orchestration+template, WorkDir, Model,
  SystemPrompt}` — no `SessionID`, no `Tools`.
- `StreamCall`; consume the stream; capture the returned `SessionID`.
- Persist `NodeExecution.LLMConversation = {messages: [...], sessionId: "<id>"}`.
  `messages` is retained for `interactive_get_history` display.
- Transition instance → `waiting` (unchanged).

**Subsequent turns — `ConversationHandler.HandleMessage`.**
- Load the stored `sessionId` from `NodeExecution.LLMConversation`.
- Build `LLMRequest{SessionID: <stored>, Prompt: <new user message only>,
  WorkDir}` — not the whole history.
- `StreamCall`; append the user message + assistant response to `messages` for
  history/display; refresh `sessionId` from the response.

**Completion signal.** No injectable tool. The agent emits a sentinel block
when the conversation concludes:

```
<arbitron:result>{ ...JSON matching outputSchema... }</arbitron:result>
```

The handler scans each assistant response for the sentinel; when found, it
parses the JSON as the node's structured output and completes the session
(`completeSession`, unchanged). The node's `outputSchema` is communicated to the
agent by injecting it into the orchestration prompt (a "when done, emit
`<arbitron:result>` matching this schema" instruction).

**User-initiated end.** `interactive_end_session` → `ConversationHandler.EndSession`
remains the fallback completion path (captures existing output or `{}`).

**Constant change.** `InteractiveCompletionToolName` / `...Description` are
removed and replaced by sentinel marker constants. The `complete_session`
tool-building code in `InteractiveExecutor` and `ConversationHandler` is removed.
`LLMRequest.Messages` becomes unused by the `claude-cli` path (kept on the port
for other adapters).

### Part 5 — Config & wiring

**Config (`domain/config` + `adapter/config` loader).**
- `LLM.Provider` gains the value `"claude-cli"`.
- New optional `LLM` fields: `BinaryPath` (auto-resolve if empty), `Model`
  (default model → `--model`), `TimeoutSeconds` (per-call timeout).
- No API key required for `claude-cli`.

**Wiring (`cmd/arbitron/main.go`).** When `config.LLM.Provider == "claude-cli"`,
construct `claudecli.New(...)` as the `engine.LLMProvider`. Construct the
`WorkspaceManager` and pass it to the engine.

## Testing strategy

Strict TDD; every unit covers all five scenario categories (happy path, success,
failure, error, edge case):

- **stream-json parser** — pure; exhaustive table tests: well-formed events,
  multi-event streams, `is_error` results, malformed lines, empty output,
  oversized lines.
- **claude-cli adapter** — `CommandRunner` fake with canned stream-json: happy
  path, non-zero exit, binary-not-found, timeout/context-cancel, malformed
  output, `--resume`/`--model` arg threading.
- **WorkspaceManager** — create, idempotent re-create, remove, remove-missing.
- **engine workspace integration** — dir created on run start, removed on
  success, kept on failure; `{{workspace}}` seeded into the variable store.
- **interactive rework** — first turn captures session id; `HandleMessage`
  resumes via session id; sentinel detection → completion; no-sentinel → normal
  turn; `EndSession` fallback.

## Sequencing

A multi-step feature. Suggested implementation order (each built test-first):

1. Part 2 — port fields (additive, unblocks the rest).
2. Part 1 — `claude-cli` adapter + stream-json parser.
3. Part 3 — `WorkspaceManager` + engine integration.
4. Part 4 — interactive/conversation rework.
5. Part 5 — config + wiring.

## Open questions

None — all resolved during brainstorming.
