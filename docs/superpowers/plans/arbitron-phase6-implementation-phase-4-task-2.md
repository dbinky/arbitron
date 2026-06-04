# Phase 4, Task 2: Read Scoping — Get / trace / variables ownership checks returning `ErrNotFound`

## Goal (Source of truth: design §1 "Instance Visibility")

Scope every single-instance read to the caller's visible set. After loading an instance, the service checks the owner constraint; a non-owner non-admin receives `instance.ErrNotFound` — **not** a forbidden error — so a user cannot even confirm another user's instance exists (isolation). Apply this to: instance `Get`, trace (`ListByInstance`), node-trace (`GetByInstanceAndNode`), variables (current), and variable-history. Confirm the same constraint on the per-user notification list.

## Dependencies

- Task 1 (`domain/engine/authz.go` with `canView(ctx, ownerID)` and the `ownerConstraint` helper; `ListFilters.OwnerID`).
- Phase 1 identity in context.

## Background (current state)

- `adapter/http/instance_handler.go` performs reads **directly against the repositories**, each guarded only by a UUID lookup:
  - `Get` (line 224): `h.instances.Get(ctx, id)` → 404 on `ErrNotFound`.
  - `GetTrace` (line 390): `h.instances.Get` existence check, then `h.nodeExecs.ListByInstance(ctx, id)`.
  - `GetNodeTrace` (line 420): `h.instances.Get`, then `h.nodeExecs.GetByInstanceAndNode(ctx, id, nodeID)`.
  - `GetVariables` (line 470): `h.instances.Get`, returns `inst.Variables`.
  - `GetVariableHistory` (line 492): `h.instances.Get`, then `h.snapshots.ListByInstance(ctx, id)`.
- MCP mirrors these against `srv.deps.Instances`/`NodeExecs`/`Snapshots` in `tools_instance.go` (`instanceGetHandler`) and `tools_trace.go` (`traceGetHandler`, `traceGetNodeHandler`, `variablesGetHandler`, `variablesHistoryHandler`).
- The per-instance WS handler also calls `h.instances.Get` directly (Task 3 covers WS).
- `stream_handler.go` lines 105–124 document the pre-existing IDOR shape: reads authorize on UUID alone because `OwnerID` was always the single fixed local user. With Phase 1 real identities, that constraint becomes meaningful.
- `instance.ErrNotFound` already maps to HTTP 404 via `mapEngineError` (line 144) and the MCP handlers surface `Get` errors as tool errors.

The fix mirrors Task 1: move read enforcement into engine **service** methods that load, check `canView`, and return `ErrNotFound` for non-viewers, then repoint both adapters at the service. Existence is never leaked: a missing instance and a not-owned instance both yield `ErrNotFound`.

## Files to Create/Modify

### Modify: `domain/engine/engine.go` — add scoped read service methods

Each method loads via the repo, applies `canView`, and collapses both "absent" and "not visible" to `ErrNotFound`:

```go
// GetInstance loads an instance the caller may view, else ErrNotFound.
func (e *Engine) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return nil, err // already ErrNotFound-wrapped on absence
	}
	if !canView(ctx, inst.OwnerID) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return inst, nil
}

// authorizeView is the shared guard for the sub-resource reads (trace,
// variables, history): it loads the owning instance and returns ErrNotFound if
// the caller may not view it. It returns the loaded instance for callers that
// need its fields (e.g. GetVariables).
func (e *Engine) authorizeView(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	return e.GetInstance(ctx, id)
}

func (e *Engine) GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.nodeExecutions.ListByInstance(ctx, id)
}

func (e *Engine) GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.nodeExecutions.GetByInstanceAndNode(ctx, id, nodeID)
}

func (e *Engine) GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error) {
	inst, err := e.authorizeView(ctx, id)
	if err != nil {
		return nil, err
	}
	return inst.Variables, nil
}

func (e *Engine) GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.snapshots.ListByInstance(ctx, id)
}
```

