package layout

import (
	"container/heap"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/arbitron/arbitron/domain/workflow"
)

type groupPath struct {
	key, source, target    string
	ordinal                int
	connector              int
	kind                   EdgeKind
	label                  *LabelBox
	points                 []Point
	sa, ta                 Attachment
	sourceLead, targetLead float64
}

func groupElementBoxes(geo Geometry) map[string]Rect {
	out := map[string]Rect{}
	for _, n := range geo.Nodes {
		out[GroupElementID(n.ID, "header", "")] = n.Box
	}
	for _, e := range geo.GroupElements {
		out[e.ID] = e.Box
	}
	return out
}

func routeGroupGeometry(g Graph, geo *Geometry, m Measurements) error {
	c, o := geo.Contract, geo.Options
	boxes := groupElementBoxes(*geo)
	labels, err := edgeLabelSizes(g, m, c, o)
	if err != nil {
		return err
	}
	paths := []groupPath{}
	for _, e := range geo.Edges {
		source := GroupElementID(e.Source, "header", "")
		if g.Groups.Groups[e.Source] != nil && e.Output != workflow.GroupPortTask {
			source = GroupElementID(e.Source, "footer", "")
		}
		p := groupPath{key: ManifestRouteKey(e.Ordinal), source: source, target: GroupElementID(e.Target, "header", ""), ordinal: e.Ordinal, connector: -1, kind: e.Kind}
		p.sa.NodeID = e.Source
		p.ta.NodeID = e.Target
		if l, ok := labels[e.Ordinal]; ok {
			p.label = &LabelBox{Text: g.Edges[e.Ordinal].Text, Box: Rect{W: l.W, H: l.H}, Lines: l.Lines}
		}
		paths = append(paths, p)
	}
	for i, e := range geo.GroupConnectors {
		paths = append(paths, groupPath{key: e.ID, source: e.SourceElementID, target: e.TargetElementID, ordinal: -1, connector: i, kind: EdgeForward})
	}
	type endpoint struct {
		path   int
		source bool
		slot   float64
	}
	ports := map[string][]endpoint{}
	rightStep := map[string]float64{}
	for _, p := range paths {
		if p.kind == EdgeSelfLoop {
			step := o.PortSpacing
			if p.label != nil {
				step = math.Max(step, p.label.Box.H+2*o.LabelGap+c.MinClearancePx)
			}
			rightStep[p.source] = math.Max(rightStep[p.source], step)
		}
	}
	for i, p := range paths {
		sideS, sideT := SideBottom, SideTop
		if p.kind == EdgeSelfLoop {
			sideS, sideT = SideRight, SideRight
		}
		paths[i].sa.ElementID = p.source
		paths[i].sa.Side = sideS
		paths[i].ta.ElementID = p.target
		paths[i].ta.Side = sideT
		slot, targetSlot := o.PortSpacing, o.PortSpacing
		if sideS == SideRight {
			slot = rightStep[p.source]
			targetSlot = slot
		}
		if p.label != nil && sideS == SideBottom {
			slot += o.LabelGap + p.label.Box.W
		}
		ports[groupIDJSON(p.source, string(sideS))] = append(ports[groupIDJSON(p.source, string(sideS))], endpoint{i, true, slot})
		ports[groupIDJSON(p.target, string(sideT))] = append(ports[groupIDJSON(p.target, string(sideT))], endpoint{i, false, targetSlot})
	}
	for _, entries := range ports {
		first := paths[entries[0].path]
		id, side := first.target, first.ta.Side
		if entries[0].source {
			id, side = first.source, first.sa.Side
		}
		box := boxes[id]
		endGap := o.PortSpacing
		if side == SideRight {
			endGap = rightStep[id]
		}
		total := endGap
		for _, e := range entries {
			total += e.slot
		}
		extent := box.W
		if side == SideRight {
			extent = box.H
		}
		if total > extent+geomEps {
			return fmt.Errorf("layout: element %s needs %.2fpx of ports, has %.2f", id, total, extent)
		}
		pos := (extent-total)/2 + endGap
		for _, entry := range entries {
			p := &paths[entry.path]
			point := Point{box.X + pos, box.Y}
			if side == SideBottom {
				point.Y = box.Bottom()
			}
			if side == SideRight {
				point = Point{box.Right(), box.Y + pos}
			}
			point = quantizedPoint(point)
			if entry.source {
				p.sa.Point = point
			} else {
				p.ta.Point = point
			}
			pos += entry.slot
		}
	}
	// Reuse the ordinary channel fan ordering so opposite directions and
	// parallel occurrences get distinct, nested stubs instead of T-contacts.
	for _, entries := range ports {
		ordered := slices.Clone(entries)
		sort.SliceStable(ordered, func(i, j int) bool {
			a, b := paths[ordered[i].path], paths[ordered[j].path]
			return fanLess(&channelHop{ord: ordered[i].path, sx: a.sa.Point.X, tx: a.ta.Point.X}, &channelHop{ord: ordered[j].path, sx: b.sa.Point.X, tx: b.ta.Point.X}, ordered[i].source)
		})
		for rank, entry := range ordered {
			p := &paths[entry.path]
			if entry.source {
				p.sourceLead = float64(rank+1) * routingClearance(c, o)
			} else {
				p.targetLead = float64(len(entries)-rank) * routingClearance(c, o)
			}
		}
	}
	// Reserve branch labels before routing either class. Other paths cannot run
	// through them, including synthetic gather links.
	for i := range paths {
		p := &paths[i]
		if p.label != nil {
			p.label.Box.X = p.sa.Point.X + o.LabelGap
			p.label.Box.Y = p.sa.Point.Y + o.LabelGap
			if p.kind == EdgeSelfLoop {
				p.label.Box.Y = p.sa.Point.Y - o.LabelGap - p.label.Box.H
			}
		}
	}
	var routed []EdgeRoute
	for i := range paths {
		p := &paths[i]
		obstacles := []Rect{}
		for id, b := range boxes {
			// Containers and lane outlines surround their own contents. Text and live
			// reservations are separate obstacles below.
			decoration := false
			for _, e := range geo.GroupElements {
				if e.ID == id && (e.Role == "container" || e.Role == "lane") {
					decoration = true
					break
				}
			}
			if decoration {
				continue
			}
			b = b.Inflate(c.MinClearancePx + c.StrokePx/2)
			obstacles = append(obstacles, b)
		}
		for _, e := range geo.GroupElements {
			if e.Role == "container" || e.Role == "lane" {
				for _, t := range e.Texts {
					obstacles = append(obstacles, t.Box.Inflate(c.MinClearancePx))
				}
				if e.OverlayBox != nil {
					obstacles = append(obstacles, e.OverlayBox.Inflate(c.MinClearancePx))
				}
			}
		}
		for j, q := range paths {
			if q.label != nil {
				b := q.label.Box
				if j != i {
					b = b.Inflate(c.MinClearancePx)
				}
				obstacles = append(obstacles, b)
			}
		}
		for _, lane := range geo.GroupLanes {
			if !pathOwnsLane(g, *p, lane) {
				obstacles = append(obstacles, lane.Box.Inflate(c.MinClearancePx))
			}
		}
		start, end := p.sa.Point, p.ta.Point
		clear := routingClearance(c, o)
		a, b := Point{start.X, start.Y + p.sourceLead}, Point{end.X, end.Y - p.targetLead}
		if p.kind == EdgeSelfLoop {
			a = Point{start.X + clear, start.Y}
			b = Point{end.X + clear, end.Y}
		}
		region := groupRouteRegion(*geo, boxes[p.source], boxes[p.target])
		waypoints := []Point{a, b}
		if p.kind == EdgeLoopReturn {
			// The body-local exterior gutter remains inside its owning task lane.
			source := boxes[p.source]
			target := boxes[p.target]
			x := math.Max(source.Right(), target.Right()) + o.GutterWidth + float64(i+1)*clear
			for _, lane := range geo.GroupLanes {
				if pathOwnsLane(g, *p, lane) {
					x = lane.Box.Right() - c.GroupPaddingPx/2 - float64(i+1)*c.StrokePx
				}
			}
			waypoints = []Point{a, {x, a.Y}, {x, b.Y}, b}
		}
		points := []Point{start}
		for j := 1; j < len(waypoints); j++ {
			var segment []Point
			var err error
			if p.kind == EdgeLoopReturn && !blockedSegment(waypoints[j-1], waypoints[j], obstacles) {
				segment = []Point{waypoints[j-1], waypoints[j]}
			} else {
				segment, err = searchGroupPath(waypoints[j-1], waypoints[j], obstacles, routed, region, p.kind == EdgeForward, clear)
			}
			if err != nil {
				return fmt.Errorf("layout: group route %s: %w", p.key, err)
			}
			points = append(points, segment...)
		}
		points = append(points, end)
		p.points = simplify(points)
		routed = append(routed, EdgeRoute{Ordinal: i, Points: p.points})
		if p.ordinal >= 0 {
			e := &geo.Edges[p.ordinal]
			e.Points = p.points
			e.SourceAttach = p.sa
			e.TargetAttach = p.ta
			e.Text = p.label
			if e.Kind == EdgeLoopReturn {
				e.Side = SideRight
				e.Lane = i + 1
			}
			if e.Kind == EdgeSelfLoop {
				e.Side = SideRight
			}
		} else {
			e := &geo.GroupConnectors[p.connector]
			e.Points = p.points
			e.SourceAttach = p.sa
			e.TargetAttach = p.ta
		}
	}
	geo.Crossings = findCrossings(geo.Edges)
	if geo.Crossings == nil {
		geo.Crossings = []Crossing{}
	}
	geo.GroupCrossings = findGroupCrossings(*geo)
	return nil
}

