package layout

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func arrangeSample(t *testing.T, name string) (Graph, Geometry, Measurements) {
	t.Helper()
	g, err := BuildGraph(loadSample(t, name))
	require.NoError(t, err)
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(context.Background(), g, m, c, DefaultOptions())
	require.NoError(t, err)
	return g, geo, m
}

func TestSamples_PassGeometricPreflight(t *testing.T) {
	for _, name := range []string{"legacy-feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			g, geo, m := arrangeSample(t, name)
			fs := Preflight(g, geo, m)
			for _, f := range fs {
				if f.Severity == SeverityBlocking {
					t.Errorf("blocking finding on %s: %+v", name, f)
				}
			}
			assert.Equal(t, 0, BlockingCount(fs))
			t.Logf("%s: %d nodes, %d edges, canvas %.0f×%.0f, %d crossings", name, len(geo.Nodes), len(geo.Edges), geo.Canvas.W, geo.Canvas.H, len(geo.Crossings))
		})
	}
}

func TestSamples_SavedGeometryDefectsBlock(t *testing.T) {
	for _, name := range []string{"legacy-feature-pipeline", "pr-review", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			g, geo, m := arrangeSample(t, name)
			assert.Zero(t, BlockingCount(Preflight(g, geo, m)))
			t.Run("saved branch label over node", func(t *testing.T) {
				bad := cloneGeometry(t, geo)
				found := false
				for i := range bad.Edges {
					if bad.Edges[i].Text != nil {
						bad.Edges[i].Text.Box.X = bad.Nodes[0].Box.X
						bad.Edges[i].Text.Box.Y = bad.Nodes[0].Box.Y
						found = true
						break
					}
				}
				require.True(t, found, "sample must feed a real branch label")
				require.NoError(t, bad.Validate(g))
				assert.Positive(t, criteria(Preflight(g, bad, m))[CriterionTextCollision])
			})
			t.Run("missing saved gap annotations", func(t *testing.T) {
				require.NotEmpty(t, geo.Crossings)
				bad := cloneGeometry(t, geo)
				bad.Crossings = nil
				require.NoError(t, bad.Validate(g))
				assert.Positive(t, criteria(Preflight(g, bad, m))[CriterionAmbiguousCrossing])
			})
		})
	}
}

func TestSamples_RouteSearchRemovesCrossingsBeforePreflight(t *testing.T) {
	g := numberedLayerGraph(t, 11, [][2]int{
		{0, 4}, {0, 6}, {1, 4}, {1, 5}, {1, 6}, {2, 3}, {2, 6},
		{3, 7}, {3, 8}, {4, 7}, {4, 8}, {4, 9}, {5, 10}, {6, 8}, {6, 9}, {6, 10},
	})
	m := measured(t, g)
	o := DefaultOptions()
	o.CrossingSweeps = 0
	initial, err := Arrange(context.Background(), g, m, DefaultContract(), o)
	require.NoError(t, err)
	o.CrossingSweeps = DefaultOptions().CrossingSweeps
	selected, err := Arrange(context.Background(), g, m, DefaultContract(), o)
	require.NoError(t, err)
	assert.Less(t, len(selected.Crossings), len(initial.Crossings), "compare complete routes, not abstract layer inversions")
	assert.Zero(t, BlockingCount(Preflight(g, selected, m)))
}

func TestSamples_FeaturePipelineShape(t *testing.T) {
	_, geo, _ := arrangeSample(t, "legacy-feature-pipeline")
	assert.Len(t, geo.Nodes, 66)
	assert.Len(t, geo.Edges, 79)
	assert.Greater(t, geo.Canvas.H, geo.Canvas.W, "continuous downward progression: long workflows scroll vertically")
}

func TestSamples_PRReviewShape(t *testing.T) {
	_, geo, _ := arrangeSample(t, "pr-review")
	assert.Len(t, geo.Nodes, 19)
	assert.Greater(t, geo.Canvas.H, geo.Canvas.W)
}

func TestSamples_RalphLoopHasDistinctLoopChannels(t *testing.T) {
	_, geo, _ := arrangeSample(t, "ralph-loop")
	type span struct {
		lane   int
		side   Side
		y1, y2 float64
		ord    int
	}
	var loops []span
	for _, e := range geo.Edges {
		if e.Kind != EdgeLoopReturn {
			continue
		}
		y1, y2 := e.Points[0].Y, e.Points[0].Y
		for _, p := range e.Points {
			y1, y2 = min(y1, p.Y), max(y2, p.Y)
		}
		loops = append(loops, span{lane: e.Lane, side: e.Side, y1: y1, y2: y2, ord: e.Ordinal})
	}
	require.NotEmpty(t, loops, "ralph-loop must contain loop returns")
	for i := range loops {
		for j := i + 1; j < len(loops); j++ {
			a, b := loops[i], loops[j]
			if a.side == b.side && a.y1 < b.y2 && b.y1 < a.y2 {
				assert.NotEqual(t, a.lane, b.lane, "loop returns %d and %d overlap vertically on the same side and must use distinct lanes", a.ord, b.ord)
			}
		}
	}
}

// A branch label is read as belonging to the route nearest its centre. A wide
// label beside the first run of its own route sat nearer to a route entering the
// source node's top than to its own (G7), so labels drop to keep their own route
// the nearest one.
func TestSamples_BranchLabelsStayNearestTheirOwnRoute(t *testing.T) {
	dist := func(c Point, pts []Point) float64 {
		d := math.Inf(1)
		for i := 1; i < len(pts); i++ {
			a, b := pts[i-1], pts[i]
			x := math.Max(math.Min(a.X, b.X), math.Min(c.X, math.Max(a.X, b.X)))
			y := math.Max(math.Min(a.Y, b.Y), math.Min(c.Y, math.Max(a.Y, b.Y)))
			d = math.Min(d, math.Hypot(c.X-x, c.Y-y))
		}
		return d
	}
	for _, name := range []string{"legacy-feature-pipeline", "ralph-loop", "pr-review"} {
		_, geo, _ := arrangeSample(t, name)
		for _, e := range geo.Edges {
			if e.Text == nil {
				continue
			}
			c := Point{X: e.Text.Box.X + e.Text.Box.W/2, Y: e.Text.Box.Y + e.Text.Box.H/2}
			own := dist(c, e.Points)
			for _, o := range geo.Edges {
				if o.Ordinal != e.Ordinal {
					assert.GreaterOrEqual(t, dist(c, o.Points)+1, own, "%s: label of edge %d is nearer edge %d", name, e.Ordinal, o.Ordinal)
				}
			}
		}
	}
}
