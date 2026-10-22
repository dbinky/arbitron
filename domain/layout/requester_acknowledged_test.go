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

func TestRequesterAcknowledgedArrangement(t *testing.T) {
	for _, state := range []layout.State{layout.StatePending, layout.StateFailed, layout.StatePassed, layout.StateSuperseded} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			store := layouttest.NewMemStore()
			req := savedRequest(t)
			wf := requestWorkflow(t, *req)
			wf.ID = uuid.New()
			wf.Presentation.Reason = workflow.SaveReasonSeed
			wf.Presentation.Retained.Source = workflow.RetainedFromLayout
			wf.Presentation.Retained.FromRevision = uuid.Must(uuid.NewV7()).String()
			wf.Presentation.Retained.FromGeneration = 7
			req.WorkflowID, req.Reason, req.Retained = wf.ID, wf.Presentation.Reason, wf.Presentation.Retained
			req.Manifest = layouttest.ManifestJSON(t, wf)
			require.NoError(t, layout.NewRequester(store, 3, nil).RequestArrangement(ctx, *req))
			now := req.RequestedAt.Add(time.Second)
			switch state {
			case layout.StateFailed, layout.StatePassed:
				claimed, err := store.Claim(ctx, req.Revision, "worker", now, now.Add(time.Minute))
				require.NoError(t, err)
				require.NoError(t, store.Complete(ctx, layout.Fence{Revision: req.Revision, Generation: claimed.JobGeneration}, layout.Completion{State: state}, now))
			case layout.StateSuperseded:
				require.NoError(t, store.Supersede(ctx, req.Revision, now))
			}
			// Relocation changes the committed envelope, never the captured manifest.
			to := layout.Destination{Scope: workflow.Private(uuid.New()), Name: "moved-seed", WorkflowID: wf.ID}
			_, err := store.Relocate(ctx, layout.Destination{Scope: req.Scope, Name: req.Name}, to, now)
			require.NoError(t, err)
			req.Scope, req.Name = to.Scope, to.Name
			before, err := store.Get(ctx, req.Revision)
			require.NoError(t, err)
			require.Equal(t, state, before.State)

			reader := layout.NewRequester(readOnlyStore{Store: store, t: t}, 9, nil)
			reader.SetNotifier(func(string) { t.Fatal("acknowledgement read notified the worker") })
			got, err := reader.AcknowledgedArrangement(ctx, req.Revision)
			require.NoError(t, err)
			require.Equal(t, *req, got)
			got.Manifest[0] = '!'
			got.Retained.Positions["start"] = workflow.NodePosition{X: -999}
			delete(got.Retained.Positions, "start")
			got.Retained.Positions["foreign"] = workflow.NodePosition{Y: 999}
			again, err := reader.AcknowledgedArrangement(ctx, req.Revision)
			require.NoError(t, err)
			require.Equal(t, *req, again, "returned mutable input must not alias stored history")
			after, err := store.Get(ctx, req.Revision)
			require.NoError(t, err)
			require.Equal(t, before, after, "reading must not reset lifecycle, budget, destination or captured input")
		})
	}
}

func TestRequesterAcknowledgedArrangementErrors(t *testing.T) {
	ctx := context.Background()
	store := layouttest.NewMemStore()
	req := savedRequest(t)
	require.NoError(t, layout.NewRequester(store, 3, nil).RequestArrangement(ctx, *req))
	before, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	reader := layout.NewRequester(readOnlyStore{Store: store, t: t}, 9, nil)
	reader.SetNotifier(func(string) { t.Fatal("failed acknowledgement read notified the worker") })

	missing := uuid.Must(uuid.NewV7()).String()
	got, err := reader.AcknowledgedArrangement(ctx, missing)
	require.ErrorIs(t, err, workflow.ErrNotFound)
	require.Equal(t, workflow.ArrangementRequest{}, got)
	_, err = store.Get(ctx, missing)
	require.ErrorIs(t, err, layout.ErrRecordNotFound, "missing input must not create a row")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	got, err = reader.AcknowledgedArrangement(canceled, req.Revision)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, workflow.ArrangementRequest{}, got)

	store.SetFailure(errors.New("database offline"))
	got, err = reader.AcknowledgedArrangement(ctx, req.Revision)
	require.ErrorIs(t, err, layout.ErrStoreUnavailable)
	require.NotErrorIs(t, err, workflow.ErrNotFound)
	require.Equal(t, workflow.ArrangementRequest{}, got)
	store.SetFailure(nil)
	after, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
