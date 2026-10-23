package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
)

// Startup performs only local publication recovery before crash recovery;
// discovery (the catalog fetch) belongs to the background loop, whose immediate
// first pass runs after crash recovery has settled every run, and the
// four-hour ticker exists only then. This drives the production Start →
// startRuntime → startupPass/startScheduled sequence, not a test entry point.
func TestStartupCrashRecoveryRunsBeforeDiscoveryThenTickerStarts(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	var mu sync.Mutex
	var ds *domainStack
	var stateAtFetch []instance.State
	var runID uuid.UUID
	transport := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		stack, id := ds, runID
		mu.Unlock()
		if stack != nil {
			inst, err := stack.instanceRepo.Get(context.Background(), id)
			if err == nil {
				mu.Lock()
				stateAtFetch = append(stateAtFetch, inst.State)
				mu.Unlock()
			}
		}
		return leaf.fetch(r)
	})
	built, _, ivs := buildLifecycleRootWithDependencies(t, lifecycleFollowing, leaf, ModelDependencies{FetchTransport: transport})
	intervals := ivs
	ctx := lifecycleConnection(t, built)
	runID = uuid.New()
	require.NoError(t, built.instanceRepo.Create(ctx, &instance.Instance{
		ID: runID, OwnerID: localDefaultUser, WorkflowName: "crashed",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
	}))
	mu.Lock()
	ds = built
	mu.Unlock()
	assert.Empty(t, intervals, "no ticker before Start")

	require.NoError(t, built.Start(ctx))

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, stateAtFetch, "the background first pass fetched the catalog")
	for i, s := range stateAtFetch {
		assert.Equal(t, instance.StateFailed, s, "fetch %d ran before crash recovery", i)
	}
	got, err := built.instanceRepo.Get(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, got.State)
	rootTicker(t, intervals)
}

// A real crash-recovery persistence fault must fail Start, cancel admitted work
// and leave no scheduled model loop; Close must still join before closing SQL.
func TestStartupRecoveryFailureCancelsRuntimeRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	runID := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: runID, OwnerID: localDefaultUser, WorkflowName: "crashed",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
	}))
	_, err := ds.db.Exec(`CREATE TRIGGER fail_crash_recovery BEFORE UPDATE OF state ON instances
		WHEN OLD.state='running' BEGIN SELECT RAISE(ABORT, 'crash recovery unavailable'); END`)
	require.NoError(t, err)
	err = ds.Start(ctx)
	require.ErrorContains(t, err, "recovering crashed instances")
	require.ErrorContains(t, err, "crash recovery unavailable")
	require.Equal(t, err, ds.Start(ctx), "failed startup is cached rather than partially restarted")
	require.Empty(t, intervals, "failed recovery must not admit the four-hour loop")
	_, refreshErr := ds.modelRefresh.coordinator.Refresh(ctx)
	require.ErrorIs(t, refreshErr, modelcatalog.ErrRefreshClosed)
	inst, readErr := ds.instanceRepo.Get(ctx, runID)
	require.NoError(t, readErr)
	require.Equal(t, instance.StateRunning, inst.State, "failed recovery must not claim success")

	closed := make(chan struct{})
	go func() { ds.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not join the scheduler after failed Start")
	}
	require.Error(t, ds.db.Ping())
}
