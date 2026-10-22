package layout_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requestWorkflow(t testing.TB, req workflow.ArrangementRequest) *workflow.Workflow {
	t.Helper()
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(req.Manifest, &wf))
	return &wf
}
func readerRow(t *testing.T) (*layouttest.MemStore, *workflow.ArrangementRequest, layout.Fence, layout.StoredResult) {
	t.Helper()
	ctx := context.Background()
	m := layouttest.NewMemStore()
	req := savedRequest(t)
	require.NoError(t, layout.NewRequester(m, 5, nil).RequestArrangement(ctx, *req))
	r, err := m.Claim(ctx, req.Revision, "worker", time.Now(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	geo := layouttest.Arranged(t, requestWorkflow(t, *req))
	return m, req, layout.Fence{Revision: req.Revision, Generation: r.JobGeneration}, layout.StoredResult{Revision: req.Revision, Generation: 1, Geometry: geo, Review: unitReview(geo), Verdict: layout.VerdictPassed, CreatedAt: time.Now()}
}

// Any accidental write from Reader is a test failure, including a "helpful" retry.
type readOnlyStore struct {
	layout.Store
	t *testing.T
}

func (s readOnlyStore) Request(context.Context, layout.NewRequest) (layout.Record, bool, error) {
	s.t.Fatal("Reader wrote Request")
	return layout.Record{}, false, nil
}
func (s readOnlyStore) Claim(context.Context, string, string, time.Time, time.Time) (layout.Record, error) {
	s.t.Fatal("Reader wrote Claim")
	return layout.Record{}, nil
}
func (s readOnlyStore) Progress(context.Context, layout.Fence, layout.Stage, int, time.Time, time.Time) error {
	s.t.Fatal("Reader wrote Progress")
	return nil
}
func (s readOnlyStore) Complete(context.Context, layout.Fence, layout.Completion, time.Time) error {
	s.t.Fatal("Reader wrote Complete")
	return nil
}
func (s readOnlyStore) Checkpoint(context.Context, layout.Fence, layout.StoredResult, bool, time.Time) error {
	s.t.Fatal("Reader wrote Checkpoint")
	return nil
}
func (s readOnlyStore) Supersede(context.Context, string, time.Time) error {
	s.t.Fatal("Reader wrote Supersede")
	return nil
}
func (s readOnlyStore) Retry(context.Context, string, int, time.Time) (layout.Record, error) {
	s.t.Fatal("Reader wrote Retry")
	return layout.Record{}, nil
}
func (s readOnlyStore) Relocate(context.Context, layout.Destination, layout.Destination, time.Time) (int64, error) {
	s.t.Fatal("Reader wrote Relocate")
	return 0, nil
}
func (s readOnlyStore) MarkCached(context.Context, layout.Fence, int64, layout.Destination) error {
	s.t.Fatal("Reader wrote MarkCached")
	return nil
}
func (s readOnlyStore) InvalidateResult(context.Context, layout.Fence, int64, string, time.Time) error {
	s.t.Fatal("Reader wrote InvalidateResult")
	return nil
}
func TestReaderLegacyAndMissing(t *testing.T) {
	ctx := context.Background()
	m := layouttest.NewMemStore()
	reader := layout.NewReader(readOnlyStore{Store: m, t: t}, nil, nil)
	for _, wf := range []*workflow.Workflow{nil, layouttest.SampleWorkflow()} {
		v := reader.View(ctx, wf)
		assert.Equal(t, layout.DisplayLegacy, v.DisplayStatus)
		assert.Equal(t, layout.ProvenanceLegacy, v.Provenance)
		assert.True(t, v.Unreviewed)
		assert.False(t, v.RetryEligible)
	}
	req := savedRequest(t)
	wf := requestWorkflow(t, *req)
	v := reader.View(ctx, wf)
	assert.Equal(t, req.Revision, v.SavedRevision)
	assert.Equal(t, layout.DisplayPending, v.DisplayStatus)
	assert.Equal(t, layout.StageQueued, v.Stage)
	assert.True(t, v.RetryEligible)
	assert.Contains(t, v.AvailabilityError, "awaiting recovery")
	require.NotNil(t, v.Retained)
	assert.Equal(t, req.Retained.Positions, v.Retained.Positions)
	j := layouttest.NewFakeJournal(*req)
	reader = layout.NewReader(readOnlyStore{Store: m, t: t}, j, nil)
	v = reader.View(ctx, wf)
	assert.Equal(t, layout.DisplayPending, v.DisplayStatus)
	assert.Empty(t, v.AvailabilityError)
	j.SetErr(errors.New("disk"))
	v = reader.View(ctx, wf)
	assert.False(t, v.Available)
	assert.Equal(t, "revision journal unavailable", v.AvailabilityError)
	assert.NotContains(t, v.FailureReason, "disk", "storage error text is logged, never served")
	assert.NotEqual(t, layout.DisplayLegacy, v.DisplayStatus)
	for _, status := range []layout.DisplayStatus{layout.DisplayPending, layout.DisplayArranging, layout.DisplayRendering, layout.DisplayReviewing} {
		assert.True(t, layout.IsActive(status))
	}
	for _, status := range []layout.DisplayStatus{layout.DisplayComplete, layout.DisplayFailed, layout.DisplayLegacy, layout.DisplayUnavailable, layout.DisplayNeedsAttention} {
		assert.False(t, layout.IsActive(status))
	}
}
func TestReaderStagesAndTerminalTruth(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []layout.Stage{layout.StageQueued, layout.StageArranging, layout.StageRendering, layout.StageReviewing, layout.StagePublishing} {
		t.Run(string(stage), func(t *testing.T) {
			m, req, f, _ := readerRow(t)
			require.NoError(t, m.Progress(ctx, f, stage, 2, time.Now(), time.Now().Add(time.Second)))
			v := layout.NewReader(readOnlyStore{m, t}, nil, nil).View(ctx, requestWorkflow(t, *req))
			want := layout.DisplayArranging
			if stage == layout.StageRendering {
				want = layout.DisplayRendering
			}
			if stage == layout.StageReviewing || stage == layout.StagePublishing {
				want = layout.DisplayReviewing
			}
			assert.Equal(t, want, v.DisplayStatus)
			assert.Equal(t, 2, v.AttemptsUsed)
			assert.Equal(t, 5, v.AttemptLimit)
			assert.Equal(t, req.Revision, v.SavedRevision)
		})
	}
	for _, state := range []layout.State{layout.StatePassed, layout.StateNeedsAttention, layout.StateFailed, layout.StateSuperseded} {
		t.Run(string(state), func(t *testing.T) {
			m, req, f, best := readerRow(t)
			if state == layout.StateSuperseded {
				require.NoError(t, m.Supersede(ctx, req.Revision, time.Now()))
			}
			require.NoError(t, m.Complete(ctx, f, layout.Completion{State: state, Result: &best, ReplaceBest: true, FailureStage: layout.StageReviewing, FailureReason: "reason"}, time.Now()))
			reader := layout.NewReader(readOnlyStore{m, t}, nil, nil)
			wf := requestWorkflow(t, *req)
			v := reader.View(ctx, wf)
			want := layout.DisplayComplete
			if state == layout.StateFailed {
				want = layout.DisplayFailed
			}
			if state == layout.StateNeedsAttention {
				want = layout.DisplayNeedsAttention
			}
			assert.Equal(t, want, v.DisplayStatus)
			require.NotNil(t, v.Geometry)
			assert.Equal(t, best.Geometry, *v.Geometry)
			assert.Equal(t, layout.ProvenanceArranged, v.Provenance)
			assert.False(t, v.Unreviewed)
			assert.True(t, v.Available)
			assert.Equal(t, state != layout.StatePassed && state != layout.StateSuperseded, v.RetryEligible)
			// An independently decoded frozen instance snapshot has exactly the same sink value.
			snapshot := requestWorkflow(t, *req)
			assert.Equal(t, v, reader.View(ctx, snapshot))
			v.Geometry.Nodes[0].Text = "mutated"
			again := reader.View(ctx, snapshot)
			assert.Equal(t, best.Geometry, *again.Geometry)
		})
	}
}

// Upgrading the renderer must not bless an ordinary historical v1 pass or
// arrange it merely because somebody opened the workflow or frozen snapshot.
func TestReader_OrdinaryV1HistoryRemainsReadOnlyFallback(t *testing.T) {
	ctx := context.Background()
	m, req, fence, best := readerRow(t)
	best.Geometry.Format = layout.FormatV1
	best.Review = unitReview(best.Geometry) // matching evidence cannot bless v1
	best.CreatedAt = best.CreatedAt.UTC()
	require.NoError(t, m.Complete(ctx, fence, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, time.Now()))
	before, err := m.Get(ctx, req.Revision)
	require.NoError(t, err)
	reader := layout.NewReader(readOnlyStore{m, t}, nil, nil)
	for range 2 {
		wf := requestWorkflow(t, *req)
		view := reader.View(ctx, wf)
		require.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
		require.False(t, view.Available)
		require.Equal(t, "stored geometry is incompatible with this revision", view.FailureReason)
		require.True(t, view.RetryEligible)
		require.True(t, view.Unreviewed)
		require.Equal(t, layout.ProvenanceRetainedPositions, view.Provenance)
		require.NotNil(t, view.Retained)
		require.Equal(t, req.Retained, *view.Retained)
		require.Nil(t, view.Geometry)
		require.Nil(t, view.Contract)
		require.Nil(t, view.Evidence)
		require.Nil(t, view.Groups, "this is ordinary pre-upgrade history")
		require.Equal(t, int64(1), view.JobGeneration)
		require.Equal(t, int64(1), view.ResultGeneration)
		require.JSONEq(t, string(req.Manifest), string(layouttest.ManifestJSON(t, wf)))
	}
	after, err := m.Get(ctx, req.Revision)
	require.NoError(t, err)
	require.Equal(t, before, after)
	stored, err := m.Result(ctx, req.Revision, 1)
	require.NoError(t, err)
	require.Equal(t, best, stored)
}

type corruptResultStore struct {
	layout.Store
	result layout.StoredResult
	err    error
}

func (s corruptResultStore) Result(context.Context, string, int64) (layout.StoredResult, error) {
	return s.result, s.err
}
func TestReaderRejectsUnsupportedPass(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"missing", "corrupt", "wrong-revision", "wrong-generation", "contract", "text", "parallel-edge", "empty-evidence", "forged-digest", "blocking-review", "blocking-preflight", "verdict", "no-pointer", "superseded-no-result"} {
		t.Run(kind, func(t *testing.T) {
			m, req, f, best := readerRow(t)
			c := layout.Completion{State: layout.StatePassed, ReplaceBest: true, Result: &best}
			if kind == "no-pointer" || kind == "superseded-no-result" {
				c.Result = nil
			}
			if kind == "superseded-no-result" {
				require.NoError(t, m.Supersede(ctx, req.Revision, time.Now()))
			}
			require.NoError(t, m.Complete(ctx, f, c, time.Now()))
			bad := best
			err := error(nil)
			switch kind {
			case "missing":
				err = layout.ErrRecordNotFound
			case "corrupt":
				err = errors.New("corrupt stored JSON")
			case "wrong-revision":
				bad.Revision = "other"
			case "wrong-generation":
				bad.Generation = 99
			case "contract":
				bad.Geometry.Contract.FontPackage = "other"
			case "text":
				bad.Geometry.Nodes[0].Text = "wrong"
			case "parallel-edge":
				bad.Geometry.Edges[0].Ordinal = 1
			case "empty-evidence":
				bad.Review = unitReview(bad.Geometry)
				bad.Review.Evidence.Artifacts = nil
			case "forged-digest":
				bad.Review = unitReview(bad.Geometry)
				bad.Review.Evidence.GeometryDigest = "forged"
			case "blocking-review":
				bad.Review = unitReview(bad.Geometry)
				bad.Review.Findings = []layout.Finding{{Severity: layout.SeverityBlocking}}
			case "blocking-preflight":
				bad.Preflight = []layout.Finding{{Severity: layout.SeverityBlocking}}
			case "verdict":
				bad.Verdict = layout.VerdictFailed
			}
			reader := layout.NewReader(readOnlyStore{corruptResultStore{Store: m, result: bad, err: err}, t}, nil, nil)
			v := reader.View(ctx, requestWorkflow(t, *req))
			assert.NotEqual(t, layout.DisplayComplete, v.DisplayStatus)
			assert.True(t, v.RetryEligible)
			assert.NotEmpty(t, v.FailureReason)
			assert.Nil(t, v.Evidence)
			if kind == "contract" || kind == "text" || kind == "parallel-edge" || kind == "missing" || kind == "corrupt" || kind == "wrong-revision" || kind == "wrong-generation" || kind == "no-pointer" || kind == "superseded-no-result" {
				assert.Nil(t, v.Geometry)
				assert.Nil(t, v.Contract)
				require.NotNil(t, v.Retained)
				assert.Equal(t, req.Retained.Positions, v.Retained.Positions)
				assert.True(t, v.Unreviewed)
			}
		})
	}
}
func TestReaderUnavailableCacheIsNeverReviewedPass(t *testing.T) {
	ctx := context.Background()
	m, req, f, best := readerRow(t)
	require.NoError(t, m.Complete(ctx, f, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, time.Now()))
	m.SetFailure(errors.New("offline"))
	wf := requestWorkflow(t, *req)
	for _, kind := range []string{"valid", "revision", "generation", "job", "format", "contract", "geometry-contract", "text", "cache-error", "no-cache"} {
		t.Run(kind, func(t *testing.T) {
			cache := layouttest.NewFakeCache()
			e := layout.CacheEntry{Format: layout.CacheFormatV1, Revision: req.Revision, Generation: 1, JobGeneration: 1, Contract: best.Geometry.Contract, Geometry: best.Geometry, Verdict: layout.VerdictPassed}
			switch kind {
			case "revision":
				e.Revision = "other"
			case "generation":
				e.Generation = 0
			case "job":
				e.JobGeneration = 0
			case "format":
				e.Format = "old"
			case "contract":
				e.Contract.NodePaddingX++
			case "geometry-contract":
				e.Geometry.Contract.NodePaddingX++
			case "text":
				e.Geometry.Nodes[0].Text = "wrong"
			case "cache-error":
				cache.SetReadErr(errors.New("disk"))
			}
			if kind != "no-cache" {
				cache.Seed(req.Scope, req.Name, e)
			}
			v := layout.NewReader(readOnlyStore{m, t}, nil, cache).View(ctx, wf)
			assert.Equal(t, layout.DisplayUnavailable, v.DisplayStatus)
			assert.False(t, v.Available)
			assert.True(t, v.Unreviewed)
			assert.True(t, v.RetryEligible)
			assert.NotEmpty(t, v.AvailabilityError)
			assert.Nil(t, v.Evidence)
			if kind == "valid" {
				require.NotNil(t, v.Geometry)
				assert.Equal(t, best.Geometry, *v.Geometry)
				assert.Equal(t, layout.ProvenanceArranged, v.Provenance)
			} else {
				assert.Nil(t, v.Geometry)
				require.NotNil(t, v.Retained)
				assert.Equal(t, req.Retained.Positions, v.Retained.Positions)
			}
		})
	}
}
func TestReaderRetainedImmutableReferenceAndPartialPositions(t *testing.T) {
	ctx := context.Background()
	m, old, f, best := readerRow(t)
	require.NoError(t, m.Complete(ctx, f, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, time.Now()))
	req := savedRequest(t)
	wf := requestWorkflow(t, *req)
	wf.Presentation.Retained = workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: old.Revision, FromGeneration: 1, Positions: map[string]workflow.NodePosition{"start": {X: 1, Y: 2}, "removed": {X: 3, Y: 4}}}
	reader := layout.NewReader(readOnlyStore{m, t}, nil, nil)
	v := reader.View(ctx, wf)
	assert.Equal(t, layout.ProvenanceRetainedGeometry, v.Provenance)
	require.NotNil(t, v.Geometry)
	assert.Equal(t, best.Geometry, *v.Geometry)
	assert.True(t, v.Unreviewed)
	assert.Nil(t, v.Evidence)
	assert.NotEqual(t, layout.DisplayComplete, v.DisplayStatus)
	wf.Nodes[0].Label = "changed"
	v = reader.View(ctx, wf)
	assert.Nil(t, v.Geometry)
	assert.Equal(t, layout.ProvenanceRetainedPositions, v.Provenance)
	require.NotNil(t, v.Retained)
	assert.Equal(t, map[string]workflow.NodePosition{"start": {X: 1, Y: 2}}, v.Retained.Positions)
	v.Retained.Positions["start"] = workflow.NodePosition{X: 999}
	assert.Equal(t, 1.0, wf.Presentation.Retained.Positions["start"].X)
}
func TestViewJSONContract(t *testing.T) {
	raw, err := json.Marshal(layout.View{Findings: []layout.Finding{}})
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))
	for _, key := range []string{"savedRevision", "jobGeneration", "resultGeneration", "displayStatus", "attemptsUsed", "attemptLimit", "findings", "provenance", "unreviewed", "retryEligible", "available"} {
		assert.Contains(t, obj, key)
	}
	for _, key := range []string{"state", "stage", "failureStage", "failureReason", "evidence", "contract", "geometry", "retained", "availabilityError"} {
		assert.NotContains(t, obj, key)
	}
	assert.Len(t, obj, 11)
}
