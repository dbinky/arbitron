package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layoutCopyGate renders immediately until armed, then holds every revision
// until release, so a fresh copy's own job cannot replace the copied cache.
type layoutCopyGate struct {
	layout.Renderer
	mu      sync.Mutex
	armed   bool
	held    chan string
	release chan struct{}
}

func (g *layoutCopyGate) arm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = true
}
func (g *layoutCopyGate) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	g.mu.Lock()
	hold := g.armed
	g.mu.Unlock()
	if hold {
		select {
		case g.held <- req.Revision:
		default:
		}
		select {
		case <-g.release:
		case <-ctx.Done():
			return layout.Rendering{}, ctx.Err()
		}
	}
	return g.Renderer.Render(ctx, req)
}

// Paired seam 12: Publish/Copy carries layout.json verbatim (copyDirRecursive)
// into a destination stamped with a fresh revision. During a status-store
// outage the destination read must reject the source token and stay
// unavailable/unreviewed, while the source still serves its own cache. Once
// the store returns, the worker replaces the foreign cache with the
// destination's own and leaves the source cache untouched.
func TestLayoutE2E_CopiedCacheNeverServesDestinationDuringOutage(t *testing.T) {
	gate := &layoutCopyGate{Renderer: layouttest.NewFakeRenderer(), held: make(chan string, 1), release: make(chan struct{})}
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	// Argument-bound unit evidence isolates the cache/read seam, not visual acceptance.
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return gate, layouttest.NewFakeReviewer(), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	settled := func(rev string) layout.Record {
		t.Helper()
		var row layout.Record
		require.Eventually(t, func() bool {
			var err error
			row, err = ds.layout.store.Get(ctx, rev)
			return err == nil && row.State == layout.StatePassed && row.CacheGeneration == row.ResultGeneration
		}, 10*time.Second, 10*time.Millisecond, "revision %s must pass and settle its cache", rev)
		return row
	}
	cacheOf := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(filepath.Dir(path), filesystem.LayoutCacheFile))
		require.NoError(t, err)
		return data
	}

	// The private draft passes and caches; Publish carries that cache into the
	// shared tree, whose own job then replaces it.
	require.NoError(t, ds.workflowSvc.Create(ctx, "copy-draft", layoutE2EWorkflow("copy-draft", "Copied cache input")))
	draftRev := layoutE2ESavedRevision(t, e.privatePath("copy-draft"))
	settled(draftRev)
	require.NoError(t, ds.workflowSvc.Publish(ctx, "copy-draft", "copy-src", workflow.VisibilityPublic, ""))
	srcRev := layoutE2ESavedRevision(t, e.sharedPath("copy-src"))
	require.NotEqual(t, draftRev, srcRev)
	settled(srcRev)
	srcCache := cacheOf(e.sharedPath("copy-src"))
	published, ok, err := ds.layout.cache.ReadCache(ctx, workflow.Shared(), "copy-src")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, srcRev, published.Revision)
	require.Equal(t, layout.VerdictPassed, published.Verdict)

	// Copy stamps a fresh revision; its own job is held at rendering.
	gate.arm()
	require.NoError(t, ds.workflowSvc.Copy(ctx, "copy-src", "copy-dst"))
	dst := layoutE2EManifest(t, e.privatePath("copy-dst"))
	dstRev := dst.SavedRevision()
	require.NotEqual(t, srcRev, dstRev)
	select {
	case got := <-gate.held:
		require.Equal(t, dstRev, got)
	case <-time.After(5 * time.Second):
		t.Fatal("the destination revision's job never reached the renderer")
	}
	require.Equal(t, srcCache, cacheOf(e.privatePath("copy-dst")), "precondition: the copy carries the source's passed cache verbatim")
	_, ok, err = ds.layout.cache.ReadCache(ctx, workflow.Private(localDefaultUser), "copy-dst")
	assert.Error(t, err, "the cache reader must reject the source token at the destination")
	assert.False(t, ok)

	router := newLayoutE2ERouter(t, ds)
	exec := func(stmt string) {
		t.Helper()
		_, err := ds.db.Exec(stmt)
		require.NoError(t, err)
	}
	getLayout := func(name, rev string) map[string]any {
		t.Helper()
		status, got := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/"+name+"/layout", "")
		require.Equal(t, http.StatusOK, status, "%v", got)
		assert.Equal(t, rev, got["savedRevision"])
		assert.Equal(t, "unavailable", got["displayStatus"])
		assert.Equal(t, false, got["available"])
		assert.Equal(t, true, got["unreviewed"], "an outage never yields reviewed status")
		assert.Nil(t, got["evidence"])
		return got
	}

	// Row-only outage: the destination may show the source's reviewed result
	// only through its captured retained reference, labelled as such.
	exec("ALTER TABLE layout_revisions RENAME TO layout_revisions_offline")
	got := getLayout("copy-dst", dstRev)
	assert.Equal(t, "retained_geometry", got["provenance"], "the copied cache must not be presented as this revision's arrangement")
	retained, _ := got["retained"].(map[string]any)
	assert.Equal(t, srcRev, retained["fromRevision"])

	// Full-store outage: only the copied layout.json could supply geometry now.
	exec("ALTER TABLE layout_results RENAME TO layout_results_offline")
	got = getLayout("copy-dst", dstRev)
	assert.Nil(t, got["geometry"], "the source's cached geometry must not be drawn for the destination revision")
	assert.Equal(t, "retained_positions", got["provenance"])
	// Control: the same outage does reach cachedFallback for the token that owns the cache.
	got = getLayout("copy-src", srcRev)
	assert.Equal(t, "arranged", got["provenance"])
	assert.NotNil(t, got["geometry"])
	exec("ALTER TABLE layout_results_offline RENAME TO layout_results")
	exec("ALTER TABLE layout_revisions_offline RENAME TO layout_revisions")

	// Reverse direction: the destination's own job publishes its own token and
	// the source cache is not touched.
	close(gate.release)
	settled(dstRev)
	own, ok, err := ds.layout.cache.ReadCache(ctx, workflow.Private(localDefaultUser), "copy-dst")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, dstRev, own.Revision)
	require.Equal(t, srcCache, cacheOf(e.sharedPath("copy-src")))
}
