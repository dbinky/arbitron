package layout

import (
	"math"
	"sort"
)

// A hop crosses one channel. Negative items mark a fixed gutter endpoint. A
// direct endpoint is a dummy that runs straight to or from its track: only real
// nodes fan out to transit columns to separate their attachment lanes.
type channelHop struct {
	ord, from, to                        int
	sx, tx, sourceTransit, targetTransit float64
	fanout, track, fanin                 float64
	straight                             bool
	directSource, directTarget           bool
}

type routeChannel struct {
	hops                []*channelHop
	labelHeight, height float64
}

func routingClearance(c RendererContract, o Options) float64 {
	return math.Ceil(math.Max(o.LaneSeparation, math.Max(c.StrokePx+c.MinClearancePx, c.CrossingGapPx+2*c.StrokePx)))
}

// labelDrop lowers a branch label inside its channel band when the label is so
// wide that its centre would sit nearer to a route entering the source node's
// top than to its own route beside the label. The reviewer attributes a label
// to the route nearest its centre, so the centre must stay at least as far
// from the node top as it is from the own route (half the width plus the gap).
func (p *placement) labelDrop(source int, l labelSize) float64 {
	own := l.W / 2
	above := p.sizes[source].H + l.H/2
	return math.Max(0, math.Ceil(own-above))
}

