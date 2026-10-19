# Parallel Workflow Gathering — Phase 2 Task 7: Engine.StartGroupTask (reserved-id group-task creation)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the group launcher ONE engine entry point that creates a parallel-group task child under a RESERVED instance id — a workflow-kind task through the exact authorization / owner / visibility-stamping / archived-refusal / input-validation / group-support pipeline `Engine.Trigger` uses, a branch-kind task from a deep copy of the parent's own definition snapshot starting at the validated branch entry — idempotently, with the group linkage stamped in the same insert.
**Architecture:** `domain/engine/engine.go:TriggerWithDepartment` (lines 372–586) is split, behaviour-preservingly, into a thin wrapper plus an internal `trigger(ctx, triggerSpec)` that additionally accepts a reserved id and an explicit link, and a shared `createAndEnqueue` tail. A new `domain/engine/engine_group_task.go` adds `GroupTaskStartRequest`, the `GroupTaskStarter` port, and `Engine.StartGroupTask`, which validates the request, short-circuits an already-created reserved row, enforces owner identity and `instance.MaxSpawnDepth`, then routes to `trigger` (workflow kind) or a snapshot-copy path (branch kind).
**Tech Stack:** Go 1.25.5, standard library, `github.com/google/uuid`, testify; consumes `variable.DeepCopyMap` (P2-T6).

**Prerequisites (must be merged first):** P1-T1 (`workflow.NodeTypeParallelGroup`, `GroupConfig`, `GroupTask`, `NodeConfig.Group`, `GroupPortTask/Success/Failure`, `GroupForm*`, `GroupRule*`, `GroupUnfinished*`, `GroupGather*`), P1-T9 (`GroupSupport`, `SetGroupSupport`, `GroupSupportLevel`, `checkGroupSupport`, `ErrGroupExecutionUnsupported`, and the guard call it inserts into `TriggerWithDepartment`), P2-T1 (`instance.SpawnModeGroupTask`, `GroupTaskKind*`, `Instance.GroupRunID/GroupTaskKey/GroupTaskKind`, `SpawnLink.GroupRunID/GroupTaskKey/GroupTaskKind` and the extended `SpawnLink.Valid`), P2-T6 (`variable.DeepCopyMap`).

---

## Interfaces

**Consumes (exact):**
- `domain/engine/engine.go:284` `func triggerOwner(ctx, users) (uuid.UUID, bool)`; `:310` `(*Engine).resolvePublication`; `:334` `(*Engine).triggerAdminOverrode`; `:354` `(*Engine).sharedVisibilityFilter`; `:1289` `func parseWorkflowGraph(snapshot map[string]any) (*workflow.Workflow, map[string]workflow.Node, map[string][]edgeTarget, error)`.
- `domain/engine/trigger_stamp.go:46` `func resolveStamp(id identity.Identity, pubVisibility instance.Visibility, pubDepartment, override string) (stamp, error)`.
- `domain/workflow/resolve.go:33` `func LoadVisibleFiltered(ctx, repo, owner uuid.UUID, name string, filter SharedVisibilityFilter) (*Workflow, Scope, error)`.
- `domain/instance/spawn_context.go:30` `func SpawnLinkFromContext(ctx) (SpawnLink, bool)`; `domain/instance/origin.go` `OriginFromContext`, `OriginManual`.
- `domain/instance/spawn_link.go:76` `MaxSpawnDepth`, `:81` `ErrSpawnDepthExceeded`; P2-T1: `SpawnModeGroupTask`, `GroupTaskKindBranch`, `GroupTaskKindWorkflow`, `SpawnLink{…, GroupRunID uuid.UUID, GroupTaskKey string, GroupTaskKind GroupTaskKind}`, `Instance{…, GroupRunID *uuid.UUID, GroupTaskKey *string, GroupTaskKind GroupTaskKind}`.
- `domain/identity`: `FromContext`, `WithIdentity`, `ErrForbidden` (`instance.ErrForbidden` is the same value, `domain/instance/user_service.go:21`).
- `domain/variable/variable.go:22` `ReservedInputNames`; P2-T6 `DeepCopyMap`.
- P1-T9: `func (e *Engine) GroupSupportLevel() GroupSupport`, `func checkGroupSupport(wf *workflow.Workflow, level GroupSupport) error`.

**Produces (exact):**
```go
// domain/engine/engine_group_task.go
type GroupTaskStartRequest struct {
	TaskInstanceID   uuid.UUID              // reserved id from the roster
	ParentInstanceID uuid.UUID
	ParentNodeID     string
	GroupRunID       uuid.UUID
	TaskKey          string
	Kind             instance.GroupTaskKind
	ChildWorkflow    string                 // workflow kind
	BranchEntry      string                 // branch kind
	Variables        map[string]any         // fully composed task variables (no "workspace")
	Priority         string
}
type GroupTaskStarter interface { StartGroupTask(ctx context.Context, req GroupTaskStartRequest) error } // *Engine satisfies
func (e *Engine) StartGroupTask(ctx context.Context, req GroupTaskStartRequest) error
var ErrInvalidGroupTaskRequest = errors.New("invalid group task start request") // ADDITION (backward-compatible), see Deviations

// domain/engine/engine.go (internal, used by StartGroupTask)
type triggerSpec struct { workflowName string; variables map[string]any; priority, departmentOverride string; instanceID uuid.UUID; link *instance.SpawnLink }
func (e *Engine) trigger(ctx context.Context, spec triggerSpec) (uuid.UUID, bool, error)
func (e *Engine) createAndEnqueue(ctx context.Context, inst *instance.Instance) (bool, error)
func triggerOrigin(ctx context.Context) (instance.TriggerSource, *uuid.UUID)
```

`StartGroupTask` contract (pinned by tests):
1. `req.validate()` rejects an incomplete request (nil reserved id / parent / group run, empty node / key, unknown kind, kind/target mismatch, a reserved variable such as `"workspace"`) with `ErrInvalidGroupTaskRequest` BEFORE any read or write.
2. The caller must inject the parent OWNER's reconstructed identity on ctx (no identity ⇒ `ErrInvalidGroupTaskRequest`; an identity that is not the parent's owner ⇒ `identity.ErrForbidden`).
3. Idempotent: if a row already exists under `TaskInstanceID` and carries the same group run + task key + owner, nothing is created; a still-`queued` row is re-offered to the queue evaluator via `QueueManager.NotifyCapacityFreed` (never a second `Enqueue`); `nil` is returned. A row with that id belonging to another task ⇒ `ErrInvalidGroupTaskRequest`. A lookup storage error (anything other than `instance.ErrNotFound`) fails closed.
4. Depth = `parent.SpawnDepth + 1` checked against `instance.MaxSpawnDepth` (`ErrSpawnDepthExceeded`), read from the PERSISTED parent.
5. Workflow kind: `trigger` with `instanceID = TaskInstanceID`, the group-task link (`Mode = SpawnModeGroupTask`, depth, group fields) and `Variables` deep-copied — so owner = ctx identity, visibility gate, archived refusal, `checkGroupSupport`, required-input/default seeding, visibility/department stamping and priority `req.Priority` are all the trigger pipeline's.
6. Branch kind: the entry must be a node of the parent's snapshot AND the target of a `task` edge from `ParentNodeID`; `checkGroupSupport` runs on the parent snapshot; the child gets `WorkflowSnapshot = DeepCopyMap(parent.WorkflowSnapshot)` (never reloaded by name), `CurrentNodeID = BranchEntry`, `Variables = DeepCopyMap(req.Variables)`, and `OwnerID/WorkflowID/WorkflowName/Department/Visibility/Priority` from the parent.
7. Both kinds: `State queued`, `SpawnMode group_task`, `ParentInstanceID/ParentNodeID`, `GroupRunID/GroupTaskKey/GroupTaskKind` stamped in the SAME `Instance` literal as creation, one `instance.created` event, one `Enqueue`.

