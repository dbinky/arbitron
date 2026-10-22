package layout_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boundsNode(id string) workflow.Node {
	return workflow.Node{ID: id, Type: workflow.NodeTypeScript}
}

// crossingBipartite is the measured worst case: a source fans out to a row of
// w nodes, which reach a second row of w nodes through crossing edges until
// the manifest holds exactly edges edges.
func crossingBipartite(w, edges int) *workflow.Workflow {
	wf := &workflow.Workflow{Name: "bipartite", Nodes: []workflow.Node{boundsNode("s")}}
	a := func(i int) string { return fmt.Sprintf("a%d", i) }
	b := func(i int) string { return fmt.Sprintf("b%d", i) }
	for i := 0; i < w; i++ {
		wf.Nodes = append(wf.Nodes, boundsNode(a(i)))
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "s", Target: a(i)})
	}
	for i := 0; i < w; i++ {
		wf.Nodes = append(wf.Nodes, boundsNode(b(i)))
	}
	for k := 0; len(wf.Edges) < edges; k++ {
		i := k % w
		wf.Edges = append(wf.Edges, workflow.Edge{Source: a(i), Target: b((w - 1 - i + (k/w)*7) % w)})
	}
	return wf
}

func arrangeWorkflow(ctx context.Context, t *testing.T, wf *workflow.Workflow, o layout.Options) (layout.Geometry, error) {
	t.Helper()
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	c := layout.DefaultContract()
	return layout.Arrange(ctx, g, layouttest.Measurements(g, c), c, o)
}

func TestArrange_RejectsGraphsOverEachCap(t *testing.T) {
	cases := map[string]func() *workflow.Workflow{
		"nodes": func() *workflow.Workflow {
			wf := &workflow.Workflow{Name: "nodes"}
			for i := 0; i <= layout.MaxArrangeNodes; i++ {
				wf.Nodes = append(wf.Nodes, boundsNode(fmt.Sprintf("n%d", i)))
			}
			return wf
		},
		"edges": func() *workflow.Workflow {
			wf := &workflow.Workflow{Name: "edges", Nodes: []workflow.Node{boundsNode("a"), boundsNode("b")}}
			for i := 0; i <= layout.MaxArrangeEdges; i++ {
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "b"})
			}
			return wf
		},
		"row width": func() *workflow.Workflow {
			wf := &workflow.Workflow{Name: "wide", Nodes: []workflow.Node{boundsNode("s")}}
			for i := 0; i <= layout.MaxArrangeRowWidth; i++ {
				id := fmt.Sprintf("n%d", i)
				wf.Nodes = append(wf.Nodes, boundsNode(id))
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "s", Target: id})
			}
			return wf
		},
		// Long edges reserve a slot in every rank they pass, so a narrow
		// graph can still hold too many row items.
		"layer items": func() *workflow.Workflow {
			const chain, long = 30, 25
			wf := &workflow.Workflow{Name: "long"}
			for i := 0; i < chain; i++ {
				wf.Nodes = append(wf.Nodes, boundsNode(fmt.Sprintf("c%d", i)))
				if i > 0 {
					wf.Edges = append(wf.Edges, workflow.Edge{Source: fmt.Sprintf("c%d", i-1), Target: fmt.Sprintf("c%d", i)})
				}
			}
			for i := 0; i < long; i++ {
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "c0", Target: fmt.Sprintf("c%d", chain-1)})
			}
			require.Less(t, 1+long, layout.MaxArrangeRowWidth, "fixture must stay under the row cap")
			return wf
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			_, err := arrangeWorkflow(context.Background(), t, build(), layout.DefaultOptions())
			require.ErrorIs(t, err, layout.ErrGraphTooLarge)
			assert.Less(t, time.Since(start), time.Second, "the cap check must precede crossing search")
		})
	}
}

