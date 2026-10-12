package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bindingLiveWorkflow: script "init" outputs {branch} → interactive_agent
// "review" on profile "coder". gitVariable points at init's output, so the
// second node.started must carry the branch that only exists after init ran.
func bindingLiveWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "binding-wf",
		GitVariable: "init.branch",
		Nodes: []workflow.Node{
			{ID: "init", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "review", Label: "Review", Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: "coder"}},
		},
		Edges: []workflow.Edge{{Source: "init", Target: "review"}},
	}
}

func runBindingWorkflow(t *testing.T, wire bool) map[string]map[string]any {
	t.Helper()
	f := newTestFixtures()
	if wire {
		f.engine.SetProfileResolver(enricherProfileResolver{bindings: codexProfile.bindings})
	}
	f.workflowRepo.add(bindingLiveWorkflow())
	f.executor.onNode("init", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return &engine.NodeResult{Output: map[string]any{"branch": "feat/live"}}, nil
	})
	id, _, err := f.engine.Trigger(context.Background(), "binding-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	out := map[string]map[string]any{}
	for _, e := range f.eventBus.eventsOfType(id, "node.started") {
		d, ok := e.Data.(map[string]any)
		require.True(t, ok)
		out[d["nodeId"].(string)] = d
	}
	require.Len(t, out, 2)
	return out
}

func TestEngine_NodeStartedBinding_ScriptNodeTypeOnly(t *testing.T) {
	d := runBindingWorkflow(t, true)["init"]
	assert.Equal(t, "script", d["nodeType"])
	assert.Equal(t, "", d["model"])
	assert.Equal(t, "", d["backend"])
	assert.Equal(t, "", d["gitRef"], "branch does not exist before init runs")
}

func TestEngine_NodeStartedBinding_AgentNodeFromProfileWithLiveGitRef(t *testing.T) {
	d := runBindingWorkflow(t, true)["review"]
	assert.Equal(t, "interactive_agent", d["nodeType"])
	assert.Equal(t, "Review", d["nodeLabel"])
	assert.Equal(t, "gpt-6.1-sol", d["model"])
	assert.Equal(t, "high", d["effort"])
	assert.Equal(t, "codex", d["backend"])
	assert.Equal(t, "litellm", d["provider"])
	assert.Equal(t, "feat/live", d["gitRef"])
}

func TestEngine_NodeStartedBinding_NoResolverStillPublishes(t *testing.T) {
	d := runBindingWorkflow(t, false)["review"]
	assert.Equal(t, "interactive_agent", d["nodeType"])
	assert.Equal(t, "", d["model"], "unknown profile degrades to node config (no model)")
	assert.Equal(t, "feat/live", d["gitRef"])
}

func TestEngine_SetProfileResolver_NilPanics(t *testing.T) {
	f := newTestFixtures()
	assert.Panics(t, func() { f.engine.SetProfileResolver(nil) })
}
