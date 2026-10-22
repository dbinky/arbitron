package layout_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired seam 9: the terminal row the real Worker writes through the SQLite store is
// exactly what Reader.View displays. No MemStore or hand-built Completion stands in for
// either side; the cache Reader.cachedFallback reads is the one Worker.publish wrote.

// seamOutageStore fails the full store (Get) or only the result read, as SQLite would.
type seamOutageStore struct {
	layout.Store
	get, result bool
}

func (s seamOutageStore) Get(ctx context.Context, rev string) (layout.Record, error) {
	if s.get {
		return layout.Record{}, fmt.Errorf("get: %w", layout.ErrStoreUnavailable)
	}
	return s.Store.Get(ctx, rev)
}
func (s seamOutageStore) Result(ctx context.Context, rev string, gen int64) (layout.StoredResult, error) {
	if s.get || s.result {
		return layout.StoredResult{}, fmt.Errorf("result: %w", layout.ErrStoreUnavailable)
	}
	return s.Store.Result(ctx, rev, gen)
}

type seamRow struct {
	store *adaptsqlite.LayoutRepo
	rig   *workerRig
	wf    *workflow.Workflow
	rev   string
}

// seamRequest saves a shared manifest whose presentation names its cache destination.
func seamRequest(t *testing.T, store *adaptsqlite.LayoutRepo, limit int, options ...func(*layout.WorkerConfig)) seamRow {
	t.Helper()
	options = append([]func(*layout.WorkerConfig){func(c *layout.WorkerConfig) {
		c.Store = store
		c.Requester = layout.NewRequester(store, limit, nil)
	}}, options...)
	r := newWorkerRig(t, limit, options...)
	wf := layouttest.SampleWorkflow()
	rev := uuid.Must(uuid.NewV7()).String()
	wf.Presentation = &workflow.Presentation{Revision: rev, Scope: workflow.ScopeLabelShared, Name: "sample"}
	req := workflow.ArrangementRequest{Revision: rev, Scope: workflow.Shared(), Name: "sample", Manifest: layouttest.ManifestJSON(t, wf), RequestedAt: time.Now().UTC(), Reason: workflow.SaveReasonUpdate}
	require.NoError(t, layout.NewRequester(store, limit, nil).RequestArrangement(context.Background(), req))
	r.manifests.Set(req.Scope, req.Name, rev)
	return seamRow{store: store, rig: r, wf: requestWorkflow(t, req), rev: rev}
}

