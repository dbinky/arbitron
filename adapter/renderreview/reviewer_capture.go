package renderreview

import (
	"context"
	"image/color"
	"math"
	"sort"

	"github.com/arbitron/arbitron/domain/layout"
)

const (
	corridorStepPx     = 2.0
	channelTolerance   = 32
	minTextInkChannel  = 110
	minTextInkFraction = .025
	maxTextInkFraction = .75
	maxWhitespaceEm    = 1.5
)

// checkCapture checks the actual captured pixels, never treating a DOM claim as
// evidence that a feature was painted. Each local probe must fit in one tile.
func checkCapture(ctx context.Context, geo layout.Geometry, rep layout.RenderReport, overview shot, tiles []shot, f *findingSet) {
	if !rectNear(overview.art.Region, geo.Canvas, .01) || !shotSizeMatches(overview) {
		f.block(layout.CriterionCoverage, nil, nil, "overview does not show the full candidate canvas at its recorded size")
	}
	regions := []layout.Rect{}
	for _, s := range tiles {
		if ctx.Err() != nil {
			return
		}
		if !shotSizeMatches(s) || !near(s.art.Scale, geo.Contract.PracticalZoom, 1e-6) || !geo.Canvas.Inflate(.01).Contains(s.art.Region) {
			f.block(layout.CriterionCoverage, nil, nil, "tile scale, region or image size is invalid")
			continue
		}
		r := s.art.Region
		inset := MinTileOverlapPx / 2 / s.art.Scale
		if r.X > geo.Canvas.X+.01 {
			r.X += inset
			r.W -= inset
		}
		if r.Y > geo.Canvas.Y+.01 {
			r.Y += inset
			r.H -= inset
		}
		if s.art.Region.Right() < geo.Canvas.Right()-.01 {
			r.W -= inset
		}
		if s.art.Region.Bottom() < geo.Canvas.Bottom()-.01 {
			r.H -= inset
		}
		if validRect(r) {
			regions = append(regions, r)
		}
	}
	if !coversCanvas(geo.Canvas, regions) {
		f.block(layout.CriterionCoverage, nil, nil, "readable tiles leave uncovered canvas or insufficient interior seam clearance")
	}
	if f.blocking() > 0 {
		return
	}
	// The overview is independently required to contain rendered ink, not a blank
	// thumbnail paired with otherwise valid detail tiles.
	if !shotHasVariation(overview) {
		f.block(layout.CriterionCoverage, nil, nil, "overview contains no visible graph")
	}
	for _, n := range rep.Nodes {
		if ctx.Err() != nil {
			return
		}
		for _, t := range []layout.RenderedText{n.Label, n.Type} {
			if !textInk(ctx, tiles, t, geo.Contract.MinContrastRatio) {
				f.block(layout.CriterionTextUnreadable, []string{n.ID}, nil, "node %q text %q has no complete visible glyph ink in captured tiles", n.ID, t.Content)
			}
		}
	}
	for _, e := range rep.Edges {
		if ctx.Err() != nil {
			return
		}
		ids := []string{geo.Edges[e.Ordinal].Source, geo.Edges[e.Ordinal].Target}
		ords := []int{e.Ordinal}
		rgb, ok := colorRGB(e.StrokeColor)
		if !ok {
			continue
		}
		expected := expectedSegments(geo, geo.Edges[e.Ordinal])
		if !routeInk(ctx, tiles, e.Segments, expected, e.DashPattern, rgb, e.StrokePx) {
			f.block(layout.CriterionMergedLines, ids, ords, "edge %d path corridor ink is missing from captured tiles", e.Ordinal)
		}
		if !arrowInk(tiles, e, rgb) {
			f.block(layout.CriterionArrowDirection, ids, ords, "edge %d complete arrow interior is absent from captured tiles", e.Ordinal)
		}
		if e.Label != nil && !textInk(ctx, tiles, *e.Label, geo.Contract.MinContrastRatio) {
			f.block(layout.CriterionTextUnreadable, ids, ords, "edge %d branch text ink is absent from captured tiles", e.Ordinal)
		}
	}
	for _, c := range geo.Crossings {
		if ctx.Err() != nil {
			return
		}
		under := rep.Edges[c.Under]
		rgb, ok := colorRGB(under.StrokeColor)
		if !ok {
			continue
		}
		if !gapInkClear(tiles, c.Point, rgb, geo.Contract) {
			f.block(layout.CriterionAmbiguousCrossing, nil, []int{c.Over, c.Under}, "crossing gap contains under-edge ink in captured pixels")
		}
	}
}
func shotSizeMatches(s shot) bool {
	return validRect(s.art.Region) && finite(s.art.Scale) && s.art.Scale > 0 && near(float64(s.img.Bounds().Dx()), s.art.Region.W*s.art.Scale, 2) && near(float64(s.img.Bounds().Dy()), s.art.Region.H*s.art.Scale, 2)
}
func coversCanvas(canvas layout.Rect, regions []layout.Rect) bool {
	if !validRect(canvas) {
		return false
	}
	xs := []float64{canvas.X, canvas.Right()}
	ys := []float64{canvas.Y, canvas.Bottom()}
	for _, r := range regions {
		xs = append(xs, r.X, r.Right())
		ys = append(ys, r.Y, r.Bottom())
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	for i := 1; i < len(xs); i++ {
		x := (xs[i-1] + xs[i]) / 2
		if x < canvas.X || x > canvas.Right() {
			continue
		}
		for j := 1; j < len(ys); j++ {
			y := (ys[j-1] + ys[j]) / 2
			if y < canvas.Y || y > canvas.Bottom() {
				continue
			}
			found := false
			for _, r := range regions {
				if x >= r.X && x <= r.Right() && y >= r.Y && y <= r.Bottom() {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return len(regions) > 0
}
func shotHasVariation(s shot) bool {
	base := color.NRGBAModel.Convert(s.img.At(0, 0)).(color.NRGBA)
	for y := 0; y < s.img.Bounds().Dy(); y += 2 {
		for x := 0; x < s.img.Bounds().Dx(); x += 2 {
			c := color.NRGBAModel.Convert(s.img.At(x, y)).(color.NRGBA)
			if absInt(int(c.R)-int(base.R)) > channelTolerance || absInt(int(c.G)-int(base.G)) > channelTolerance || absInt(int(c.B)-int(base.B)) > channelTolerance {
				return true
			}
		}
	}
	return false
}
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
func pixelMatches(c color.Color, want [3]int) bool {
	v := color.NRGBAModel.Convert(c).(color.NRGBA)
	return absInt(int(v.R)-want[0]) <= channelTolerance && absInt(int(v.G)-want[1]) <= channelTolerance && absInt(int(v.B)-want[2]) <= channelTolerance && v.A == 255
}
func localTile(s shot, r layout.Rect) bool {
	return validRect(r) && s.art.Region.Inflate(.001).Contains(r)
}
func strokeInk(tiles []shot, p layout.Point, rgb [3]int, stroke float64) bool {
	if !validPoint(p) {
		return false
	}
	for _, s := range tiles {
		radius := math.Max(1, stroke*s.art.Scale/2)
		margin := radius / s.art.Scale
		if !localTile(s, layout.Rect{X: p.X - margin, Y: p.Y - margin, W: margin * 2, H: margin * 2}) {
			continue
		}
		x := (p.X - s.art.Region.X) * s.art.Scale
		y := (p.Y - s.art.Region.Y) * s.art.Scale
		for iy := int(math.Floor(y - radius)); iy <= int(math.Ceil(y+radius)); iy++ {
			for ix := int(math.Floor(x - radius)); ix <= int(math.Ceil(x+radius)); ix++ {
				if imagePointInside(s, ix, iy) && pixelMatches(s.img.At(ix, iy), rgb) {
					return true
				}
			}
		}
	}
	return false
}
func imagePointInside(s shot, x, y int) bool {
	return x >= 0 && y >= 0 && x < s.img.Bounds().Dx() && y < s.img.Bounds().Dy()
}
func dashPaints(distance float64, dash []float64) bool {
	if len(dash) == 0 {
		return true
	}
	period := 0.0
	for _, v := range dash {
		period += v
	}
	if len(dash)%2 == 1 {
		dash = append(append([]float64{}, dash...), dash...)
		period *= 2
	}
	d := math.Mod(distance, period)
	for i, v := range dash {
		if d < v-.5 {
			return i%2 == 0
		}
		if d < v+.5 {
			return false
		}
		d -= v
	}
	return false
}
func arrowInk(tiles []shot, e layout.RenderedEdge, rgb [3]int) bool {
	if len(e.ArrowPoints) != 3 {
		return false
	}
	a, b, c := e.ArrowPoints[0], e.ArrowPoints[1], e.ArrowPoints[2]
	if !validPoint(a) || !validPoint(b) || !validPoint(c) {
		return false
	}
	// Probe the triangle interior away from the route centreline: line ink alone
	// cannot pass as an arrow. All interior probes must fit in one local tile.
	for _, s := range tiles {
		minX, maxX := math.Min(a.X, math.Min(b.X, c.X)), math.Max(a.X, math.Max(b.X, c.X))
		minY, maxY := math.Min(a.Y, math.Min(b.Y, c.Y)), math.Max(a.Y, math.Max(b.Y, c.Y))
		if !localTile(s, layout.Rect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}) {
			continue
		}
		all := true
		for _, weights := range [][3]float64{{.6, .2, .2}, {.2, .2, .6}, {.3, .4, .3}} {
			p := layout.Point{X: a.X*weights[0] + b.X*weights[1] + c.X*weights[2], Y: a.Y*weights[0] + b.Y*weights[1] + c.Y*weights[2]}
			x := int(math.Floor((p.X - s.art.Region.X) * s.art.Scale))
			y := int(math.Floor((p.Y - s.art.Region.Y) * s.art.Scale))
			if !imagePointInside(s, x, y) || !pixelMatches(s.img.At(x, y), rgb) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
func textInk(ctx context.Context, tiles []shot, t layout.RenderedText, minContrast float64) bool {
	if len(t.Glyphs) == 0 {
		return false
	}
	for _, r := range t.Glyphs {
		if !validRect(r) {
			return false
		}
		found := false
		for _, s := range tiles {
			if ctx.Err() != nil {
				return false
			}
			if !localTile(s, r) {
				continue
			}
			// Range bounds describe complete lines. Require contrasting ink throughout
			// each line, allowing normal whitespace but not a blank word/label region.
			x0, x1 := int(math.Floor((r.X-s.art.Region.X)*s.art.Scale)), int(math.Ceil((r.Right()-s.art.Region.X)*s.art.Scale))
			y0, y1 := int(math.Floor((r.Y-s.art.Region.Y)*s.art.Scale)), int(math.Ceil((r.Bottom()-s.art.Region.Y)*s.art.Scale))
			ink, total, maxBlank, blank := 0, 0, 0, 0
			minL, maxL := 1.0, 0.0
			for x := x0; x < x1; x++ {
				if ctx.Err() != nil {
					return false
				}
				column := false
				for y := y0; y < y1; y++ {
					if !imagePointInside(s, x, y) {
						continue
					}
					v := color.NRGBAModel.Convert(s.img.At(x, y)).(color.NRGBA)
					total++
					lum := luminance(v)
					minL = math.Min(minL, lum)
					maxL = math.Max(maxL, lum)
					if int(v.R)+int(v.G)+int(v.B) > 3*minTextInkChannel && v.A == 255 {
						ink++
						column = true
					}
				}
				if column {
					blank = 0
				} else {
					blank++
					if blank > maxBlank {
						maxBlank = blank
					}
				}
			}
			if total > 0 && (maxL+.05)/(minL+.05) >= minContrast && float64(ink)/float64(total) > minTextInkFraction && float64(ink)/float64(total) < maxTextInkFraction && float64(maxBlank) <= t.FontPx*s.art.Scale*maxWhitespaceEm {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func luminance(c color.NRGBA) float64 {
	linear := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= .04045 {
			return s / 12.92
		}
		return math.Pow((s+.055)/1.055, 2.4)
	}
	return .2126*linear(c.R) + .7152*linear(c.G) + .0722*linear(c.B)
}
func clearGap(tiles []shot, p layout.Point, rgb [3]int) bool {
	for _, s := range tiles {
		if !localTile(s, layout.Rect{X: p.X - 1, Y: p.Y - .25, W: 2, H: .5}) {
			continue
		}
		x := int(math.Round((p.X - s.art.Region.X) * s.art.Scale))
		y := int(math.Floor((p.Y - s.art.Region.Y) * s.art.Scale))
		if !pixelMatches(s.img.At(x, y), rgb) {
			return true
		}
	}
	return false
}