func (p *placement) channels() []routeChannel {
	// Channel index = source rank + 1, including the channel above rank zero.
	channels := make([]routeChannel, len(p.layers.rows)+1)
	for _, e := range p.graph.Edges {
		switch p.kinds[e.Ordinal] {
		case EdgeForward:
			chain := p.layers.chains[e.Ordinal]
			for j := 1; j < len(chain); j++ {
				from, to := chain[j-1], chain[j]
				sx, tx := p.itemX(from), p.itemX(to)
				if j == 1 {
					sx = p.sourceX[e.Ordinal]
				}
				if j == len(chain)-1 {
					tx = p.targetX[e.Ordinal]
				}
				ch := p.layers.items[from].rank + 1
				channels[ch].hops = append(channels[ch].hops, &channelHop{ord: e.Ordinal, from: from, to: to, sx: sx, tx: tx})
			}
		case EdgeLoopReturn:
			s, _ := p.graph.NodeIndex(e.Source)
			t, _ := p.graph.NodeIndex(e.Target)
			exit, entry := p.ranks[s]+1, p.ranks[t]
			channels[exit].hops = append(channels[exit].hops, &channelHop{ord: e.Ordinal, from: p.layers.itemOfNode[s], to: -1, sx: p.sourceX[e.Ordinal]})
			channels[entry].hops = append(channels[entry].hops, &channelHop{ord: e.Ordinal, from: -1, to: p.layers.itemOfNode[t], tx: p.targetX[e.Ordinal]})
		}
		if p.kinds[e.Ordinal] != EdgeSelfLoop {
			if l, ok := p.labels[e.Ordinal]; ok {
				s, _ := p.graph.NodeIndex(e.Source)
				ch := p.ranks[s] + 1
				channels[ch].labelHeight = math.Max(channels[ch].labelHeight, l.H+p.labelDrop(s, l))
			}
		}
	}
	clearance := routingClearance(p.contract, p.options)
	// Reserve all transit columns before placing gutters. No precedence is dropped.
	left, right := p.left, p.right
	for ch := range channels {
		hops := channels[ch].hops
		type endpoint struct {
			h      *channelHop
			source bool
			x      float64
		}
		var endpoints []endpoint
		var occupied []float64
		for _, h := range hops {
			if h.from >= 0 {
				occupied = append(occupied, h.sx)
			}
			if h.to >= 0 {
				occupied = append(occupied, h.tx)
			}
		}
		for _, h := range hops {
			h.straight = h.from >= 0 && h.to >= 0 && h.sx == h.tx
			if h.straight {
				for _, other := range hops {
					if other == h {
						continue
					}
					if (other.from >= 0 && math.Abs(other.sx-h.sx) < clearance) || (other.to >= 0 && math.Abs(other.tx-h.sx) < clearance) {
						h.straight = false
						break
					}
				}
			}
			if h.straight {
				continue
			}
			// A dummy column is already reserved, and a dummy's row neighbours sit a
			// node gap away, so its vertical joins its track without a fan jog.
			h.directSource = p.isDummy(h.from) && !nearEndpoint(hops, h, h.sx, true, clearance)
			h.directTarget = p.isDummy(h.to) && !nearEndpoint(hops, h, h.tx, false, clearance)
			if h.directSource {
				h.sourceTransit = h.sx
			} else if h.from >= 0 {
				endpoints = append(endpoints, endpoint{h, true, h.sx})
			}
			if h.directTarget {
				h.targetTransit = h.tx
			} else if h.to >= 0 {
				endpoints = append(endpoints, endpoint{h, false, h.tx})
			}
		}
		sort.SliceStable(endpoints, func(i, j int) bool {
			a, b := endpoints[i], endpoints[j]
			if a.x != b.x {
				return a.x < b.x
			}
			if a.source != b.source {
				return a.source
			}
			return a.h.ord < b.h.ord
		})
		for _, ep := range endpoints {
			// Jump past sorted reservations rather than scanning empty pixels.
			// Nextafter also makes progress when clearance is below the float ULP.
			sort.Float64s(occupied)
			x := roundPx(ep.x + clearance)
			for _, v := range occupied {
				if math.Abs(x-v) < clearance-geomEps {
					x = roundPx(v + clearance)
					if x-v < clearance-geomEps {
						x = math.Nextafter(x, math.Inf(1))
					}
				}
			}
			occupied = append(occupied, x)
			left = math.Min(left, x)
			right = math.Max(right, x)
			if ep.source {
				ep.h.sourceTransit = x
			} else {
				ep.h.targetTransit = x
			}
		}
	}
	for ord, loop := range p.loops {
		loop.x = right + p.options.GutterWidth + float64(loop.lane-1)*clearance
		if loop.side == SideLeft {
			loop.x = left - p.options.GutterWidth - float64(loop.lane-1)*clearance
		}
		loop.x = roundPx(loop.x)
		p.loops[ord] = loop
	}
	for ch := range channels {
		channel := &channels[ch]
		for _, h := range channel.hops {
			if loop, ok := p.loops[h.ord]; ok {
				if h.from < 0 {
					h.sx = loop.x
					h.sourceTransit = loop.x
				}
				if h.to < 0 {
					h.tx = loop.x
					h.targetTransit = loop.x
				}
			}
		}
		var sources, targets, tracks []*channelHop
		for _, h := range channel.hops {
			if !h.straight {
				tracks = append(tracks, h)
				if h.from >= 0 && !h.directSource {
					sources = append(sources, h)
				}
				if h.to >= 0 && !h.directTarget {
					targets = append(targets, h)
				}
			}
		}
		// Outer return exits must be below inner exits; outer entries above inner
		// entries. The same directional ordering keeps their endpoint fans nested.
		sort.SliceStable(sources, func(i, j int) bool { return fanLess(sources[i], sources[j], true) })
		sort.SliceStable(targets, func(i, j int) bool { return fanLess(targets[i], targets[j], false) })
		sort.SliceStable(tracks, func(i, j int) bool {
			a, b := tracks[i], tracks[j]
			group := func(h *channelHop) int {
				if h.from < 0 {
					return 0
				}
				if h.to < 0 {
					return 2
				}
				return 1
			}
			ga, gb := group(a), group(b)
			if ga != gb {
				return ga < gb
			}
			if ga != 1 {
				la, lb := p.loops[a.ord], p.loops[b.ord]
				if la.lane != lb.lane {
					if ga == 0 {
						return la.lane > lb.lane
					}
					return la.lane < lb.lane
				}
			}
			alo, blo := math.Min(a.sx, a.tx), math.Min(b.sx, b.tx)
			if alo != blo {
				return alo < blo
			}
			return a.ord < b.ord
		})
		y := clearance
		if channel.labelHeight > 0 {
			y += p.options.LabelGap*2 + channel.labelHeight
		}
		for _, h := range sources {
			h.fanout = y
			y += clearance
		}
		y += clearance
		for _, h := range tracks {
			h.track = y
			y += clearance
		}
		y += clearance
		for _, h := range targets {
			h.fanin = y
			y += clearance
		}
		// Empty/straight-only channels still provide the requested rank spacing.
		channel.height = y + p.options.RankSpacing
		if len(tracks) == 0 {
			channel.height = p.options.RankSpacing + channel.labelHeight
			if channel.labelHeight > 0 {
				channel.height += 2 * p.options.LabelGap
			}
		}
		if ch == 0 && len(channel.hops) == 0 {
			channel.height = 0
		}
	}
	return channels
}

