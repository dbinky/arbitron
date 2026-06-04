package engine_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	engine "github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dismissalRecorder is a minimal instance.DismissalRepository for engine tests.
type dismissalRecorder struct {
	mu        sync.Mutex
	dismissed map[uuid.UUID]map[uuid.UUID]bool // instanceID -> userID -> true
	cleared   []uuid.UUID
}

func newDismissalRecorder() *dismissalRecorder {
	return &dismissalRecorder{dismissed: map[uuid.UUID]map[uuid.UUID]bool{}}
}
func (d *dismissalRecorder) Dismiss(_ context.Context, u, i uuid.UUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dismissed[i] == nil {
		d.dismissed[i] = map[uuid.UUID]bool{}
	}
	d.dismissed[i][u] = true
	return nil
}
func (d *dismissalRecorder) DismissMany(_ context.Context, u uuid.UUID, ids []uuid.UUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, i := range ids {
		if d.dismissed[i] == nil {
			d.dismissed[i] = map[uuid.UUID]bool{}
		}
		d.dismissed[i][u] = true
	}
	return nil
}
func (d *dismissalRecorder) Restore(_ context.Context, u, i uuid.UUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dismissed[i] != nil {
		delete(d.dismissed[i], u)
	}
	return nil
}
func (d *dismissalRecorder) DismissedIDs(_ context.Context, u uuid.UUID) (map[uuid.UUID]bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[uuid.UUID]bool{}
	for i, users := range d.dismissed {
		if users[u] {
			out[i] = true
		}
	}
	return out, nil
}
func (d *dismissalRecorder) ClearForInstance(_ context.Context, i uuid.UUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleared = append(d.cleared, i)
	delete(d.dismissed, i)
	return nil
}
func (d *dismissalRecorder) clearedCount(i uuid.UUID) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.cleared {
		if c == i {
			n++
		}
	}
	return n
}

var _ instance.DismissalRepository = (*dismissalRecorder)(nil)

// errClearRepo delegates everything to the embedded recorder but always fails ClearForInstance.
type errClearRepo struct {
	*dismissalRecorder
	err error
}

func (e *errClearRepo) ClearForInstance(_ context.Context, _ uuid.UUID) error { return e.err }

var _ instance.DismissalRepository = (*errClearRepo)(nil)

// Integration: retrying a dismissed FAILED instance clears its dismissal so it
// resurfaces. Uses a single-node workflow whose node fails once then succeeds.
func TestEngine_RetryClearsDismissal(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	dis := newDismissalRecorder()
	f.engine.SetDismissals(dis)

	// Node A fails the first time, succeeds on retry.
	firstRun := true
	var mu sync.Mutex
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		isFirst := firstRun
		firstRun = false
		mu.Unlock()
		if isFirst {
			return nil, fmt.Errorf("node A exploded")
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, _, err := f.engine.Trigger(bgAdmin(), wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// User dismisses the failed instance.
	user := uuid.New()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))

	// Retry it.
	require.NoError(t, f.engine.Retry(bgAdmin(), id, nil))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.GreaterOrEqual(t, dis.clearedCount(id), 1, "Retry must ClearForInstance the dismissal")
	ids, _ := dis.DismissedIDs(context.Background(), user)
	assert.False(t, ids[id], "after retry the instance is no longer dismissed for the user")
}

// Success: Retry clears dismissals for ALL users who dismissed the instance —
// ClearForInstance is a cross-user operation, not a per-user Restore. A bug that
// called Restore(user, id) only for a known single user would pass the single-user
// happy test above but fail here.
func TestEngine_RetryClearsDismissalForAllUsers(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	dis := newDismissalRecorder()
	f.engine.SetDismissals(dis)

	firstRun := true
	var mu sync.Mutex
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		isFirst := firstRun
		firstRun = false
		mu.Unlock()
		if isFirst {
			return nil, fmt.Errorf("node A failed")
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, _, err := f.engine.Trigger(bgAdmin(), wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// Two distinct users dismiss the same failed instance.
	userA := uuid.New()
	userB := uuid.New()
	require.NoError(t, dis.Dismiss(context.Background(), userA, id))
	require.NoError(t, dis.Dismiss(context.Background(), userB, id))

	require.NoError(t, f.engine.Retry(bgAdmin(), id, nil))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	idsA, _ := dis.DismissedIDs(context.Background(), userA)
	idsB, _ := dis.DismissedIDs(context.Background(), userB)
	assert.False(t, idsA[id], "after retry user A's dismissal must be cleared")
	assert.False(t, idsB[id], "after retry user B's dismissal must be cleared")
}

// Rerun produces a NEW instance id with no dismissal rows; the old instance's
// dismissal (if any) is untouched.
func TestEngine_RerunYieldsUndismissedNewInstance(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	dis := newDismissalRecorder()
	f.engine.SetDismissals(dis)

	id, _, err := f.engine.Trigger(bgAdmin(), wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	// Dismiss the original completed instance for a user.
	user := uuid.New()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))

	newID, err := f.engine.Rerun(bgAdmin(), id)
	require.NoError(t, err)
	assert.NotEqual(t, id, newID, "rerun must create a new instance id")

	ids, _ := dis.DismissedIDs(context.Background(), user)
	assert.True(t, ids[id], "original instance stays dismissed (historical)")
	assert.False(t, ids[newID], "new instance has no dismissal rows")
	assert.Equal(t, 0, dis.clearedCount(newID), "rerun does not clear dismissals")
}

// Edge: Retry calls ClearForInstance even when the instance was never dismissed —
// the clear is unconditional (idempotent no-op when no rows exist). A conditional
// "only clear if dismissed" optimization would violate this invariant.
func TestEngine_RetryCallsClearEvenIfNotDismissed(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	dis := newDismissalRecorder()
	f.engine.SetDismissals(dis)

	firstRun := true
	var mu sync.Mutex
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		isFirst := firstRun
		firstRun = false
		mu.Unlock()
		if isFirst {
			return nil, fmt.Errorf("node A failed")
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, _, err := f.engine.Trigger(bgAdmin(), wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// Do NOT dismiss — retry must call ClearForInstance regardless.
	require.NoError(t, f.engine.Retry(bgAdmin(), id, nil))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.GreaterOrEqual(t, dis.clearedCount(id), 1,
		"Retry must always call ClearForInstance, even when the instance was never dismissed")
}

// When the dismissal repo fails to clear on Retry, Retry propagates the error
// before launching the run goroutine.
func TestEngine_RetryClearDismissalError(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	f.engine.SetDismissals(&errClearRepo{
		dismissalRecorder: newDismissalRecorder(),
		err:               fmt.Errorf("db down"),
	})

	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node A failed")
	})

	id, _, err := f.engine.Trigger(bgAdmin(), wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	retryErr := f.engine.Retry(bgAdmin(), id, nil)
	assert.ErrorContains(t, retryErr, "failed to clear dismissals on retry")

	// Ordering invariant: ClearForInstance runs BEFORE the instance is flipped to
	// running, so a clear failure must NOT leave the instance stuck in running
	// with no run goroutine. It stays failed and remains retryable.
	inst, err := f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, inst.State,
		"clear-failure must not strand the instance in running (stuck-running window)")
}
