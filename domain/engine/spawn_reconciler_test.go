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

// ListWaitingOnTerminalChild's JOIN produces one row per terminal child, so a
// node-level retry that left more than one terminal child for the same
// (parent, node) key surfaces as TWO candidates naming the SAME parent. The
// reconciler must not double-queue or error on the second: its own
// re-verification against the current row (parent.State != waiting after the
// first SetQueued) is what makes the duplicate harmless, and this test is
// what would catch a regression if that guard were ever weakened to key on
// something the second candidate could still satisfy.
func TestSpawnWaitReconciler_DuplicateCandidatesForSameParentAreHandledOnce(t *testing.T) {
	r := newReconcilerFixture(t)
	parent := r.seedWaitingParent(t, "spawn-1")
	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{
		{ParentID: parent, ParentNodeID: "spawn-1", ChildID: uuid.New(), ChildState: instance.StateFailed},
		{ParentID: parent, ParentNodeID: "spawn-1", ChildID: uuid.New(), ChildState: instance.StateCompleted},
	}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateQueued, r.stateOf(t, parent))
	assert.Equal(t, 1, r.f.queueManager.notifyCount(), "one notify per Reconcile call, not one per duplicate candidate")
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
