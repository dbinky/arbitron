package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedInstance puts an instance owned by owner in the given state into the repo.
func seedInstance(f *testFixtures, owner uuid.UUID, state instance.State) uuid.UUID {
	id := uuid.New()
	failedNode := "n1"
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:                id,
		OwnerID:           owner,
		WorkflowName:      "wf",
		State:             state,
		Priority:          instance.PriorityNormal,
		Variables:         map[string]any{"k": "v"},
		OriginalVariables: map[string]any{"k": "v"},
		FailedNodeID:      &failedNode,
	}
	f.instanceRepo.mu.Unlock()
	return id
}

func currentState(t *testing.T, f *testFixtures, id uuid.UUID) instance.State {
	t.Helper()
	inst, err := f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return inst.State
}

// --- Happy path: owner may pause/cancel/setpriority ---

func TestIntervention_Owner_CanAct(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	ctx := userCtx(owner)

	running := seedInstance(f, owner, instance.StateRunning)
	require.NoError(t, f.engine.Pause(ctx, running))
	assert.Equal(t, instance.StatePaused, currentState(t, f, running))

	queued := seedInstance(f, owner, instance.StateQueued)
	require.NoError(t, f.engine.SetPriority(ctx, queued, "high"))

	cancelable := seedInstance(f, owner, instance.StateQueued)
	require.NoError(t, f.engine.Cancel(ctx, cancelable))
	assert.Equal(t, instance.StateCancelled, currentState(t, f, cancelable))
}

// --- Validation: admin (non-owner) may act ---

func TestIntervention_Admin_CanAct(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	admin := adminCtx(uuid.New()) // not the owner

	running := seedInstance(f, owner, instance.StateRunning)
	require.NoError(t, f.engine.Pause(admin, running))
	assert.Equal(t, instance.StatePaused, currentState(t, f, running))

	queued := seedInstance(f, owner, instance.StateQueued)
	require.NoError(t, f.engine.Cancel(admin, queued))
	assert.Equal(t, instance.StateCancelled, currentState(t, f, queued))
}

// --- Error/failure: viewer in a non-actionable state gets the state sentinel ---

func TestIntervention_OwnerWrongState_StateSentinel(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	ctx := userCtx(owner)

	// Pause on a completed instance -> ErrNotRunning (gate passes, state fails).
	completed := seedInstance(f, owner, instance.StateCompleted)
	err := f.engine.Pause(ctx, completed)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotRunning)
	assert.NotErrorIs(t, err, instance.ErrNotFound)

	// Retry on a running instance -> ErrNotFailed.
	running := seedInstance(f, owner, instance.StateRunning)
	err = f.engine.Retry(ctx, running, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFailed)

	// Rerun on a running (non-terminal) instance -> ErrCannotRerun.
	_, err = f.engine.Rerun(ctx, running)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrCannotRerun)
}

// --- Error/failure: infra load error propagates (not masked as not-found) ---

func TestIntervention_InfraError_Propagates(t *testing.T) {
	f := newTestFixtures()
	sentinel := errors.New("db connection reset")
	f.instanceRepo.getErr = sentinel
	err := f.engine.Pause(userCtx(uuid.New()), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "infra load error propagates")
	assert.NotErrorIs(t, err, instance.ErrNotFound, "infra error is not masked as not-found")
}

// --- Boundary: Rerun by an admin owns the new instance ---

func TestIntervention_RerunByAdmin_NewInstanceOwnedByActor(t *testing.T) {
	f := newTestFixtures()
	// A workflow Trigger can resolve for the acting admin (the mock repo's Load
	// ignores scope and reads a flat map).
	f.workflowRepo.mu.Lock()
	f.workflowRepo.workflows["wf"] = &workflow.Workflow{
		Name:  "wf",
		Nodes: []workflow.Node{{ID: "n1", Type: workflow.NodeTypeScript, Entrypoint: true}},
	}
	f.workflowRepo.mu.Unlock()
	// Keep the new instance queued so we can inspect its owner without a run.
	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(_ context.Context, _ uuid.UUID, _ uuid.UUID) (bool, error) {
		return false, nil
	}
	f.queueManager.mu.Unlock()

	owner := uuid.New()
	source := seedInstance(f, owner, instance.StateCompleted)
	adminID := uuid.New()

	newID, err := f.engine.Rerun(adminCtx(adminID), source)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, newID)

	newInst, err := f.instanceRepo.Get(context.Background(), newID)
	require.NoError(t, err)
	assert.Equal(t, adminID, newInst.OwnerID, "the new instance is owned by the acting admin, not the source owner")

	// Source untouched.
	src, err := f.instanceRepo.Get(context.Background(), source)
	require.NoError(t, err)
	assert.Equal(t, owner, src.OwnerID)
	assert.Equal(t, instance.StateCompleted, src.State)
}

// --- Isolation/security: non-owner non-admin refused, no state change ---

func TestIntervention_NonOwner_RefusedNoStateChange(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	intruder := userCtx(uuid.New())

	running := seedInstance(f, owner, instance.StateRunning)
	queued := seedInstance(f, owner, instance.StateQueued)
	failed := seedInstance(f, owner, instance.StateFailed)
	terminal := seedInstance(f, owner, instance.StateCompleted)

	assert.ErrorIs(t, f.engine.Pause(intruder, running), instance.ErrNotFound)
	assert.ErrorIs(t, f.engine.Resume(intruder, running), instance.ErrNotFound)
	assert.ErrorIs(t, f.engine.Cancel(intruder, running), instance.ErrNotFound)
	assert.ErrorIs(t, f.engine.SetPriority(intruder, queued, "high"), instance.ErrNotFound)
	assert.ErrorIs(t, f.engine.Retry(intruder, failed, nil), instance.ErrNotFound)
	_, err := f.engine.Rerun(intruder, terminal)
	assert.ErrorIs(t, err, instance.ErrNotFound)

	// No state changed for any of them.
	assert.Equal(t, instance.StateRunning, currentState(t, f, running))
	assert.Equal(t, instance.StateQueued, currentState(t, f, queued))
	assert.Equal(t, instance.StateFailed, currentState(t, f, failed))
	assert.Equal(t, instance.StateCompleted, currentState(t, f, terminal))
}
