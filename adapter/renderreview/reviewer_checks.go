package renderreview

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/layout"
)

const (
	boxTolerancePx      = 1.5
	endpointTolerancePx = 2.0
	borderTolerancePx   = 3.0
	strokeTolerancePx   = .25
)

func near(a, b, t float64) bool { return finite(a) && finite(b) && math.Abs(a-b) <= t }
func validRect(r layout.Rect) bool {
	return finite(r.X, r.Y, r.W, r.H, r.Right(), r.Bottom()) && r.W > 0 && r.H > 0
}
func validPoint(p layout.Point) bool              { return finite(p.X) && finite(p.Y) }
func pointNear(a, b layout.Point, t float64) bool { return near(a.X, b.X, t) && near(a.Y, b.Y, t) }
func rectNear(a, b layout.Rect, t float64) bool {
	return validRect(a) && validRect(b) && near(a.X, b.X, t) && near(a.Y, b.Y, t) && near(a.W, b.W, t) && near(a.H, b.H, t)
}
func checkFonts(rep layout.RenderReport, f *findingSet) {
	if !rep.FontsReady || !rep.FontFamilyResolved || rep.Contract.FontFamily != layout.DefaultContract().FontFamily || rep.Contract.FontPackage != layout.DefaultContract().FontPackage {
		f.block(layout.CriterionTextUnreadable, nil, nil, "bundled contract fonts were not loaded and resolved")
	}
}
func checkText(t layout.RenderedText, c layout.RendererContract, z float64, nodes []string, edges []int, f *findingSet) {
	if !t.Visible || strings.TrimSpace(t.Content) == "" || len(t.Glyphs) == 0 || !validRect(t.Box) || !finite(t.FontPx) || !finite(t.Contrast) || t.FontPx*z < c.MinReadableTextPx-1e-6 || t.Contrast < c.MinContrastRatio {
		f.block(layout.CriterionTextUnreadable, nodes, edges, "text %q is invisible, missing glyphs or below readable font/contrast thresholds", t.Content)
	}
	if t.Clipped {
		f.block(layout.CriterionTextClipped, nodes, edges, "text %q is clipped", t.Content)
	}
	if t.Occluded {
		f.block(layout.CriterionTextCollision, nodes, edges, "text %q is occluded", t.Content)
	}
	for _, g := range t.Glyphs {
		if !validRect(g) || !t.Box.Inflate(boxTolerancePx).Contains(g) {
			f.block(layout.CriterionTextClipped, nodes, edges, "text %q glyph bounds escape its box", t.Content)
			break
		}
	}
}
func checkNodes(geo layout.Geometry, rep layout.RenderReport, f *findingSet) {
	byID := map[string]layout.NodeBox{}
	for _, n := range geo.Nodes {
		byID[n.ID] = n
	}
	for i, n := range rep.Nodes {
		ids := []string{n.ID}
		want := byID[n.ID]
		if !rectNear(n.Box, want.Box, boxTolerancePx) {
			f.block(layout.CriterionRenderMismatch, ids, nil, "node %q box differs from candidate", n.ID)
		}
		for j, t := range []layout.RenderedText{n.Label, n.Type} {
			expected := want.Text
			if j == 1 {
				expected = want.TypeCaption
			}
			if t.Content != expected {
				f.block(layout.CriterionRenderMismatch, ids, nil, "node %q text %q differs from %q", n.ID, t.Content, expected)
			}
			checkText(t, geo.Contract, rep.Zoom, ids, nil, f)
			for _, g := range t.Glyphs {
				if validRect(n.Box) && validRect(g) && !n.Box.Inflate(boxTolerancePx).Contains(g) {
					f.block(layout.CriterionTextClipped, ids, nil, "node %q glyphs escape node", n.ID)
					break
				}
			}
		}
		if glyphOverlap(n.Label, n.Type) {
			f.block(layout.CriterionTextCollision, ids, nil, "node %q label and type overlap", n.ID)
		}
		for _, other := range rep.Nodes[i+1:] {
			if glyphOverlap(n.Label, other.Label) || glyphOverlap(n.Label, other.Type) || glyphOverlap(n.Type, other.Label) || glyphOverlap(n.Type, other.Type) {
				f.block(layout.CriterionTextCollision, []string{n.ID, other.ID}, nil, "text from different nodes overlaps")
			}
			if validRect(n.Box) && validRect(other.Box) && n.Box.Inflate(geo.Contract.MinClearancePx/2).Overlaps(other.Box.Inflate(geo.Contract.MinClearancePx/2)) {
				f.block(layout.CriterionNodeOverlap, []string{n.ID, other.ID}, nil, "rendered nodes overlap or lack clearance")
			}
		}
	}
}
func glyphOverlap(a, b layout.RenderedText) bool {
	for _, x := range a.Glyphs {
		for _, y := range b.Glyphs {
			if validRect(x) && validRect(y) && x.Overlaps(y) {
				return true
			}
		}
	}
	return false
}
func segmentLength(s layout.RenderedSegment) float64 {
	return math.Hypot(s.End.X-s.Start.X, s.End.Y-s.Start.Y)
}
func atDistance(s layout.RenderedSegment, d float64) layout.Point {
	l := segmentLength(s)
	return layout.Point{X: s.Start.X + (s.End.X-s.Start.X)*d/l, Y: s.Start.Y + (s.End.Y-s.Start.Y)*d/l}
}
func pointSegmentDistance(p layout.Point, s layout.RenderedSegment) float64 {
	if !validPoint(p) || !validPoint(s.Start) || !validPoint(s.End) {
		return math.Inf(1)
	}
	dx, dy := s.End.X-s.Start.X, s.End.Y-s.Start.Y
	l := dx*dx + dy*dy
	if l == 0 {
		return math.Hypot(p.X-s.Start.X, p.Y-s.Start.Y)
	}
	t := math.Max(0, math.Min(1, ((p.X-s.Start.X)*dx+(p.Y-s.Start.Y)*dy)/l))
	return math.Hypot(p.X-s.Start.X-t*dx, p.Y-s.Start.Y-t*dy)
}

