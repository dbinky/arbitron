package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// A visible receipt left by a failed directory sync is not a durable acknowledgement.
func TestRevisionJournal_MarkCommittedRetriesDirectorySync(t *testing.T) {
	ctx := context.Background()
	j := NewRevisionJournal(t.TempDir())
	req := journalRequest(t, "demo")
	require.NoError(t, j.Record(ctx, req))
	boom := errors.New("journal directory sync unavailable")
	j.files.sync = func(f *os.File) error {
		if f.Name() == j.dir {
			return boom
		}
		return f.Sync()
	}
	require.ErrorIs(t, j.MarkCommitted(ctx, req.Revision), boom)
	receipt := filepath.Join(j.dir, req.Revision+".committed")
	before, err := os.ReadFile(receipt)
	require.NoError(t, err, "the failure must follow the real receipt rename")
	require.ErrorIs(t, j.MarkCommitted(ctx, req.Revision), boom, "retry must establish durability even when the receipt exists")
	j.files.sync = nil
	require.NoError(t, j.MarkCommitted(ctx, req.Revision))
	after, err := os.ReadFile(receipt)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// Omitting the journal durability barrier, or treating an incomplete enumeration
// as absent input, would delete this real backup before a durable receipt exists.
func TestCommitSource_ReceiptSyncFailurePreservesBackup(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	repo := NewWorkflowRepo(filepath.Join(base, "workflows"))
	j := NewRevisionJournal(repo.basePath)
	db, err := adaptsqlite.Open(filepath.Join(base, "layout.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	store := adaptsqlite.NewLayoutRepo(db)
	requester := layout.NewRequester(store, 3, nil)
	req := journalRequest(t, "demo")
	require.NoError(t, repo.Create(ctx, req.Scope, req.Name, &workflow.Workflow{Name: req.Name, Description: "prior bytes"}))
	dir, err := repo.resolveWorkflowDir(req.Scope, req.Name)
	require.NoError(t, err)
	prior, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
	require.NoError(t, err)
	require.NoError(t, j.Record(ctx, req))
	tree, err := repo.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: req.Scope, Name: req.Name}, req.Revision)
	require.NoError(t, err)
	require.NoError(t, repo.files.write(ctx, filepath.Join(dir, "workflow.json"), req.Manifest, 0600))
	input, err := os.ReadFile(filepath.Join(j.dir, req.Revision+".json"))
	require.NoError(t, err)
	boom := errors.New("journal directory sync unavailable")
	failDirectorySync := func(f *os.File) error {
		if f.Name() == j.dir {
			return boom
		}
		return f.Sync()
	}
	j.files.sync = failDirectorySync
	require.ErrorIs(t, j.MarkCommitted(ctx, req.Revision), boom)
	require.FileExists(t, filepath.Join(j.dir, req.Revision+".committed"))
	// Reopen to rule out in-memory receipt state; the disk outage persists.
	j = NewRevisionJournal(repo.basePath)
	j.files.sync = failDirectorySync
	source, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Journal: j, Repo: repo, Mutations: repo, Trees: repo, Relocator: requester})
	require.NoError(t, err)
	repo.SetReadSettler(source)
	requests, recoveryErr := source.CommittedRequests(ctx)
	pending, err := repo.PendingTreeCommits(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.TreeCommit{tree}, pending, "failed receipt sync must retain the rollback intent")
	require.ErrorIs(t, recoveryErr, boom)
	require.Empty(t, requests, "an unsynced receipt must not reach arrangement")
	loaded, err := repo.Load(ctx, req.Scope, req.Name)
	require.ErrorIs(t, err, boom)
	require.Nil(t, loaded, "readers must wait for receipt durability")
	backup, err := os.ReadFile(filepath.Join(repo.treeRoot(), tree.ID, "backup", "workflow.json"))
	require.NoError(t, err)
	require.Equal(t, prior, backup)
	retained, err := os.ReadFile(filepath.Join(j.dir, req.Revision+".json"))
	require.NoError(t, err)
	require.Equal(t, input, retained)
	require.NoDirExists(t, filepath.Join(j.dir, "quarantine", req.Revision))
	_, err = store.Get(ctx, req.Revision)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)

	// Once storage recovers, establish durability before cleanup and replay.
	j.files.sync = nil
	requests, err = source.CommittedRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.ArrangementRequest{req}, requests)
	pending, err = repo.PendingTreeCommits(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	loaded, err = repo.Load(ctx, req.Scope, req.Name)
	require.NoError(t, err)
	require.Equal(t, req.Revision, loaded.SavedRevision())
	for _, request := range requests {
		require.NoError(t, requester.RequestArrangement(ctx, request))
	}
	row, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	require.Equal(t, req.Manifest, row.Manifest)
}
