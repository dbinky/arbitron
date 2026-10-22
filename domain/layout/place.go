package layout

import (
	"math"
	"sort"
)

// packRow solves ordered least-squares placement by pooling adjacent violators.
// Subtracting each item's cumulative width/gap turns spacing into isotonic order.
func packRow(widths, anchors, desired []float64, gap float64) []float64 {
	type block struct {
		start, end int
		sum        float64
		count      int
	}
	offsets := make([]float64, len(widths))
	blocks := make([]block, 0, len(widths))
	for i := range widths {
		if i > 0 {
			offsets[i] = offsets[i-1] + widths[i-1] + gap
		}
		blocks = append(blocks, block{i, i + 1, desired[i] - anchors[i] - offsets[i], 1})
		for len(blocks) > 1 {
			k := len(blocks) - 1
			a, b := blocks[k-1], blocks[k]
			if a.sum/float64(a.count) <= b.sum/float64(b.count) {
				break
			}
			blocks[k-1] = block{a.start, b.end, a.sum + b.sum, a.count + b.count}
			blocks = blocks[:k]
		}
	}
	xs := make([]float64, len(widths))
	for _, b := range blocks {
		for i := b.start; i < b.end; i++ {
			xs[i] = b.sum/float64(b.count) + offsets[i]
		}
	}
	return xs
}

// placement owns one candidate's working coordinates; it never aliases inputs.
type placement struct {
	graph              Graph
	kinds              []EdgeKind
	ranks              []int
	layers             *layering
	sizes              []nodeSize
	labels             map[int]labelSize
	contract           RendererContract
	options            Options
	x, widths, anchors []float64
	selfExtent         []float64
	selfLane           map[int]float64
	loops              map[int]loopLane
	sourceX, targetX   []float64
	left, right        float64
}

type loopLane struct {
	side              Side
	lane, first, last int
	x                 float64
}

func newPlacement(g Graph, kinds []EdgeKind, ranks []int, l *layering, sizes []nodeSize, labels map[int]labelSize, c RendererContract, o Options) *placement {
	p := &placement{graph: g, kinds: kinds, ranks: ranks, layers: l, sizes: sizes, labels: labels, contract: c, options: o,
		x: make([]float64, len(l.items)), widths: make([]float64, len(l.items)), anchors: make([]float64, len(l.items)),
		selfExtent: make([]float64, len(g.Nodes)), selfLane: map[int]float64{}, loops: map[int]loopLane{}, sourceX: make([]float64, len(g.Edges)), targetX: make([]float64, len(g.Edges))}
	for _, e := range g.Edges {
		if kinds[e.Ordinal] == EdgeSelfLoop {
			n, _ := g.NodeIndex(e.Source)
			step := o.LaneSeparation
			if label, ok := labels[e.Ordinal]; ok {
				step = math.Max(step, label.W+2*o.LabelGap)
			}
			p.selfExtent[n] += step
			p.selfLane[e.Ordinal] = p.selfExtent[n]
		}
	}
	for id, it := range l.items {
		p.widths[id], p.anchors[id] = o.DummyWidth, o.DummyWidth/2
		if it.node >= 0 {
			p.widths[id] = sizes[it.node].W + p.selfExtent[it.node]
			p.anchors[id] = sizes[it.node].W / 2
		}
	}
	p.alignRows()
	p.left, p.right = math.Inf(1), math.Inf(-1)
	for i := range p.x {
		p.x[i] = roundPx(p.x[i])
		p.left = math.Min(p.left, p.x[i])
		p.right = math.Max(p.right, p.x[i]+p.widths[i])
	}
	p.assignLoops()
	p.assignPorts()
	return p
}

func (p *placement) alignRows() {
	l := p.layers
	place := func(r int, adj [][]int) {
		row := l.rows[r]
		widths, anchors, desired := make([]float64, len(row)), make([]float64, len(row)), make([]float64, len(row))
		cursor := 0.0
		for j, id := range row {
			widths[j], anchors[j] = p.widths[id], p.anchors[id]
			desired[j] = cursor + anchors[j]
			cursor += widths[j] + p.options.NodeSpacing
			if len(adj[id]) > 0 {
				desired[j] = 0
				for _, n := range adj[id] {
					desired[j] += p.x[n] + p.anchors[n]
				}
				desired[j] /= float64(len(adj[id]))
			}
		}
		xs := packRow(widths, anchors, desired, p.options.NodeSpacing)
		for j, id := range row {
			p.x[id] = xs[j]
		}
	}
	// Seed every row before using either direction's neighbours.
	empty := make([][]int, len(l.items))
	for r := range l.rows {
		place(r, empty)
	}
	const alignmentPasses = 4
	for pass := 0; pass < alignmentPasses; pass++ {
		for r := 1; r < len(l.rows); r++ {
			place(r, l.up)
		}
		for r := len(l.rows) - 2; r >= 0; r-- {
			place(r, l.down)
		}
	}
	for r := 1; r < len(l.rows); r++ {
		place(r, l.up)
	}
}

