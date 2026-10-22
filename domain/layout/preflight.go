package layout

import (
	"fmt"
	"math"
	"sort"
)

type addFinding func(c Criterion, sev Severity, nodes []string, edges []int, format string, args ...any)

// Preflight checks a candidate against the acceptance criteria using geometry
// and measurements only. It is not visual review: a candidate with no
// preflight findings must still pass rendered readability review.
func Preflight(g Graph, geo Geometry, m Measurements) []Finding {
	fs := []Finding{}
	add := func(c Criterion, sev Severity, nodes []string, edges []int, format string, args ...any) {
		fs = append(fs, Finding{Criterion: c, Severity: sev, Source: SourcePreflight, NodeIDs: nodes, EdgeOrdinals: edges, Evidence: fmt.Sprintf(format, args...)})
	}
	if err := geo.Validate(g); err != nil {
		var groups []string
		if g.Groups != nil {
			groups = append(groups, g.Groups.Order...)
		}
		add(CriterionCoverage, SeverityBlocking, groups, nil, "%v", err)
		return fs
	}
	if m.Contract != geo.Contract || !m.FontsReady {
		add(CriterionCoverage, SeverityBlocking, nil, nil, "measurements do not match candidate contract or fonts are not ready")
		return fs
	}
	if g.Groups != nil {
		checkReadable(geo.Contract, add)
		checkTextFits(geo, m, add)
		checkGroupHeaderTextFits(g, geo, m, add)
		checkLabels(geo, add)
		fs = append(fs, preflightGroups(g, geo, m)...)
		for i := range fs {
			f := &fs[i]
			if len(f.NodeIDs) == 0 {
				f.NodeIDs = append([]string{}, g.Groups.Order...)
			}
		}
		return fs
	}
	checkReadable(geo.Contract, add)
	checkNodeOverlap(geo, add)
	checkRoutesAvoidNodes(geo, add)
	checkLabels(geo, add)
	checkTextFits(geo, m, add)
	checkGroupHeaderTextFits(g, geo, m, add)
	checkMergedLines(geo, add)
	checkAttachments(geo, add)
	checkArrows(geo, add)
	checkCrossings(geo, add)
	checkProgression(geo, add)
	checkLoops(g, geo, add)
	return fs
}

func checkReadable(c RendererContract, add addFinding) {
	for _, t := range []struct {
		name string
		px   float64
	}{{"node label", c.NodeLabelFontPx}, {"node type caption", c.NodeTypeFontPx}, {"branch label", c.EdgeLabelFontPx}} {
		if t.px*c.PracticalZoom < c.MinReadableTextPx {
			add(CriterionTextUnreadable, SeverityBlocking, nil, nil,
				"%s renders at %.1fpx at practical zoom %.2f (minimum %.1fpx)", t.name, t.px*c.PracticalZoom, c.PracticalZoom, c.MinReadableTextPx)
		}
	}
}

func checkNodeOverlap(geo Geometry, add addFinding) {
	half := geo.Contract.MinClearancePx / 2
	for i := range geo.Nodes {
		for j := i + 1; j < len(geo.Nodes); j++ {
			a, b := geo.Nodes[i], geo.Nodes[j]
			if a.Box.Inflate(half).Overlaps(b.Box.Inflate(half)) {
				add(CriterionNodeOverlap, SeverityBlocking, []string{a.ID, b.ID}, nil,
					"%q and %q overlap or are closer than %.0fpx", a.ID, b.ID, geo.Contract.MinClearancePx)
			}
		}
	}
}

// segmentHitsRect reports whether an axis-aligned segment passes through the
// interior of r (running along r's boundary does not count).
func segmentHitsRect(a, b Point, r Rect) bool {
	if nearly(a.X, b.X) {
		y1, y2 := math.Min(a.Y, b.Y), math.Max(a.Y, b.Y)
		return a.X > r.X && a.X < r.Right() && y1 < r.Bottom() && y2 > r.Y
	}
	if nearly(a.Y, b.Y) {
		x1, x2 := math.Min(a.X, b.X), math.Max(a.X, b.X)
		return a.Y > r.Y && a.Y < r.Bottom() && x1 < r.Right() && x2 > r.X
	}
	return true // non-orthogonal segments are rejected by Validate
}

