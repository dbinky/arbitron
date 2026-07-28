package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
)

// A DIRECT self-spawn is the one cycle visible inside a single definition, and
// the one an author is most likely to write by accident. Catching it at publish
// time turns ten wasted generations into an error with an obvious fix.
//
// It is NOT the cycle defence: an indirect cycle (A spawns B spawns A) is
// invisible from either definition, which is why the runtime spawn-depth bound
// exists.
func TestValidate_RejectsDirectSelfSpawn(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "recurse",
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "x", Operation: "set", Value: 1}}}},
			{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
				ChildWorkflow: "recurse", SpawnMode: workflow.SpawnModeFireAndForget,
			}},
			{ID: "done", Type: workflow.NodeTypeTransform,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "y", Operation: "set", Value: 1}}}},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "spawn-1"},
			{Source: "spawn-1", Target: "done"},
		},
	}

	errs := workflow.Validate(wf, nil, nil, nil)
	var found bool
	for _, e := range errs {
		if e.NodeID == "spawn-1" && e.Code == "INVALID_SPAWN_WORKFLOW" {
			found = true
		}
	}
	assert.True(t, found, "a spawn_workflow node naming its own workflow must fail validation: %+v", errs)
}

// A spawn of a DIFFERENT workflow is the normal case and must stay valid.
func TestValidate_AllowsSpawningAnotherWorkflow(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "parent",
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "x", Operation: "set", Value: 1}}}},
			{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
				ChildWorkflow: "child", SpawnMode: workflow.SpawnModeFireAndForget,
			}},
			{ID: "done", Type: workflow.NodeTypeTransform,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "y", Operation: "set", Value: 1}}}},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "spawn-1"},
			{Source: "spawn-1", Target: "done"},
		},
	}

	assert.Empty(t, workflow.Validate(wf, nil, nil, nil))
}
