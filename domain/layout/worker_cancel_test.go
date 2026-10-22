package layout_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/stretchr/testify/require"
)

type stageGate struct {
	renderer layout.Renderer
	reviewer layout.Reviewer
	stage    string
	attempt  int
	entered  chan struct{}
}

func (g *stageGate) wait(ctx context.Context, stage string, attempt int) error {
	if g.stage == stage && (g.attempt == 0 || attempt == g.attempt) {
		close(g.entered)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (g *stageGate) Measure(ctx context.Context, q layout.MeasureRequest) (layout.Measurements, error) {
	if err := g.wait(ctx, "measure", 1); err != nil {
		return layout.Measurements{}, err
	}
	return g.renderer.Measure(ctx, q)
}
func (g *stageGate) Render(ctx context.Context, q layout.RenderRequest) (layout.Rendering, error) {
	if err := g.wait(ctx, "render", q.Attempt); err != nil {
		return layout.Rendering{}, err
	}
	return g.renderer.Render(ctx, q)
}
func (g *stageGate) Review(ctx context.Context, q layout.ReviewRequest) (layout.Review, error) {
	if err := g.wait(ctx, "review", q.Attempt); err != nil {
		return layout.Review{}, err
	}
	return g.reviewer.Review(ctx, q)
}
func gatedWorker(t *testing.T, r *workerRig, stage string, attempt int, store layout.Store) *stageGate {
	t.Helper()
	gate := &stageGate{renderer: r.renderer, reviewer: r.reviewer, stage: stage, attempt: attempt, entered: make(chan struct{})}
	co, err := layout.NewCoordinator(gate, gate)
	require.NoError(t, err)
	cfg := r.cfg
	cfg.Coordinator = co
	if store != nil {
		cfg.Store = store
	}
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	return gate
}
func TestWorkerStageTimeouts(t *testing.T) {
	for _, stage := range []string{"measure", "render", "review"} {
		t.Run(stage, func(t *testing.T) {
			r := newWorkerRig(t, 5, func(c *layout.WorkerConfig) { c.StageTimeout = 15 * time.Millisecond })
			req := r.request(t)
			gatedWorker(t, r, stage, 1, nil)
			require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
			row := r.row(t, req.Revision)
			require.Equal(t, layout.StateFailed, row.State)
			want := layout.StageRendering
			if stage == "review" {
				want = layout.StageReviewing
			}
			require.Equal(t, want, row.FailureStage)
			require.Contains(t, row.FailureReason, "timed out")
			require.Equal(t, 1, row.AttemptsUsed)
		})
	}
}

type checkpointStore struct {
	layout.Store
	deadline chan time.Duration
}

func (s *checkpointStore) Checkpoint(ctx context.Context, f layout.Fence, r layout.StoredResult, replace bool, now time.Time) error {
	d, ok := ctx.Deadline()
	if !ok {
		s.deadline <- 0
		return errors.New("checkpoint missing deadline")
	}
	s.deadline <- time.Until(d)
	return s.Store.Checkpoint(ctx, f, r, replace, now)
}
func TestWorkerShutdownCheckpointsAndReclaims(t *testing.T) {
	r := newWorkerRig(t, 5)
	req := r.request(t)
	r.reviewer.SetScript(layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: "retained review"}}})
	spy := &checkpointStore{Store: r.store, deadline: make(chan time.Duration, 1)}
	gate := gatedWorker(t, r, "render", 2, spy)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(ctx, req.Revision) }()
	<-gate.entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	d := <-spy.deadline
	require.Greater(t, d, time.Duration(0))
	require.LessOrEqual(t, d, 30*time.Second)
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateInProgress, row.State)
	require.Nil(t, row.FinishedAt)
	require.NotNil(t, row.LeaseExpiresAt)
	require.Equal(t, int64(1), row.ResultGeneration)
	best, err := r.store.Result(context.Background(), req.Revision, 1)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review))
	require.Equal(t, "retained review", best.Review.Findings[0].Evidence)
	fence := layout.Fence{Revision: req.Revision, Generation: 1}
	require.NoError(t, r.store.Checkpoint(context.Background(), fence, best, true, time.Now()))
	conflict := best
	conflict.CreatedAt = conflict.CreatedAt.Add(time.Second)
	require.Error(t, r.store.Checkpoint(context.Background(), fence, conflict, true, time.Now()))
	later := row.LeaseExpiresAt.Add(time.Second)
	claimed, err := r.store.Claim(context.Background(), req.Revision, "new-owner", later, later.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(2), claimed.JobGeneration)
	require.ErrorIs(t, r.store.Checkpoint(context.Background(), fence, best, true, later), layout.ErrStaleGeneration)
	require.ErrorIs(t, r.store.Complete(context.Background(), fence, layout.Completion{State: layout.StatePassed}, later), layout.ErrStaleGeneration)
}
func TestWorkerShutdownNoCandidatePreservesPriorBest(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	r.reviewer.SetScript(blockingScript())
	require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
	original := r.row(t, req.Revision)
	_, err := r.store.Retry(context.Background(), req.Revision, 1, time.Now())
	require.NoError(t, err)
	gate := gatedWorker(t, r, "measure", 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(ctx, req.Revision) }()
	<-gate.entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateInProgress, row.State)
	require.Equal(t, int64(2), row.JobGeneration)
	require.Equal(t, original.ResultGeneration, row.ResultGeneration)
	require.Equal(t, original.Findings, row.Findings)
}
func TestWorkerJobStageTimeoutCopyIsIndependent(t *testing.T) {
	r := newWorkerRig(t, 1)
	r.renderer.RenderDelay = 40 * time.Millisecond
	fast := r.cfg
	fast.StageTimeout = 5 * time.Millisecond
	slow := r.cfg
	slow.StageTimeout = 200 * time.Millisecond
	short, err := layout.NewWorker(fast)
	require.NoError(t, err)
	defer short.Stop()
	long, err := layout.NewWorker(slow)
	require.NoError(t, err)
	defer long.Stop()
	a := r.request(t)
	b := r.request(t)
	done := make(chan error, 2)
	go func() { done <- short.ProcessRevision(context.Background(), a.Revision) }()
	go func() { done <- long.ProcessRevision(context.Background(), b.Revision) }()
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	require.Equal(t, layout.StateFailed, r.row(t, a.Revision).State)
	require.Equal(t, layout.StatePassed, r.row(t, b.Revision).State)
}
func TestWorkerShutdownBeforeCandidateLeavesRecoverable(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	gate := gatedWorker(t, r, "measure", 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(ctx, req.Revision) }()
	<-gate.entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateInProgress, row.State)
	require.Zero(t, row.ResultGeneration)
}
func TestWorkerFinalizerBoundedAndOutageRecoverable(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	spy := &finalizerStore{Store: r.store, deadline: make(chan time.Duration, 1), release: make(chan struct{})}
	cfg := r.cfg
	cfg.Store = spy
	var err error
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(context.Background(), req.Revision) }()
	d := <-spy.deadline
	close(spy.release)
	require.ErrorIs(t, <-done, layout.ErrStoreUnavailable)
	require.Greater(t, d, time.Duration(0))
	require.LessOrEqual(t, d, 30*time.Second)
	require.Equal(t, layout.StateInProgress, r.row(t, req.Revision).State)
}

type finalizerStore struct {
	layout.Store
	deadline chan time.Duration
	release  chan struct{}
}

func (s *finalizerStore) Complete(ctx context.Context, f layout.Fence, c layout.Completion, now time.Time) error {
	d, ok := ctx.Deadline()
	if !ok {
		s.deadline <- 0
		return errors.New("unbounded")
	}
	s.deadline <- time.Until(d)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return layout.ErrStoreUnavailable
	}
}

func TestWorkerDeadlineRetainsReviewedBest(t *testing.T) {
	r := newWorkerRig(t, 5, func(c *layout.WorkerConfig) { c.JobTimeout = 30 * time.Millisecond })
	req := r.request(t)
	r.reviewer.SetScript(layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: "first"}}})
	gatedWorker(t, r, "review", 2, nil)
	require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateFailed, row.State)
	require.Equal(t, layout.StageReviewing, row.FailureStage)
	require.True(t, strings.Contains(row.FailureReason, "rendered review timed out"))
	best, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
	require.NoError(t, err)
	require.Equal(t, "first", best.Review.Findings[0].Evidence)
}
