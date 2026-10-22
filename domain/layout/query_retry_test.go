package layout_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type queryResultStore struct {
	layout.Store
	resultErr error
	corrupt   bool
}

func (s *queryResultStore) Result(ctx context.Context, rev string, generation int64) (layout.StoredResult, error) {
	if s.resultErr != nil {
		return layout.StoredResult{}, s.resultErr
	}
	r, err := s.Store.Result(ctx, rev, generation)
	if s.corrupt {
		r.Geometry.Contract.Version = "incompatible"
	}
	return r, err
}

func TestQueryService_CorruptPassedResultRetry(t *testing.T) {
	for _, problem := range []string{"missing", "incompatible", "outage", "valid"} {
		t.Run(problem, func(t *testing.T) {
			h := newQueryHarness(t)
			scope := workflow.Private(queryOwner)
			wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
			wf.Presentation.Retained = workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"a": {X: 10, Y: 20}}}
			h.workflows.readable["wf"], h.workflows.writable["wf"] = queryAccessEntry{wf, scope}, queryAccessEntry{wf, scope}
			h.completePassed(t, wf, scope)
			before := h.get(t, queryRev1)
			store := &queryResultStore{Store: h.store, corrupt: problem == "incompatible"}
			if problem == "missing" {
				store.resultErr = layout.ErrRecordNotFound
			}
			if problem == "outage" {
				store.resultErr = layout.ErrStoreUnavailable
			}
			h.q = layout.NewQueryService(layout.NewReader(store, nil, nil), layout.NewRequester(store, 3, nil), h.workflows, h.instances, &queryMutationGate{t: t, access: h.workflows})
			v, err := h.q.WorkflowLayout(context.Background(), "wf")
			require.NoError(t, err)
			if problem == "missing" || problem == "incompatible" {
				assert.Equal(t, layout.DisplayNeedsAttention, v.DisplayStatus)
				assert.True(t, v.RetryEligible)
				assert.Nil(t, v.Geometry)
				require.NotNil(t, v.Retained)
				assert.Equal(t, wf.Presentation.Retained.Positions, v.Retained.Positions)
			}
			ack, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
			switch problem {
			case "outage":
				assert.ErrorIs(t, err, layout.ErrStoreUnavailable)
				assert.Equal(t, before, h.get(t, queryRev1))
			case "valid":
				assert.ErrorIs(t, err, layout.ErrRetryConflict)
				assert.Equal(t, before, h.get(t, queryRev1))
			default:
				require.NoError(t, err)
				assert.Equal(t, before.JobGeneration+1, ack.JobGeneration)
				after := h.get(t, queryRev1)
				assert.Equal(t, layout.StatePending, after.State)
				assert.Equal(t, ack.JobGeneration, after.JobGeneration)
				assert.Equal(t, before.Manifest, after.Manifest)
			}
		})
	}
}

