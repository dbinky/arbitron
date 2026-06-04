# Phase 4, Task 3: WebSocket Stream Scoping — per-instance connect check + system-stream fan-out filter

## Goal (Source of truth: design §2 "WebSocket Stream Scoping")

Scope both WebSocket surfaces to the caller's visible set:

- **Per-instance stream** (`GET /api/v1/instances/{id}/ws`, `StreamHandler.HandleWebSocket`): on connect, load the instance and apply the ownership check **before** subscribing/upgrading. A non-owner non-admin is rejected (close 1008 / 404-equivalent), closing the documented "any client can stream/message any instance" gap.
- **System stream** (`GET /api/v1/ws`, `SystemStreamHandler.HandleWebSocket`): for non-admins, filter the fan-out so only events for instances the caller owns are delivered; admins receive all. The event carries the `instanceID` (it is the `Subscribe` key); ownership is resolved once per instance and cached per connection.

## Dependencies

- Task 1 (`ownerConstraint`) and Task 2 (`canView`, `Engine.GetInstance` scoped read).
- Phase 1 identity in context: the auth middleware must wrap the WS upgrade handlers so `r.Context()` carries an `Identity` (design §4 mounts the middleware over the WS upgrade). This task assumes the request context at `HandleWebSocket` entry already has the identity.

## Background (current state)

- `adapter/http/stream_handler.go` `HandleWebSocket` (line 96): parses the instance UUID, then (lines 105–124) a long comment explains the **intentional** pre-existing IDOR — it subscribes/streams to any caller knowing the UUID because `OwnerID` was always the single fixed local user. It calls `h.instances.Get(r.Context(), id)` only to verify existence, then `websocket.Accept`, then `h.eventBus.Subscribe(id)`. The inbound read goroutine routes `interactive.send`/`interactive.end` to `h.conversationHandler` (Task 4 covers those mutations).
- `adapter/http/system_stream_handler.go` `HandleWebSocket` (line 32): immediately `websocket.Accept`, then `h.eventBus.SubscribeAll()` and forwards **every** event via `writeWSEvent`. No per-connection filtering.
- `engine.Event` (`domain/engine/ports.go:204`) is `{Type, Timestamp, Data any}` — the instance ID is **not** a field on the event; it is the `Publish(instanceID, event)` routing key. `SubscribeAll()` returns events without their instance ID attached, so the system stream cannot currently tell which instance an event belongs to.

This last point forces a small EventBus contract addition: the system stream must learn each event's instance ID to filter by ownership.

## Files to Create/Modify

### Modify: `domain/engine/ports.go` — expose the instance ID to `SubscribeAll` consumers

The per-instance `Subscribe(id)` channel is already scoped, so it needs nothing. The system stream needs the routing key. Add an `InstanceID` to the event envelope delivered by `SubscribeAll` without breaking the per-instance shape. Preferred: a dedicated fan-out type so the JSON wire shape of `Event` is untouched:

```go
// ScopedEvent pairs an event with the instance it was published for, so
// SubscribeAll consumers (the system stream) can filter by instance ownership.
type ScopedEvent struct {
	InstanceID uuid.UUID
	Event      Event
}

type EventBus interface {
	Publish(instanceID uuid.UUID, event Event)
	Subscribe(instanceID uuid.UUID) (<-chan Event, func())
	// SubscribeAll now yields ScopedEvent so consumers can authorize per event.
	SubscribeAll() (<-chan ScopedEvent, func())
}
```

Update the concrete bus implementation (the in-memory event bus adapter — discover it under `adapter/` or `domain/engine`; it is the only `SubscribeAll` implementer) to deliver `ScopedEvent{InstanceID, Event}`. `Publish` already knows the instance ID. This is the minimal change that lets the system stream filter; it does not alter the per-instance `Subscribe` path or the on-the-wire `Event` JSON (the handler still writes `evt.Event`).

### Modify: `adapter/http/stream_handler.go` — per-instance ownership check on connect

Repoint the existence load to the scoped service read from Task 2 and reject non-viewers before upgrading. Replace the IDOR comment block with the real check:

```go
// Load + authorize in one step: GetInstance returns ErrNotFound for both a
// missing instance and one the caller may not view (no existence leak).
inst, err := h.service.GetInstance(r.Context(), id)
if err != nil {
	if errors.Is(err, instance.ErrNotFound) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found")
		return
	}
	WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
	return
}
```

