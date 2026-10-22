package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A measurement fault hides branch clipping from preflight. The real DOM
// reviewer must discover it, drive Refine and publish the newly rendered result.
func TestRefine_RealBrowserSavedBranchClippingReachesSink(t *testing.T) {
	browser := requireBrowserE2E(t)
	probe := &renderPortsProbe{understateBranch: true}
	installRenderPortsProbe(t, probe)
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "branch-refinement"
	wf := layoutE2EWorkflow(name, "Start")
	wf.Edges[0].Label = "Überprüfung\n審查"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, wf))
	saved := layoutE2EManifest(t, env.privatePath(name))
	before, err := os.ReadFile(env.privatePath(name))
	require.NoError(t, err)
	rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
	require.Equal(t, layout.StatePassed, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
	require.Greater(t, rec.AttemptsUsed, 1, "visual clipping must require another rendered candidate")
	assert.LessOrEqual(t, rec.AttemptsUsed, rec.AttemptLimit)
	result, err := ds.layout.store.Result(ctx, rec.Revision, rec.ResultGeneration)
	require.NoError(t, err)
	attempts := probe.observed()
	require.Len(t, attempts, rec.AttemptsUsed)
	graph, err := layout.BuildGraph(saved)
	require.NoError(t, err)
	probe.mu.Lock()
	request, measured, used := probe.measureRequest, probe.measured, probe.used
	probe.mu.Unlock()
	assert.Equal(t, layout.MeasureRequestFor(graph, layout.DefaultContract()), request)
	assert.Equal(t, 2, measured.Items[layout.EdgeLabelKey(0)].Lines)
	assert.Equal(t, 1, used.Items[layout.EdgeLabelKey(0)].Lines)
	first := attempts[0]
	assert.Equal(t, layout.DefaultOptions(), first.request.Geometry.Options)
	for _, finding := range layout.Preflight(graph, first.request.Geometry, used) {
		assert.NotEqual(t, layout.CriterionTextClipped, finding.Criterion, "the defect must be visual-only")
	}
	require.NotNil(t, first.request.Rendering.Report.Edges[0].Label)
	assert.True(t, first.request.Rendering.Report.Edges[0].Label.Clipped)
	assert.Equal(t, saved.Edges[0].Label, first.request.Rendering.Report.Edges[0].Label.Content)
	clipping := false
	for _, finding := range first.review.Findings {
		if finding.Criterion == layout.CriterionTextClipped && finding.Severity == layout.SeverityBlocking {
			assert.Contains(t, finding.EdgeOrdinals, 0)
			clipping = true
		}
	}
	require.True(t, clipping, "actual branch clipping must reach the production reviewer: %+v", first.review.Findings)
	for i, attempt := range attempts {
		assert.Equal(t, rec.Revision, attempt.request.Revision)
		assert.Equal(t, i+1, attempt.request.Attempt)
		assert.Equal(t, layout.GeometryDigest(attempt.request.Geometry), attempt.review.Evidence.GeometryDigest)
		assertEvidenceOnDisk(t, env, attempt.review.Evidence)
		if i == 0 {
			continue
		}
		prior := attempts[i-1]
		findings := append(layout.Preflight(graph, prior.request.Geometry, used), prior.review.Findings...)
		next, changed := layout.Refine(prior.request.Geometry.Options, findings)
		require.True(t, changed)
		assert.Equal(t, next, attempt.request.Geometry.Options, "real findings must feed the next actual candidate")
		assert.Greater(t, attempt.request.Geometry.Options.NodeTextSlack, prior.request.Geometry.Options.NodeTextSlack)
		assert.Greater(t, attempt.request.Geometry.Edges[0].Text.Box.H, prior.request.Geometry.Edges[0].Text.Box.H)
		assert.NotEqual(t, prior.request.Rendering.Artifacts[0].Path, attempt.request.Rendering.Artifacts[0].Path)
	}
	last := attempts[len(attempts)-1]
	assert.False(t, last.request.Rendering.Report.Edges[0].Label.Clipped)
	assert.True(t, last.review.Rendered)
	assert.Zero(t, layout.BlockingCount(last.review.Findings))
	assert.Equal(t, layout.GeometryDigest(last.request.Geometry), layout.GeometryDigest(result.Geometry))
	assert.Equal(t, &last.review, result.Review)
	view := ds.layout.reader.View(ctx, saved)
	assert.Equal(t, layout.DisplayComplete, view.DisplayStatus)
	assert.False(t, view.Unreviewed)
	require.NotNil(t, view.Geometry)
	assert.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
	require.NotNil(t, view.Evidence)
	assert.Equal(t, result.Review.Evidence.ForViewer(), *view.Evidence)
	require.Eventually(t, func() bool {
		entry, ok, err := ds.layout.cache.ReadCache(ctx, rec.Destination.Scope, rec.Destination.Name)
		return err == nil && ok && entry.Revision == rec.Revision && entry.Generation == rec.ResultGeneration && layout.GeometryDigest(entry.Geometry) == layout.GeometryDigest(result.Geometry)
	}, time.Minute, 50*time.Millisecond)
	after, err := os.ReadFile(env.privatePath(name))
	require.NoError(t, err)
	assert.Equal(t, before, after, "refinement is presentation-only")
}
