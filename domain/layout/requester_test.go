package layout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func savedRequest(t testing.TB) *workflow.ArrangementRequest {
	t.Helper()
	wf := layouttest.SampleWorkflow()
	rev := uuid.Must(uuid.NewV7()).String()
	at := time.Now().UTC()
	retained := workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"start": {X: 19, Y: 23}}}
	wf.Presentation = &workflow.Presentation{Revision: rev, SavedAt: at, Reason: workflow.SaveReasonUpdate, Scope: workflow.ScopeLabelShared, Name: "sample", Retained: retained}
	return &workflow.ArrangementRequest{Revision: rev, Scope: workflow.Shared(), Name: "sample", Manifest: layouttest.ManifestJSON(t, wf), Reason: workflow.SaveReasonUpdate, RequestedAt: at, Retained: retained}
}
func TestRequesterCapturedSink(t *testing.T) {
	ctx := context.Background()
	m := layouttest.NewMemStore()
	r := layout.NewRequester(m, 3, nil)
	assert.Equal(t, 3, r.AttemptLimit())
	assert.Equal(t, 1, layout.NewRequester(m, 0, nil).AttemptLimit())
	assert.Equal(t, 1, layout.NewRequester(m, -9, nil).AttemptLimit())
	notifications := []string{}
	r.SetNotifier(func(rev string) { notifications = append(notifications, rev) })
	req := savedRequest(t)
	require.NoError(t, r.RequestArrangement(ctx, *req))
	row, err := m.Get(ctx, req.Revision)
	require.NoError(t, err)
	assert.Equal(t, req.Manifest, row.Manifest)
	assert.Equal(t, req.Retained, row.Retained)
	assert.Equal(t, req.RequestedAt, row.RequestedAt)
	assert.Equal(t, req.Reason, row.Reason)
	assert.Equal(t, 3, row.AttemptLimit)
	assert.Equal(t, req.Name, row.Destination.Name)
	assert.Equal(t, []string{req.Revision}, notifications)
	require.NoError(t, r.RelocateArrangements(ctx, req.Scope, req.Name, workflow.Private(uuid.New()), "moved"))
	moved, _ := m.Get(ctx, req.Revision)
	assert.Equal(t, "moved", moved.Destination.Name)
	require.NoError(t, r.RequestArrangement(ctx, *req))
	again, _ := m.Get(ctx, req.Revision)
	assert.Equal(t, moved, again)
	other := savedRequest(t)
	require.NoError(t, r.RequestArrangement(ctx, *other))
	row, _ = m.Get(ctx, req.Revision)
	assert.Equal(t, layout.StatePending, row.State, "new saves never supersede historical work")
	m.SetFailure(errors.New("offline"))
	before := len(notifications)
	assert.ErrorIs(t, r.RequestArrangement(ctx, *savedRequest(t)), layout.ErrStoreUnavailable)
	assert.Len(t, notifications, before)
	m.SetFailure(nil)
	// Recovery after a live store outage replays immutable captured bytes at a moved locator.
	replay := savedRequest(t)
	replay.Scope = workflow.Private(uuid.New())
	replay.Name = "relocated-before-enqueue"
	require.NoError(t, r.RequestArrangement(ctx, *replay))
	row, _ = m.Get(ctx, replay.Revision)
	assert.Equal(t, replay.Manifest, row.Manifest)
	assert.Equal(t, replay.Scope, row.Destination.Scope)
	assert.Equal(t, replay.Name, row.Destination.Name)
}
func TestRequesterRejectsEnvelopeNotGraph(t *testing.T) {
	ctx := context.Background()
	for _, mutate := range []func(*workflow.ArrangementRequest){func(r *workflow.ArrangementRequest) { r.Revision = "" }, func(r *workflow.ArrangementRequest) { r.Manifest = nil }, func(r *workflow.ArrangementRequest) { r.Manifest = []byte("bad") }, func(r *workflow.ArrangementRequest) { r.Revision = uuid.NewString() }, func(r *workflow.ArrangementRequest) { r.Revision = uuid.Must(uuid.NewV7()).String() }, func(r *workflow.ArrangementRequest) { r.Name = "../escape" }, func(r *workflow.ArrangementRequest) { r.Scope.Kind = 99 }, func(r *workflow.ArrangementRequest) { r.Scope = workflow.Private(uuid.Nil) }, func(r *workflow.ArrangementRequest) { r.Scope.OwnerID = uuid.New() }} {
		m := layouttest.NewMemStore()
		r := layout.NewRequester(m, 5, nil)
		req := savedRequest(t)
		mutate(req)
		assert.ErrorIs(t, r.RequestArrangement(ctx, *req), layout.ErrInvalidArrangementRequest)
		rows, _ := m.Claimable(ctx, time.Now(), 0)
		assert.Empty(t, rows)
	}
	m := layouttest.NewMemStore()
	r := layout.NewRequester(m, 5, nil)
	req := savedRequest(t)
	wf := layouttest.SampleWorkflow()
	wf.Presentation = &workflow.Presentation{Revision: req.Revision}
	wf.Edges = append(wf.Edges, workflow.Edge{Source: "missing", Target: "start"})
	req.Manifest = layouttest.ManifestJSON(t, wf)
	require.NoError(t, r.RequestArrangement(ctx, *req))
	row, err := m.Get(ctx, req.Revision)
	require.NoError(t, err)
	assert.Equal(t, req.Manifest, row.Manifest, "bad topology fails asynchronously, not on authoring save")
}
func TestRequesterRetryEvidence(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"valid", "missing", "contract", "text", "empty-evidence", "blocking-preflight", "blocking-review", "inconsistent-verdict", "corrupt-input", "wrong-input-revision", "superseded-invalid", "superseded-valid"} {
		t.Run(kind, func(t *testing.T) {
			m := layouttest.NewMemStore()
			r := layout.NewRequester(m, 4, nil)
			req := savedRequest(t)
			if kind == "corrupt-input" {
				req.Manifest = []byte("corrupt")
			}
			if kind == "wrong-input-revision" {
				wf := requestWorkflow(t, *req)
				wf.Presentation.Revision = "other"
				req.Manifest = layouttest.ManifestJSON(t, wf)
			}
			_, _, err := m.Request(ctx, layout.NewRequest{Revision: req.Revision, Destination: layout.Destination{Scope: req.Scope, Name: req.Name}, Manifest: req.Manifest, AttemptLimit: 5, RequestedAt: req.RequestedAt})
			require.NoError(t, err)
			row, err := m.Claim(ctx, req.Revision, "worker", time.Now(), time.Now().Add(time.Minute))
			require.NoError(t, err)
			f := layout.Fence{Revision: req.Revision, Generation: row.JobGeneration}
			geo := layouttest.Arranged(t, layouttest.SampleWorkflow())
			best := layout.StoredResult{Revision: req.Revision, Generation: 1, Geometry: geo, Review: unitReview(geo), Verdict: layout.VerdictPassed, CreatedAt: time.Now()}
			switch kind {
			case "contract":
				best.Geometry.Contract.NodePaddingX++
			case "text":
				best.Geometry.Nodes[0].Text = "wrong"
			case "empty-evidence", "superseded-invalid":
				best.Review.Evidence.Artifacts = nil
			case "blocking-preflight":
				best.Preflight = []layout.Finding{{Severity: layout.SeverityBlocking}}
			case "blocking-review":
				best.Review.Findings = []layout.Finding{{Severity: layout.SeverityBlocking}}
			case "inconsistent-verdict":
				best.Verdict = layout.VerdictNeedsAttention
			}
			if kind == "superseded-invalid" || kind == "superseded-valid" {
				require.NoError(t, m.Supersede(ctx, req.Revision, time.Now()))
			}
			c := layout.Completion{State: layout.StatePassed, ReplaceBest: true, Result: &best}
			if kind == "missing" {
				c.Result = nil
			}
			require.NoError(t, m.Complete(ctx, f, c, time.Now()))
			notified := ""
			r.SetNotifier(func(rev string) { notified = rev })
			retried, err := r.Retry(ctx, req.Revision)
			if kind == "valid" || kind == "superseded-valid" {
				assert.ErrorIs(t, err, layout.ErrRetryConflict)
				assert.Empty(t, notified)
			} else {
				require.NoError(t, err)
				assert.EqualValues(t, 2, retried.JobGeneration)
				assert.Equal(t, 4, retried.AttemptLimit)
				assert.Equal(t, layout.StatePending, retried.State)
				assert.Equal(t, req.Revision, notified)
				assert.Equal(t, req.Manifest, retried.Manifest)
			}
		})
	}
}