func (p *placement) isDummy(id int) bool { return id >= 0 && p.layers.items[id].node < 0 }

// nearEndpoint reports another hop's same-side endpoint within clearance of x.
func nearEndpoint(hops []*channelHop, h *channelHop, x float64, source bool, clearance float64) bool {
	for _, other := range hops {
		if other == h {
			continue
		}
		if (source && other.from >= 0 && math.Abs(other.sx-x) < clearance) || (!source && other.to >= 0 && math.Abs(other.tx-x) < clearance) {
			return true
		}
	}
	return false
}

// fanLess groups by direction before comparing endpoint position. Mixing
// opposite directions in a pair-dependent comparator would violate transitivity.
func fanLess(a, b *channelHop, source bool) bool {
	ar, br := a.tx >= a.sx, b.tx >= b.sx
	if ar != br {
		return !ar
	}
	ax, bx := a.tx, b.tx
	if source {
		ax, bx = a.sx, b.sx
	}
	if ax != bx {
		if ar {
			return ax > bx
		}
		return ax < bx
	}
	return a.ord < b.ord
}

func (p *placement) route() Geometry {
	channels := p.channels()
	rows := len(p.layers.rows)
	tops, bottoms := make([]float64, rows), make([]float64, rows)
	y := channels[0].height
	for r, row := range p.layers.rows {
		tops[r] = y
		h := 0.0
		for _, id := range row {
			if n := p.layers.items[id].node; n >= 0 {
				h = math.Max(h, p.sizes[n].H)
			}
		}
		bottoms[r] = y + h
		y = bottoms[r] + channels[r+1].height
	}
	geo := Geometry{Format: FormatV2, Contract: p.contract, Options: p.options, Nodes: make([]NodeBox, len(p.graph.Nodes)), Edges: make([]EdgeRoute, len(p.graph.Edges))}
	for i, n := range p.graph.Nodes {
		geo.Nodes[i] = NodeBox{ID: n.ID, Text: n.Text, TypeCaption: n.TypeCaption, TextLines: p.sizes[i].TextLines, Rank: p.ranks[i], Box: Rect{X: p.nodeX(i), Y: tops[p.ranks[i]], W: p.sizes[i].W, H: p.sizes[i].H}}
	}
	for _, e := range p.graph.Edges {
		geo.Edges[e.Ordinal] = EdgeRoute{Ordinal: e.Ordinal, Source: e.Source, Target: e.Target, Output: e.Output, Label: e.Label, Kind: p.kinds[e.Ordinal]}
	}
	// Assemble forward pieces in channel order. Return exits/entries are joined
	// separately, because their traversal runs upwards through the fixed gutter.
	pieces := make(map[int][][]Point)
	exits, entries := map[int][]Point{}, map[int][]Point{}
	for ch, channel := range channels {
		base := 0.0
		if ch > 0 {
			base = bottoms[ch-1]
		}
		for _, h := range channel.hops {
			start, end := Point{h.sx, base}, Point{h.tx, base + channel.height}
			if h.from >= 0 {
				n := p.layers.items[h.from].node
				if n >= 0 {
					start.Y = geo.Nodes[n].Box.Bottom()
				}
			}
			if h.to >= 0 {
				end.Y = tops[ch]
			}
			var points []Point
			if h.straight {
				points = []Point{start, end}
			} else {
				if h.from < 0 {
					start.Y = base + h.track
					points = append(points, start)
				} else if h.directSource {
					points = append(points, start, Point{h.sx, base + h.track})
				} else {
					points = append(points, start, Point{h.sx, base + h.fanout}, Point{h.sourceTransit, base + h.fanout}, Point{h.sourceTransit, base + h.track})
				}
				points = append(points, Point{h.targetTransit, base + h.track})
				if h.to < 0 {
					end = Point{h.tx, base + h.track}
				} else if h.directTarget {
					points = append(points, end)
				} else {
					points = append(points, Point{h.targetTransit, base + h.fanin}, Point{h.tx, base + h.fanin}, end)
				}
			}
			if h.to < 0 {
				exits[h.ord] = points
			} else if h.from < 0 {
				entries[h.ord] = points
			} else {
				pieces[h.ord] = append(pieces[h.ord], points)
			}
		}
	}
	selfCount := make([]int, len(p.graph.Nodes))
	for _, e := range p.graph.Edges {
		if p.kinds[e.Ordinal] == EdgeSelfLoop {
			n, _ := p.graph.NodeIndex(e.Source)
			selfCount[n]++
		}
	}
	selfIndex := make([]int, len(p.graph.Nodes))
	for _, e := range p.graph.Edges {
		route := &geo.Edges[e.Ordinal]
		s, _ := p.graph.NodeIndex(e.Source)
		t, _ := p.graph.NodeIndex(e.Target)
		sourceSide, targetSide := SideBottom, SideTop
		switch p.kinds[e.Ordinal] {
		case EdgeForward:
			for _, piece := range pieces[e.Ordinal] {
				route.Points = append(route.Points, piece...)
			}
		case EdgeLoopReturn:
			loop := p.loops[e.Ordinal]
			route.Side, route.Lane = loop.side, loop.lane
			route.Points = append(route.Points, exits[e.Ordinal]...)
			route.Points = append(route.Points, entries[e.Ordinal]...)
		case EdgeSelfLoop:
			box := geo.Nodes[s].Box
			n, k := selfCount[s], selfIndex[s]
			selfIndex[s]++
			exitY := box.Y + box.H*float64(n-k)/float64(2*n+1)
			entryY := box.Y + box.H*float64(n+1+k)/float64(2*n+1)
			laneX := box.Right() + p.selfLane[e.Ordinal]
			route.Side = SideRight
			route.Points = []Point{{box.Right(), exitY}, {laneX, exitY}, {laneX, entryY}, {box.Right(), entryY}}
			sourceSide, targetSide = SideRight, SideRight
		}
		if label, ok := p.labels[e.Ordinal]; ok {
			box := Rect{X: route.Points[0].X + p.options.LabelGap, Y: bottoms[p.ranks[s]] + p.options.LabelGap + p.labelDrop(s, label), W: label.W, H: label.H}
			if p.kinds[e.Ordinal] == EdgeSelfLoop {
				box.X = geo.Nodes[s].Box.Right() + p.selfLane[e.Ordinal] - p.options.LabelGap - label.W
				box.Y = route.Points[0].Y - p.options.LabelGap - label.H
			}
			route.Text = &LabelBox{Text: e.Text, Box: box, Lines: label.Lines}
		}
		route.SourceAttach = Attachment{NodeID: p.graph.Nodes[s].ID, ElementID: GroupElementID(p.graph.Nodes[s].ID, "header", ""), Side: sourceSide, Point: route.Points[0]}
		route.TargetAttach = Attachment{NodeID: p.graph.Nodes[t].ID, ElementID: GroupElementID(p.graph.Nodes[t].ID, "header", ""), Side: targetSide, Point: route.Points[len(route.Points)-1]}
	}
	return normalize(geo)
}