func TestWorkerTerminalRowReachesReaderSQLite(t *testing.T) {
	ctx := context.Background()
	drive := map[string]func(t *testing.T) seamRow{
		"passed": func(t *testing.T) seamRow {
			s := seamRequest(t, newSQLiteLayoutStore(t), 1)
			require.NoError(t, s.rig.worker.ProcessRevision(ctx, s.rev))
			return s
		},
		"needs_attention": func(t *testing.T) seamRow {
			s := seamRequest(t, newSQLiteLayoutStore(t), 1)
			s.rig.reviewer.SetScript(blockingScript())
			require.NoError(t, s.rig.worker.ProcessRevision(ctx, s.rev))
			return s
		},
		// A reviewed needs_attention best survives a retry whose renderer is down.
		"failed-with-best": func(t *testing.T) seamRow {
			s := seamRequest(t, newSQLiteLayoutStore(t), 1)
			s.rig.reviewer.SetScript(blockingScript())
			require.NoError(t, s.rig.worker.ProcessRevision(ctx, s.rev))
			_, err := s.rig.cfg.Requester.Retry(ctx, s.rev)
			require.NoError(t, err)
			s.rig.renderer.RenderErr = errors.New("renderer down")
			require.NoError(t, s.rig.worker.ProcessRevision(ctx, s.rev))
			return s
		},
		// Supersede lands while attempt 2 renders; attempt 1's unpassed best is retained.
		"superseded-unpassed": func(t *testing.T) seamRow {
			store := newSQLiteLayoutStore(t)
			s := seamRequest(t, store, 5, func(c *layout.WorkerConfig) { c.LeaseDuration = 30 * time.Millisecond })
			s.rig.reviewer.SetScript(blockingScript())
			gate := gatedWorker(t, s.rig, "render", 2, nil)
			done := make(chan error, 1)
			go func() { done <- s.rig.worker.ProcessRevision(ctx, s.rev) }()
			<-gate.entered
			require.NoError(t, store.Supersede(ctx, s.rev, time.Now()))
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("supersede did not end the job")
			}
			return s
		},
	}
	want := map[string]struct {
		state      layout.State
		display    layout.DisplayStatus
		retry      bool
		unreviewed bool
	}{
		"passed":              {layout.StatePassed, layout.DisplayComplete, false, false},
		"needs_attention":     {layout.StateNeedsAttention, layout.DisplayNeedsAttention, true, false},
		"failed-with-best":    {layout.StateFailed, layout.DisplayFailed, true, false},
		"superseded-unpassed": {layout.StateSuperseded, layout.DisplayNeedsAttention, true, false},
	}
	for name, run := range drive {
		w := want[name]
		t.Run(name, func(t *testing.T) {
			s := run(t)
			row, err := s.store.Get(ctx, s.rev)
			require.NoError(t, err)
			require.Equal(t, w.state, row.State)
			require.Positive(t, row.ResultGeneration, "the worker must retain a best result")
			best, err := s.store.Result(ctx, s.rev, row.ResultGeneration)
			require.NoError(t, err)
			if name != "passed" {
				require.NotEqual(t, layout.VerdictPassed, row.Verdict)
			}

			reader := layout.NewReader(readOnlyStore{s.store, t}, s.rig.journal, s.rig.cache)
			v := reader.View(ctx, s.wf)
			assert.Equal(t, w.display, v.DisplayStatus)
			assert.Equal(t, w.state, v.State)
			assert.True(t, v.Available)
			assert.Equal(t, w.retry, v.RetryEligible)
			assert.Equal(t, w.unreviewed, v.Unreviewed)
			assert.Equal(t, layout.ProvenanceArranged, v.Provenance)
			require.NotNil(t, v.Geometry, "a terminal row with a retained best must display its geometry")
			assert.Equal(t, best.Geometry, *v.Geometry)
			assert.Equal(t, row.JobGeneration, v.JobGeneration)
			assert.Equal(t, row.ResultGeneration, v.ResultGeneration)
			if name == "passed" {
				assert.Empty(t, v.Findings)
			} else {
				assert.NotEmpty(t, v.Findings, "unpassed rows must surface their findings")
			}
			if name == "failed-with-best" {
				assert.Equal(t, layout.StageRendering, v.FailureStage)
				assert.Equal(t, "rendering failed", v.FailureReason)
			}
			// An independently decoded frozen instance snapshot reads the same value.
			assert.Equal(t, v, reader.View(ctx, requestWorkflow(t, workflow.ArrangementRequest{Manifest: row.Manifest})))

			// The cache was written by Worker.publish for this revision and fence.
			// Superseded is terminal, so its retained unpassed best is published too.
			published := s.rig.cache.Published(row.Destination)
			require.NotEmpty(t, published)
			last := published[len(published)-1]
			require.Equal(t, s.rev, last.Revision)
			require.Equal(t, row.ResultGeneration, last.Generation)
			require.Equal(t, row.JobGeneration, last.JobGeneration)
			for _, outage := range []struct {
				name        string
				get, result bool
			}{{"full", true, false}, {"result-only", false, true}} {
				t.Run(outage.name, func(t *testing.T) {
					ov := layout.NewReader(readOnlyStore{seamOutageStore{Store: s.store, get: outage.get, result: outage.result}, t}, s.rig.journal, s.rig.cache).View(ctx, s.wf)
					assert.Equal(t, layout.DisplayUnavailable, ov.DisplayStatus, "outage never restores a terminal status")
					assert.False(t, ov.Available)
					assert.True(t, ov.Unreviewed, "cached bytes never restore reviewed status")
					assert.Nil(t, ov.Evidence)
					assert.True(t, ov.RetryEligible)
					assert.NotEmpty(t, ov.AvailabilityError)
					require.NotNil(t, ov.Geometry)
					assert.Equal(t, best.Geometry, *ov.Geometry)
					assert.Equal(t, row.ResultGeneration, ov.ResultGeneration)
				})
			}
		})
	}
}

// A readable row's fences outrank the cache during a result-only outage: after a retry
// installs a newer best, the gen-1 cache bytes Worker.publish wrote are not displayed.
func TestReaderResultOutageRejectsWorkerCacheFromOlderGenerationSQLite(t *testing.T) {
	ctx := context.Background()
	s := seamRequest(t, newSQLiteLayoutStore(t), 1)
	s.rig.reviewer.SetScript(blockingScript())
	require.NoError(t, s.rig.worker.ProcessRevision(ctx, s.rev))
	first, err := s.store.Get(ctx, s.rev)
	require.NoError(t, err)
	require.Len(t, s.rig.cache.Published(first.Destination), 1)

	_, err = s.rig.cfg.Requester.Retry(ctx, s.rev)
	require.NoError(t, err)
	s.rig.reviewer.SetScript(layouttest.ReviewScript{})
	s.rig.cache.SetPublishErr(errors.New("disk full"))
	require.ErrorContains(t, s.rig.worker.ProcessRevision(ctx, s.rev), "disk full")
	row, err := s.store.Get(ctx, s.rev)
	require.NoError(t, err)
	require.Equal(t, layout.StatePassed, row.State)
	require.Greater(t, row.ResultGeneration, first.ResultGeneration)

	v := layout.NewReader(readOnlyStore{seamOutageStore{Store: s.store, result: true}, t}, s.rig.journal, s.rig.cache).View(ctx, s.wf)
	assert.Equal(t, layout.DisplayUnavailable, v.DisplayStatus)
	assert.True(t, v.Unreviewed)
	assert.Nil(t, v.Geometry, "gen-1 cache must not be drawn for the gen-2 row")
	assert.Equal(t, row.ResultGeneration, v.ResultGeneration)
}
