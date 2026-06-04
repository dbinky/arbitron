# Phase 4, Task 4: Interactive-Endpoint Ownership — gate `HandleMessage` / `EndSession` in the domain service

## Goal (Source of truth: design §2 "Interactive endpoints")

Apply the ownership-or-admin check **before mutating** an interactive session. These are the most sensitive surface called out in `server.go` ("without auth, any client can send messages to or terminate any instance's interactive session"). Because the WebSocket inbound router, the REST `interact/end` endpoint, and the MCP `interactive_*` tools all funnel into the `ConversationHandler` domain service, enforcing the check **inside** `HandleMessage` and `EndSession` makes all three surfaces inherit it — no interface can drift.

A non-owner non-admin gets `instance.ErrNotFound` on a read-shaped failure and the write is refused; the design's read/write distinction means the participate action (a write) is refused once the instance is loaded and found not-visible, surfaced as `ErrNotFound` so existence is not leaked through the interactive surface either.

## Dependencies

- Task 2 (`canView` in `domain/engine/authz.go`).
- Phase 1 identity in context. Critically, the WS inbound goroutine, the REST handler, and the MCP tool handler must pass the **request** context (carrying the identity) into `HandleMessage`/`EndSession`. They already pass `ctx`/`r.Context()` today (`stream_handler.go:233/235`, `interactive_handler.go:40`, `tools_interactive.go:56/74`), so no signature change is needed — only that the middleware has populated that context.

## Background (current state)

- `domain/engine/conversation.go` `conversationHandler` implements `HandleMessage(ctx, instanceID, content)` and `EndSession(ctx, instanceID)`. Both start by calling `loadAndValidate(ctx, instanceID)` (line 306), which loads the instance via `h.instances.Get` and verifies it is in interactive-wait state. **No ownership check today** — any caller with the UUID can drive the session.
- The three call sites:
  - WS: `stream_handler.go` `handleInteractiveInbound` (line 218) routes `interactive.send` → `HandleMessage`, `interactive.end` → `EndSession`. (Task 3 already gates *connecting* to the per-instance WS, but the inbound mutation must still be authorized at the service for defence-in-depth and for the MCP/REST paths.)
  - REST: `interactive_handler.go` `EndSession` (POST `/instances/{id}/interact/end`).
  - MCP: `tools_interactive.go` `interactiveSendMessageHandler` → `HandleMessage`; `interactiveEndSessionHandler` → `EndSession`; `interactiveGetHistoryHandler` reads `Instances.Get`/`NodeExecs.GetByInstanceAndNode` directly (a read — scope it like Task 2).
- `loadAndValidate` already returns `instance.ErrNotInteractiveWait` for the wrong-state case; mapping for that exists in all three adapters. Adding an ownership branch that returns `ErrNotFound` slots into the existing error mapping (404 in REST/WS, not-found tool error in MCP).

## Files to Create/Modify

### Modify: `domain/engine/conversation.go` — authorize inside `loadAndValidate`

`loadAndValidate` is the single chokepoint both mutating methods share. Add the ownership check immediately after the instance loads, before the state validation, so a non-viewer is rejected with `ErrNotFound` (existence not leaked) rather than `ErrNotInteractiveWait` (which would confirm the instance exists):

```go
func (h *conversationHandler) loadAndValidate(ctx context.Context, instanceID uuid.UUID) (*instance.Instance, string, workflow.NodeConfig, error) {
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, "", workflow.NodeConfig{}, fmt.Errorf("conversation: load instance: %w", err)
	}

	// Authorization: participating in (writing to) an interactive session is
	// allowed only for the instance owner or an admin. A non-viewer gets
	// ErrNotFound so the interactive surface does not leak existence.
	if !canView(ctx, inst.OwnerID) {
		return nil, "", workflow.NodeConfig{}, fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrNotFound)
	}

	if inst.State != instance.StateWaiting {
		// ... unchanged ...
	}
	// ... rest unchanged ...
}
```

Because `canView` is in `domain/engine` (Task 2) and `conversationHandler` lives in the same package, this is a direct call — no new dependency. Both `HandleMessage` and `EndSession` inherit the gate through `loadAndValidate`.

Note on attribution: this task only **gates** participation. Recording admin participation on the `interactive.message`/`interactive.ended` events (actor attribution) is Task 6; here we add the check only.

### Modify: `adapter/mcp/tools_interactive.go` — scope `interactive_get_history` (read)

`interactiveGetHistoryHandler` reads conversation history directly from repos. Route it through the scoped read service from Task 2 so a non-owner gets `ErrNotFound`:

```go
inst, err := srv.deps.Engine.GetInstance(ctx, id) // scoped; ErrNotFound if not visible
if err != nil {
	return gomcp.NewToolResultError(fmt.Sprintf("failed to get instance: %s", err)), nil
}
// ... then load node execution via the existing NodeExecs call (instance already authorized) ...
```

`interactiveSendMessageHandler` and `interactiveEndSessionHandler` need no change — they already call `ConversationHandler`, which now enforces the gate.

### No change required: REST `interactive_handler.go`, WS `stream_handler.go`

Both already call `ConversationHandler.EndSession`/`HandleMessage` with the request context. The new gate inside `loadAndValidate` covers them. The WS inbound error mapping (`stream_handler.go:243`) already maps `instance.ErrNotFound` → "Session not found", so a non-owner who somehow reaches the inbound router (e.g. via a different surface) gets a generic client-safe message. Confirm the REST `EndSession` 404 branch (`interactive_handler.go:43`) covers the new `ErrNotFound` path — it does.

## Testing (strict TDD — five scenario categories)

Extend `domain/engine/conversation_test.go` (or a focused `conversation_authz_test.go`) with a fake `InstanceRepository` returning an instance owned by user A in interactive-wait state, plus the existing fakes for selector/resolver/eventBus.

- **Happy path:** owner A → `HandleMessage` and `EndSession` proceed (stream consumed / session ended) exactly as today.
- **Validation:** admin (non-owner) → both proceed; the admin may participate in any session.
- **Error/failure:** instance load returns an infra error → wrapped error propagated (not masked).
- **Boundary:** instance owned by A but **not** in `StateWaiting` and caller is A → still `ErrNotInteractiveWait` (the ownership gate passes, the state gate fires) — order matters: a *viewer* in the wrong state sees the state error, a *non-viewer* in any state sees `ErrNotFound`.
- **Isolation/security:** non-owner non-admin (user B) → both `HandleMessage` and `EndSession` return `ErrNotFound`, and **no mutation occurs**: assert `UpdateVariables`/`UpdateState` and the LLM stream are never invoked, and no `interactive.*` event is published. Add a thin MCP test asserting `interactive_send_message` for a non-owner yields the not-found tool error, and a REST test asserting `POST /instances/{id}/interact/end` for a non-owner returns 404.
