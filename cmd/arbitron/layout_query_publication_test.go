package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutQueryE2E_RetryFirstLaterSaveRejectsOldPublication(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "query-race"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Original saved graph")))
	wf := layoutE2EManifest(t, e.privatePath(name))
	first := layoutRenderWaitTerminal(t, ds, wf.SavedRevision(), time.Minute)
	require.Equal(t, layout.StatePassed, first.State)
	require.Eventually(t, func() bool {
		row, err := ds.layout.store.Get(ctx, first.Revision)
		return err == nil && row.CacheGeneration == row.ResultGeneration
	}, time.Minute, 10*time.Millisecond)
	ds.layout.worker.Stop()
	entry, ok, err := ds.layout.cache.ReadCache(ctx, first.Destination.Scope, name)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, entry.Geometry.Nodes, "publication must use actual captured geometry, never an empty entry")

	// A lost authoritative result makes the previously passed row retryable.
	// Keep its real published cache as the delayed old-worker input.
	_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision = ?", first.Revision)
	require.NoError(t, err)
	ack, err := ds.layout.query.RetryWorkflow(ctx, name, first.Revision)
	require.NoError(t, err)
	require.Equal(t, first.JobGeneration+1, ack.JobGeneration)
	fence := func(owned context.Context) error {
		current, err := ds.layout.store.Get(owned, first.Revision)
		if err != nil {
			return err
		}
		if current.JobGeneration != entry.JobGeneration || current.ResultGeneration != entry.Generation || current.Destination != first.Destination || !current.State.Terminal() {
			return layout.ErrStaleGeneration
		}
		return nil
	}
	cachePath := filepath.Join(filepath.Dir(e.privatePath(name)), filesystem.LayoutCacheFile)
	require.NoError(t, os.Remove(cachePath))
	assert.ErrorIs(t, ds.layout.cache.PublishCache(ctx, first.Destination, entry, fence), layout.ErrStaleGeneration)
	require.NoFileExists(t, cachePath, "a rejected old generation must not recreate a lost cache")

	// Retry linearized first; an independent process now saves and reviews a
	// newer revision. The pending historical job must remain independently queued.
	start, wait, _ := queryStartMutation(t, e, "update-reviewed")
	start()
	wait()
	saved := layoutE2EManifest(t, e.privatePath(name))
	require.NotEqual(t, first.Revision, saved.SavedRevision())
	manifestBytes, err := os.ReadFile(e.privatePath(name))
	require.NoError(t, err)
	cacheBytes, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	newer, err := ds.layout.store.Get(ctx, saved.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, layout.StatePassed, newer.State)
	currentCache, ok, err := ds.layout.cache.ReadCache(ctx, newer.Destination.Scope, name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, saved.SavedRevision(), currentCache.Revision)
	old, err := ds.layout.store.Get(ctx, first.Revision)
	require.NoError(t, err)
	assert.Equal(t, ack.JobGeneration, old.JobGeneration)
	assert.NotEqual(t, layout.StateSuperseded, old.State)
	assert.Equal(t, first.Manifest, old.Manifest)

	require.ErrorIs(t, ds.layout.cache.PublishCache(ctx, first.Destination, entry, fence), layout.ErrNotCurrent)
	manifestAfter, err := os.ReadFile(e.privatePath(name))
	require.NoError(t, err)
	cacheAfter, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.Equal(t, manifestBytes, manifestAfter)
	assert.Equal(t, cacheBytes, cacheAfter, "a late prior generation cannot replace the newer saved revision's geometry")
	view, err := ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.NotNil(t, view.Geometry)
	assert.Equal(t, newer.Revision, view.SavedRevision)
	assert.Equal(t, layout.GeometryDigest(currentCache.Geometry), layout.GeometryDigest(*view.Geometry))
}