func groupRouteRegion(geo Geometry, source, target Rect) Rect {
	region := geo.Canvas
	include := func(box Rect) {
		if box.Contains(source) && box.Contains(target) && box.W*box.H < region.W*region.H {
			region = box
		}
	}
	for _, e := range geo.GroupElements {
		if e.Role == "container" {
			include(e.Box)
		}
	}
	for _, l := range geo.GroupLanes {
		include(l.Box)
	}
	return region
}

func pathOwnsLane(g Graph, p groupPath, l GroupLane) bool {
	owns := func(element string) bool {
		var node string
		for _, n := range g.Nodes {
			if element == GroupElementID(n.ID, "header", "") || element == GroupElementID(n.ID, "footer", "") {
				node = n.ID
				break
			}
		}
		if element == GroupElementID(l.GroupNodeID, "task", l.TaskKey) {
			return true
		}
		for node != "" {
			owner, ok := g.Groups.Owner[node]
			if !ok {
				break
			}
			if owner.GroupNodeID == l.GroupNodeID && owner.TaskKey == l.TaskKey {
				return true
			}
			node = owner.GroupNodeID
		}
		return false
	}
	return owns(p.source) || owns(p.target)
}

func blockedSegment(a, b Point, obstacles []Rect) bool {
	for _, r := range obstacles {
		if segmentHitsRect(a, b, r) {
			return true
		}
	}
	return false
}