This requires `StreamHandler` to hold the engine service (an interface with `GetInstance(ctx, uuid.UUID) (*instance.Instance, error)`); add it to `NewStreamHandler`'s deps and wire it in `server.go`'s `NewStreamHandler(...)` call. Rejection happens **before** `websocket.Accept`, so an unauthorized client gets a plain HTTP 404, never an upgraded socket. Delete the stale IDOR comment (lines 105–124) since the gap is now closed; replace it with a one-line note that authorization is enforced via `GetInstance`.

### Modify: `adapter/http/system_stream_handler.go` — per-connection ownership filter

Resolve the connection's identity once, then filter each `ScopedEvent` before writing. Admins skip the filter; non-admins consult a per-connection ownership cache (so each instance ID is resolved at most once):

```go
func (h *SystemStreamHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	id, ok := identity.FromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	// ... websocket.Accept as today ...

	ch, unsub := h.eventBus.SubscribeAll()
	defer unsub()

	// Per-connection ownership cache: instanceID -> visible? Resolved lazily.
	visible := make(map[uuid.UUID]bool)

	for {
		select {
		case sev, ok := <-ch:
			if !ok { /* close as today */ }
			if !h.canSee(r.Context(), id, sev.InstanceID, visible) {
				continue // drop events for instances this caller may not see
			}
			if err := writeWSEvent(ctx, conn, sev.Event); err != nil {
				return
			}
		// clientDone / ctx.Done as today
		}
	}
}

// canSee returns whether the identity may receive events for instanceID,
// memoizing the per-connection result. Admins see all.
func (h *SystemStreamHandler) canSee(ctx context.Context, id identity.Identity, instanceID uuid.UUID, cache map[uuid.UUID]bool) bool {
	if id.IsAdmin() {
		return true
	}
	if v, ok := cache[instanceID]; ok {
		return v
	}
	inst, err := h.instances.Get(ctx, instanceID)
	v := err == nil && inst.OwnerID == id.UserID
	cache[instanceID] = v
	return v
}
```

`SystemStreamHandler` gains an `instances instance.InstanceRepository` dependency (for the ownership lookup); add it to `NewSystemStreamHandler` and wire `d.Instances` in `server.go`. The cache is per-connection (local map), so an ownership change mid-connection is not observed until reconnect — acceptable, and noted in a comment. Use the engine `canView` helper logic inline (the handler is an adapter and cannot import the unexported `canView`; replicate the admin-or-owner test as shown, or expose a small exported `identity`-based helper).

### Modify: `adapter/http/server.go`

- `NewStreamHandler(d.Instances, d.NodeExecs, d.EventBus, d.ConversationHandler, d.Service)` — pass the engine service for the connect-time `GetInstance` check.
- `NewSystemStreamHandler(d.EventBus, d.Instances)` — pass the repo for ownership resolution.
- Remove the `TODO(phase-6)` auth note and the `/mcp` "NO authentication" warning only after Phase 1's middleware is mounted; if Phase 1 already removed them, leave as-is. The WS routes (`/ws`, `/instances/{id}/ws`) must sit **inside** the authenticated `/api/v1` group so the identity is present.

## Testing (strict TDD — five scenario categories)

Per-instance (`adapter/http/stream_handler_test.go`, using a fake service + identity-injecting context):

- **Happy path:** owner connects → upgrade succeeds, events stream.
- **Validation:** admin (non-owner) connects → upgrade succeeds.
- **Error/failure:** service `GetInstance` returns an infra error → 500, no upgrade.
- **Boundary:** instance in a terminal state for the owner → the existing single `state_changed`-then-close path still works post-authorization.
- **Isolation/security:** non-owner non-admin connects → **404 before upgrade**; assert `websocket.Accept` is never reached (no socket, no `Subscribe`).

System stream (`adapter/http/system_stream_handler_test.go`, fake bus emitting `ScopedEvent`s for two owners' instances + a fake `InstanceRepository`):

- **Happy path:** admin connection receives events for both owners' instances.
- **Validation:** non-admin owner-A connection receives only owner-A instance events; owner-B events are dropped.
- **Error/failure:** ownership lookup errors for an instance → that instance's events are dropped (fail-closed), other instances unaffected.
- **Boundary:** repeated events for the same instance hit the cache (assert `instances.Get` is called once per instance, not per event).
- **Isolation/security:** no identity in context → 401, no `SubscribeAll`. Two concurrent connections (owner A, owner B) over one bus each see only their own instance's events.

EventBus (`SubscribeAll` implementer test): `Publish(instanceID, evt)` is delivered to `SubscribeAll` as `ScopedEvent{InstanceID: instanceID, Event: evt}`; the per-instance `Subscribe(id)` channel still yields the bare `Event` unchanged.
