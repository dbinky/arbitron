package layout

import (
	"errors"
	"fmt"
)

// Arrangement size caps. Crossing reduction and candidate routing are
// super-linear (route crossing discovery is quadratic in segments, transpose is
// O(items·E²) per pass), so an unbounded manifest could pin a worker slot for
// hours. The caps sit well above the largest shipped sample (feature-pipeline:
// 66 nodes, 79 edges) while keeping a worst-case graph at the caps — dense
// crossing bipartite rows or long edges filling every rank — to a few seconds
// of Arrange with DefaultOptions. Over-cap graphs fail before measuring.
const (
	// MaxArrangeNodes caps workflow nodes.
	MaxArrangeNodes = 150
	// MaxArrangeEdges caps workflow edges (every occurrence, including loops).
	MaxArrangeEdges = 160
	// MaxArrangeRowWidth caps the items in one rank row: real nodes plus one
	// reserved slot per long forward edge passing through that rank.
	MaxArrangeRowWidth = 40
	// MaxArrangeLayerItems caps all row items (nodes plus long-edge slots).
	MaxArrangeLayerItems = 600
)

// ErrGraphTooLarge rejects manifests whose arrangement cost is not bounded.
var ErrGraphTooLarge = errors.New("layout: workflow is too large to arrange")

// checkArrangeBounds rejects graphs over the arrangement caps in O(N+E), before
// any crossing search. g must have a complete index (BuildGraph output).
func checkArrangeBounds(g Graph) error {
	if n := len(g.Nodes); n > MaxArrangeNodes {
		return fmt.Errorf("%w: %d nodes exceeds the limit of %d", ErrGraphTooLarge, n, MaxArrangeNodes)
	}
	if e := len(g.Edges); e > MaxArrangeEdges {
		return fmt.Errorf("%w: %d edges exceeds the limit of %d", ErrGraphTooLarge, e, MaxArrangeEdges)
	}
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	maxRank := 0
	for _, r := range ranks {
		maxRank = max(maxRank, r)
	}
	// width[r] counts nodes; diff accumulates long-edge slots per rank.
	width := make([]int, maxRank+1)
	diff := make([]int, maxRank+2)
	for _, r := range ranks {
		width[r]++
	}
	items := len(g.Nodes)
	for _, e := range g.Edges {
		if kinds[e.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		if span := ranks[t] - ranks[s] - 1; span > 0 {
			diff[ranks[s]+1]++
			diff[ranks[t]]--
			items += span
		}
	}
	if items > MaxArrangeLayerItems {
		return fmt.Errorf("%w: %d row items (nodes plus long-edge slots) exceeds the limit of %d", ErrGraphTooLarge, items, MaxArrangeLayerItems)
	}
	slots := 0
	for r := range width {
		slots += diff[r]
		if w := width[r] + slots; w > MaxArrangeRowWidth {
			return fmt.Errorf("%w: rank %d holds %d items (nodes plus long-edge slots), exceeding the row width limit of %d", ErrGraphTooLarge, r, w, MaxArrangeRowWidth)
		}
	}
	return nil
}