type pathState struct{ x, y, axis int }
type pathDistance struct {
	contacts, crossings int
	length              float64
}

func (a pathDistance) less(b pathDistance) bool {
	if a.contacts != b.contacts {
		return a.contacts < b.contacts
	}
	if a.crossings != b.crossings {
		return a.crossings < b.crossings
	}
	return a.length < b.length
}

type pathStep struct {
	state          pathState
	cost, estimate pathDistance
	order          int
}
type pathHeap []pathStep

func (h pathHeap) Len() int { return len(h) }
func (h pathHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.estimate != b.estimate {
		return a.estimate.less(b.estimate)
	}
	return a.order < b.order
}
func (h pathHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *pathHeap) Push(x any)   { *h = append(*h, x.(pathStep)) }
func (h *pathHeap) Pop() any     { a := *h; x := a[len(a)-1]; *h = a[:len(a)-1]; return x }

// A rectilinear visibility grid shares measured obstacles across both route
// classes. Dijkstra/A* costs prefer fewer proper crossings, then short paths
// and bends. Sorted coordinates and stable queue ties make it reproducible.
func searchGroupPath(start, end Point, obstacles []Rect, routed []EdgeRoute, canvas Rect, down bool, clear float64) ([]Point, error) {
	xs, ys := []float64{start.X, end.X, canvas.X + clear/2, canvas.Right() - clear/2}, []float64{start.Y, end.Y, canvas.Y + clear/2, canvas.Bottom() - clear/2}
	for _, r := range obstacles {
		xs = append(xs, r.X, r.Right())
		ys = append(ys, r.Y, r.Bottom())
	}
	for _, e := range routed {
		for _, p := range e.Points {
			xs = append(xs, p.X-clear, p.X+clear)
			ys = append(ys, p.Y-clear, p.Y+clear)
		}
	}
	coords := func(v []float64, lo, hi float64) []float64 {
		out := []float64{}
		for _, x := range v {
			if x >= lo && x <= hi {
				out = append(out, roundPx(x))
			}
		}
		sort.Float64s(out)
		return slices.Compact(out)
	}
	xs = coords(xs, canvas.X, canvas.Right())
	ys = coords(ys, canvas.Y, canvas.Bottom())
	sourceX, sourceXOK := slices.BinarySearch(xs, roundPx(start.X))
	sourceY, sourceYOK := slices.BinarySearch(ys, roundPx(start.Y))
	targetX, targetXOK := slices.BinarySearch(xs, roundPx(end.X))
	targetY, targetYOK := slices.BinarySearch(ys, roundPx(end.Y))
	// Coordinates outside the owning region were filtered out above. An
	// insertion index is neither an endpoint nor necessarily a valid grid index.
	if !sourceXOK || !sourceYOK || !targetXOK || !targetYOK {
		return nil, fmt.Errorf("route endpoints outside search region: %+v to %+v", start, end)
	}
	source := pathState{sourceX, sourceY, 0}
	costs := map[pathState]pathDistance{source: {}}
	prev := map[pathState]pathState{}
	q := &pathHeap{}
	heap.Init(q)
	heap.Push(q, pathStep{state: source})
	point := func(s pathState) Point { return Point{xs[s.x], ys[s.y]} }
	order := 0
	existing := segmentsOf(routed)
	for q.Len() > 0 {
		current := heap.Pop(q).(pathStep)
		s := current.state
		if current.cost != costs[s] {
			continue
		}
		if s.x == targetX && s.y == targetY {
			out := []Point{point(s)}
			for s != source {
				s = prev[s]
				out = append(out, point(s))
			}
			slices.Reverse(out)
			return simplify(out), nil
		}
		a := point(s)
		for _, d := range [][3]int{{0, 1, 2}, {1, 0, 1}, {-1, 0, 1}, {0, -1, 2}} {
			n := pathState{s.x + d[0], s.y + d[1], d[2]}
			if n.x < 0 || n.x >= len(xs) || n.y < 0 || n.y >= len(ys) || down && d[1] < 0 {
				continue
			}
			b := point(n)
			if blockedSegment(a, b, obstacles) {
				continue
			}
			delta := pathDistance{length: math.Abs(a.X-b.X) + math.Abs(a.Y-b.Y)}
			if s.axis != 0 && s.axis != n.axis {
				delta.length += clear
			}
			seg := segment{a: a, b: b, horiz: a.Y == b.Y}
			forbidden := false
			for _, other := range existing {
				if s.axis != 0 && s.axis != n.axis && distanceToSegment(a, other) < clear/2-geomEps {
					forbidden = true
					break
				}
				if seg.horiz == other.horiz {
					gap, overlap := parallelDistance(seg, other)
					if overlap > geomEps && gap < clear/2 {
						delta.contacts++
					}
				} else if p, ok := intersection(seg, other); ok {
					delta.crossings++
					radius := clear / 2
					if math.Min(math.Hypot(p.X-other.a.X, p.Y-other.a.Y), math.Hypot(p.X-other.b.X, p.Y-other.b.Y)) < radius {
						forbidden = true
						break
					}
				}
			}
			if forbidden {
				continue
			}
			cost := pathDistance{contacts: current.cost.contacts + delta.contacts, crossings: current.cost.crossings + delta.crossings, length: current.cost.length + delta.length}
			if old, ok := costs[n]; ok && !cost.less(old) {
				continue
			}
			costs[n] = cost
			prev[n] = s
			order++
			heuristic := math.Abs(b.X-end.X) + math.Abs(b.Y-end.Y)
			estimate := cost
			estimate.length += heuristic
			heap.Push(q, pathStep{n, cost, estimate, order})
		}
	}
	return nil, fmt.Errorf("no obstacle-free orthogonal path from %+v to %+v", start, end)
}