// Rejecting an old result must not hide the repair generation or offer a retry
// that the store will reject. Removing the active-state guard breaks this matrix.
func TestQueryService_ActiveRepairPreservesStatusAndRetryPolicy(t *testing.T) {
	ctx := context.Background()
	for _, problem := range []string{"incompatible", "missing", "outage", "deadline"} {
		for _, active := range []struct {
			stage layout.Stage
			want  layout.DisplayStatus
		}{
			{layout.StageQueued, layout.DisplayPending},
			{layout.StageArranging, layout.DisplayArranging},
			{layout.StageRendering, layout.DisplayRendering},
			{layout.StageReviewing, layout.DisplayReviewing},
			{layout.StagePublishing, layout.DisplayReviewing},
		} {
			t.Run(problem+"/"+string(active.stage), func(t *testing.T) {
				h := newQueryHarness(t)
				scope := workflow.Private(queryOwner)
				wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
				wf.Presentation.Retained = workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"a": {X: 10, Y: 20}}}
				h.workflows.readable["wf"], h.workflows.writable["wf"] = queryAccessEntry{wf, scope}, queryAccessEntry{wf, scope}
				id := uuid.New()
				h.instances.viewable[id], h.instances.retryable[id] = querySnapshotCopy(t, wf), querySnapshotCopy(t, wf)
				h.completePassed(t, wf, scope)
				store := &queryResultStore{Store: h.store, corrupt: true}
				h.q = layout.NewQueryService(layout.NewReader(store, nil, nil), layout.NewRequester(store, 3, nil), h.workflows, h.instances, &queryMutationGate{t: t, access: h.workflows})
				ack, err := h.q.RetryWorkflow(ctx, "wf", queryRev1)
				require.NoError(t, err)
				require.EqualValues(t, 2, ack.JobGeneration)
				if active.stage != layout.StageQueued {
					row, err := h.store.Claim(ctx, queryRev1, "repair", queryNow, queryNow.Add(time.Minute))
					require.NoError(t, err)
					require.NoError(t, h.store.Progress(ctx, layout.Fence{Revision: queryRev1, Generation: row.JobGeneration}, active.stage, 1, queryNow, queryNow.Add(time.Minute)))
				}
				switch problem {
				case "missing":
					store.resultErr = layout.ErrRecordNotFound
				case "outage":
					store.resultErr = layout.ErrStoreUnavailable
				case "deadline":
					store.resultErr = context.DeadlineExceeded
				}
				before := h.get(t, queryRev1)
				require.EqualValues(t, 1, before.ResultGeneration, "exercise the retained prior-result branch")
				v, err := h.q.WorkflowLayout(ctx, "wf")
				require.NoError(t, err)
				assert.Equal(t, active.want, v.DisplayStatus)
				assert.False(t, v.RetryEligible)
				assert.Equal(t, before.State, v.State)
				assert.Equal(t, active.stage, v.Stage)
				assert.Equal(t, ack.JobGeneration, v.JobGeneration)
				assert.False(t, v.Available)
				assert.True(t, v.Unreviewed)
				assert.NotEmpty(t, v.AvailabilityError)
				assert.Nil(t, v.Geometry)
				assert.Nil(t, v.Evidence)
				require.NotNil(t, v.Retained)
				assert.Equal(t, wf.Presentation.Retained.Positions, v.Retained.Positions)
				iv, err := h.q.InstanceLayout(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, v, iv)
				_, err = h.q.RetryWorkflow(ctx, "wf", queryRev1)
				assert.ErrorIs(t, err, layout.ErrRetryConflict)
				_, err = h.q.RetryInstance(ctx, id, queryRev1)
				assert.ErrorIs(t, err, layout.ErrRetryConflict)
				assert.Equal(t, before, h.get(t, queryRev1), "reads and rejected retries must not mutate the active generation")
			})
		}
	}
}

func TestQueryService_RetryInstance_PrefersCommittedRelocatedEnvelope(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint("committed=", committed), func(t *testing.T) {
			h := newQueryHarness(t)
			wf := queryStamped("original", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
			scope := workflow.Private(queryOwner)
			journal := layouttest.NewFakeJournal(workflow.ArrangementRequest{
				Revision: queryRev1, Scope: scope, Name: "moved", WorkflowID: wf.ID, Reason: wf.Presentation.Reason,
				Manifest: layouttest.ManifestJSON(t, wf), Retained: wf.Presentation.Retained, RequestedAt: queryNow,
			})
			if committed {
				require.NoError(t, journal.MarkCommitted(context.Background(), queryRev1))
			}
			id := uuid.New()
			h.instances.retryable[id] = querySnapshotCopy(t, wf)
			gate := &queryMutationGate{t: t, access: h.workflows}
			h.q = layout.NewQueryService(layout.NewReader(h.store, journal, nil), layout.NewRequester(h.store, 3, nil), h.workflows, h.instances, gate)
			ack, err := h.q.RetryInstance(context.Background(), id, queryRev1)
			require.NoError(t, err)
			rec := h.get(t, queryRev1)
			wantName := "original"
			if committed {
				wantName = "moved"
			}
			assert.Equal(t, wantName, rec.Destination.Name)
			assert.Equal(t, scope, rec.Destination.Scope)
			require.Len(t, gate.historical, 1)
			assert.Contains(t, gate.historical[0], workflow.MutationTarget{Scope: scope, Name: wantName}, "the inserted destination must be locked")
			assert.Equal(t, queryRev1, ack.SavedRevision)
			assert.JSONEq(t, string(layouttest.ManifestJSON(t, wf)), string(rec.Manifest))
			assert.Zero(t, h.workflows.calls())
		})
	}
}