func checkRoutesAvoidNodes(geo Geometry, add addFinding) {
	for _, e := range geo.Edges {
		last := len(e.Points) - 1
		for s := 1; s <= last; s++ {
			for _, n := range geo.Nodes {
				if (s == 1 && n.ID == e.Source) || (s == last && n.ID == e.Target) {
					continue
				}
				if segmentHitsRect(e.Points[s-1], e.Points[s], n.Box) {
					add(CriterionEdgeThroughNode, SeverityBlocking, []string{n.ID}, []int{e.Ordinal},
						"edge %d (%s→%s) segment %d passes through %q", e.Ordinal, e.Source, e.Target, s, n.ID)
				}
			}
		}
	}
}

func checkLabels(geo Geometry, add addFinding) {
	clear := geo.Contract.MinClearancePx / 2
	for _, e := range geo.Edges {
		if e.Text == nil {
			continue
		}
		box := e.Text.Box
		for _, n := range geo.Nodes {
			if box.Inflate(clear).Overlaps(n.Box.Inflate(clear)) {
				add(CriterionTextCollision, SeverityBlocking, []string{n.ID}, []int{e.Ordinal}, "label of edge %d overlaps node %q", e.Ordinal, n.ID)
			}
		}
		for _, o := range geo.Edges {
			if o.Text == nil || o.Ordinal <= e.Ordinal {
				continue
			}
			if box.Inflate(clear).Overlaps(o.Text.Box.Inflate(clear)) {
				add(CriterionTextCollision, SeverityBlocking, nil, []int{e.Ordinal, o.Ordinal}, "labels of edges %d and %d overlap", e.Ordinal, o.Ordinal)
			}
		}
		for _, o := range geo.Edges {
			if o.Ordinal == e.Ordinal {
				continue
			}
			for s := 1; s < len(o.Points); s++ {
				if segmentHitsRect(o.Points[s-1], o.Points[s], box) {
					add(CriterionTextCollision, SeverityBlocking, nil, []int{e.Ordinal, o.Ordinal}, "edge %d passes through the label of edge %d", o.Ordinal, e.Ordinal)
					break
				}
			}
		}
		// Labels are beside their own route, never centered over its stroke.
		// Compute the minimum distance from the label Rect to each orthogonal
		// segment of its own edge. Require the nearest segment to be within
		// geo.Options.LabelGap+geomEps, and no foreign segment to be equally
		// close or closer (ties are ambiguous). Distances use the full segment,
		// not only vertices; inside/intersection distance is zero.
		associated := labelAssociated(geo, e)
		if !associated {
			add(CriterionIndistinctBranch, SeverityBlocking, nil, []int{e.Ordinal}, "label %q is not attached to its own edge %d", e.Text.Text, e.Ordinal)
		}
	}
}

func checkTextFits(geo Geometry, m Measurements, add addFinding) {
	c := geo.Contract
	for _, n := range geo.Nodes {
		label, err1 := metricForText(m, NodeLabelKey(n.ID), n.Text, c.NodeLabelFontPx, c.LineHeight, c.NodeTextMaxWidth)
		caption, err2 := metricForText(m, NodeTypeKey(n.ID), n.TypeCaption, c.NodeTypeFontPx, c.LineHeight, c.NodeTextMaxWidth)
		if err1 != nil || err2 != nil {
			add(CriterionCoverage, SeverityBlocking, []string{n.ID}, nil, "missing text measurement for %q", n.ID)
			continue
		}
		innerW := n.Box.W - 2*c.NodePaddingX - 2*c.NodeBorderPx
		innerH := n.Box.H - 2*c.NodeBorderPx - c.NodeTypeBarPx - 2*c.NodePaddingY
		if n.TypeCaption != "" {
			innerH -= c.NodeTextGapPx
		}
		if innerW+geomEps < math.Ceil(math.Max(label.Width, caption.Width)) || innerH+geomEps < math.Ceil(label.Height)+math.Ceil(caption.Height) {
			add(CriterionTextClipped, SeverityBlocking, []string{n.ID}, nil, "box %.0f×%.0f is smaller than the text of %q", n.Box.W, n.Box.H, n.ID)
		}
	}
	for _, e := range geo.Edges {
		if e.Text == nil {
			continue
		}
		tm, err := metricForText(m, EdgeLabelKey(e.Ordinal), e.Text.Text, c.EdgeLabelFontPx, c.LineHeight, c.EdgeLabelMaxWidth)
		if err != nil {
			add(CriterionCoverage, SeverityBlocking, nil, []int{e.Ordinal}, "missing text measurement for edge %d", e.Ordinal)
			continue
		}
		if e.Text.Box.W-2*c.EdgeLabelPaddingX+geomEps < math.Ceil(tm.Width) || e.Text.Box.H-2*c.EdgeLabelPaddingY+geomEps < math.Ceil(tm.Height) {
			add(CriterionTextClipped, SeverityBlocking, nil, []int{e.Ordinal}, "label box of edge %d is smaller than %q", e.Ordinal, e.Text.Text)
		}
	}
}