// overBankPx is the ink kept beyond each side of an over crossing's gap span.
const overBankPx = 1

// expectedSegment is one drawn piece; restart marks a piece that begins an SVG
// subpath, where the dash pattern starts again (Chrome restarts even at a
// same-point move).
type expectedSegment struct {
	layout.RenderedSegment
	restart bool
}

// expectedSegments independently applies the persisted crossing-gap contract to
// each orthogonal segment; gaps never get inferred from a lucky dash hole. A
// dashed route restarts its dash one dash before each gap, so both banks are
// inked, and in dash steps across each crossing it passes over, so its ink is
// continuous across the gap span. This mirrors
// dashboard/src/graph/arranged/routedPath.ts:buildRoutedPath.
func expectedSegments(geo layout.Geometry, e layout.EdgeRoute) []expectedSegment {
	dash := ""
	if e.Kind != layout.EdgeForward {
		dash = geo.Contract.LoopReturnDash
	}
	return expectedRouteSegments(geo, e.Points, layout.ManifestRouteKey(e.Ordinal), dash)
}

func expectedRouteSegments(geo layout.Geometry, points []layout.Point, key, pattern string) []expectedSegment {
	out := []expectedSegment{}
	half := geo.Contract.CrossingGapPx / 2
	on := 0.0
	if dash, ok := parseDash(pattern); ok && len(dash) > 0 {
		on = dash[0]
	}
	crossings := slices.Clone(geo.GroupCrossings)
	for _, c := range geo.Crossings {
		crossings = append(crossings, layout.GroupCrossing{Point: c.Point, Over: layout.ManifestRouteKey(c.Over), Under: layout.ManifestRouteKey(c.Under)})
	}
	moved := true
	for i := 1; i < len(points); i++ {
		s := layout.RenderedSegment{Start: points[i-1], End: points[i]}
		length := segmentLength(s)
		type event struct {
			at  float64
			gap bool
		}
		var events []event
		for _, c := range crossings {
			if pointSegmentDistance(c.Point, s) >= .01 {
				continue
			}
			d := math.Hypot(c.Point.X-s.Start.X, c.Point.Y-s.Start.Y)
			if c.Under == key {
				events = append(events, event{d, true})
				if on > 0 {
					events = append(events, event{math.Max(0, d-half-on), false})
				}
			} else if c.Over == key && on > 0 {
				for r := d - half - overBankPx; ; r += on {
					events = append(events, event{math.Max(0, r), false})
					if r+on >= d+half+overBankPx {
						break
					}
				}
			}
		}
		// Order by where each event starts drawing, gaps first on ties.
		from := func(v event) float64 {
			if v.gap {
				return v.at - half
			}
			return v.at
		}
		sort.SliceStable(events, func(a, b int) bool {
			if from(events[a]) != from(events[b]) {
				return from(events[a]) < from(events[b])
			}
			return events[a].gap && !events[b].gap
		})
		cursor := 0.0
		piece := func(to float64) {
			out = append(out, expectedSegment{layout.RenderedSegment{Start: atDistance(s, cursor), End: atDistance(s, to)}, moved})
			moved = false
		}
		for _, v := range events {
			if !v.gap {
				if v.at >= length || v.at < cursor || (v.at == cursor && moved) {
					continue
				}
				if v.at > cursor {
					piece(v.at)
				}
				cursor, moved = v.at, true
				continue
			}
			start := math.Max(cursor, v.at-half)
			end := math.Min(length, v.at+half)
			if end <= cursor {
				continue
			}
			if start > cursor {
				piece(start)
			}
			cursor, moved = end, true
		}
		if cursor < length {
			out = append(out, expectedSegment{layout.RenderedSegment{Start: atDistance(s, cursor), End: s.End}, moved})
			moved = false
		}
	}
	return out
}
func parseDash(s string) ([]float64, bool) {
	if s == "" || s == "none" {
		return nil, true
	}
	parts := strings.Fields(strings.ReplaceAll(s, ",", " "))
	out := []float64{}
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSuffix(p, "px"), 64)
		if err != nil || !finite(v) || v <= 0 {
			return nil, false
		}
		out = append(out, v)
	}
	return out, len(out) > 0
}
func dashEqual(a, b string) bool {
	if a == "" {
		return false
	}
	x, ok := parseDash(a)
	y, ok2 := parseDash(b)
	if !ok || !ok2 || len(x) != len(y) {
		return false
	}
	for i := range x {
		if !near(x[i], y[i], .01) {
			return false
		}
	}
	return true
}
func colorRGB(s string) ([3]int, bool) {
	var r, g, b int
	if strings.HasPrefix(s, "#") && len(s) == 7 {
		v, e := strconv.ParseUint(s[1:], 16, 32)
		return [3]int{int(v >> 16), int(v>>8) & 255, int(v) & 255}, e == nil
	}
	n, e := fmt.Sscanf(s, "rgb(%d, %d, %d)", &r, &g, &b)
	return [3]int{r, g, b}, n == 3 && e == nil && r >= 0 && r <= 255 && g >= 0 && g <= 255 && b >= 0 && b <= 255
}
func onBorder(p layout.Point, side layout.Side, r layout.Rect) bool {
	if !validPoint(p) || !validRect(r) {
		return false
	}
	switch side {
	case layout.SideTop:
		return near(p.Y, r.Y, borderTolerancePx) && p.X > r.X && p.X < r.Right()
	case layout.SideBottom:
		return near(p.Y, r.Bottom(), borderTolerancePx) && p.X > r.X && p.X < r.Right()
	case layout.SideLeft:
		return near(p.X, r.X, borderTolerancePx) && p.Y > r.Y && p.Y < r.Bottom()
	case layout.SideRight:
		return near(p.X, r.Right(), borderTolerancePx) && p.Y > r.Y && p.Y < r.Bottom()
	}
	return false
}
func checkEdges(geo layout.Geometry, rep layout.RenderReport, f *findingSet) {
	boxes := map[string]layout.Rect{}
	for _, n := range rep.Nodes {
		boxes[layout.GroupElementID(n.ID, "header", "")] = n.Box
	}
	for _, e := range rep.GroupElements {
		boxes[e.ID] = e.Box
	}
	for i, e := range rep.Edges {
		want := geo.Edges[i]
		ids := []string{want.Source, want.Target}
		ords := []int{i}
		expected := expectedSegments(geo, want)
		match := len(expected) > 0 && len(e.Segments) == len(expected)
		for j, s := range e.Segments {
			if j >= len(expected) || !pointNear(s.Start, expected[j].Start, .05) || !pointNear(s.End, expected[j].End, .05) {
				match = false
			}
		}
		color, dash := geo.Contract.ForwardColor, "none"
		loop := want.Kind != layout.EdgeForward
		if loop {
			color = geo.Contract.LoopReturnColor
			dash = geo.Contract.LoopReturnDash
		}
		actual, ok := colorRGB(e.StrokeColor)
		wanted, _ := colorRGB(color)
		if !match || !e.Visible || !ok || actual != wanted || !finite(e.StrokePx) || e.StrokePx < geo.Contract.StrokePx-strokeTolerancePx {
			f.block(layout.CriterionRenderMismatch, ids, ords, "edge %d actual path/style differs from reviewed route", i)
		}
		if e.Dashed != loop || !dashEqual(e.DashPattern, dash) {
			f.block(layout.CriterionIndistinctLoop, ids, ords, "edge %d dash treatment does not distinguish its kind", i)
		}
		if len(want.Points) < 2 {
			f.block(layout.CriterionRenderMismatch, ids, ords, "edge %d has no candidate route", i)
			continue
		}
		if !pointNear(e.Start, want.Points[0], endpointTolerancePx) || !onBorder(e.Start, want.SourceAttach.Side, boxes[want.SourceAttach.ElementID]) {
			f.block(layout.CriterionAmbiguousAttachment, ids, ords, "edge %d start does not attach to its source", i)
		}
		if !pointNear(e.End, want.Points[len(want.Points)-1], endpointTolerancePx) {
			f.block(layout.CriterionAmbiguousAttachment, ids, ords, "edge %d end differs from route", i)
		}
		if !onBorder(e.End, want.TargetAttach.Side, boxes[want.TargetAttach.ElementID]) || !arrowMatches(e, want, geo.Contract.ArrowPx) {
			f.block(layout.CriterionArrowDirection, ids, ords, "edge %d arrow is missing, reversed or detached from target", i)
		}
		if want.Text != nil {
			if e.Label == nil {
				f.block(layout.CriterionIndistinctBranch, ids, ords, "edge %d branch label missing", i)
			} else {
				t := *e.Label
				checkText(t, geo.Contract, rep.Zoom, ids, ords, f)
				if t.Content != want.Text.Text {
					f.block(layout.CriterionRenderMismatch, ids, ords, "edge %d branch text differs", i)
				}
				if !rectNear(t.Box, want.Text.Box, boxTolerancePx) {
					f.block(layout.CriterionIndistinctBranch, ids, ords, "edge %d branch label displaced", i)
				}
				for _, n := range rep.Nodes {
					if glyphOverlap(t, n.Label) || glyphOverlap(t, n.Type) {
						f.block(layout.CriterionTextCollision, []string{n.ID}, ords, "branch text %d overlaps node text", i)
					}
				}
				for _, other := range rep.Edges[i+1:] {
					if other.Label != nil && glyphOverlap(t, *other.Label) {
						f.block(layout.CriterionTextCollision, ids, []int{i, other.Ordinal}, "branch text overlaps another branch")
					}
				}
				if rectRouteClearance(t.Box, e.Segments) < geo.Options.LabelGap-boxTolerancePx {
					f.block(layout.CriterionIndistinctBranch, ids, ords, "edge %d branch label lacks route clearance", i)
				}
				own := routeDistance(t.Box, e.Segments)
				for j, other := range rep.Edges {
					if j != i && routeDistance(t.Box, other.Segments)+boxTolerancePx < own {
						f.block(layout.CriterionIndistinctBranch, ids, ords, "edge %d label is closer to edge %d", i, j)
						break
					}
				}
			}
		} else if e.Label != nil {
			f.block(layout.CriterionRenderMismatch, ids, ords, "edge %d has an unexpected label", i)
		}
		for _, s := range e.Segments {
			for _, n := range rep.Nodes {
				if n.ID != want.Source && n.ID != want.Target && segmentHitsRect(s, n.Box) {
					f.block(layout.CriterionEdgeThroughNode, []string{n.ID}, ords, "edge %d crosses unrelated node %q", i, n.ID)
				}
			}
			for _, n := range rep.Nodes {
				for _, t := range []layout.RenderedText{n.Label, n.Type} {
					for _, g := range t.Glyphs {
						if segmentHitsRect(s, g) {
							f.block(layout.CriterionTextCollision, []string{n.ID}, ords, "edge %d crosses text", i)
							break
						}
					}
				}
			}
			for _, other := range rep.Edges {
				if other.Label != nil && segmentHitsRect(s, other.Label.Box) {
					f.block(layout.CriterionTextCollision, ids, []int{i, other.Ordinal}, "edge %d crosses branch label %d", i, other.Ordinal)
				}
			}
		}
		for j := i + 1; j < len(rep.Edges); j++ {
			other := rep.Edges[j]
			w := geo.Edges[j]
			if routesOverlap(e.Segments, other.Segments) {
				f.block(layout.CriterionMergedLines, nil, []int{i, j}, "edge paths overlap and conceal their separate destinations")
			}
			if want.Target == w.Target && pointNear(e.End, other.End, endpointTolerancePx) {
				f.block(layout.CriterionAmbiguousAttachment, []string{want.Target}, []int{i, j}, "edges merge at their destination")
			}
			if want.Source == w.Source && pointNear(e.Start, other.Start, endpointTolerancePx) {
				f.block(layout.CriterionAmbiguousAttachment, []string{want.Source}, []int{i, j}, "edges merge at their source")
			}
		}
	}
}
func arrowMatches(e layout.RenderedEdge, w layout.EdgeRoute, size float64) bool {
	if !e.HasArrow || len(e.ArrowPoints) != 3 || len(w.Points) < 2 {
		return false
	}
	end := w.Points[len(w.Points)-1]
	start := w.Points[len(w.Points)-2]
	dx, dy := end.X-start.X, end.Y-start.Y
	l := math.Hypot(dx, dy)
	dx /= l
	dy /= l
	expected := []layout.Point{{X: end.X - dx*size + dy*size/2, Y: end.Y - dy*size - dx*size/2}, end, {X: end.X - dx*size - dy*size/2, Y: end.Y - dy*size + dx*size/2}}
	for i := range expected {
		if !pointNear(expected[i], e.ArrowPoints[i], endpointTolerancePx) {
			return false
		}
	}
	return true
}
func segmentHitsRect(s layout.RenderedSegment, r layout.Rect) bool {
	if !validRect(r) || !validPoint(s.Start) || !validPoint(s.End) {
		return false
	}
	if near(s.Start.X, s.End.X, .01) {
		return s.Start.X > r.X+.01 && s.Start.X < r.Right()-.01 && math.Max(s.Start.Y, s.End.Y) > r.Y+.01 && math.Min(s.Start.Y, s.End.Y) < r.Bottom()-.01
	}
	if near(s.Start.Y, s.End.Y, .01) {
		return s.Start.Y > r.Y+.01 && s.Start.Y < r.Bottom()-.01 && math.Max(s.Start.X, s.End.X) > r.X+.01 && math.Min(s.Start.X, s.End.X) < r.Right()-.01
	}
	return false
}
func rectRouteClearance(r layout.Rect, segs []layout.RenderedSegment) float64 {
	if !validRect(r) {
		return math.Inf(1)
	}
	d := math.Inf(1)
	for _, s := range segs {
		dx := math.Max(0, math.Max(r.X-math.Max(s.Start.X, s.End.X), math.Min(s.Start.X, s.End.X)-r.Right()))
		dy := math.Max(0, math.Max(r.Y-math.Max(s.Start.Y, s.End.Y), math.Min(s.Start.Y, s.End.Y)-r.Bottom()))
		d = math.Min(d, math.Hypot(dx, dy))
	}
	return d
}
func routesOverlap(a, b []layout.RenderedSegment) bool {
	for _, s := range a {
		for _, t := range b {
			if !validPoint(s.Start) || !validPoint(s.End) || !validPoint(t.Start) || !validPoint(t.End) {
				continue
			}
			if near(s.Start.X, s.End.X, .01) && near(t.Start.X, t.End.X, .01) && near(s.Start.X, t.Start.X, .01) && math.Min(math.Max(s.Start.Y, s.End.Y), math.Max(t.Start.Y, t.End.Y))-math.Max(math.Min(s.Start.Y, s.End.Y), math.Min(t.Start.Y, t.End.Y)) > endpointTolerancePx {
				return true
			}
			if near(s.Start.Y, s.End.Y, .01) && near(t.Start.Y, t.End.Y, .01) && near(s.Start.Y, t.Start.Y, .01) && math.Min(math.Max(s.Start.X, s.End.X), math.Max(t.Start.X, t.End.X))-math.Max(math.Min(s.Start.X, s.End.X), math.Min(t.Start.X, t.End.X)) > endpointTolerancePx {
				return true
			}
		}
	}
	return false
}
func routeDistance(r layout.Rect, segs []layout.RenderedSegment) float64 {
	d := math.Inf(1)
	for _, s := range segs {
		d = math.Min(d, pointSegmentDistance(layout.Point{X: r.CenterX(), Y: r.CenterY()}, s))
	}
	return d
}
func checkCrossings(geo layout.Geometry, rep layout.RenderReport, f *findingSet) {
	good := true
	for i, c := range rep.Crossings {
		want := geo.Crossings[i]
		if c.Over != want.Over || c.Under != want.Under || !pointNear(c.Point, want.Point, .01) {
			f.block(layout.CriterionRenderMismatch, nil, []int{want.Over, want.Under}, "crossing %d differs from candidate", i)
			good = false
		}
		if !c.UnderGapVisible || !c.OverContinuous {
			f.block(layout.CriterionAmbiguousCrossing, nil, []int{want.Over, want.Under}, "crossing %d lacks a visible under gap or continuous over edge", i)
			good = false
		}
	}
	for i, c := range rep.GroupCrossings {
		want := geo.GroupCrossings[i]
		if !c.UnderGapVisible || !c.OverContinuous {
			f.block(layout.CriterionAmbiguousCrossing, crossingOwners(geo, want), nil, "crossing %s / %s lacks a visible under gap or continuous over edge", want.Over, want.Under)
			good = false
		}
	}
	if good && f.blocking() == 0 && layout.CrossingCount(geo) > 0 {
		f.add(layout.CriterionCrossings, layout.SeverityAdvisory, nil, nil, "%d crossing(s) remain in the rendered layout", layout.CrossingCount(geo))
	}
}
func checkStates(geo layout.Geometry, rep layout.RenderReport, f *findingSet) {
	for _, s := range rep.States {
		var nodes []string
		var edges []int
		for _, n := range rep.Nodes {
			for _, issue := range s.Issues {
				if strings.Contains(issue, "node "+n.ID+" ") {
					nodes = append(nodes, n.ID)
					break
				}
			}
		}
		for _, e := range rep.Edges {
			for _, issue := range s.Issues {
				if strings.Contains(issue, fmt.Sprintf("edge %d ", e.Ordinal)) {
					edges = append(edges, e.Ordinal)
					break
				}
			}
		}
		for _, e := range geo.GroupConnectors {
			for _, issue := range s.Issues {
				if strings.Contains(issue, e.ID) && !slices.Contains(nodes, e.GroupNodeID) {
					nodes = append(nodes, e.GroupNodeID)
				}
			}
		}
		for _, e := range geo.GroupElements {
			// The canonical prefix also addresses dynamic overlay keys, which are
			// measured alternatives rather than static Geometry.Texts entries.
			prefix := strings.TrimSuffix(layout.GroupTextKey(e.ID, ""), `,""]`)
			for _, issue := range s.Issues {
				if strings.Contains(issue, prefix) && !slices.Contains(nodes, e.GroupNodeID) {
					nodes = append(nodes, e.GroupNodeID)
				}
			}
		}
		// Group cases mount every captured group. A bare failed flag still
		// needs real owners when no detailed issue identifies a narrower one.
		if len(nodes) == 0 && (strings.HasPrefix(s.State, "group:") || strings.HasPrefix(s.State, `["group-probe",`)) {
			for _, n := range geo.Nodes {
				if n.TypeCaption == "parallel group" {
					nodes = append(nodes, n.ID)
				}
			}
		}
		if !s.NodeBoxesStable || len(s.Issues) > 0 {
			f.block(layout.CriterionRenderMismatch, nodes, edges, "runtime state %q changes geometry/markers: %v", s.State, s.Issues)
		}
		if !s.LabelsReadable {
			f.block(layout.CriterionTextUnreadable, nodes, edges, "runtime state %q obscures text: %v", s.State, s.Issues)
		}
	}
}
