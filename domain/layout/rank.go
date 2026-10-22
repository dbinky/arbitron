package layout

// topoOrder returns node indexes in a topological order of the forward-edge
// subgraph (Kahn's algorithm, ready queue seeded in manifest order and appended
// in manifest edge order, so the order is deterministic). classifyEdges guarantees
// the forward subgraph is acyclic, so every node appears exactly once.
func topoOrder(g Graph, kinds []EdgeKind) []int {
	n := len(g.Nodes)
	indeg := make([]int, n)
	out := make([][]int, n)
	for _, e := range g.Edges {
		if kinds[e.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		out[s] = append(out[s], t)
		indeg[t]++
	}
	order := make([]int, 0, n)
	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		order = append(order, v)
		for _, t := range out[v] {
			indeg[t]--
			if indeg[t] == 0 {
				queue = append(queue, t)
			}
		}
	}
	return order
}

// assignRanks gives every node its longest-path distance from a source over
// forward edges. Loop returns and self loops are excluded, so every forward
// edge points to a strictly greater rank (continuous downward progression) and
// every loop return points to a strictly smaller one.
func assignRanks(g Graph, kinds []EdgeKind) []int {
	ranks := make([]int, len(g.Nodes))
	out := make([][]int, len(g.Nodes))
	for _, e := range g.Edges {
		if kinds[e.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		out[s] = append(out[s], t)
	}
	for _, v := range topoOrder(g, kinds) {
		for _, t := range out[v] {
			if ranks[v]+1 > ranks[t] {
				ranks[t] = ranks[v] + 1
			}
		}
	}
	return ranks
}