## Wiring & data provenance

- **Composition root:** **P2-T14** (`cmd/arbitron/main.go:initDomain`) passes the already-constructed `eng` (`*engine.Engine`, built by `engine.NewEngine` in `initDomain`) as the `GroupTaskStarter` argument of `engine.NewGroupLauncher` (P2-T8). The method needs no wiring of its own; its dependencies are the engine's existing fields (`workflows`, `instances`, `users`, `publishers`, `resolver`, `queueManager`, `eventBus`) plus `groupSupport` set by `eng.SetGroupSupport(engine.GroupSupportResults)` (P2-T14).
- **Producers of every input:**
  - `req.TaskInstanceID`, `GroupRunID`, `TaskKey`, `Kind`, `ChildWorkflow`/`BranchEntry` ← `GroupLauncher.Launch` (P2-T8) from the roster it persisted with `GroupRunRepository.CreateWithTasks` (P2-T3) and `workflow.ParseGroupConfig` (P1-T1).
  - `req.Variables` ← `engine.ComposeTaskVariables` (P2-T8) over the start snapshot of the parent's live store (`engine.go:868` `variable.FromSnapshotWithNodeIDs`).
  - `req.Priority` ← `string(parent.Priority)` in `GroupLauncher.Launch`.
  - Owner identity on ctx ← `GroupLauncher` via `IdentityResolver.IdentityForUser(parent.OwnerID)` = `cmd/arbitron/main.go:userIdentityResolver.IdentityForUser` (main.go:1455, the same value the spawn executor uses at main.go:926/946).
  - `parent.SpawnDepth`, `WorkflowSnapshot`, `OwnerID`, `Department`, `Visibility`, `Priority` ← `instance.InstanceRepository.Get` (`adapter/sqlite/instance_repo.go:136`).
  - Child workflow definition/visibility ← `workflow.LoadVisibleFiltered` through `e.workflows` and `e.sharedVisibilityFilter(ctx)`.
- **Provenance probes:** `TestStartGroupTask_WorkflowKind_InvisibleChildIsNotFound` fails if the workflow-kind path stops using the ctx identity's visibility gate; `TestStartGroupTask_RequiresTheParentOwnersIdentity` fails if the owner identity input is absent or ignored; `TestStartGroupTask_BranchKind_RunsTheParentsSnapshotFromItsEntry` fails if the branch path reloads the definition by name instead of copying the parent's persisted snapshot.

**Deviations (backward-compatible refinements of the contract):**
- Adds the sentinel `ErrInvalidGroupTaskRequest` (the contract names no sentinel for an incomplete request).
- "Re-enqueue only if still queued" is implemented as `QueueManager.NotifyCapacityFreed`, not a second `Enqueue`: `queue.Manager.Enqueue` (`domain/queue/manager.go:153-171`) calls `StartInstance` without a queued→running compare-and-set, so a second `Enqueue` racing the evaluator could start one run twice. A queued row is already in the queue; nudging the evaluator re-offers it safely.

---

### Task 7: Engine.StartGroupTask

**Files:**
- Modify: `domain/engine/engine.go:372-586` — replace `TriggerWithDepartment` (doc comment through closing brace, including the `checkGroupSupport` guard P1-T9 inserted right after the visibility-gated load) with the wrapper + `triggerSpec` + `trigger` + `createAndEnqueue` + `triggerOrigin` below.
- Create: `domain/engine/engine_group_task.go`
- Test: `domain/engine/engine_group_task_test.go` (package `engine_test`; reuses `newScopeAwareRepo`/`scopeAwareRepo.put` and `ctxAs` from `engine_trigger_scope_test.go:20-131`, `fakePubLookup`/`ctxWithDept` from `engine_trigger_visibility_test.go:16-37`, `newArchiveResolver` from `engine_archived_trigger_test.go:17-33`, and `newMockInstanceRepo`, `newMockNodeExecutionRepo`, `newMockSnapshotRepo`, `newMockUserRepo`, `newMockEventBus`, `newMockQueueManager`, `mockQueueRepo` from `engine_test.go:95-725`).

#### Cycle 1 — behaviour-preserving split of the trigger pipeline

- [x] Step 1: Establish the green baseline the refactor must keep.

```bash
go test ./domain/engine/ -run 'TestTrigger|TestEngine_Trigger|TestEngineSpawn|TestEngineChildInputs|TestEngine_WorkflowSnapshot|TestEngine_Rerun|TestGroupSupport' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```

- [x] Step 2: Refactor. In `domain/engine/engine.go`, replace the whole `TriggerWithDepartment` function (its doc comment at line 372 through its closing brace at line 586 in the pre-P1-T9 file; P1-T9 added its group-support guard inside it) with the following. Every existing comment is preserved; the only behavioural additions are the reserved id, the explicit link, and the group-task fields, none of which a legacy caller can reach.

