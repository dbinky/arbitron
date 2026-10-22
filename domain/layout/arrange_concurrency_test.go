package layout

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In-place sorting or scratch storage shared between calls must not corrupt a
// captured graph, its measurements, or another job's returned presentation.
func TestArrange_ConcurrentCallsKeepInputsAndResultsIndependent(t *testing.T) {
	wf := &workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Entrypoint: true}, {ID: "b", Label: strings.Repeat("検証 Überprüfung ", 8)}, {ID: "c"}, {ID: "d"}},
		Edges: []workflow.Edge{
			{Source: "a", Target: "b", Label: "次へ"}, {Source: "b", Target: "c"}, {Source: "c", Target: "d"},
			{Source: "a", Target: "d"}, {Source: "a", Target: "d"},
			{Source: "c", Target: "a"}, {Source: "d", Target: "b"}, {Source: "b", Target: "b"},
		},
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	c, o, m := DefaultContract(), DefaultOptions(), measured(t, g)
	before, err := json.Marshal([]any{wf, g, m})
	require.NoError(t, err)
	indexBefore := maps.Clone(g.index)
	baseline, err := Arrange(context.Background(), g, m, c, o)
	require.NoError(t, err)
	want, err := json.Marshal(baseline)
	require.NoError(t, err)
	require.NotEmpty(t, GeometryDigest(baseline))

	const calls = 8
	type result struct {
		geometry Geometry
		err      error
	}
	start, done := make(chan struct{}), make(chan result, calls)
	for range calls {
		go func() {
			<-start
			geo, err := Arrange(context.Background(), g, m, c, o)
			done <- result{geo, err}
		}()
	}
	close(start)
	results := make([]result, calls)
	for i := range results {
		results[i] = <-done
	}
	for _, got := range results {
		require.NoError(t, got.err)
		require.NoError(t, got.geometry.Validate(g))
		raw, err := json.Marshal(got.geometry)
		require.NoError(t, err)
		assert.Equal(t, want, raw, "fixed measurements and policy must produce identical bytes")
		assert.Equal(t, GeometryDigest(baseline), GeometryDigest(got.geometry))
	}

	// Downstream annotation of one result must not modify another result.
	results[0].geometry.Nodes[0].Box.X++
	results[0].geometry.Edges[0].Points[0].X++
	require.NotNil(t, results[0].geometry.Edges[0].Text)
	results[0].geometry.Edges[0].Text.Box.X++
	for _, got := range results[1:] {
		raw, err := json.Marshal(got.geometry)
		require.NoError(t, err)
		assert.Equal(t, want, raw, "nodes, routes and label boxes belong to one result")
	}
	after, err := json.Marshal([]any{wf, g, m})
	require.NoError(t, err)
	assert.Equal(t, before, after, "successful concurrent calls preserve all input bytes")
	assert.Equal(t, indexBefore, g.index)
}