// simplify removes redundant points but never erases a direction reversal.
func simplify(points []Point) []Point {
	out := make([]Point, 0, len(points))
	for _, p := range points {
		p = quantizedPoint(p)
		if len(out) > 0 && p == out[len(out)-1] {
			continue
		}
		for len(out) > 1 {
			a, b := out[len(out)-2], out[len(out)-1]
			between := (a.X == b.X && b.X == p.X && b.Y >= math.Min(a.Y, p.Y) && b.Y <= math.Max(a.Y, p.Y)) || (a.Y == b.Y && b.Y == p.Y && b.X >= math.Min(a.X, p.X) && b.X <= math.Max(a.X, p.X))
			if !between {
				break
			}
			out = out[:len(out)-1]
		}
		out = append(out, p)
	}
	return out
}

// segment retains manifest occurrence identity; no pair-key deduplication.
type segment struct {
	ord   int
	a, b  Point
	horiz bool
}

func segmentsOf(routes []EdgeRoute) []segment {
	var out []segment
	for _, e := range routes {
		for i := 1; i < len(e.Points); i++ {
			a, b := quantizedPoint(e.Points[i-1]), quantizedPoint(e.Points[i])
			out = append(out, segment{e.Ordinal, a, b, a.Y == b.Y})
		}
	}
	return out
}