func checkMergedLines(geo Geometry, add addFinding) {
	minSep := geo.Contract.StrokePx + geo.Contract.MinClearancePx
	segs := segmentsOf(geo.Edges)
	reported := map[[2]int]bool{}
	for i := range segs {
		for j := i + 1; j < len(segs); j++ {
			a, b := segs[i], segs[j]
			if a.ord == b.ord || a.horiz != b.horiz {
				continue
			}
			gap, overlap := parallelDistance(a, b)
			key := [2]int{min(a.ord, b.ord), max(a.ord, b.ord)}
			if gap < minSep && overlap > geomEps && !reported[key] {
				reported[key] = true
				add(CriterionMergedLines, SeverityBlocking, nil, []int{key[0], key[1]},
					"edges %d and %d run %.1fpx apart for %.0fpx, concealing their destinations", key[0], key[1], gap, overlap)
			}
		}
	}
}

func checkAttachments(geo Geometry, add addFinding) {
	boxes := map[string]Rect{}
	for _, n := range geo.Nodes {
		boxes[n.ID] = n.Box
	}
	type att struct {
		ord   int
		along float64
	}
	groups := map[string][]att{}
	for _, e := range geo.Edges {
		for _, a := range []Attachment{e.SourceAttach, e.TargetAttach} {
			if !onSide(a.Point, a.Side, boxes[a.NodeID]) {
				add(CriterionAmbiguousAttachment, SeverityBlocking, []string{a.NodeID}, []int{e.Ordinal},
					"edge %d attaches off the %s side of %q", e.Ordinal, a.Side, a.NodeID)
				continue
			}
			along := a.Point.X
			if a.Side == SideLeft || a.Side == SideRight {
				along = a.Point.Y
			}
			k := a.NodeID + "|" + string(a.Side)
			groups[k] = append(groups[k], att{ord: e.Ordinal, along: along})
		}
	}
	minSep := geo.Contract.StrokePx + geo.Contract.MinClearancePx
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		as := groups[k]
		sort.SliceStable(as, func(i, j int) bool { return as[i].along < as[j].along })
		for i := 1; i < len(as); i++ {
			if as[i].along-as[i-1].along < minSep {
				add(CriterionAmbiguousAttachment, SeverityBlocking, nil, []int{as[i-1].ord, as[i].ord},
					"edges %d and %d share an attachment point on %s", as[i-1].ord, as[i].ord, k)
			}
		}
	}
}

func checkArrows(geo Geometry, add addFinding) {
	for _, e := range geo.Edges {
		n := len(e.Points)
		p, q := e.Points[n-2], e.Points[n-1]
		in := false
		switch e.TargetAttach.Side {
		case SideTop:
			in = nearly(p.X, q.X) && q.Y > p.Y
		case SideBottom:
			in = nearly(p.X, q.X) && q.Y < p.Y
		case SideLeft:
			in = nearly(p.Y, q.Y) && q.X > p.X
		case SideRight:
			in = nearly(p.Y, q.Y) && q.X < p.X
		}
		a, b := e.Points[0], e.Points[1]
		out := false
		switch e.SourceAttach.Side {
		case SideBottom:
			out = nearly(a.X, b.X) && b.Y > a.Y
		case SideTop:
			out = nearly(a.X, b.X) && b.Y < a.Y
		case SideRight:
			out = nearly(a.Y, b.Y) && b.X > a.X
		case SideLeft:
			out = nearly(a.Y, b.Y) && b.X < a.X
		}
		if !in || !out {
			add(CriterionArrowDirection, SeverityBlocking, []string{e.Source, e.Target}, []int{e.Ordinal},
				"edge %d does not leave %q and enter %q through their attachment sides", e.Ordinal, e.Source, e.Target)
		}
	}
}

