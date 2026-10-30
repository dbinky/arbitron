package renderreview

import (
	"context"
	"image/color"
	"math"

	"github.com/arbitron/arbitron/domain/layout"
)

// Baseline fills differ by only a few channel levels. Stroke matching's broad
// antialias tolerance cannot distinguish an erased card from its parent fill.
const (
	fillChannelTolerance = 1
	fillSampleStepPx     = 8
	paintClearancePx     = 2
)

func checkGroupCapture(ctx context.Context, geo layout.Geometry, rep layout.RenderReport, tiles []shot, f *findingSet) {
	outline, _ := colorRGB(geo.Contract.ForwardColor)
	var foreground []layout.Rect
	for _, edge := range rep.Edges {
		if edge.Label != nil {
			foreground = append(foreground, edge.Label.Box.Inflate(geo.Contract.StrokePx))
		}
	}
	for i, e := range rep.GroupElements {
		if ctx.Err() != nil {
			return
		}
		w := geo.GroupElements[i]
		ids := []string{w.GroupNodeID}
		if !outlineInk(ctx, tiles, e.Box, outline, geo.Contract.NodeBorderPx, foreground) {
			f.block(layout.CriterionRenderMismatch, ids, nil, "group element %s outline ink is missing from captured tiles", e.ID)
		}
		fill, _ := colorRGB(groupFill(w.Role))
		if !groupFillInk(ctx, tiles, geo, rep, i, fill) {
			f.block(layout.CriterionRenderMismatch, ids, nil, "group element %s background ink is missing from captured tiles", e.ID)
		}
		for _, t := range e.Texts {
			if !textInk(ctx, tiles, t.Text, geo.Contract.MinContrastRatio) {
				f.block(layout.CriterionTextUnreadable, ids, nil, "group text %s has no complete glyph ink in captured tiles", t.Key)
			}
		}
	}
	for i, e := range rep.GroupConnectors {
		if ctx.Err() != nil {
			return
		}
		w := geo.GroupConnectors[i]
		ids := []string{w.GroupNodeID}
		expected := expectedRouteSegments(geo, w.Points, w.ID, geo.Contract.GroupConnectorDash)
		if !routeInk(ctx, tiles, e.Segments, expected, geo.Contract.GroupConnectorDash, outline, geo.Contract.StrokePx) {
			f.block(layout.CriterionMergedLines, ids, nil, "connector %s corridor ink is missing from captured tiles", e.ID)
		}
		if !arrowInk(tiles, layout.RenderedEdge{ArrowPoints: e.ArrowPoints}, outline) {
			f.block(layout.CriterionArrowDirection, ids, nil, "connector %s arrow ink is missing from captured tiles", e.ID)
		}
	}
	for _, c := range geo.GroupCrossings {
		if ctx.Err() != nil {
			return
		}
		rgb := outline
		for _, e := range rep.Edges {
			if layout.ManifestRouteKey(e.Ordinal) == c.Under {
				rgb, _ = colorRGB(e.StrokeColor)
			}
		}
		if !gapInkClear(tiles, c.Point, rgb, geo.Contract) {
			f.block(layout.CriterionAmbiguousCrossing, crossingOwners(geo, c), nil, "crossing %s / %s gap contains under-route ink", c.Over, c.Under)
		}
	}
}

func outlineInk(ctx context.Context, tiles []shot, r layout.Rect, rgb [3]int, width float64, foreground []layout.Rect) bool {
	ink := func(p layout.Point) bool {
		for _, box := range foreground {
			if box.Contains(layout.Rect{X: p.X, Y: p.Y}) {
				return true
			}
		}
		return strokeInk(tiles, p, rgb, width)
	}
	// Local complete stroke neighbourhoods, not an entire container in one tile.
	for d := width * 2; d < r.W-width*2; d += corridorStepPx {
		if ctx.Err() != nil || !ink(layout.Point{X: r.X + d, Y: r.Y + width/2}) || !ink(layout.Point{X: r.X + d, Y: r.Bottom() - width/2}) {
			return false
		}
	}
	for d := width * 2; d < r.H-width*2; d += corridorStepPx {
		if ctx.Err() != nil || !ink(layout.Point{X: r.X + width/2, Y: r.Y + d}) || !ink(layout.Point{X: r.Right() - width/2, Y: r.Y + d}) {
			return false
		}
	}
	return true
}

