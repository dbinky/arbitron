package layout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func TestWorker_GroupRetainedAfterLaterErrorsAndWorseRetry(t *testing.T) {
	for _, kind := range []string{"later review error", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			r := newWorkerRig(t, 5)
			req := r.input(t)
			wf, _, _ := layouttest.CapturedGroup(t, "nested")
			wf.Presentation = &workflow.Presentation{Revision: req.Revision}
			req.Manifest = layouttest.ManifestJSON(t, wf)
			require.NoError(t, r.requester.RequestArrangement(context.Background(), req))
			r.manifests.Set(req.Scope, req.Name, req.Revision)
			before := append([]byte(nil), req.Manifest...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.reviewer.SetScript(layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, NodeIDs: []string{"fan"}, Evidence: "first group candidate"}}}, layouttest.ReviewScript{Err: errors.New("later review failed")})
			if kind == "cancellation" {
				r.renderer.OnRender = func(q layout.RenderRequest) {
					if q.Attempt == 2 {
						cancel()
					}
				}
			}
			err := r.worker.ProcessRevision(ctx, req.Revision)
			if kind == "cancellation" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			row := r.row(t, req.Revision)
			require.Equal(t, int64(1), row.ResultGeneration)
			best, err := r.store.Result(context.Background(), req.Revision, 1)
			require.NoError(t, err)
			require.NotEmpty(t, best.Geometry.GroupElements)
			require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review))
			require.Equal(t, "first group candidate", best.Review.Findings[0].Evidence)
			require.Equal(t, before, row.Manifest)
			if kind == "cancellation" {
				view := layout.NewReader(r.store, nil, nil).View(context.Background(), wf)
				require.NotNil(t, view.Geometry)
				require.Equal(t, best.Geometry, *view.Geometry)
				viewed := best.Review.Evidence.ForViewer()
				require.Equal(t, &viewed, view.Evidence)
				require.Equal(t, int64(1), view.ResultGeneration)
				require.Equal(t, row.JobGeneration, view.JobGeneration)
				return
			}
			_, err = r.requester.Retry(context.Background(), req.Revision)
			require.NoError(t, err)
			r.renderer.OnRender = nil
			r.renderer.RenderErr = errors.New("worse group retry")
			require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
			after := r.row(t, req.Revision)
			require.Equal(t, int64(1), after.ResultGeneration)
			require.Equal(t, int64(2), after.JobGeneration)
			retained, err := r.store.Result(context.Background(), req.Revision, after.ResultGeneration)
			require.NoError(t, err)
			require.Equal(t, best, retained)
			require.Equal(t, before, after.Manifest)
			view := layout.NewReader(r.store, nil, nil).View(context.Background(), wf)
			require.NotNil(t, view.Geometry)
			require.Equal(t, best.Geometry, *view.Geometry)
			viewed := best.Review.Evidence.ForViewer()
			require.Equal(t, &viewed, view.Evidence)
			require.True(t, view.RetryEligible)
			require.Equal(t, int64(1), view.ResultGeneration, "retained best keeps its own generation")
			require.Equal(t, int64(2), view.JobGeneration)
		})
	}
}