// The caps exist to bound Arrange. A worst-case graph exactly at the edge and
// row caps must finish within a fixed bound (measured at a few seconds or
// less; the bound leaves headroom for slow and -race runs).
func TestArrange_GraphAtCapFinishesWithinBound(t *testing.T) {
	for _, w := range []int{layout.MaxArrangeRowWidth, 3} {
		t.Run(fmt.Sprintf("width %d", w), func(t *testing.T) {
			wf := crossingBipartite(w, layout.MaxArrangeEdges)
			require.Len(t, wf.Edges, layout.MaxArrangeEdges)
			start := time.Now()
			geo, err := arrangeWorkflow(context.Background(), t, wf, layout.DefaultOptions())
			elapsed := time.Since(start)
			require.NoError(t, err)
			require.Len(t, geo.Nodes, len(wf.Nodes))
			assert.Less(t, elapsed, 30*time.Second)
			t.Logf("arranged %d nodes / %d edges in %s", len(wf.Nodes), len(wf.Edges), elapsed)
		})
	}
}

func TestArrange_PreCancelledContextReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := arrangeWorkflow(ctx, t, crossingBipartite(layout.MaxArrangeRowWidth, layout.MaxArrangeEdges), layout.DefaultOptions())
	require.ErrorIs(t, err, context.Canceled)
}

// Cancellation mid-search returns promptly rather than finishing a refined
// (64-sweep) search that runs for many seconds at the caps.
func TestArrange_CancelledMidSearchReturnsPromptly(t *testing.T) {
	o := layout.DefaultOptions()
	o.CrossingSweeps = 64
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := arrangeWorkflow(ctx, t, crossingBipartite(layout.MaxArrangeRowWidth, layout.MaxArrangeEdges), o)
	elapsed := time.Since(start)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 3*time.Second)
}

func TestCoordinatorRun_OverCapGraphFailsArrangingBeforeMeasuring(t *testing.T) {
	wf := crossingBipartite(10, layout.MaxArrangeEdges+1)
	renderer := layouttest.NewFakeRenderer()
	co, err := layout.NewCoordinator(renderer, layouttest.NewFakeReviewer())
	require.NoError(t, err)
	res := co.Run(context.Background(), layout.Job{Revision: "rev-1", Manifest: wf, MaxAttempts: 3}, nil)
	require.NotNil(t, res.Failure)
	assert.Equal(t, layout.VerdictFailed, res.Verdict)
	assert.Equal(t, layout.StageArranging, res.Failure.Stage)
	assert.Contains(t, res.Failure.Reason, "too large to arrange")
	assert.Contains(t, res.Failure.Reason, fmt.Sprintf("limit of %d", layout.MaxArrangeEdges))
	assert.False(t, res.Cancelled)
	assert.Zero(t, renderer.MeasureCalls(), "over-cap graphs fail before any renderer work")
}

// Arrange has its own stage deadline: a search that outlives it fails the
// attempt at StageArranging instead of holding the worker slot.
func TestCoordinatorRun_ArrangeStageTimeout(t *testing.T) {
	o := layout.DefaultOptions()
	o.CrossingSweeps = 64
	renderer := layouttest.NewFakeRenderer()
	co, err := layout.NewCoordinator(renderer, layouttest.NewFakeReviewer(), layout.WithOptions(o), layout.WithStageTimeout(150*time.Millisecond))
	require.NoError(t, err)
	wf := crossingBipartite(layout.MaxArrangeRowWidth, layout.MaxArrangeEdges)
	start := time.Now()
	res := co.Run(context.Background(), layout.Job{Revision: "rev-1", Manifest: wf, MaxAttempts: 1}, nil)
	elapsed := time.Since(start)
	require.NotNil(t, res.Failure)
	assert.Equal(t, layout.StageArranging, res.Failure.Stage)
	assert.True(t, strings.Contains(res.Failure.Reason, context.DeadlineExceeded.Error()), res.Failure.Reason)
	assert.False(t, res.Cancelled, "a stage deadline is not job cancellation")
	assert.Equal(t, 1, renderer.MeasureCalls())
	assert.Zero(t, renderer.RenderCalls())
	assert.Less(t, elapsed, 3*time.Second)
}
