# Parallel Workflow Gathering — Phase 2 Task 12: Subtree cancellation through terminal intermediates and after parent completion (P2-D)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make `Engine.Cancel` stop EVERY unfinished descendant of a run — including descendants that hang below an already-terminal intermediate (a completed branch, a completed fire-and-forget child) and descendants of a parent that has itself already completed or failed (a `finish_independently` loser) — without ever changing a terminal parent's own state or outcome, and make the orphan sweep find active runs stranded under ANY cancelled ancestor.
**Architecture:** The link port gains one traversal read, `SpawnLinkRepository.ListChildren` (every child with its state, terminal ones included); `Engine.cascadeCancel` walks it instead of `ListActiveChildren` and returns how many descendants it cancelled. `Engine.Cancel` keeps its order (parent first, detached cascade second) and adds one rule: a parent that authorizes but is already terminal still cascades, and the call succeeds iff at least one descendant was cancelled (otherwise the original `ErrCannotCancel` is returned). `InstanceRepo.ListOrphanedActiveChildren` becomes a recursive CTE that climbs ancestry through completed/failed rows until it meets a cancelled one.
**Tech Stack:** Go 1.25.5, SQLite (hand-written SQL, recursive CTE), testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Cancelling a parent stops its unfinished parallel work, including nested tasks and child workflows"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §5 "Cancellation, lifecycle, events" and Data Provenance row "Complete subtree cancellation, including terminal intermediates/terminal parent independent work" (PLANNED DEPENDENCY P2-D — resolved here).