func TestQueryService_HistoricalMissingRequestsRemainIndependent(t *testing.T) {
	h := newQueryHarness(t)
	for _, rev := range []string{queryRev1, queryRev2} {
		wf := queryStamped("deleted", rev, workflow.ScopeLabelPrivate, "a", "b")
		id := uuid.New()
		h.instances.retryable[id] = wf
		ack, err := h.q.RetryInstance(context.Background(), id, rev)
		require.NoError(t, err)
		assert.Equal(t, rev, ack.SavedRevision)
	}
	for _, rev := range []string{queryRev1, queryRev2} {
		assert.Equal(t, layout.StatePending, h.get(t, rev).State)
	}
	assert.Zero(t, h.workflows.calls(), "deleted current workflow is not needed")
}

func TestQueryService_RetryWorkflowInverseOrdering(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	old := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	newer := queryStamped("wf", queryRev2, workflow.ScopeLabelPrivate, "a", "b")
	h.workflows.writable["wf"] = queryAccessEntry{old, scope}
	h.completeFailed(t, old, scope)
	ack, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.NoError(t, err)
	h.request(t, newer, scope)
	assert.Equal(t, ack.JobGeneration, h.get(t, queryRev1).JobGeneration)
	assert.Equal(t, layout.StatePending, h.get(t, queryRev1).State)
	assert.Equal(t, layout.StatePending, h.get(t, queryRev2).State)
	// Old generation cannot complete after retry accepted.
	err = h.store.Complete(context.Background(), layout.Fence{Revision: queryRev1, Generation: ack.JobGeneration - 1}, layout.Completion{State: layout.StateFailed}, queryNow)
	assert.True(t, errors.Is(err, layout.ErrStaleGeneration))
}

// A revision token is not write authority over a row at another destination.
func TestQueryService_RetryWorkflowRejectsForeignDestination(t *testing.T) {
	for _, mismatch := range []string{"scope", "name", "identity"} {
		t.Run(mismatch, func(t *testing.T) {
			h := newQueryHarness(t)
			scope := workflow.Private(queryOwner)
			wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
			h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
			stored := querySnapshotCopy(t, wf)
			storedScope := scope
			switch mismatch {
			case "scope":
				storedScope = workflow.Shared()
			case "name":
				stored.Presentation.Name = "other"
			case "identity":
				stored.ID = uuid.New()
			}
			h.completeFailed(t, stored, storedScope)
			before := h.get(t, queryRev1)
			_, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
			assert.ErrorIs(t, err, layout.ErrRevisionConflict)
			assert.Equal(t, before, h.get(t, queryRev1), "foreign history must remain byte-for-byte unchanged")
		})
	}
}

// Recovery can lose the insert race to a historical request for the same token.
// The existing foreign row must not be acknowledged or notified as our request.
type competingQueryRequestStore struct {
	layout.Store
	request layout.NewRequest
}

func (s *competingQueryRequestStore) Request(ctx context.Context, req layout.NewRequest) (layout.Record, bool, error) {
	if _, _, err := s.Store.Request(ctx, s.request); err != nil {
		return layout.Record{}, false, err
	}
	return s.Store.Request(ctx, req)
}

func TestQueryService_MissingRetryRejectsForeignInsertWinner(t *testing.T) {
	h := newQueryHarness(t)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	h.workflows.writable["wf"] = queryAccessEntry{wf, workflow.Private(queryOwner)}
	store := &competingQueryRequestStore{Store: h.store, request: layout.NewRequest{
		Revision: queryRev1, Destination: layout.Destination{Scope: workflow.Shared(), Name: "wf", WorkflowID: wf.ID},
		Manifest: layouttest.ManifestJSON(t, wf), AttemptLimit: 3, RequestedAt: queryNow,
	}}
	requester := layout.NewRequester(store, 3, nil)
	notified := false
	requester.SetNotifier(func(string) { notified = true })
	q := layout.NewQueryService(layout.NewReader(store, nil, nil), requester, h.workflows, h.instances, &queryMutationGate{t: t, access: h.workflows})
	_, err := q.RetryWorkflow(context.Background(), "wf", queryRev1)
	assert.ErrorIs(t, err, layout.ErrRevisionConflict)
	assert.False(t, notified)
	row := h.get(t, queryRev1)
	assert.Equal(t, workflow.Shared(), row.Destination.Scope)
	assert.EqualValues(t, 1, row.JobGeneration)
	assert.Equal(t, queryNow, row.UpdatedAt)
	assert.Equal(t, layout.StatePending, row.State)
	// The winner remains an ordinary claimable request, not a modified retry.
	_, err = h.store.Claim(context.Background(), queryRev1, "worker", queryNow, queryNow.Add(time.Minute))
	require.NoError(t, err)
}