```go
// TriggerWithDepartment loads a workflow, validates inputs, stamps the run's
// visibility+department (applying the optional validated per-trigger department
// override), creates the instance, and enqueues it. departmentOverride is "" for
// the default Trigger entrypoint; when non-empty it must be one of the caller's
// effective departments (else ErrInvalidDepartmentOverride and no instance).
func (e *Engine) TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority string, departmentOverride string) (uuid.UUID, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	spec := triggerSpec{
		workflowName:       workflowName,
		variables:          variables,
		priority:           priority,
		departmentOverride: departmentOverride,
	}
	// The parent/child spawn link rides the context (Engine.Spawn sets it via
	// instance.WithSpawnLink). An INCOMPLETE link is ignored by trigger, exactly
	// as before.
	if l, ok := instance.SpawnLinkFromContext(ctx); ok {
		spec.link = &l
	}
	return e.trigger(ctx, spec)
}

// triggerSpec is everything one run creation needs. TriggerWithDepartment
// builds it from its arguments and the context-carried spawn link;
// StartGroupTask builds it with a RESERVED instance id and a group-task link,
// so a workflow-kind group task is created through the identical
// owner / visibility / archive / input / stamping pipeline as any trigger.
type triggerSpec struct {
	workflowName       string
	variables          map[string]any
	priority           string
	departmentOverride string
	// instanceID is the id the run is created under; uuid.Nil mints a fresh one.
	instanceID uuid.UUID
	// link is the parent link to stamp; nil or incomplete creates a root run.
	link *instance.SpawnLink
}

// trigger is the body TriggerWithDepartment always had, parameterized by spec.
func (e *Engine) trigger(ctx context.Context, spec triggerSpec) (uuid.UUID, bool, error) {
	workflowName := spec.workflowName
	variables := spec.variables

	// Resolve the triggering owner: the context identity for REST/MCP triggers,
	// or the default user for system triggers (scheduler/webhook) that carry no
	// identity. The workflow is resolved across the owner's visible scopes
	// (private first, then shared) so a shared workflow runs directly with no
	// private copy.
	owner, ok := triggerOwner(ctx, e.users)
	if !ok {
		return uuid.Nil, false, fmt.Errorf("failed to resolve triggering user")
	}

	// Gate the shared read path by visibility. The owner above may be the default
	// system user; gating keys on the CONTEXT identity (a REST/MCP caller). A
	// system trigger (no context identity) skips gating — a scheduler/webhook
	// trigger already passed authorization at registration time.
	filter := e.sharedVisibilityFilter(ctx)
	wf, scope, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, filter)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to load workflow: %w", err)
	}

	// P1-T9's group-support guard: refuse a group-bearing workflow this server
	// cannot execute, right after the visibility-gated load and before anything
	// is created. (Keep this statement exactly where P1-T9 placed it.)
	if err := checkGroupSupport(wf, e.GroupSupportLevel()); err != nil {
		return uuid.Nil, false, err
	}

	// Refuse to trigger an archived workflow. IsArchived is part of the resolver
	// contract (no runtime type assertion), so the check engages whenever a
	// resolver is wired (the SQLite registry; local mode has no resolver and so
	// cannot — and need not — block). It runs AFTER the visibility-gated load so a
	// caller who cannot see the workflow still gets not-found (existence is not
	// leaked via the archived path), and BEFORE any instance is built so an
	// archived trigger creates nothing.
	//
	// FAIL CLOSED: the archived flag is a deliberate "do not run this" invariant,
	// so an IsArchived lookup error refuses the trigger rather than running an
	// possibly-archived workflow. A transient registry outage blocks the trigger
	// (surfaced loudly) instead of silently defeating the invariant.
	if e.resolver != nil {
		archived, aerr := e.resolver.IsArchived(ctx, workflowName)
		if aerr != nil {
			return uuid.Nil, false, fmt.Errorf("checking archived state of %q: %w", workflowName, aerr)
		}
		if archived {
			return uuid.Nil, false, fmt.Errorf("%w: %s", workflow.ErrArchived, workflowName)
		}
	}

	// Stamp the run's audience from the resolved publication (private when the
	// workflow has no publication record), applying the validated per-trigger
	// department override. A system trigger (no context identity) yields a
	// zero-value identity whose Departments is nil, so a public/department run is
	// stamped with an empty attribution department. An invalid override fails the
	// trigger before any instance is created.
	vis, pubDept := e.resolvePublication(ctx, scope, workflowName)
	callerID, _ := identity.FromContext(ctx)
	st, stErr := resolveStamp(callerID, vis, pubDept, spec.departmentOverride)
	if stErr != nil {
		return uuid.Nil, false, stErr
	}

	// Emit admin.override when admin status was the SOLE reason the caller could
	// access the workflow. This consults the ONE canonical sole-reason predicate
	// (workflow.AdminOverrode) the read paths also use, so the override condition —
	// which keeps the publisher branch (an admin who PUBLISHED the workflow sees it
	// for a non-admin reason) — cannot drift from the read path (PR #21).
	// Best-effort and nil-safe: identical contract to all other audit paths.
	if e.auditRecorder != nil && e.triggerAdminOverrode(ctx, scope, workflowName, callerID) {
		e.auditRecorder.Record(ctx, AuditActionAdminOverride, workflowName, `{"action":"trigger"}`)
	}

	// Validate inputs.
	vars := variable.NewStore()
	for _, inp := range wf.Inputs {
		val, provided := variables[inp.Name]
		if !provided || val == nil {
			if inp.Required {
				return uuid.Nil, false, fmt.Errorf("missing required input: %s", inp.Name)
			}
			if inp.Default != nil {
				if err := vars.SetInput(inp.Name, inp.Default); err != nil {
					return uuid.Nil, false, fmt.Errorf("seed input default %q: %w", inp.Name, err)
				}
			}
		} else {
			if err := vars.SetInput(inp.Name, val); err != nil {
				return uuid.Nil, false, fmt.Errorf("seed input %q: %w", inp.Name, err)
			}
		}
	}

	// Also set any extra variables that don't match input definitions.
	// SECURITY: this path accepts arbitrary caller-supplied keys; SetInput's
	// reserved-name guard prevents writing to engine-seeded variables like
	// "workspace".
	for k, v := range variables {
		if _, exists := vars.Get(k); !exists {
			if err := vars.SetInput(k, v); err != nil {
				return uuid.Nil, false, fmt.Errorf("seed extra variable %q: %w", k, err)
			}
		}
	}

	// Snapshot the workflow definition as a JSON-serializable map.
	wfBytes, err := json.Marshal(wf)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to snapshot workflow: %w", err)
	}
	var wfSnapshot map[string]any
	if err := json.Unmarshal(wfBytes, &wfSnapshot); err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to parse workflow snapshot: %w", err)
	}

	// OwnerID is the triggering user resolved above (the context identity, or the
	// default user for system triggers). A shared workflow therefore runs in the
	// triggering user's queue, independent of the publisher.
	ownerID := owner

	now := time.Now()
	snap := vars.Snapshot()
	origVars := make(map[string]any, len(snap))
	for k, v := range snap {
		origVars[k] = v
	}

	// Stamp run provenance from the trigger context. Manual REST/MCP triggers
	// carry no origin and default to OriginManual; the two schedule trigger sites
	// (cron fireOne, run-now Runner.Fire) set it via instance.WithOrigin. The
	// engine reads it WITHOUT importing the schedule package, the same
	// one-directional edge identity uses.
	triggerSource, scheduleID := triggerOrigin(ctx)

	// Stamp the parent/child link from the spec. It is stamped into the SAME
	// Instance literal below — never as a follow-up write. This trigger path
	// creates AND enqueues, so a link written after enqueue could lose a race
	// with a fast child reaching a terminal state, leaving its waiting parent
	// with nothing to match. An INCOMPLETE link is ignored entirely rather than
	// partially persisted. A group-task link additionally carries the group run
	// and task key, stamped in the same literal for the same reason.
	var parentInstanceID *uuid.UUID
	var parentNodeID *string
	var spawnMode instance.SpawnMode
	var groupRunID *uuid.UUID
	var groupTaskKey *string
	var groupTaskKind instance.GroupTaskKind
	// spawnDepth stays 0 for a root run, which is exactly what a root's depth is.
	spawnDepth := 0
	if l := spec.link; l != nil && l.Valid() {
		p := l.ParentInstanceID
		n := l.ParentNodeID
		parentInstanceID = &p
		parentNodeID = &n
		spawnMode = l.Mode
		spawnDepth = l.SpawnDepth
		if l.Mode == instance.SpawnModeGroupTask {
			g := l.GroupRunID
			k := l.GroupTaskKey
			groupRunID = &g
			groupTaskKey = &k
			groupTaskKind = l.GroupTaskKind
		}
	}

	// Resolve the stable workflow id (name->id) so the run carries an identity
	// reference that survives a later rename/edit. The registry is authoritative;
	// when it is unwired or has no row for this name, fall back to the id carried
	// in the loaded definition (the lazy backfill stamps it onto the folder), or
	// uuid.Nil for a fully legacy workflow. Resolution NEVER fails the trigger —
	// WorkflowName + WorkflowSnapshot remain the display + execution authority.
	workflowID := wf.ID
	if e.resolver != nil {
		if rid, rerr := e.resolver.ResolveID(ctx, workflowName); rerr == nil {
			workflowID = rid
		}
	}

	// A group task is created under the id its roster RESERVED before enqueue;
	// every other trigger mints a fresh one.
	id := spec.instanceID
	if id == uuid.Nil {
		id = uuid.New()
	}

	inst := &instance.Instance{
		ID:                id,
		OwnerID:           ownerID,
		WorkflowID:        workflowID,
		WorkflowName:      workflowName,
		WorkflowSnapshot:  wfSnapshot,
		Department:        st.Department,
		Visibility:        st.Visibility,
		TriggerSource:     triggerSource,
		ScheduleID:        scheduleID,
		ParentInstanceID:  parentInstanceID,
		ParentNodeID:      parentNodeID,
		SpawnMode:         spawnMode,
		SpawnDepth:        spawnDepth,
		GroupRunID:        groupRunID,
		GroupTaskKey:      groupTaskKey,
		GroupTaskKind:     groupTaskKind,
		State:             instance.StateQueued,
		Priority:          instance.Priority(spec.priority),
		Variables:         snap,
		OriginalVariables: origVars,
		QueuedAt:          &now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	started, err := e.createAndEnqueue(ctx, inst)
	if err != nil {
		return uuid.Nil, false, err
	}
	return inst.ID, started, nil
}

// createAndEnqueue is the tail every run-creation path shares: persist the
// row, announce it, and hand it to the queue. The error strings are the ones
// TriggerWithDepartment has always returned.
func (e *Engine) createAndEnqueue(ctx context.Context, inst *instance.Instance) (bool, error) {
	if err := e.instances.Create(ctx, inst); err != nil {
		return false, fmt.Errorf("failed to create instance: %w", err)
	}

	e.eventBus.Publish(inst.ID, Event{
		Type:      "instance.created",
		Timestamp: inst.CreatedAt,
		Data:      map[string]any{"workflowName": inst.WorkflowName},
	})

	started, err := e.queueManager.Enqueue(ctx, inst.ID, inst.OwnerID)
	if err != nil {
		return false, fmt.Errorf("failed to enqueue instance: %w", err)
	}
	return started, nil
}

// triggerOrigin reads the run provenance carried on ctx (instance.WithOrigin),
// defaulting to a manual trigger with no schedule.
func triggerOrigin(ctx context.Context) (instance.TriggerSource, *uuid.UUID) {
	if o, ok := instance.OriginFromContext(ctx); ok {
		return o.Source, o.ScheduleID
	}
	return instance.OriginManual, nil
}
```