**Durable terminal-parent intent (resolves the former escalation):** cancelling an already-terminal parent cannot write `cancelled` onto the parent (its outcome is final), so the intent is persisted separately: `Engine.Cancel` stamps `instances.subtree_cancel_requested_at` (column added by P2-T2's migration 019) through the new port method `SpawnLinkRepository.MarkSubtreeCancelRequested` BEFORE its best-effort walk, and fails closed (returns the error, cascades nothing) if that write fails. `ListOrphanedActiveChildren` treats a stamped ancestor exactly like a cancelled one for every stranded run created at or before the stamp, so a process death or a per-branch storage error part-way through the walk is finished by the already-wired orphan sweep (`cmd/arbitron/main.go:1134-1136`). Runs created after the stamp (e.g. after a later `Retry` reopened a failed parent) are not swept.

**Interfaces**
- Consumes (existing code):
  - `domain/instance/spawn_link.go:161-191` `type SpawnLinkRepository interface { ChildCountsByParents; ListActiveChildren; ListOrphanedActiveChildren }`.
  - `domain/instance/instance.go:47` `func (s State) IsTerminal() bool`; `domain/instance/errors` sentinel `instance.ErrCannotCancel` (message `"instance cannot be cancelled"`), `instance.ErrForbidden`, `instance.ErrNotFound`.
  - `domain/engine/engine.go:693` `func (e *Engine) authorizeIntervention(ctx, id) (*instance.Instance, error)` — loads + `canView` + `canIntervene`; it does NOT look at state, so it authorizes terminal runs too.
  - `domain/engine/engine.go:1554-1597` `func (e *Engine) cancelAuthorized(ctx, id) error` — authorizes FIRST, then switches on state; it returns an error wrapping `instance.ErrCannotCancel` ONLY from its `default:` branch, i.e. only after authorization succeeded. Every successful cancellation records an intervention and calls `e.OnChildTerminal`.
  - `domain/engine/cancel_cascade.go:87-123` `func (e *Engine) cascadeCancel(ctx, parentID, visited)`.
  - `domain/engine/orphan_reconciler.go:78-119` `func (r *OrphanedChildReconciler) Reconcile(ctx) error`.
  - `adapter/sqlite/instance_repo.go:650-687` `ListActiveChildren`, `:689-730` `ListOrphanedActiveChildren`; helpers `uuidToString`, `parseUUID` (same file/package).
  - P2-T1 (contract): `instance.SpawnModeGroupTask`, `Instance.GroupRunID *uuid.UUID`, `Instance.GroupTaskKey *string`, `Instance.GroupTaskKind instance.GroupTaskKind`, `instance.GroupTaskKindBranch` (used only by one unit test to model a group task).
  - P2-T2 (migration `019_parallel_groups.up.sql`): nullable column `instances.subtree_cancel_requested_at TEXT` (same `'%Y-%m-%dT%H:%M:%SZ'` layout as `created_at`).
- Produces (exact signatures):
  - `domain/instance/spawn_link.go`: `type ChildRef struct { ID uuid.UUID; State State }`
  - `SpawnLinkRepository` gains `ListChildren(ctx context.Context, parentID uuid.UUID) ([]ChildRef, error)` — ALL children incl. terminal, creation order, one level, empty non-nil slice when none.
  - `SpawnLinkRepository` gains `MarkSubtreeCancelRequested(ctx context.Context, id uuid.UUID) error` — stamps `subtree_cancel_requested_at = now` on a TERMINAL row (an active or unknown row is left untouched, `nil`); a storage failure is returned.
  - `ListOrphanedActiveChildren` (same signature) now returns every active run that has a cancelled ancestor — or a subtree-cancel-stamped ancestor whose stamp is not older than the run's `created_at` — reachable through completed/failed intermediates (an active intermediate stops the climb: that intermediate is itself the orphan and its cancellation cascades).
  - `func (NoopSpawnLinks) ListChildren(context.Context, uuid.UUID) ([]instance.ChildRef, error)` (always empty); `func (NoopSpawnLinks) MarkSubtreeCancelRequested(context.Context, uuid.UUID) error` (always nil).
  - `func (r *InstanceRepo) ListChildren(ctx context.Context, parentID uuid.UUID) ([]instance.ChildRef, error)`; `func (r *InstanceRepo) MarkSubtreeCancelRequested(ctx context.Context, id uuid.UUID) error`.
  - `func (e *Engine) cascadeCancel(ctx context.Context, parentID uuid.UUID, visited map[uuid.UUID]bool) int` (unexported; returns the number of descendants cancelled).
  - `Engine.Cancel(ctx, id) error` semantics: active run → unchanged (cancel, cascade, nil). Terminal run the caller may intervene on → `MarkSubtreeCancelRequested` first (a failure is returned wrapped as `recording subtree cancellation of <id>: ...` and nothing is cascaded), then cascade; `nil` if ≥1 descendant was cancelled, else the original error wrapping `instance.ErrCannotCancel` (the stamp stays, so any descendant the walk could not reach is still swept). The terminal run's own state, `CompletedAt`, variables, events and audit trail are never touched. Authorization failure (`ErrNotFound`/`ErrForbidden`) or storage failure → returned as before, nothing cascaded.

**Wiring & data provenance**
- Composition root: already wired — `cmd/arbitron/main.go:880` `eng.SetSpawnLinks(instanceRepo)` installs `*adaptsqlite.InstanceRepo` (which gains `ListChildren` here) as the engine's link port, and `cmd/arbitron/main.go:1134-1136` builds `engine.NewOrphanedChildReconciler(instanceRepo, instanceRepo, identityResolver, eng)` and runs it on the spawn sweep ticker. No new wiring line; the e2e test in Step 6 (`cmd/arbitron/cancel_subtree_e2e_test.go`) proves the real `InstanceRepo.ListChildren` is what the engine walks through `initDomain`.
- Producers of every input:
  - `parentID` / child rows: `instances.parent_instance_id`, written by `adapter/sqlite/instance_repo.go:createInstanceSQL` from `Instance.ParentInstanceID` — set by `domain/engine/engine_spawn.go:Engine.Spawn` (legacy spawn) and by `Engine.StartGroupTask` (P2-T7) for group tasks (`spawn_mode` NULL, `group_run_id` set, per P2-T2).
  - child `state`: `instances.state`, written by every terminal/transition path in `domain/engine/engine.go` and `domain/queue/manager.go`.
  - the cancelling identity: the request identity on `ctx` (`identity.FromContext`) for operator calls; the reconstructed owner identity for `OrphanedChildReconciler` and for the group coordinator's own `RunCanceller.Cancel` calls (P2-T10).
- Consumers made live by this task: `GroupCoordinator` / `GroupLauncher` (P2-T10/P2-T8) cancel tasks through `RunCanceller.Cancel` = `*Engine.Cancel`, so a task that is itself terminal but owns a nested group's active tasks is now cleaned up by the same call.
- Producer of the durable intent: `instances.subtree_cancel_requested_at` is written ONLY by `InstanceRepo.MarkSubtreeCancelRequested`, called ONLY from `Engine.Cancel` for an authorized terminal run; it is read ONLY by `listOrphanedActiveChildrenSQL`, whose consumer `OrphanedChildReconciler.Reconcile` already runs at startup and on the spawn sweep ticker (main.go:1134-1136).
- Provenance probes: `TestCancelSubtree_GrandchildUnderCompletedIntermediateIsCancelled` fails if `cascadeCancel` reads `ListActiveChildren` instead of `ListChildren` (the completed intermediate is never listed); `TestCancelSubtreeE2E_CascadeCrossesCompletedIntermediateThroughCompositionRoot` fails if `InstanceRepo.ListChildren` filters terminal rows or is not the repository the engine holds; `TestCancelSubtree_InterruptedTerminalParentWalkIsFinishedBySweep` fails if the intent is not recorded before the walk or the sweep ignores it; `TestCancelSubtreeE2E_CompletedParentStopsRunningChildThroughCompositionRoot` asserts the stamp on the real `instances` row.

### Task 12: Subtree cancellation (P2-D)

**Files:**
- Modify: `domain/instance/spawn_link.go` (add `ChildRef` after `ChildVisibility`, lines 137-151; extend the interface, lines 153-191)
- Modify: `adapter/sqlite/instance_repo.go` (add `ListChildren` after `ListActiveChildren`, i.e. after line 687; replace `ListOrphanedActiveChildren`, lines 689-730)
- Modify: `adapter/sqlite/queries/instances.sql` (lines 91-107: documentation-only source of truth; sqlc is not run)
- Modify: `domain/engine/cancel_cascade.go` (NoopSpawnLinks lines 24-40; `cascadeCancel` lines 57-123)
- Modify: `domain/engine/engine.go` (`Cancel` doc + body, lines 1518-1551)
- Modify: `domain/engine/orphan_reconciler.go` (type doc, lines 24-48; `Reconcile` doc, lines 71-77 — documentation only, code unchanged)
- Modify (tests): `domain/engine/cancel_cascade_test.go` (fake `fakeSpawnLinks` lines 16-89; replace `TestCancelCascade_ParentInAnUncancellableStateReturnsErrCannotCancel`, lines 343-354)
- Modify (tests): `domain/engine/enricher_child_count_test.go` (fake `countingSpawnLinks`, lines 47-53)
- Create (tests): `adapter/sqlite/instance_subtree_queries_test.go`
- Create (tests): `domain/engine/cancel_subtree_test.go`
- Create (tests): `cmd/arbitron/cancel_subtree_e2e_test.go`

Every implementation of `instance.SpawnLinkRepository` in the tree today (from `grep -rn "ListOrphanedActiveChildren" --include=*.go`) and therefore every type that must gain `ListChildren` and `MarkSubtreeCancelRequested` (P2-T2, which adds `instances.subtree_cancel_requested_at`, must be merged first):
1. `adapter/sqlite/instance_repo.go` — `*InstanceRepo` (production).
2. `domain/engine/cancel_cascade.go` — `NoopSpawnLinks` (Null Object).
3. `domain/engine/cancel_cascade_test.go` — `*fakeSpawnLinks` (test fake).
4. `domain/engine/enricher_child_count_test.go` — `*countingSpawnLinks` (test fake).
If another task's plan (e.g. P2-T10 or P4-T1) adds a further fake before this task lands, it must add `ListChildren` and `MarkSubtreeCancelRequested` too (or embed `engine.NoopSpawnLinks`); run `grep -rn "ListOrphanedActiveChildren" --include=*.go .` at Step 7 and update every hit.

#### Cycle 1 — storage: `ListChildren` and the recursive orphan query

- [x] Step 1: Write the failing storage tests. Create `adapter/sqlite/instance_subtree_queries_test.go` (package `sqlite`; reuses `setupTestDB`, `NewInstanceRepo`, `newLinkedInstance`, `spawnTestOwner` from `instance_spawn_link_test.go` and `childOf` from `instance_spawn_queries_test.go`):

```go
package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- ListChildren ----------

// EVERY child, terminal ones included, with its current state, in creation
// order. Terminal children are the whole point: a completed branch or a
// completed fire-and-forget child may still own active descendants the cancel
// cascade must reach.
func TestListChildren_ReturnsEveryChildWithStateInCreationOrder(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	states := []instance.State{
		instance.StateCompleted, instance.StateRunning, instance.StateFailed,
		instance.StateCancelled, instance.StateQueued, instance.StateWaiting, instance.StatePaused,
	}
	want := make([]instance.ChildRef, 0, len(states))
	for i, st := range states {
		mode := instance.SpawnModeFireAndForget
		if i%2 == 1 {
			mode = instance.SpawnModeWaitForChild
		}
		id := childOf(t, repo, p.ID, fmt.Sprintf("n%d", i), mode, st, owner, instance.VisibilityPrivate, "")
		want = append(want, instance.ChildRef{ID: id, State: st})
	}

	got, err := repo.ListChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, want, got,
		"every child must be returned — terminal ones too — in creation order with its state")
}

func TestListChildren_NoChildrenReturnsEmptySlice(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.ListChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.NotNil(t, got, "must return an empty slice, not nil")
	assert.Empty(t, got)
}

// ONE level, and only this parent's children: recursion belongs to the domain
// cascade, which carries the visited set.
func TestListChildren_IsOneLevelAndScopedToTheParent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	other := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, other))

	child := childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, other.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListChildren(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, instance.ChildRef{ID: child, State: instance.StateCompleted}, got[0])
}

func TestListChildren_StorageFailureSurfacesAsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, db.DB.Close())

	_, err := repo.ListChildren(ctx, p.ID)
	require.Error(t, err, "a storage failure must never look like \"no children\"")
}

// ---------- ListOrphanedActiveChildren (recursive) ----------

// cancelled root -> completed child -> failed grandchild -> RUNNING
// great-grandchild. The old one-level join could never see this run: its own
// parent is failed, not cancelled. The climb through terminal intermediates is
// what finds it.
func TestListOrphanedActiveChildren_FindsGreatGrandchildUnderCancelledRootThroughTerminalIntermediates(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCancelled
	require.NoError(t, repo.Create(ctx, root))
	child := childOf(t, repo, root.ID, "n1", instance.SpawnModeWaitForChild, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	grand := childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateFailed, owner, instance.VisibilityPrivate, "")
	great := childOf(t, repo, grand, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{great}, got)
}

// An ACTIVE intermediate stops the climb: the intermediate is itself the
// orphan, and cancelling it cascades to its own active children. Returning the
// grandchild as well would only produce a benign ErrCannotCancel later, but the
// query keeps the old "topmost stranded run" shape.
func TestListOrphanedActiveChildren_ActiveIntermediateIsTheOrphanNotItsChildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCancelled
	require.NoError(t, repo.Create(ctx, root))
	child := childOf(t, repo, root.ID, "n1", instance.SpawnModeWaitForChild, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{child}, got)
}

// A COMPLETED root's still-running descendants are legitimate independent work
// (a finish_independently loser, a fire-and-forget child). No cancelled
// ancestor means no cancellation intent: the sweep must never become a
// run-killer.
func TestListOrphanedActiveChildren_IgnoresIndependentWorkUnderCompletedAncestors(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, root))
	child := childOf(t, repo, root.ID, "n1", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, root.ID, "n2", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Several stranded runs under one cancelled root are all returned, oldest first.
func TestListOrphanedActiveChildren_ReturnsAllStrandedRunsOldestFirst(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCancelled
	require.NoError(t, repo.Create(ctx, root))
	mid := childOf(t, repo, root.ID, "n1", instance.SpawnModeWaitForChild, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	first := childOf(t, repo, mid, "n1", instance.SpawnModeFireAndForget, instance.StateQueued, owner, instance.VisibilityPrivate, "")
	second := childOf(t, repo, mid, "n2", instance.SpawnModeFireAndForget, instance.StatePaused, owner, instance.VisibilityPrivate, "")
	direct := childOf(t, repo, root.ID, "n2", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{first, second, direct}, got)
}

// Corrupt link data (an ancestor cycle through terminal rows) must terminate
// the recursive CTE instead of looping; parent_instance_id deliberately has no
// foreign key, so such rows are storable.
func TestListOrphanedActiveChildren_CorruptAncestorCycleTerminates(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	aID, bID := uuid.New(), uuid.New()
	a := newLinkedInstance(instance.SpawnLink{ParentInstanceID: bID, ParentNodeID: "n", Mode: instance.SpawnModeFireAndForget})
	a.ID = aID
	a.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, a))
	b := newLinkedInstance(instance.SpawnLink{ParentInstanceID: aID, ParentNodeID: "n", Mode: instance.SpawnModeFireAndForget})
	b.ID = bID
	b.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, b))
	childOf(t, repo, aID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err, "the bounded climb must terminate on a cycle")
	assert.Empty(t, got)
}

// ---------- durable subtree-cancellation intent ----------

func subtreeStamp(t *testing.T, db testDB, id uuid.UUID) any {
	t.Helper()
	var v any
	require.NoError(t, db.DB.QueryRow(`SELECT subtree_cancel_requested_at FROM instances WHERE id = ?`,
		id.String()).Scan(&v))
	return v
}

// The stamp lands on terminal rows only, in created_at's layout; an active row
// (whose own cancellation IS the intent) and an unknown id are left alone.
func TestMarkSubtreeCancelRequested_StampsTerminalRowsOnly(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	done := newLinkedInstance(instance.SpawnLink{})
	done.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, done))
	active := newLinkedInstance(instance.SpawnLink{})
	active.State = instance.StateRunning
	require.NoError(t, repo.Create(ctx, active))

	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, done.ID))
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, active.ID))
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, uuid.New()), "an unknown id is not an error")

	stamp, ok := subtreeStamp(t, db, done.ID).(string)
	require.True(t, ok, "a terminal row must carry the stamp")
	_, err := parseTime(stamp)
	assert.NoError(t, err, "the stamp uses created_at's RFC3339 layout so the orphan query can compare them")
	assert.Nil(t, subtreeStamp(t, db, active.ID), "an active row is never stamped")
}

func TestMarkSubtreeCancelRequested_StorageFailureSurfacesAsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	require.NoError(t, db.DB.Close())
	require.Error(t, repo.MarkSubtreeCancelRequested(context.Background(), uuid.New()))
}

// THE DURABILITY ASSERTION: a COMPLETED root whose subtree cancellation was
// requested (stamped) is cancellation intent for the run the interrupted walk
// left behind, through a completed intermediate.
func TestListOrphanedActiveChildren_StampedTerminalAncestorIsCancellationIntent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, root))
	mid := childOf(t, repo, root.ID, "n1", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	leaf := childOf(t, repo, mid, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, root.ID))

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{leaf}, got)
}

// A run created AFTER the stamp (e.g. by a parent reopened through Retry) was
// never covered by the request and is not swept.
func TestListOrphanedActiveChildren_RunCreatedAfterTheStampIsNotSwept(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateFailed
	require.NoError(t, repo.Create(ctx, root))
	childOf(t, repo, root.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	_, err := db.DB.Exec(`UPDATE instances SET subtree_cancel_requested_at = '2000-01-01T00:00:00Z' WHERE id = ?`, root.ID.String())
	require.NoError(t, err)

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "the child was created after the request and carries no cancellation intent")
}
```

- [x] Step 2: Run it, confirm it fails.

Confirmed compile failure: missing `ChildRef`, `ListChildren`, and `MarkSubtreeCancelRequested`.

```bash
go test ./adapter/sqlite/ -run 'TestListChildren|TestListOrphanedActiveChildren|TestMarkSubtreeCancelRequested' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/adapter/sqlite [github.com/arbitron/arbitron/adapter/sqlite.test]
adapter/sqlite/instance_subtree_queries_test.go:32:16: undefined: instance.ChildRef
adapter/sqlite/instance_subtree_queries_test.go:42:19: repo.ListChildren undefined (type *InstanceRepo has no field or method ListChildren)
adapter/sqlite/instance_subtree_queries_test.go:...: repo.MarkSubtreeCancelRequested undefined (type *InstanceRepo has no field or method MarkSubtreeCancelRequested)
...
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
```

- [x] Step 3: Add the domain type. In `domain/instance/spawn_link.go`, insert immediately after the `ChildVisibility` struct (after line 151, before the `SpawnLinkRepository` doc comment):

```go
// ChildRef is one row of a parent's children as the cancel cascade traverses
// them: the child's id and its state at the moment it was listed.
//
// State is advisory (it may be stale by the time the caller acts on it); the
// cascade always re-reads and re-authorizes the child through the engine's
// authorized cancellation path before deciding anything.
type ChildRef struct {
	ID    uuid.UUID
	State State
}
```

(The interface method is added in Cycle 2, Step 8, together with every other implementation, so the tree compiles at every commit.)

- [x] Step 4: Implement the storage side. In `adapter/sqlite/instance_repo.go`, insert immediately after `ListActiveChildren` (after line 687):

```go
// ListChildren implements instance.SpawnLinkRepository.
//
// EVERY child of parentID — terminal ones included — with its current state,
// in creation order (rowid breaks created_at's one-second ties). Spawn mode is
// not a filter: legacy spawn children (spawn_mode set) and parallel-group tasks
// (spawn_mode NULL, group_run_id set) are both children of their parent row.
//
// Terminal children are the reason this exists. A completed parallel-group
// branch, or a completed fire-and-forget child, can still own ACTIVE
// descendants; ListActiveChildren hides exactly those intermediates, so a
// cascade walking it could never reach what hangs below them. One level only:
// the domain cascade recurses and carries the visited set.
func (r *InstanceRepo) ListChildren(ctx context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	const q = `SELECT id, state FROM instances
  WHERE parent_instance_id = ?
  ORDER BY created_at ASC, rowid ASC`

	rows, err := r.db.QueryContext(ctx, q, uuidToString(parentID))
	if err != nil {
		return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
	}
	defer rows.Close()

	out := []instance.ChildRef{}
	for rows.Next() {
		var raw, state string
		if err := rows.Scan(&raw, &state); err != nil {
			return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
		}
		out = append(out, instance.ChildRef{ID: id, State: instance.State(state)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
	}
	return out, nil
}

// MarkSubtreeCancelRequested implements instance.SpawnLinkRepository.
//
// It persists an operator's request to cancel the SUBTREE of a TERMINAL run.
// The run's own state is final and is never rewritten, so the intent lives in
// its own column (migration 019). Engine.Cancel writes it BEFORE its
// best-effort walk; listOrphanedActiveChildrenSQL treats a stamped ancestor
// like a cancelled one, so a walk interrupted by a crash or a per-branch
// storage error is finished by the orphan sweep.
//
// Only terminal rows are stamped: an active run's own cancellation is its
// intent, and a row that was reopened (Retry) between authorization and this
// write must not carry a stale request. A re-request moves the stamp to now, so
// it covers every descendant created up to the latest request. An unknown or
// active row is left untouched and is not an error. The stamp uses created_at's
// layout so the orphan query compares them lexically.
func (r *InstanceRepo) MarkSubtreeCancelRequested(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE instances
   SET subtree_cancel_requested_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
 WHERE id = ? AND state IN ('completed','failed','cancelled')`

	if _, err := r.db.ExecContext(ctx, q, uuidToString(id)); err != nil {
		return fmt.Errorf("recording subtree cancellation request for %s: %w", id, err)
	}
	return nil
}
```

Then replace the whole of `ListOrphanedActiveChildren` (lines 689-730, doc comment included) with:

```go
// orphanAncestorWalkLimit bounds the recursive ancestor climb in
// ListOrphanedActiveChildren. Real chains are at most instance.MaxSpawnDepth
// hops deep (every spawn and every group task increments SpawnDepth against
// that ceiling); the generous margin only exists so corrupt, cyclic link data
// (parent_instance_id deliberately has no foreign key) terminates the CTE.
const orphanAncestorWalkLimit = 64

