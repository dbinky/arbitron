package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// embeddedSurface is the production asset FS. These tests are the provenance
// probes of the P3T4 surface: they fail when render.html is missing from the
// embedded build, cannot mount the shared renderer, or reports nothing.
func embeddedSurface(t *testing.T) fs.FS {
	t.Helper()
	assets := dashboard.Assets()
	_, err := fs.Stat(assets, "render.html")
	require.NoError(t, err, "adapter/dashboard/dist has no render.html; run `make ui` (P3T5 Task 5.4)")
	return assets
}

func loadSampleWorkflow(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", name, "workflow.json"))
	require.NoError(t, err)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(raw, &wf))
	return &wf
}

func TestBrowserRenderer_RealSurfaceMeasure(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	c := layout.DefaultContract()
	long := "Überprüfe sämtliche Änderungen im Pull-Request — 審查所有拉取請求 — und bestätige jede Abhängigkeit gründlich"
	req := layout.MeasureRequest{Contract: c, Items: []layout.TextItem{
		{Key: "long", Text: long, Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "short", Text: "Start", Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "type", Text: "agent call", Style: layout.StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		{Key: "branch", Text: "approved", Style: layout.StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth},
		{Key: "empty", Text: "", Style: layout.StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth},
		{Key: "wide", Text: "WWWWWWWW", Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "narrow", Text: "iiiiiiii", Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "newline", Text: "測試\nCafé", Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "unbroken", Text: strings.Repeat("測試", 40), Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
	}}

	m, err := b.Measure(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, m.FontsReady, "the bundled Inter faces were loaded")
	assert.Equal(t, c, m.Contract, "the surface measures with the same renderer contract")

	lm := m.Items["long"]
	assert.Greater(t, lm.Lines, 1, "a long Unicode label wraps instead of being clipped")
	assert.LessOrEqual(t, lm.Width, c.NodeTextMaxWidth, "wrapped text never exceeds the wrap width")
	assert.Greater(t, lm.Width, c.NodeTextMaxWidth/2)
	assert.GreaterOrEqual(t, lm.Height, float64(lm.Lines)*c.NodeLabelFontPx*c.LineHeight-1)

	sm := m.Items["short"]
	assert.Equal(t, 1, sm.Lines)
	assert.Greater(t, sm.Width, 0.0)
	assert.Less(t, sm.Width, c.NodeTextMaxWidth)
	assert.InDelta(t, math.Ceil(c.NodeLabelFontPx*c.LineHeight), sm.Height, 1)

	assert.Equal(t, 1, m.Items["type"].Lines)
	assert.Equal(t, 1, m.Items["branch"].Lines)
	assert.Less(t, m.Items["branch"].Width, c.EdgeLabelMaxWidth)
	assert.Equal(t, layout.TextMetrics{}, m.Items["empty"])
	assert.Greater(t, m.Items["wide"].Width, 2*m.Items["narrow"].Width, "real proportional glyph metrics, not a per-character guess")
	assert.Equal(t, 2, m.Items["newline"].Lines)
	assert.Greater(t, m.Items["unbroken"].Lines, 1)
	assert.LessOrEqual(t, m.Items["unbroken"].Width, c.NodeTextMaxWidth)
}

func TestBrowserRenderer_RealSurfaceRender(t *testing.T) {
	cfg := testBrowserConfig(t, embeddedSurface(t))
	cfg.TileWidth, cfg.TileHeight = 640, 480 // force several tiles on a sample-sized canvas
	b := newTestRenderer(t, cfg)
	ctx := context.Background()

	wf := loadSampleWorkflow(t, "pr-review")
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	c := layout.DefaultContract()
	m, err := b.Measure(ctx, layout.MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := layout.Arrange(context.Background(), g, m, c, layout.DefaultOptions())
	require.NoError(t, err)

	nodeTypes := make(map[string]string, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodeTypes[n.ID] = string(n.Type)
	}
	req := layout.NewRenderRequest("rev-pr-review", 1, geo, nodeTypes)
	before := time.Now().UTC()
	r, err := b.Render(ctx, req)
	require.NoError(t, err)

	// Identity: the rendering is tied to this exact candidate.
	assert.Equal(t, "rev-pr-review", r.Revision)
	assert.Equal(t, 1, r.Attempt)
	assert.Equal(t, layout.GeometryDigest(geo), r.GeometryDigest)
	assert.False(t, r.RenderedAt.Before(before.Add(-time.Second)))

	rep := r.Report
	assert.Equal(t, layout.ContractVersion, rep.SurfaceVersion)
	assert.Equal(t, geo.Contract, rep.Contract)
	assert.True(t, rep.FontsReady)
	assert.True(t, rep.FontFamilyResolved)
	assert.Equal(t, c.PracticalZoom, rep.Zoom)
	assert.Equal(t, geo.Canvas, rep.Canvas)

	// Coverage: every node and edge of the candidate was drawn where the geometry put it.
	require.Len(t, rep.Nodes, len(geo.Nodes))
	byID := map[string]layout.RenderedNode{}
	for _, n := range rep.Nodes {
		byID[n.ID] = n
	}
	for _, n := range geo.Nodes {
		got, ok := byID[n.ID]
		require.True(t, ok, "node %s missing from the report", n.ID)
		assert.InDelta(t, n.Box.X, got.Box.X, 1.5, "node %s x", n.ID)
		assert.InDelta(t, n.Box.Y, got.Box.Y, 1.5, "node %s y", n.ID)
		assert.InDelta(t, n.Box.W, got.Box.W, 1.5, "node %s w", n.ID)
		assert.InDelta(t, n.Box.H, got.Box.H, 1.5, "node %s h", n.ID)
		assert.Equal(t, n.Text, got.Label.Content)
		assert.Equal(t, n.TypeCaption, got.Type.Content)
		for _, text := range []layout.RenderedText{got.Label, got.Type} {
			assert.NotEmpty(t, text.Glyphs, n.ID)
			assert.True(t, text.Visible, n.ID)
			assert.False(t, text.Clipped, n.ID)
			assert.False(t, text.Occluded, n.ID)
			assert.GreaterOrEqual(t, text.FontPx*c.PracticalZoom, c.MinReadableTextPx)
			assert.GreaterOrEqual(t, text.Contrast, c.MinContrastRatio)
		}
	}
	require.Len(t, rep.Edges, len(geo.Edges))
	for i, e := range rep.Edges {
		require.Equal(t, i, e.Ordinal, "edges are reported in ordinal order")
		route := geo.Edges[i]
		assert.True(t, e.HasArrow, "edge %d has an arrowhead", e.Ordinal)
		assert.True(t, e.Visible)
		require.NotEmpty(t, e.Segments)
		assert.NotEmpty(t, e.StrokeColor)
		assert.Greater(t, e.StrokePx, 0.0)
		assert.Len(t, e.ArrowPoints, 3)
		if route.Text != nil {
			require.NotNil(t, e.Label)
			assert.Equal(t, route.Text.Text, e.Label.Content)
			assert.NotEmpty(t, e.Label.Glyphs)
			assert.True(t, e.Label.Visible)
		}
		assert.InDelta(t, route.Points[0].X, e.Start.X, 2, "edge %d start x", e.Ordinal)
		assert.InDelta(t, route.Points[0].Y, e.Start.Y, 2, "edge %d start y", e.Ordinal)
		last := route.Points[len(route.Points)-1]
		assert.InDelta(t, last.X, e.End.X, 2, "edge %d end x", e.Ordinal)
		assert.InDelta(t, last.Y, e.End.Y, 2, "edge %d end y", e.Ordinal)
	}
	assert.Len(t, rep.Crossings, len(geo.Crossings))
	require.Len(t, rep.States, len(layout.RuntimeStates))
	for i, s := range layout.RuntimeStates {
		assert.Equal(t, s, rep.States[i].State)
		assert.True(t, rep.States[i].NodeBoxesStable, "%s: %v", s, rep.States[i].Issues)
		assert.True(t, rep.States[i].LabelsReadable, "%s: %v", s, rep.States[i].Issues)
	}

	// Evidence: overview + tiles on disk, hashed, covering the whole canvas.
	tiles := PlanTiles(geo.Canvas, c.PracticalZoom, 640, 480, DefaultOverlap)
	require.GreaterOrEqual(t, len(tiles), 2)
	require.Len(t, r.Artifacts, len(tiles)+1)
	assert.Equal(t, "overview", r.Artifacts[0].Kind)
	assert.Equal(t, geo.Canvas, r.Artifacts[0].Region)
	assert.GreaterOrEqual(t, distinctColors(t, r.Artifacts[0].Path), 3, "the overview shows the drawn graph")
	regions := make([]layout.Rect, 0, len(tiles))
	for i, a := range r.Artifacts {
		assert.Equal(t, filepath.Join(cfg.ArtifactDir, "rev-pr-review", "1"), filepath.Dir(filepath.Dir(a.Path)))
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		assert.Equal(t, hex.EncodeToString(sum[:]), a.SHA256, a.Path)
		img, err := png.DecodeConfig(bytes.NewReader(raw))
		require.NoError(t, err)
		assert.InDelta(t, a.Region.W*a.Scale, float64(img.Width), 2, a.Path)
		assert.InDelta(t, a.Region.H*a.Scale, float64(img.Height), 2, a.Path)
		if i == 0 {
			continue
		}
		assert.Equal(t, "tile", a.Kind)
		assert.Equal(t, c.PracticalZoom, a.Scale, "tiles are captured at practical zoom")
		assert.Equal(t, tiles[i-1], a.Region)
		regions = append(regions, a.Region)
	}
	assertCovers(t, geo.Canvas, regions)
}