func distanceToSegment(p Point, s segment) float64 {
	x := math.Max(math.Min(s.a.X, s.b.X), math.Min(p.X, math.Max(s.a.X, s.b.X)))
	y := math.Max(math.Min(s.a.Y, s.b.Y), math.Min(p.Y, math.Max(s.a.Y, s.b.Y)))
	return math.Hypot(p.X-x, p.Y-y)
}

func findGroupCrossings(geo Geometry) []GroupCrossing {
	all := slices.Clone(geo.Edges)
	keys := make([]string, len(all))
	for i, e := range all {
		keys[i] = ManifestRouteKey(e.Ordinal)
		all[i].Ordinal = i
	}
	for _, e := range geo.GroupConnectors {
		keys = append(keys, e.ID)
		all = append(all, EdgeRoute{Ordinal: len(all), Points: e.Points})
	}
	var out []GroupCrossing
	for _, x := range findCrossings(all) {
		if x.Over < len(geo.Edges) && x.Under < len(geo.Edges) {
			continue
		}
		out = append(out, GroupCrossing{Point: x.Point, Over: keys[x.Over], Under: keys[x.Under]})
	}
	return out
}

func groupRouteCost(g Graph, geo Geometry) routeCost {
	return routeCost{contacts: len(groupContacts(g, geo)), crossings: CrossingCount(geo)}
}
