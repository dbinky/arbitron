package layout_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	require.Eventually(t, check, 3*time.Second, time.Millisecond)
}
func TestWorkerLifecycleNotifiedAndDurable(t *testing.T) {
	r := newWorkerRig(t, 1, func(c *layout.WorkerConfig) { c.PollInterval = 10 * time.Millisecond })
	before := r.request(t)
	r.worker.Notify(before.Revision)
	r.worker.Start(context.Background())
	r.worker.Start(context.Background())
	defer r.worker.Stop()
	// One save delivers a hint, another relies solely on the durable poll.
	r.requester.SetNotifier(r.worker.Notify)
	notified := r.request(t)
	r.requester.SetNotifier(nil)
	silent := r.request(t)
	eventually(t, func() bool {
		return r.row(t, before.Revision).State.Terminal() && r.row(t, notified.Revision).State.Terminal() && r.row(t, silent.Revision).State.Terminal()
	})
	for _, rev := range []string{before.Revision, notified.Revision, silent.Revision} {
		row := r.row(t, rev)
		require.Equal(t, layout.StatePassed, row.State)
		result, err := r.store.Result(context.Background(), rev, row.ResultGeneration)
		require.NoError(t, err)
		require.Len(t, result.Geometry.Nodes, 4)
		require.Len(t, result.Geometry.Edges, 4)
		require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
	}
	r.worker.Stop()
	r.worker.Stop()
	calls := r.renderer.RenderCalls()
	r.worker.Notify("after-stop")
	r.worker.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, calls, r.renderer.RenderCalls())
}

type heldRenderer struct {
	layout.Renderer
	entered chan struct{}
	release chan struct{}
	active  atomic.Int32
	max     atomic.Int32
}

func (r *heldRenderer) Render(ctx context.Context, q layout.RenderRequest) (layout.Rendering, error) {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		old := r.max.Load()
		if n <= old || r.max.CompareAndSwap(old, n) {
			break
		}
	}
	r.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return layout.Rendering{}, ctx.Err()
	case <-r.release:
		return r.Renderer.Render(ctx, q)
	}
}
func TestWorkerLifecycleConcurrencyAndOverflow(t *testing.T) {
	r := newWorkerRig(t, 1, func(c *layout.WorkerConfig) { c.PollInterval = 5 * time.Millisecond })
	held := &heldRenderer{Renderer: r.renderer, entered: make(chan struct{}, 10), release: make(chan struct{})}
	co, err := layout.NewCoordinator(held, r.reviewer)
	require.NoError(t, err)
	cfg := r.cfg
	cfg.Coordinator = co
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	var revs []string
	for range 10 {
		revs = append(revs, r.request(t).Revision)
	}
	for i := range 1000 {
		r.worker.Notify(fmt.Sprintf("overflow-hint-%d", i))
	}
	for _, rev := range revs {
		r.worker.Notify(rev)
	}
	r.worker.Start(context.Background())
	defer r.worker.Stop()
	<-held.entered
	<-held.entered
	time.Sleep(15 * time.Millisecond)
	require.Equal(t, int32(2), held.max.Load())
	close(held.release)
	eventually(t, func() bool {
		for _, rev := range revs {
			if !r.row(t, rev).State.Terminal() {
				return false
			}
		}
		return true
	})
	require.Equal(t, int32(2), held.max.Load())
	require.Equal(t, 10, r.renderer.RenderCalls())
}
func TestWorkerLifecycleStopCancelsAndJoins(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	gate := gatedWorker(t, r, "render", 1, nil)
	r.worker.Start(context.Background())
	<-gate.entered
	r.worker.Stop()
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateInProgress, row.State)
	require.Nil(t, row.FinishedAt)
	calls := r.renderer.MeasureCalls()
	r.worker.Notify(req.Revision)
	require.ErrorIs(t, r.worker.ProcessRevision(context.Background(), req.Revision), context.Canceled)
	require.ErrorIs(t, r.worker.Drain(context.Background()), context.Canceled)
	require.ErrorIs(t, r.worker.Recover(context.Background()), context.Canceled)
	require.ErrorIs(t, r.worker.ReconcileCache(context.Background()), context.Canceled)
	require.Equal(t, calls, r.renderer.MeasureCalls())
}
func TestWorkerLifecycleStopBeforeStartAndCancelledStart(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	r.worker.Stop()
	r.worker.Stop()
	r.worker.Notify(req.Revision)
	r.worker.Start(context.Background())
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, layout.StatePending, r.row(t, req.Revision).State)
	r = newWorkerRig(t, 1)
	req = r.request(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.worker.Start(ctx)
	r.worker.Stop()
	require.Equal(t, layout.StatePending, r.row(t, req.Revision).State)
}

