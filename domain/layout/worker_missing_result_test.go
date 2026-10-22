package layout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

// Lose only the referenced immutable result, not the captured input or any new
// generation. All other reads and writes still use the real Store contract.
type missingPriorResultStore struct {
	layout.Store
	revision   string
	generation int64
	err        error
}

func (s missingPriorResultStore) Result(ctx context.Context, rev string, gen int64) (layout.StoredResult, error) {
	if rev == s.revision && gen == s.generation {
		return layout.StoredResult{}, s.err
	}
	return s.Store.Result(ctx, rev, gen)
}

func TestWorker_RetryReplacesMissingPriorResultButNotStoreOutage(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "outage"}[missing], func(t *testing.T) {
			ctx := context.Background()
			r := newWorkerRig(t, 1)
			req := r.request(t)
			require.NoError(t, r.worker.ProcessRevision(ctx, req.Revision))
			before := r.row(t, req.Revision)
			require.Equal(t, layout.StatePassed, before.State)
			outage := errors.New("result storage unavailable")
			lost := missingPriorResultStore{Store: r.store, revision: req.Revision, generation: before.ResultGeneration, err: outage}
			if missing {
				lost.err = layout.ErrRecordNotFound
			}
			require.NoError(t, r.store.InvalidateResult(ctx, layout.Fence{Revision: req.Revision, Generation: before.JobGeneration}, before.ResultGeneration, "lost result", time.Now()))
			requester := layout.NewRequester(lost, 1, nil)
			ack, err := requester.Retry(ctx, req.Revision)
			require.NoError(t, err)
			cfg := r.cfg
			cfg.Store, cfg.Requester = lost, requester
			worker, err := layout.NewWorker(cfg)
			require.NoError(t, err)
			err = worker.ProcessRevision(ctx, req.Revision)
			if !missing {
				require.ErrorIs(t, err, outage)
				require.Equal(t, before.ResultGeneration, r.row(t, req.Revision).ResultGeneration)
				return
			}
			require.NoError(t, err)
			after := r.row(t, req.Revision)
			require.Equal(t, layout.StatePassed, after.State)
			require.Equal(t, ack.JobGeneration, after.ResultGeneration)
			result, err := r.store.Result(ctx, req.Revision, after.ResultGeneration)
			require.NoError(t, err)
			require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
			require.Equal(t, req.Revision, result.Revision)
			require.NotEmpty(t, r.cache.Published(after.Destination))
		})
	}
}

// Serve the referenced prior result with a blocking finding beside its passed
// verdict. Its rendered evidence still matches, so it remains "reviewed".
type rejectedPriorResultStore struct {
	layout.Store
	revision   string
	generation int64
}

func (s rejectedPriorResultStore) Result(ctx context.Context, rev string, gen int64) (layout.StoredResult, error) {
	result, err := s.Store.Result(ctx, rev, gen)
	if err == nil && rev == s.revision && gen == s.generation {
		result.Preflight = append(append([]layout.Finding(nil), result.Preflight...), layout.Finding{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourcePreflight, Evidence: "stored with a blocking finding"})
	}
	return result, err
}

// Retry keeps the prior best only while it is a valid best. A prior pass that
// Retry invalidated must not out-rank an equally scored, valid new pass.
func TestWorker_RetryReplacesInvalidatedPriorPass(t *testing.T) {
	ctx := context.Background()
	r := newWorkerRig(t, 1)
	req := r.request(t)
	require.NoError(t, r.worker.ProcessRevision(ctx, req.Revision))
	before := r.row(t, req.Revision)
	require.Equal(t, layout.StatePassed, before.State)
	rejected := rejectedPriorResultStore{Store: r.store, revision: req.Revision, generation: before.ResultGeneration}
	requester := layout.NewRequester(rejected, 1, nil)
	ack, err := requester.Retry(ctx, req.Revision)
	require.NoError(t, err, "an incompatible stored pass is retry-eligible")
	cfg := r.cfg
	cfg.Store, cfg.Requester = rejected, requester
	worker, err := layout.NewWorker(cfg)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessRevision(ctx, req.Revision))
	after := r.row(t, req.Revision)
	require.Equal(t, layout.StatePassed, after.State)
	require.Equal(t, ack.JobGeneration, after.ResultGeneration, "the valid retry result replaces the rejected prior")
	require.Equal(t, after.ResultGeneration, after.CacheGeneration)
	require.NotEmpty(t, r.cache.Published(after.Destination))
}
