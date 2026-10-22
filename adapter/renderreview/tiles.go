package renderreview

import (
	"github.com/arbitron/arbitron/domain/layout"
	"math"
)

const (
	DefaultTileWidth  = 1600
	DefaultTileHeight = 1000
	DefaultOverlap    = .15
	// MinTileOverlapPx is the minimum seam width in canvas coordinates.
	MinTileOverlapPx = 24.0
	maxTiles         = 256
)

// PlanTiles returns bounded, overlapping canvas regions in row-major order.
// Invalid inputs or plans exceeding the renderer's capture budget yield nil.
func PlanTiles(canvas layout.Rect, zoom float64, tileW, tileH int, overlap float64) []layout.Rect {
	if !finite(canvas.X, canvas.Y, canvas.W, canvas.H, canvas.Right(), canvas.Bottom(), zoom, overlap) ||
		canvas.W <= 0 || canvas.H <= 0 || zoom <= 0 || tileW <= 0 || tileH <= 0 || overlap < 0 || overlap >= 1 {
		return nil
	}
	w, h := float64(tileW)/zoom, float64(tileH)/zoom
	if !finite(w, h) || w <= 0 || h <= 0 {
		return nil
	}
	cols, rows := spanCount(canvas.W, w, overlap), spanCount(canvas.H, h, overlap)
	if cols == 0 || rows == 0 || cols > maxTiles/rows {
		return nil
	}
	out := make([]layout.Rect, 0, cols*rows)
	for r := 0; r < rows; r++ {
		y := spanStart(canvas.Y, canvas.H, h, overlap, r, rows)
		for c := 0; c < cols; c++ {
			x := spanStart(canvas.X, canvas.W, w, overlap, c, cols)
			out = append(out, layout.Rect{X: x, Y: y, W: math.Min(w, canvas.W), H: math.Min(h, canvas.H)})
		}
	}
	return out
}

// Count before allocating or converting to int, including overflow inputs.
func spanCount(length, tile, overlap float64) int {
	if length <= tile {
		return 1
	}
	seam := math.Max(overlap*tile, MinTileOverlapPx)
	if 2*seam >= tile {
		return 0
	}
	n := math.Ceil((length-tile)/(tile-seam)-1e-9) + 1
	if !finite(n) || n < 1 || n > maxTiles {
		return 0
	}
	return int(n)
}

func spanStart(origin, length, tile, overlap float64, i, n int) float64 {
	if n == 1 {
		return origin
	}
	if i == n-1 {
		return origin + length - tile
	}
	return origin + float64(i)*(tile-math.Max(overlap*tile, MinTileOverlapPx))
}

func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
