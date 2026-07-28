# Phase 4 — Task 5: the spawn-wait reconciler

**Goal:** Guarantee that no parent waits forever on a child that is already finished — find waiting parents whose awaited child is terminal and re-queue them at startup.

**Architecture:** New file `domain/engine/spawn_reconciler.go`. An in-process wake is lost if the process dies between the child's terminal write and the parent's re-queue; the reconciler closes that hole and is **the authority for correctness** (the in-process hooks only make the common case immediate). It runs at startup beside `RecoverCrashedInstances` and the queue's `RecoverTimers`/`RecoverWebhooks`, and **after** `RecoverCrashedInstances` — that sweep marks crashed *children* as failed, so running afterwards resumes their parents through `failure` in the same startup pass instead of hanging until the next restart. A query failure is logged and surfaced but must not prevent the process from starting, matching the posture of the existing recovery sweeps.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 5: reconciler + startup wiring

**Files:**
- Create: `domain/engine/spawn_reconciler.go`
- Modify: `cmd/arbitron/main.go:988-1000` (startup sequence, after `RecoverCrashedInstances`)
- Test:   `domain/engine/spawn_reconciler_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/spawn_reconciler_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconcilerFixture wires a reconciler over the shared engine test fixtures.
type reconcilerFixture struct {
	f          *testFixtures
	spawnWaits *fakeSpawnWaits
	rec        *engine.SpawnWaitReconciler
}

func newReconcilerFixture(t *testing.T) *reconcilerFixture {
	t.Helper()
	f := newTestFixtures()
	sw := newFakeSpawnWaits()
	return &reconcilerFixture{
		f:          f,
		spawnWaits: sw,
		rec:        engine.NewSpawnWaitReconciler(sw, f.instanceRepo, &mockQueueRepo{instRepo: f.instanceRepo}, f.queueManager),
	}
}

// seedWaitingParent creates a waiting parent at the given node.
func (r *reconcilerFixture) seedWaitingParent(t *testing.T, nodeID string) uuid.UUID {
	t.Helper()
	node := nodeID
	p := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "parent",
		State: instance.StateWaiting, Priority: instance.PriorityNormal, CurrentNodeID: &node,
	}
	require.NoError(t, r.f.instanceRepo.Create(context.Background(), p))
	return p.ID
}

func (r *reconcilerFixture) stateOf(t *testing.T, id uuid.UUID) instance.State {
	t.Helper()
	got, err := r.f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return got.State
}

// ---------- happy path ----------

func TestSpawnWaitReconciler_ReQueuesParentWhoseChildIsAlreadyTerminal(t *testing.T) {
	r := newReconcilerFixture(t)
	parent := r.seedWaitingParent(t, "spawn-1")
	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{{
		ParentID: parent, ParentNodeID: "spawn-1",
		ChildID: uuid.New(), ChildState: instance.StateCompleted,
	}}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateQueued, r.stateOf(t, parent),
		"an in-process wake lost to a crash must be recovered at startup")
	assert.GreaterOrEqual(t, r.f.queueManager.notifyCount(), 1)
}

// ---------- success ----------

func TestSpawnWaitReconciler_ReQueuesEveryCandidate(t *testing.T) {
	r := newReconcilerFixture(t)
	p1 := r.seedWaitingParent(t, "spawn-1")
	p2 := r.seedWaitingParent(t, "spawn-2")
	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{
		{ParentID: p1, ParentNodeID: "spawn-1", ChildID: uuid.New(), ChildState: instance.StateFailed},
		{ParentID: p2, ParentNodeID: "spawn-2", ChildID: uuid.New(), ChildState: instance.StateCancelled},
	}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateQueued, r.stateOf(t, p1))
	assert.Equal(t, instance.StateQueued, r.stateOf(t, p2))
}

// Running AFTER RecoverCrashedInstances is what lets a parent whose child
// crashed mid-flight resume in the SAME startup pass: that sweep marks the
// crashed child failed, which makes it a candidate here.
func TestSpawnWaitReconciler_ResumesParentOfACrashedChildAfterRecovery(t *testing.T) {
	r := newReconcilerFixture(t)
	ctx := context.Background()
	parent := r.seedWaitingParent(t, "spawn-1")

	// A child left RUNNING by a crash.
	pid := parent
	node := "spawn-1"
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "child",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeWaitForChild,
	}
	require.NoError(t, r.f.instanceRepo.Create(ctx, child))

	// RecoverCrashedInstances runs FIRST and fails the crashed child.
	require.NoError(t, r.f.engine.RecoverCrashedInstances())
	got, err := r.f.instanceRepo.Get(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, instance.StateFailed, got.State)

	// Only now is the parent a candidate.
	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{{
		ParentID: parent, ParentNodeID: "spawn-1", ChildID: child.ID, ChildState: instance.StateFailed,
	}}
	require.NoError(t, r.rec.Reconcile(ctx))

	assert.Equal(t, instance.StateQueued, r.stateOf(t, parent),
		"a parent whose child crashed mid-flight resumes in the SAME startup pass")
}

// ---------- failure / edge cases ----------

func TestSpawnWaitReconciler_NoCandidatesIsANoOp(t *testing.T) {
	r := newReconcilerFixture(t)

	require.NoError(t, r.rec.Reconcile(context.Background()))
	assert.Equal(t, 0, r.f.queueManager.notifyCount())
}

// A parent that is no longer waiting (cancelled between the query and the
// re-queue) must NOT be resurrected.
func TestSpawnWaitReconciler_NonWaitingParentIsSkipped(t *testing.T) {
	r := newReconcilerFixture(t)
	ctx := context.Background()
	parent := r.seedWaitingParent(t, "spawn-1")
	require.NoError(t, r.f.instanceRepo.UpdateState(ctx, parent, instance.StateCancelled))

	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{{
		ParentID: parent, ParentNodeID: "spawn-1", ChildID: uuid.New(), ChildState: instance.StateCompleted,
	}}
	require.NoError(t, r.rec.Reconcile(ctx))

	assert.Equal(t, instance.StateCancelled, r.stateOf(t, parent),
		"a cancelled parent must never be resurrected by the reconciler")
}

// A parent that has since advanced past the awaited node is skipped.
func TestSpawnWaitReconciler_NodeMismatchIsSkipped(t *testing.T) {
	r := newReconcilerFixture(t)
	parent := r.seedWaitingParent(t, "spawn-B")

	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{{
		ParentID: parent, ParentNodeID: "spawn-A", ChildID: uuid.New(), ChildState: instance.StateCompleted,
	}}
	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateWaiting, r.stateOf(t, parent))
}

// ---------- error ----------

// The query failing must be SURFACED but must not block startup — the same
// posture the existing recovery sweeps take.
func TestSpawnWaitReconciler_QueryFailureIsReturnedNotPanicked(t *testing.T) {
	r := newReconcilerFixture(t)
	sentinel := errors.New("db down")
	r.spawnWaits.listErr = sentinel

	err := r.rec.Reconcile(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

// A re-queue failure for one candidate must not abandon the rest.
func TestSpawnWaitReconciler_OneFailureDoesNotAbandonTheRest(t *testing.T) {
	r := newReconcilerFixture(t)
	good := r.seedWaitingParent(t, "spawn-1")
	missing := uuid.New() // no such instance

	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{
		{ParentID: missing, ParentNodeID: "spawn-1", ChildID: uuid.New(), ChildState: instance.StateCompleted},
		{ParentID: good, ParentNodeID: "spawn-1", ChildID: uuid.New(), ChildState: instance.StateCompleted},
	}
	require.NoError(t, r.rec.Reconcile(context.Background()),
		"one uncooperative candidate must not fail the whole sweep")
	assert.Equal(t, instance.StateQueued, r.stateOf(t, good))
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestSpawnWaitReconciler
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/spawn_reconciler_test.go:...: undefined: engine.SpawnWaitReconciler
domain/engine/spawn_reconciler_test.go:...: undefined: engine.NewSpawnWaitReconciler
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [ ] **Step 3: Minimal implementation**

Create `domain/engine/spawn_reconciler.go`:

```go
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
)

