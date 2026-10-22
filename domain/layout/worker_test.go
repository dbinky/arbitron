package layout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type workerRig struct {
	store     *layouttest.MemStore
	renderer  *layouttest.FakeRenderer
	reviewer  *layouttest.FakeReviewer
	manifests *layouttest.FakeManifests
	journal   *layouttest.FakeJournal
	committed *layouttest.FakeCommittedRevisions
	cache     *layouttest.FakeCache
	requester *layout.Requester
	cfg       layout.WorkerConfig
	worker    *layout.Worker
}

func newWorkerRig(t *testing.T, limit int, options ...func(*layout.WorkerConfig)) *workerRig {
	t.Helper()
	r := &workerRig{store: layouttest.NewMemStore(), renderer: layouttest.NewFakeRenderer(), reviewer: layouttest.NewFakeReviewer(), manifests: layouttest.NewFakeManifests(), journal: layouttest.NewFakeJournal(), committed: layouttest.NewFakeCommittedRevisions(), cache: layouttest.NewFakeCache()}
	r.requester = layout.NewRequester(r.store, limit, nil)
	co, err := layout.NewCoordinator(r.renderer, r.reviewer)
	require.NoError(t, err)
	r.cfg = layout.WorkerConfig{Store: r.store, Coordinator: co, Journal: r.journal, Committed: r.committed, Manifests: r.manifests, Cache: r.cache, Requester: r.requester, Owner: "test-worker"}
	for _, o := range options {
		o(&r.cfg)
	}
	r.worker, err = layout.NewWorker(r.cfg)
	require.NoError(t, err)
	return r
}
func (r *workerRig) input(t *testing.T) workflow.ArrangementRequest {
	t.Helper()
	wf := layouttest.SampleWorkflow()
	rev := uuid.Must(uuid.NewV7()).String()
	wf.Presentation = &workflow.Presentation{Revision: rev}
	req := workflow.ArrangementRequest{Revision: rev, Scope: workflow.Scope{Kind: workflow.ScopeShared}, Name: "sample", Manifest: layouttest.ManifestJSON(t, wf), RequestedAt: time.Now().UTC(), Reason: workflow.SaveReasonUpdate}
	return req
}
func (r *workerRig) request(t *testing.T) workflow.ArrangementRequest {
	t.Helper()
	req := r.input(t)
	require.NoError(t, r.requester.RequestArrangement(context.Background(), req))
	r.manifests.Set(req.Scope, req.Name, req.Revision)
	return req
}
func (r *workerRig) row(t *testing.T, rev string) layout.Record {
	t.Helper()
	row, err := r.store.Get(context.Background(), rev)
	require.NoError(t, err)
	return row
}
func TestNewWorkerRequiredPorts(t *testing.T) {
	r := newWorkerRig(t, 1)
	for _, remove := range []func(*layout.WorkerConfig){func(c *layout.WorkerConfig) { c.Store = nil }, func(c *layout.WorkerConfig) { c.Coordinator = nil }, func(c *layout.WorkerConfig) { c.Journal = nil }, func(c *layout.WorkerConfig) { c.Committed = nil }, func(c *layout.WorkerConfig) { c.Manifests = nil }, func(c *layout.WorkerConfig) { c.Cache = nil }, func(c *layout.WorkerConfig) { c.Requester = nil }} {
		cfg := r.cfg
		remove(&cfg)
		_, err := layout.NewWorker(cfg)
		require.ErrorIs(t, err, layout.ErrWorkerMisconfigured)
	}
}
func TestWorkerProcessCapturedInput(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	// The only mutable producer now points elsewhere; captured graph remains authoritative.
	r.manifests.Set(req.Scope, req.Name, "later-save")
	require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StatePassed, row.State)
	require.Equal(t, 1, row.AttemptsUsed)
	require.Equal(t, 1, row.AttemptLimit)
	result, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
	require.NoError(t, err)
	graph, err := layout.BuildGraph(layouttest.SampleWorkflow())
	require.NoError(t, err)
	require.NoError(t, result.Geometry.Validate(graph))
	require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
	require.Equal(t, r.renderer.Digests()[0], layout.GeometryDigest(result.Geometry))
	require.Equal(t, req.Revision, result.Revision)
	require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
	require.Equal(t, 1, r.renderer.RenderCalls())
	require.NoError(t, r.worker.ProcessRevision(context.Background(), "missing"))
}
func TestWorkerProcessBudgetsAndFailure(t *testing.T) {
	for _, limit := range []int{1, 5} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			r := newWorkerRig(t, limit)
			req := r.request(t)
			scripts := make([]layouttest.ReviewScript, limit)
			for i := range scripts {
				scripts[i].Findings = []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, NodeIDs: []string{"start", "check"}, Evidence: "overlap"}}
			}
			r.reviewer.SetScript(scripts...)
			require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
			row := r.row(t, req.Revision)
			require.Equal(t, layout.StateNeedsAttention, row.State)
			require.Equal(t, limit, row.AttemptsUsed)
			require.Equal(t, limit, r.reviewer.Calls())
			require.Equal(t, "budget", row.FailureReason)
			require.NotEmpty(t, row.Findings)
		})
	}
	t.Run("no refinement", func(t *testing.T) {
		r := newWorkerRig(t, 5)
		req := r.request(t)
		r.reviewer.SetScript(layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionRenderMismatch, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: "cannot refine"}}})
		require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
		row := r.row(t, req.Revision)
		require.Equal(t, 1, row.AttemptsUsed)
		require.Equal(t, "no_refinement", row.FailureReason)
		require.Equal(t, layout.StateNeedsAttention, row.State)
	})
	t.Run("later review error keeps own evidence", func(t *testing.T) {
		r := newWorkerRig(t, 5)
		req := r.request(t)
		r.reviewer.SetScript(layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, NodeIDs: []string{"start", "check"}, Evidence: "first candidate"}}}, layouttest.ReviewScript{Err: errors.New("review exploded")})
		require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
		row := r.row(t, req.Revision)
		require.Equal(t, layout.StateFailed, row.State)
		require.Equal(t, 2, row.AttemptsUsed)
		require.Equal(t, layout.StageReviewing, row.FailureStage)
		require.Equal(t, "rendered review failed", row.FailureReason)
		best, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
		require.NoError(t, err)
		require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review))
		require.Equal(t, "first candidate", best.Review.Findings[0].Evidence)
		oldDigest := layout.GeometryDigest(best.Geometry)
		oldFindings := best.UnresolvedFindings()
		_, err = r.store.Retry(context.Background(), req.Revision, 1, time.Now())
		require.NoError(t, err)
		r.renderer.RenderErr = errors.New("worse retry")
		require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
		row = r.row(t, req.Revision)
		require.Equal(t, int64(1), row.ResultGeneration)
		require.Equal(t, oldFindings, row.Findings)
		retained, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
		require.NoError(t, err)
		require.Equal(t, oldDigest, layout.GeometryDigest(retained.Geometry))
	})
	t.Run("measure failure has no candidate", func(t *testing.T) {
		r := newWorkerRig(t, 5)
		req := r.request(t)
		r.renderer.MeasureErr = errors.New("fonts missing")
		require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
		row := r.row(t, req.Revision)
		require.Equal(t, layout.StateFailed, row.State)
		require.Zero(t, row.ResultGeneration)
		require.Equal(t, 1, row.AttemptsUsed)
		require.Equal(t, layout.StageRendering, row.FailureStage)
	})
}