func groupFillInk(ctx context.Context, tiles []shot, geo layout.Geometry, rep layout.RenderReport, index int, rgb [3]int) bool {
	e := rep.GroupElements[index]
	w := geo.GroupElements[index]
	excluded := []layout.Rect{}
	for _, other := range rep.GroupElements {
		if other.ID != e.ID && e.Box.Contains(other.Box) {
			excluded = append(excluded, other.Box.Inflate(paintClearancePx))
		}
	}
	for _, n := range rep.Nodes {
		if e.Box.Contains(n.Box) && layout.GroupElementID(n.ID, "header", "") != e.ID {
			excluded = append(excluded, n.Box.Inflate(paintClearancePx))
		}
	}
	for _, t := range e.Texts {
		for _, g := range t.Text.Glyphs {
			excluded = append(excluded, g.Inflate(paintClearancePx))
		}
	}
	if w.Role == "header" {
		for _, n := range rep.Nodes {
			if n.ID == w.GroupNodeID {
				excluded = append(excluded, n.Label.Box.Inflate(paintClearancePx), n.Type.Box.Inflate(paintClearancePx), layout.Rect{X: e.Box.X, Y: e.Box.Y, W: e.Box.W, H: geo.Contract.NodeBorderPx + geo.Contract.NodeTypeBarPx + paintClearancePx})
			}
		}
	}
	var segs []layout.RenderedSegment
	for _, r := range rep.Edges {
		segs = append(segs, r.Segments...)
		if r.Label != nil {
			excluded = append(excluded, r.Label.Box.Inflate(paintClearancePx))
		}
	}
	for _, r := range rep.GroupConnectors {
		segs = append(segs, r.Segments...)
	}
	samples := 0
	for y := e.Box.Y + fillSampleStepPx; y < e.Box.Bottom()-fillSampleStepPx/2; y += fillSampleStepPx {
		for x := e.Box.X + fillSampleStepPx; x < e.Box.Right()-fillSampleStepPx/2; x += fillSampleStepPx {
			if ctx.Err() != nil {
				return false
			}
			p := layout.Point{X: x, Y: y}
			skip := false
			for _, r := range excluded {
				if r.Contains(layout.Rect{X: x, Y: y}) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			for _, s := range segs {
				if pointSegmentDistance(p, s) < geo.Contract.ArrowPx+paintClearancePx {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			samples++
			if !fillPixel(tiles, p, rgb) {
				return false
			}
		}
	}
	return samples > 0
}
func fillPixel(tiles []shot, p layout.Point, rgb [3]int) bool {
	for _, s := range tiles {
		if !localTile(s, layout.Rect{X: p.X - 1, Y: p.Y - 1, W: 2, H: 2}) {
			continue
		}
		x := int(math.Floor((p.X - s.art.Region.X) * s.art.Scale))
		y := int(math.Floor((p.Y - s.art.Region.Y) * s.art.Scale))
		if !imagePointInside(s, x, y) {
			continue
		}
		v := color.NRGBAModel.Convert(s.img.At(x, y)).(color.NRGBA)
		if v.A == 255 && absInt(int(v.R)-rgb[0]) <= fillChannelTolerance && absInt(int(v.G)-rgb[1]) <= fillChannelTolerance && absInt(int(v.B)-rgb[2]) <= fillChannelTolerance {
			return true
		}
	}
	return false
}

// routeInk respects same-point SVG subpath restarts as well as crossing gaps.
// A rounded corner curves off its chord, so it is probed at its arc midpoint
// instead, and advances the dash phase by its arc length as the browser draws it.
func routeInk(ctx context.Context, tiles []shot, segs []layout.RenderedSegment, expected []expectedSegment, pattern string, rgb [3]int, stroke float64) bool {
	dash, ok := parseDash(pattern)
	if !ok {
		return false
	}
	phase := 0.0
	var prev *layout.Point
	for j, s := range segs {
		restart := prev != nil && !pointNear(*prev, s.Start, .05)
		var corner *expectedSegment
		if len(expected) == len(segs) {
			restart = expected[j].restart
			if expected[j].corner {
				corner = &expected[j]
			}
		}
		if restart {
			phase = 0
		}
		end := s.End
		prev = &end
		l := segmentLength(s)
		if !finite(l) || l == 0 {
			return false
		}
		if corner != nil {
			arc := arcLength(s)
			if dashPaints(phase+arc/2, dash) && !strokeInk(tiles, arcMidpoint(*corner), rgb, stroke) {
				return false
			}
			phase += arc
			continue
		}
		for d := 0.0; d <= l; d += corridorStepPx {
			if ctx.Err() != nil {
				return false
			}
			if dashPaints(d+phase, dash) && !strokeInk(tiles, atDistance(s, d), rgb, stroke) {
				return false
			}
		}
		phase += l
	}
	return len(segs) > 0
}
func gapInkClear(tiles []shot, p layout.Point, rgb [3]int, c layout.RendererContract) bool {
	for d := -c.CrossingGapPx/2 + 1; d < c.CrossingGapPx/2; d++ {
		if math.Abs(d) <= c.StrokePx/2+1 {
			continue
		}
		if !clearGap(tiles, layout.Point{X: p.X, Y: p.Y + d}, rgb) {
			return false
		}
	}
	return true
}
