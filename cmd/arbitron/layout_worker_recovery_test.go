package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Recovery must feed the production browser/reviewer from committed journal
// bytes, including history which can no longer be loaded from the catalog.
func TestLayoutRender_WorkerRecoveryUsesCommittedSavedInputs(t *testing.T) {
	browser := requireBrowserE2E(t)
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "3"})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)

	// Consume the initial sweep before simulating an outage, so startup replay
	// cannot masquerade as recovery by the live production poll.
	require.NoError(t, ds.workflowSvc.Create(ctx, "ready", layoutE2EWorkflow("ready", "Ready")))
	ready := layoutRenderWaitTerminal(t, ds, layoutE2ESavedRevision(t, env.privatePath("ready")), time.Minute)
	require.Equal(t, layout.StatePassed, ready.State, "failure %s: %s; findings %+v", ready.FailureStage, ready.FailureReason, ready.Findings)
	_, err := ds.db.Exec(`CREATE TRIGGER worker_recovery_outage BEFORE INSERT ON layout_revisions
 BEGIN SELECT RAISE(ABORT, 'worker recovery enqueue outage'); END`)
	require.NoError(t, err)

	require.NoError(t, ds.workflowSvc.Create(ctx, "history", layoutE2EWorkflow("history", "Original captured graph")))
	original := layoutE2EManifest(t, env.privatePath("history"))
	require.NoError(t, ds.workflowSvc.Update(ctx, "history", layoutE2EWorkflow("history", "Current captured graph")))
	current := layoutE2EManifest(t, env.privatePath("history"))
	require.NotEqual(t, original.SavedRevision(), current.SavedRevision())
	require.NoError(t, ds.workflowSvc.Create(ctx, "deleted", layoutE2EWorkflow("deleted", "Deleted captured graph")))
	deleted := layoutE2EManifest(t, env.privatePath("deleted"))
	require.NoError(t, ds.workflowSvc.Delete(ctx, "deleted"))

	// A visible shared candidate without its exact publication proof is not a
	// committed save. Use the actual stamper/tree protocol, not a source fake.
	var phantom workflow.ArrangementRequest
	require.NoError(t, ds.layout.mutations.WithMutations(ctx, []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "phantom"}}, func(owned context.Context) error {
		wf := layoutE2EWorkflow("phantom", "Uncommitted candidate")
		var err error
		phantom, err = ds.layout.stamper.Stamp(owned, workflow.Shared(), "phantom", wf, nil, workflow.SaveReasonPublish)
		if err != nil {
			return err
		}
		if _, err = ds.workflowRepo.(workflow.TreeCommitter).PrepareTreeCommit(owned, workflow.MutationTarget{Scope: workflow.Shared(), Name: "phantom"}, phantom.Revision); err != nil {
			return err
		}
		return ds.workflowRepo.Create(owned, workflow.Shared(), "phantom", wf)
	}))

	entries, err := ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	for _, wf := range []*workflow.Workflow{original, current, deleted} {
		_, err := ds.layout.store.Get(ctx, wf.SavedRevision())
		require.ErrorIs(t, err, layout.ErrRecordNotFound)
	}
	_, err = ds.db.Exec("DROP TRIGGER worker_recovery_outage")
	require.NoError(t, err)

	// Do not call Recover, Notify or Drain and do not restart/resave. Only the
	// already-running worker's periodic recovery can create these missing rows.
	for _, wf := range []*workflow.Workflow{original, current, deleted} {
		row := layoutRenderWaitTerminal(t, ds, wf.SavedRevision(), time.Minute)
		require.Equal(t, layout.StatePassed, row.State, "failure %s: %s; findings %+v", row.FailureStage, row.FailureReason, row.Findings)
		require.Equal(t, 3, row.AttemptLimit)
		require.Equal(t, int64(1), row.JobGeneration)
		require.Equal(t, wf.Presentation.Retained, row.Retained)
		require.Equal(t, wf.Presentation.Reason, row.Reason)
		require.True(t, wf.Presentation.SavedAt.Equal(row.RequestedAt))
		require.Equal(t, layout.Destination{Scope: workflow.Private(localDefaultUser), Name: wf.Name, WorkflowID: wf.ID}, row.Destination)
		saved, err := json.Marshal(wf)
		require.NoError(t, err)
		require.JSONEq(t, string(saved), string(row.Manifest))
		result, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
		require.NoError(t, err)
		graph, err := layout.BuildGraph(wf)
		require.NoError(t, err)
		require.NoError(t, result.Geometry.Validate(graph))
		require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
		// Only the destination's current revision keeps its capture: a
		// superseded or deleted revision's evidence is released (Must 5), while
		// its stored review still validates.
		if wf == current {
			assertEvidenceOnDisk(t, env, result.Review.Evidence)
		} else {
			assertEvidenceReleased(t, result.Review.Evidence)
		}
		view := ds.layout.reader.View(ctx, wf)
		require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
		require.NotNil(t, view.Geometry)
		require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
	}
	require.Eventually(t, func() bool {
		entry, ok, err := ds.layout.cache.ReadCache(ctx, workflow.Private(localDefaultUser), "history")
		return err == nil && ok && entry.Revision == current.SavedRevision()
	}, time.Minute, 50*time.Millisecond)
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "history"))
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "deleted"))
	require.Equal(t, 0, layoutE2ECountRows(t, ds.db, "phantom"))
	require.NoDirExists(t, filepath.Dir(env.privatePath("deleted")), "history must not recreate a deleted destination")
	require.NoDirExists(t, filepath.Dir(env.sharedPath("phantom")), "unrecorded publication must be rolled back")
	quarantined, err := os.ReadDir(filepath.Join(env.workspaceDir, ".arbitron-layout-journal", "quarantine"))
	require.NoError(t, err)
	require.NotEmpty(t, quarantined)
	entries, err = ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	require.Empty(t, entries)
}