// listOrphanedActiveChildrenSQL climbs from every active non-root run through
// its ancestors. The climb CONTINUES only through completed/failed rows and
// MATCHES on the first cancelled one — or on a terminal ancestor whose subtree
// cancellation was requested (MarkSubtreeCancelRequested) no earlier than the
// stranded run was created; an active ancestor stops it (that ancestor is
// itself the orphan, or healthy), and so does a root.
const listOrphanedActiveChildrenSQL = `WITH RECURSIVE up(orphan_id, ancestor_id, hops) AS (
  SELECT c.id, c.parent_instance_id, 1
    FROM instances c
   WHERE c.state IN ('queued','running','waiting','paused')
     AND c.parent_instance_id IS NOT NULL
  UNION ALL
  SELECT up.orphan_id, a.parent_instance_id, up.hops + 1
    FROM up
    JOIN instances a ON a.id = up.ancestor_id
   WHERE a.state IN ('completed','failed')
     AND a.parent_instance_id IS NOT NULL
     AND up.hops < ?
)
SELECT c.id
  FROM instances c
 WHERE c.id IN (
   SELECT up.orphan_id
     FROM up
     JOIN instances a ON a.id = up.ancestor_id
     JOIN instances o ON o.id = up.orphan_id
    WHERE a.state = 'cancelled'
       OR (a.subtree_cancel_requested_at IS NOT NULL
           AND o.created_at <= a.subtree_cancel_requested_at))
 ORDER BY c.created_at ASC, c.rowid ASC`

