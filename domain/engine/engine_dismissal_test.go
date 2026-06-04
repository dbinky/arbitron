package engine_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	engine "github.com/arbitron/arbitron/domain/engine"
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

	id, _, err := f.engine.Trigger(wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// User dismisses the failed instance.
	user := uuid.New()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))

	// Retry it.
	require.NoError(t, f.engine.Retry(id, nil))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.GreaterOrEqual(t, dis.clearedCount(id), 1, "Retry must ClearForInstance the dismissal")
	ids, _ := dis.DismissedIDs(context.Background(), user)
	assert.False(t, ids[id], "after retry the instance is no longer dismissed for the user")
}

// Rerun produces a NEW instance id with no dismissal rows; the old instance's
// dismissal (if any) is untouched.
func TestEngine_RerunYieldsUndismissedNewInstance(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	dis := newDismissalRecorder()
	f.engine.SetDismissals(dis)

	id, _, err := f.engine.Trigger(wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	// Dismiss the original completed instance for a user.
	user := uuid.New()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))

	newID, err := f.engine.Rerun(id)
	require.NoError(t, err)
	assert.NotEqual(t, id, newID, "rerun must create a new instance id")

	ids, _ := dis.DismissedIDs(context.Background(), user)
	assert.True(t, ids[id], "original instance stays dismissed (historical)")
	assert.False(t, ids[newID], "new instance has no dismissal rows")
	assert.Equal(t, 0, dis.clearedCount(newID), "rerun does not clear dismissals")
}
