# Phase 4, Task 1: List Scoping — `ListFilters.OwnerID` + service-set owner constraint

## Goal (Source of truth: design §1 "Instance Visibility")

Scope `instance.List` to the caller's visible set. Add an `OwnerID *uuid.UUID` field to `instance.ListFilters`, derive an owner constraint from the request-scoped identity **inside a domain service** (non-admin ⇒ own `UserID`; admin ⇒ unconstrained), and add the optional owner predicate to the SQLite `ListInstances` query. The crucial invariant: the **service** sets `filters.OwnerID` from `identity.FromContext`; callers never set it, so a non-admin cannot widen scope by supplying another owner's UUID.

After this task `GET /api/v1/instances`, MCP `instance_list`, and any future caller of the list service return only instances the caller may see.

## Dependencies

- Phase 1 (Identity core): `domain/identity` with `identity.FromContext(ctx) (Identity, bool)` and `Identity.IsAdmin() bool`, and the auth middleware that injects an `Identity` into every request context (REST, `/mcp`, WS). This task assumes that package exists and that every code path reaching the list service carries an identity.
- First task of Phase 4; Tasks 2–6 build on the `authz` helper introduced here.

## Background (current state)

- `domain/instance/instance.go` (~line 129) defines `ListFilters{ State, WorkflowName, Since, Limit, Offset }` — no owner field. `Instance` already carries `OwnerID uuid.UUID` (line 48).
- `domain/instance/ports.go` declares `InstanceRepository.List(ctx, ListFilters) ([]InstanceSummary, error)`. The queue port already proves owner-awareness exists: `QueueRepository.CountRunningByOwner(ctx, ownerID)`.
- `adapter/sqlite/queries/instances.sql` — `ListInstances :many` filters on `state`, `workflow_name`, `since` via `sqlc.narg(...)` (NULL ⇒ no filter); `ORDER BY created_at DESC LIMIT/OFFSET`. There is no `owner_id` predicate and the projection omits `owner_id`.
- `adapter/sqlite/instance_repo.go` `List` (line 82) maps `ListFilters` → `generated.ListInstancesParams`, setting `State`/`WorkflowName`/`Since` only when non-nil.
- `adapter/http/instance_handler.go` `List` (line 180) builds `ListFilters` from query params and calls `h.instances.List(r.Context(), filters)` **directly on the repository** — there is no list method on the engine service today. The MCP `instanceListHandler` (`adapter/mcp/tools_instance.go:99`) does the same against `srv.deps.Instances.List`.
- Because both surfaces call the repo directly, the owner constraint cannot be enforced in the repo (the repo has no identity). The design requires enforcement in a **domain service**, so this task introduces a `ListInstances` service method on the engine and an `authz` helper, and repoints both adapters at it.

## Files to Create/Modify

### Modify: `domain/instance/instance.go`

Add the owner field to `ListFilters`:

```go
// ListFilters specifies filtering and pagination options for listing instances.
type ListFilters struct {
	State        *State
	WorkflowName *string
	Since        *time.Time
	// OwnerID, when non-nil, restricts results to instances owned by that user.
	// SECURITY: this is set by the engine's ListInstances service from the
	// request identity (non-admin ⇒ own UserID); list callers (HTTP/MCP handlers)
	// MUST NOT set it from request input, or a non-admin could widen scope.
	OwnerID *uuid.UUID

	Limit  int
	Offset int
}
```

### Create: `domain/engine/authz.go`

The single place that derives an owner constraint from the context identity. Lives in `domain/engine` so every service method shares it; depends only on `domain/identity` (no adapter import).

```go
package engine

import (
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// ownerConstraint returns the OwnerID filter the caller is allowed to see:
// nil for an admin (unconstrained), or a pointer to the caller's own UserID
// for a non-admin. The bool reports whether an identity was present.
func ownerConstraint(ctx context.Context) (*uuid.UUID, bool) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, false
	}
	if id.IsAdmin() {
		return nil, true
	}
	uid := id.UserID
	return &uid, true
}

// canView reports whether the context identity may read an instance owned by
// ownerID: admins may read any instance; a non-admin only their own.
func canView(ctx context.Context, ownerID uuid.UUID) bool {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false
	}
	return id.IsAdmin() || id.UserID == ownerID
}
```

(`context` import elided in the snippet for brevity; include it.) `canView` is unused in this task but is the shared helper Tasks 2/4/5 consume — introduce it here so the authz logic lives in one file. If a linter rejects the unused function, defer `canView` to Task 2; note that in the commit.