// ListOrphanedActiveChildren implements instance.SpawnLinkRepository.
//
// It is the retry surface behind the in-process cancel cascade: every active
// run with a CANCELLED ancestor reachable through terminal (completed/failed)
// intermediates. The cascade now walks through terminal intermediates
// (ListChildren), so its residue can sit several levels below the cancelled
// run — a running grandchild under a completed branch under a cancelled root.
// A one-level join on the direct parent cannot see that run, because its own
// parent is completed, not cancelled.
//
// An ACTIVE intermediate stops the climb: that intermediate is the orphan, and
// cancelling it (Engine.Cancel cascades) reaches everything below it. A
// completed or failed ROOT with active descendants is NOT matched unless an
// operator requested its subtree cancellation (subtree_cancel_requested_at,
// written by MarkSubtreeCancelRequested before Engine.Cancel's walk) at or
// after the stranded run's creation: without that stamp there is no
// cancellation intent anywhere above them — that is legitimate independent
// work (a finish_independently loser, a fire-and-forget child).
//
// Result is oldest-first so a sweep tears down the longest-stranded runs
// first. The climb is bounded by orphanAncestorWalkLimit.
func (r *InstanceRepo) ListOrphanedActiveChildren(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.db.QueryContext(ctx, listOrphanedActiveChildrenSQL, orphanAncestorWalkLimit)
	if err != nil {
		return nil, fmt.Errorf("listing orphaned active children: %w", err)
	}
	defer rows.Close()

	out := []uuid.UUID{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("listing orphaned active children: %w", err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing orphaned active children: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing orphaned active children: %w", err)
	}
	return out, nil
}
```

Then, in `adapter/sqlite/queries/instances.sql`, replace lines 91-107 (the `ListActiveChildren` and `ListOrphanedActiveChildren` blocks) with the readable source of truth (documentation only — `sqlc` is not run and `adapter/sqlite/generated/` is not touched):

```sql
-- name: ListActiveChildren :many
-- Every active child of a parent, REGARDLESS of spawn mode. Retained on the
-- port; the cancel cascade now walks ListChildren instead.
SELECT id FROM instances
WHERE parent_instance_id = ?
  AND state IN ('queued','running','waiting','paused')
ORDER BY created_at ASC;

-- name: ListChildren :many
-- NOTE: executed hand-written by InstanceRepo.ListChildren. EVERY child of a
-- parent, terminal ones included, with its state: the cancel cascade must
-- traverse terminal intermediates to reach their active descendants.
SELECT id, state FROM instances
WHERE parent_instance_id = ?
ORDER BY created_at ASC, rowid ASC;

-- name: ListOrphanedActiveChildren :many
-- NOTE: executed hand-written by InstanceRepo.ListOrphanedActiveChildren
-- (listOrphanedActiveChildrenSQL). Every active run with a CANCELLED ancestor
-- (or a terminal ancestor whose subtree_cancel_requested_at is not older than
-- the run) reachable through completed/failed intermediates; an active ancestor
-- stops the climb. The bound (64) guards corrupt cyclic links.
WITH RECURSIVE up(orphan_id, ancestor_id, hops) AS (
  SELECT c.id, c.parent_instance_id, 1
    FROM instances c
   WHERE c.state IN ('queued','running','waiting','paused')
     AND c.parent_instance_id IS NOT NULL
  UNION ALL
  SELECT up.orphan_id, a.parent_instance_id, up.hops + 1
    FROM up
    JOIN instances a ON a.id = up.ancestor_id
   WHERE a.state IN ('completed','failed')
     AND a.parent_instance_id IS NOT NULL
     AND up.hops < 64
)
SELECT c.id
  FROM instances c
 WHERE c.id IN (
   SELECT up.orphan_id FROM up
     JOIN instances a ON a.id = up.ancestor_id
     JOIN instances o ON o.id = up.orphan_id
    WHERE a.state = 'cancelled'
       OR (a.subtree_cancel_requested_at IS NOT NULL
           AND o.created_at <= a.subtree_cancel_requested_at))
 ORDER BY c.created_at ASC, c.rowid ASC;

-- name: MarkSubtreeCancelRequested :exec
-- NOTE: executed hand-written by InstanceRepo.MarkSubtreeCancelRequested.
-- Durable subtree-cancellation intent on a TERMINAL run (Engine.Cancel).
UPDATE instances
   SET subtree_cancel_requested_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
 WHERE id = ? AND state IN ('completed','failed','cancelled');
```

- [x] Step 5: Run the new and the existing link-query tests, confirm they pass.

Focused query tests and the full SQLite suite passed.

```bash
go test ./adapter/sqlite/ -run 'TestListChildren|TestListOrphanedActiveChildren|TestMarkSubtreeCancelRequested|TestListActiveChildren|TestChildCounts|TestSpawnQueries' -count=1
go test ./adapter/sqlite/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.XXXs
ok  	github.com/arbitron/arbitron/adapter/sqlite	X.XXXs
```
(`TestListOrphanedActiveChildren_FindsActiveChildOfCancelledParent` and `_IgnoresHealthyAndTerminalRows` in `instance_spawn_queries_test.go` still pass unchanged: a direct cancelled parent is hop 1 of the climb.)

Commit:
```bash
git add domain/instance/spawn_link.go adapter/sqlite/instance_repo.go adapter/sqlite/queries/instances.sql adapter/sqlite/instance_subtree_queries_test.go
git commit -m "feat(sqlite): list all children and find orphans through terminal intermediates" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 2 — domain: traverse terminal intermediates, cancel after parent completion

- [x] Step 6: Write the failing domain and e2e tests.

(a) In `domain/engine/cancel_cascade_test.go`, add two fields to `fakeSpawnLinks` (after `unfiltered bool`, line 33):

```go
	// marked records MarkSubtreeCancelRequested per id (terminal ids only,
	// like the production WHERE clause); markErr makes that write fail.
	marked  map[uuid.UUID]bool
	markErr error
```

then replace the fake's `ListOrphanedActiveChildren` (lines 77-90, doc comment included) with the methods below, so the fake honours the NEW contracts with the same load-bearing arguments (parent id, `listErr`, live state, the marked id) the production queries honour:

```go
// MarkSubtreeCancelRequested records the durable intent for id — only when id
// is terminal, exactly like the production statement — or fails with markErr.
func (f *fakeSpawnLinks) MarkSubtreeCancelRequested(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return f.markErr
	}
	if !f.states(id).IsTerminal() {
		return nil
	}
	if f.marked == nil {
		f.marked = map[uuid.UUID]bool{}
	}
	f.marked[id] = true
	return nil
}

func (f *fakeSpawnLinks) isMarked(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.marked[id]
}

// ListChildren returns EVERY child of parentID with its current state,
// terminal ones included — the traversal surface the cascade walks. It honours
// listErr exactly like ListActiveChildren, so a branch-scoped storage failure
// can still be simulated. `unfiltered` is irrelevant here: nothing is filtered.
func (f *fakeSpawnLinks) ListChildren(_ context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.listErr[parentID]; err != nil {
		return nil, err
	}
	out := []instance.ChildRef{}
	for _, c := range f.children[parentID] {
		out = append(out, instance.ChildRef{ID: c, State: f.states(c)})
	}
	return out, nil
}

// ListOrphanedActiveChildren mirrors the production recursive query: every
// non-terminal run whose ancestor climb — continuing only through completed or
// failed runs — reaches a CANCELLED run or a subtree-cancel-MARKED run. An
// active ancestor stops the climb, as does a root. The 64-hop bound matches
// orphanAncestorWalkLimit. (The fake has no creation times; the production
// "created at or before the stamp" rule is pinned by the sqlite tests.)
func (f *fakeSpawnLinks) ListOrphanedActiveChildren(_ context.Context) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parentOf := map[uuid.UUID]uuid.UUID{}
	for parent, kids := range f.children {
		for _, c := range kids {
			if c != parent {
				parentOf[c] = parent
			}
		}
	}
	out := []uuid.UUID{}
	for child := range parentOf {
		if f.states(child).IsTerminal() {
			continue
		}
		anc, ok := parentOf[child]
		for hops := 0; ok && hops < 64; hops++ {
			st := f.states(anc)
			if st == instance.StateCancelled || f.marked[anc] {
				out = append(out, child)
				break
			}
			if st != instance.StateCompleted && st != instance.StateFailed {
				break
			}
			anc, ok = parentOf[anc]
		}
	}
	return out, nil
}
```

(b) In the same file, replace `TestCancelCascade_ParentInAnUncancellableStateReturnsErrCannotCancel` (lines 343-354) — its premise ("a refused parent cancellation cascades to nothing") is exactly the behaviour this task changes — with:

```go
// A terminal parent is no longer a dead end: an authorized cancel on it still
// stops its active descendants, and succeeds because one was stopped. The
// parent's own completed state is untouched. (Replaces the Phase-0 test whose
// premise was "a refused parent cancellation cascades to nothing".)
func TestCancelCascade_TerminalParentStillCascadesToActiveChild(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, child))
}
```

(c) In `domain/engine/enricher_child_count_test.go`, add after `countingSpawnLinks.ListActiveChildren` (after line 49):

```go
func (c *countingSpawnLinks) ListChildren(context.Context, uuid.UUID) ([]instance.ChildRef, error) {
	return nil, nil
}

func (c *countingSpawnLinks) MarkSubtreeCancelRequested(context.Context, uuid.UUID) error {
	return nil
}
```

(d) Create `domain/engine/cancel_subtree_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ instance.SpawnLinkRepository = engine.NoopSpawnLinks{}

// groupTaskOf creates a parallel-group task child of parent (the P2-T1 link
// shape: SpawnModeGroupTask plus group linkage), owned by the fixture owner.
func (c *cascadeFixture) groupTaskOf(t *testing.T, parent uuid.UUID, state instance.State) uuid.UUID {
	t.Helper()
	pid := parent
	node := "group-1"
	runID := uuid.New()
	key := "loser"
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: c.owner, WorkflowName: "wf",
		State: state, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
	}
	require.NoError(t, c.f.instanceRepo.Create(context.Background(), inst))
	c.links.link(parent, inst.ID)
	return inst.ID
}

// ---------- happy path ----------

// THE CORE P2-D ASSERTION. A completed intermediate (a branch that finished)
// still owns a running grandchild. ListActiveChildren never lists the
// completed intermediate, so a cascade walking it can never reach the
// grandchild. No `unfiltered` bypass here: the real traversal surface must
// produce the intermediate.
func TestCancelSubtree_GrandchildUnderCompletedIntermediateIsCancelled(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateRunning)
	mid := c.run(t, instance.StateCompleted)
	grand := c.run(t, instance.StateRunning)
	c.links.link(root, mid)
	c.links.link(mid, grand)

	require.NoError(t, c.f.engine.Cancel(c.ctx, root))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, root))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, mid), "a terminal intermediate is traversed, never rewritten")
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, grand))
	assert.Equal(t, 2, c.interventions.count(), "root and grandchild are audited; the untouched intermediate is not")
}

// THE CORE TERMINAL-PARENT ASSERTION. A first_success group decided with
// finish_independently leaves a loser running after its parent completed.
// Cancelling the completed parent must stop the loser, succeed, and leave the
// parent's own state, events and audit trail exactly as they were.
func TestCancelSubtree_CompletedParentStopsIndependentGroupTaskWithoutChangingItsState(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	loser := c.groupTaskOf(t, parent, instance.StateRunning)

	var mu sync.Mutex
	var transitioned []uuid.UUID
	c.f.instanceRepo.onUpdateState = func(id uuid.UUID, _ instance.State) {
		mu.Lock()
		transitioned = append(transitioned, id)
		mu.Unlock()
	}

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCompleted, c.stateOf(t, parent), "the parent's outcome is never reopened")
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, loser))
	mu.Lock()
	assert.Equal(t, []uuid.UUID{loser}, transitioned, "only the loser's state may be written")
	mu.Unlock()
	assert.Empty(t, c.f.eventBus.eventsOfType(parent, engine.EventTypeInstanceCancelled),
		"a completed parent must not announce a cancellation that did not happen to it")
	assert.Len(t, c.f.eventBus.eventsOfType(loser, engine.EventTypeInstanceCancelled), 1)

	parentRecords, err := c.interventions.ListByInstance(context.Background(), parent)
	require.NoError(t, err)
	assert.Empty(t, parentRecords, "no intervention is recorded against the untouched parent")
	loserRecords, err := c.interventions.ListByInstance(context.Background(), loser)
	require.NoError(t, err)
	assert.Len(t, loserRecords, 1)
}

// ---------- success ----------

// A crash-recovered (failed) parent can still have its independent work torn
// down, and the cascade climbs through every terminal kind on the way.
func TestCancelSubtree_DeepChainThroughMixedTerminalIntermediates(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateFailed)
	a := c.run(t, instance.StateCompleted)
	b := c.run(t, instance.StateFailed)
	d := c.run(t, instance.StateCancelled)
	leaf := c.run(t, instance.StateQueued)
	c.links.link(root, a)
	c.links.link(a, b)
	c.links.link(b, d)
	c.links.link(d, leaf)

	require.NoError(t, c.f.engine.Cancel(c.ctx, root))

	assert.Equal(t, instance.StateFailed, c.stateOf(t, root))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, a))
	assert.Equal(t, instance.StateFailed, c.stateOf(t, b))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, d))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, leaf))
}

// LEGACY UNCHANGED: an active parent's spawn children — awaited and
// fire-and-forget, active and terminal — get exactly the treatment they got
// before: every active one cancelled and audited, every terminal one left as it
// was.
func TestCancelSubtree_LegacySpawnChildrenOfActiveParentUnchanged(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateWaiting)
	awaited := c.run(t, instance.StateRunning)
	fireForget := c.run(t, instance.StateQueued)
	done := c.run(t, instance.StateCompleted)
	c.links.link(parent, awaited)
	c.links.link(parent, fireForget)
	c.links.link(parent, done)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, awaited))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, fireForget))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, done))
	assert.Equal(t, 3, c.interventions.count())
}

// ---------- failure ----------

// A terminal parent with nothing left to stop keeps the old contract exactly:
// the original ErrCannotCancel, naming the parent's state.
func TestCancelSubtree_TerminalParentWithOnlyTerminalDescendantsReturnsErrCannotCancel(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateCompleted)
	grand := c.run(t, instance.StateFailed)
	c.links.link(parent, child)
	c.links.link(child, grand)

	err := c.f.engine.Cancel(c.ctx, parent)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
	assert.Contains(t, err.Error(), "(state: completed)")
	assert.Equal(t, 0, c.interventions.count())
}

func TestCancelSubtree_TerminalParentWithoutChildrenReturnsErrCannotCancel(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCancelled)

	err := c.f.engine.Cancel(c.ctx, parent)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
}

// Authorization is checked on the terminal parent itself: a department peer
// who may VIEW but not intervene gets ErrForbidden and nothing below it moves,
// even a descendant the actor could authorize in isolation.
func TestCancelSubtree_TerminalParentAuthorizationFailureCascadesNothing(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.runInDepartment(t, instance.StateCompleted, "finance")
	child := c.runInDepartment(t, instance.StateRunning, "eng")
	c.links.link(parent, child)

	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})

	err := c.f.engine.Cancel(leadCtx, parent)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrForbidden)
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child))
	assert.Equal(t, 0, c.interventions.count())
}

// Authorization still gates TERMINAL intermediates: the cascade re-authorizes
// every node it traverses, so a completed intermediate the actor may not
// intervene on halts its branch exactly as an active one does.
func TestCancelSubtree_AuthorizationFailureOnTerminalIntermediateHaltsBranch(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.runInDepartment(t, instance.StateRunning, "eng")
	mid := c.runInDepartment(t, instance.StateCompleted, "finance")
	grand := c.runInDepartment(t, instance.StateRunning, "eng")
	c.links.link(root, mid)
	c.links.link(mid, grand)

	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})

	require.NoError(t, c.f.engine.Cancel(leadCtx, root))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, root))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, mid))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, grand),
		"the cascade must not reach past a terminal node it could not authorize")
}

// ---------- error ----------

// A storage failure listing a terminal parent's children leaves nothing
// cancelled, so the caller gets the honest answer: the parent could not be
// cancelled.
func TestCancelSubtree_TerminalParentListFailureReturnsErrCannotCancel(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)
	c.links.listErr[parent] = errors.New("db down")

	err := c.f.engine.Cancel(c.ctx, parent)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child))
}

// ---------- edge cases ----------

func TestNoopSpawnLinks_ListChildrenReportsNone(t *testing.T) {
	got, err := engine.NoopSpawnLinks{}.ListChildren(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// The orphan sweep now finds a stranded run several levels below the cancelled
// root, through completed and failed intermediates — the exact residue an
// interrupted terminal-intermediate cascade leaves.
func TestOrphanReconciler_SweepsGreatGrandchildUnderCancelledRootThroughTerminalIntermediates(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateCancelled)
	mid1 := c.run(t, instance.StateCompleted)
	mid2 := c.run(t, instance.StateFailed)
	leaf := c.run(t, instance.StateRunning)
	c.links.link(root, mid1)
	c.links.link(mid1, mid2)
	c.links.link(mid2, leaf)

	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, leaf))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, mid1))
	assert.Equal(t, instance.StateFailed, c.stateOf(t, mid2))
}

// Independent work under a COMPLETED root carries no cancellation intent and
// must survive the sweep.
func TestOrphanReconciler_LeavesIndependentWorkUnderCompletedRootAlone(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateCompleted)
	loser := c.groupTaskOf(t, root, instance.StateRunning)

	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateRunning, c.stateOf(t, loser))
}

// ---------- durable terminal-parent intent ----------

// THE DURABILITY ASSERTION. The in-process walk below a completed parent is
// interrupted (the child listing fails, standing in for a crash part-way
// through), so nothing is cancelled and the call reports ErrCannotCancel — but
// the request was recorded first, and the orphan sweep finishes the job.
func TestCancelSubtree_InterruptedTerminalParentWalkIsFinishedBySweep(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	loser := c.groupTaskOf(t, parent, instance.StateRunning)
	c.links.listErr[parent] = errors.New("db down")

	err := c.f.engine.Cancel(c.ctx, parent)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
	assert.Equal(t, instance.StateRunning, c.stateOf(t, loser), "the interrupted walk reached nothing")
	assert.True(t, c.links.isMarked(parent), "the subtree cancellation must be recorded BEFORE the walk")

	delete(c.links.listErr, parent)
	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, loser), "the sweep finishes the recorded request")
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, parent), "the parent's outcome is never reopened")
}

// If the intent cannot be recorded, the call fails closed: a success answer
// must always mean the request is durable.
func TestCancelSubtree_IntentWriteFailureFailsClosed(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)
	c.links.markErr = errors.New("disk full")

	err := c.f.engine.Cancel(c.ctx, parent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recording subtree cancellation")
	assert.Contains(t, err.Error(), "disk full")
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child), "nothing is cascaded without a durable request")
	assert.Equal(t, 0, c.interventions.count())
}

// An ACTIVE parent's own cancelled state is its durable intent; it is never
// marked, and a denied caller marks nothing.
func TestCancelSubtree_OnlyAuthorizedTerminalParentsAreMarked(t *testing.T) {
	c := newCascadeFixture(t)
	active := c.run(t, instance.StateRunning)
	require.NoError(t, c.f.engine.Cancel(c.ctx, active))
	assert.False(t, c.links.isMarked(active))

	denied := c.runInDepartment(t, instance.StateCompleted, "finance")
	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})
	assert.ErrorIs(t, c.f.engine.Cancel(leadCtx, denied), instance.ErrForbidden)
	assert.False(t, c.links.isMarked(denied), "an unauthorized caller records no intent")
}

func TestNoopSpawnLinks_MarkSubtreeCancelRequestedIsANoOp(t *testing.T) {
	assert.NoError(t, engine.NoopSpawnLinks{}.MarkSubtreeCancelRequested(context.Background(), uuid.New()))
}
```

(e) Create `cmd/arbitron/cancel_subtree_e2e_test.go` — legacy spawn workflows only, so it is independent of every other Phase 2 task:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootSubtreeStack boots the REAL composition root with a three-level
// fire-and-forget chain: cs-root -> cs-mid -> cs-leaf. Root and mid complete
// almost immediately; the leaf sleeps 30s. 30s is far beyond every wait in
// these tests, so a leaf observed as "cancelled" can only have been cancelled
// (process-group SIGKILL via the run context), never confused with finishing.
func bootSubtreeStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	writeWorkflow(t, workspaceDir, "cs-leaf", `{
  "name": "cs-leaf",
  "description": "long-running leaf",
  "inputs": [],
  "nodes": [
    {"id": "hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}}
  ],
  "edges": []
}`)
	writeWorkflow(t, workspaceDir, "cs-mid", `{
  "name": "cs-mid",
  "description": "completes after spawning the leaf",
  "inputs": [],
  "nodes": [
    {"id": "m1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "mid", "operation": "set", "value": true}]}},
    {"id": "spawn-leaf", "type": "spawn_workflow",
     "config": {"childWorkflow": "cs-leaf", "spawnMode": "fire_and_forget"}}
  ],
  "edges": [
    {"source": "m1", "target": "spawn-leaf"}
  ]
}`)
	writeWorkflow(t, workspaceDir, "cs-root", `{
  "name": "cs-root",
  "description": "completes after spawning the mid",
  "inputs": [],
  "nodes": [
    {"id": "r1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "root", "operation": "set", "value": true}]}},
    {"id": "spawn-mid", "type": "spawn_workflow",
     "config": {"childWorkflow": "cs-mid", "spawnMode": "fire_and_forget"}}
  ],
  "edges": [
    {"source": "r1", "target": "spawn-mid"}
  ]
}`)

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// onlyChild waits for parentID to have exactly one child and returns it.
func onlyChild(t *testing.T, ds *domainStack, parentID uuid.UUID) uuid.UUID {
	t.Helper()
	var kids []instance.InstanceSummary
	require.Eventually(t, func() bool {
		kids = childrenOf(t, ds, parentID)
		return len(kids) == 1
	}, 40*time.Second, 100*time.Millisecond, "parent %s never produced exactly one child", parentID)
	return kids[0].ID
}

// A completed parent's still-running fire-and-forget child is stopped by
// cancelling the parent, and the parent's own outcome is untouched — through
// the real SQLite ListChildren the composition root installs.
func TestCancelSubtreeE2E_CompletedParentStopsRunningChildThroughCompositionRoot(t *testing.T) {
	ds := bootSubtreeStack(t)

	midID, _, err := ds.eng.Trigger(context.Background(), "cs-mid", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, midID, instance.StateCompleted)
	leafID := onlyChild(t, ds, midID)
	waitForState(t, ds, leafID, instance.StateRunning)

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), midID))

	waitForState(t, ds, leafID, instance.StateCancelled)
	mid, err := ds.instanceRepo.Get(context.Background(), midID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, mid.State, "the completed parent's outcome is never reopened")

	// The request was recorded durably on the real row (migration 019 column,
	// written through the InstanceRepo the composition root installed), so an
	// interrupted walk would be finished by the orphan sweep.
	var stamp *string
	require.NoError(t, ds.db.QueryRow(`SELECT subtree_cancel_requested_at FROM instances WHERE id = ?`,
		midID.String()).Scan(&stamp))
	require.NotNil(t, stamp, "Engine.Cancel on a terminal run must persist the subtree-cancellation intent")
}

