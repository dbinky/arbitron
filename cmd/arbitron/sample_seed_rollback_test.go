package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// A directory at the final file path makes the actual atomic supporting-file
// rename fail. Optionally obstruct ready.json too, exercising a real rollback
// I/O error without replacing either persistence implementation.
type seedFileObstruction struct {
	*filesystem.WorkflowRepo
	workspace     string
	breakRollback bool
	manifest      []byte
	intentPath    string
}

func (r *seedFileObstruction) WriteFile(ctx context.Context, scope workflow.Scope, name, rel string, content []byte) error {
	if rel == "z-last.txt" {
		var err error
		dir := filepath.Join(r.workspace, "shared", name)
		r.manifest, err = os.ReadFile(filepath.Join(dir, "workflow.json"))
		if err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(dir, rel), 0700); err != nil {
			return err
		}
	}
	return r.WorkflowRepo.WriteFile(ctx, scope, name, rel, content)
}

func (r *seedFileObstruction) RollbackTreeCommit(ctx context.Context, id string) error {
	if r.breakRollback {
		r.intentPath = filepath.Join(r.workspace, ".arbitron-tree-commits", id, "ready.json")
		if err := os.Rename(r.intentPath, r.intentPath+".saved"); err != nil {
			return err
		}
		if err := os.Mkdir(r.intentPath, 0700); err != nil {
			return err
		}
	}
	return r.WorkflowRepo.RollbackTreeCommit(ctx, id)
}

func TestSeedSampleWorkflows_DurableSupportingFileRollback(t *testing.T) {
	for _, brokenRollback := range []bool{false, true} {
		name := "successful rollback"
		if brokenRollback {
			name = "failed rollback retains evidence"
		}
		t.Run(name, func(t *testing.T) {
			e := durableSeedEnv(t)
			ds, _ := e.boot(t)
			repo := &seedFileObstruction{WorkflowRepo: ds.workflowRepo.(*filesystem.WorkflowRepo), workspace: e.workspaceDir, breakRollback: brokenRollback}
			err := seedWithRoot(t, e, ds, repo, ds.layout.journal, ds.publishedRepo)
			require.ErrorContains(t, err, "z-last.txt")
			var visible workflow.Workflow
			require.NoError(t, json.Unmarshal(repo.manifest, &visible), "fault occurs after real manifest visibility, not before saving")
			require.NotEmpty(t, visible.SavedRevision())
			ctx := context.Background()
			require.Equal(t, 0, layoutE2ECountRows(t, ds.db, durableSeedName))
			_, published, err := ds.publishedRepo.PublicationOf(ctx, durableSeedName)
			require.NoError(t, err)
			require.False(t, published)
			ledger, err := filesystem.NewSeedLedger(e.workspaceDir).Seeded(ctx)
			require.NoError(t, err)
			require.False(t, ledger[durableSeedName])
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
			if brokenRollback {
				require.Len(t, entries, 1)
				require.Equal(t, workflow.JournalPrepared, entries[0].State)
				require.Equal(t, visible.SavedRevision(), entries[0].Request.Revision)
				require.JSONEq(t, string(repo.manifest), string(entries[0].Request.Manifest))
				identity, err := registry.Resolve(ctx, durableSeedName)
				require.NoError(t, err)
				require.Equal(t, visible.ID, identity.ID)
				requests, err := ds.layout.commits.CommittedRequests(ctx)
				require.Error(t, err, "undecidable rollback must not turn visible bytes into a commit")
				require.Empty(t, requests)
				after, err := os.ReadFile(e.sharedPath(durableSeedName))
				require.NoError(t, err)
				require.Equal(t, repo.manifest, after)
				support, err := os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath(durableSeedName)), "support.txt"))
				require.NoError(t, err)
				require.Equal(t, "real sample supporting content", string(support))
				require.NoError(t, os.Remove(repo.intentPath))
				require.NoError(t, os.Rename(repo.intentPath+".saved", repo.intentPath))
				require.NoError(t, ds.layout.worker.Recover(ctx))
			} else {
				require.Empty(t, entries)
				_, err := registry.Resolve(ctx, durableSeedName)
				require.ErrorIs(t, err, workflow.ErrNotFound)
			}
			require.NoFileExists(t, e.sharedPath(durableSeedName))
			require.NoDirExists(t, filepath.Dir(e.sharedPath(durableSeedName)))
			require.Equal(t, 0, layoutE2ECountRows(t, ds.db, durableSeedName))
			entries, err = ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries)
			trees, err := ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, trees)
		})
	}
}
