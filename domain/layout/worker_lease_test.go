package layout_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/stretchr/testify/require"
)

func TestWorkerLeaseHeartbeatPreventsReclaim(t *testing.T) {
	r := newWorkerRig(t, 1, func(c *layout.WorkerConfig) { c.LeaseDuration = 45 * time.Millisecond })
	held := &heldRenderer{Renderer: r.renderer, entered: make(chan struct{}, 1), release: make(chan struct{})}
	co, err := layout.NewCoordinator(held, r.reviewer)
	require.NoError(t, err)
	cfg := r.cfg
	cfg.Coordinator = co
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	defer r.worker.Stop()
	req := r.request(t)
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(context.Background(), req.Revision) }()
	<-held.entered
	time.Sleep(140 * time.Millisecond)
	otherRenderer := layouttest.NewFakeRenderer()
	otherCo, err := layout.NewCoordinator(otherRenderer, layouttest.NewFakeReviewer())
	require.NoError(t, err)
	otherCfg := r.cfg
	otherCfg.Owner = "second-worker"
	otherCfg.Coordinator = otherCo
	other, err := layout.NewWorker(otherCfg)
	require.NoError(t, err)
	defer other.Stop()
	require.NoError(t, other.ProcessRevision(context.Background(), req.Revision))
	row := r.row(t, req.Revision)
	require.Equal(t, int64(1), row.JobGeneration)
	require.Equal(t, "test-worker", row.LeaseOwner)
	require.Equal(t, layout.StageRendering, row.Stage)
	require.Equal(t, 1, row.AttemptsUsed)
	require.True(t, row.LeaseExpiresAt.After(time.Now()))
	close(held.release)
	require.NoError(t, <-done)
	row = r.row(t, req.Revision)
	require.Equal(t, layout.StatePassed, row.State)
	require.Equal(t, int64(1), row.ResultGeneration)
	require.Equal(t, int64(1), row.JobGeneration)
}
func TestWorkerLeaseLossCancelsAndFencesWrites(t *testing.T) {
	r := newWorkerRig(t, 1, func(c *layout.WorkerConfig) { c.LeaseDuration = 30 * time.Millisecond })
	req := r.request(t)
	gate := gatedWorker(t, r, "render", 1, nil)
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(context.Background(), req.Revision) }()
	<-gate.entered
	future := time.Now().Add(time.Minute)
	otherCfg := r.cfg
	otherCfg.Owner = "new-owner"
	otherCfg.Clock = func() time.Time { return future }
	other, err := layout.NewWorker(otherCfg)
	require.NoError(t, err)
	defer other.Stop()
	require.NoError(t, other.ProcessRevision(context.Background(), req.Revision))
	select {
	case err = <-done:
		require.ErrorIs(t, err, layout.ErrStaleGeneration)
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel renderer")
	}
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StatePassed, row.State)
	require.Equal(t, int64(2), row.JobGeneration)
	require.Equal(t, int64(2), row.ResultGeneration)
	best, err := r.store.Result(context.Background(), req.Revision, 2)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review))
	_, err = r.store.Result(context.Background(), req.Revision, 1)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)
}
func TestWorkerLeaseSupersedeKeepsBest(t *testing.T) {
	r := newWorkerRig(t, 5, func(c *layout.WorkerConfig) { c.LeaseDuration = 30 * time.Millisecond })
	req := r.request(t)
	r.reviewer.SetScript(blockingScript())
	gate := gatedWorker(t, r, "render", 2, nil)
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(context.Background(), req.Revision) }()
	<-gate.entered
	require.NoError(t, r.store.Supersede(context.Background(), req.Revision, time.Now()))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("supersede did not cancel renderer")
	}
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateSuperseded, row.State)
	best, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review))
	require.Equal(t, "overlap", best.Review.Findings[0].Evidence)
}
func TestWorkerLeaseHeartbeatAttemptAndHistory(t *testing.T) {
	r := newWorkerRig(t, 5, func(c *layout.WorkerConfig) { c.LeaseDuration = 30 * time.Millisecond })
	req := r.request(t)
	r.reviewer.SetScript(blockingScript())
	gate := gatedWorker(t, r, "review", 2, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(ctx, req.Revision) }()
	<-gate.entered
	time.Sleep(50 * time.Millisecond)
	row := r.row(t, req.Revision)
	require.Equal(t, 2, row.AttemptsUsed)
	require.Equal(t, layout.StageReviewing, row.Stage)
	require.True(t, row.LeaseExpiresAt.After(time.Now()))
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	// Reclaim historical interrupted work even though a new save is current.
	newer := r.input(t)
	r.committed.Prepare(newer)
	r.committed.Commit(newer.Revision)
	require.NoError(t, r.worker.Recover(context.Background()))
	r.manifests.Set(newer.Scope, newer.Name, newer.Revision)
	future := row.LeaseExpiresAt.Add(time.Minute)
	cfg := r.cfg
	cfg.Clock = func() time.Time { return future }
	w, err := layout.NewWorker(cfg)
	require.NoError(t, err)
	defer w.Stop()
	r.reviewer.SetScript()
	require.NoError(t, w.ProcessRevision(context.Background(), req.Revision))
	row = r.row(t, req.Revision)
	require.Equal(t, layout.StatePassed, row.State)
	require.Equal(t, int64(2), row.JobGeneration)
	require.Empty(t, r.cache.Published(row.Destination))
}