Note: the trace/history walls authorize on the **owning instance**, never on the sub-records, so a non-owner who guesses an instance ID still gets `ErrNotFound` before any node execution is read.

### Modify: `adapter/http/instance_handler.go`

Add the five methods to the `InstanceService` interface and repoint the handlers:

- `Get` → `h.service.GetInstance(r.Context(), id)`; keep the existing `ErrNotFound` → 404 mapping (now also covers "not owned").
- `GetTrace` → drop the direct `instances.Get` + `nodeExecs.ListByInstance`; call `h.service.GetTrace(...)`. Map `ErrNotFound` → 404.
- `GetNodeTrace` → `h.service.GetNodeTrace(r.Context(), id, nodeID)`. Note `GetByInstanceAndNode`'s own `ErrNotFound` (missing node) still maps to 404 with the "node execution not found" message; an unauthorized instance returns 404 from the instance-level check first.
- `GetVariables` → `h.service.GetVariables(...)`.
- `GetVariableHistory` → `h.service.GetVariableHistory(...)`.

The handler keeps its `errors.Is(err, instance.ErrNotFound)` → 404 branches; the only change is the call target. The handler's direct `nodeExecs`/`snapshots` repo fields become unused for these paths — remove them from the struct only if no other method uses them (Get/trace are the sole consumers), otherwise leave wired.

### Modify: `adapter/mcp/tools_instance.go`, `adapter/mcp/tools_trace.go`, `adapter/mcp/deps.go`

Add `GetInstance`, `GetTrace`, `GetNodeTrace`, `GetVariables`, `GetVariableHistory` to the MCP `EngineService` interface (`deps.go`). Repoint:
- `instanceGetHandler` → `srv.deps.Engine.GetInstance(ctx, id)`.
- `traceGetHandler` → `srv.deps.Engine.GetTrace(ctx, id)`.
- `traceGetNodeHandler` → `srv.deps.Engine.GetNodeTrace(ctx, instID, nodeID)`.
- `variablesGetHandler` → `srv.deps.Engine.GetVariables(ctx, id)`.
- `variablesHistoryHandler` → `srv.deps.Engine.GetVariableHistory(ctx, id)`.

Errors surface via the existing `NewToolResultError` wrap; a non-owner gets the `ErrNotFound` string, identical to a genuinely missing instance (no existence leak).

### Confirm (no change unless gap): notification list scoping

Notifications are already per-user. Verify `domain/notification` carries the owning user and that the notification list service filters by the caller's `UserID` for non-admins (admins unconstrained), matching `instance` list scoping. If `notification.ListFilters` lacks an owner/`UserID` field, add it and set it from identity in the notification list service the same way Task 1 does for instances; otherwise document that the existing per-user query already satisfies the constraint. (`domain/notification/ports.go` `ListFilters{ Dismissed, Limit, Offset }` — confirm against the notification service before deciding.)

## Testing (strict TDD — five scenario categories)

New unit `domain/engine/engine_read_test.go` with a fake `InstanceRepository`/`NodeExecutionRepository`/`VariableSnapshotRepository`. Seed an instance owned by user A.

- **Happy path:** owner A reads `GetInstance`/`GetTrace`/`GetNodeTrace`/`GetVariables`/`GetVariableHistory` → success, full data.
- **Validation:** admin (different `UserID`, `IsAdmin()==true`) reads the same instance → success on every method.
- **Error/failure:** repo `Get` returns a non-`ErrNotFound` infra error → propagated unchanged (not masked as `ErrNotFound`).
- **Boundary:** instance genuinely absent (`repo.Get` → `ErrNotFound`) → each method returns `ErrNotFound`; sub-resource repos are never called (assert call counts zero).
- **Isolation/security:** non-owner non-admin (user B) reads each method → `ErrNotFound`, and the **sub-resource repos are never queried** (trace/variables/history never loaded), proving existence and content are both withheld. Add an HTTP test asserting `GET /instances/{id}/trace` for a non-owner returns **404** (identical body to a missing instance), and an MCP test asserting `trace_get` for a non-owner returns the same not-found tool error as a missing instance.