- [x] Step 3: Run the same baseline plus the whole package; it must stay green.

```bash
gofmt -l domain/engine/engine.go
go test ./domain/engine/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` prints nothing.)

- [x] Step 4: Commit the refactor on its own.

```bash
git add domain/engine/engine.go
git commit -m "refactor(engine): split trigger pipeline into trigger(spec) and createAndEnqueue" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 2 — StartGroupTask

- [x] Step 5: Write the failing tests. Create `domain/engine/engine_group_task_test.go`:

```go
package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinelInstanceRepo wraps the shared mock so a miss wraps instance.ErrNotFound,
// exactly as the SQLite repository does (adapter/sqlite/instance_repo.go:141-143).
// StartGroupTask distinguishes "no row under the reserved id yet" from a storage
// failure; the plain mock's unwrapped miss would look like the latter.
type sentinelInstanceRepo struct{ *mockInstanceRepo }

func (r sentinelInstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	_, ok := r.instances[id]
	injected := r.getErr
	r.mu.Unlock()
	if injected == nil && !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return r.mockInstanceRepo.Get(ctx, id)
}

var _ instance.InstanceRepository = sentinelInstanceRepo{}

// groupTaskHarness wires a real Engine whose Enqueue records and leaves the run
// queued, so the created row is inspectable and nothing executes.
type groupTaskHarness struct {
	repo      *scopeAwareRepo
	instances *mockInstanceRepo
	bus       *mockEventBus
	qm        *mockQueueManager
	engine    *engine.Engine

	mu             sync.Mutex
	enqueued       []uuid.UUID
	enqueuedOwners []uuid.UUID
}

func newGroupTaskHarness(t *testing.T) *groupTaskHarness {
	t.Helper()
	repo := newScopeAwareRepo()
	instRepo := newMockInstanceRepo()
	bus := newMockEventBus()
	qm := newMockQueueManager()
	eng := engine.NewEngine(repo, sentinelInstanceRepo{instRepo}, newMockNodeExecutionRepo(), newMockSnapshotRepo(),
		newMockUserRepo(), map[workflow.NodeType]engine.NodeExecutor{}, bus, qm, &mockQueueRepo{instRepo: instRepo}, "")
	eng.SetGroupSupport(engine.GroupSupportResults)

	h := &groupTaskHarness{repo: repo, instances: instRepo, bus: bus, qm: qm, engine: eng}
	qm.mu.Lock()
	qm.enqueueFn = func(_ context.Context, id uuid.UUID, owner uuid.UUID) (bool, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.enqueued = append(h.enqueued, id)
		h.enqueuedOwners = append(h.enqueuedOwners, owner)
		return false, nil
	}
	qm.mu.Unlock()
	return h
}

func (h *groupTaskHarness) enqueuedIDs() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uuid.UUID(nil), h.enqueued...)
}

// gtParentWorkflow is a parent whose group node "g" fans out to branch bodies
// a1 and b1. "stray" exists in the snapshot but is not a task target.
func gtParentWorkflow() workflow.Workflow {
	return workflow.Workflow{
		Name: "parent-wf",
		Nodes: []workflow.Node{
			{ID: "g", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
				GatherMode:         workflow.GroupGatherResults,
			}}},
			{ID: "a1", Type: workflow.NodeTypeScript},
			{ID: "b1", Type: workflow.NodeTypeScript},
			{ID: "after", Type: workflow.NodeTypeScript},
			{ID: "fail", Type: workflow.NodeTypeScript},
			{ID: "stray", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "g", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "g", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "g", Target: "after", Output: workflow.GroupPortSuccess},
			{Source: "g", Target: "fail", Output: workflow.GroupPortFailure},
		},
	}
}

func gtSnapshot(t *testing.T, wf workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(b, &snap))
	return snap
}

func (h *groupTaskHarness) seedParent(t *testing.T, owner uuid.UUID, depth int) *instance.Instance {
	t.Helper()
	wf := gtParentWorkflow()
	g := "g"
	p := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowID: uuid.New(), WorkflowName: wf.Name,
		WorkflowSnapshot: gtSnapshot(t, wf), Department: "engineering", Visibility: instance.VisibilityDepartment,
		State: instance.StateRunning, Priority: instance.PriorityHigh, SpawnDepth: depth, CurrentNodeID: &g,
	}
	require.NoError(t, h.instances.Create(context.Background(), p))
	return p
}

func gtChildWorkflow(name string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name,
		Inputs: []workflow.InputField{
			{Name: "repo", Type: "string", Required: true},
			{Name: "dryRun", Type: "boolean", Default: false},
		},
		Nodes: []workflow.Node{{ID: "c1", Type: workflow.NodeTypeScript, Entrypoint: true}},
	}
}

func gtWorkflowReq(parent *instance.Instance) engine.GroupTaskStartRequest {
	return engine.GroupTaskStartRequest{
		TaskInstanceID: uuid.New(), ParentInstanceID: parent.ID, ParentNodeID: "g", GroupRunID: uuid.New(),
		TaskKey: "review", Kind: instance.GroupTaskKindWorkflow, ChildWorkflow: "child-wf",
		Variables: map[string]any{"repo": "acme/widgets", "ticket": "T-1"},
		Priority:  string(instance.PriorityHigh),
	}
}

