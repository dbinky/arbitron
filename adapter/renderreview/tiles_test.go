package renderreview

import (
	"math"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covered(tiles []layout.Rect, x, y float64) bool {
	for _, tile := range tiles {
		if x >= tile.X-1e-9 && x <= tile.Right()+1e-9 && y >= tile.Y-1e-9 && y <= tile.Bottom()+1e-9 {
			return true
		}
	}
	return false
}

func assertCovers(t *testing.T, canvas layout.Rect, tiles []layout.Rect) {
	t.Helper()
	xs, ys := []float64{canvas.Right()}, []float64{canvas.Bottom()}
	for x := canvas.X; x < canvas.Right(); x += 7 {
		xs = append(xs, x)
	}
	for y := canvas.Y; y < canvas.Bottom(); y += 7 {
		ys = append(ys, y)
	}
	for _, x := range xs {
		for _, y := range ys {
			require.True(t, covered(tiles, x, y), "uncovered point (%v,%v)", x, y)
		}
	}
}

func TestPlanTiles_CoversWholeCanvasIncludingEdgeRemainder(t *testing.T) {
	canvas := layout.Rect{W: 4000, H: 2500}
	tiles := PlanTiles(canvas, .9, 1600, 1000, .15)
	require.Len(t, tiles, 9)
	assertCovers(t, canvas, tiles)
	for _, tile := range tiles {
		assert.True(t, canvas.Contains(tile))
		assert.LessOrEqual(t, tile.W*.9, 1600.0+1e-9)
		assert.LessOrEqual(t, tile.H*.9, 1000.0+1e-9)
	}
	assert.InDelta(t, canvas.Right(), tiles[8].Right(), 1e-9)
	assert.InDelta(t, canvas.Bottom(), tiles[8].Bottom(), 1e-9)
	assert.InDelta(t, 0, tiles[0].X, 1e-9)
	assert.InDelta(t, 1511.111, tiles[1].X, 1e-3)
}

func TestPlanTiles_NeighboursOverlap(t *testing.T) {
	tiles := PlanTiles(layout.Rect{W: 5000, H: 3000}, .9, 1600, 1000, .15)
	require.NotEmpty(t, tiles)
	cols := 0
	for _, tile := range tiles {
		if tile.Y == tiles[0].Y {
			cols++
		}
	}
	require.Greater(t, cols, 1)
	for i := 0; i+1 < cols; i++ {
		assert.GreaterOrEqual(t, tiles[i].Right()-tiles[i+1].X, .15*1600/.9-1e-9)
	}
	for r := 0; (r+1)*cols < len(tiles); r++ {
		assert.GreaterOrEqual(t, tiles[r*cols].Bottom()-tiles[(r+1)*cols].Y, .15*1000/.9-1e-9)
	}
}

func TestPlanTiles_EnforcesMinimumOverlapInCanvasPx(t *testing.T) {
	canvas := layout.Rect{W: 1000, H: 100}
	tiles := PlanTiles(canvas, 1, 200, 200, 0)
	require.Len(t, tiles, 6)
	for i := 0; i+1 < len(tiles); i++ {
		assert.GreaterOrEqual(t, tiles[i].Right()-tiles[i+1].X, MinTileOverlapPx-1e-9)
	}
	assertCovers(t, canvas, tiles)
}

func TestPlanTiles_OffsetCanvasStaysInside(t *testing.T) {
	canvas := layout.Rect{X: -120, Y: 40, W: 3000, H: 900}
	tiles := PlanTiles(canvas, .9, 1600, 1000, .15)
	require.Len(t, tiles, 2)
	assertCovers(t, canvas, tiles)
	for _, tile := range tiles {
		assert.True(t, canvas.Contains(tile))
	}
	assert.Equal(t, canvas.X, tiles[0].X)
	assert.Equal(t, canvas.Y, tiles[0].Y)
}

func TestPlanTiles_TinyCanvasIsOneTile(t *testing.T) {
	canvas := layout.Rect{W: 200, H: 100}
	assert.Equal(t, []layout.Rect{canvas}, PlanTiles(canvas, .9, 1600, 1000, .15))
}
func TestPlanTiles_ExactFitIsOneTile(t *testing.T) {
	canvas := layout.Rect{W: 1600, H: 1000}
	assert.Equal(t, []layout.Rect{canvas}, PlanTiles(canvas, 1, 1600, 1000, .15))
}
func TestPlanTiles_HigherZoomNeedsMoreTiles(t *testing.T) {
	canvas := layout.Rect{W: 3000, H: 3000}
	assert.Greater(t, len(PlanTiles(canvas, 2, 1600, 1000, .15)), len(PlanTiles(canvas, .9, 1600, 1000, .15)))
}
func TestPlanTiles_InvalidInputIsGuarded(t *testing.T) {
	ok := layout.Rect{W: 100, H: 100}
	cases := map[string]struct {
		canvas  layout.Rect
		zoom    float64
		w, h    int
		overlap float64
	}{
		"zero width":      {layout.Rect{H: 100}, .9, 1600, 1000, .15},
		"negative height": {layout.Rect{W: 100, H: -5}, .9, 1600, 1000, .15},
		"NaN canvas":      {layout.Rect{X: math.NaN(), W: 100, H: 100}, .9, 1600, 1000, .15},
		"infinite canvas": {layout.Rect{W: math.Inf(1), H: 100}, .9, 1600, 1000, .15},
		"zero zoom":       {ok, 0, 1600, 1000, .15}, "negative zoom": {ok, -1, 1600, 1000, .15},
		"zero tile width": {ok, .9, 0, 1000, .15}, "negative tile h": {ok, .9, 1600, -1, .15},
		"negative overlap": {ok, .9, 1600, 1000, -.1}, "overlap of one": {ok, .9, 1600, 1000, 1},
		"tile under 2 seam": {layout.Rect{W: 500, H: 500}, 1, 40, 40, .15},
		"extreme count":     {layout.Rect{W: math.MaxFloat64, H: 100}, 1, 200, 200, .15},
		"overflow edge":     {layout.Rect{X: math.MaxFloat64, W: math.MaxFloat64, H: 100}, 1, 200, 200, .15},
		"overflow tile":     {ok, math.SmallestNonzeroFloat64, 200, 200, .15},
		"NaN overlap":       {ok, 1, 200, 200, math.NaN()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { assert.Nil(t, PlanTiles(c.canvas, c.zoom, c.w, c.h, c.overlap)) })
	}
}
