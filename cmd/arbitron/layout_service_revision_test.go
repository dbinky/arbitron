package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Drive the root's actual cache with browser-produced, saved-revision evidence.
// Gate only the store fence, inside the cache lock, to order the competing removal.
func TestServiceRevisionE2E_DeleteUnpublishRaceCache(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	for _, operation := range []string{"delete", "unpublish"} {
		for _, order := range []string{"cache-first", "removal-first"} {
			t.Run(operation+"/"+order, func(t *testing.T) {
				name := operation + "-" + order
				path := e.privatePath(name)
				require.NoError(t, ds.workflowSvc.Create(ctx, name, layoutE2EWorkflow(name, "Removal/cache race")))
				remove := ds.workflowSvc.Delete
				if operation == "unpublish" {
					target := name + "-shared"
					require.NoError(t, ds.workflowSvc.Publish(ctx, name, target, workflow.VisibilityPublic, ""))
					name, path = target, e.sharedPath(target)
					remove = ds.workflowSvc.Unpublish
				}
				wf := layoutE2EManifest(t, path)
				row := layoutRenderWaitTerminal(t, ds, wf.SavedRevision(), time.Minute)
				require.Equal(t, layout.StatePassed, row.State, "real browser evidence is required: %s %+v", row.FailureReason, row.Findings)
				result, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
				require.NoError(t, err)
				require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
				var entry layout.CacheEntry
				require.Eventually(t, func() bool {
					var ok bool
					entry, ok, err = ds.layout.cache.ReadCache(ctx, row.Destination.Scope, name)
					return err == nil && ok && entry.Generation == row.ResultGeneration
				}, 5*time.Second, 10*time.Millisecond)
				require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(entry.Geometry))
				// Force a real write rather than merely observing the worker's earlier file.
				require.NoError(t, os.Remove(filepath.Join(filepath.Dir(path), filesystem.LayoutCacheFile)))
				raceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				var racing sync.WaitGroup
				defer func() { cancel(); racing.Wait() }()
				published := make(chan error, 1)
				fence := func(owned context.Context) error {
					current, err := ds.layout.store.Get(owned, row.Revision)
					if err != nil {
						return err
					}
					if current.Destination != row.Destination || current.JobGeneration != entry.JobGeneration || current.ResultGeneration != entry.Generation || !current.State.Terminal() {
						return layout.ErrStaleGeneration
					}
					return nil
				}
				if order == "cache-first" {
					entered, release := make(chan struct{}), make(chan struct{})
					var once sync.Once
					unblock := func() { once.Do(func() { close(release) }) }
					defer unblock()
					racing.Add(1)
					go func() {
						defer racing.Done()
						published <- ds.layout.cache.PublishCache(raceCtx, row.Destination, entry, func(owned context.Context) error {
							if err := fence(owned); err != nil {
								return err
							}
							close(entered)
							select {
							case <-release:
								return nil
							case <-owned.Done():
								return owned.Err()
							}
						})
					}()
					select {
					case <-entered:
					case <-raceCtx.Done():
						t.Fatal("saved geometry never reached cache fence")
					}
					removed := make(chan error, 1)
					started := make(chan struct{})
					racing.Add(1)
					go func() {
						defer racing.Done()
						close(started)
						removed <- remove(raceCtx, name)
					}()
					<-started
					var removalErr error
					removalFinished := false
					select {
					case removalErr = <-removed:
						removalFinished = true
					case <-time.After(100 * time.Millisecond):
					}
					unblock()
					require.NoError(t, <-published)
					if !removalFinished {
						removalErr = <-removed
					}
					require.NoError(t, removalErr)
				} else {
					require.NoError(t, ds.layout.mutations.WithMutations(raceCtx, []workflow.MutationTarget{{Scope: row.Destination.Scope, Name: name}}, func(owned context.Context) error {
						started := make(chan struct{})
						racing.Add(1)
						go func() {
							defer racing.Done()
							close(started)
							published <- ds.layout.cache.PublishCache(raceCtx, row.Destination, entry, fence)
						}()
						<-started
						select {
						case err := <-published:
							t.Fatalf("cache escaped the removal lock: %v", err)
						case <-time.After(100 * time.Millisecond):
						}
						return remove(owned, name)
					}))
					require.ErrorIs(t, <-published, layout.ErrDestinationGone)
				}
				require.NoDirExists(t, filepath.Dir(path), "cache must never resurrect a deleted tree")
				_, err = adaptsqlite.NewWorkflowIdentityRepo(ds.db).ResolveID(ctx, name)
				require.ErrorIs(t, err, workflow.ErrNotFound)
				_, ok, err := ds.publishedRepo.PublicationOf(ctx, name)
				require.NoError(t, err)
				require.False(t, ok)
				require.NoError(t, ds.layout.worker.ReconcileCache(ctx))
				require.NoDirExists(t, filepath.Dir(path))
				after, err := ds.layout.store.Get(ctx, row.Revision)
				require.NoError(t, err)
				require.Equal(t, row.Manifest, after.Manifest)
				require.Equal(t, row.ResultGeneration, after.ResultGeneration)
				require.Equal(t, 1, layoutE2ECountRows(t, ds.db, name), "removal/cache publication cannot author revisions")
				history, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
				require.NoError(t, err)
				require.Equal(t, result, history, "deletion preserves immutable reviewed history")
			})
		}
	}
}