// PROVENANCE PROBE for the real InstanceRepo.ListChildren: root and mid are
// BOTH completed, the leaf is running two levels down. A repository that
// filtered terminal rows (or an engine still walking ListActiveChildren) never
// lists the completed mid and the leaf keeps running.
func TestCancelSubtreeE2E_CascadeCrossesCompletedIntermediateThroughCompositionRoot(t *testing.T) {
	ds := bootSubtreeStack(t)

	rootID, _, err := ds.eng.Trigger(context.Background(), "cs-root", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, rootID, instance.StateCompleted)
	midID := onlyChild(t, ds, rootID)
	waitForState(t, ds, midID, instance.StateCompleted)
	leafID := onlyChild(t, ds, midID)
	waitForState(t, ds, leafID, instance.StateRunning)

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), rootID))

	waitForState(t, ds, leafID, instance.StateCancelled)
	waitForState(t, ds, midID, instance.StateCompleted)
	waitForState(t, ds, rootID, instance.StateCompleted)
}

// A completed run with nothing left running keeps the old refusal.
func TestCancelSubtreeE2E_TerminalRunWithNothingActiveStillRefuses(t *testing.T) {
	ds := bootSubtreeStack(t)

	leafless, _, err := ds.eng.Trigger(context.Background(), "cs-mid", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, leafless, instance.StateCompleted)
	leafID := onlyChild(t, ds, leafless)
	waitForState(t, ds, leafID, instance.StateRunning)
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), leafID))
	waitForState(t, ds, leafID, instance.StateCancelled)

	err = ds.eng.Cancel(adminCtxForLocalUser(t, ds), leafless)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
}
```

(The 40s budget matches `waitForState` in `spawn_wait_e2e_test.go`.)

- [x] Step 7: Run them, confirm they fail.

Domain tests fail to compile on the two missing Null Object methods; real-root E2E tests fail with `ErrCannotCancel` for completed parents. Repository search confirms all four link-port implementers listed above.

```bash
go test ./domain/engine/ -run 'TestCancelSubtree|TestCancelCascade|TestOrphanReconciler|TestNoopSpawnLinks|TestEnricherChildCount' -count=1
```
Expected (compile failure — `NoopSpawnLinks` has no `ListChildren` yet and `var _ instance.SpawnLinkRepository = engine.NoopSpawnLinks{}` still compiles because the interface does not require it, so the first error is the direct call):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/cancel_subtree_test.go:238:38: engine.NoopSpawnLinks{}.ListChildren undefined (type engine.NoopSpawnLinks has no field or method ListChildren)
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

```bash
go test ./cmd/arbitron/ -run 'TestCancelSubtreeE2E' -v -count=1
```
Expected (runtime failure — the e2e file compiles against today's API):
```
--- FAIL: TestCancelSubtreeE2E_CompletedParentStopsRunningChildThroughCompositionRoot
    cancel_subtree_e2e_test.go:...: Error: Received unexpected error:
        instance ... cannot be cancelled (state: completed): instance cannot be cancelled