func (p *placement) nodeX(n int) float64  { return p.x[p.layers.itemOfNode[n]] }
func (p *placement) itemX(id int) float64 { return roundPx(p.x[id] + p.anchors[id]) }

func (p *placement) assignLoops() {
	var ords []int
	for _, e := range p.graph.Edges {
		if p.kinds[e.Ordinal] != EdgeLoopReturn {
			continue
		}
		s, _ := p.graph.NodeIndex(e.Source)
		t, _ := p.graph.NodeIndex(e.Target)
		body := loopBody(p.graph, p.kinds, e.Ordinal)
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, n := range body {
			lo = math.Min(lo, p.nodeX(n))
			hi = math.Max(hi, p.nodeX(n)+p.sizes[n].W+p.selfExtent[n])
		}
		// Match chooseLoopSide's open-side policy using this loop's body only.
		spine := (p.nodeX(s) + p.sizes[s].W/2 + p.nodeX(t) + p.sizes[t].W/2) / 2
		side := SideRight
		if hi-spine > spine-lo {
			side = SideLeft
		}
		p.loops[e.Ordinal] = loopLane{side: side, first: p.ranks[t] - 1, last: p.ranks[s]}
		ords = append(ords, e.Ordinal)
	}
	sort.SliceStable(ords, func(i, j int) bool {
		a, b := p.loops[ords[i]], p.loops[ords[j]]
		return a.last-a.first < b.last-b.first
	})
	for _, ord := range ords {
		loop := p.loops[ord]
		lane := 1
		for {
			used := false
			for _, prev := range ords {
				v := p.loops[prev]
				if v.lane == lane && v.side == loop.side && v.first <= loop.last && loop.first <= v.last {
					used = true
					break
				}
			}
			if !used {
				break
			}
			lane++
		}
		loop.lane = lane
		p.loops[ord] = loop
	}
}

func (p *placement) assignPorts() {
	bottom, top := make([][]int, len(p.graph.Nodes)), make([][]int, len(p.graph.Nodes))
	for _, e := range p.graph.Edges {
		if p.kinds[e.Ordinal] == EdgeSelfLoop {
			continue
		}
		s, _ := p.graph.NodeIndex(e.Source)
		t, _ := p.graph.NodeIndex(e.Target)
		bottom[s] = append(bottom[s], e.Ordinal)
		top[t] = append(top[t], e.Ordinal)
	}
	key := func(ord int, out bool) (int, float64) {
		if loop, ok := p.loops[ord]; ok {
			if loop.side == SideLeft {
				return 0, float64(loop.lane)
			}
			return 2, -float64(loop.lane)
		}
		chain := p.layers.chains[ord]
		id := chain[1]
		if !out {
			id = chain[len(chain)-2]
		}
		return 1, p.itemX(id)
	}
	for n := range p.graph.Nodes {
		for _, out := range []bool{true, false} {
			ords := top[n]
			if out {
				ords = bottom[n]
			}
			sort.SliceStable(ords, func(i, j int) bool {
				a, x := key(ords[i], out)
				b, y := key(ords[j], out)
				if a != b {
					return a < b
				}
				if x != y {
					return x < y
				}
				return ords[i] < ords[j]
			})
			width := 0.0
			slot := func(ord int) float64 {
				w := p.options.PortSpacing
				if out {
					if l, ok := p.labels[ord]; ok {
						w += p.options.LabelGap + l.W
					}
				}
				return w
			}
			for _, ord := range ords {
				width += slot(ord)
			}
			x := p.nodeX(n) + (p.sizes[n].W-width)/2
			for _, ord := range ords {
				v := roundPx(x + p.options.PortSpacing/2)
				if out {
					p.sourceX[ord] = v
				} else {
					p.targetX[ord] = v
				}
				x += slot(ord)
			}
		}
	}
}