// A storage failure while loading a passed result must not masquerade as missing evidence.
type resultErrorStore struct {
	layout.Store
	err           error
	invalidations int
}

func (s *resultErrorStore) Result(context.Context, string, int64) (layout.StoredResult, error) {
	return layout.StoredResult{}, s.err
}
func (s *resultErrorStore) InvalidateResult(ctx context.Context, f layout.Fence, g int64, reason string, now time.Time) error {
	s.invalidations++
	return s.Store.InvalidateResult(ctx, f, g, reason, now)
}
func TestRequesterRetryOutageAndStaleInvalidation(t *testing.T) {
	ctx := context.Background()
	m := layouttest.NewMemStore()
	req := savedRequest(t)
	r := layout.NewRequester(m, 5, nil)
	require.NoError(t, r.RequestArrangement(ctx, *req))
	row, err := m.Claim(ctx, req.Revision, "worker", time.Now(), time.Now().Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, m.Complete(ctx, layout.Fence{Revision: req.Revision, Generation: row.JobGeneration}, layout.Completion{State: layout.StatePassed}, time.Now()))
	s := &resultErrorStore{Store: m, err: layout.ErrStoreUnavailable}
	_, err = layout.NewRequester(s, 5, nil).Retry(ctx, req.Revision)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
	assert.Zero(t, s.invalidations)
	before, _ := m.Get(ctx, req.Revision)
	assert.Equal(t, layout.StatePassed, before.State)
	s.err = layout.ErrRecordNotFound
	_, err = layout.NewRequester(s, 5, nil).Retry(ctx, req.Revision)
	require.NoError(t, err)
	assert.Equal(t, 1, s.invalidations)
	assert.ErrorIs(t, m.InvalidateResult(ctx, layout.Fence{Revision: req.Revision, Generation: 1}, 0, "late", time.Now()), layout.ErrStaleGeneration)
	after, _ := m.Get(ctx, req.Revision)
	assert.EqualValues(t, 2, after.JobGeneration)
}