// SpawnWaitReconciler guarantees that no parent waits forever on a child that
// is already finished.
//
// An in-process wake (Engine.OnChildTerminal) is LOST if the process dies
// between the child's terminal write and the parent's re-queue. This sweep
// closes that hole and is therefore THE AUTHORITY FOR CORRECTNESS: the
// in-process hooks only make the common case immediate.
//
// STARTUP ORDERING IS SIGNIFICANT: it must run AFTER
// Engine.RecoverCrashedInstances. That sweep marks instances left in the
// running state by a crash as failed — which includes crashed CHILDREN — so
// running afterwards means those children are already terminal and their
// parents resume through the failure port in the SAME startup pass instead of
// hanging until the next restart. A waiting parent is itself untouched by
// RecoverCrashedInstances, since it is not in the running state.
type SpawnWaitReconciler struct {
	spawnWaits instance.SpawnWaitRepository
	instances  instance.InstanceRepository
	queueRepo  instance.QueueRepository
	queueMgr   QueueManager
}

// NewSpawnWaitReconciler constructs the reconciler.
func NewSpawnWaitReconciler(
	spawnWaits instance.SpawnWaitRepository,
	instances instance.InstanceRepository,
	queueRepo instance.QueueRepository,
	queueMgr QueueManager,
) *SpawnWaitReconciler {
	return &SpawnWaitReconciler{
		spawnWaits: spawnWaits,
		instances:  instances,
		queueRepo:  queueRepo,
		queueMgr:   queueMgr,
	}
}

