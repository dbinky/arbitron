package layout_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Invalid historical configs must remain inspectable through the authorized
// read/retry seam, but cannot become ordinary-box arrangement successes.
func TestQueryService_InvalidSavedGroupsRemainReadableAndRetryable(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "fan")
	wf.Nodes[0].Type = workflow.NodeTypeParallelGroup
	wf.Presentation.Retained = workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"fan": {X: 123, Y: 456}}}
	h.workflows.readable["wf"] = queryAccessEntry{wf, scope}
	h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
	id := uuid.New()
	h.instances.viewable[id] = querySnapshotCopy(t, wf)
	h.instances.retryable[id] = querySnapshotCopy(t, wf)
	before, err := json.Marshal(wf)
	require.NoError(t, err)
	renderer := layouttest.NewFakeRenderer()
	co, err := layout.NewCoordinator(renderer, layouttest.NewFakeReviewer())
	require.NoError(t, err)
	result := co.Run(context.Background(), layout.Job{Revision: wf.SavedRevision(), Manifest: wf, MaxAttempts: 5}, nil)
	require.Equal(t, layout.VerdictFailed, result.Verdict)
	require.NotNil(t, result.Failure)
	assert.Equal(t, layout.StageArranging, result.Failure.Stage)
	assert.Contains(t, result.Failure.Reason, layout.ErrInvalidGroups.Error())
	assert.Nil(t, result.Best, "invalid input never yields a reviewable candidate")
	assert.Zero(t, renderer.MeasureCalls())
	fence := h.claim(t, wf, scope)
	require.NoError(t, h.store.Complete(context.Background(), fence, layout.Completion{State: layout.StateFailed, FailureStage: result.Failure.Stage, FailureReason: result.Failure.Reason}, queryNow))
	for _, read := range []func() (layout.View, error){func() (layout.View, error) { return h.q.WorkflowLayout(context.Background(), "wf") }, func() (layout.View, error) { return h.q.InstanceLayout(context.Background(), id) }} {
		view, err := read()
		require.NoError(t, err, "malformed group config cannot hide its stored failure")
		assert.Equal(t, queryRev1, view.SavedRevision)
		assert.Equal(t, layout.DisplayFailed, view.DisplayStatus)
		assert.Contains(t, view.FailureReason, layout.ErrInvalidGroups.Error())
		assert.True(t, view.Unreviewed)
		assert.True(t, view.RetryEligible)
		assert.Nil(t, view.Geometry)
		require.NotNil(t, view.Retained)
		assert.Equal(t, wf.Presentation.Retained.Positions, view.Retained.Positions)
	}
	ack, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ack.JobGeneration)
	row := h.get(t, queryRev1)
	assert.JSONEq(t, string(before), string(row.Manifest))
	after, err := json.Marshal(wf)
	require.NoError(t, err)
	assert.Equal(t, before, after, "reading/retrying must not rewrite legacy input")
}
