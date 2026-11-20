package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pause does not interrupt the node in flight: it finishes, and the run loop
// stops before the next node. A later Resume restarts at current_node_id, so
// that must already name the NEXT node. Otherwise the finished node (possibly
// a long agent call) runs a second time.
func TestEngine_PauseDuringNode_ResumeDoesNotRerunFinishedNode(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "pause-rerun-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
			{Source: "B", Target: "C"},
		},
	})

	var mu sync.Mutex
	calls := map[string]int{}
	count := func(id string) int { mu.Lock(); defer mu.Unlock(); return calls[id] }
	bStarted := make(chan struct{})
	release := make(chan struct{})
	for _, id := range []string{"A", "B", "C"} {
		id := id
		f.executor.onNode(id, func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
			mu.Lock()
			calls[id]++
			n := calls[id]
			mu.Unlock()
			if id == "B" && n == 1 {
				close(bStarted)
				<-release
			}
			return &engine.NodeResult{Output: map[string]any{"done": id}}, nil
		})
	}

	id, _, err := f.engine.Trigger(context.Background(), "pause-rerun-wf", nil, "normal")
	require.NoError(t, err)
	<-bStarted

	require.NoError(t, f.engine.Pause(bgAdmin(), id))
	close(release)

	// The run loop parks after B completes; current_node_id must point at C.
	require.Eventually(t, func() bool {
		inst, err := f.instanceRepo.Get(context.Background(), id) // a copy: the walker writes concurrently
		return err == nil && inst.CurrentNodeID != nil && *inst.CurrentNodeID == "C"
	}, 5*time.Second, 10*time.Millisecond, "a paused run must record the next node, not the one that finished")
	assert.Equal(t, 0, count("C"), "C must not run while paused")

	require.NoError(t, f.engine.Resume(bgAdmin(), id))
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, 1, count("B"), "the node that finished during the pause must not run again on resume")
	assert.Equal(t, 1, count("C"))
}

// Resume can land while the paused run's node is STILL in flight (an agent
// call that outlives the pause). The live walker must carry on; starting a
// second walker at current_node_id would run the in-flight node twice, in the
// same workspace, with two goroutines walking one graph.
func TestEngine_ResumeWhileNodeInFlight_DoesNotStartSecondWalker(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "resume-inflight-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
			{Source: "B", Target: "C"},
		},
	})

	var mu sync.Mutex
	calls := map[string]int{}
	count := func(id string) int { mu.Lock(); defer mu.Unlock(); return calls[id] }
	bStarted := make(chan struct{})
	release := make(chan struct{})
	for _, id := range []string{"A", "B", "C"} {
		id := id
		f.executor.onNode(id, func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
			mu.Lock()
			calls[id]++
			n := calls[id]
			mu.Unlock()
			if id == "B" && n == 1 {
				close(bStarted)
				<-release
			}
			return &engine.NodeResult{Output: map[string]any{"done": id}}, nil
		})
	}

	id, _, err := f.engine.Trigger(context.Background(), "resume-inflight-wf", nil, "normal")
	require.NoError(t, err)
	<-bStarted

	require.NoError(t, f.engine.Pause(bgAdmin(), id))
	require.NoError(t, f.engine.Resume(bgAdmin(), id)) // B is still running
	time.Sleep(50 * time.Millisecond)                  // give a wrongly started second walker time to re-run B
	close(release)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, 1, count("B"), "the in-flight node must not be started a second time")
	assert.Equal(t, 1, count("C"), "exactly one walker continues the graph")
}

// The other side of the handoff: the walker has ALREADY parked when Resume
// arrives, so Resume must start a fresh walker — and the parked walker's exit
// must not deregister it (a later Cancel needs the live walker's context).
func TestEngine_ResumeAfterWalkerParked_StartsWalkerThatCancelCanStop(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "resume-parked-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{{Source: "A", Target: "B"}},
	})
	aRelease := make(chan struct{})
	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	var once sync.Once
	f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		once.Do(func() { close(aStarted) })
		<-aRelease
		return &engine.NodeResult{Output: map[string]any{}}, nil
	})
	bStopped := make(chan struct{})
	f.executor.onNode("B", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		close(bStarted)
		<-ctx.Done() // only a Cancel of the LIVE walker's context ends B
		close(bStopped)
		return nil, ctx.Err()
	})

	id, _, err := f.engine.Trigger(context.Background(), "resume-parked-wf", nil, "normal")
	require.NoError(t, err)
	<-aStarted
	require.NoError(t, f.engine.Pause(bgAdmin(), id))
	close(aRelease)
	require.Eventually(t, func() bool {
		inst, err := f.instanceRepo.Get(context.Background(), id)
		return err == nil && inst.CurrentNodeID != nil && *inst.CurrentNodeID == "B"
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // let the parked walker finish exiting

	require.NoError(t, f.engine.Resume(bgAdmin(), id))
	<-bStarted
	require.NoError(t, f.engine.Cancel(bgAdmin(), id))

	select {
	case <-bStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel did not reach the resumed walker: its context was deregistered")
	}
}
