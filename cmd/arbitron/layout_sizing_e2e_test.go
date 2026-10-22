package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Sizing must consume browser measurements in the save -> worker -> SQLite ->
// Reader/cache path, not just when Arrange is called directly by a unit test.
func TestLayoutSizing_RealBrowserSavedUnicodeMeasurementsReachView(t *testing.T) {
	browser := requireBrowserE2E(t)
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "unicode-sizing"
	wf := layoutE2EWorkflow(name, "Start")
	wf.Edges[0].Label = "Continue"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, wf))
	r1 := layoutE2ESavedRevision(t, env.privatePath(name))
	first := layoutRenderWaitTerminal(t, ds, r1, 2*time.Minute)
	require.Equal(t, layout.StatePassed, first.State, "%s: %+v", first.FailureReason, first.Findings)
	short, err := ds.layout.store.Result(ctx, r1, first.ResultGeneration)
	require.NoError(t, err)

	wf, err = ds.workflowSvc.Load(ctx, name)
	require.NoError(t, err)
	// Include supported emoji ZWJ graphemes in both wrapping paths: scalar
	// coverage alone cannot prove that these sequences fit their measured boxes.
	wf.Nodes[0].Label = "Café\n" + strings.Repeat("測試👩‍💻", 40)
	wf.Edges[0].Label = "Überprüfung\n" + strings.Repeat("審查👩‍💻", 20)
	require.NoError(t, ds.workflowSvc.Update(ctx, name, wf))
	saved := layoutE2EManifest(t, env.privatePath(name))
	r2 := saved.SavedRevision()
	require.NotEqual(t, r1, r2)
	before, err := os.ReadFile(env.privatePath(name))
	require.NoError(t, err)
	second := layoutRenderWaitTerminal(t, ds, r2, 2*time.Minute)
	require.Equal(t, layout.StatePassed, second.State, "%s: %+v", second.FailureReason, second.Findings)
	result, err := ds.layout.store.Result(ctx, r2, second.ResultGeneration)
	require.NoError(t, err)
	geo := result.Geometry
	graph, err := layout.BuildGraph(saved)
	require.NoError(t, err)
	metrics, err := ds.layout.renderer.Measure(context.Background(), layout.MeasureRequestFor(graph, geo.Contract))
	require.NoError(t, err)
	nodeMetric := metrics.Items[layout.NodeLabelKey("t1")]
	edgeMetric := metrics.Items[layout.EdgeLabelKey(0)]
	require.Greater(t, nodeMetric.Lines, 2, "explicit newline and unbroken Unicode must wrap")
	require.Greater(t, edgeMetric.Lines, 2)
	assert.LessOrEqual(t, nodeMetric.Width, geo.Contract.NodeTextMaxWidth)
	assert.LessOrEqual(t, edgeMetric.Width, geo.Contract.EdgeLabelMaxWidth)
	require.Len(t, geo.Nodes, 3)
	require.Len(t, geo.Edges, 2)
	assert.Equal(t, saved.Nodes[0].Label, geo.Nodes[0].Text)
	assert.Equal(t, nodeMetric.Lines, geo.Nodes[0].TextLines)
	assert.Greater(t, geo.Nodes[0].Box.H, short.Geometry.Nodes[0].Box.H)
	assert.GreaterOrEqual(t, geo.Nodes[0].Box.H, nodeMetric.Height+2*geo.Contract.NodePaddingY)
	require.NotNil(t, geo.Edges[0].Text)
	assert.Equal(t, saved.Edges[0].Label, geo.Edges[0].Text.Text)
	assert.Equal(t, edgeMetric.Lines, geo.Edges[0].Text.Lines)
	assert.Greater(t, geo.Edges[0].Text.Box.H, short.Geometry.Edges[0].Text.Box.H)
	assert.GreaterOrEqual(t, geo.Edges[0].Text.Box.H, edgeMetric.Height+2*geo.Contract.EdgeLabelPaddingY)
	require.NotNil(t, result.Review)
	assert.Zero(t, layout.BlockingCount(result.Review.Findings))
	assertEvidenceOnDisk(t, env, result.Review.Evidence)

	view := ds.layout.reader.View(ctx, saved)
	require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
	require.NotNil(t, view.Geometry)
	assert.Equal(t, layout.GeometryDigest(geo), layout.GeometryDigest(*view.Geometry))
	require.Eventually(t, func() bool {
		entry, ok, err := ds.layout.cache.ReadCache(ctx, second.Destination.Scope, name)
		return err == nil && ok && entry.Revision == r2 && layout.GeometryDigest(entry.Geometry) == layout.GeometryDigest(geo)
	}, 30*time.Second, 100*time.Millisecond)
	after, err := os.ReadFile(env.privatePath(name))
	require.NoError(t, err)
	assert.Equal(t, before, after, "measurement/review must not rewrite the saved source")
	old, err := ds.layout.store.Result(ctx, r1, first.ResultGeneration)
	require.NoError(t, err)
	assert.Equal(t, layout.GeometryDigest(short.Geometry), layout.GeometryDigest(old.Geometry))
}
