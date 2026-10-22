package layout

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Required geometry collections must reach render.html and both dashboard
// consumers as arrays, including a crossing-free chain and a lone node.
func TestArrange_WireCollectionsAreArrays(t *testing.T) {
	for _, wf := range []*workflow.Workflow{
		{Nodes: []workflow.Node{{ID: "only"}}},
		{Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}}, Edges: []workflow.Edge{{Source: "a", Target: "b"}}},
	} {
		g, err := BuildGraph(wf)
		require.NoError(t, err)
		geo := arrangeFor(t, g, DefaultOptions())
		raw, err := json.Marshal(geo)
		require.NoError(t, err)
		var wire map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &wire))
		for _, key := range []string{"nodes", "edges", "crossings"} {
			require.NotEmpty(t, wire[key])
			require.Equal(t, byte('['), wire[key][0], "%s must be an array, not null: %s", key, wire[key])
		}
	}
}
