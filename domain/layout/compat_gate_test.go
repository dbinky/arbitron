package layout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

// The retry gate in passProblem must stand on its own: a result whose evidence
// digest was recomputed for incompatible geometry still reads as a bad pass.
func TestRetry_PassWithIncompatibleGeometryAndConsistentDigestIsInvalidated(t *testing.T) {
	ctx := context.Background()
	store, wf, best := capturedGroupRow(t, "nested")
	best.Geometry.Format = layout.FormatV1
	best.Review.Evidence.GeometryDigest = layout.GeometryDigest(best.Geometry)
	require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review), "evidence must match so only Compatible can reject")
	port := corruptResultStore{Store: store, result: best}
	ack, err := layout.NewRequester(port, 5, nil).Retry(ctx, wf.SavedRevision())
	require.NoError(t, err, "an incompatible pass must be retryable, not a conflict")
	require.Equal(t, int64(2), ack.JobGeneration)
	require.Equal(t, layout.StatePending, ack.State)
}

// A needs-attention result never reaches passProblem, so the cache-publish gate
// is the only thing keeping incompatible geometry out of the published cache.
func TestWorkerCache_IncompatibleStoredResultIsNeverPublished(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	r.reviewer.SetScript(blockingScript())
	r.cache.SetPublishErr(errors.New("disk full"))
	require.ErrorContains(t, r.worker.ProcessRevision(context.Background(), req.Revision), "disk full")
	row := r.row(t, req.Revision)
	require.Equal(t, layout.StateNeedsAttention, row.State)
	stored, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
	require.NoError(t, err)
	stored.Geometry.Format = layout.FormatV1

	r.cache.SetPublishErr(nil)
	cfg := r.cfg
	cfg.Store = corruptResultStore{Store: r.store, result: stored}
	w, err := layout.NewWorker(cfg)
	require.NoError(t, err)
	require.ErrorContains(t, w.ReconcileCache(context.Background()), "incompatible cache result")
	require.Empty(t, r.cache.Published(row.Destination))
	require.Zero(t, r.row(t, req.Revision).CacheGeneration)
}

// A result from a previous renderer contract is never published to the cache,
// but it is not an error either: it settles once, quietly, instead of failing
// every reconcile pass until someone views and re-arranges the revision.
func TestWorkerCache_PreviousContractResultSettlesWithoutPublishing(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.request(t)
	r.reviewer.SetScript(blockingScript())
	r.cache.SetPublishErr(errors.New("disk full"))
	require.ErrorContains(t, r.worker.ProcessRevision(context.Background(), req.Revision), "disk full")
	row := r.row(t, req.Revision)
	stored, err := r.store.Result(context.Background(), req.Revision, row.ResultGeneration)
	require.NoError(t, err)
	stored.Geometry.Contract = layout.PreviousContracts()[0]

	r.cache.SetPublishErr(nil)
	cfg := r.cfg
	cfg.Store = corruptResultStore{Store: r.store, result: stored}
	w, err := layout.NewWorker(cfg)
	require.NoError(t, err)
	require.NoError(t, w.ReconcileCache(context.Background()))
	require.Empty(t, r.cache.Published(row.Destination))
	require.Equal(t, row.ResultGeneration, r.row(t, req.Revision).CacheGeneration, "settled, so later passes skip it")
	require.NoError(t, w.ReconcileCache(context.Background()))
}
