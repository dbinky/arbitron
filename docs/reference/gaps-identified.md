# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### TEST-002: Thin adapter-level end-to-end authz coverage
**Area**: `adapter/http` / `adapter/mcp` integration tests
**Detail**: Per-user scoping is well unit-tested in `domain/engine` (`engine_list_test.go`, `engine_read_test.go`, `conversation_authz_test.go`, `engine_intervention_authz_test.go`, `engine_intervention_attr_test.go`) and proven cross-surface by the MCP/REST parity test, but several adapter tests delegate the scoped service to the underlying repos (admin-equivalent) so legacy behavior tests stay green. There is little coverage that drives the **real** engine through the HTTP router (and the MCP handler) with a **non-admin** identity in context end-to-end.
**Fix direction**: Add a small set of end-to-end adapter tests that construct the real engine + router with a non-admin identity injected (as the auth middleware would) and assert: list returns only own; get/trace/variables on another user's instance → 404; WS per-instance connect rejected; interactive send/end rejected; intervention rejected. Mirror for the MCP handler to lock in parity.

### ATTR-003 (optional): Interventions not folded inline into the trace payload
**Area**: Instance trace surface (`adapter/http` trace handler, MCP `trace`)
**Detail**: Admin interventions are exposed via a dedicated `GET /api/v1/instances/{id}/interventions` endpoint and the `trace_interventions` MCP tool rather than inline in the existing `GET /trace` array response. This was a deliberate choice to avoid breaking the trace contract the dashboard consumes. Functionally complete; this item is only about presentation.
**Fix direction**: If the product wants interventions inline, introduce a versioned/object-shaped trace response and update the dashboard consumer accordingly. Otherwise the user may move this to Won't Fix.

## Fixed Previously

### NOTIF-001: Notification list was not scoped per user
**Area**: Instance read scoping / notifications (`adapter/http/notification_handler.go`, MCP `tools_system.go`, `adapter/sqlite/notification_repo.go`, `domain/notification/ports.go`)
**Fixed**: Iteration 1
**Detail**: Phase 4 read scoping returned `ErrNotFound` for non-owners on instances/traces/variables, but the notification surface was un-scoped. A non-admin could list, count unread, mark-read, and dismiss notifications from other users in org mode.
**Resolution**: Added `OwnerID *uuid.UUID` to `notification.ListFilters` and updated `CountUnread`/`MarkAllRead`/`Dismiss` signatures to accept `ownerID *uuid.UUID` (nil = admin, non-nil = filter to owner). Scoping in the SQLite repo is done via JOIN with `instances.owner_id` — no schema migration required. Both the HTTP handler (`NotificationHandler`) and MCP handler extract the caller identity from context and pass the constraint, matching the pattern used by `domain/engine.ownerConstraint`. Cross-user `Dismiss` fails closed with `ErrNotFound`. Isolation verified by `TestNotificationRepo_IsolationByOwner` (SQLite level) and `TestNotificationHandler_OwnerScoping` (handler level, all four methods).

## Won't Fix (Beyond Current Scope)

_(items logged here are accepted limitations or out-of-scope enhancements — only the user can add to or move items in this section. Pre-seeded from the Phase 6 spec's out-of-scope boundary so the loop does NOT spend iterations building deferred work.)_

### SCOPE-W01: Scheduled & webhook triggers
**Area**: Triggers
**Detail**: Scheduled (cron) and webhook triggers are **Phase 7**, explicitly out of scope for Phase 6 (spec "Overview"). Do not build trigger types here.

### SCOPE-W02: Parallel fan-out / fan-in execution
**Area**: Engine
**Detail**: Fan-out/fan-in parallel branch execution is **Phase 8**, out of scope for Phase 6. Do not build it here.

### SCOPE-W03: Granular per-token permission scopes
**Area**: Personal API tokens
**Detail**: A personal API token carries the **full permissions of its owning user**; per-token granular scopes are explicitly deferred (spec §2). Tokens are made safe by being individually revocable. Do not add scope/permission fields to tokens in this phase.

### SCOPE-W04: Resource-server / per-request IdP token validation
**Area**: Auth model
**Detail**: The credential model is deliberately Arbitron-native (sessions + tokens; IdP only at login). Validating IdP-issued JWTs on every request was considered and rejected. Do not introduce per-request IdP/JWKS validation.

### SCOPE-W05: Admin approval gate for publishing
**Area**: Publishing
**Detail**: Publishing is **self-service** by design (any User may publish; spec §4 "Defaults"). Do not add an admin approval workflow.
