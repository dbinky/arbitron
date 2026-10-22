package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Paired seam 11: relocation/delete versus the worker's cache tail, through the
// root's real SQLite store, mutation lock and on-disk cache. A late publication
// still holding the pre-move locator must neither write at the old name nor
// settle the relocated row; the relocated destination must still get its cache.
func TestLayoutE2E_RelocationLatePublishTargetsMovedDestination(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	// Argument-bound unit evidence isolates the cache/Store lifecycle, not visual acceptance.
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return layouttest.NewFakeRenderer(), layouttest.NewFakeReviewer(), nil, nil
	}
	for _, operation := range []string{"transfer", "rename"} {
		t.Run(operation, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			ds, _ := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			name := operation + "-cache"
			require.NoError(t, ds.workflowSvc.Create(ctx, name, layoutE2EWorkflow(name, "Relocated cache input")))
			oldPath := e.privatePath(name)
			rev := layoutE2ESavedRevision(t, oldPath)
			var before layout.Record
			require.Eventually(t, func() bool {
				var err error
				before, err = ds.layout.store.Get(ctx, rev)
				return err == nil && before.State == layout.StatePassed && before.CacheGeneration == before.ResultGeneration
			}, 5*time.Second, 10*time.Millisecond, "the pre-move result must be published and settled")
			entry, ok, err := ds.layout.cache.ReadCache(ctx, before.Destination.Scope, name)
			require.NoError(t, err)
			require.True(t, ok)
			oldDest, fence := before.Destination, layout.Fence{Revision: rev, Generation: before.JobGeneration}

			newPath := e.privatePath(name + "-renamed")
			if operation == "transfer" {
				owner := uuid.New()
				require.NoError(t, ds.workflowSvc.TransferOwnership(ctx, name, owner))
				newPath = filepath.Join(e.workspaceDir, "users", owner.String(), name, "workflow.json")
			} else {
				require.NoError(t, ds.workflowSvc.Rename(ctx, name, name+"-renamed"))
			}
			require.NoDirExists(t, filepath.Dir(oldPath))
			moved, err := ds.layout.store.Get(ctx, rev)
			require.NoError(t, err)
			require.NotEqual(t, oldDest, moved.Destination, "Relocate must move the row's locator")
			require.Equal(t, before.ResultGeneration, moved.ResultGeneration)

			// What Worker.publish does after its unlocked reads saw the old locator:
			// PublishCache under the old lock, its store fence, then MarkCached.
			workerFence := func(owned context.Context) error {
				current, err := ds.layout.store.Get(owned, rev)
				if err != nil {
					return err
				}
				if current.JobGeneration != fence.Generation || current.ResultGeneration != entry.Generation || current.Destination != oldDest || !current.State.Terminal() {
					return layout.ErrStaleGeneration
				}
				return nil
			}
			require.ErrorIs(t, ds.layout.cache.PublishCache(ctx, oldDest, entry, workerFence), layout.ErrDestinationGone)
			require.NoDirExists(t, filepath.Dir(oldPath), "a late publish cannot recreate the moved-away folder")
			require.ErrorIs(t, ds.layout.store.MarkCached(ctx, fence, entry.Generation, oldDest), layout.ErrStaleGeneration,
				"a late settle at the old locator must not swallow the relocated row's pending publication")

			if operation == "transfer" {
				// The tree copy carried layout.json verbatim; remove it so only the worker can supply it.
				require.NoError(t, os.Remove(filepath.Join(filepath.Dir(newPath), filesystem.LayoutCacheFile)))
			}
			// The same reconcileCache path the dispatcher sweeps on every poll.
			require.NoError(t, ds.layout.worker.ReconcileCache(ctx))
			settled, err := ds.layout.store.Get(ctx, rev)
			require.NoError(t, err)
			require.Equal(t, moved.Destination, settled.Destination)
			require.Equal(t, settled.ResultGeneration, settled.CacheGeneration, "the relocated row settles at its new destination")
			current := layoutE2EManifest(t, newPath)
			if operation == "transfer" {
				published, ok, err := ds.layout.cache.ReadCache(ctx, moved.Destination.Scope, name)
				require.NoError(t, err)
				require.True(t, ok, "the relocated destination must get its cache")
				require.Equal(t, rev, published.Revision)
				require.Equal(t, current.SavedRevision(), published.Revision)
				return
			}

			// Rename stamps a new revision; recreating the old name stamps another.
			// The old revision's late publish must not land on either.
			require.NotEqual(t, rev, current.SavedRevision())
			layoutE2EWaitTerminal(t, ds, current.SavedRevision())
			require.NoError(t, ds.workflowSvc.Create(ctx, name, layoutE2EWorkflow(name, "Recreated after rename")))
			recreated := layoutE2EManifest(t, oldPath)
			require.NotEqual(t, rev, recreated.SavedRevision())
			require.NotEqual(t, before.Destination.WorkflowID, recreated.ID)
			require.ErrorIs(t, ds.layout.cache.PublishCache(ctx, oldDest, entry, workerFence), layout.ErrNotCurrent)
			layoutE2EWaitTerminal(t, ds, recreated.SavedRevision())
			require.NoError(t, ds.layout.worker.ReconcileCache(ctx))
			for path, want := range map[string]string{oldPath: recreated.SavedRevision(), newPath: current.SavedRevision()} {
				data, err := os.ReadFile(filepath.Join(filepath.Dir(path), filesystem.LayoutCacheFile))
				require.NoError(t, err)
				require.NotContains(t, string(data), `"revision":"`+rev+`"`, "old revision's cache leaked into %s", path)
				require.Contains(t, string(data), `"revision":"`+want+`"`)
			}
		})
	}
}
