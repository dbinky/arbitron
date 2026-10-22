package layout

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unitEvidence(g Geometry) Review {
	digest := GeometryDigest(g)
	return Review{Rendered: true, Evidence: Evidence{GeometryDigest: digest, RendererVersion: g.Contract.Version, Contract: g.Contract, FontsReady: true, Zoom: g.Contract.PracticalZoom, Tiles: 1, ReviewedAt: time.Unix(1, 0).UTC(), Artifacts: []Artifact{
		{Kind: "overview", Path: "/unit/" + digest + "-overview.png", SHA256: digest, Region: g.Canvas, Scale: g.Contract.PracticalZoom / 2},
		{Kind: "tile", Path: "/unit/" + digest + "-tile.png", SHA256: digest, Region: g.Canvas, Scale: g.Contract.PracticalZoom},
	}}}
}

func cloneValue[T any](t *testing.T, v T) T {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestHasRenderedEvidence(t *testing.T) {
	g := validTwoNodeGeometry()
	require.True(t, HasRenderedEvidence(g, ptrReview(unitEvidence(g))), "matching complete evidence")
	assert.False(t, HasRenderedEvidence(g, nil))
	cases := map[string]func(*Review){
		"not rendered": func(r *Review) { r.Rendered = false },
		"digest":       func(r *Review) { r.Evidence.GeometryDigest = "stale" },
		"contract":     func(r *Review) { r.Evidence.Contract.NodePaddingX++ },
		"version":      func(r *Review) { r.Evidence.RendererVersion = "old" },
		"zoom": func(r *Review) {
			r.Evidence.Zoom = 1
			for i := range r.Evidence.Artifacts {
				r.Evidence.Artifacts[i].Scale = 1
			}
		},
		"fonts":       func(r *Review) { r.Evidence.FontsReady = false },
		"time":        func(r *Review) { r.Evidence.ReviewedAt = time.Time{} },
		"overview":    func(r *Review) { r.Evidence.Artifacts = r.Evidence.Artifacts[1:] },
		"tile":        func(r *Review) { r.Evidence.Artifacts = r.Evidence.Artifacts[:1]; r.Evidence.Tiles = 0 },
		"count":       func(r *Review) { r.Evidence.Tiles++ },
		"path":        func(r *Review) { r.Evidence.Artifacts[0].Path = "" },
		"hash":        func(r *Review) { r.Evidence.Artifacts[1].SHA256 = "no" },
		"hash nonhex": func(r *Review) { r.Evidence.Artifacts[1].SHA256 = strings.Repeat("z", 64) },
		"region zero": func(r *Review) { r.Evidence.Artifacts[1].Region.W = 0 },
		"region nan":  func(r *Review) { r.Evidence.Artifacts[1].Region.X = math.NaN() },
		"region inf":  func(r *Review) { r.Evidence.Artifacts[1].Region.H = math.Inf(1) },
		"outside": func(r *Review) {
			a := r.Evidence.Artifacts[1]
			a.Region.X = -100
			r.Evidence.Artifacts = append(r.Evidence.Artifacts, a)
			r.Evidence.Tiles++
		},
		"redundant zero tile": func(r *Review) {
			a := r.Evidence.Artifacts[1]
			a.Region.W = 0
			r.Evidence.Artifacts = append(r.Evidence.Artifacts, a)
			r.Evidence.Tiles++
		},
		"redundant negative tile": func(r *Review) {
			a := r.Evidence.Artifacts[1]
			a.Region.W = -1
			r.Evidence.Artifacts = append(r.Evidence.Artifacts, a)
			r.Evidence.Tiles++
		},
		"overview partial": func(r *Review) { r.Evidence.Artifacts[0].Region.W /= 2 },
		"scale zero":       func(r *Review) { r.Evidence.Artifacts[0].Scale = 0 },
		"scale nan":        func(r *Review) { r.Evidence.Artifacts[1].Scale = math.NaN() },
		"scale inf":        func(r *Review) { r.Evidence.Artifacts[0].Scale = math.Inf(1) },
		"tile scale":       func(r *Review) { r.Evidence.Artifacts[1].Scale /= 2 },
		"overview scale":   func(r *Review) { r.Evidence.Artifacts[0].Scale = 2 },
		"unknown kind":     func(r *Review) { r.Evidence.Artifacts[0].Kind = "other" },
		"partial tile":     func(r *Review) { r.Evidence.Artifacts[1].Region.H /= 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { r := unitEvidence(g); mutate(&r); assert.False(t, HasRenderedEvidence(g, &r)) })
	}
	for _, name := range []string{"nonzero origin", "empty canvas", "nan geometry"} {
		t.Run(name, func(t *testing.T) {
			bad := cloneValue(t, g)
			switch name {
			case "nonzero origin":
				bad.Canvas.X = 1
			case "empty canvas":
				bad.Canvas.W = 0
			case "nan geometry":
				bad.Nodes[0].Box.W = math.NaN()
			}
			r := unitEvidence(g)
			r.Evidence.GeometryDigest = GeometryDigest(bad)
			assert.False(t, HasRenderedEvidence(bad, &r))
		})
	}
}

func ptrReview(r Review) *Review { return &r }

func TestHasRenderedEvidence_TileUnion(t *testing.T) {
	g := validTwoNodeGeometry()
	g.Canvas = Rect{W: 100, H: 1000}
	for _, hole := range []bool{false, true} {
		r := unitEvidence(g)
		tile := r.Evidence.Artifacts[1]
		r.Evidence.Artifacts = r.Evidence.Artifacts[:1]
		for _, box := range []Rect{{W: 60, H: 600}, {Y: 400, W: 60, H: 600}, {X: 40, W: 60, H: 1000}} {
			a := tile
			a.Region = box
			r.Evidence.Artifacts = append(r.Evidence.Artifacts, a)
		}
		if hole {
			r.Evidence.Artifacts[2].Region.Y = 650
			r.Evidence.Artifacts[2].Region.H = 350
		}
		r.Evidence.Tiles = 3
		assert.Equal(t, !hole, HasRenderedEvidence(g, &r), "overlapping long-canvas union; hole=%v", hole)
	}
}