// crossingNearBend reports whether a crossing lies within radius of any vertex
// of either crossing edge, where a gap could be mistaken for a junction.
func crossingNearBend(c Crossing, routes []EdgeRoute, radius float64) bool {
	for _, r := range routes {
		if r.Ordinal != c.Over && r.Ordinal != c.Under {
			continue
		}
		for _, p := range r.Points {
			if math.Hypot(p.X-c.Point.X, p.Y-c.Point.Y) < radius {
				return true
			}
		}
	}
	return false
}

// checkCrossings checks every perpendicular contact, not just proper interior
// intersections. Gaps cannot turn a bend or T-touch into a traceable crossing.
func checkCrossings(geo Geometry, add addFinding) {
	actual := findCrossings(geo.Edges)
	ambiguous := false
	bad := func(edges []int, format string, args ...any) {
		ambiguous = true
		add(CriterionAmbiguousCrossing, SeverityBlocking, nil, edges, format, args...)
	}
	matches := func(a, b Crossing) bool {
		return a.Over == b.Over && a.Under == b.Under && samePoint(a.Point, b.Point)
	}
	for i, c := range geo.Crossings {
		valid := false
		for _, a := range actual {
			valid = valid || matches(c, a)
		}
		for _, prev := range geo.Crossings[:i] {
			if matches(c, prev) {
				valid = false
			}
		}
		if !valid {
			bad([]int{c.Over, c.Under}, "false or duplicate crossing annotation at (%.2f, %.2f)", c.Point.X, c.Point.Y)
		}
	}
	segs := segmentsOf(geo.Edges)
	for i, a := range segs {
		for _, b := range segs[i+1:] {
			if a.ord == b.ord {
				continue
			}
			p, contact := intersection(a, b)
			if !contact || sharedBoundary(geo, a.ord, b.ord, p) {
				continue
			}
			if !segmentInterior(p, a) || !segmentInterior(p, b) {
				bad([]int{a.ord, b.ord}, "edges %d and %d meet at a bend/endpoint (%.2f, %.2f)", a.ord, b.ord, p.X, p.Y)
			}
		}
	}
	radius := geo.Contract.CrossingGapPx/2 + geo.Contract.StrokePx
	for i, c := range actual {
		treated := false
		for _, ann := range geo.Crossings {
			treated = treated || matches(c, ann)
		}
		if !treated || geo.Contract.CrossingTreatment != "gap" {
			bad([]int{c.Over, c.Under}, "crossing at (%.2f, %.2f) has no gap treatment", c.Point.X, c.Point.Y)
		}
		if crossingNearBend(c, geo.Edges, radius) {
			bad([]int{c.Over, c.Under}, "crossing at (%.2f, %.2f) is too close to a bend/attachment", c.Point.X, c.Point.Y)
		}
		for _, e := range geo.Edges {
			if e.Ordinal != c.Over && e.Ordinal != c.Under {
				continue
			}
			pos, length := routePosition(c.Point, e.Points)
			if length-pos < geo.Contract.ArrowPx+radius-geomEps {
				bad([]int{e.Ordinal}, "crossing interferes with edge %d's arrow", e.Ordinal)
			}
			if e.Ordinal == c.Under {
				for _, other := range actual[i+1:] {
					if other.Under != c.Under {
						continue
					}
					otherPos, _ := routePosition(other.Point, e.Points)
					if math.Abs(pos-otherPos) < 2*radius-geomEps {
						bad([]int{c.Under, c.Over, other.Over}, "gap intervals overlap on edge %d", c.Under)
					}
				}
			}
		}
	}
	if len(actual) > 0 && !ambiguous {
		add(CriterionCrossings, SeverityAdvisory, nil, nil, "%d remaining, gap-treated crossings", len(actual))
	}
}

