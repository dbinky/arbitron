package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireBrowserE2E(t *testing.T) string {
	t.Helper()
	path, err := renderreview.DetectBrowser(os.Getenv("ARBITRON_LAYOUT_BROWSERPATH"))
	if err != nil {
		if os.Getenv("ARBITRON_REQUIRE_BROWSER") == "1" {
			t.Fatalf("ARBITRON_REQUIRE_BROWSER=1 but no browser is available: %v", err)
		}
		t.Skipf("real-browser test: opt-in via `make test-render` (ARBITRON_REQUIRE_BROWSER=1): %v", err)
	}
	return path
}

func useLayoutE2EBrowser(t *testing.T, path string) {
	t.Helper()
	t.Setenv("ARBITRON_LAYOUT_BROWSERPATH", path)
	if os.Geteuid() == 0 {
		t.Setenv("ARBITRON_LAYOUT_BROWSERNOSANDBOX", "true")
	}
}

func layoutRenderWaitTerminal(t *testing.T, ds *domainStack, revision string, within time.Duration) layout.Record {
	t.Helper()
	var rec layout.Record
	require.Eventually(t, func() bool {
		got, err := ds.layout.store.Get(context.Background(), revision)
		rec = got
		return err == nil && got.State.Terminal()
	}, within, 250*time.Millisecond, "revision %s must reach terminal arrangement state", revision)
	return rec
}

// layoutCacheFile waits for the worker's cache publish of a settled revision
// and returns the cache path. The row turns terminal before PublishCache writes
// layout.json, so a test that removes the cache must wait first, and before it
// stops the worker, whose cancellation would abandon the publish.
func layoutCacheFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "layout.json")
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "the worker never published %s", path)
	return path
}

func assertDownwardArrangement(t *testing.T, geo layout.Geometry) {
	t.Helper()
	require.NotEmpty(t, geo.Nodes)
	require.NotEmpty(t, geo.Edges)
	boxes := map[string]layout.NodeBox{}
	top, bottom := map[int]float64{}, map[int]float64{}
	for _, n := range geo.Nodes {
		boxes[n.ID] = n
		if v, ok := top[n.Rank]; !ok || n.Box.Y < v {
			top[n.Rank] = n.Box.Y
		}
		if v, ok := bottom[n.Rank]; !ok || n.Box.Bottom() > v {
			bottom[n.Rank] = n.Box.Bottom()
		}
	}
	for _, e := range geo.Edges {
		if e.Kind != layout.EdgeForward {
			continue
		}
		src, dst := boxes[e.Source], boxes[e.Target]
		assert.Greater(t, dst.Rank, src.Rank, "forward edge %d descends a rank", e.Ordinal)
		assert.GreaterOrEqual(t, dst.Box.Y, src.Box.Bottom(), "forward edge %d target below source", e.Ordinal)
		for i := 1; i < len(e.Points); i++ {
			assert.GreaterOrEqual(t, e.Points[i].Y, e.Points[i-1].Y-0.5, "forward edge %d never climbs", e.Ordinal)
		}
	}
	ranks := make([]int, 0, len(top))
	for r := range top {
		ranks = append(ranks, r)
	}
	sort.Ints(ranks)
	for i := 1; i < len(ranks); i++ {
		assert.Greater(t, top[ranks[i]], bottom[ranks[i-1]], "rank %d starts below rank %d", ranks[i], ranks[i-1])
	}
}

// assertEvidenceReleased checks the capture a stored result cites has left the
// disk (PR #85 Must 5): only the current revision's selected evidence stays.
func assertEvidenceReleased(t *testing.T, ev layout.Evidence) {
	t.Helper()
	require.NotEmpty(t, ev.Artifacts)
	for _, a := range ev.Artifacts {
		assert.NoFileExists(t, a.Path, "released evidence must leave the disk")
	}
}

func assertEvidenceOnDisk(t *testing.T, env *layoutE2EEnv, ev layout.Evidence) {
	t.Helper()
	require.Greater(t, len(ev.Artifacts), 1)
	assert.Equal(t, "overview", ev.Artifacts[0].Kind)
	assert.Equal(t, len(ev.Artifacts)-1, ev.Tiles)
	root := filepath.Join(env.root, "layout-artifacts") + string(filepath.Separator)
	for _, a := range ev.Artifacts {
		assert.True(t, strings.HasPrefix(a.Path, root), "evidence must use configured artifactdir: %s", a.Path)
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		assert.Equal(t, a.SHA256, hex.EncodeToString(sum[:]))
	}
}