func gtBranchReq(parent *instance.Instance) engine.GroupTaskStartRequest {
	return engine.GroupTaskStartRequest{
		TaskInstanceID: uuid.New(), ParentInstanceID: parent.ID, ParentNodeID: "g", GroupRunID: uuid.New(),
		TaskKey: "a", Kind: instance.GroupTaskKindBranch, BranchEntry: "a1",
		Variables: map[string]any{"repo": "acme/widgets", "cfg": map[string]any{"depth": map[string]any{"leaf": "v"}}},
		Priority:  string(instance.PriorityLow),
	}
}

// ---------- happy path ----------

func TestStartGroupTask_WorkflowKind_CreatesTheLinkedChildUnderTheReservedID(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	parent := h.seedParent(t, owner, 2)
	req := gtWorkflowReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	child := h.instances.get(req.TaskInstanceID)
	require.NotNil(t, child, "the row must be created under the RESERVED id")
	assert.Equal(t, owner, child.OwnerID)
	assert.Equal(t, "child-wf", child.WorkflowName)
	require.NotNil(t, child.ParentInstanceID)
	assert.Equal(t, parent.ID, *child.ParentInstanceID)
	require.NotNil(t, child.ParentNodeID)
	assert.Equal(t, "g", *child.ParentNodeID)
	assert.Equal(t, instance.SpawnModeGroupTask, child.SpawnMode)
	require.NotNil(t, child.GroupRunID)
	assert.Equal(t, req.GroupRunID, *child.GroupRunID)
	require.NotNil(t, child.GroupTaskKey)
	assert.Equal(t, "review", *child.GroupTaskKey)
	assert.Equal(t, instance.GroupTaskKindWorkflow, child.GroupTaskKind)
	assert.Equal(t, 3, child.SpawnDepth, "depth derives from the parent's persisted depth")
	assert.Equal(t, instance.PriorityHigh, child.Priority)
	assert.Equal(t, instance.StateQueued, child.State)
	assert.Nil(t, child.CurrentNodeID, "a workflow task starts at its own entrypoint")
	assert.Equal(t, instance.OriginManual, child.TriggerSource)
	assert.Equal(t, "acme/widgets", child.Variables["repo"])
	assert.Equal(t, "T-1", child.Variables["ticket"], "base variables beyond the child's declared inputs ride along")
	assert.Equal(t, false, child.Variables["dryRun"], "declared defaults still apply")
	assert.Equal(t, instance.VisibilityPrivate, child.Visibility, "a private child workflow stamps a private run, as Trigger does")
	assert.NotNil(t, child.WorkflowSnapshot)

	assert.Equal(t, []uuid.UUID{req.TaskInstanceID}, h.enqueuedIDs())
	assert.Equal(t, []uuid.UUID{owner}, h.enqueuedOwners)
	assert.Len(t, h.bus.eventsOfType(req.TaskInstanceID, "instance.created"), 1)
}

func TestStartGroupTask_BranchKind_RunsTheParentsSnapshotFromItsEntry(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	child := h.instances.get(req.TaskInstanceID)
	require.NotNil(t, child)
	assert.Equal(t, parent.WorkflowSnapshot, child.WorkflowSnapshot, "the branch runs the parent's definition snapshot")
	require.NotNil(t, child.CurrentNodeID)
	assert.Equal(t, "a1", *child.CurrentNodeID, "Engine.run honours a preset cursor: the branch starts at its entry")
	assert.Equal(t, owner, child.OwnerID)
	assert.Equal(t, parent.WorkflowID, child.WorkflowID)
	assert.Equal(t, "parent-wf", child.WorkflowName)
	assert.Equal(t, "engineering", child.Department)
	assert.Equal(t, instance.VisibilityDepartment, child.Visibility)
	assert.Equal(t, instance.PriorityHigh, child.Priority, "branch priority is the PARENT's, not the request's")
	assert.Equal(t, instance.SpawnModeGroupTask, child.SpawnMode)
	assert.Equal(t, instance.GroupTaskKindBranch, child.GroupTaskKind)
	require.NotNil(t, child.GroupTaskKey)
	assert.Equal(t, "a", *child.GroupTaskKey)
	require.NotNil(t, child.GroupRunID)
	assert.Equal(t, req.GroupRunID, *child.GroupRunID)
	assert.Equal(t, 1, child.SpawnDepth)
	assert.Equal(t, instance.StateQueued, child.State)
	assert.Equal(t, req.Variables, child.Variables)
	assert.Equal(t, child.Variables, child.OriginalVariables)
	assert.Equal(t, []uuid.UUID{req.TaskInstanceID}, h.enqueuedIDs())
	assert.Len(t, h.bus.eventsOfType(req.TaskInstanceID, "instance.created"), 1)

	// Deep independence: neither the request map nor the child's snapshot shares
	// state with the parent.
	req.Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"] = "mutated"
	assert.Equal(t, "v", child.Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])
	child.WorkflowSnapshot["nodes"].([]any)[0].(map[string]any)["id"] = "mutated"
	assert.Equal(t, "g", h.instances.get(parent.ID).WorkflowSnapshot["nodes"].([]any)[0].(map[string]any)["id"])
}

// ---------- error / failure ----------

func TestStartGroupTask_DepthCeiling(t *testing.T) {
	for _, kind := range []instance.GroupTaskKind{instance.GroupTaskKindBranch, instance.GroupTaskKindWorkflow} {
		t.Run(string(kind), func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, instance.MaxSpawnDepth)
			req := gtBranchReq(parent)
			if kind == instance.GroupTaskKindWorkflow {
				req = gtWorkflowReq(parent)
			}

			err := h.engine.StartGroupTask(ctxAs(owner), req)
			require.ErrorIs(t, err, instance.ErrSpawnDepthExceeded)
			assert.Nil(t, h.instances.get(req.TaskInstanceID), "the refusal lands before the row")
			assert.Empty(t, h.enqueuedIDs())
		})
	}
}

func TestStartGroupTask_AllowsTheDeepestPermittedGeneration(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, instance.MaxSpawnDepth-1)
	req := gtBranchReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
	assert.Equal(t, instance.MaxSpawnDepth, h.instances.get(req.TaskInstanceID).SpawnDepth)
}

func TestStartGroupTask_WorkflowKind_InvisibleChildIsNotFound(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Shared(), gtChildWorkflow("child-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"child-wf": {Publisher: uuid.New(), Visibility: workflow.VisibilityDepartment, Department: "sales"},
	}})
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)

	err := h.engine.StartGroupTask(ctxWithDept(owner, "engineering"), req)
	require.ErrorIs(t, err, workflow.ErrNotFound, "a task must never learn that a workflow its owner cannot see exists")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_WorkflowKind_ArchivedChildRefused(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	res := newArchiveResolver()
	res.register("child-wf", uuid.New())
	res.markArchived("child-wf")
	h.engine.SetWorkflowResolver(res)
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, workflow.ErrArchived)
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_WorkflowKind_MissingRequiredChildInput(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)
	req.Variables = map[string]any{"ticket": "T-1"}

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required input: repo")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_WorkflowKind_GroupSupportGuardApplies(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	files := gtParentWorkflow()
	files.Name = "child-wf"
	files.Nodes[0].Config.Group.GatherMode = workflow.GroupGatherResultsAndFiles
	files.Nodes[0].Config.Group.ResolverProfile = "resolver"
	h.repo.put(workflow.Private(owner), &files)
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, engine.ErrGroupExecutionUnsupported,
		"a child workflow with a file-gathering group is refused on a results-only server, exactly as Trigger refuses it")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
}

