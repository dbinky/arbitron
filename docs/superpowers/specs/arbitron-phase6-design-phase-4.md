# Phase 6 Implementation Design — Phase 4: Visibility & Admin Intervention

## Overview

This final phase makes the authorization model real and observable. It scopes every read and stream to what the caller may see, enforces that intervention actions are permitted, and records attribution when an admin acts on another user's instance. It depends on Phase 1 (context identity) and the owner-aware data already present (instance `ownerID`, queue per-user counts). After this phase the spec's success criteria 7 (visibility enforced everywhere) and 8 (admin intervention attributed) are met.

All enforcement lives in the domain services, so REST, WebSocket, and MCP inherit identical rules — no interface can drift.

---

## 1. Instance Visibility

A small `authz` helper in the instance/engine service derives an owner constraint from the context identity:

- Non-admin → constraint = `OwnerID == identity.UserID`.
- Admin → no constraint.

Apply it to:

- **List:** `instance.ListFilters` gains an `OwnerID *uuid.UUID` field. The **service** sets it (callers never do): non-admin ⇒ self, admin ⇒ nil. The sqlite `List` query and `queries/instances.sql` add the optional owner predicate.
- **Get / trace / node-trace / variables / variable-history:** after loading, the service checks the ownership constraint; a non-owner non-admin gets `ErrNotFound` (not `ErrForbidden`) so a user cannot even confirm another user's instance exists (isolation).

Notifications are already per-user; confirm the same constraint is applied to the notification list.

---

## 2. WebSocket Stream Scoping

- **Per-instance stream** (`/instances/{id}/ws`, `streamHandler`): on connect, load the instance and apply the ownership check before subscribing. Unauthorized → close with a 1008/403-equivalent. This closes the documented "any client can stream/message any instance" gap.
- **System stream** (`/ws`, `sysStreamHandler`): for non-admins, filter the event fan-out so only events for instances they own are delivered; admins receive all. The event already carries `instanceID`; the handler resolves ownership (cached per connection) before forwarding.
- **Interactive endpoints** (send message / end session): apply the ownership-or-admin check before mutating the session — these are the most sensitive unauthenticated surface called out in `server.go`.

---

## 3. MCP Tool Scoping

MCP tools call the same domain services, so list/read scoping and intervention authorization come for free. Two explicit assertions:

- Listing/reading tools (`instance_list`, `instance_get`, `trace_*`, `variables_*`) return only the caller's visible set.
- Admin-only tools (`system_*`, and intervention on others' instances) exist for all callers but the domain rejects non-admins — matching the Phase 5 spec statement that admin tools are "available as tools but rejected for non-admin users."

---

## 4. Admin Intervention & Authorization

Intervention operations already exist (`pause`, `resume`, `cancel`, `set_priority`, `retry`, `rerun`, interactive participate). This phase adds the authorization gate and attribution:

- **Gate:** each operation is allowed if the caller owns the instance **or** is an admin; otherwise `ErrNotFound`/`ErrForbidden` per the read/write distinction.
- The owner always retains full control of their own instances; admins gain control over everyone's.

---

## 5. Attribution

When an admin acts on an instance they do **not** own, the action is recorded so the owner and other admins can see who intervened.

- Migration `007_interventions.up.sql` adds an `instance_interventions` audit table: `id`, `instance_id`, `actor_user_id`, `action`, `detail` (nullable), `created_at`.
- The instance service writes an intervention row whenever the acting identity differs from the instance owner (self-actions by the owner are not noise-logged).
- The intervention list is exposed on the trace surface: REST `GET /api/v1/instances/{id}/interventions` and folded into the trace view; an `instance.intervened` event is published on the bus so live viewers see it. Interactive participation by an admin is attributed the same way (actor recorded on the `interactive.message`/`interactive.ended` events).

Visibility of the intervention log follows the same rule: the instance owner and admins can see it.

---

## 6. Testing Strategy

Each unit covers happy path, validation, error/failure, boundary, and isolation/security:

- **List scoping:** non-admin sees only own instances; admin sees all; the service sets the owner filter regardless of any caller-supplied value (a non-admin cannot widen scope by passing someone else's owner).
- **Get/trace/variables:** owner ok; admin ok; non-owner non-admin → `ErrNotFound` (existence not leaked).
- **WS per-instance:** owner/admin subscribe; non-owner rejected at connect.
- **WS system stream:** non-admin receives only own-instance events; admin receives all.
- **Interactive endpoints:** non-owner non-admin cannot send/end; owner and admin can.
- **Intervention gate:** non-owner non-admin pause/cancel/etc. rejected; admin permitted.
- **Attribution:** admin action on another user's instance writes one intervention row with the correct actor and action; owner acting on own instance writes none; the row is visible to owner and admins, hidden from unrelated users.
- **MCP parity:** an integration test asserting an MCP `instance_list` call returns the same scoped set as the REST endpoint for the same identity.