type observedStore struct {
	layout.Store
	calls atomic.Int64
}

func (s *observedStore) Claim(ctx context.Context, rev, owner string, now, until time.Time) (layout.Record, error) {
	s.calls.Add(1)
	return s.Store.Claim(ctx, rev, owner, now, until)
}
func (s *observedStore) Claimable(ctx context.Context, now time.Time, n int) ([]string, error) {
	s.calls.Add(1)
	return s.Store.Claimable(ctx, now, n)
}
func (s *observedStore) PendingCache(ctx context.Context, n int) ([]layout.Record, error) {
	s.calls.Add(1)
	return s.Store.PendingCache(ctx, n)
}
func (s *observedStore) Get(ctx context.Context, rev string) (layout.Record, error) {
	s.calls.Add(1)
	return s.Store.Get(ctx, rev)
}
func (s *observedStore) Result(ctx context.Context, rev string, gen int64) (layout.StoredResult, error) {
	s.calls.Add(1)
	return s.Store.Result(ctx, rev, gen)
}
func (s *observedStore) Progress(ctx context.Context, f layout.Fence, stage layout.Stage, attempt int, now, until time.Time) error {
	s.calls.Add(1)
	return s.Store.Progress(ctx, f, stage, attempt, now, until)
}
func (s *observedStore) Complete(ctx context.Context, f layout.Fence, c layout.Completion, now time.Time) error {
	s.calls.Add(1)
	return s.Store.Complete(ctx, f, c, now)
}
func (s *observedStore) Checkpoint(ctx context.Context, f layout.Fence, r layout.StoredResult, replace bool, now time.Time) error {
	s.calls.Add(1)
	return s.Store.Checkpoint(ctx, f, r, replace, now)
}
func (s *observedStore) MarkCached(ctx context.Context, f layout.Fence, gen int64, d layout.Destination) error {
	s.calls.Add(1)
	return s.Store.MarkCached(ctx, f, gen, d)
}
func TestWorkerLifecycleZeroCollaboratorCallsAfterStop(t *testing.T) {
	r := newWorkerRig(t, 1, func(c *layout.WorkerConfig) { c.PollInterval = time.Millisecond })
	req := r.request(t)
	spy := &observedStore{Store: r.store}
	cfg := r.cfg
	cfg.Store = spy
	var err error
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	r.worker.Start(context.Background())
	eventually(t, func() bool { return r.row(t, req.Revision).State.Terminal() })
	r.worker.Stop()
	storeCalls := spy.calls.Load()
	renderCalls := r.renderer.RenderCalls()
	measureCalls := r.renderer.MeasureCalls()
	manifestCalls := r.manifests.Calls()
	cacheCalls := r.cache.PublishCalls()
	r.worker.Notify(req.Revision)
	r.worker.Start(context.Background())
	r.worker.Stop()
	require.ErrorIs(t, r.worker.ProcessRevision(context.Background(), req.Revision), context.Canceled)
	require.ErrorIs(t, r.worker.ReconcileCache(context.Background()), context.Canceled)
	require.ErrorIs(t, r.worker.Recover(context.Background()), context.Canceled)
	require.ErrorIs(t, r.worker.Drain(context.Background()), context.Canceled)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, storeCalls, spy.calls.Load())
	require.Equal(t, renderCalls, r.renderer.RenderCalls())
	require.Equal(t, measureCalls, r.renderer.MeasureCalls())
	require.Equal(t, manifestCalls, r.manifests.Calls())
	require.Equal(t, cacheCalls, r.cache.PublishCalls())
}
func TestWorkerDrainCancelledJoinsJobs(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	gate := gatedWorker(t, r, "render", 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.worker.Drain(ctx) }()
	<-gate.entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, layout.StateInProgress, r.row(t, req.Revision).State)
	r.worker.Stop()
}
func TestWorkerDrainFiniteAndBounded(t *testing.T) {
	r := newWorkerRig(t, 1)
	r.renderer.RenderDelay = 5 * time.Millisecond
	var revs []string
	for range 10 {
		revs = append(revs, r.request(t).Revision)
	}
	require.NoError(t, r.worker.Drain(context.Background()))
	require.Equal(t, 2, r.renderer.MaxInFlight())
	for _, rev := range revs {
		require.Equal(t, layout.StatePassed, r.row(t, rev).State)
	}
	require.NoError(t, r.worker.Drain(context.Background()))
	require.Equal(t, 10, r.renderer.RenderCalls())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, r.worker.Drain(ctx), context.Canceled)
}