func TestLayoutRender_RealBrowserAcceptance(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, name := range []string{"pr-review", "legacy-feature-pipeline", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			assertSampleReviewed(t, browser, name)
		})
	}
}

func assertSampleReviewed(t *testing.T, browser, name string) {
	t.Helper()
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5", realSample: name})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
	ds, _ := env.boot(t)
	ctx := context.Background()
	revision := layoutE2ESavedRevision(t, env.sharedPath(name))
	before, err := os.ReadFile(env.sharedPath(name))
	require.NoError(t, err)
	rec := layoutRenderWaitTerminal(t, ds, revision, 20*time.Minute)
	require.Equal(t, layout.StatePassed, rec.State, "failure stage %q reason %q; unresolved findings %+v", rec.FailureStage, rec.FailureReason, rec.Findings)
	assert.Equal(t, layout.VerdictPassed, rec.Verdict)
	assert.False(t, rec.Unreviewed)
	assert.Equal(t, 5, rec.AttemptLimit)
	assert.GreaterOrEqual(t, rec.AttemptsUsed, 1)
	assert.LessOrEqual(t, rec.AttemptsUsed, 5)
	assert.Zero(t, layout.BlockingCount(rec.Findings))
	require.Positive(t, rec.ResultGeneration)
	res, err := ds.layout.store.Result(ctx, revision, rec.ResultGeneration)
	require.NoError(t, err)
	require.NotNil(t, res.Review)
	assert.True(t, res.Review.Rendered)
	require.True(t, layout.HasRenderedEvidence(res.Geometry, res.Review), "the stored candidate must carry complete matching rendered evidence")
	geo := res.Geometry
	t.Logf("reviewed %s revision=%s attempt=%d digest=%s", name, revision, rec.AttemptsUsed, layout.GeometryDigest(geo))
	assert.Equal(t, layout.GeometryDigest(geo), res.Review.Evidence.GeometryDigest)
	assert.Equal(t, geo.Contract, res.Review.Evidence.Contract)
	assert.True(t, res.Review.Evidence.FontsReady)
	assert.Equal(t, 0.9, res.Review.Evidence.Zoom)
	assertEvidenceOnDisk(t, env, res.Review.Evidence)
	if name == "pr-review" {
		assert.Len(t, geo.Nodes, 19)
		assert.Len(t, geo.Edges, 19)
	}
	if name == "legacy-feature-pipeline" {
		assert.Len(t, geo.Nodes, 66)
		assert.Len(t, geo.Edges, 79)
	}
	if name != "pr-review" {
		assertDistinctLoopChannels(t, geo)
	}
	wf := layoutE2EManifest(t, env.sharedPath(name))
	assert.Len(t, geo.Nodes, len(wf.Nodes))
	assert.Len(t, geo.Edges, len(wf.Edges))
	assertDownwardArrangement(t, geo)
	view := ds.layout.reader.View(ctx, wf)
	assert.Equal(t, revision, view.SavedRevision)
	assert.Equal(t, layout.DisplayComplete, view.DisplayStatus)
	assert.Equal(t, layout.ProvenanceArranged, view.Provenance)
	require.NotNil(t, view.Geometry)
	assert.Equal(t, layout.GeometryDigest(geo), layout.GeometryDigest(*view.Geometry))
	require.Eventually(t, func() bool {
		entry, ok, err := ds.layout.cache.ReadCache(ctx, rec.Destination.Scope, rec.Destination.Name)
		return err == nil && ok && entry.Revision == revision && entry.JobGeneration == rec.JobGeneration && entry.Generation == rec.ResultGeneration && layout.GeometryDigest(entry.Geometry) == layout.GeometryDigest(geo)
	}, 60*time.Second, 100*time.Millisecond)
	after, err := os.ReadFile(env.sharedPath(name))
	require.NoError(t, err)
	assert.Equal(t, before, after, "arrangement never rewrites the authoring manifest")
}

