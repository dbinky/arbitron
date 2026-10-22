package layout_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func capturedGroupRow(t *testing.T, name string) (*layouttest.MemStore, *workflow.Workflow, layout.StoredResult) {
	t.Helper()
	ctx := context.Background()
	wf, geo, review := layouttest.CapturedGroup(t, name)
	req := savedRequest(t)
	wf.ID = req.WorkflowID
	wf.Name = req.Name
	wf.Presentation = requestWorkflow(t, *req).Presentation
	req.Manifest = layouttest.ManifestJSON(t, wf)
	store := layouttest.NewMemStore()
	require.NoError(t, layout.NewRequester(store, 5, nil).RequestArrangement(ctx, *req))
	row, err := store.Claim(ctx, req.Revision, "worker", time.Now(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	best := layout.StoredResult{Revision: req.Revision, Generation: row.JobGeneration, Geometry: geo, Review: review, Verdict: layout.VerdictPassed, Score: layout.ScoreOf(1, review.Findings, layout.CrossingCount(geo)), CreatedAt: time.Now().UTC()}
	require.NoError(t, store.Complete(ctx, layout.Fence{Revision: req.Revision, Generation: row.JobGeneration}, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true, AttemptsUsed: 1}, time.Now()))
	return store, wf, best
}

func TestReader_GroupStoredTruthAndRetry(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"branches", "workflows", "nested", "loop"} {
		t.Run(name, func(t *testing.T) {
			store, wf, best := capturedGroupRow(t, name)
			before := layouttest.ManifestJSON(t, wf)
			view := layout.NewReader(readOnlyStore{store, t}, nil, nil).View(ctx, wf)
			require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
			require.False(t, view.Unreviewed)
			require.False(t, view.RetryEligible)
			require.Equal(t, int64(1), view.JobGeneration)
			require.Equal(t, int64(1), view.ResultGeneration)
			require.NotNil(t, view.Geometry)
			require.Equal(t, best.Geometry, *view.Geometry)
			viewed := best.Review.Evidence.ForViewer()
			require.Equal(t, &viewed, view.Evidence)
			// The served view carries no server paths, while the persisted pass
			// keeps them for HasRenderedEvidence and still reloads as passed.
			body, err := json.Marshal(view)
			require.NoError(t, err)
			for _, a := range best.Review.Evidence.Artifacts {
				require.NotEmpty(t, a.Path)
				require.NotContains(t, string(body), a.Path)
			}
			for _, a := range view.Evidence.Artifacts {
				require.Empty(t, a.Path)
				require.NotEmpty(t, a.SHA256)
			}
			groups, findings := workflow.AnalyzeGroups(wf)
			require.Empty(t, findings)
			require.Equal(t, groups, view.Groups)
			snapshot := requestWorkflow(t, workflow.ArrangementRequest{Manifest: before})
			require.Equal(t, view, layout.NewReader(store, nil, nil).View(ctx, snapshot))
			_, err = layout.NewRequester(store, 5, nil).Retry(ctx, wf.SavedRevision())
			require.ErrorIs(t, err, layout.ErrRetryConflict)
			require.Equal(t, before, layouttest.ManifestJSON(t, wf))
		})
	}
	for _, tc := range []struct {
		kind       string
		status     layout.DisplayStatus
		reason     string
		provenance layout.Provenance
	}{
		{"missing geometry", layout.DisplayNeedsAttention, "stored geometry is incompatible with this revision", layout.ProvenanceRetainedPositions},
		{"v1", layout.DisplayNeedsAttention, "stored geometry is incompatible with this revision", layout.ProvenanceRetainedPositions},
		{"wrong contract", layout.DisplayNeedsAttention, "stored geometry is incompatible with this revision", layout.ProvenanceRetainedPositions},
		{"missing result", layout.DisplayNeedsAttention, "stored result is missing", layout.ProvenanceRetainedPositions},
		{"corrupt result", layout.DisplayNeedsAttention, "stored result is corrupt", layout.ProvenanceRetainedPositions},
		{"outage", layout.DisplayUnavailable, "stored result unavailable", layout.ProvenanceRetainedPositions},
		{"missing evidence", layout.DisplayNeedsAttention, "stored result lacks matching rendered evidence", layout.ProvenanceArranged},
		{"wrong generation", layout.DisplayNeedsAttention, "stored geometry is incompatible with this revision", layout.ProvenanceRetainedPositions},
		{"missing ownership", layout.DisplayFailed, "layout: invalid parallel group graph: node \"fan\": parallel_group has no group configuration: set config.group with form, tasks, completionRule, unfinishedBehavior and gatherMode", layout.ProvenanceRetainedPositions},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			kind := tc.kind
			store, wf, best := capturedGroupRow(t, "nested")
			before := layouttest.ManifestJSON(t, wf)
			var loadErr error
			switch kind {
			case "missing geometry":
				best.Geometry.GroupElements = nil
			case "v1":
				best.Geometry.Format = layout.FormatV1
			case "wrong contract":
				best.Geometry.Contract.GroupPaddingPx++
			case "missing result":
				loadErr = layout.ErrRecordNotFound
			case "corrupt result":
				loadErr = layout.ErrStoreCorrupt
			case "outage":
				loadErr = layout.ErrStoreUnavailable
			case "missing evidence":
				best.Review = nil
			case "wrong generation":
				best.Generation++
			case "missing ownership":
				wf.Nodes[1].Config.Group = nil
			}
			port := corruptResultStore{Store: store, result: best, err: loadErr}
			view := layout.NewReader(readOnlyStore{port, t}, nil, nil).View(ctx, wf)
			require.Equal(t, tc.status, view.DisplayStatus)
			require.False(t, view.Available)
			require.True(t, view.Unreviewed)
			require.True(t, view.RetryEligible)
			require.Nil(t, view.Evidence)
			require.Equal(t, tc.reason, view.FailureReason)
			require.Equal(t, tc.provenance, view.Provenance)
			require.NotNil(t, view.Retained)
			require.Equal(t, map[string]workflow.NodePosition{"start": {X: 19, Y: 23}}, view.Retained.Positions)
			if kind != "missing ownership" {
				groups, fs := workflow.AnalyzeGroups(wf)
				require.Empty(t, fs)
				require.Equal(t, groups, view.Groups)
				require.Equal(t, before, layouttest.ManifestJSON(t, wf))
			}
			if kind == "missing geometry" || kind == "v1" || kind == "wrong contract" {
				require.Nil(t, view.Geometry)
			}
			if kind == "missing geometry" || kind == "v1" || kind == "wrong contract" || kind == "missing result" || kind == "missing evidence" {
				ack, err := layout.NewRequester(port, 5, nil).Retry(ctx, wf.SavedRevision())
				require.NoError(t, err)
				require.Equal(t, int64(2), ack.JobGeneration)
				require.Equal(t, layout.StatePending, ack.State)
			}
		})
	}
}
