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

func blockingScript() layouttest.ReviewScript {
	return layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: "overlap"}}}
}
func TestWorkerCacheOutageAndRetainedBestRetry(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	r.reviewer.SetScript(blockingScript())
	r.cache.SetPublishErr(errors.New("disk full"))
	require.ErrorContains(t, r.worker.ProcessRevision(context.Background(), req.Revision), "disk full")
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateNeedsAttention, row.State)
	require.Equal(t, int64(1), row.ResultGeneration)
	require.Zero(t, row.CacheGeneration)
	r.cache.SetPublishErr(nil)
	require.NoError(t, r.worker.ReconcileCache(context.Background()))
	published := r.cache.Published(row.Destination)
	require.Len(t, published, 1)
	require.Equal(t, int64(1), r.row(t, req.Revision).CacheGeneration)
	_, err := r.store.Retry(context.Background(), req.Revision, 1, time.Now())
	require.NoError(t, err)
	r.renderer.RenderErr = errors.New("retry renderer down")
	require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
	row = r.row(t, req.Revision)
	require.Equal(t, layout.StateFailed, row.State)
	require.Equal(t, int64(1), row.ResultGeneration)
	published = r.cache.Published(row.Destination)
	require.Len(t, published, 2)
	require.Equal(t, int64(1), published[1].Generation)
	require.Equal(t, int64(2), published[1].JobGeneration)
	require.Equal(t, published[0].Geometry, published[1].Geometry)
}
func TestWorkerCacheLateRevisionAndMissingDestination(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		r := newWorkerRig(t, 1)
		req := r.request(t)
		if deleted {
			r.manifests.Remove(req.Scope, req.Name)
		} else {
			r.manifests.Set(req.Scope, req.Name, uuid.Must(uuid.NewV7()).String())
		}
		require.NoError(t, r.worker.ProcessRevision(context.Background(), req.Revision))
		row := r.row(t, req.Revision)
		require.Empty(t, r.cache.Published(row.Destination))
		require.Equal(t, row.ResultGeneration, row.CacheGeneration)
	}
}

type hookedCache struct {
	layout.CachePublisher
	before func()
	after  func()
}

func (c *hookedCache) PublishCache(ctx context.Context, d layout.Destination, e layout.CacheEntry, f func(context.Context) error) error {
	if c.before != nil {
		c.before()
	}
	err := c.CachePublisher.PublishCache(ctx, d, e, f)
	if c.after != nil {
		c.after()
	}
	return err
}
func TestWorkerCacheGenerationAndSettlementRaces(t *testing.T) {
	for _, when := range []string{"before fence", "after publish"} {
		t.Run(when, func(t *testing.T) {
			r := newWorkerRig(t, 1)
			req := r.request(t)
			r.reviewer.SetScript(blockingScript())
			retry := func() {
				_, err := r.store.Retry(context.Background(), req.Revision, 1, time.Now())
				require.NoError(t, err)
			}
			hook := &hookedCache{CachePublisher: r.cache}
			if when == "before fence" {
				hook.before = retry
			} else {
				hook.after = retry
			}
			cfg := r.cfg
			cfg.Cache = hook
			var err error
			r.worker, err = layout.NewWorker(cfg)
			require.NoError(t, err)
			require.ErrorIs(t, r.worker.ProcessRevision(context.Background(), req.Revision), layout.ErrStaleGeneration)
			row := r.row(t, req.Revision)
			require.Equal(t, int64(2), row.JobGeneration)
			require.Equal(t, int64(1), row.ResultGeneration)
			require.Zero(t, row.CacheGeneration)
			if when == "before fence" {
				require.Empty(t, r.cache.Published(row.Destination))
			}
			require.NoError(t, r.worker.ReconcileCache(context.Background()))
			require.Zero(t, r.row(t, req.Revision).CacheGeneration, "unfinished retry must not settle old best")
		})
	}
}
func TestWorkerCacheLateGenerationWithRetainedResult(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	r.reviewer.SetScript(blockingScript())
	hook := &hookedCache{CachePublisher: r.cache, before: func() {
		_, err := r.store.Retry(context.Background(), req.Revision, 1, time.Now())
		require.NoError(t, err)
		row, err := r.store.Claim(context.Background(), req.Revision, "other-worker", time.Now(), time.Now().Add(time.Minute))
		require.NoError(t, err)
		require.NoError(t, r.store.Complete(context.Background(), layout.Fence{Revision: req.Revision, Generation: row.JobGeneration}, layout.Completion{State: layout.StateFailed}, time.Now()))
	}}
	cfg := r.cfg
	cfg.Cache = hook
	var err error
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	require.ErrorIs(t, r.worker.ProcessRevision(context.Background(), req.Revision), layout.ErrStaleGeneration)
	row := r.row(t, req.Revision)
	require.Equal(t, int64(2), row.JobGeneration)
	require.Equal(t, int64(1), row.ResultGeneration)
	require.Empty(t, r.cache.Published(row.Destination))
	require.Zero(t, row.CacheGeneration)
}
func TestWorkerCacheOwnershipMove(t *testing.T) {
	for _, during := range []string{"render", "publication", "settlement"} {
		t.Run(during, func(t *testing.T) {
			r := newWorkerRig(t, 1)
			req := r.request(t)
			old := r.row(t, req.Revision).Destination
			newDest := layout.Destination{Scope: workflow.Scope{Kind: workflow.ScopePrivate, OwnerID: uuid.New()}, Name: req.Name}
			move := func() {
				_, err := r.store.Relocate(context.Background(), old, newDest, time.Now())
				require.NoError(t, err)
				r.manifests.Remove(old.Scope, old.Name)
				r.manifests.Set(newDest.Scope, newDest.Name, req.Revision)
			}
			if during == "render" {
				r.renderer.OnRender = func(layout.RenderRequest) { move() }
			} else {
				hook := &hookedCache{CachePublisher: r.cache}
				if during == "publication" {
					hook.before = move
				} else {
					hook.after = move
				}
				cfg := r.cfg
				cfg.Cache = hook
				var err error
				r.worker, err = layout.NewWorker(cfg)
				require.NoError(t, err)
			}
			err := r.worker.ProcessRevision(context.Background(), req.Revision)
			if during == "render" {
				require.NoError(t, err)
				require.Empty(t, r.cache.Published(old))
				require.Len(t, r.cache.Published(newDest), 1)
			} else {
				require.ErrorIs(t, err, layout.ErrStaleGeneration)
				require.Zero(t, r.row(t, req.Revision).CacheGeneration)
				r.worker, err = layout.NewWorker(r.cfg)
				require.NoError(t, err)
				require.NoError(t, r.worker.ReconcileCache(context.Background()))
				require.Len(t, r.cache.Published(newDest), 1)
			}
		})
	}
}