### Modify: `domain/engine/engine.go`

Add a `ListInstances` service method. It sets the owner filter from the identity and rejects requests with no identity (defence-in-depth; middleware should always set one):

```go
// ListInstances returns the instances visible to the caller. The owner filter
// is derived from the request identity: a non-admin sees only their own
// instances; an admin sees all. Any OwnerID the caller set on filters is
// overwritten here so scope cannot be widened from request input.
func (e *Engine) ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	owner, ok := ownerConstraint(ctx)
	if !ok {
		return nil, instance.ErrNotFound // no identity: reveal nothing
	}
	filters.OwnerID = owner // service-set; ignore any caller-supplied value
	return e.instances.List(ctx, filters)
}
```

Returning `ErrNotFound` (not an auth error) for the no-identity case keeps the isolation posture consistent with Task 2 and avoids leaking that the surface is unauthenticated.

### Modify: `adapter/sqlite/queries/instances.sql`

Add the optional owner predicate and include `owner_id` in the projection (sqlc needs the column referenced; the summary does not expose it but the predicate must compile):

```sql
-- name: ListInstances :many
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at
FROM instances
WHERE (sqlc.narg('state') IS NULL OR state = sqlc.narg('state'))
AND (sqlc.narg('workflow_name') IS NULL OR workflow_name = sqlc.narg('workflow_name'))
AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
AND (sqlc.narg('owner_id') IS NULL OR owner_id = sqlc.narg('owner_id'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');
```

Regenerate sqlc (`make sqlc` / `sqlc generate`) so `generated.ListInstancesParams` gains `OwnerID sql.NullString` (narg ⇒ nullable).

### Modify: `adapter/sqlite/instance_repo.go`

In `List`, set the new param when the filter is present (mirror the `State`/`Since` pattern, using `uuidToString` from `convert.go`):

```go
if filters.OwnerID != nil {
	params.OwnerID = stringToNullString(uuidToString(*filters.OwnerID))
}
```

### Modify: `adapter/http/instance_handler.go`

Repoint `List` from the repository to the engine service so the owner constraint is enforced. Add `ListInstances(ctx, instance.ListFilters) ([]instance.InstanceSummary, error)` to the `InstanceService` interface (line 20) and call `h.service.ListInstances(r.Context(), filters)` instead of `h.instances.List(...)`. Do **not** read an `owner`/`ownerId` query param — the service owns that decision.

### Modify: `adapter/mcp/tools_instance.go` and `adapter/mcp/deps.go`

In `deps.go`, add `ListInstances` to the MCP `EngineService` interface. In `instanceListHandler`, call `srv.deps.Engine.ListInstances(ctx, filters)` instead of `srv.deps.Instances.List(ctx, filters)`. The MCP request context already carries the caller identity (Phase 1 middleware wraps `/mcp`), so scoping is inherited.

## Testing (strict TDD — five scenario categories)

Write tests first. New unit: `domain/engine/engine_list_test.go` with an in-package fake `InstanceRepository` that records the `ListFilters` it receives and returns a fixed summary set. Identity is injected via `identity.WithIdentity(ctx, ...)`.

- **Happy path:** admin identity → `ListInstances` calls the repo with `filters.OwnerID == nil`; all returned summaries pass through unchanged.
- **Validation:** a caller-supplied `filters.OwnerID` set to some *other* user's UUID is **overwritten** — for a non-admin the recorded `filters.OwnerID` equals the caller's own `UserID`, never the supplied value (the scope-widening guard).
- **Error/failure:** repo `List` returns a wrapped error → `ListInstances` propagates it unchanged (no swallow).
- **Boundary:** context with **no** identity → `ListInstances` returns `instance.ErrNotFound` and never calls the repo (assert the fake's call count is zero).
- **Isolation/security:** non-admin identity → recorded `filters.OwnerID != nil` and equals the caller `UserID`; a second non-admin with a different `UserID` gets a different filter. Add a SQLite-backed repo test (`adapter/sqlite/instance_repo_test.go`) seeding two owners' instances and asserting `List` with `OwnerID = ownerA` returns only ownerA's rows, and `OwnerID = nil` returns both.

Add one HTTP handler test (`adapter/http/instance_handler_test.go`) and one MCP test (`adapter/mcp/tools_instance_test.go`) asserting the handlers delegate to the service (a fake service records the call), so the repo-bypass regression is caught.