type unboundRenderer struct{ layout.Renderer }

func (r unboundRenderer) Render(ctx context.Context, q layout.RenderRequest) (layout.Rendering, error) {
	out, err := r.Renderer.Render(ctx, q)
	out.Artifacts = nil
	return out, err
}
func TestWorkerProcessMissingEvidence(t *testing.T) {
	r := newWorkerRig(t, 1)
	co, err := layout.NewCoordinator(unboundRenderer{r.renderer}, r.reviewer)
	require.NoError(t, err)
	cfg := r.cfg
	cfg.Coordinator = co
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	req := r.request(t)
	require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateFailed, row.State)
	require.NotEqual(t, layout.VerdictPassed, row.Verdict)
	result, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
	require.NoError(t, err)
	require.False(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
}
func TestWorkerProcessCorruptInputAndOutage(t *testing.T) {
	for _, raw := range []string{"{", `{"nodes":[]}`, `{"presentation":{"revision":"wrong"}}`} {
		r := newWorkerRig(t, 1)
		rev := uuid.Must(uuid.NewV7()).String()
		fallback := workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"start": {X: 12, Y: 34}}}
		_, _, err := r.store.Request(context.Background(), layout.NewRequest{Revision: rev, Manifest: []byte(raw), Retained: fallback, AttemptLimit: 1, RequestedAt: time.Now()})
		require.NoError(t, err)
		require.NoError(t, r.worker.ProcessRevision(context.Background(), rev))
		row := r.row(t, rev)
		require.Equal(t, layout.StateFailed, row.State)
		require.Equal(t, layout.StageArranging, row.FailureStage)
		require.Equal(t, fallback, row.Retained)
		require.Zero(t, r.renderer.RenderCalls())
	}
	r := newWorkerRig(t, 1)
	req := r.request(t)
	r.store.SetFailure(layout.ErrStoreUnavailable)
	require.ErrorIs(t, r.worker.ProcessRevision(context.Background(), req.Revision), layout.ErrStoreUnavailable)
	r.store.SetFailure(nil)
	require.Equal(t, layout.StatePending, r.row(t, req.Revision).State)
}
