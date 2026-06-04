# Phase 3, Task 7: Run Shared Directly — Trigger Scope Resolution

## Goal

Finalize the engine `Trigger` path so triggering a workflow resolves it by name across the **caller's visible scopes** (private first, then shared), snapshots the resolved definition into the instance, and sets the instance `OwnerID` to the **triggering** user (not the publisher). A shared workflow runs directly with no private copy required.

Source of truth: design §6 (Run Shared Directly).

## Dependencies

- **Task 1** (`Scope`), **Task 2** (`LoadVisible`, identity-aware Trigger interim, `OwnerID` set from triggering user), **Phase 1** (`identity.FromContext`).
- Builds on the interim Trigger change from Task 2; this task makes the semantics deliberate and fully tested.

## Cross-phase dependency (this task is the AUTHORITATIVE owner of the `Trigger(ctx, …)` change)

This task is the single owner of adding `context.Context` to `Engine.Trigger` and of resolving the new instance's `OwnerID` from the context identity. The final signature, agreed across phases, is:

```go
func (e *Engine) Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
```

**Phase 4, Task 5 (Intervention Authorization) DEPENDS ON this task** for `Trigger`'s `ctx` and identity-derived owner: its `Rerun` forwards the request `ctx` into `Trigger` and relies on this task's owner-resolution to stamp the acting caller as the rerun's owner. Phase 4 Task 5 does NOT re-define `Trigger`; it only adds `ctx` to the six intervention methods (`Pause/Resume/Cancel/SetPriority/Retry/Rerun`) and the `authorizeIntervention` gate. If build order places Phase 4 before Phase 3, this task's `Trigger` change is a prerequisite that must land first.

## Background (current state)

- `domain/engine/engine.go` `Trigger(workflowName, variables, priority)` (line 105):
  - line 108 (pre-Task-2): `wf, err := e.workflows.Load(ctx, workflowName)` — flat, scope-blind.
  - line 146-153: marshals `wf` → `WorkflowSnapshot` (a `map[string]any`) stored on the instance. **This snapshot is why re-publishing a shared workflow never affects running/queued instances** (Task 5 boundary).
  - line 156: `ownerID, err := e.users.GetDefaultUser(ctx)` — always the default user today.
  - line 168-180: builds `instance.Instance{OwnerID: ownerID, WorkflowName: workflowName, WorkflowSnapshot: wfSnapshot, …}`.
  - line 192: `e.queueManager.Enqueue(ctx, inst.ID, inst.OwnerID)` — per-user queue capacity keys off `OwnerID`, so the owner must be the triggering user for the instance to show in *their* queue.
- `Trigger` uses `ctx := context.Background()` (line 106) — it ignores any caller context today. To resolve the triggering identity, `Trigger` must receive the caller's context.
- Callers of `Trigger`: `adapter/http/workflow_handler.go` `Trigger` handler (via the `WorkflowService`/`TriggerService` interface — `Trigger(workflowName, variables, priority)`), and any scheduler/webhook trigger path in the engine. The HTTP signature `Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)` has **no ctx** today.

## Files to Create/Modify

### Modify: `domain/engine/engine.go` — `Trigger` signature + resolution

- Add `ctx context.Context` as the first parameter so the triggering identity flows in:

  ```go
  func (e *Engine) Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
  ```

  Drop the internal `ctx := context.Background()`; use the passed `ctx`. (Keep a `context.Background()` fallback only for internal/system callers that genuinely have none — see scheduler note below.)

- Resolve owner + workflow from the caller's visible scopes:

  ```go
  owner, ok := triggerOwner(ctx, e.users)
  // owner = identity user if present, else default user (system/scheduler triggers)

  wf, _, err := workflow.LoadVisible(ctx, e.workflows, owner, workflowName)
  if err != nil {
  	return uuid.Nil, false, fmt.Errorf("failed to load workflow: %w", err)
  }
  ```

  where:

  ```go
  // triggerOwner returns the triggering user's id: the context identity when
  // present (REST/MCP triggers), otherwise the default user (scheduler/webhook
  // system triggers that carry no identity).
  func triggerOwner(ctx context.Context, users instance.UserRepository) (uuid.UUID, bool) {
  	if id, ok := identity.FromContext(ctx); ok {
  		return id.UserID, true
  	}
  	def, err := users.GetDefaultUser(ctx)
  	if err != nil {
  		return uuid.Nil, false
  	}
  	return def, false
  }
  ```

- Replace the unconditional `ownerID, err := e.users.GetDefaultUser(ctx)` (line 156) — `ownerID = owner` from `triggerOwner`. The snapshot logic (146-153) is unchanged: it snapshots whatever `LoadVisible` returned (private or shared). The instance's `OwnerID` is the triggering user regardless of whether the definition came from shared — so a shared workflow run shows in the **triggering** user's queue, independent of the publisher.

- **No private copy is created.** Running a shared workflow directly only snapshots it; do not call `CopyTree` here.

### Modify: `adapter/http/workflow_handler.go` — thread request context into Trigger

- The handler's `Trigger` already has `r.Context()`. Change the `TriggerService` interface (renamed from the old local `WorkflowService` in Task 3) to `Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)` and pass `r.Context()`. The request context carries the identity injected by the Phase 1 auth middleware, so `LoadVisible`/`triggerOwner` see the caller.

### Modify: MCP trigger path (`adapter/mcp/`)

- The instance-trigger MCP tool (`instance_trigger`, defined in the instance tools, not `tools_workflow.go`) calls the engine `Trigger`. Update it to pass the tool handler's `ctx` (which carries identity from the `/mcp` middleware) into `Trigger`. Grep `\.Trigger(` across `adapter/mcp` and update each call.

### Modify: scheduler / webhook trigger callers

- Any engine-internal caller of `Trigger` (timer scheduler, webhook resume) has no request identity. Pass `context.Background()`; `triggerOwner` falls back to the default user. Confirm via grep `\.Trigger(` across `domain/engine` and `adapter/` that every call site supplies a `ctx` argument now (signature change is breaking).

## Testing (strict TDD — five scenario categories)

Engine tests use fakes for `workflow.WorkflowRepository`, `instance.UserRepository`, `instance.InstanceRepository`, and the queue manager; inject identity via `identity.WithIdentity`.

- **Happy path:** triggering a workflow that exists only in **shared** creates an instance whose `WorkflowSnapshot` matches the shared definition and whose `OwnerID == triggering user` (not the publisher); no private folder is created (assert `repo.Exists(Private(caller), name)` is still false after trigger).
- **Validation:** triggering with required inputs missing still returns the existing `missing required input` error (resolution happens before input validation, so the error path is preserved).
- **Error/failure:** triggering a name present in neither private nor shared returns the `failed to load workflow` wrap over `ErrNotFound`.
- **Boundary:** when the **same name** exists in both the caller's private and shared scopes, the **private** definition wins (private-first) — assert the snapshot equals the private one; a system trigger (no identity in ctx) resolves against the default user + shared.
- **Isolation/security:** user B triggering a workflow that exists only in user A's **private** scope gets `ErrNotFound` (A's private space is invisible to B); a shared workflow triggered by B produces an instance owned by B and enqueued under B's per-user capacity (verify `Enqueue` received B's id), never the publisher's.
