package layouttest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func renderRequest(t *testing.T) layout.RenderRequest {
	wf := layouttest.SampleWorkflow()
	types := map[string]string{}
	for _, n := range wf.Nodes {
		types[n.ID] = string(n.Type)
	}
	return layout.NewRenderRequest("saved", 1, layouttest.Arranged(t, wf), types)
}
func TestFakeRendererAndReviewerArguments(t *testing.T) {
	ctx := context.Background()
	r := layouttest.NewFakeRenderer()
	reviewer := layouttest.NewFakeReviewer()
	req := renderRequest(t)
	for i := 1; i <= 2; i++ {
		req.Revision += "next"
		req.Attempt = i
		if i == 2 {
			req.Geometry.Canvas.W += 50
		}
		out, err := r.Render(ctx, req)
		require.NoError(t, err)
		require.Equal(t, req.Revision, out.Revision)
		require.Equal(t, i, out.Attempt)
		require.Equal(t, layout.GeometryDigest(req.Geometry), out.GeometryDigest)
		require.Equal(t, len(req.Geometry.Nodes), len(out.Report.Nodes))
		require.Equal(t, len(req.Geometry.Edges), len(out.Report.Edges))
		require.Len(t, out.Report.States, len(req.States))
		for j, n := range req.Geometry.Nodes {
			require.Equal(t, n.Text, out.Report.Nodes[j].Label.Content)
			require.Equal(t, n.TypeCaption, out.Report.Nodes[j].Type.Content)
		}
		review, err := reviewer.Review(ctx, layout.ReviewRequest{Revision: req.Revision, Attempt: i, Geometry: req.Geometry, Rendering: out})
		require.NoError(t, err)
		require.True(t, layout.HasRenderedEvidence(req.Geometry, &review))
		require.Equal(t, out.Artifacts, review.Evidence.Artifacts)
		bad := out
		bad.Report.Nodes = bad.Report.Nodes[:1]
		_, err = reviewer.Review(ctx, layout.ReviewRequest{Revision: req.Revision, Attempt: i, Geometry: req.Geometry, Rendering: bad})
		require.Error(t, err)
	}
	require.NotEqual(t, r.Digests()[0], r.Digests()[1])
	require.Equal(t, []string{"savednext", "savednextnext"}, r.Revisions())
	require.Equal(t, []int{1, 1, 2, 2}, reviewer.Attempts())
	for _, mutate := range []func(*layout.RenderRequest){
		func(q *layout.RenderRequest) { q.WorkflowGroups = &workflow.GroupGraph{Order: []string{"unexpected"}} },
		func(q *layout.RenderRequest) { q.Revision = "" }, func(q *layout.RenderRequest) { q.Attempt = 0 }, func(q *layout.RenderRequest) { q.Geometry = layout.Geometry{} }, func(q *layout.RenderRequest) { q.WorkflowNodeTypes = nil }, func(q *layout.RenderRequest) { q.WorkflowNodeTypes["start"] = "agent_call" }, func(q *layout.RenderRequest) { q.States = nil },
	} {
		q := renderRequest(t)
		mutate(&q)
		_, err := r.Render(ctx, q)
		require.Error(t, err)
	}
	_, err := reviewer.Review(ctx, layout.ReviewRequest{})
	require.Error(t, err)
	out, err := r.Render(ctx, req)
	require.NoError(t, err)
	for _, mutate := range []func(*layout.ReviewRequest){
		func(q *layout.ReviewRequest) { q.WorkflowGroups = &workflow.GroupGraph{Order: []string{"unexpected"}} },
		func(q *layout.ReviewRequest) { q.Revision = "wrong" }, func(q *layout.ReviewRequest) { q.Attempt++ }, func(q *layout.ReviewRequest) { q.Rendering.GeometryDigest = "wrong" }, func(q *layout.ReviewRequest) { q.Rendering.Report.Contract.NodePaddingX++ }, func(q *layout.ReviewRequest) { q.Rendering.Report.Nodes[0].Type.Content = "wrong" },
	} {
		// Get a fresh report; a mutation must not alias a prior capture.
		out, err = r.Render(ctx, req)
		require.NoError(t, err)
		q := layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, Rendering: out}
		mutate(&q)
		_, err = reviewer.Review(ctx, q)
		require.Error(t, err)
	}
	sentinel := errors.New("review failed")
	reviewer.SetScript(layouttest.ReviewScript{Err: sentinel})
	out, err = r.Render(ctx, req)
	require.NoError(t, err)
	_, err = reviewer.Review(ctx, layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, Rendering: out})
	require.ErrorIs(t, err, sentinel)
}
func TestFakeRendererChangedTypesAndReviewerConcurrency(t *testing.T) {
	r := layouttest.NewFakeRenderer()
	reviewer := layouttest.NewFakeReviewer()
	req := renderRequest(t)
	req.WorkflowNodeTypes["start"] = "tool_call"
	req.Geometry.Nodes[0].TypeCaption = "tool call"
	out, err := r.Render(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "tool call", out.Report.Nodes[0].Type.Content)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reviewer.SetScript(layouttest.ReviewScript{})
			review, err := reviewer.Review(context.Background(), layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, Rendering: out})
			require.NoError(t, err)
			require.True(t, layout.HasRenderedEvidence(req.Geometry, &review))
			_ = reviewer.Calls()
			_ = reviewer.Attempts()
		}()
	}
	wg.Wait()
	require.Equal(t, 10, reviewer.Calls())
}
func TestFakeRendererMeasurementsAndCancellation(t *testing.T) {
	r := layouttest.NewFakeRenderer()
	c := layout.DefaultContract()
	req := layout.MeasureRequest{Contract: c, Items: []layout.TextItem{{Key: "text", Text: "Long measurement text", Style: layout.StyleNodeLabel, MaxWidth: 200}}}
	a, err := r.Measure(context.Background(), req)
	require.NoError(t, err)
	req.Items[0].Style = layout.StyleNodeType
	b, err := r.Measure(context.Background(), req)
	require.NoError(t, err)
	require.NotEqual(t, a.Items, b.Items)
	req.Items[0].MaxWidth = 20
	b, err = r.Measure(context.Background(), req)
	require.NoError(t, err)
	require.Greater(t, b.Items["text"].Lines, a.Items["text"].Lines)
	req.Items[0].Text = "x"
	b, err = r.Measure(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, b.Items["text"].Lines)
	r.RenderDelay = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = r.Render(ctx, renderRequest(t))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 4, r.MeasureCalls())
}
func TestFakeRendererConcurrentCounters(t *testing.T) {
	r := layouttest.NewFakeRenderer()
	r.RenderDelay = 20 * time.Millisecond
	q := renderRequest(t)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Render(context.Background(), q)
			require.NoError(t, err)
			_ = r.RenderCalls()
			_ = r.Digests()
			_ = r.Revisions()
		}()
	}
	wg.Wait()
	require.Equal(t, 10, r.RenderCalls())
	require.Greater(t, r.MaxInFlight(), 1)
}