func TestStartGroupTask_BranchKind_GroupSupportGuardApplies(t *testing.T) {
	h := newGroupTaskHarness(t)
	h.engine.SetGroupSupport(engine.GroupSupportNone)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, engine.ErrGroupExecutionUnsupported)
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_BranchEntryMustBeATaskTargetOfTheGroupNode(t *testing.T) {
	cases := []struct {
		entry string
		want  string
	}{
		{"missing", "is not a node of the parent's workflow snapshot"},
		{"stray", `is not the target of a "task" edge from group node "g"`},
		{"after", `is not the target of a "task" edge from group node "g"`},
	}
	for _, c := range cases {
		t.Run(c.entry, func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			parent := h.seedParent(t, owner, 0)
			req := gtBranchReq(parent)
			req.BranchEntry = c.entry

			err := h.engine.StartGroupTask(ctxAs(owner), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
			assert.Nil(t, h.instances.get(req.TaskInstanceID))
			assert.Empty(t, h.enqueuedIDs())
		})
	}
}

func TestStartGroupTask_RequiresTheParentOwnersIdentity(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)

	req := gtBranchReq(parent)
	err := h.engine.StartGroupTask(context.Background(), req)
	require.ErrorIs(t, err, engine.ErrInvalidGroupTaskRequest, "no identity on ctx: never fall back to the default user")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))

	req = gtBranchReq(parent)
	err = h.engine.StartGroupTask(ctxAs(uuid.New()), req)
	require.ErrorIs(t, err, identity.ErrForbidden, "a task may only be created under the parent owner's identity")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_UnreadableParentFailsClosed(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	req := engine.GroupTaskStartRequest{
		TaskInstanceID: uuid.New(), ParentInstanceID: uuid.New(), ParentNodeID: "g", GroupRunID: uuid.New(),
		TaskKey: "a", Kind: instance.GroupTaskKindBranch, BranchEntry: "a1",
	}

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, instance.ErrNotFound)
	assert.Empty(t, h.instances.all())
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_ReservedIDLookupStorageErrorFailsClosed(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	h.instances.getErr = errors.New("database is locked")
	req := gtBranchReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is locked", "a storage failure must not be mistaken for 'no row yet'")
	h.instances.getErr = nil
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

// ---------- edge cases ----------

func TestStartGroupTask_IncompleteRequestRejectedBeforeAnyRow(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *engine.GroupTaskStartRequest)
	}{
		{"no reserved id", func(r *engine.GroupTaskStartRequest) { r.TaskInstanceID = uuid.Nil }},
		{"no parent", func(r *engine.GroupTaskStartRequest) { r.ParentInstanceID = uuid.Nil }},
		{"no parent node", func(r *engine.GroupTaskStartRequest) { r.ParentNodeID = "" }},
		{"no group run", func(r *engine.GroupTaskStartRequest) { r.GroupRunID = uuid.Nil }},
		{"no task key", func(r *engine.GroupTaskStartRequest) { r.TaskKey = "" }},
		{"unknown kind", func(r *engine.GroupTaskStartRequest) { r.Kind = instance.GroupTaskKind("spawn") }},
		{"workflow kind without a child workflow", func(r *engine.GroupTaskStartRequest) {
			r.Kind = instance.GroupTaskKindWorkflow
			r.BranchEntry = ""
			r.ChildWorkflow = ""
		}},
		{"workflow kind with a branch entry", func(r *engine.GroupTaskStartRequest) {
			r.Kind = instance.GroupTaskKindWorkflow
			r.ChildWorkflow = "child-wf"
		}},
		{"branch kind without an entry", func(r *engine.GroupTaskStartRequest) { r.BranchEntry = "" }},
		{"branch kind with a child workflow", func(r *engine.GroupTaskStartRequest) { r.ChildWorkflow = "child-wf" }},
		{"reserved workspace variable", func(r *engine.GroupTaskStartRequest) {
			r.Variables = map[string]any{"workspace": "/somewhere/else"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, 0)
			req := gtBranchReq(parent)
			c.mutate(&req)

			err := h.engine.StartGroupTask(ctxAs(owner), req)
			require.ErrorIs(t, err, engine.ErrInvalidGroupTaskRequest)
			assert.Len(t, h.instances.all(), 1, "only the parent row exists")
			assert.Empty(t, h.enqueuedIDs())
		})
	}
}

func TestStartGroupTask_IdempotentRecallCreatesAndEnqueuesOnce(t *testing.T) {
	for _, kind := range []instance.GroupTaskKind{instance.GroupTaskKindBranch, instance.GroupTaskKindWorkflow} {
		t.Run(string(kind), func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, 0)
			req := gtBranchReq(parent)
			if kind == instance.GroupTaskKindWorkflow {
				req = gtWorkflowReq(parent)
			}

			require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
			require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req), "a re-call (crash recovery, replay) is not an error")

			assert.Len(t, h.instances.all(), 2, "parent + exactly one task row")
			assert.Equal(t, []uuid.UUID{req.TaskInstanceID}, h.enqueuedIDs(), "never enqueued twice")
			assert.Equal(t, 1, h.qm.notifyCount(), "a still-queued row is re-offered to the queue evaluator")
			assert.Len(t, h.bus.eventsOfType(req.TaskInstanceID, "instance.created"), 1)
		})
	}
}

func TestStartGroupTask_RecallForAnAlreadyRunningTaskIsANoOp(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)
	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
	require.NoError(t, h.instances.UpdateState(context.Background(), req.TaskInstanceID, instance.StateRunning))

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
	assert.Equal(t, 0, h.qm.notifyCount(), "a row that already left the queue is not re-offered")
	assert.Equal(t, instance.StateRunning, h.instances.get(req.TaskInstanceID).State)
}

func TestStartGroupTask_ReservedIDBelongingToAnotherTaskIsRefused(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)
	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	other := req
	other.TaskKey = "b"
	other.BranchEntry = "b1"
	err := h.engine.StartGroupTask(ctxAs(owner), other)
	require.ErrorIs(t, err, engine.ErrInvalidGroupTaskRequest)
	assert.Equal(t, "a", *h.instances.get(req.TaskInstanceID).GroupTaskKey, "the existing row is untouched")
	assert.Len(t, h.enqueuedIDs(), 1)
}

// ---------- regression: legacy entry points are unchanged ----------

func TestTrigger_AfterRefactor_RootRunUnchanged(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))

	id, started, err := h.engine.Trigger(ctxAs(owner), "child-wf", map[string]any{"repo": "r"}, "normal")
	require.NoError(t, err)
	assert.False(t, started)
	assert.NotEqual(t, uuid.Nil, id, "a trigger mints a fresh id")
	inst := h.instances.get(id)
	require.NotNil(t, inst)
	assert.True(t, inst.IsRoot())
	assert.Equal(t, instance.SpawnMode(""), inst.SpawnMode)
	assert.Nil(t, inst.GroupRunID)
	assert.Nil(t, inst.GroupTaskKey)
	assert.Equal(t, instance.GroupTaskKind(""), inst.GroupTaskKind)
	assert.Equal(t, 0, inst.SpawnDepth)
	assert.Equal(t, []uuid.UUID{id}, h.enqueuedIDs())
	assert.Len(t, h.bus.eventsOfType(id, "instance.created"), 1)
}

