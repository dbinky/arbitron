package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise layering through real authoring, browser measurements/review and the
// persisted Reader/cache sinks. Direct Arrange calls alone cannot prove wiring.
func TestLayoutLayers_RealBrowserSavedRowsReachView(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, tc := range []struct {
		name  string
		ids   []string
		edges []workflow.Edge
	}{
		{
			name: "reorder",
			ids:  []string{"a", "b", "x", "y"},
			edges: []workflow.Edge{
				{Source: "a", Target: "y"}, {Source: "b", Target: "x"},
			},
		},
		{
			name: "parallel-shortcuts",
			ids:  []string{"a", "b", "c", "d"},
			edges: []workflow.Edge{
				{Source: "a", Target: "b"}, {Source: "b", Target: "c"},
				{Source: "c", Target: "d"}, {Source: "a", Target: "d"},
				{Source: "a", Target: "d"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			wf := &workflow.Workflow{Name: tc.name, Edges: tc.edges}
			for _, id := range tc.ids {
				wf.Nodes = append(wf.Nodes, workflow.Node{
					ID: id, Type: workflow.NodeTypeTransform, Label: "Step " + id,
					Entrypoint: id == "a" || (tc.name == "reorder" && id == "b"),
					Config:     workflow.NodeConfig{Operations: []workflow.Operation{{Variable: id, Operation: "set", Value: true}}},
				})
			}
			require.NoError(t, ds.workflowSvc.Create(ctx, tc.name, wf))
			saved := layoutE2EManifest(t, env.privatePath(tc.name))
			before, err := os.ReadFile(env.privatePath(tc.name))
			require.NoError(t, err)
			rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), 2*time.Minute)
			require.Equal(t, layout.StatePassed, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
			require.Equal(t, 1, rec.AttemptsUsed, "refinement must not hide missing initial ordering")
			result, err := ds.layout.store.Result(ctx, rec.Revision, rec.ResultGeneration)
			require.NoError(t, err)
			geo := result.Geometry
			graph, err := layout.BuildGraph(saved)
			require.NoError(t, err)
			require.True(t, layout.Compatible(geo, graph))
			require.Len(t, geo.Nodes, len(tc.ids))
			require.Len(t, geo.Edges, len(tc.edges))
			assertDownwardArrangement(t, geo)
			boxes := map[string]layout.NodeBox{}
			for _, n := range geo.Nodes {
				boxes[n.ID] = n
			}
			if tc.name == "reorder" {
				// In manifest order a->y and b->x are twisted. The saved
				// result must reverse one row, not just report fewer crossings.
				assert.Equal(t, boxes["a"].Rank, boxes["b"].Rank)
				assert.Equal(t, boxes["x"].Rank, boxes["y"].Rank)
				assert.Equal(t, boxes["a"].Box.X < boxes["b"].Box.X, boxes["y"].Box.X < boxes["x"].Box.X)
				assert.Empty(t, geo.Crossings)
				metrics, err := ds.layout.renderer.Measure(context.Background(), layout.MeasureRequestFor(graph, geo.Contract))
				require.NoError(t, err)
				noSearch := geo.Options
				noSearch.CrossingSweeps = 0
				unordered, err := layout.Arrange(context.Background(), graph, metrics, geo.Contract, noSearch)
				require.NoError(t, err)
				assert.NotEmpty(t, unordered.Crossings, "negative control must need the ordering search")
			} else {
				assert.Equal(t, []int{0, 1, 2, 3}, []int{boxes["a"].Rank, boxes["b"].Rank, boxes["c"].Rank, boxes["d"].Rank})
				// Separate dummy slots must feed separate long-edge channels,
				// rather than merging identical shortcut occurrences.
				for _, id := range []string{"b", "c"} {
					n := boxes[id].Box
					var channels []float64
					for _, edge := range geo.Edges[3:] {
						for i := 1; i < len(edge.Points); i++ {
							a, b := edge.Points[i-1], edge.Points[i]
							if a.X == b.X && min(a.Y, b.Y) < n.CenterY() && max(a.Y, b.Y) > n.CenterY() {
								channels = append(channels, a.X)
								assert.False(t, a.X > n.X && a.X < n.Right(), "shortcut %d crosses intermediate node %s", edge.Ordinal, id)
							}
						}
					}
					require.Len(t, channels, 2, "both shortcuts must traverse intermediate row %s", id)
					assert.NotEqual(t, channels[0], channels[1], "parallel occurrences need separate slots in row %s", id)
				}
			}
			require.True(t, layout.HasRenderedEvidence(geo, result.Review))
			assertEvidenceOnDisk(t, env, result.Review.Evidence)
			view := ds.layout.reader.View(ctx, saved)
			require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
			assert.Equal(t, rec.Revision, view.SavedRevision)
			assert.Equal(t, layout.ProvenanceArranged, view.Provenance)
			require.NotNil(t, view.Geometry)
			assert.Equal(t, layout.GeometryDigest(geo), layout.GeometryDigest(*view.Geometry))
			require.Eventually(t, func() bool {
				entry, ok, err := ds.layout.cache.ReadCache(ctx, rec.Destination.Scope, tc.name)
				return err == nil && ok && entry.Revision == rec.Revision && entry.Generation == rec.ResultGeneration && layout.GeometryDigest(entry.Geometry) == layout.GeometryDigest(geo)
			}, 30*time.Second, 100*time.Millisecond)
			after, err := os.ReadFile(env.privatePath(tc.name))
			require.NoError(t, err)
			assert.Equal(t, before, after, "layering is presentation-only")
		})
	}
}