type failingDestinationCache struct {
	layout.CachePublisher
	bad string
}

func (c failingDestinationCache) PublishCache(ctx context.Context, d layout.Destination, e layout.CacheEntry, f func(context.Context) error) error {
	if d.Name == c.bad {
		return errors.New("permanent destination outage")
	}
	return c.CachePublisher.PublishCache(ctx, d, e, f)
}
func TestWorkerCacheBadDestinationDoesNotStarveOthers(t *testing.T) {
	r := newWorkerRig(t, 1)
	cfg := r.cfg
	cfg.Cache = failingDestinationCache{r.cache, "broken"}
	var err error
	r.worker, err = layout.NewWorker(cfg)
	require.NoError(t, err)
	bad := r.input(t)
	bad.Name = "broken"
	require.NoError(t, r.requester.RequestArrangement(context.Background(), bad))
	r.manifests.Set(bad.Scope, bad.Name, bad.Revision)
	require.ErrorContains(t, r.worker.ProcessRevision(context.Background(), bad.Revision), "permanent destination outage")
	good := r.request(t)
	r.cache.SetPublishErr(errors.New("temporary outage"))
	require.Error(t, r.worker.ProcessRevision(context.Background(), good.Revision))
	r.cache.SetPublishErr(nil)
	require.ErrorContains(t, r.worker.ReconcileCache(context.Background()), "permanent destination outage")
	require.Zero(t, r.row(t, bad.Revision).CacheGeneration)
	row := r.row(t, good.Revision)
	require.Equal(t, row.ResultGeneration, row.CacheGeneration)
	require.Len(t, r.cache.Published(row.Destination), 1)
}
func TestWorkerCacheReconciliationContinues(t *testing.T) {
	r := newWorkerRig(t, 1)
	r.cache.SetPublishErr(errors.New("offline"))
	var revs []string
	for range 3 {
		req := r.request(t)
		revs = append(revs, req.Revision)
		require.Error(t, r.worker.ProcessRevision(context.Background(), req.Revision))
	}
	r.cache.SetPublishErr(nil)
	require.NoError(t, r.worker.ReconcileCache(context.Background()))
	for _, rev := range revs {
		row := r.row(t, rev)
		require.Equal(t, row.ResultGeneration, row.CacheGeneration)
	}
	require.Len(t, r.cache.Published(r.row(t, revs[2]).Destination), 1)
}