func assertDistinctLoopChannels(t *testing.T, geo layout.Geometry) {
	t.Helper()
	var loops []layout.EdgeRoute
	for _, edge := range geo.Edges {
		if edge.Kind == layout.EdgeLoopReturn {
			assert.Contains(t, []layout.Side{layout.SideLeft, layout.SideRight}, edge.Side)
			assert.GreaterOrEqual(t, edge.Lane, 1)
			loops = append(loops, edge)
		}
	}
	require.NotEmpty(t, loops)
	for i, a := range loops {
		for _, b := range loops[i+1:] {
			if a.Side != b.Side {
				continue
			}
			ax, ay0, ay1 := loopChannel(a)
			bx, by0, by1 := loopChannel(b)
			if ay0 < by1 && by0 < ay1 {
				assert.NotEqual(t, ax, bx, "loop returns %d and %d need distinct channels", a.Ordinal, b.Ordinal)
			}
		}
	}
}

func loopChannel(edge layout.EdgeRoute) (x, y0, y1 float64) {
	best := -1.0
	for i := 1; i < len(edge.Points); i++ {
		a, b := edge.Points[i-1], edge.Points[i]
		if a.X == b.X && math.Abs(b.Y-a.Y) > best {
			best, x, y0, y1 = math.Abs(b.Y-a.Y), a.X, min(a.Y, b.Y), max(a.Y, b.Y)
		}
	}
	return
}

func TestLayoutRender_MissingBrowserFailsVisiblyAndIsRetryable(t *testing.T) {
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "3"})
	useLayoutE2EBrowser(t, filepath.Join(t.TempDir(), "no-such-chromium"))
	ds, _ := env.boot(t)
	require.IsType(t, &renderreview.BrowserRenderer{}, ds.layout.renderer)
	require.IsType(t, &renderreview.ReadabilityReviewer{}, ds.layout.reviewer)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "needs-browser", layoutE2EWorkflow("needs-browser", "Start")))
	savedBytes, err := os.ReadFile(env.privatePath("needs-browser"))
	require.NoError(t, err)
	revision := layoutE2ESavedRevision(t, env.privatePath("needs-browser"))
	rec := layoutE2EWaitTerminal(t, ds, revision)
	assert.Equal(t, layout.StateFailed, rec.State)
	assert.Equal(t, layout.StageRendering, rec.FailureStage)
	assert.Contains(t, rec.FailureReason, layout.ErrRendererUnavailable.Error())
	assert.Equal(t, 1, rec.AttemptsUsed)
	assert.Zero(t, rec.ResultGeneration)
	assert.Equal(t, workflow.RetainedFromManifest, rec.Retained.Source)
	assert.Equal(t, layoutE2EPositions, rec.Retained.Positions)
	saved := layoutE2EManifest(t, env.privatePath("needs-browser"))
	require.NotNil(t, saved.Presentation)
	assert.Equal(t, revision, saved.Presentation.Revision)
	require.Len(t, saved.Nodes, 3)
	assert.Equal(t, "Start t1", saved.Nodes[0].Label)
	view := ds.layout.reader.View(ctx, saved)
	assert.Equal(t, layout.DisplayFailed, view.DisplayStatus)
	assert.Equal(t, layoutE2EPositions, view.Retained.Positions)

	retried, err := ds.layout.requester.Retry(ctx, revision)
	require.NoError(t, err)
	assert.Equal(t, rec.JobGeneration+1, retried.JobGeneration)
	require.Eventually(t, func() bool {
		got, err := ds.layout.store.Get(context.Background(), revision)
		return err == nil && got.JobGeneration == retried.JobGeneration && got.State == layout.StateFailed && got.FailureStage == layout.StageRendering && strings.Contains(got.FailureReason, layout.ErrRendererUnavailable.Error())
	}, 60*time.Second, 50*time.Millisecond, "retry must run through the actual worker")
	after, err := os.ReadFile(env.privatePath("needs-browser"))
	require.NoError(t, err)
	assert.Equal(t, savedBytes, after, "failure and Retry never rewrite authoring content")
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(env.logPath)
		return err == nil && strings.Contains(string(raw), "no Chrome/Chromium browser found for layout review")
	}, 10*time.Second, 50*time.Millisecond)
}
