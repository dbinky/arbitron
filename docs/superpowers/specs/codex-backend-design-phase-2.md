# Codex Backend — Design Phase 2: `interactive_agent` (streaming)

**Spec:** `docs/specs/codex-backend-spec.md`
**Phase goal:** Make `interactive_agent` (streaming) nodes work on the `codex`
backend, so live-streaming agent nodes (e.g. feature-pipeline's `interviewer`)
can run on the ChatGPT subscription. Depends on Phase 1.

## Context — the constraint this phase removes

Interactive agent streaming is currently **claude-only**. Two facts from the
codebase:

1. `adapter/provider/interactive_stream_selector.go` holds a SINGLE injected
   `agentStream engine.ChatProvider` (the claudecli stream adapter) and, for an
   agent-kind binding, returns it only when `b.Provider == "claude-cli"` (or
   empty), else `ErrUnsupportedAgentProvider`. Dispatch is by **provider**, and
   there is exactly one agent stream backend.
2. `adapter/opencode` has **no** streaming adapter at all — only the batch
   `Provider`. So no non-Claude backend can stream today.

This phase adds the first non-Claude streaming agent adapter (codex) and
generalizes the selector to dispatch agent-kind by **backend**, consistent with
`AgentProviderFor`.

## Components

### 1. `adapter/codex/stream_adapter.go` (+ streaming parse)
- `StreamProvider` implements `engine.ChatProvider` (`Chat` + `StreamChat`),
  parallel to `claudecli.StreamAgentAdapter`. It drives `codex exec` in
  streaming-JSON mode and surfaces incremental assistant text as
  `engine.ChatChunk`s on the channel returned by `StreamChat`.
- Reuses the same `args.go` argv builder as Phase 1 (codex `exec`, `--yolo`,
  `-m <model>`, JSON streaming, prompt via stdin) and the same `env.go`
  subscription env (no key). The streaming parser is a thin incremental layer
  over the Phase-1 event vocabulary (mirrors how claudecli shares
  `streamjson.go`/`streamstate.go` between its batch and stream adapters);
  factor the shared event decoding so batch `parse.go` and the stream path do
  not diverge.
- `ToolScope` precedence matches the existing agents: a non-empty
  `ChatRequest.ToolScope` (the resolved profile's `AllowedTools`) overrides the
  adapter's global scope. Under `--yolo` this narrows tools; absent a scope the
  posture is fully open (the documented v1 posture).

### 2. Generalize `InteractiveStreamSelector`
(`adapter/provider/interactive_stream_selector.go`)
- Replace the single `agentStream` field with backend-keyed dispatch for
  agent-kind bindings, using the SAME backend vocabulary and the SAME
  default-backend floor as the batch path:
  - `claude-code` → the claudecli stream adapter,
  - `codex` → the codex stream adapter,
  - `opencode` → **unsupported** for streaming: a clear, fail-closed error
    (it has no stream adapter; explicitly out of scope),
  - unknown/empty-after-floor → fail-closed error.
- Chat-kind bindings are unchanged (delegate to the `ChatProviderSelector`).
- Rename/repurpose `ErrUnsupportedAgentProvider` →
  `ErrUnsupportedAgentBackend` (backend-axis dispatch), keeping a wrapped
  sentinel for `errors.Is`.
- Constructor signature changes from `(chat, agentStream)` to
  `(chat, agentStreamsByBackend)` (a small struct/map of backend → stream
  `ChatProvider`, plus the default-backend floor). Because the selector lives in
  the adapter layer it may import both `claudecli` and `codex` — domain/engine
  stays adapter-free.

### 3. Composition root (`cmd/arbitron/main.go`)
- Construct the codex stream adapter alongside the existing
  `claudecli.NewStreamAgentAdapter`.
- Pass both into `NewInteractiveStreamSelector` keyed by backend, with the
  configured default-agent-backend floor.

## Data flow

`InteractiveAgentExecutor` → `InteractiveStreamSelector.StreamProviderFor(binding)`
→ (agent-kind, backend `codex`) codex `StreamProvider` → `StreamChat` →
`CommandRunner.Run` → `codex exec` streaming JSON → incremental
`engine.ChatChunk`s relayed to the interactive session → terminal result folded
into the node's `outputSchema` JSON.

## Error handling (all fail-closed)
- Unknown/typo backend on an agent-kind binding → `ErrUnsupportedAgentBackend`.
- `opencode` on an interactive node → explicit "opencode does not support
  interactive streaming" error (not a silent fallback to claude).
- Binary/login/parse failures surface the same typed errors as Phase 1.

## Testing (strict TDD, 5 scenario categories)
- **happy path:** codex `StreamChat` over a fake runner emitting incremental
  JSON events → ordered `ChatChunk`s + terminal result.
- **input validation:** empty WorkDir/model rejected.
- **error handling:** mid-stream runner error / malformed event → stream closes
  with a typed error, no partial-success masquerade.
- **selector dispatch:** agent-kind `claude-code` → claude stream; `codex` →
  codex stream; `opencode` → unsupported error; unknown → unsupported error;
  chat-kind still delegates to the chat selector. Cover `errors.Is` on the
  sentinel.
- **security:** stream env carries no `OPENAI_API_KEY`; `HOME` preserved.
- **acceptance:** an `interactive_agent` node bound to a codex profile streams
  to completion and emits its `outputSchema` result via the fake runner.

## Out of scope
Opencode interactive streaming (a separate future effort). Readiness probe and
sample config land in Phase 3.
