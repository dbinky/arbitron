package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

// Actual retries reuse revision/attempt, but must not overwrite a prior review's
// captures. Both roots construct the production browser and raster reviewer;
// the observer forwards every request unchanged and records their real output.
func TestLayoutRender_RealBrowserRetryPreservesBothCaptures(t *testing.T) {
	browser := requireBrowserE2E(t)
	originalAssets := layoutRenderAssets
	t.Cleanup(func() { layoutRenderAssets = originalAssets })
	assets := originalAssets()
	html, err := fs.ReadFile(assets, "render.html")
	require.NoError(t, err)
	// Simulate broken rendered ink, not a fabricated report or failing reviewer.
	blank := []byte(strings.Replace(string(html), "</head>", `<style>html,body {background:white!important} #render-root {opacity:0!important}</style></head>`, 1))
	layoutRenderAssets = func() fs.FS { return reportSurfaceAssets{assets, blank} }
	probe := &renderPortsProbe{}
	installRenderPortsProbe(t, probe)
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
	ds, closeFirst := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "immutable-captures"
	wf := layoutE2EWorkflow(name, "Café 審查 ✅")
	wf.Edges[0].Label = "Freigabe 審查"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, wf))
	saved := layoutE2EManifest(t, env.privatePath(name))
	savedBytes, err := os.ReadFile(env.privatePath(name))
	require.NoError(t, err)
	first := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
	require.Equal(t, layout.StateNeedsAttention, first.State, "%s: %+v", first.FailureReason, first.Findings)
	require.Equal(t, 1, first.AttemptsUsed)
	firstResult, err := ds.layout.store.Result(ctx, first.Revision, first.ResultGeneration)
	require.NoError(t, err)
	require.NotNil(t, firstResult.Review)
	require.False(t, firstResult.Review.Rendered)
	attempts := probe.observed()
	require.Len(t, attempts, 1)
	firstCapture := attempts[0].request
	require.Equal(t, firstResult.Review.Evidence, attempts[0].review.Evidence)
	blankOverview := layout.Finding{Criterion: layout.CriterionCoverage, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: "overview contains no visible graph"}
	require.Contains(t, firstResult.Review.Findings, blankOverview)
	view, err := ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
	require.Positive(t, layout.BlockingCount(view.Findings))
	assertEvidenceOnDisk(t, env, firstResult.Review.Evidence)
	originalBytes := make(map[string][]byte)
	for _, a := range firstCapture.Rendering.Artifacts {
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		originalBytes[a.Path] = raw
	}
	closeFirst()

	// Repair the actual surface and retry the SAME saved revision through the
	// authorized production query, with a fresh root/browser over the same DB.
	layoutRenderAssets = originalAssets
	ds, _ = env.boot(t)
	ctx = adminCtxForLocalUser(t, ds)
	require.IsType(t, &renderreview.BrowserRenderer{}, probe.Renderer)
	require.IsType(t, &renderreview.ReadabilityReviewer{}, probe.reviewer)
	ack, err := ds.layout.query.RetryWorkflow(ctx, name, first.Revision)
	require.NoError(t, err)
	require.Equal(t, first.JobGeneration+1, ack.JobGeneration)
	second := layoutRenderWaitTerminal(t, ds, first.Revision, time.Minute)
	require.Equal(t, ack.JobGeneration, second.JobGeneration)
	require.Equal(t, layout.StatePassed, second.State, "%s: %+v", second.FailureReason, second.Findings)
	require.Equal(t, 1, second.AttemptsUsed)
	require.Equal(t, first.Manifest, second.Manifest)
	captured, err := json.Marshal(saved)
	require.NoError(t, err)
	require.JSONEq(t, string(captured), string(second.Manifest))
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, name), "Retry creates a generation, not another saved revision")
	secondResult, err := ds.layout.store.Result(ctx, second.Revision, second.ResultGeneration)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(secondResult.Geometry, secondResult.Review))
	assertEvidenceOnDisk(t, env, secondResult.Review.Evidence)
	attempts = probe.observed()
	require.Len(t, attempts, 2)
	secondCapture := attempts[1].request
	for _, req := range []layout.ReviewRequest{firstCapture, secondCapture} {
		require.Equal(t, saved.SavedRevision(), req.Revision)
		require.Equal(t, 1, req.Attempt)
		require.Equal(t, layout.GeometryDigest(req.Geometry), req.Rendering.GeometryDigest)
		require.Len(t, req.Rendering.Report.Nodes, len(saved.Nodes))
		for i, n := range req.Rendering.Report.Nodes {
			require.Equal(t, saved.Nodes[i].ID, n.ID)
			require.Equal(t, saved.Nodes[i].Label, n.Label.Content)
			require.Equal(t, strings.ReplaceAll(string(saved.Nodes[i].Type), "_", " "), n.Type.Content)
		}
		require.Len(t, req.Rendering.Report.Edges, len(saved.Edges))
		require.NotNil(t, req.Rendering.Report.Edges[0].Label)
		require.Equal(t, saved.Edges[0].Label, req.Rendering.Report.Edges[0].Label.Content)
	}
	require.NotEqual(t, firstCapture.Rendering.Artifacts[0].SHA256, secondCapture.Rendering.Artifacts[0].SHA256, "blank and repaired pages must have different actual pixels")
	for _, a := range secondCapture.Rendering.Artifacts {
		_, reused := originalBytes[a.Path]
		require.False(t, reused, "retry reused published evidence path %s", a.Path)
		require.Equal(t, filepath.Join(env.root, "layout-artifacts", first.Revision, "1"), filepath.Dir(filepath.Dir(a.Path)))
	}
	for _, a := range firstCapture.Rendering.Artifacts {
		after, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		require.Equal(t, originalBytes[a.Path], after, "retry overwrote earlier review evidence")
		sum := sha256.Sum256(after)
		require.Equal(t, a.SHA256, hex.EncodeToString(sum[:]))
	}
	retained, err := ds.layout.store.Result(ctx, first.Revision, first.ResultGeneration)
	require.NoError(t, err)
	require.Equal(t, firstResult, retained, "Retry must preserve the first generation's review and evidence")

	// Reopen BOTH actual captures after retry. A hash, path or report alias
	// must fail here, not merely leave a plausible-looking SQLite verdict.
	for i, req := range []layout.ReviewRequest{firstCapture, secondCapture} {
		rv, err := probe.reviewer.Review(ctx, req)
		require.NoError(t, err)
		require.Equal(t, req.Rendering.Artifacts, rv.Evidence.Artifacts)
		if i == 0 {
			require.False(t, rv.Rendered)
			require.Contains(t, rv.Findings, blankOverview)
		} else {
			require.True(t, layout.HasRenderedEvidence(req.Geometry, &rv))
			require.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
		}
	}
	view, err = ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
	require.Equal(t, second.ResultGeneration, view.ResultGeneration)
	require.NotNil(t, view.Geometry)
	require.Equal(t, layout.GeometryDigest(secondResult.Geometry), layout.GeometryDigest(*view.Geometry))
	after, err := os.ReadFile(env.privatePath(name))
	require.NoError(t, err)
	require.Equal(t, savedBytes, after, "render/review/retry never changes authoring bytes")
}
