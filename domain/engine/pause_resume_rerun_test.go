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
		inst := f.instanceRepo.get(id)
		return inst.CurrentNodeID != nil && *inst.CurrentNodeID == "C"
	}, 5*time.Second, 10*time.Millisecond, "a paused run must record the next node, not the one that finished")
	assert.Equal(t, 0, count("C"), "C must not run while paused")

	require.NoError(t, f.engine.Resume(bgAdmin(), id))
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, 1, count("B"), "the node that finished during the pause must not run again on resume")
	assert.Equal(t, 1, count("C"))
}
