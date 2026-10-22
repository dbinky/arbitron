package renderreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

type reviewFixture struct {
	geo   layout.Geometry
	dir   string
	tiles []layout.Artifact
	req   layout.ReviewRequest
}

func fixtureScenes() []layout.Geometry {
	base := reviewGeometry()
	merged := reviewGeometry()
	merged.Edges[2].Target = "b"
	merged.Edges[2].Points = []layout.Point{{X: 200, Y: 100}, {X: 200, Y: 150}, {X: 140, Y: 150}, {X: 140, Y: 200}}
	merged.Edges[2].TargetAttach = layout.Attachment{NodeID: "b", ElementID: layout.GroupElementID("b", "header", ""), Side: layout.SideTop, Point: layout.Point{X: 140, Y: 200}}
	merged.Crossings = nil
	parallel := reviewGeometry()
	dup := parallel.Edges[0]
	dup.Ordinal = 3
	dup.Output = ""
	dup.Text = nil
	dup.Points = []layout.Point{{X: 110, Y: 100}, {X: 110, Y: 200}}
	dup.SourceAttach.Point = dup.Points[0]
	dup.TargetAttach.Point = dup.Points[1]
	parallel.Edges = append(parallel.Edges, dup)
	return []layout.Geometry{base, merged, parallel}
}

func TestGenerateReadabilityFixture(t *testing.T) {
	if os.Getenv("ARBITRON_UPDATE_RENDER_FIXTURES") != "1" {
		t.Skip("explicit real-browser fixture update only")
	}
	cfg := testBrowserConfig(t, embeddedSurface(t))
	cfg.TileWidth = 360
	cfg.TileHeight = 260
	b := newTestRenderer(t, cfg)
	// All captions/labels are measured by the real bundled-font producer before
	// recording these hand-routed scenes. Their deliberately merged ports remain.
	c := layout.DefaultContract()
	fontRaw, err := os.ReadFile(filepath.Join("..", "..", "dashboard", "src", "graph", "arranged", "fontCoverage.json"))
	require.NoError(t, err)
	var fonts struct {
		Fingerprint string `json:"fingerprint"`
	}
	require.NoError(t, json.Unmarshal(fontRaw, &fonts))
	require.NotEmpty(t, fonts.Fingerprint)
	fontEvidence := struct {
		Fingerprint string            `json:"fingerprint"`
		Assets      map[string]string `json:"assets"`
	}{fonts.Fingerprint, map[string]string{}}
	assets := embeddedSurface(t)
	paths, err := fs.Glob(assets, "assets/*.woff2")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		raw, err := fs.ReadFile(assets, path)
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		fontEvidence.Assets[path] = hex.EncodeToString(sum[:])
	}
	items := []layout.TextItem{}
	for _, n := range reviewGeometry().Nodes {
		items = append(items, layout.TextItem{Key: n.ID, Text: n.Text, Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth}, layout.TextItem{Key: n.ID + "-type", Text: n.TypeCaption, Style: layout.StyleNodeType, MaxWidth: c.NodeTextMaxWidth})
	}
	items = append(items, layout.TextItem{Key: "branch", Text: "yes", Style: layout.StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth})
	m, err := b.Measure(context.Background(), layout.MeasureRequest{Contract: c, Items: items})
	require.NoError(t, err)
	for _, g := range fixtureScenes() {
		for _, n := range g.Nodes {
			require.Less(t, m.Items[n.ID].Width, n.Box.W-c.NodePaddingX*2)
			require.Less(t, m.Items[n.ID+"-type"].Width, n.Box.W-c.NodePaddingX*2)
			require.LessOrEqual(t, m.Items[n.ID].Height+m.Items[n.ID+"-type"].Height+c.NodeTextGapPx+c.NodeTypeBarPx+2*(c.NodePaddingY+c.NodeBorderPx), n.Box.H)
		}
		r, err := b.Render(context.Background(), layout.NewRenderRequest("rev-1", 1, g, map[string]string{"a": "agent_call", "b": "agent_call", "c": "transform"}))
		require.NoError(t, err)
		dir := filepath.Join("testdata", "readability", layout.GeometryDigest(g))
		require.NoError(t, os.MkdirAll(dir, 0755))
		for i := range r.Artifacts {
			a := &r.Artifacts[i]
			raw, err := os.ReadFile(a.Path)
			require.NoError(t, err)
			a.Path = filepath.Base(a.Path)
			require.NoError(t, os.WriteFile(filepath.Join(dir, a.Path), raw, 0644))
		}
		for name, v := range map[string]any{"geometry.json": g, "rendering.json": r, "measurements.json": m, "fonts.json": fontEvidence} {
			raw, err := json.MarshalIndent(v, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), raw, 0644))
		}
	}
}

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	return newReviewFixtureFor(t, reviewGeometry())
}
func newReviewFixtureFor(t *testing.T, g layout.Geometry) *reviewFixture {
	t.Helper()
	dir := filepath.Join("testdata", "readability", layout.GeometryDigest(g))
	raw, err := os.ReadFile(filepath.Join(dir, "geometry.json"))
	require.NoError(t, err, "generate real captures explicitly; unit runs never invent evidence")
	var stored layout.Geometry
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Equal(t, layout.GeometryDigest(g), layout.GeometryDigest(stored))
	raw, err = os.ReadFile(filepath.Join(dir, "rendering.json"))
	require.NoError(t, err)
	var r layout.Rendering
	require.NoError(t, json.Unmarshal(raw, &r))
	require.Equal(t, layout.GeometryDigest(g), r.GeometryDigest)
	require.Equal(t, g.Contract, r.Report.Contract)
	require.Equal(t, layout.ContractVersion, r.Report.SurfaceVersion)
	require.NotEmpty(t, r.Report.Nodes)
	fontRaw, err := os.ReadFile(filepath.Join(dir, "fonts.json"))
	require.NoError(t, err)
	var fonts struct {
		Fingerprint string            `json:"fingerprint"`
		Assets      map[string]string `json:"assets"`
	}
	require.NoError(t, json.Unmarshal(fontRaw, &fonts))
	require.NotEmpty(t, fonts.Fingerprint)
	require.NotEmpty(t, fonts.Assets)
	for _, hash := range fonts.Assets {
		decoded, err := hex.DecodeString(hash)
		require.NoError(t, err)
		require.Len(t, decoded, sha256.Size)
	}
	fx := &reviewFixture{geo: g, dir: t.TempDir()}
	for i := range r.Artifacts {
		a := &r.Artifacts[i]
		require.Equal(t, filepath.Base(a.Path), a.Path)
		raw, err = os.ReadFile(filepath.Join(dir, a.Path))
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		require.Equal(t, a.SHA256, hex.EncodeToString(sum[:]))
		a.Path = filepath.Join(fx.dir, a.Path)
		require.NoError(t, os.WriteFile(a.Path, raw, 0600))
		if a.Kind == "tile" {
			fx.tiles = append(fx.tiles, *a)
		}
	}
	require.NotEmpty(t, fx.tiles)
	fx.req = layout.ReviewRequest{Revision: "rev-1", Attempt: 1, Geometry: g, Rendering: r}
	return fx
}
