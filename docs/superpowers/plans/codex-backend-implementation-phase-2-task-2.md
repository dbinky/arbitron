# Phase 2 / Task 2 — Generalize `InteractiveStreamSelector` to dispatch by backend

**Design:** `docs/superpowers/specs/codex-backend-design-phase-2.md`
**Depends on:** Phase 1 Task 1 (`BackendCodex`), Phase 2 Task 1 (codex
`StreamProvider`).

## Goal
Replace the selector's single hardcoded claude stream adapter (dispatched by
provider, claude-only) with backend-axis dispatch, so agent-kind interactive
bindings route `claude-code → claude stream`, `codex → codex stream`, and
`opencode → unsupported` (fail-closed), using the same vocabulary + default
floor as the batch path.

## Files
- `adapter/provider/interactive_stream_selector.go` (edit) +
  `..._test.go` (edit)
- `cmd/arbitron/main.go` (edit — construct codex stream adapter; new
  constructor signature)

## TDD steps
1. **RED** — new selector tests:
   - agent-kind `Backend: "claude-code"` → claude stream adapter.
   - agent-kind `Backend: "codex"` → codex stream adapter.
   - agent-kind `Backend: "opencode"` → `ErrUnsupportedAgentBackend`
     ("opencode does not support interactive streaming").
   - agent-kind unknown/`"Codex"` → `ErrUnsupportedAgentBackend`.
   - agent-kind empty `Backend` → floored to the default-agent-backend, then
     dispatched (assert it uses the injected floor).
   - chat-kind → still delegates to the `ChatProviderSelector` (unchanged).
   - `errors.Is(err, ErrUnsupportedAgentBackend)` holds (wrapped sentinel).
2. **GREEN:**
   - rename sentinel `ErrUnsupportedAgentProvider` →
     `ErrUnsupportedAgentBackend` (keep `%w` wrapping).
   - change the struct to hold `agentStreamsByBackend map[string]engine.ChatProvider`
     (or a small typed holder) plus `defaultBackend string`.
   - dispatch agent-kind by `b.Backend` with the empty→default floor; unknown
     and `opencode` → the unsupported error.
   - constructor: `NewInteractiveStreamSelector(chat ChatProviderSelector,
     agentStreams map[string]engine.ChatProvider, defaultBackend string)`.
3. **composition (GREEN):** in `main.go`, build the codex `StreamProvider`
   alongside `claudecli.NewStreamAgentAdapter`, assemble the
   `{ "claude-code": claudeStream, "codex": codexStream }` map, and pass the
   configured default-agent-backend floor.

## Acceptance
- `go test ./adapter/provider/...` passes.
- `make build` succeeds.
- An `interactive_agent` node with a `backend: codex` profile selects the codex
  stream adapter; an `opencode` interactive node fails closed with a clear
  message; `claude-code` interactive nodes are unaffected (regression-safe).
