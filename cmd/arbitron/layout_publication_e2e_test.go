package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Publication proof must describe the saved destination, not its source or an
// earlier revision, and a failed replacement must restore the entire old tree.
func TestLayoutPublicationE2E_ExactProofAndFailedReplacement(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "First publication")))
	require.NoError(t, ds.workflowRepo.WriteFile(ctx, workflow.Private(localDefaultUser), "source", "support.txt", []byte("original support")))
	source := layoutE2EManifest(t, e.privatePath("source"))
	require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "published", workflow.VisibilityDepartment, "engineering"))

	assertProof := func() workflow.Publication {
		t.Helper()
		saved := layoutE2EManifest(t, e.sharedPath("published"))
		proof, ok, err := ds.publishedRepo.PublicationOf(ctx, "published")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, saved.SavedRevision(), proof.Revision)
		require.Equal(t, saved.ID, proof.WorkflowID)
		require.Equal(t, localDefaultUser, proof.Publisher)
		require.Equal(t, "source", proof.SourceName)
		var revision, id string
		require.NoError(t, ds.db.QueryRowContext(ctx, "SELECT saved_revision, workflow_id FROM published_workflows WHERE name='published'").Scan(&revision, &id))
		require.Equal(t, proof.Revision, revision)
		require.Equal(t, proof.WorkflowID.String(), id)
		rec := layoutE2EWaitTerminal(t, ds, revision)
		layoutE2EAssertOutcome(t, rec)
		require.Equal(t, saved.ID, rec.Destination.WorkflowID)
		require.Equal(t, workflow.Shared(), rec.Destination.Scope)
		require.Equal(t, "published", rec.Destination.Name)
		var captured workflow.Workflow
		require.NoError(t, json.Unmarshal(rec.Manifest, &captured))
		require.Equal(t, *saved, captured, "the worker must consume the exact committed destination manifest")
		return proof
	}
	first := assertProof()
	require.NotEqual(t, source.SavedRevision(), first.Revision)
	require.NotEqual(t, source.ID, first.WorkflowID)
	require.Equal(t, workflow.VisibilityDepartment, first.Visibility)
	require.Equal(t, "engineering", first.Department)

	require.NoError(t, ds.workflowSvc.SetVisibility(ctx, "published", workflow.VisibilityPublic, ""))
	public := assertProof()
	want := first
	want.Visibility, want.Department = workflow.VisibilityPublic, ""
	require.Equal(t, want, public, "metadata changes must retain exact revision and identity proof")
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "published"))

	require.NoError(t, ds.workflowSvc.Update(ctx, "source", layoutE2EWorkflow("source", "Replacement publication")))
	require.NoError(t, ds.workflowRepo.WriteFile(ctx, workflow.Private(localDefaultUser), "source", "support.txt", []byte("replacement support")))
	oldBytes, err := os.ReadFile(e.sharedPath("published"))
	require.NoError(t, err)
	// Reject the actual SQLite proof update after the copied tree is visible.
	_, err = ds.db.ExecContext(ctx, `CREATE TRIGGER reject_publication BEFORE UPDATE ON published_workflows
	WHEN NEW.name='published' AND NEW.saved_revision <> OLD.saved_revision
	BEGIN SELECT RAISE(ABORT, 'publication proof unavailable'); END`)
	require.NoError(t, err)
	err = ds.workflowSvc.Publish(ctx, "source", "published", workflow.VisibilityPublic, "")
	require.ErrorContains(t, err, "publication proof unavailable")
	_, err = ds.db.ExecContext(ctx, "DROP TRIGGER reject_publication")
	require.NoError(t, err)
	afterBytes, err := os.ReadFile(e.sharedPath("published"))
	require.NoError(t, err)
	require.Equal(t, oldBytes, afterBytes)
	supportPath := filepath.Join(filepath.Dir(e.sharedPath("published")), "support.txt")
	support, err := os.ReadFile(supportPath)
	require.NoError(t, err)
	require.Equal(t, "original support", string(support))
	require.Equal(t, public, assertProof())
	require.NoError(t, ds.layout.worker.Recover(ctx))
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "published"), "a rejected proof must not enqueue a phantom revision")

	require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "published", workflow.VisibilityPublic, ""))
	next := assertProof()
	require.NotEqual(t, public.Revision, next.Revision)
	require.Equal(t, public.WorkflowID, next.WorkflowID)
	support, err = os.ReadFile(supportPath)
	require.NoError(t, err)
	require.Equal(t, "replacement support", string(support))
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "published"))
}
