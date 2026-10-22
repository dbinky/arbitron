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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// An already-running peer consumes the real prepare left by a process killed
// immediately after Register, before any tree intent. Foreign authority and
// failed compensation must not be mistaken for permission to delete by name.
func TestSeedSampleWorkflows_PostRegisterClaimControls(t *testing.T) {
	for _, boundary := range []string{"foreign-id", "private-scope", "shared-owner", "surviving-tree", "surviving-publication", "registry-delete-error"} {
		t.Run(boundary, func(t *testing.T) {
			e := durableSeedEnv(t)
			ds, _ := e.boot(t)
			ds.layout.worker.Stop()
			req := killSeedAt(t, e, "identity")
			ctx := context.Background()
			registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
			claim, err := registry.Resolve(ctx, durableSeedName)
			require.NoError(t, err)
			require.Equal(t, workflow.RegistryEntry{ID: req.WorkflowID, Name: req.Name, Scope: workflow.ScopeShared}, claim)
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Equal(t, []workflow.JournalEntry{{Request: req, State: workflow.JournalPrepared}}, entries)
			trees, err := ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, trees)
			require.NoDirExists(t, filepath.Dir(e.sharedPath(durableSeedName)))

			var treeBytes, supportBytes []byte
			var proof workflow.Publication
			switch boundary {
			case "foreign-id", "private-scope", "shared-owner":
				require.NoError(t, registry.Delete(ctx, claim.ID))
				switch boundary {
				case "foreign-id":
					claim.ID = uuid.New()
				case "private-scope":
					claim.Scope, claim.OwnerID = workflow.ScopePrivate, localDefaultUser
				case "shared-owner":
					claim.OwnerID = localDefaultUser
				}
				require.NoError(t, registry.Register(ctx, claim))
			case "surviving-tree":
				wf := &workflow.Workflow{ID: uuid.New(), Name: durableSeedName, Description: "unrelated existing workflow"}
				require.NoError(t, ds.workflowRepo.Create(ctx, workflow.Shared(), durableSeedName, wf))
				require.NoError(t, ds.workflowRepo.WriteFile(ctx, workflow.Shared(), durableSeedName, "support.txt", []byte("unrelated supporting bytes")))
				treeBytes, err = os.ReadFile(e.sharedPath(durableSeedName))
				require.NoError(t, err)
				supportBytes, err = os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath(durableSeedName)), "support.txt"))
				require.NoError(t, err)
			case "surviving-publication":
				proof = workflow.Publication{Publisher: localDefaultUser, SourceName: "another-source", Visibility: workflow.VisibilityPublic, WorkflowID: claim.ID, Revision: uuid.Must(uuid.NewV7()).String()}
				require.NoError(t, ds.publishedRepo.CommitPublication(ctx, durableSeedName, proof))
			case "registry-delete-error":
				_, err = ds.db.Exec(`CREATE TRIGGER seed_claim_delete_outage BEFORE DELETE ON workflow_identity BEGIN SELECT RAISE(ABORT, 'seed claim delete unavailable'); END`)
				require.NoError(t, err)
			}
			requests, settleErr := ds.layout.commits.CommittedRequests(ctx)
			require.Empty(t, requests, "a prepared attempt is never a committed job")
			got, err := registry.Resolve(ctx, durableSeedName)
			require.NoError(t, err)
			require.Equal(t, claim, got)
			if boundary == "registry-delete-error" {
				require.ErrorContains(t, settleErr, "seed claim delete unavailable")
				entries, err = ds.layout.journal.Entries(ctx)
				require.NoError(t, err)
				require.Equal(t, []workflow.JournalEntry{{Request: req, State: workflow.JournalPrepared}}, entries, "failed compensation must retain its exact input")
				_, err = ds.db.Exec("DROP TRIGGER seed_claim_delete_outage")
				require.NoError(t, err)
				requests, err = ds.layout.commits.CommittedRequests(ctx)
				require.NoError(t, err)
				require.Empty(t, requests)
				_, err = registry.Resolve(ctx, durableSeedName)
				require.ErrorIs(t, err, workflow.ErrNotFound)
				require.NoError(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo))
				wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
				require.NotEqual(t, req.Revision, wf.SavedRevision())
				require.NotEqual(t, req.WorkflowID, wf.ID)
				row, err := ds.layout.store.Get(ctx, wf.SavedRevision())
				require.NoError(t, err)
				var captured workflow.Workflow
				require.NoError(t, json.Unmarshal(row.Manifest, &captured))
				require.Equal(t, *wf, captured)
				source := layoutE2EManifest(t, filepath.Join(e.samplesDir, durableSeedName, "workflow.json"))
				require.Equal(t, source.Nodes, captured.Nodes)
				require.Equal(t, source.Edges, captured.Edges)
				require.Equal(t, layout.Destination{Scope: workflow.Shared(), Name: durableSeedName, WorkflowID: wf.ID}, row.Destination)
				require.Equal(t, workflow.SaveReasonSeed, row.Reason)
				require.Equal(t, wf.Presentation.SavedAt, row.RequestedAt)
				require.Equal(t, wf.Presentation.Retained, row.Retained)
				require.Equal(t, 3, row.AttemptLimit)
				ledger, err := filesystem.NewSeedLedger(e.workspaceDir).Seeded(ctx)
				require.NoError(t, err)
				require.True(t, ledger[durableSeedName])
				require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
			} else {
				require.NoError(t, settleErr)
				require.Error(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo))
				require.Equal(t, 0, layoutE2ECountRows(t, ds.db, durableSeedName))
				ledger, err := filesystem.NewSeedLedger(e.workspaceDir).Seeded(ctx)
				require.NoError(t, err)
				require.False(t, ledger[durableSeedName])
				if treeBytes != nil {
					after, err := os.ReadFile(e.sharedPath(durableSeedName))
					require.NoError(t, err)
					require.Equal(t, treeBytes, after)
					after, err = os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath(durableSeedName)), "support.txt"))
					require.NoError(t, err)
					require.Equal(t, supportBytes, after)
				} else {
					require.NoDirExists(t, filepath.Dir(e.sharedPath(durableSeedName)))
				}
				if boundary == "surviving-publication" {
					after, present, err := ds.publishedRepo.PublicationOf(ctx, durableSeedName)
					require.NoError(t, err)
					require.True(t, present)
					require.Equal(t, proof, after)
				}
				got, err = registry.Resolve(ctx, durableSeedName)
				require.NoError(t, err)
				require.Equal(t, claim, got)
			}
			_, err = ds.layout.store.Get(ctx, req.Revision)
			require.ErrorIs(t, err, layout.ErrRecordNotFound)
		})
	}
}
