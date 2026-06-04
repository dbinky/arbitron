# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### NOTIF-001: Notification list is not scoped per user
**Area**: Instance read scoping / notifications (`adapter/http/notification_handler.go` `List`, MCP `notification_list`, `adapter/sqlite/notification_repo.go`, migration)
**Detail**: Phase 4 read scoping returns `ErrNotFound` for non-owners on instances/traces/variables, but the **notification list is not yet owner-scoped**. The `notifications` table has only an `instance_id` FK and **no owner/user_id column**, so the list cannot be filtered to the requesting identity without a schema change. This is a real isolation gap in org mode: a non-admin could currently list notifications that are not theirs.
**Fix direction**: Add a migration introducing an owner column on `notifications` (or join through the owning instance's `OwnerID`), then add an identity-aware list service mirroring `Engine.ListInstances` and repoint **both** `notification_handler.List` and the MCP `notification_list` tool at it. Add isolation tests (non-admin sees only own; admin sees all) across REST and MCP. Note migrations 003–007 are taken; use the next free number.

### TEST-002: Thin adapter-level end-to-end authz coverage
**Area**: `adapter/http` / `adapter/mcp` integration tests
**Detail**: Per-user scoping is well unit-tested in `domain/engine` (`engine_list_test.go`, `engine_read_test.go`, `conversation_authz_test.go`, `engine_intervention_authz_test.go`, `engine_intervention_attr_test.go`) and proven cross-surface by the MCP/REST parity test, but several adapter tests delegate the scoped service to the underlying repos (admin-equivalent) so legacy behavior tests stay green. There is little coverage that drives the **real** engine through the HTTP router (and the MCP handler) with a **non-admin** identity in context end-to-end.
**Fix direction**: Add a small set of end-to-end adapter tests that construct the real engine + router with a non-admin identity injected (as the auth middleware would) and assert: list returns only own; get/trace/variables on another user's instance → 404; WS per-instance connect rejected; interactive send/end rejected; intervention rejected. Mirror for the MCP handler to lock in parity.

### ATTR-003 (optional): Interventions not folded inline into the trace payload
**Area**: Instance trace surface (`adapter/http` trace handler, MCP `trace`)
**Detail**: Admin interventions are exposed via a dedicated `GET /api/v1/instances/{id}/interventions` endpoint and the `trace_interventions` MCP tool rather than inline in the existing `GET /trace` array response. This was a deliberate choice to avoid breaking the trace contract the dashboard consumes. Functionally complete; this item is only about presentation.
**Fix direction**: If the product wants interventions inline, introduce a versioned/object-shaped trace response and update the dashboard consumer accordingly. Otherwise the user may move this to Won't Fix.

## Fixed Previously

<!--
Example of a fixed issue entry:

### COMP-A-001: Input validation missing for empty strings
**Area**: Component A (`path/to/component_a.py`)
**Fixed**: Iteration 3
**Detail**: ...
-->

_(none yet — this is the seed state.)_

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
