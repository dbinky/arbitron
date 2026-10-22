package layout

// classifyEdges labels every edge occurrence (indexed by ordinal) using the
// dashboard's DFS back-edge semantics (dashboard/src/graph/loops.ts
// findBackEdges): DFS roots are edge sources in edge order, then every
// remaining node in manifest order; children are visited in edge order; an
// edge to the node itself is a self loop and an edge to a node still on the
// DFS stack closes a cycle (loop return). Unlike the dashboard, identity is the
// edge ordinal, so identical parallel edges are each classified.
func classifyEdges(g Graph) []EdgeKind {
	n := len(g.Nodes)
	kinds := make([]EdgeKind, len(g.Edges))
	adj := make([][]int, n)
	for _, e := range g.Edges {
		kinds[e.Ordinal] = EdgeForward
		if e.Source == e.Target {
			kinds[e.Ordinal] = EdgeSelfLoop
		}
		s, _ := g.NodeIndex(e.Source)
		adj[s] = append(adj[s], e.Ordinal)
	}

	roots := make([]int, 0, n)
	isRoot := make([]bool, n)
	for _, e := range g.Edges {
		s, _ := g.NodeIndex(e.Source)
		if !isRoot[s] {
			isRoot[s] = true
			roots = append(roots, s)
		}
	}
	for i := 0; i < n; i++ {
		if !isRoot[i] {
			isRoot[i] = true
			roots = append(roots, i)
		}
	}

	type frame struct{ node, next int }
	visited := make([]bool, n)
	onStack := make([]bool, n)
	for _, root := range roots {
		if visited[root] {
			continue
		}
		visited[root], onStack[root] = true, true
		stack := []frame{{node: root}}
		for len(stack) > 0 {
			top := len(stack) - 1
			node := stack[top].node
			if stack[top].next >= len(adj[node]) {
				onStack[node] = false
				stack = stack[:top]
				continue
			}
			ord := adj[node][stack[top].next]
			stack[top].next++
			child, _ := g.NodeIndex(g.Edges[ord].Target)
			switch {
			case child == node:
				// self loop, already classified
			case onStack[child]:
				kinds[ord] = EdgeLoopReturn
			case !visited[child]:
				visited[child], onStack[child] = true, true
				stack = append(stack, frame{node: child})
			}
		}
	}
	return kinds
}

// loopBody returns, in manifest order, the node indexes of the loop closed by
// the loop-return (or self-loop) edge ordinal: its head (target), its tail
// (source) and every node that is both forward-reachable from the head and
// forward-reaches the tail. Only forward edges are followed, so one loop's body
// cannot leak into another through a shared head (as in loops.ts).
func loopBody(g Graph, kinds []EdgeKind, ordinal int) []int {
	e := g.Edges[ordinal]
	head, _ := g.NodeIndex(e.Target)
	tail, _ := g.NodeIndex(e.Source)
	if head == tail {
		return []int{head}
	}
	n := len(g.Nodes)
	fwd := make([][]int, n)
	rev := make([][]int, n)
	for _, fe := range g.Edges {
		if kinds[fe.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(fe.Source)
		t, _ := g.NodeIndex(fe.Target)
		fwd[s] = append(fwd[s], t)
		rev[t] = append(rev[t], s)
	}
	down := reach(head, fwd, n)
	up := reach(tail, rev, n)
	body := make([]int, 0)
	for i := 0; i < n; i++ {
		if i == head || i == tail || (down[i] && up[i]) {
			body = append(body, i)
		}
	}
	return body
}

// reach is a BFS over adjacency lists; it marks start and everything reachable.
func reach(start int, adj [][]int, n int) []bool {
	seen := make([]bool, n)
	seen[start] = true
	queue := []int{start}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range adj[v] {
			if !seen[w] {
				seen[w] = true
				queue = append(queue, w)
			}
		}
	}
	return seen
}