func TestSpawn_AfterRefactor_LegacyLinkUnchanged(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	parent := h.seedParent(t, owner, 4)

	childID, err := h.engine.Spawn(ctxAs(owner), engine.SpawnRequest{
		ChildWorkflow: "child-wf", Variables: map[string]any{"repo": "r"}, Priority: "normal",
		Link: instance.SpawnLink{ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeWaitForChild},
	})
	require.NoError(t, err)
	child := h.instances.get(childID)
	require.NotNil(t, child)
	assert.Equal(t, instance.SpawnModeWaitForChild, child.SpawnMode)
	assert.Equal(t, 5, child.SpawnDepth)
	assert.Nil(t, child.GroupRunID, "a legacy spawn carries no group linkage")
	assert.Nil(t, child.GroupTaskKey)
	assert.Equal(t, instance.GroupTaskKind(""), child.GroupTaskKind)
}
```

- [x] Step 6: Run it, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestStartGroupTask|TestTrigger_AfterRefactor|TestSpawn_AfterRefactor' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/engine_group_task_test.go:139:31: undefined: engine.GroupTaskStartRequest
domain/engine/engine_group_task_test.go:179:29: h.engine.StartGroupTask undefined (type *engine.Engine has no field or method StartGroupTask)
domain/engine/engine_group_task_test.go:381:28: undefined: engine.ErrInvalidGroupTaskRequest
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 7: Implement. Create `domain/engine/engine_group_task.go`:

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupTaskStartRequest is everything StartGroupTask needs to create ONE
// parallel-group task child. TaskInstanceID is the id the group's roster
// RESERVED before anything was enqueued, which is what makes a task
// addressable (and recoverable) before it exists.
type GroupTaskStartRequest struct {
	TaskInstanceID   uuid.UUID
	ParentInstanceID uuid.UUID
	ParentNodeID     string
	GroupRunID       uuid.UUID
	TaskKey          string
	Kind             instance.GroupTaskKind
	// ChildWorkflow is the child workflow's name (workflow kind only).
	ChildWorkflow string
	// BranchEntry is the validated entry node of the branch body (branch kind only).
	BranchEntry string
	// Variables are the fully composed task variables. They never contain
	// "workspace": each task's own working area is seeded by its own run.
	Variables map[string]any
	// Priority is the parent's priority (workflow kind). Branch tasks always take
	// the parent's persisted priority.
	Priority string
}

// GroupTaskStarter creates one group task child. *Engine satisfies it.
type GroupTaskStarter interface {
	StartGroupTask(ctx context.Context, req GroupTaskStartRequest) error
}

var _ GroupTaskStarter = (*Engine)(nil)

// ErrInvalidGroupTaskRequest is returned when a group task request is
// incomplete or inconsistent, or the reserved id already belongs to another
// task. It is always returned BEFORE any row is created.
var ErrInvalidGroupTaskRequest = errors.New("invalid group task start request")

func (r GroupTaskStartRequest) validate() error {
	var problems []string
	if r.TaskInstanceID == uuid.Nil {
		problems = append(problems, "TaskInstanceID (the roster's reserved id) is unset")
	}
	if r.ParentInstanceID == uuid.Nil {
		problems = append(problems, "ParentInstanceID is unset")
	}
	if r.ParentNodeID == "" {
		problems = append(problems, "ParentNodeID is unset")
	}
	if r.GroupRunID == uuid.Nil {
		problems = append(problems, "GroupRunID is unset")
	}
	if r.TaskKey == "" {
		problems = append(problems, "TaskKey is unset")
	}
	switch r.Kind {
	case instance.GroupTaskKindWorkflow:
		if r.ChildWorkflow == "" {
			problems = append(problems, "a workflow task needs ChildWorkflow")
		}
		if r.BranchEntry != "" {
			problems = append(problems, "a workflow task must not name a BranchEntry")
		}
	case instance.GroupTaskKindBranch:
		if r.BranchEntry == "" {
			problems = append(problems, "a branch task needs BranchEntry")
		}
		if r.ChildWorkflow != "" {
			problems = append(problems, "a branch task must not name a ChildWorkflow")
		}
	default:
		problems = append(problems, fmt.Sprintf("Kind %q is neither %q nor %q",
			r.Kind, instance.GroupTaskKindBranch, instance.GroupTaskKindWorkflow))
	}
	var reserved []string
	for k := range r.Variables {
		if variable.ReservedInputNames[k] {
			reserved = append(reserved, k)
		}
	}
	sort.Strings(reserved)
	for _, k := range reserved {
		problems = append(problems, fmt.Sprintf("variable %q is engine-seeded and may not be supplied", k))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: task %q: %s", ErrInvalidGroupTaskRequest, r.TaskKey, strings.Join(problems, "; "))
	}
	return nil
}

// StartGroupTask creates one parallel-group task child under its reserved id
// and enqueues it.
//
// The caller (GroupLauncher) injects the parent OWNER's reconstructed identity
// on ctx, exactly as SpawnWorkflowExecutor.prepareAndSpawn does for Spawn; the
// identity is required and must be the parent's owner, so a task can never be
// created under anyone else's credentials or queue.
//
// IDEMPOTENT. A row that already exists under req.TaskInstanceID for the same
// group run and task is left alone (a still-queued one is re-offered to the
// queue evaluator) and nil is returned, so a re-call after a crash or a replay
// can never duplicate a child.
//
// Workflow kind reuses the trigger pipeline (owner from ctx, visibility gate,
// archived refusal, group-support guard, input validation, audience stamping)
// via triggerSpec. Branch kind copies the PARENT's definition snapshot and
// starts at the validated branch entry; it never reloads a definition by name.
func (e *Engine) StartGroupTask(ctx context.Context, req GroupTaskStartRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := req.validate(); err != nil {
		return err
	}
	caller, ok := identity.FromContext(ctx)
	if !ok {
		return fmt.Errorf("%w: task %q: the parent owner's reconstructed identity must be on the context",
			ErrInvalidGroupTaskRequest, req.TaskKey)
	}

	existing, err := e.instances.Get(ctx, req.TaskInstanceID)
	switch {
	case err == nil && existing != nil:
		return e.resumeExistingGroupTask(ctx, req, caller, existing)
	case err != nil && !errors.Is(err, instance.ErrNotFound):
		// A storage failure must never be mistaken for "not created yet": that
		// is exactly how a second child would be born.
		return fmt.Errorf("group task %q: look up reserved instance %s: %w", req.TaskKey, req.TaskInstanceID, err)
	}

	parent, err := e.instances.Get(ctx, req.ParentInstanceID)
	if err != nil {
		return fmt.Errorf("group task %q: load parent instance: %w", req.TaskKey, err)
	}
	if parent == nil {
		return fmt.Errorf("group task %q: parent instance %s: %w", req.TaskKey, req.ParentInstanceID, instance.ErrNotFound)
	}
	if caller.UserID != parent.OwnerID {
		return fmt.Errorf("group task %q: %w: the context identity %s is not the owner of parent run %s",
			req.TaskKey, identity.ErrForbidden, caller.UserID, parent.ID)
	}

	// The same runtime recursion bound Engine.Spawn enforces (engine_spawn.go:77-87),
	// derived from the parent's PERSISTED depth.
	depth := parent.SpawnDepth + 1
	if depth > instance.MaxSpawnDepth {
		return fmt.Errorf("group task %q: %w (depth %d, limit %d); the run tree is %d generations deep, "+
			"which usually means a spawn or group cycle",
			req.TaskKey, instance.ErrSpawnDepthExceeded, depth, instance.MaxSpawnDepth, parent.SpawnDepth)
	}

	link := instance.SpawnLink{
		ParentInstanceID: req.ParentInstanceID,
		ParentNodeID:     req.ParentNodeID,
		Mode:             instance.SpawnModeGroupTask,
		SpawnDepth:       depth,
		GroupRunID:       req.GroupRunID,
		GroupTaskKey:     req.TaskKey,
		GroupTaskKind:    req.Kind,
	}
	if !link.Valid() {
		return fmt.Errorf("%w: task %q: incomplete group-task link %+v", ErrInvalidGroupTaskRequest, req.TaskKey, link)
	}

	if req.Kind == instance.GroupTaskKindWorkflow {
		_, _, err := e.trigger(ctx, triggerSpec{
			workflowName: req.ChildWorkflow,
			variables:    variable.DeepCopyMap(req.Variables),
			priority:     req.Priority,
			instanceID:   req.TaskInstanceID,
			link:         &link,
		})
		if err != nil {
			return fmt.Errorf("group task %q: start child workflow %q: %w", req.TaskKey, req.ChildWorkflow, err)
		}
		return nil
	}
	return e.startBranchTask(ctx, req, parent, link)
}

// resumeExistingGroupTask is the idempotent path: the reserved row already
// exists. It is accepted only if it IS this task.
func (e *Engine) resumeExistingGroupTask(ctx context.Context, req GroupTaskStartRequest, caller identity.Identity, existing *instance.Instance) error {
	if existing.GroupRunID == nil || *existing.GroupRunID != req.GroupRunID ||
		existing.GroupTaskKey == nil || *existing.GroupTaskKey != req.TaskKey {
		return fmt.Errorf("%w: task %q: reserved instance id %s already belongs to a different run",
			ErrInvalidGroupTaskRequest, req.TaskKey, req.TaskInstanceID)
	}
	if existing.OwnerID != caller.UserID {
		return fmt.Errorf("group task %q: %w: the context identity %s does not own instance %s",
			req.TaskKey, identity.ErrForbidden, caller.UserID, existing.ID)
	}
	if existing.State == instance.StateQueued {
		// Already in the queue: re-offer it to the evaluator. A second Enqueue
		// could StartInstance a row the evaluator is starting concurrently.
		e.queueManager.NotifyCapacityFreed(ctx)
	}
	return nil
}

// startBranchTask creates a branch task: the parent's own definition, started
// at the validated branch entry, with the parent's ownership and audience.
func (e *Engine) startBranchTask(ctx context.Context, req GroupTaskStartRequest, parent *instance.Instance, link instance.SpawnLink) error {
	wf, nodeMap, _, err := parseWorkflowGraph(parent.WorkflowSnapshot)
	if err != nil {
		return fmt.Errorf("group task %q: parse the parent's workflow snapshot: %w", req.TaskKey, err)
	}
	if _, ok := nodeMap[req.BranchEntry]; !ok {
		return fmt.Errorf("group task %q: branch entry %q is not a node of the parent's workflow snapshot",
			req.TaskKey, req.BranchEntry)
	}
	if !isGroupTaskEdge(wf, req.ParentNodeID, req.BranchEntry) {
		return fmt.Errorf("group task %q: branch entry %q is not the target of a %q edge from group node %q in the parent's snapshot",
			req.TaskKey, req.BranchEntry, workflow.GroupPortTask, req.ParentNodeID)
	}
	if err := checkGroupSupport(wf, e.GroupSupportLevel()); err != nil {
		return fmt.Errorf("group task %q: %w", req.TaskKey, err)
	}

	source, scheduleID := triggerOrigin(ctx)
	parentID := link.ParentInstanceID
	parentNode := link.ParentNodeID
	runID := link.GroupRunID
	key := link.GroupTaskKey
	entry := req.BranchEntry
	vars := variable.DeepCopyMap(req.Variables)
	if vars == nil {
		vars = map[string]any{}
	}
	now := time.Now()

	inst := &instance.Instance{
		ID:                req.TaskInstanceID,
		OwnerID:           parent.OwnerID,
		WorkflowID:        parent.WorkflowID,
		WorkflowName:      parent.WorkflowName,
		WorkflowSnapshot:  variable.DeepCopyMap(parent.WorkflowSnapshot),
		Department:        parent.Department,
		Visibility:        parent.Visibility,
		TriggerSource:     source,
		ScheduleID:        scheduleID,
		ParentInstanceID:  &parentID,
		ParentNodeID:      &parentNode,
		SpawnMode:         instance.SpawnModeGroupTask,
		SpawnDepth:        link.SpawnDepth,
		GroupRunID:        &runID,
		GroupTaskKey:      &key,
		GroupTaskKind:     instance.GroupTaskKindBranch,
		State:             instance.StateQueued,
		Priority:          parent.Priority,
		Variables:         vars,
		OriginalVariables: variable.DeepCopyMap(vars),
		CurrentNodeID:     &entry,
		QueuedAt:          &now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if _, err := e.createAndEnqueue(ctx, inst); err != nil {
		return fmt.Errorf("group task %q: %w", req.TaskKey, err)
	}
	return nil
}

// isGroupTaskEdge reports whether the snapshot has a task edge from the group
// node to entry — the shape P1-T6 writes for every branch task.
func isGroupTaskEdge(wf *workflow.Workflow, groupNodeID, entry string) bool {
	for _, edge := range wf.Edges {
		if edge.Source == groupNodeID && edge.Target == entry && edge.Output == workflow.GroupPortTask {
			return true
		}
	}
	return false
}
```

