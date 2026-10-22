package layout

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Group input is revision-bound, just like the node/edge identities checked by
// Arrange. A missing or edited map must not silently become an ordinary layout.
func TestArrange_GroupInputMustMatchCapturedManifest(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		for _, tc := range []struct {
			name string
			edit func(*workflow.GroupGraph)
		}{
			{"empty", func(g *workflow.GroupGraph) { *g = workflow.GroupGraph{} }},
			{"missing scope", func(g *workflow.GroupGraph) { delete(g.Groups, "fan") }},
			{"nil scope", func(g *workflow.GroupGraph) { g.Groups["fan"] = nil }},
			{"missing order", func(g *workflow.GroupGraph) { g.Order = nil }},
			{"extra order", func(g *workflow.GroupGraph) { g.Order = append(g.Order, "ghost") }},
			{"wrong node", func(g *workflow.GroupGraph) { g.Groups["fan"].NodeID = "ghost" }},
			{"invalid config", func(g *workflow.GroupGraph) { g.Groups["fan"].Config.Form = "guess" }},
			{"reordered tasks", func(g *workflow.GroupGraph) {
				tasks := g.Groups["fan"].Config.Tasks
				tasks[0], tasks[1] = tasks[1], tasks[0]
			}},
			{"changed child input", func(g *workflow.GroupGraph) {
				g.Groups["fan"].Config.Tasks[0].Inputs["nested"].(map[string]any)["values"].([]any)[0] = "different revision"
			}},
			{"wrong continuation", func(g *workflow.GroupGraph) { g.Groups["fan"].SuccessTarget = "oops" }},
			{"extra owner", func(g *workflow.GroupGraph) {
				g.Owner["done"] = workflow.BodyOwner{GroupNodeID: "fan", TaskKey: "second"}
			}},
		} {
			t.Run(form+"/"+tc.name, func(t *testing.T) {
				wf := capturedGroupWorkflow(form, false)
				before, err := json.Marshal(wf)
				require.NoError(t, err)
				g, err := BuildGraph(wf)
				require.NoError(t, err)
				m := measured(t, g)
				tc.edit(g.Groups)
				geo, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
				require.ErrorIs(t, err, ErrInvalidGroups)
				assert.Equal(t, Geometry{}, geo, "bad ownership must not produce an ordinary-box candidate")
				after, err := json.Marshal(wf)
				require.NoError(t, err)
				assert.Equal(t, before, after, "rejection must preserve the saved input")
			})
		}
		t.Run(form+"/nil", func(t *testing.T) {
			g, err := BuildGraph(capturedGroupWorkflow(form, false))
			require.NoError(t, err)
			m := measured(t, g)
			g.Groups = nil
			geo, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
			require.ErrorIs(t, err, ErrInvalidGroups)
			assert.Equal(t, Geometry{}, geo)
		})
	}
}

func TestArrange_GroupOwnershipMustMatchCapturedNesting(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*workflow.GroupGraph)
	}{
		{"missing owner", func(g *workflow.GroupGraph) { delete(g.Owner, "a") }},
		{"wrong task", func(g *workflow.GroupGraph) { g.Owner["a"] = workflow.BodyOwner{GroupNodeID: "fan", TaskKey: "first"} }},
		{"missing body", func(g *workflow.GroupGraph) { delete(g.Groups["fan"].Bodies, "second") }},
		{"wrong body", func(g *workflow.GroupGraph) { g.Groups["fan"].Bodies["second"][0] = "b" }},
		{"missing parent", func(g *workflow.GroupGraph) { g.Groups["a"].Parent = nil }},
		{"wrong parent", func(g *workflow.GroupGraph) { g.Groups["a"].Parent.TaskKey = "first" }},
		{"missing nested", func(g *workflow.GroupGraph) { g.Groups["fan"].Nested = nil }},
		{"reordered groups", func(g *workflow.GroupGraph) { g.Order[0], g.Order[1] = g.Order[1], g.Order[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := BuildGraph(capturedGroupWorkflow(workflow.GroupFormBranches, true))
			require.NoError(t, err)
			m := measured(t, g)
			tc.edit(g.Groups)
			geo, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
			require.ErrorIs(t, err, ErrInvalidGroups)
			assert.Equal(t, Geometry{}, geo)
		})
	}
}

func TestArrange_GroupInputRejectsExtraOwnershipOnOrdinaryGraph(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups *workflow.GroupGraph
	}{
		{"empty", &workflow.GroupGraph{}},
		{"extra scope", &workflow.GroupGraph{Groups: map[string]*workflow.GroupScope{"ghost": nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "ordinary"}}})
			require.NoError(t, err)
			m := measured(t, g)
			g.Groups = tc.groups
			geo, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
			require.ErrorIs(t, err, ErrInvalidGroups)
			assert.Equal(t, Geometry{}, geo)
		})
	}
}

// A detached but identical map is valid; pointer identity is not the contract.
// This tests the Arrange sink, not just a comparison helper, and does not claim
// that ordinary-box v1 group geometry has passed rendered group review.
func TestArrange_GroupInputAcceptsIndependentExactCopy(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		t.Run(form, func(t *testing.T) {
			wf := capturedGroupWorkflow(form, form == workflow.GroupFormBranches)
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			m := measured(t, g)
			g.Groups, err = cloneGroupGraph(g.Groups)
			require.NoError(t, err)
			// Subsequent authoring cannot change this captured graph.
			wf.Nodes[1].Config.Group.Tasks[0].Key = "later-save"
			wf.Nodes[1].Config.Group.Tasks[0].Inputs["nested"].(map[string]any)["values"].([]any)[0] = "later input"
			geo, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
			require.NoError(t, err)
			require.NoError(t, geo.Validate(g))
			assert.Len(t, geo.Nodes, len(wf.Nodes))
			assert.Len(t, geo.Edges, len(wf.Edges))
		})
	}
}