func sharedBoundary(geo Geometry, a, b int, p Point) bool {
	var aa, bb []Attachment
	for _, e := range geo.Edges {
		if e.Ordinal == a {
			aa = []Attachment{e.SourceAttach, e.TargetAttach}
		}
		if e.Ordinal == b {
			bb = []Attachment{e.SourceAttach, e.TargetAttach}
		}
	}
	for _, x := range aa {
		for _, y := range bb {
			if x.NodeID != y.NodeID || !samePoint(x.Point, p) || !samePoint(y.Point, p) {
				continue
			}
			for _, n := range geo.Nodes {
				if n.ID == x.NodeID && onSide(p, x.Side, n.Box) && onSide(p, y.Side, n.Box) {
					return true
				}
			}
		}
	}
	return false
}

// routePosition returns arc length to p and total orthogonal route length.
func routePosition(p Point, points []Point) (position, length float64) {
	position = math.Inf(1)
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		if containsPoint(Rect{X: math.Min(a.X, b.X), Y: math.Min(a.Y, b.Y), W: math.Abs(a.X - b.X), H: math.Abs(a.Y - b.Y)}, p) {
			position = math.Min(position, length+math.Abs(p.X-a.X)+math.Abs(p.Y-a.Y))
		}
		length += math.Abs(b.X-a.X) + math.Abs(b.Y-a.Y)
	}
	return position, length
}

func checkProgression(geo Geometry, add addFinding) {
	boxes := map[string]Rect{}
	for _, n := range geo.Nodes {
		boxes[n.ID] = n.Box
	}
	for _, e := range geo.Edges {
		if e.Kind != EdgeForward {
			continue
		}
		if boxes[e.Target].Y <= boxes[e.Source].Bottom() {
			add(CriterionUpwardProgression, SeverityBlocking, []string{e.Source, e.Target}, []int{e.Ordinal},
				"forward edge %d leads from %q to %q, which is not below it", e.Ordinal, e.Source, e.Target)
			continue
		}
		for i := 1; i < len(e.Points); i++ {
			if e.Points[i].Y < e.Points[i-1].Y-geomEps {
				add(CriterionUpwardProgression, SeverityBlocking, nil, []int{e.Ordinal}, "forward edge %d climbs at segment %d", e.Ordinal, i)
				break
			}
		}
	}
}

