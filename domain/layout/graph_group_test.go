package layout

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capturedGroupWorkflow(form string, nested bool) *workflow.Workflow {
	cfg := &workflow.GroupConfig{Form: form, CompletionRule: workflow.GroupRuleAllSucceed, UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining, GatherMode: workflow.GroupGatherResults,
		Tasks: []workflow.GroupTask{{Key: "second", Workflow: "同じ子 ✅", Inputs: map[string]any{"nested": map[string]any{"values": []any{"original"}}}}, {Key: "first", Workflow: "同じ子 ✅"}}}
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "start", Entrypoint: true}, {ID: "fan", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: cfg}}, {ID: "done"}, {ID: "oops"}}, Edges: []workflow.Edge{{Source: "start", Target: "fan"}, {Source: "fan", Target: "done", Output: workflow.GroupPortSuccess}, {Source: "fan", Target: "oops", Output: workflow.GroupPortFailure}}}
	if form == workflow.GroupFormBranches {
		for i, id := range []string{"a", "b"} {
			cfg.Tasks[i].Workflow = ""
			cfg.Tasks[i].Entry = id
			wf.Nodes = append(wf.Nodes, workflow.Node{ID: id})
			wf.Edges = append(wf.Edges, workflow.Edge{Source: "fan", Target: id, Output: workflow.GroupPortTask, Label: "Same port label"})
		}
	}
	if nested {
		inner := capturedGroupWorkflow(workflow.GroupFormWorkflows, false)
		wf.Nodes[4].Type = workflow.NodeTypeParallelGroup
		wf.Nodes[4].Config = inner.Nodes[1].Config
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: "inner-done"}, workflow.Node{ID: "inner-oops"})
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "inner-done", Output: workflow.GroupPortSuccess}, workflow.Edge{Source: "a", Target: "inner-oops", Output: workflow.GroupPortFailure})
	}
	return wf
}

func TestBuildGraph_GroupInputIsCaptured(t *testing.T) {
	for _, tc := range []struct {
		name, form string
		nested     bool
	}{{"branches", workflow.GroupFormBranches, false}, {"workflows", workflow.GroupFormWorkflows, false}, {"nested", workflow.GroupFormBranches, true}} {
		t.Run(tc.name, func(t *testing.T) {
			wf := capturedGroupWorkflow(tc.form, tc.nested)
			before, err := json.Marshal(wf)
			require.NoError(t, err)
			want, findings := workflow.AnalyzeGroups(wf)
			require.Empty(t, findings)
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			require.NotNil(t, g.Groups, "captured group ownership must reach the layout graph")
			require.Equal(t, want, g.Groups)
			assert.Equal(t, []string{"second", "first"}, []string{g.Groups.Groups["fan"].Config.Tasks[0].Key, g.Groups.Groups["fan"].Config.Tasks[1].Key})
			assert.Len(t, g.Nodes, len(wf.Nodes))
			assert.Len(t, g.Edges, len(wf.Edges))
			for i, e := range wf.Edges {
				assert.Equal(t, GraphEdge{Ordinal: i, Source: e.Source, Target: e.Target, Output: e.Output, Label: e.Label, Text: branchText(e)}, g.Edges[i])
			}
			if tc.form == workflow.GroupFormBranches {
				assert.Equal(t, workflow.BodyOwner{GroupNodeID: "fan", TaskKey: "second"}, g.Groups.Owner["a"])
			}
			if tc.nested {
				require.Equal(t, &workflow.BodyOwner{GroupNodeID: "fan", TaskKey: "second"}, g.Groups.Groups["a"].Parent)
				assert.Equal(t, []string{"a"}, g.Groups.Groups["fan"].Nested)
			}
			g.Groups.Groups["fan"].Config.Tasks[0].Inputs["nested"].(map[string]any)["values"].([]any)[0] = "changed"
			g.Groups.Order[0] = "changed"
			g.Groups.Owner["done"] = workflow.BodyOwner{GroupNodeID: "changed"}
			after, err := json.Marshal(wf)
			require.NoError(t, err)
			assert.Equal(t, before, after, "captured input cannot mutate any manifest bytes")
			g, err = BuildGraph(wf)
			require.NoError(t, err)
			wf.Nodes[1].Config.Group.Tasks[0].Key = "mutated-manifest"
			assert.Equal(t, "second", g.Groups.Groups["fan"].Config.Tasks[0].Key, "manifest cannot mutate captured ownership")
		})
	}
	t.Run("ordinary", func(t *testing.T) {
		g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "ordinary"}}})
		require.NoError(t, err)
		assert.Nil(t, g.Groups)
	})
}

func TestBuildGraph_InvalidGroupsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*workflow.Workflow)
	}{
		{"missing config", func(w *workflow.Workflow) { w.Nodes[1].Config.Group = nil }},
		{"empty config", func(w *workflow.Workflow) { w.Nodes[1].Config.Group = &workflow.GroupConfig{} }},
		{"bad config", func(w *workflow.Workflow) { w.Nodes[1].Config.Group.CompletionRule = "guess" }},
		{"missing task edge", func(w *workflow.Workflow) { w.Edges = w.Edges[:len(w.Edges)-1] }},
		{"mismatched entry", func(w *workflow.Workflow) { w.Nodes[1].Config.Group.Tasks[0].Entry = "ghost" }},
		{"group cycle", func(w *workflow.Workflow) { w.Edges = append(w.Edges, workflow.Edge{Source: "done", Target: "fan"}) }},
		{"body escape", func(w *workflow.Workflow) { w.Edges = append(w.Edges, workflow.Edge{Source: "a", Target: "done"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := capturedGroupWorkflow(workflow.GroupFormBranches, false)
			tc.mutate(wf)
			g, err := BuildGraph(wf)
			require.ErrorIs(t, err, ErrInvalidGroups)
			assert.Empty(t, g.Nodes, "invalid groups cannot become ordinary boxes")
		})
	}
}
