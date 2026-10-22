package layout_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Paired row 5: the Coordinator's mixed-crossing Score is persisted in real
// SQLite and compared by the Worker's storedBetter on a later Retry. A score
// is only comparable between results the Reader can display: a prior stored
// under another renderer contract ranks by different rules and is never served.
func TestWorkerScore_GroupCrossingsAcrossRetryGenerationsSQLite(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name         string
		priorOld     bool
		wantResult   int64
		wantReviewed bool
	}{
		// Equal scores, both compatible: the earlier best keeps its generation.
		{name: "equal compatible retry keeps the first best", wantResult: 1, wantReviewed: true},
		// Equal (or better) score, but the prior was arranged under another
		// contract. Keeping it leaves Retry unable to ever repair the view.
		{name: "incompatible prior is replaced by the compatible retry", priorOld: true, wantResult: 2, wantReviewed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSQLiteLayoutStore(t)
			r := newWorkerRig(t, 5, func(c *layout.WorkerConfig) {
				c.Store = store
				c.Requester = layout.NewRequester(store, 5, nil)
			})
			wf, _, _ := layouttest.CapturedGroup(t, "loop")
			rev := uuid.Must(uuid.NewV7()).String()
			wf.Presentation = &workflow.Presentation{Revision: rev, Scope: workflow.ScopeLabelShared, Name: "grouped"}
			req := workflow.ArrangementRequest{Revision: rev, Scope: workflow.Shared(), Name: "grouped", Manifest: layouttest.ManifestJSON(t, wf), RequestedAt: time.Now().UTC(), Reason: workflow.SaveReasonUpdate}
			require.NoError(t, r.cfg.Requester.RequestArrangement(ctx, req))
			r.manifests.Set(req.Scope, req.Name, rev)
			read := requestWorkflow(t, req)

			current := *r.cfg.Coordinator
			if tc.priorOld {
				old := layout.DefaultContract()
				old.Version = "arranged-renderer/1"
				co, err := layout.NewCoordinator(r.renderer, r.reviewer, layout.WithContract(old))
				require.NoError(t, err)
				*r.cfg.Coordinator = *co
			}
			finding := layout.Finding{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, NodeIDs: []string{"fan"}, Evidence: "same blocking finding"}
			blocked := []layouttest.ReviewScript{{Findings: []layout.Finding{finding}}, {Findings: []layout.Finding{finding}}, {Findings: []layout.Finding{finding}}, {Findings: []layout.Finding{finding}}, {Findings: []layout.Finding{finding}}}
			r.reviewer.SetScript(blocked...)
			if err := r.worker.ProcessRevision(ctx, rev); tc.priorOld {
				// This binary will not publish the old contract; an older one did.
				require.ErrorContains(t, err, "incompatible cache result")
			} else {
				require.NoError(t, err)
			}
			first, err := store.Result(ctx, rev, 1)
			require.NoError(t, err)
			require.NotEmpty(t, first.Geometry.GroupCrossings, "exercise real mixed crossings")
			require.Equal(t, len(first.Geometry.Crossings)+len(first.Geometry.GroupCrossings), first.Score.Crossings, "mixed crossings survive SQLite in the stored score")

			*r.cfg.Coordinator = current
			_, err = r.cfg.Requester.Retry(ctx, rev)
			require.NoError(t, err)
			r.reviewer.SetScript(blocked...)
			require.NoError(t, r.worker.ProcessRevision(ctx, rev))
			second, err := store.Result(ctx, rev, 2)
			require.NoError(t, err)
			require.False(t, second.Score.Less(first.Score), "the retry is no better by score alone")
			require.Equal(t, first.Score.Crossings, second.Score.Crossings)

			row, err := store.Get(ctx, rev)
			require.NoError(t, err)
			require.Equal(t, int64(2), row.JobGeneration)
			require.Equal(t, tc.wantResult, row.ResultGeneration)
			view := layout.NewReader(store, nil, nil).View(ctx, read)
			require.Equal(t, tc.wantResult, view.ResultGeneration)
			require.True(t, view.Available, view.FailureReason)
			require.NotNil(t, view.Geometry)
			want := first
			if tc.wantResult == 2 {
				want = second
			}
			require.Equal(t, want.Geometry, *view.Geometry)
			require.Equal(t, layout.DefaultContract(), view.Geometry.Contract)
			require.Equal(t, !tc.wantReviewed, view.Unreviewed)
		})
	}
}