- [x] Step 8: Run the new tests, then the whole package, gofmt and vet.

```bash
go test ./domain/engine/ -run 'TestStartGroupTask|TestTrigger_AfterRefactor|TestSpawn_AfterRefactor' -count=1 -v
go test ./domain/engine/ -count=1
gofmt -l domain/engine/engine.go domain/engine/engine_group_task.go domain/engine/engine_group_task_test.go
go vet ./domain/engine/
```
Expected:
```
--- PASS: TestStartGroupTask_WorkflowKind_CreatesTheLinkedChildUnderTheReservedID (0.00s)
--- PASS: TestStartGroupTask_BranchKind_RunsTheParentsSnapshotFromItsEntry (0.00s)
...
--- PASS: TestSpawn_AfterRefactor_LegacyLinkUnchanged (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 9: Run the dependent packages that call the trigger path (MCP, HTTP, schedule, main) to prove the refactor is invisible to them.

```bash
go test ./domain/... ./adapter/mcp/... ./adapter/http/... -count=1
```
Expected: every package `ok`.

- [x] Step 10: Commit.

Verification: baseline green before/after refactor; new tests failed on the missing API, then passed. Ordered self-review completed (spec, sink/provenance coverage, code quality). Tests additionally assert validation before reads and complete links at enqueue. Removing the workflow-variable deep copy made the sink test fail; mutation reverted. Focused tests (including `-race`), all domain/MCP/HTTP/command tests, gofmt and engine vet pass.

```bash
git add domain/engine/engine_group_task.go domain/engine/engine_group_task_test.go
git commit -m "feat(engine): StartGroupTask creates reserved-id branch and workflow group tasks idempotently" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
