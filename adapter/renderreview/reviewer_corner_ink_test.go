package renderreview

import (
	"context"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
)

// paintedTile is a 1:1 tile with a 2px stroke along each painted path.
func paintedTile(w, h int, paths ...func(float64) layout.Point) shot {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
		if i%4 != 3 {
			img.Pix[i] = 20
		}
	}
	for _, path := range paths {
		for k := 0; k <= 2000; k++ {
			q := path(float64(k) / 2000)
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					img.Set(int(math.Floor(q.X))+dx, int(math.Floor(q.Y))+dy, color.NRGBA{138, 138, 176, 255})
				}
			}
		}
	}
	return shot{art: layout.Artifact{Region: layout.Rect{W: float64(w), H: float64(h)}, Scale: 1}, img: img}
}

func line(a, b layout.Point) func(float64) layout.Point {
	return func(t float64) layout.Point { return layout.Point{X: a.X + (b.X-a.X)*t, Y: a.Y + (b.Y-a.Y)*t} }
}

func arc(c layout.Point, r, from, to float64) func(float64) layout.Point {
	return func(t float64) layout.Point {
		a := from + (to-from)*t
		return layout.Point{X: c.X + r*math.Cos(a), Y: c.Y + r*math.Sin(a)}
	}
}

// The corner neighbourhood is probed at its arc midpoint, so a route whose
// corners were never painted fails. (A wrong sweep with the same chord is
// rejected earlier and strictly, by report.ts pathPoints: at a 6px radius the
// two midpoints are within one probe window of each other.)
func TestRouteInk_ProbesTheCornerArcMidpoint(t *testing.T) {
	p := func(x, y float64) layout.Point { return layout.Point{X: x, Y: y} }
	geo := layout.Geometry{Contract: layout.DefaultContract()}
	points := []layout.Point{p(10, 10), p(50, 10), p(50, 50)}
	expected := expectedSegments(geo, layout.EdgeRoute{Ordinal: 0, Kind: layout.EdgeForward, Points: points})
	var segs []layout.RenderedSegment
	for _, s := range expected {
		r := s.RenderedSegment
		r.Corner = s.corner
		segs = append(segs, r)
	}
	rgb := [3]int{138, 138, 176}
	straight := []func(float64) layout.Point{line(p(10, 10), p(44, 10)), line(p(50, 16), p(50, 50))}
	painted := paintedTile(64, 64, append(straight, arc(p(44, 16), 6, -math.Pi/2, 0))...)
	assert.True(t, routeInk(context.Background(), []shot{painted}, segs, expected, "", rgb, 2))
	missing := paintedTile(64, 64, straight...)
	assert.False(t, routeInk(context.Background(), []shot{missing}, segs, expected, "", rgb, 2), "no corner ink")
}
