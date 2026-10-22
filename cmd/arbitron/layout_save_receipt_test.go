package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Keep Record and receipt I/O real: a directory at the receipt path makes
// MarkCommitted fail after the manifest (and shared publication) has committed.
type authoringReceiptObstruction struct {
	workflow.RevisionJournal
	dir string
}

func (j authoringReceiptObstruction) MarkCommitted(ctx context.Context, revision string) error {
	if err := os.Mkdir(filepath.Join(j.dir, revision+".committed"), 0700); err != nil {
		return err
	}
	return j.RevisionJournal.MarkCommitted(ctx, revision)
}

// Losing the prepared input, allowing an overwrite before settlement, or
// replaying the current manifest instead of the captured one breaks this test.
func TestLayoutSaveReceiptFailureSurvivesOverwriteAndDelete(t *testing.T) {
	for _, operation := range []string{"create", "private-update", "shared-update"} {
		t.Run(operation, func(t *testing.T) {
			db := layoutE2EOpenDB(t)
			root := t.TempDir()
			repo := filesystem.NewWorkflowRepo(root)
			pubs := adaptsqlite.NewPublishedWorkflowRepo(db)
			registry := adaptsqlite.NewWorkflowIdentityRepo(db)
			ls, err := newLayoutStack(layoutE2EConfig(root, 3), db, repo, pubs, registry, layoutE2EDiscardLogger())
			require.NoError(t, err)
			t.Cleanup(ls.stop)
			// Keep background polling stopped so every recovery boundary is observable.
			svc := workflow.NewWorkflowService(repo, pubs)
			svc.SetRegistry(registry)
			svc.SetActiveInstanceCounter(adaptsqlite.NewInstanceRepo(db))
			svc.SetMutationCoordinator(ls.mutations)
			svc.SetRevisionStamper(ls.stamper)
			ctx := adminCtxForLocalUser(t, nil)
			name, scope := "receipt", workflow.Private(localDefaultUser)
			if operation != "create" {
				require.NoError(t, svc.Create(ctx, name, layoutE2EWorkflow(name, "Prior graph")))
			}
			if operation == "shared-update" {
				require.NoError(t, svc.Publish(ctx, name, "shared-receipt", workflow.VisibilityPublic, ""))
				name, scope = "shared-receipt", workflow.Shared()
			}
			baseline := layoutE2ECountRows(t, db, name)
			journalDir := filepath.Join(root, ".arbitron-layout-journal")
			stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{
				Journal: authoringReceiptObstruction{ls.journal, journalDir}, Requester: ls.requester,
				Settler: ls.commits, Displayed: ls.store, Logger: layoutE2EDiscardLogger(),
			})
			require.NoError(t, err)
			svc.SetRevisionStamper(stamper)
			input := layoutE2EWorkflow(name, "Captured during receipt outage")
			if operation == "create" {
				err = svc.Create(ctx, name, input)
			} else {
				err = svc.Update(ctx, name, input)
			}
			// Characterize the current manifest-commit success policy for R2.2.
			require.NoError(t, err)
			revision := input.SavedRevision()
			require.NotEmpty(t, revision)
			entries, err := ls.journal.Entries(ctx)
			require.Error(t, err, "receipt I/O is still unavailable")
			require.Empty(t, entries, "undecided input cannot be exposed as committed")
			preparedPath := filepath.Join(journalDir, revision+".json")
			preparedBytes, err := os.ReadFile(preparedPath)
			require.NoError(t, err)
			var captured workflow.ArrangementRequest
			require.NoError(t, json.Unmarshal(preparedBytes, &captured))
			manifest, err := json.Marshal(input)
			require.NoError(t, err)
			require.Equal(t, manifest, captured.Manifest)
			require.Equal(t, scope, captured.Scope)
			require.Equal(t, name, captured.Name)
			_, err = ls.store.Get(ctx, revision)
			require.ErrorIs(t, err, layout.ErrRecordNotFound)
			_, err = repo.Load(ctx, scope, name)
			require.Error(t, err, "settled reads must fail closed until receipt repair")
			require.Error(t, svc.Update(ctx, name, layoutE2EWorkflow(name, "Must not overwrite")))
			after, err := os.ReadFile(preparedPath)
			require.NoError(t, err)
			require.Equal(t, preparedBytes, after)
			require.Equal(t, baseline, layoutE2ECountRows(t, db, name))
			trees, err := repo.PendingTreeCommits(ctx)
			require.NoError(t, err)
			if operation == "shared-update" {
				require.Len(t, trees, 1, "receipt failure must retain the prior shared tree")
				proof, ok, err := pubs.PublicationOf(ctx, name)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, revision, proof.Revision)
				require.Equal(t, input.ID, proof.WorkflowID)
			} else {
				require.Empty(t, trees)
			}

			require.NoError(t, os.Remove(filepath.Join(journalDir, revision+".committed")))
			svc.SetRevisionStamper(ls.stamper)
			// The next save must receipt the old input before replacing its proof.
			require.NoError(t, svc.Update(ctx, name, layoutE2EWorkflow(name, "Newer graph")))
			entries, err = ls.journal.Entries(ctx)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, workflow.JournalCommitted, entries[0].State)
			require.Equal(t, captured, entries[0].Request)
			require.NoError(t, svc.Delete(ctx, name))
			for replay := 0; replay < 2; replay++ {
				require.NoError(t, ls.worker.Recover(ctx))
			}
			row, err := ls.store.Get(ctx, revision)
			require.NoError(t, err)
			require.Equal(t, captured.Manifest, row.Manifest)
			require.Equal(t, captured.Retained, row.Retained)
			require.Equal(t, captured.RequestedAt, row.RequestedAt)
			require.Equal(t, captured.Reason, row.Reason)
			require.Equal(t, layout.Destination{Scope: scope, Name: name, WorkflowID: input.ID}, row.Destination)
			require.Equal(t, int64(1), row.JobGeneration)
			require.Equal(t, baseline+2, layoutE2ECountRows(t, db, name))
			entries, err = ls.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries)
			trees, err = repo.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, trees)
		})
	}
}
