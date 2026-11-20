package engine_test

import (
	"context"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdopter records adoption calls and, unless decline is set, moves the
// parent running -> waiting in the fixture repo, as Engine.AdoptStrandedParent
// does for a parent with no live walker.
type fakeAdopter struct {
	mu      sync.Mutex
	repo    *mockInstanceRepo
	decline bool
	calls   []string
}

func (a *fakeAdopter) AdoptStrandedParent(ctx context.Context, parentID uuid.UUID, nodeID string) (bool, error) {
	a.mu.Lock()
	a.calls = append(a.calls, parentID.String()+"@"+nodeID)
	decline := a.decline
	a.mu.Unlock()
	if decline {
		return false, nil
	}
	return true, a.repo.UpdateState(ctx, parentID, instance.StateWaiting)
}

func (r *reconcilerFixture) seedRunningParent(t *testing.T, nodeID string) uuid.UUID {
	t.Helper()
	id := r.seedWaitingParent(t, nodeID)
	require.NoError(t, r.f.instanceRepo.UpdateState(context.Background(), id, instance.StateRunning))
	return id
}

func strandedCandidate(parent uuid.UUID, node string, child instance.State) instance.SpawnWaitCandidate {
	return instance.SpawnWaitCandidate{ParentID: parent, ParentNodeID: node, ChildID: uuid.New(), ChildState: child}
}

func TestSpawnWaitReconciler_AdoptsStrandedParent_FailedChild(t *testing.T) {
	r := newReconcilerFixture(t)
	a := &fakeAdopter{repo: r.f.instanceRepo}
	r.rec.WithStrandedAdoption(a)
	parent := r.seedRunningParent(t, "spawn-1")
	r.spawnWaits.runningCandidates = []instance.SpawnWaitCandidate{strandedCandidate(parent, "spawn-1", instance.StateFailed)}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, []string{parent.String() + "@spawn-1"}, a.calls)
	assert.Equal(t, instance.StateQueued, r.stateOf(t, parent))
	assert.GreaterOrEqual(t, r.f.queueManager.notifyCount(), 1)
}

func TestSpawnWaitReconciler_AdoptsStrandedParent_CompletedChild(t *testing.T) {
	r := newReconcilerFixture(t)
	a := &fakeAdopter{repo: r.f.instanceRepo}
	r.rec.WithStrandedAdoption(a)
	parent := r.seedRunningParent(t, "spawn-1")
	r.spawnWaits.runningCandidates = []instance.SpawnWaitCandidate{strandedCandidate(parent, "spawn-1", instance.StateCompleted)}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateQueued, r.stateOf(t, parent))
}

// The adopted parent is absent from the waiting-candidate list the reconciler
// read (it was running when listed), yet it must be re-queued in the same tick.
func TestSpawnWaitReconciler_AdoptedParentIsRequeuedSameTick(t *testing.T) {
	r := newReconcilerFixture(t)
	r.rec.WithStrandedAdoption(&fakeAdopter{repo: r.f.instanceRepo})
	parent := r.seedRunningParent(t, "spawn-1")
	r.spawnWaits.runningCandidates = []instance.SpawnWaitCandidate{strandedCandidate(parent, "spawn-1", instance.StateFailed)}
	r.spawnWaits.candidates = nil

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateQueued, r.stateOf(t, parent))
}

func TestSpawnWaitReconciler_AdopterDeclines_ParentUntouched(t *testing.T) {
	r := newReconcilerFixture(t)
	r.rec.WithStrandedAdoption(&fakeAdopter{repo: r.f.instanceRepo, decline: true})
	parent := r.seedRunningParent(t, "spawn-1")
	r.spawnWaits.runningCandidates = []instance.SpawnWaitCandidate{strandedCandidate(parent, "spawn-1", instance.StateFailed)}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateRunning, r.stateOf(t, parent), "a live walker's parent is never moved")
}

func TestSpawnWaitReconciler_NoAdopter_SkipsPass(t *testing.T) {
	r := newReconcilerFixture(t)
	parent := r.seedRunningParent(t, "spawn-1")
	r.spawnWaits.runningCandidates = []instance.SpawnWaitCandidate{strandedCandidate(parent, "spawn-1", instance.StateFailed)}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, 0, r.spawnWaits.runningListCalls)
	assert.Equal(t, instance.StateRunning, r.stateOf(t, parent))
}

// A failing adoption listing must not stop the existing waiting pass.
func TestSpawnWaitReconciler_StrandedListFailure_WaitingPassStillRuns(t *testing.T) {
	r := newReconcilerFixture(t)
	r.rec.WithStrandedAdoption(&fakeAdopter{repo: r.f.instanceRepo})
	r.spawnWaits.runningListErr = assert.AnError
	waiting := r.seedWaitingParent(t, "spawn-1")
	r.spawnWaits.candidates = []instance.SpawnWaitCandidate{strandedCandidate(waiting, "spawn-1", instance.StateCompleted)}

	require.NoError(t, r.rec.Reconcile(context.Background()))

	assert.Equal(t, instance.StateQueued, r.stateOf(t, waiting))
}
