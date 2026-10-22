package layout

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGraph_DisplayTextAndTypeCaption(t *testing.T) {
	wf := &workflow.Workflow{
		Nodes: []workflow.Node{
			{ID: "init", Type: workflow.NodeTypeScript, Label: "Init workspace", Entrypoint: true},
			{ID: "plan-review", Type: workflow.NodeTypeAgentCall},
			{ID: "gate", Type: workflow.NodeTypeConditional, Label: ""},
		},
	}

	g, err := BuildGraph(wf)
	require.NoError(t, err)
	require.Len(t, g.Nodes, 3)

	assert.Equal(t, GraphNode{ID: "init", Text: "Init workspace", TypeCaption: "script", Entrypoint: true}, g.Nodes[0])
	assert.Equal(t, "plan-review", g.Nodes[1].Text, "empty label falls back to the node id")
	assert.Equal(t, "agent call", g.Nodes[1].TypeCaption, "underscores become spaces like the dashboard caption")
	assert.Equal(t, "gate", g.Nodes[2].Text)
	assert.Equal(t, "conditional", g.Nodes[2].TypeCaption)
	assert.False(t, g.Nodes[2].Entrypoint)
}

func TestBuildGraph_EdgeOrdinalsAndBranchText(t *testing.T) {
	wf := &workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []workflow.Edge{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "c", Output: "pass"},
			{Source: "b", Target: "a", Output: "fail", Label: "back-edge: retry"},
			{Source: "b", Target: "c", Output: "pass"}, // identical parallel edge
		},
	}

	g, err := BuildGraph(wf)
	require.NoError(t, err)
	require.Len(t, g.Edges, 4, "every edge occurrence is kept, including identical parallels")

	assert.Equal(t, []GraphEdge{
		{Ordinal: 0, Source: "a", Target: "b"},
		{Ordinal: 1, Source: "b", Target: "c", Output: "pass", Text: "pass"},
		{Ordinal: 2, Source: "b", Target: "a", Output: "fail", Label: "back-edge: retry", Text: "back-edge: retry"},
		{Ordinal: 3, Source: "b", Target: "c", Output: "pass", Text: "pass"},
	}, g.Edges, "every occurrence preserves its endpoints and verification data in manifest order")
	for i, e := range g.Edges {
		assert.Equal(t, i, e.Ordinal, "ordinal is the manifest slice index")
	}
	assert.Equal(t, "", g.Edges[0].Text, "no label and no output means no branch text")
	assert.Equal(t, "pass", g.Edges[1].Text, "output is used when label is empty")
	assert.Equal(t, "back-edge: retry", g.Edges[2].Text, "label wins over output")
	assert.Equal(t, "fail", g.Edges[2].Output, "verification data is preserved verbatim")
	assert.Equal(t, g.Edges[1].Source, g.Edges[3].Source)
	assert.Equal(t, g.Edges[1].Target, g.Edges[3].Target)
	assert.NotEqual(t, g.Edges[1].Ordinal, g.Edges[3].Ordinal, "parallel edges keep distinct identities")
}

func TestBuildGraph_NodeIndex(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "x"}, {ID: "y"}}})
	require.NoError(t, err)

	i, ok := g.NodeIndex("y")
	assert.True(t, ok)
	assert.Equal(t, 1, i)

	_, ok = g.NodeIndex("missing")
	assert.False(t, ok)
}

func TestBuildGraph_SingleNodeNoEdges(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "only", Type: workflow.NodeTypeScript}}})
	require.NoError(t, err)
	assert.Len(t, g.Nodes, 1)
	assert.Empty(t, g.Edges)
}

func TestBuildGraph_Errors(t *testing.T) {
	cases := []struct {
		name string
		wf   *workflow.Workflow
		want error
	}{
		{name: "nil workflow", wf: nil, want: ErrEmptyGraph},
		{name: "no nodes", wf: &workflow.Workflow{}, want: ErrEmptyGraph},
		{name: "empty node id", wf: &workflow.Workflow{Nodes: []workflow.Node{{ID: ""}}}, want: ErrEmptyNodeID},
		{name: "duplicate node id", wf: &workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "a"}}}, want: ErrDuplicateNode},
		{name: "dangling source", wf: &workflow.Workflow{
			Nodes: []workflow.Node{{ID: "a"}},
			Edges: []workflow.Edge{{Source: "ghost", Target: "a"}},
		}, want: ErrDanglingEdge},
		{name: "dangling target", wf: &workflow.Workflow{
			Nodes: []workflow.Node{{ID: "a"}},
			Edges: []workflow.Edge{{Source: "a", Target: "ghost"}},
		}, want: ErrDanglingEdge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildGraph(tc.wf)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "got %v, want %v", err, tc.want)
		})
	}
}

func TestBuildGraph_DoesNotMutateManifest(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "keep-me",
		Nodes: []workflow.Node{{
			ID: "a", Label: "A", Type: workflow.NodeTypeScript,
			Config:   workflow.NodeConfig{Command: "echo keep-me", Env: map[string]string{"KEEP": "value"}},
			Position: &workflow.NodePosition{X: 1, Y: 2},
		}},
		Edges: []workflow.Edge{{Source: "a", Target: "a", Output: "again"}},
	}
	before, err := json.Marshal(wf)
	require.NoError(t, err)

	g, err := BuildGraph(wf)
	require.NoError(t, err)
	require.Equal(t, []GraphNode{{ID: "a", Text: "A", TypeCaption: "script"}}, g.Nodes)
	require.Equal(t, []GraphEdge{{Ordinal: 0, Source: "a", Target: "a", Output: "again", Text: "again"}}, g.Edges)
	after, err := json.Marshal(wf)
	require.NoError(t, err)
	assert.Equal(t, before, after, "all manifest bytes, including nested configuration and manual coordinates, are untouched")

	g.Nodes[0].Text = "changed graph"
	g.Edges[0].Output = "changed graph"
	after, err = json.Marshal(wf)
	require.NoError(t, err)
	assert.Equal(t, before, after, "graph values do not alias manifest data")

	wf.Nodes[0].ID = "changed-manifest"
	wf.Edges[0].Target = "changed-manifest"
	assert.Equal(t, "a", g.Nodes[0].ID, "the graph retains its captured node data")
	assert.Equal(t, "a", g.Edges[0].Target, "the graph retains its captured edge data")
	i, ok := g.NodeIndex("a")
	assert.True(t, ok)
	assert.Zero(t, i)
}

func TestBuildGraph_UnicodeLabelsPreserved(t *testing.T) {
	label := "Überprüfung — 検証ステップ ✅ with a deliberately long label that must wrap, not ellipsize"
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "u", Label: label}}})
	require.NoError(t, err)
	require.Len(t, g.Nodes, 1)
	assert.Equal(t, label, g.Nodes[0].Text, "full text is kept; wrapping happens in sizing/rendering")
}
