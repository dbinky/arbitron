package renderreview

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

type groupReviewAttempt struct {
	request layout.ReviewRequest
	review  layout.Review
}

// Observe the real review without changing its input, findings or evidence.
type recordingGroupReviewer struct {
	layout.Reviewer
	attempts []groupReviewAttempt
}

func (r *recordingGroupReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error) {
	review, err := r.Reviewer.Review(ctx, req)
	if err == nil {
		r.attempts = append(r.attempts, groupReviewAttempt{req, review})
	}
	return review, err
}

// G1: drive the long, automatically wrapped label through the real Measure →
// Arrange → Render → Review → Refine loop. It must reach a passed review within
// the attempt budget; clipping on every attempt is the seam failure.
func TestReadabilityReviewer_GroupActualWrapRefinementVerdict(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	raw, err := os.ReadFile("testdata/readability/group-branches/workflow.json")
	require.NoError(t, err)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(raw, &wf))
	for i := range wf.Nodes {
		wf.Nodes[i].Label = "Überprüfe sämtliche Änderungen — 審查所有拉取請求 — und bestätige jede Abhängigkeit gründlich"
	}
	before, err := json.Marshal(wf)
	require.NoError(t, err)
	g, err := layout.BuildGraph(&wf)
	require.NoError(t, err)
	r := &recordingGroupReviewer{Reviewer: NewReadabilityReviewer(nil)}
	c, err := layout.NewCoordinator(b, r)
	require.NoError(t, err)
	res := c.Run(context.Background(), layout.Job{Revision: "group-wrap-refinement", Manifest: &wf, MaxAttempts: 5}, nil)
	require.Nil(t, res.Failure, "%+v", res.Failure)
	require.NotEmpty(t, r.attempts)
	require.Equal(t, len(r.attempts), res.AttemptsUsed)
	require.LessOrEqual(t, res.AttemptsUsed, 5)
	require.NotNil(t, res.Best)
	require.NoError(t, res.Best.Geometry.Validate(g))
	require.Equal(t, g.Groups, r.attempts[0].request.WorkflowGroups)
	for i, a := range r.attempts {
		require.Equal(t, i+1, a.request.Attempt)
		require.NoError(t, a.request.ValidateGeometry())
		require.Equal(t, a.request.Rendering.Artifacts, a.review.Evidence.Artifacts)
		require.True(t, layout.HasRenderedEvidence(a.request.Geometry, &a.review), "%+v", a.review.Findings)
		t.Logf("attempt=%d slack=%.0f clipped=%d blocking=%d", i+1, a.request.Geometry.Options.NodeTextSlack,
			len(withCriterion(a.review.Findings, layout.CriterionTextClipped)), layout.BlockingCount(a.review.Findings))
		if i > 0 {
			require.NotEqual(t, layout.GeometryDigest(r.attempts[i-1].request.Geometry), layout.GeometryDigest(a.request.Geometry), "refinement must reach changed geometry")
		}
	}
	best := r.attempts[res.Best.Attempt-1]
	require.Equal(t, best.request.Geometry, res.Best.Geometry)
	require.Equal(t, best.review, *res.Best.Review)
	require.False(t, res.Unreviewed)
	require.Equal(t, layout.VerdictPassed, res.Verdict, "%+v", res.Findings)
	require.True(t, res.Best.Passed)
	require.Zero(t, layout.BlockingCount(res.Findings), "%+v", res.Findings)
	require.Empty(t, withCriterion(res.Findings, layout.CriterionTextClipped))
	after, err := json.Marshal(wf)
	require.NoError(t, err)
	require.Equal(t, before, after)
	t.Logf("verdict=%s attempts=%d best=%d", res.Verdict, res.AttemptsUsed, res.Best.Attempt)
}
