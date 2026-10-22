package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is Arrange's production provenance gate, not the all-samples-passed
// readability acceptance gate owned by P3T7. Readability findings must survive
// in the real result/view rather than hiding structurally valid geometry.
func TestLayoutArrange_RealBrowserSavedSamplesReachView(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5", realSample: name})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := context.Background()
			saved := layoutE2EManifest(t, env.sharedPath(name))
			before, err := os.ReadFile(env.sharedPath(name))
			require.NoError(t, err)
			rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), 3*time.Minute)
			// The named acceptance samples must reach `passed` within five attempts
			// (R1.6); any other sample may still end needs_attention.
			allowed := []layout.State{layout.StatePassed, layout.StateNeedsAttention}
			if name != "harness-update" {
				allowed = []layout.State{layout.StatePassed}
			}
			require.Contains(t, allowed, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
			require.GreaterOrEqual(t, rec.AttemptsUsed, 1)
			require.LessOrEqual(t, rec.AttemptsUsed, 5)
			require.Positive(t, rec.ResultGeneration)
			result, err := ds.layout.store.Result(ctx, rec.Revision, rec.ResultGeneration)
			require.NoError(t, err)
			geo := result.Geometry
			graph, err := layout.BuildGraph(saved)
			require.NoError(t, err)
			require.NoError(t, geo.Validate(graph))
			// Check measured text at the persisted sink, independently of Arrange.
			metrics, err := ds.layout.renderer.Measure(ctx, layout.MeasureRequestFor(graph, geo.Contract))
			require.NoError(t, err)
			for _, n := range geo.Nodes {
				metric := metrics.Items[layout.NodeLabelKey(n.ID)]
				assert.Equal(t, metric.Lines, n.TextLines, "node %s must use browser wrapping", n.ID)
				assert.GreaterOrEqual(t, n.Box.H, metric.Height+2*geo.Contract.NodePaddingY)
			}
			for _, e := range geo.Edges {
				if e.Text != nil {
					metric := metrics.Items[layout.EdgeLabelKey(e.Ordinal)]
					assert.Equal(t, metric.Lines, e.Text.Lines, "edge %d must use browser wrapping", e.Ordinal)
					assert.GreaterOrEqual(t, e.Text.Box.H, metric.Height+2*geo.Contract.EdgeLabelPaddingY)
				}
			}
			require.NotNil(t, result.Review)
			assert.Equal(t, layout.GeometryDigest(geo), result.Review.Evidence.GeometryDigest)
			assert.Equal(t, geo.Contract, result.Review.Evidence.Contract)
			assert.True(t, result.Review.Evidence.FontsReady)
			assert.False(t, result.Review.Evidence.ReviewedAt.IsZero())
			assertEvidenceOnDisk(t, env, result.Review.Evidence)
			if rec.State == layout.StatePassed {
				require.True(t, layout.HasRenderedEvidence(geo, result.Review))
			} else {
				require.NotEmpty(t, result.UnresolvedFindings(), "readability failures must reach the sink")
				t.Logf("readability acceptance remains unresolved: %+v", result.UnresolvedFindings())
			}
			if len(saved.Edges) > 0 {
				assertDownwardArrangement(t, geo)
			} else {
				require.Len(t, geo.Nodes, 1)
				assert.Empty(t, geo.Edges)
			}
			assertSavedArrangement(t, geo)

			// Verify the authoritative result, display and convenience cache agree.
			view := ds.layout.reader.View(ctx, saved)
			require.True(t, view.Available, "%s", view.AvailabilityError)
			assert.Equal(t, result.IsUnreviewed(), view.Unreviewed)
			assert.Equal(t, rec.State, view.State)
			assert.Equal(t, rec.Revision, view.SavedRevision)
			assert.Equal(t, rec.ResultGeneration, view.ResultGeneration)
			assert.Equal(t, layout.ProvenanceArranged, view.Provenance)
			require.NotNil(t, view.Geometry)
			assert.Equal(t, layout.GeometryDigest(geo), layout.GeometryDigest(*view.Geometry))
			assert.Equal(t, result.UnresolvedFindings(), view.Findings)
			require.Eventually(t, func() bool {
				entry, ok, err := ds.layout.cache.ReadCache(ctx, rec.Destination.Scope, name)
				return err == nil && ok && entry.Revision == rec.Revision && entry.Generation == rec.ResultGeneration && layout.GeometryDigest(entry.Geometry) == layout.GeometryDigest(geo)
			}, 30*time.Second, 100*time.Millisecond)
			after, err := os.ReadFile(env.sharedPath(name))
			require.NoError(t, err)
			assert.Equal(t, before, after, "arrangement must not mutate the saved manifest")
		})
	}
}

func assertSavedArrangement(t *testing.T, geo layout.Geometry) {
	t.Helper()
	boxes := map[string]layout.Rect{}
	for i, n := range geo.Nodes {
		boxes[n.ID] = n.Box
		for _, other := range geo.Nodes[i+1:] {
			assert.False(t, n.Box.Overlaps(other.Box), "%s overlaps %s", n.ID, other.ID)
		}
	}
	for _, edge := range geo.Edges {
		if edge.Kind == layout.EdgeForward || edge.Kind == layout.EdgeLoopReturn {
			assert.Equal(t, layout.SideBottom, edge.SourceAttach.Side)
			assert.Equal(t, layout.SideTop, edge.TargetAttach.Side)
			last := len(edge.Points) - 1
			assert.Greater(t, edge.Points[last].Y, edge.Points[last-1].Y, "edge %d must enter downward", edge.Ordinal)
		}
		if edge.Kind == layout.EdgeLoopReturn {
			assert.Positive(t, edge.Lane)
			assert.Contains(t, []layout.Side{layout.SideLeft, layout.SideRight}, edge.Side)
			climbsInGutter := false
			for i := 1; i < len(edge.Points); i++ {
				a, b := edge.Points[i-1], edge.Points[i]
				if a.X != b.X || b.Y >= a.Y {
					continue
				}
				outside := true
				for _, n := range geo.Nodes {
					outside = outside && (a.X < n.Box.X || a.X > n.Box.Right())
				}
				climbsInGutter = climbsInGutter || outside
			}
			assert.True(t, climbsInGutter, "return %d must climb outside all rows", edge.Ordinal)
		}
		if edge.Text != nil && edge.Kind != layout.EdgeSelfLoop {
			assert.InDelta(t, edge.SourceAttach.Point.X+geo.Options.LabelGap, edge.Text.Box.X, 0.01)
			assert.GreaterOrEqual(t, edge.Text.Box.Y, boxes[edge.Source].Bottom())
		}
		for i := 1; i < len(edge.Points); i++ {
			a, b := edge.Points[i-1], edge.Points[i]
			for _, n := range geo.Nodes {
				if n.ID == edge.Source || n.ID == edge.Target {
					continue
				}
				crosses := a.Y > n.Box.Y && a.Y < n.Box.Bottom() && math.Min(a.X, b.X) < n.Box.Right() && math.Max(a.X, b.X) > n.Box.X
				if a.X == b.X {
					crosses = a.X > n.Box.X && a.X < n.Box.Right() && math.Min(a.Y, b.Y) < n.Box.Bottom() && math.Max(a.Y, b.Y) > n.Box.Y
				}
				assert.False(t, crosses, "edge %d segment %d crosses %s", edge.Ordinal, i, n.ID)
			}
		}
	}
}