// Reconcile re-queues every waiting parent whose awaited child is already
// terminal.
//
// The query error is RETURNED so the caller can surface it; the composition
// root logs it without blocking startup, matching the posture of the existing
// recovery sweeps. A per-candidate failure is logged and the sweep continues —
// one uncooperative parent must not abandon the rest.
func (r *SpawnWaitReconciler) Reconcile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	candidates, err := r.spawnWaits.ListWaitingOnTerminalChild(ctx)
	if err != nil {
		return fmt.Errorf("spawn-wait reconciler: listing candidates: %w", err)
	}

	requeued := 0
	for _, c := range candidates {
		// Re-verify against the CURRENT row: the query result may be stale (the
		// parent could have been cancelled, or already woken in-process, between
		// the query and now). A cancelled parent must never be resurrected.
		parent, gerr := r.instances.Get(ctx, c.ParentID)
		if gerr != nil {
			slog.Warn("spawn-wait reconciler: skipping unreadable parent",
				"parentID", c.ParentID, "error", gerr)
			continue
		}
		if parent.State != instance.StateWaiting {
			continue
		}
		if parent.CurrentNodeID == nil || *parent.CurrentNodeID != c.ParentNodeID {
			continue
		}

		if serr := r.queueRepo.SetQueued(ctx, c.ParentID, time.Now()); serr != nil {
			slog.Error("spawn-wait reconciler: failed to re-queue parent",
				"parentID", c.ParentID, "childID", c.ChildID, "error", serr)
			continue
		}
		requeued++
	}

	if requeued > 0 {
		slog.Info("spawn-wait reconciler: re-queued parents whose awaited child had already finished",
			"count", requeued)
		r.queueMgr.NotifyCapacityFreed(ctx)
	}
	return nil
}
```

- [ ] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run TestSpawnWaitReconciler -v
```

Expected output:

```
--- PASS: TestSpawnWaitReconciler_ReQueuesParentWhoseChildIsAlreadyTerminal (0.00s)
--- PASS: TestSpawnWaitReconciler_ReQueuesEveryCandidate (0.00s)
--- PASS: TestSpawnWaitReconciler_ResumesParentOfACrashedChildAfterRecovery (0.00s)
--- PASS: TestSpawnWaitReconciler_NoCandidatesIsANoOp (0.00s)
--- PASS: TestSpawnWaitReconciler_NonWaitingParentIsSkipped (0.00s)
--- PASS: TestSpawnWaitReconciler_NodeMismatchIsSkipped (0.00s)
--- PASS: TestSpawnWaitReconciler_QueryFailureIsReturnedNotPanicked (0.00s)
--- PASS: TestSpawnWaitReconciler_OneFailureDoesNotAbandonTheRest (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.6s
```

- [ ] **Step 5: Wire it into the startup sequence**

In `cmd/arbitron/main.go`, immediately after the `RecoverCrashedInstances` block
(which ends with its closing `}` around line 1000) and BEFORE
`queueMgr.RecoverTimers`, insert:

```go
	// 14-spawn. Reconcile parents stuck waiting on a child that already finished.
	//
	// ORDERING IS SIGNIFICANT: this runs AFTER RecoverCrashedInstances, which
	// marks crashed CHILDREN as failed — so their parents resume through the
	// failure port in this same startup pass instead of hanging until the next
	// restart. A waiting parent is itself untouched by that sweep, since it is not
	// in the running state.
	//
	// A failure here is logged, NOT fatal: the same posture as the timer/webhook
	// recovery sweeps. A parent missed on this boot is picked up on the next one.
	logger.Info("reconciling parents waiting on spawned children")
	spawnReconciler := engine.NewSpawnWaitReconciler(instanceRepo, instanceRepo, queueRepo, queueMgr)
	if err := spawnReconciler.Reconcile(context.Background()); err != nil {
		logger.Error("spawn-wait reconciliation incomplete", "error", err)
	}
```

- [ ] **Step 6: Verify the startup ordering is what the plan says**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
grep -n "RecoverCrashedInstances\|spawnReconciler.Reconcile\|RecoverTimers" cmd/arbitron/main.go
```

Expected output — the reconciler line number must be GREATER than the
`RecoverCrashedInstances` line and LESS than `RecoverTimers`:

```
990:	if err := eng.RecoverCrashedInstances(); err != nil {
1004:	if err := spawnReconciler.Reconcile(context.Background()); err != nil {
1010:	if err := queueMgr.RecoverTimers(context.Background()); err != nil {
```

- [ ] **Step 7: Full Go gate**

```bash
gofmt -w cmd/arbitron/ && go build ./... && go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [ ] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/spawn_reconciler.go domain/engine/spawn_reconciler_test.go cmd/arbitron/main.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): reconcile parents waiting on already-finished children at startup"
```
