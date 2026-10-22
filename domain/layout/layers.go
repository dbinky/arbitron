package layout

import (
	"context"
	"sort"
)

// layerItem reserves a slot for a real node or a long forward edge occurrence.
type layerItem struct {
	node int // node index, or -1 for a dummy
	edge int // edge ordinal for a dummy, or -1 for a node
	rank int
}

// layering holds rank rows, forward edge chains and occurrence-preserving
// adjacency between consecutive rows.
type layering struct {
	rows       [][]int
	items      []layerItem
	itemOfNode []int
	chains     map[int][]int
	up, down   [][]int
}

// buildLayering places nodes in manifest order, then reserves a separate dummy
// in each intermediate rank for every long forward edge occurrence.
func buildLayering(g Graph, kinds []EdgeKind, ranks []int) *layering {
	maxRank := 0
	for _, r := range ranks {
		maxRank = max(maxRank, r)
	}
	l := &layering{
		rows:       make([][]int, maxRank+1),
		itemOfNode: make([]int, len(g.Nodes)),
		chains:     make(map[int][]int),
	}
	addItem := func(it layerItem) int {
		id := len(l.items)
		l.items = append(l.items, it)
		l.up = append(l.up, nil)
		l.down = append(l.down, nil)
		l.rows[it.rank] = append(l.rows[it.rank], id)
		return id
	}
	for i := range g.Nodes {
		l.itemOfNode[i] = addItem(layerItem{node: i, edge: -1, rank: ranks[i]})
	}
	for _, e := range g.Edges {
		if kinds[e.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		chain := []int{l.itemOfNode[s]}
		for r := ranks[s] + 1; r < ranks[t]; r++ {
			chain = append(chain, addItem(layerItem{node: -1, edge: e.Ordinal, rank: r}))
		}
		chain = append(chain, l.itemOfNode[t])
		for k := 0; k+1 < len(chain); k++ {
			l.down[chain[k]] = append(l.down[chain[k]], chain[k+1])
			l.up[chain[k+1]] = append(l.up[chain[k+1]], chain[k])
		}
		l.chains[e.Ordinal] = chain
	}
	return l
}

// orderLayers evaluates a deterministic prefix of barycentre and transposition
// candidates. Increasing the budget retains every smaller budget's candidates.
// It checks ctx before every round and transpose pass, returning ctx.Err() with
// l.rows unspecified.
func orderLayers(ctx context.Context, l *layering, sweeps int) error {
	if sweeps <= 0 {
		return nil
	}
	best, bestCross := cloneRows(l.rows), l.crossings()
	retainBest := func() {
		if score := l.crossings(); score < bestCross {
			best, bestCross = cloneRows(l.rows), score
		}
	}
	for round := 1; round <= sweeps && bestCross > 0; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for r := 1; r < len(l.rows); r++ {
			l.sortRowBy(r, l.up)
		}
		for r := len(l.rows) - 2; r >= 0; r-- {
			l.sortRowBy(r, l.down)
		}
		trajectory := cloneRows(l.rows)
		retainBest()
		if err := l.transpose(ctx, round); err != nil {
			return err
		}
		retainBest()
		// Transposition must not change the next barycentre round's starting point.
		l.rows = trajectory
	}
	l.rows = cloneRows(best)
	return nil
}

func cloneRows(rows [][]int) [][]int {
	out := make([][]int, len(rows))
	for r, row := range rows {
		out[r] = append([]int(nil), row...)
	}
	return out
}

// sortRowBy uses mean adjacent-row positions, retaining current position for
// isolated items. Stable sorting preserves the current order on equal keys.
func (l *layering) sortRowBy(r int, neighbors [][]int) {
	pos := l.positions()
	keys := make([]float64, len(l.items))
	for _, id := range l.rows[r] {
		keys[id] = float64(pos[id])
		if len(neighbors[id]) == 0 {
			continue
		}
		sum := 0.0
		for _, n := range neighbors[id] {
			sum += float64(pos[n])
		}
		keys[id] = sum / float64(len(neighbors[id]))
	}
	sort.SliceStable(l.rows[r], func(i, j int) bool {
		return keys[l.rows[r][i]] < keys[l.rows[r][j]]
	})
}

// transpose visits rows and pairs left-to-right, accepting only strict total
// crossing reductions. It stops at the pass budget or a local minimum, or with
// ctx.Err() (checked per row, since one pass is O(items·E²)).
func (l *layering) transpose(ctx context.Context, passes int) error {
	best := l.crossings()
	for pass := 0; pass < passes && best > 0; pass++ {
		improved := false
		for _, row := range l.rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			for i := 0; i+1 < len(row); i++ {
				row[i], row[i+1] = row[i+1], row[i]
				if score := l.crossings(); score < best {
					best, improved = score, true
				} else {
					row[i], row[i+1] = row[i+1], row[i]
				}
			}
		}
		if !improved {
			break
		}
	}
	return nil
}

// positions maps item ids to positions within their rows.
func (l *layering) positions() []int {
	pos := make([]int, len(l.items))
	for _, row := range l.rows {
		for i, id := range row {
			pos[id] = i
		}
	}
	return pos
}

// crossings counts adjacent-row segment pairs with strictly reversed endpoints.
// Shared endpoints are not crossings; parallel occurrences retain multiplicity.
func (l *layering) crossings() int {
	pos := l.positions()
	total := 0
	for r := 0; r+1 < len(l.rows); r++ {
		type seg struct{ a, b int }
		var segs []seg
		for _, id := range l.rows[r] {
			for _, d := range l.down[id] {
				segs = append(segs, seg{pos[id], pos[d]})
			}
		}
		for i := 0; i < len(segs); i++ {
			for j := i + 1; j < len(segs); j++ {
				if (segs[i].a < segs[j].a && segs[i].b > segs[j].b) || (segs[i].a > segs[j].a && segs[i].b < segs[j].b) {
					total++
				}
			}
		}
	}
	return total
}