--- FAIL: TestCancelSubtreeE2E_CascadeCrossesCompletedIntermediateThroughCompositionRoot
    ... cannot be cancelled (state: completed): instance cannot be cancelled
--- PASS: TestCancelSubtreeE2E_TerminalRunWithNothingActiveStillRefuses
FAIL
```

Then run `grep -rn "ListOrphanedActiveChildren" --include=*.go .` and confirm the only implementers are the four listed under **Files** (plus any added by tasks that landed earlier — add `ListChildren` to those too).

- [x] Step 8: Implement.

(a) `domain/instance/spawn_link.go` — in the `SpawnLinkRepository` interface (lines 161-191), insert after the `ListActiveChildren` method and replace the `ListOrphanedActiveChildren` doc comment, so the interface reads:

```go
type SpawnLinkRepository interface {
	// ChildCountsByParents returns the number of children per parent id,
	// restricted to what vis permits the caller to see.
	//
	// Contract (mirrors NodeExecutionRepository.AggregateByInstances):
	//   - Parents with no visible children are ABSENT from the map; absence
	//     means zero, not an error.
	//   - An empty parentIDs slice returns an empty map WITHOUT touching storage.
	// It exists so a page of the run list resolves every expander in ONE round
	// trip instead of N+1 queries.
	ChildCountsByParents(ctx context.Context, parentIDs []uuid.UUID, vis ChildVisibility) (map[uuid.UUID]int, error)

	// ListActiveChildren returns the ids of parentID's children that are still
	// in a cancellable state (queued, running, waiting, paused), REGARDLESS of
	// spawn mode. A parent with no active children returns an empty slice,
	// not an error. The cancel cascade no longer walks this (see ListChildren):
	// it hides terminal intermediates that may own active descendants.
	ListActiveChildren(ctx context.Context, parentID uuid.UUID) ([]uuid.UUID, error)

	// ListChildren returns EVERY child of parentID — terminal ones included —
	// with its state, in creation order, ONE level deep, regardless of spawn
	// mode (legacy spawn children and parallel-group tasks alike). It is the
	// cancel cascade's traversal surface: a completed branch or a completed
	// fire-and-forget child can still own active descendants, and only a walk
	// that lists the terminal intermediate can reach them. No children is an
	// empty slice, not an error.
	ListChildren(ctx context.Context, parentID uuid.UUID) ([]ChildRef, error)

	// MarkSubtreeCancelRequested durably records that an authorized caller
	// asked to cancel the SUBTREE of the TERMINAL run id (whose own state is
	// final and is never rewritten). Engine.Cancel calls it before its
	// best-effort walk so ListOrphanedActiveChildren can finish an interrupted
	// walk. An active or unknown id is left untouched and is not an error; a
	// storage failure is returned.
	MarkSubtreeCancelRequested(ctx context.Context, id uuid.UUID) error

	// ListOrphanedActiveChildren returns the ids of every still-active run that
	// has a CANCELLED ancestor — or a terminal ancestor whose subtree
	// cancellation was requested (MarkSubtreeCancelRequested) at or after the
	// run's creation — reachable through terminal (completed/failed)
	// intermediates: the residue an interrupted cancel cascade leaves behind,
	// now that the cascade walks through terminal intermediates. An ACTIVE
	// ancestor stops the climb (it is the orphan itself, or healthy); an
	// unmarked completed/failed root with active descendants is NOT an orphan
	// (that is legitimate independent work, e.g. a finish_independently loser).
	//
	// The cascade is a best-effort in-process walk: a storage error on one
	// branch or a process death mid-walk leaves active runs below a cancelled
	// run. Without a sweep that state is permanent and invisible. An empty
	// result is the normal case, not an error.
	ListOrphanedActiveChildren(ctx context.Context) ([]uuid.UUID, error)
}
```

(b) `domain/engine/cancel_cascade.go` — add after `NoopSpawnLinks.ListActiveChildren` (after line 35):

```go
// ListChildren always reports no children, which is what makes the cancel
// cascade a no-op under this Null Object.
func (NoopSpawnLinks) ListChildren(context.Context, uuid.UUID) ([]instance.ChildRef, error) {
	return []instance.ChildRef{}, nil
}