func checkLoops(g Graph, geo Geometry, add addFinding) {
	kinds := classifyEdges(g)
	left, right := math.Inf(1), math.Inf(-1)
	for _, n := range geo.Nodes {
		left, right = math.Min(left, n.Box.X), math.Max(right, n.Box.Right())
	}
	minSep := geo.Contract.StrokePx + geo.Contract.MinClearancePx
	type gutter struct {
		ord, lane      int
		side           Side
		x, top, bottom float64
	}
	var gutters []gutter
	for _, e := range geo.Edges {
		bad := func(reason string) {
			add(CriterionIndistinctLoop, SeverityBlocking, []string{e.Source, e.Target}, []int{e.Ordinal}, "edge %d: %s", e.Ordinal, reason)
		}
		if e.Ordinal < 0 || e.Ordinal >= len(kinds) || e.Kind != kinds[e.Ordinal] {
			bad("route kind disagrees with graph classification")
		}
		switch e.Kind {
		case EdgeLoopReturn:
			found := false
			for i := 1; i < len(e.Points); i++ {
				a, b := e.Points[i-1], e.Points[i]
				exterior := (e.Side == SideLeft && a.X < left-geomEps) || (e.Side == SideRight && a.X > right+geomEps)
				if nearly(a.X, b.X) && b.Y < a.Y-geomEps && exterior && e.Lane > 0 {
					found = true
					gutters = append(gutters, gutter{e.Ordinal, e.Lane, e.Side, a.X, b.Y, a.Y})
				}
			}
			if !found {
				bad("no upward vertical in the declared exterior gutter with a positive lane")
			}
		case EdgeSelfLoop:
			var box Rect
			for _, n := range geo.Nodes {
				if n.ID == e.Source {
					box = n.Box
				}
			}
			if len(e.Points) < 4 || e.Side != SideRight || !onSide(e.SourceAttach.Point, SideRight, box) || !onSide(e.TargetAttach.Point, SideRight, box) || math.Abs(e.SourceAttach.Point.Y-e.TargetAttach.Point.Y) < minSep {
				bad("self-loop ports are not distinct on the right boundary")
			}
			for i := 1; i < len(e.Points); i++ {
				a, b := e.Points[i-1], e.Points[i]
				if math.Min(a.X, b.X) < box.Right()-geomEps || segmentHitsRect(a, b, box) || (nearly(a.X, b.X) && a.X <= box.Right()+geomEps) {
					bad("self-loop body is not outside the right boundary")
					break
				}
			}
		}
	}
	for i, a := range gutters {
		for _, b := range gutters[i+1:] {
			if a.ord != b.ord && a.side == b.side && a.top < b.bottom && b.top < a.bottom && (a.lane == b.lane || math.Abs(a.x-b.x) < minSep) {
				add(CriterionIndistinctLoop, SeverityBlocking, nil, []int{a.ord, b.ord}, "overlapping loop returns share a gutter lane")
			}
		}
	}
	// Nested bodies can share nodes, but self-loop strokes must remain separate.
	selfLoops := make(map[int]bool)
	for _, e := range geo.Edges {
		selfLoops[e.Ordinal] = e.Kind == EdgeSelfLoop
	}
	segs := segmentsOf(geo.Edges)
	for i, a := range segs {
		for _, b := range segs[i+1:] {
			if a.ord == b.ord {
				continue
			}
			if !selfLoops[a.ord] && !selfLoops[b.ord] {
				continue
			}
			_, contact := intersection(a, b)
			if a.horiz == b.horiz {
				gap, overlap := parallelDistance(a, b)
				contact = gap < minSep && overlap > geomEps
			}
			if contact {
				add(CriterionIndistinctLoop, SeverityBlocking, nil, []int{a.ord, b.ord}, "self-loop bodies overlap or intersect another route")
			}
		}
	}
}

func parallelDistance(a, b segment) (gap, overlap float64) {
	if a.horiz {
		return math.Abs(a.a.Y - b.a.Y), math.Min(math.Max(a.a.X, a.b.X), math.Max(b.a.X, b.b.X)) - math.Max(math.Min(a.a.X, a.b.X), math.Min(b.a.X, b.b.X))
	}
	return math.Abs(a.a.X - b.a.X), math.Min(math.Max(a.a.Y, a.b.Y), math.Max(b.a.Y, b.b.Y)) - math.Max(math.Min(a.a.Y, a.b.Y), math.Min(b.a.Y, b.b.Y))
}

// labelAssociated compares the whole label box with every full route segment.
// Equal foreign proximity is ambiguous, including for self-loop labels.
func labelAssociated(geo Geometry, edge EdgeRoute) bool {
	if edge.Text == nil || !positiveRect(edge.Text.Box) {
		return false
	}
	box := edge.Text.Box
	nearest := func(points []Point) float64 {
		d := math.Inf(1)
		for i := 1; i < len(points); i++ {
			a, b := points[i-1], points[i]
			if !finitePoint(a) || !finitePoint(b) {
				return math.Inf(1)
			}
			dx := math.Max(0, math.Max(box.X-math.Max(a.X, b.X), math.Min(a.X, b.X)-box.Right()))
			dy := math.Max(0, math.Max(box.Y-math.Max(a.Y, b.Y), math.Min(a.Y, b.Y)-box.Bottom()))
			d = math.Min(d, math.Hypot(dx, dy))
		}
		return d
	}
	own := nearest(edge.Points)
	if own <= geomEps || own > geo.Options.LabelGap+geomEps {
		return false
	}
	for _, other := range geo.Edges {
		if other.Ordinal != edge.Ordinal && nearest(other.Points) <= own+geomEps {
			return false
		}
	}
	return true
}
