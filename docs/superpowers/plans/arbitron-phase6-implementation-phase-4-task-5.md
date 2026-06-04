# Phase 4, Task 5: Intervention Authorization — gate pause / resume / cancel / set_priority / retry / rerun

## Goal (Source of truth: design §4 "Admin Intervention & Authorization")

Add the authorization gate to every intervention operation. Each of `Pause`, `Resume`, `Cancel`, `SetPriority`, `Retry`, `Rerun` (plus interactive participate, already gated in Task 4) is allowed **iff** the caller owns the instance **or** is an admin; otherwise the operation is refused. The owner always retains full control of their own instances; admins gain control over everyone's.

Because these are write operations, a non-viewer is refused: returning `instance.ErrNotFound` keeps existence unleaked (consistent with reads), which the design permits ("`ErrNotFound`/`ErrForbidden` per the read/write distinction" — we use `ErrNotFound` so a probe cannot distinguish "exists but forbidden" from "absent").

## Dependencies

- Task 2 (`canView` in `domain/engine/authz.go`).
- Phase 1 identity in context.
- **Cross-phase dependency — Phase 3, Task 7 OWNS the `Trigger(ctx, …)` change.** Phase 3 Task 7 is the single authoritative owner of adding `context.Context` to `Engine.Trigger` and of resolving the new instance's `OwnerID` from the context identity (caller's `UserID`, falling back to `GetDefaultUser` for system/scheduler triggers). **This task does NOT re-define, re-signature, or re-implement `Trigger`'s ctx/owner threading.** It only *consumes* the already-ctx-threaded `Trigger` from `Rerun` (passing the request `ctx` through). If Phase 4 lands before Phase 3 in a given build order, treat `Trigger(ctx, …)` as a prerequisite delivered by Phase 3 Task 7; do not introduce a conflicting `Trigger` signature here.
- **Signature change (THIS task's scope):** the engine *intervention* methods currently take **no `context.Context`** and call `context.Background()` internally (`engine.go` `Pause` line 776, `Resume` 817, `Cancel` 929, `Retry` 991, `Rerun` 1040, `SetPriority` 1069). This task adds `ctx context.Context` to exactly those six methods plus the `authorizeIntervention` gate. `Trigger`'s own ctx is out of scope here (owned by Phase 3 Task 7). The intervention-method change ripples to the `InstanceService` interfaces in both `adapter/http/instance_handler.go` and `adapter/mcp/deps.go`, and to the MCP/HTTP call sites. This is the load-bearing refactor of this task — do it first, keep it green, then add the gate.

## Background (current state)

- `domain/engine/engine.go` intervention methods all begin with `ctx := context.Background()` then `inst, err := e.instances.Get(ctx, id)` and a state-machine switch. None consult identity.
- HTTP `InstanceService` (`instance_handler.go:20`): `Pause(id)`, `Resume(id)`, `Cancel(id)`, `Retry(id, edits)`, `Rerun(id)`, `SetPriority(id, priority)` — no `ctx`. Handlers call e.g. `h.service.Pause(id)` with the request context discarded.
- MCP `EngineService` (`deps.go:20`): same no-`ctx` shape; handlers call `srv.deps.Engine.Pause(id)`.
- `Trigger`'s ctx threading and identity-derived `OwnerID` are delivered by **Phase 3, Task 7** (the authoritative owner of that change); this task only consumes the resulting `Trigger(ctx, …)` from `Rerun`.
- Existing error mapping already covers `ErrNotFound` → 404 (HTTP `mapEngineError`) and tool errors (MCP), so refusal slots in cleanly.

## Files to Create/Modify

### Modify: `domain/engine/engine.go` — thread `ctx` and add the gate

1. Change each method to accept `ctx context.Context` as the first parameter and drop the internal `context.Background()`:

```go
func (e *Engine) Pause(ctx context.Context, instanceID uuid.UUID) error { ... }
func (e *Engine) Resume(ctx context.Context, instanceID uuid.UUID) error { ... }
func (e *Engine) Cancel(ctx context.Context, instanceID uuid.UUID) error { ... }
func (e *Engine) Retry(ctx context.Context, instanceID uuid.UUID, variableEdits map[string]any) error { ... }
func (e *Engine) Rerun(ctx context.Context, instanceID uuid.UUID) (uuid.UUID, error) { ... }
func (e *Engine) SetPriority(ctx context.Context, id uuid.UUID, priority string) error { ... }
```

The internal goroutines these methods spawn (`go e.run(runCtx, ...)`) must keep using a background-derived `runCtx` (the run loop outlives the request), so only the *load/authorize* portion uses the request `ctx`. Keep the existing `context.WithCancel(context.Background())` for `runCtx`; do **not** tie the run loop to the request context.

2. Add a shared write-authorization guard and call it right after the instance loads in each method:

```go
// authorizeIntervention loads the instance and confirms the caller may act on
// it (owner or admin). It returns the loaded instance so the caller reuses it
// (avoiding a second Get). A non-viewer gets ErrNotFound (existence not leaked).
func (e *Engine) authorizeIntervention(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canView(ctx, inst.OwnerID) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return inst, nil
}
```

Then in each method replace the opening `inst, err := e.instances.Get(ctx, id)` with `inst, err := e.authorizeIntervention(ctx, id)`. The subsequent state-machine logic is unchanged and reuses `inst`.

3. `Rerun` is special: it authorizes the **source** instance (the one being rerun) via `authorizeIntervention`, then calls the already-ctx-threaded `e.Trigger(ctx, …)` for the new instance, **passing the same request `ctx`** so Phase 3 Task 7's owner-resolution stamps the acting caller as the new instance's owner. This task adds NO new `Trigger` logic — it relies entirely on Phase 3 Task 7 for `Trigger`'s `ctx` parameter and identity-derived `OwnerID`. The new instance is independently scoped and owned by the actor.

### `Trigger` owner from identity — OWNED BY PHASE 3, TASK 7 (not this task)

Resolving `Trigger`'s new-instance `OwnerID` from the context identity (caller's `UserID`, else `GetDefaultUser` fallback for system/scheduler triggers) and adding the `ctx context.Context` first parameter to `Trigger(ctx, workflowName, variables, priority)` is implemented **exclusively in Phase 3, Task 7** ("Run Shared Directly — Trigger Scope Resolution"). The final signature both phases agree on is:

```go
func (e *Engine) Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
```

Do **not** add `resolveOwner`, change `Trigger`'s signature, or alter its owner logic in this task — that would conflict with Phase 3 Task 7. `Rerun` here simply forwards its request `ctx` into `Trigger`. (The MCP `instanceTriggerHandler` and the workflow-trigger HTTP handler are repointed to pass their request context by Phase 3 Task 7 as well.)

### Modify: `adapter/http/instance_handler.go`

Add `ctx context.Context` to every method in the `InstanceService` interface and pass `r.Context()`:

```go
type InstanceService interface {
	ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) // Task 1
	GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error)                            // Task 2
	GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)                         // Task 2
	GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)       // Task 2
	GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error)                               // Task 2
	GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)            // Task 2
	Pause(ctx context.Context, id uuid.UUID) error
	Resume(ctx context.Context, id uuid.UUID) error
	Cancel(ctx context.Context, id uuid.UUID) error
	Retry(ctx context.Context, id uuid.UUID, variableEdits map[string]any) error
	Rerun(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	SetPriority(ctx context.Context, id uuid.UUID, priority string) error
}
```

Update `Pause`/`Resume`/`Cancel`/`Retry`/`Rerun`/`SetPriority` handlers to call `h.service.Xxx(r.Context(), id, ...)`. The existing `mapEngineError` already maps `ErrNotFound` → 404, so a refused intervention returns 404 (matching the read posture).

### Modify: `adapter/mcp/deps.go` and `adapter/mcp/tools_instance.go`

Mirror the interface change in MCP `EngineService` (add `ctx` to `Trigger`, `Pause`, `Resume`, `Cancel`, `Retry`, `Rerun`, `SetPriority`, plus the Task 1/2 read methods). Update each handler to pass its `ctx` (the handlers already receive `ctx context.Context` — note several currently use `_ context.Context`; switch them to `ctx` and forward it).

## Testing (strict TDD — five scenario categories)

New unit `domain/engine/engine_intervention_authz_test.go` with a fake `InstanceRepository` returning an instance owned by user A in an actionable state (e.g. running for Pause, failed for Retry, terminal for Rerun), plus fake queue manager / event bus so the state-machine bodies run.

- **Happy path:** owner A → each of `Pause/Resume/Cancel/SetPriority/Retry/Rerun` succeeds and performs its state transition (assert `UpdateState`/`Trigger` invoked).
- **Validation:** admin (non-owner) → each operation succeeds (admin control over everyone's instances).
- **Error/failure:** instance load returns an infra error → propagated; for a viewer in a non-actionable state the existing state sentinel (`ErrNotRunning`, `ErrNotFailed`, `ErrCannotRerun`, …) is returned unchanged (the gate passes, the state check fires).
- **Boundary:** `Rerun` by an admin on user A's terminal instance → the **new** instance is owned by the admin (because `Rerun` forwards the request `ctx` into `Trigger`, and Phase 3 Task 7's `Trigger` stamps the actor from `identity.FromContext`), and the source instance is untouched; assert the new owner. (This test depends on Phase 3 Task 7's `Trigger(ctx, …)` being in place.)
- **Isolation/security:** non-owner non-admin (user B) → every intervention returns `ErrNotFound` and **no state change occurs** (assert `UpdateState`/`UpdatePriority`/`Trigger`/context-cancel are never called, and no event published). Add HTTP tests (`POST /instances/{id}/pause` etc. for a non-owner → 404) and MCP tests (`instance_cancel` for a non-owner → not-found tool error) to prove the gate holds across surfaces.