// A transfer can move the committed envelope between the unlocked lookup and the
// insert. Retry must lock the destination it inserts at and re-read the journal
// under that lock; an unlocked insert strands the row at the vacated locator.
func TestQueryService_RetryInstance_RevalidatesRelocationUnderLock(t *testing.T) {
	ctx := context.Background()
	h := newQueryHarness(t)
	wf := queryStamped("original", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	scope, moved := workflow.Private(queryOwner), workflow.Private(uuid.New())
	journal := layouttest.NewFakeJournal(workflow.ArrangementRequest{
		Revision: queryRev1, Scope: scope, Name: "original", WorkflowID: wf.ID, Reason: wf.Presentation.Reason,
		Manifest: layouttest.ManifestJSON(t, wf), Retained: wf.Presentation.Retained, RequestedAt: queryNow,
	})
	require.NoError(t, journal.MarkCommitted(ctx, queryRev1))
	id := uuid.New()
	h.instances.retryable[id] = querySnapshotCopy(t, wf)
	gate := &queryMutationGate{t: t, access: h.workflows}
	relocated := false
	gate.before = func() {
		if !relocated {
			relocated = true
			require.NoError(t, journal.Relocate(ctx, scope, "original", moved, "original"))
		}
	}
	h.q = layout.NewQueryService(layout.NewReader(h.store, journal, nil), layout.NewRequester(h.store, 3, nil), h.workflows, h.instances, gate)

	_, err := h.q.RetryInstance(ctx, id, queryRev1)
	require.NoError(t, err)
	rec := h.get(t, queryRev1)
	assert.Equal(t, layout.Destination{Scope: moved, Name: "original", WorkflowID: wf.ID}, rec.Destination)
	assert.JSONEq(t, string(layouttest.ManifestJSON(t, wf)), string(rec.Manifest))
	require.Len(t, gate.historical, 2, "a moved envelope invalidates the first lock set")
	assert.Equal(t, []workflow.MutationTarget{{Scope: scope, Name: "original"}}, gate.historical[0])
	assert.Equal(t, []workflow.MutationTarget{{Scope: scope, Name: "original"}, {Scope: moved, Name: "original"}}, gate.historical[1])
}

func TestQueryService_RetryInstance_UnsettledRelocationConflicts(t *testing.T) {
	ctx := context.Background()
	h := newQueryHarness(t)
	wf := queryStamped("original", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	at := workflow.Private(queryOwner)
	journal := layouttest.NewFakeJournal(workflow.ArrangementRequest{
		Revision: queryRev1, Scope: at, Name: "original", WorkflowID: wf.ID,
		Manifest: layouttest.ManifestJSON(t, wf), RequestedAt: queryNow,
	})
	require.NoError(t, journal.MarkCommitted(ctx, queryRev1))
	id := uuid.New()
	h.instances.retryable[id] = querySnapshotCopy(t, wf)
	gate := &queryMutationGate{t: t, access: h.workflows}
	gate.before = func() {
		next := workflow.Private(uuid.New())
		require.NoError(t, journal.Relocate(ctx, at, "original", next, "original"))
		at = next
	}
	h.q = layout.NewQueryService(layout.NewReader(h.store, journal, nil), layout.NewRequester(h.store, 3, nil), h.workflows, h.instances, gate)

	_, err := h.q.RetryInstance(ctx, id, queryRev1)
	assert.ErrorIs(t, err, layout.ErrRevisionConflict)
	_, err = h.store.Get(ctx, queryRev1)
	assert.ErrorIs(t, err, layout.ErrRecordNotFound, "no unlocked destination may be inserted")
}
