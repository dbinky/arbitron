package layout_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Superseded is a reserved lifecycle state with no production producer, so every
// consumer branch is exercised here against the real SQLite store rather than MemStore.
func newSQLiteLayoutStore(t *testing.T) *adaptsqlite.LayoutRepo {
	t.Helper()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "layout.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	return adaptsqlite.NewLayoutRepo(db)
}

// supersededRow persists a claimed revision, optionally completes it with a passed best, then supersedes it.
func supersededRow(t *testing.T, store *adaptsqlite.LayoutRepo, withPass, validPass bool) (req *workflow.ArrangementRequest, best layout.StoredResult) {
	t.Helper()
	ctx := context.Background()
	req = savedRequest(t)
	require.NoError(t, layout.NewRequester(store, 5, nil).RequestArrangement(ctx, *req))
	row, err := store.Claim(ctx, req.Revision, "worker", time.Now(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	geo := layouttest.Arranged(t, requestWorkflow(t, *req))
	best = layout.StoredResult{Revision: req.Revision, Generation: 1, Geometry: geo, Review: unitReview(geo), Verdict: layout.VerdictPassed, CreatedAt: time.Now()}
	if !validPass {
		best.Review.Evidence.Artifacts = nil
	}
	f := layout.Fence{Revision: req.Revision, Generation: row.JobGeneration}
	if withPass {
		require.NoError(t, store.Checkpoint(ctx, f, best, true, time.Now()))
	}
	require.NoError(t, store.Supersede(ctx, req.Revision, time.Now()))
	if withPass {
		require.NoError(t, store.Complete(ctx, f, layout.Completion{State: layout.StatePassed, ReplaceBest: false}, time.Now()))
	}
	return req, best
}

func TestWorkerSupersedeSQLiteKeepsBestEvidence(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteLayoutStore(t)
	r := newWorkerRig(t, 5, func(c *layout.WorkerConfig) {
		c.Store = store
		c.Requester = layout.NewRequester(store, 5, nil)
		c.LeaseDuration = 30 * time.Millisecond
	})
	req := r.input(t)
	require.NoError(t, layout.NewRequester(store, 5, nil).RequestArrangement(ctx, req))
	r.manifests.Set(req.Scope, req.Name, req.Revision)
	r.reviewer.SetScript(blockingScript())
	gate := gatedWorker(t, r, "render", 2, nil)
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessRevision(ctx, req.Revision) }()
	<-gate.entered
	require.NoError(t, store.Supersede(ctx, req.Revision, time.Now()))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("supersede did not cancel the renderer against the SQLite store")
	}
	row, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	require.Equal(t, layout.StateSuperseded, row.State)
	require.Positive(t, row.ResultGeneration, "attempt 1's best must be retained")
	best, err := store.Result(ctx, req.Revision, row.ResultGeneration)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(best.Geometry, best.Review))
	require.Equal(t, "overlap", best.Review.Findings[0].Evidence)
}

func TestReaderSupersededSQLite(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name            string
		withPass, valid bool
		retryEligible   bool
		available       bool
	}{
		{"valid-pass", true, true, false, true},
		{"invalid-pass", true, false, true, false},
		{"no-pass", false, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newSQLiteLayoutStore(t)
			req, best := supersededRow(t, store, tc.withPass, tc.valid)
			row, err := store.Get(ctx, req.Revision)
			require.NoError(t, err)
			require.Equal(t, layout.StateSuperseded, row.State)
			v := layout.NewReader(store, nil, nil).View(ctx, requestWorkflow(t, *req))
			assert.Equal(t, tc.retryEligible, v.RetryEligible)
			assert.Equal(t, tc.available, v.Available)
			if tc.available {
				assert.Equal(t, layout.DisplayComplete, v.DisplayStatus)
				require.NotNil(t, v.Geometry)
				assert.Equal(t, best.Geometry, *v.Geometry)
			} else {
				assert.Equal(t, layout.DisplayNeedsAttention, v.DisplayStatus)
				assert.NotEmpty(t, v.AvailabilityError)
			}
		})
	}
}

func TestRequesterRetrySupersededSQLite(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name            string
		withPass, valid bool
	}{{"valid-pass", true, true}, {"invalid-pass", true, false}, {"no-pass", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSQLiteLayoutStore(t)
			req, _ := supersededRow(t, store, tc.withPass, tc.valid)
			rev := req.Revision
			before, err := store.Get(ctx, rev)
			require.NoError(t, err)
			notified := ""
			r := layout.NewRequester(store, 4, nil)
			r.SetNotifier(func(rev string) { notified = rev })
			retried, err := r.Retry(ctx, rev)
			if tc.valid {
				assert.ErrorIs(t, err, layout.ErrRetryConflict)
				assert.Empty(t, notified)
				after, gerr := store.Get(ctx, rev)
				require.NoError(t, gerr)
				assert.Equal(t, before.JobGeneration, after.JobGeneration)
				assert.Equal(t, layout.StateSuperseded, after.State)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, before.JobGeneration+1, retried.JobGeneration)
			assert.Equal(t, 4, retried.AttemptLimit)
			assert.Equal(t, layout.StatePending, retried.State)
			assert.Equal(t, rev, notified)
			after, err := store.Get(ctx, rev)
			require.NoError(t, err)
			assert.Equal(t, retried.JobGeneration, after.JobGeneration)
		})
	}
}
