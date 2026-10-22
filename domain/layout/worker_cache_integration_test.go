package layout_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

type fencedHookCache struct {
	layout.CachePublisher
	beforeFence func()
}

func (c fencedHookCache) PublishCache(ctx context.Context, d layout.Destination, e layout.CacheEntry, f func(context.Context) error) error {
	return c.CachePublisher.PublishCache(ctx, d, e, func(owned context.Context) error { c.beforeFence(); return f(owned) })
}
func TestWorkerCacheCrossProcessFence(t *testing.T) {
	ctx := context.Background()
	if path := os.Getenv("ARBITRON_WORKER_CAS_DB"); path != "" {
		db, err := adaptsqlite.Open(path)
		require.NoError(t, err)
		defer db.Close()
		store := adaptsqlite.NewLayoutRepo(db)
		rev := os.Getenv("ARBITRON_WORKER_CAS_REV")
		_, err = store.Retry(ctx, rev, 1, time.Now())
		require.NoError(t, err)
		row, err := store.Claim(ctx, rev, "other-process", time.Now(), time.Now().Add(time.Minute))
		require.NoError(t, err)
		require.NoError(t, store.Complete(ctx, layout.Fence{Revision: rev, Generation: row.JobGeneration}, layout.Completion{State: layout.StateFailed}, time.Now()))
		return
	}
	r := newWorkerRig(t, 1)
	req := r.input(t)
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	journal := filesystem.NewRevisionJournal(base)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(req.Manifest, &wf))
	require.NoError(t, repo.Create(ctx, req.Scope, req.Name, &wf))
	path := filepath.Join(t.TempDir(), "layout.sqlite")
	db, err := adaptsqlite.Open(path)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, adaptsqlite.RunMigrations(db))
	store := adaptsqlite.NewLayoutRepo(db)
	requester := layout.NewRequester(store, 1, nil)
	require.NoError(t, requester.RequestArrangement(ctx, req))
	reviewer := layouttest.NewFakeReviewer(layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionRenderMismatch, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: "unrefinable"}}})
	co, err := layout.NewCoordinator(r.renderer, reviewer)
	require.NoError(t, err)
	realCache := filesystem.NewLayoutCache(repo)
	hook := fencedHookCache{CachePublisher: realCache, beforeFence: func() {
		exe, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWorkerCacheCrossProcessFence$", "-test.count=1")
		cmd.Env = append(os.Environ(), "ARBITRON_WORKER_CAS_DB="+path, "ARBITRON_WORKER_CAS_REV="+req.Revision)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}}
	cfg := r.cfg
	cfg.Store = store
	cfg.Requester = requester
	cfg.Coordinator = co
	cfg.Journal = journal
	cfg.Manifests = layout.NewRepositoryManifests(repo)
	cfg.Cache = hook
	worker, err := layout.NewWorker(cfg)
	require.NoError(t, err)
	defer worker.Stop()
	require.ErrorIs(t, worker.ProcessRevision(ctx, req.Revision), layout.ErrStaleGeneration)
	row, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	require.Equal(t, int64(2), row.JobGeneration)
	require.Equal(t, int64(1), row.ResultGeneration)
	require.Zero(t, row.CacheGeneration)
	_, ok, err := realCache.ReadCache(ctx, req.Scope, req.Name)
	require.NoError(t, err)
	require.False(t, ok, "late process must not leave bytes at the filesystem sink")
	cfg.Cache = realCache
	current, err := layout.NewWorker(cfg)
	require.NoError(t, err)
	defer current.Stop()
	require.NoError(t, current.ReconcileCache(ctx))
	entry, ok, err := realCache.ReadCache(ctx, req.Scope, req.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(2), entry.JobGeneration)
	require.Equal(t, int64(1), entry.Generation)
	require.NoError(t, repo.Delete(ctx, req.Scope, req.Name))
	require.NoError(t, current.ReconcileCache(ctx))
	exists, err := repo.Exists(ctx, req.Scope, req.Name)
	require.NoError(t, err)
	require.False(t, exists, "cache reconciliation cannot recreate a deleted directory")
}