// MarkSubtreeCancelRequested records nothing: with no link store there is no
// subtree to finish later.
func (NoopSpawnLinks) MarkSubtreeCancelRequested(context.Context, uuid.UUID) error {
	return nil
}
```

and change the `ListActiveChildren` doc comment on lines 31-32 to:

```go
// ListActiveChildren always reports no active children.
```

Then replace `cascadeCancel` and its doc comment (lines 57-123) with:

```go
// cascadeCancel cancels every active DESCENDANT of parentID, depth-first, and
// returns how many it cancelled.
//
// SCOPE: EVERY child, BOTH spawn modes and parallel-group tasks, AND THROUGH
// TERMINAL INTERMEDIATES. The walk lists every child (ListChildren), not only
// active ones: a completed branch, or a completed fire-and-forget child, can
// still own active descendants, and a walk over ListActiveChildren never
// lists that intermediate, so it could never reach them. A terminal child is
// TRAVERSED — re-authorized, left exactly as it is, and descended — never
// rewritten. The fire-and-forget reasoning is unchanged: the spec's carve-outs
// are child->parent only; nothing weakens parent->child.
//
// AUTHORIZATION is re-checked on EVERY node the walk passes, terminal ones
// included: cancelAuthorized authorizes before it looks at state, and returns
// ErrCannotCancel only for an AUTHORIZED terminal run. Any other error (denied,
// not found, storage) leaves that child untouched, so descending past it with
// the same identity could cancel a grandchild the actor could not authorize on
// the child — the branch is halted instead.
//
// PARTIAL FAILURE IS TOLERATED, NOT FATAL. The caller's own transition (if
// any) has already happened; an uncooperative descendant is logged. The
// orphan sweep repairs the residue under a cancelled ancestor.
//
// The `visited` set makes corrupt or hand-edited link data degrade into a
// halted traversal rather than unbounded recursion.
//
// ctx MUST already be DETACHED from the caller's request (see Cancel): the
// walk issues one authorized cancellation per descendant, and a client
// disconnect part-way through would abandon the remainder of the subtree.
func (e *Engine) cascadeCancel(ctx context.Context, parentID uuid.UUID, visited map[uuid.UUID]bool) int {
	children, err := e.spawnLinks.ListChildren(ctx, parentID)
	if err != nil {
		// Logged, not fatal: any descendant left running remains visible in the
		// hierarchy (and, under a cancelled ancestor, is swept as an orphan).
		slog.Error("cancel cascade: failed to list children",
			"parentID", parentID, "error", err)
		return 0
	}

	cancelled := 0
	for _, child := range children {
		if visited[child.ID] {
			continue
		}
		visited[child.ID] = true

		cerr := e.cancelAuthorized(ctx, child.ID)
		switch {
		case cerr == nil:
			cancelled++
		case errors.Is(cerr, instance.ErrCannotCancel):
			// An AUTHORIZED terminal child (listed terminal, or raced to a
			// terminal state since the list): traverse it, it may own active
			// descendants.
		default:
			slog.Warn("cancel cascade: failed to cancel child, branch not descended",
				"parentID", parentID, "childID", child.ID, "listedState", child.State, "error", cerr)
			continue
		}
		cancelled += e.cascadeCancel(ctx, child.ID, visited)
	}
	return cancelled
}
```

(c) `domain/engine/engine.go` — replace the `Cancel` doc comment and body (lines 1518-1551) with:

```go
// Cancel sets a running, queued, paused, or waiting instance to cancelled and
// cancels its context/timer as appropriate, then cancels every active
// descendant it spawned, recursively — through terminal intermediates.
//
// A TERMINAL instance (completed, failed, cancelled) the caller may intervene
// on is not a dead end: its own state, outcome, events and audit trail are
// left exactly as they are, but its still-active descendants — a
// finish_independently group loser, a fire-and-forget child, a nested group's
// tasks below a completed branch — are cancelled. The call then succeeds iff
// at least one descendant was cancelled; with nothing left to stop it returns
// the original error wrapping instance.ErrCannotCancel, so "cannot cancel a
// finished run" keeps its meaning. Before the walk the request is recorded
// durably (SpawnLinkRepository.MarkSubtreeCancelRequested) so the orphan sweep
// finishes a walk interrupted by a crash or storage error; if that write fails
// the error is returned and nothing is cascaded. An authorization or storage
// failure on the instance itself is returned as before and nothing is cascaded.
//
// ORDER IS A CORRECTNESS REQUIREMENT, NOT A STYLE PREFERENCE. The child-terminal
// wake only re-queues a parent that is STILL in the waiting state. Cancelling
// the parent FIRST means it is already cancelled by the time its children reach
// their terminal state, so the wake is a no-op and the parent cannot be
// resurrected through its own failure port by the very children being torn
// down. Reversing the order produces a parent that flips from cancelled back to
// queued. (A terminal parent is not waiting either, so the same guard holds.)
func (e *Engine) Cancel(ctx context.Context, instanceID uuid.UUID) error {
	ownErr := e.cancelAuthorized(ctx, instanceID)
	if ownErr != nil && !errors.Is(ownErr, instance.ErrCannotCancel) {
		// Denied, not found, or a storage failure: this run and its subtree are
		// untouched, and the caller has not been authorized to reach either.
		return ownErr
	}
	// From here the caller is AUTHORIZED on instanceID: cancelAuthorized only
	// returns ErrCannotCancel from its terminal-state branch, after
	// authorizeIntervention succeeded.
	if ownErr != nil {
		// TERMINAL run: its own state cannot carry the request, so record it
		// DURABLY before the best-effort walk. A crash or a per-branch storage
		// error part-way through the walk then leaves runs the orphan sweep
		// (ListOrphanedActiveChildren) recognises and finishes. Fail closed: a
		// success answer must always mean the request survives a restart.
		if err := e.spawnLinks.MarkSubtreeCancelRequested(context.WithoutCancel(ctx), instanceID); err != nil {
			return fmt.Errorf("recording subtree cancellation of %s: %w", instanceID, err)
		}
	}
	//
	// DETACH the cascade from the caller's request. The walk MUST run to
	// completion: on the request context, a client disconnect or a write
	// timeout part-way through would silently strand the rest of the subtree —
	// active descendants running with the parent's inherited credentials and
	// nobody watching. This is the same posture OnChildTerminal already takes
	// at its call sites.
	//
	// context.WithoutCancel (not context.Background) because cancelAuthorized
	// re-authorizes every descendant from the identity carried on ctx: dropping
	// the values would turn the cascade into an unauthenticated walk. Only
	// cancellation and the deadline are shed.
	stopped := e.cascadeCancel(context.WithoutCancel(ctx), instanceID, map[uuid.UUID]bool{instanceID: true})
	if ownErr != nil && stopped == 0 {
		return ownErr
	}
	return nil
}
```

(`errors` and `fmt` are already imported by engine.go.)

(d) `domain/engine/orphan_reconciler.go` — documentation only. Replace the last paragraph of the type doc comment (lines 47-48, "Cancel cascades, so cancelling a top-level orphan tears down its own subtree in the same pass; the query only needs to be one level deep.") with:

```go
// The query climbs retained ancestry THROUGH terminal intermediates
// (ListOrphanedActiveChildren): the cascade itself walks through completed
// branches and children, so its residue can sit several levels below the
// cancelled run. An active intermediate is returned instead of what lies below
// it, and Cancel cascades from there. A run returned twice in one pass (already
// cancelled by an earlier orphan's cascade) yields ErrCannotCancel, the benign
// case below.
```

and replace the `Reconcile` doc comment (lines 71-77) with:

```go
// Reconcile cancels every active run that has a cancelled ancestor (or a
// terminal ancestor whose subtree cancellation Engine.Cancel recorded)
// reachable through terminal intermediates.
//
// The query error is RETURNED so the caller can surface it; a per-orphan
// failure is logged and the sweep continues — one uncooperative run must not
// abandon the rest. An orphan that reached a terminal state between the query
// and the cancel (or was already reached by an earlier orphan's cascade in this
// pass) returns ErrCannotCancel, the COMMON benign race, and is not logged as a
// failure.
```

- [x] Step 9: Run the tests, confirm they pass; then the wider suites.

All task-focused engine and real-root E2E tests passed, then passed again with `-race -count=3`. The full SQLite suite passed both normally and with `-race`. The wider requested gate passed every listed package except the already-blocked P2-T11 engine integration (`TestParallelGroupExecutor_EngineIntegration_ParentVariablesHoldOrderedResultsWithoutMerging`, waiting parent never queues because terminal receipts are missing). No task-local test failed.

```bash
go test ./domain/engine/ -run 'TestCancelSubtree|TestCancelCascade|TestOrphanReconciler|TestNoopSpawnLinks|TestSetSpawnLinks|TestEnricherChildCount' -count=1
go test ./cmd/arbitron/ -run 'TestCancelSubtreeE2E|TestSpawnWaitE2E_CancellingParentCascades' -v -count=1
go test ./domain/... ./adapter/sqlite/ ./adapter/http/ ./adapter/mcp/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
--- PASS: TestCancelSubtreeE2E_CompletedParentStopsRunningChildThroughCompositionRoot
--- PASS: TestCancelSubtreeE2E_CascadeCrossesCompletedIntermediateThroughCompositionRoot
--- PASS: TestCancelSubtreeE2E_TerminalRunWithNothingActiveStillRefuses
--- PASS: TestSpawnWaitE2E_CancellingParentCascadesToActiveChildThroughCompositionRoot
ok  	github.com/arbitron/arbitron/cmd/arbitron	X.XXXs
ok  	... (every listed package)
```
Notes for the implementer: `TestCancelCascade_AlreadyTerminalChildStillDescendsIntoActiveGrandchild` and the two `VisitedGuard...` tests still set `unfiltered = true`; that flag now only affects the unused `ListActiveChildren`, and those tests keep passing because `ListChildren` returns the same links. `engine_test.go:2060` (cancelling a completed instance with no children) still expects `"cannot be cancelled"` and still gets it (`NoopSpawnLinks.ListChildren` reports none).

- [x] Step 10: gofmt, vet, commit.

Self-review completed in order: (1) spec compliance — only the requested traversal, durable intent, terminal-parent semantics and documentation changed; existing composition-root ports supply real child rows. (2) anti-green-theater — real `initDomain`, SQLite, queue, engine and spawned processes drive persisted descendant cancellation and the real intent stamp; strengthened E2E assertions preserve the completed parent's full persisted state, timestamps, variables and outcome. (3) code quality — followed neighboring repository/error conventions, retained authorization and detached-context ordering, bounded corrupt cycles, formatted and vetted the affected packages. Focused engine/E2E and full SQLite race tests passed after review. Two task commits implement the two planned TDD cycles; pushed the branch.

```bash
gofmt -l domain/instance domain/engine adapter/sqlite cmd/arbitron
go vet ./domain/engine/ ./domain/instance/ ./adapter/sqlite/ ./cmd/arbitron/
git add domain/instance/spawn_link.go domain/engine/cancel_cascade.go domain/engine/engine.go domain/engine/orphan_reconciler.go domain/engine/cancel_cascade_test.go domain/engine/enricher_child_count_test.go domain/engine/cancel_subtree_test.go cmd/arbitron/cancel_subtree_e2e_test.go
git commit -m "feat(engine): cancel subtrees through terminal intermediates and after parent completion" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
(`gofmt -l` and `go vet` print nothing.)

Behaviour change to call out in the PR description: `POST /instances/{id}/cancel` and MCP `instance_cancel` on a completed/failed run now return success (and stop its active descendants) when it has any; the run itself stays completed/failed. With no active descendants they keep returning 409 / `ErrCannotCancel`.
