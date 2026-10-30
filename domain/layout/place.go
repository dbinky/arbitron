package layout

import (
	"math"
	"slices"
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
	for i := range p.x {
		p.x[i] = roundPx(p.x[i])
	}
	p.straightenChains()
	p.left, p.right = math.Inf(1), math.Inf(-1)
	for i := range p.x {
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

// straightenPasses bounds the straightening sweeps; each kept move strictly
// lowers the total shift count, so sweeps stop early once nothing improves.
const straightenPasses = 8

// straightenChains moves each long edge's dummies onto one shared column, so
// the edge runs straight instead of stepping at every rank. Rows crowded with
// dummies are packed against their real nodes, whose widths differ by rank, so
// a dummy may push the dummies beside it outward to make room. A real node is
// a wall and never moves, and row order and node spacing always hold. A move
// is kept only when it lowers the total number of shifts over every chain.
func (p *placement) straightenChains() {
	l := p.layers
	gap := p.options.NodeSpacing
	slot := make([]int, len(l.items))
	for _, row := range l.rows {
		for i, id := range row {
			slot[id] = i
		}
	}
	ords := make([]int, 0, len(l.chains))
	for ord, chain := range l.chains {
		if len(chain) > 3 {
			ords = append(ords, ord)
		}
	}
	sort.Ints(ords)
	total := func() int {
		n := 0
		for _, ord := range ords {
			dummies := l.chains[ord][1 : len(l.chains[ord])-1]
			for i := 1; i < len(dummies); i++ {
				if p.itemX(dummies[i]) != p.itemX(dummies[i-1]) {
					n++
				}
			}
		}
		return n
	}
	// moveTo puts id's column at x, pushing dummies on the side it moves
	// towards just far enough to keep the gap. It restores the row and reports
	// false when a real node is in the way.
	moveTo := func(id int, x float64) bool {
		row := l.rows[l.items[id].rank]
		saved := make([]float64, len(row))
		for j, other := range row {
			saved[j] = p.x[other]
		}
		blocked := false
		i := slot[id]
		p.x[id] = roundPx(x - p.anchors[id])
		for j := i - 1; j >= 0 && !blocked; j-- {
			limit := p.x[row[j+1]] - gap - p.widths[row[j]]
			if p.x[row[j]] <= limit+geomEps {
				break
			}
			blocked = l.items[row[j]].node >= 0
			p.x[row[j]] = math.Floor(limit/geomEps) * geomEps
		}
		for j := i + 1; j < len(row) && !blocked; j++ {
			limit := p.x[row[j-1]] + p.widths[row[j-1]] + gap
			if p.x[row[j]] >= limit-geomEps {
				break
			}
			blocked = l.items[row[j]].node >= 0
			p.x[row[j]] = math.Ceil(limit/geomEps) * geomEps
		}
		if blocked {
			for j, other := range row {
				p.x[other] = saved[j]
			}
		}
		return !blocked
	}
	for range straightenPasses {
		improved := false
		for _, ord := range ords {
			dummies := l.chains[ord][1 : len(l.chains[ord])-1]
			before := slices.Clone(p.x)
			base := total()
			var best []float64
			bestTotal, bestMove := base, 0.0
			var columns []float64
			for _, id := range dummies {
				if x := p.itemX(id); !slices.Contains(columns, x) {
					columns = append(columns, x)
				}
			}
			for _, column := range columns {
				copy(p.x, before)
				for _, id := range dummies {
					if p.itemX(id) != column {
						moveTo(id, column)
					}
				}
				n, move := total(), 0.0
				for i := range p.x {
					move += math.Abs(p.x[i] - before[i])
				}
				if n < bestTotal || n == bestTotal && best != nil && move < bestMove {
					best, bestTotal, bestMove = slices.Clone(p.x), n, move
				}
			}
			copy(p.x, before)
			if best != nil {
				copy(p.x, best)
				improved = true
			}
		}
		if !improved {
			break
		}
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
