package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This is a save-side provenance check, not rendered acceptance: the production
// worker may report the not-yet-installed renderer, but must capture the save.
func TestRevisionMetadataE2E_CreateRejectsClientPresentation(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	clientRevision := uuid.Must(uuid.NewV7()).String()
	client := &workflow.Presentation{
		Revision: clientRevision, SavedAt: time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC),
		Reason: workflow.SaveReasonSeed, Scope: workflow.ScopeLabelShared, Name: "foreign",
		Retained: workflow.RetainedLayout{
			Source: workflow.RetainedFromLayout, FromRevision: clientRevision, FromGeneration: 99,
			Positions: map[string]workflow.NodePosition{"t1": {X: -999, Y: -888}, "foreign": {X: 1, Y: 2}},
		},
	}
	input := layoutE2EWorkflow("untrusted-name", "Original")
	input.Presentation = client
	before := time.Now().UTC()
	require.NoError(t, ds.workflowSvc.Create(ctx, "metadata", input))
	after := time.Now().UTC()

	assertCaptured := func(reason workflow.SaveReason, count int) *workflow.Workflow {
		t.Helper()
		saved := layoutE2EManifest(t, e.privatePath("metadata"))
		require.NotNil(t, saved.Presentation)
		p := saved.Presentation
		token, err := uuid.Parse(p.Revision)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), token.Version())
		require.NotEqual(t, clientRevision, p.Revision)
		require.Equal(t, workflow.ScopeLabelPrivate, p.Scope)
		require.Equal(t, localDefaultUser, p.OwnerID)
		require.Equal(t, "metadata", p.Name)
		require.Equal(t, reason, p.Reason)
		require.Equal(t, workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: layoutE2EPositions}, p.Retained)
		rec := layoutE2EWaitTerminal(t, ds, p.Revision)
		require.Equal(t, workflow.Private(localDefaultUser), rec.Destination.Scope)
		require.Equal(t, saved.ID, rec.Destination.WorkflowID)
		require.Equal(t, "metadata", rec.Destination.Name)
		require.Equal(t, reason, rec.Reason)
		require.Equal(t, p.SavedAt, rec.RequestedAt)
		require.Equal(t, p.Retained, rec.Retained)
		require.Equal(t, 3, rec.AttemptLimit)
		captured, err := json.Marshal(saved)
		require.NoError(t, err)
		require.Equal(t, captured, rec.Manifest, "SQLite must retain the exact stamper input, not just the revision")
		require.Equal(t, count, layoutE2ECountRows(t, ds.db, "metadata"))
		return saved
	}
	first := assertCaptured(workflow.SaveReasonCreate, 1)
	require.WithinRange(t, first.Presentation.SavedAt, before, after)
	require.Equal(t, clientRevision, client.Revision, "stamping replaces rather than mutates client metadata")
	var phantom int
	require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions WHERE revision = ?", clientRevision).Scan(&phantom))
	require.Zero(t, phantom, "the client revision must never become a job")

	seen := map[string]bool{first.SavedRevision(): true}
	// Identical, A→B→A and position-only saves must each issue a fresh token.
	for i, label := range []string{"Original", "Changed", "Original", "Original"} {
		next := layoutE2EWorkflow("metadata", label)
		if i == 3 {
			next.Nodes[0].Position = &workflow.NodePosition{X: 987, Y: 654}
		}
		require.NoError(t, ds.workflowSvc.Update(ctx, "metadata", next))
		saved := assertCaptured(workflow.SaveReasonUpdate, i+2)
		require.False(t, seen[saved.SavedRevision()], "every authoring save gets a unique revision")
		seen[saved.SavedRevision()] = true
		if i == 3 {
			require.Equal(t, next.Nodes[0].Position, saved.Nodes[0].Position)
		}
	}
	entries, err := ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	require.Empty(t, entries, "acknowledged rows permit journal cleanup")
}