// One revision has one captured input: a second request for the same token
// with different bytes must not be acknowledged, or recovery would discard
// the committed journal input while the worker arranges the first bytes.
func TestRequesterRejectsConflictingInputForSameRevision(t *testing.T) {
	ctx := context.Background()
	m := layouttest.NewMemStore()
	r := layout.NewRequester(m, 5, nil)
	notified := 0
	r.SetNotifier(func(string) { notified++ })
	req := savedRequest(t)
	require.NoError(t, r.RequestArrangement(ctx, *req))
	require.NoError(t, r.RequestArrangement(ctx, *req), "an identical replay is idempotent")
	equivalent := *req
	equivalent.Manifest = append([]byte(" "), req.Manifest...)
	require.NoError(t, r.RequestArrangement(ctx, equivalent), "formatting alone is not a different input")
	require.Equal(t, 3, notified)

	wf := layouttest.SampleWorkflow()
	wf.Presentation = &workflow.Presentation{Revision: req.Revision, Name: "sample"}
	wf.Nodes[0].Label = "mutated after capture"
	conflicting := *req
	conflicting.Manifest = layouttest.ManifestJSON(t, wf)
	assert.ErrorIs(t, r.RequestArrangement(ctx, conflicting), layout.ErrConflictingArrangementInput)
	assert.Equal(t, 3, notified, "a conflicting input is never announced")
	row, err := m.Get(ctx, req.Revision)
	require.NoError(t, err)
	assert.Equal(t, req.Manifest, row.Manifest, "the first acknowledged bytes stay authoritative")
}

// Numbers are captured input too: integers beyond float64 precision that
// differ are different captures, while number formatting alone is not.
func TestRequesterRejectsConflictingLargeIntegerInput(t *testing.T) {
	ctx := context.Background()
	m := layouttest.NewMemStore()
	r := layout.NewRequester(m, 5, nil)
	req := savedRequest(t)
	req.Manifest = withMaxTokens(t, req.Manifest, 9007199254740992)
	require.NoError(t, r.RequestArrangement(ctx, *req))
	conflicting := *req
	conflicting.Manifest = withMaxTokens(t, req.Manifest, 9007199254740993)
	assert.ErrorIs(t, r.RequestArrangement(ctx, conflicting), layout.ErrConflictingArrangementInput)
	row, err := m.Get(ctx, req.Revision)
	require.NoError(t, err)
	assert.Equal(t, req.Manifest, row.Manifest)

	equivalent := *req
	equivalent.Manifest = bytes.Replace(req.Manifest, []byte(`"temperature":0.5`), []byte(`"temperature":5e-1`), 1)
	require.NotEqual(t, req.Manifest, equivalent.Manifest)
	require.NoError(t, r.RequestArrangement(ctx, equivalent), "number formatting alone is not a different input")
}

func withMaxTokens(t testing.TB, manifest []byte, n int) []byte {
	t.Helper()
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(manifest, &wf))
	wf.Nodes[0].Config.MaxTokens = n
	wf.Nodes[0].Config.Temperature = 0.5
	out, err := json.Marshal(&wf)
	require.NoError(t, err)
	return out
}