func intersection(a, b segment) (Point, bool) {
	if a.horiz == b.horiz {
		return Point{}, false
	}
	if !a.horiz {
		a, b = b, a
	}
	p := Point{b.a.X, a.a.Y}
	return p, p.X >= math.Min(a.a.X, a.b.X) && p.X <= math.Max(a.a.X, a.b.X) && p.Y >= math.Min(b.a.Y, b.b.Y) && p.Y <= math.Max(b.a.Y, b.b.Y)
}
func segmentInterior(p Point, s segment) bool {
	if s.horiz {
		return p.X > math.Min(s.a.X, s.b.X)+geomEps && p.X < math.Max(s.a.X, s.b.X)-geomEps
	}
	return p.Y > math.Min(s.a.Y, s.b.Y)+geomEps && p.Y < math.Max(s.a.Y, s.b.Y)-geomEps
}
func findCrossings(routes []EdgeRoute) []Crossing {
	segs := segmentsOf(routes)
	var out []Crossing
	seen := map[Crossing]bool{}
	for i, a := range segs {
		for _, b := range segs[i+1:] {
			if a.ord == b.ord {
				continue
			}
			p, ok := intersection(a, b)
			if !ok || !segmentInterior(p, a) || !segmentInterior(p, b) {
				continue
			}
			over, under := a.ord, b.ord
			if !a.horiz {
				over, under = under, over
			}
			c := Crossing{Point: p, Over: over, Under: under}
			if !seen[c] {
				out = append(out, c)
				seen[c] = true
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Under != b.Under {
			return a.Under < b.Under
		}
		if a.Point.Y != b.Point.Y {
			return a.Point.Y < b.Point.Y
		}
		if a.Point.X != b.Point.X {
			return a.Point.X < b.Point.X
		}
		return a.Over < b.Over
	})
	return out
}

type routeCost struct{ contacts, crossings int }

func (a routeCost) less(b routeCost) bool {
	return a.contacts < b.contacts || (a.contacts == b.contacts && a.crossings < b.crossings)
}
func crossesRect(s segment, r Rect) bool {
	if s.horiz {
		return s.a.Y > r.Y && s.a.Y < r.Bottom() && math.Max(s.a.X, s.b.X) > r.X && math.Min(s.a.X, s.b.X) < r.Right()
	}
	return s.a.X > r.X && s.a.X < r.Right() && math.Max(s.a.Y, s.b.Y) > r.Y && math.Min(s.a.Y, s.b.Y) < r.Bottom()
}

// costOf is local routing search policy, not the later preflight/review verdict.
func costOf(geo Geometry) routeCost {
	segs := segmentsOf(geo.Edges)
	crossings := findCrossings(geo.Edges)
	cost := routeCost{crossings: len(crossings)}
	c := geo.Contract
	parallelClear := c.StrokePx + c.MinClearancePx
	gapReach := c.CrossingGapPx/2 + c.StrokePx
	for i, n := range geo.Nodes {
		for _, other := range geo.Nodes[i+1:] {
			if n.Box.Overlaps(other.Box) {
				cost.contacts++
			}
		}
		for _, s := range segs {
			if crossesRect(s, n.Box.Inflate(c.StrokePx/2)) {
				// Only the route's boundary segment may meet its own source/target.
				ownBoundary := false
				for _, e := range geo.Edges {
					if e.Ordinal == s.ord {
						ownBoundary = (e.Source == n.ID && (s.a == e.SourceAttach.Point || s.b == e.SourceAttach.Point)) || (e.Target == n.ID && (s.a == e.TargetAttach.Point || s.b == e.TargetAttach.Point))
						break
					}
				}
				if !ownBoundary || crossesRect(s, n.Box) {
					cost.contacts++
				}
			}
		}
	}
	for i, e := range geo.Edges {
		if e.Text == nil {
			continue
		}
		box := e.Text.Box
		for _, n := range geo.Nodes {
			if box.Overlaps(n.Box) {
				cost.contacts++
			}
		}
		for _, other := range geo.Edges[i+1:] {
			if other.Text != nil && box.Overlaps(other.Text.Box) {
				cost.contacts++
			}
		}
		for _, s := range segs {
			if crossesRect(s, box.Inflate(c.StrokePx/2)) {
				cost.contacts++
			}
		}
	}
	for i, a := range segs {
		for _, b := range segs[i+1:] {
			if a.ord == b.ord && a.horiz != b.horiz && (a.b == b.a || a.a == b.b) {
				continue // a normal bend, not a reversal or self-intersection
			}
			if a.horiz == b.horiz {
				offset := math.Abs(a.a.X - b.a.X)
				lo, hi := math.Max(math.Min(a.a.Y, a.b.Y), math.Min(b.a.Y, b.b.Y)), math.Min(math.Max(a.a.Y, a.b.Y), math.Max(b.a.Y, b.b.Y))
				if a.horiz {
					offset = math.Abs(a.a.Y - b.a.Y)
					lo = math.Max(math.Min(a.a.X, a.b.X), math.Min(b.a.X, b.b.X))
					hi = math.Min(math.Max(a.a.X, a.b.X), math.Max(b.a.X, b.b.X))
				}
				if hi > lo+geomEps && offset < parallelClear-geomEps {
					cost.contacts++
				}
			} else if p, ok := intersection(a, b); ok {
				if a.ord == b.ord {
					cost.contacts++
					continue
				}
				if !segmentInterior(p, a) || !segmentInterior(p, b) {
					cost.contacts++
					continue
				}
				// A gap must not consume a bend/attachment on either participating stroke.
				for _, s := range []segment{a, b} {
					if math.Min(math.Hypot(p.X-s.a.X, p.Y-s.a.Y), math.Hypot(p.X-s.b.X, p.Y-s.b.Y)) < gapReach-geomEps {
						cost.contacts++
					}
				}
			}
		}
	}
	for i, a := range crossings {
		for _, b := range crossings[i+1:] {
			if a.Under == b.Under && nearly(a.Point.X, b.Point.X) && math.Abs(a.Point.Y-b.Point.Y) < c.CrossingGapPx+2*c.StrokePx-geomEps {
				cost.contacts++
			}
		}
	}
	return cost
}
