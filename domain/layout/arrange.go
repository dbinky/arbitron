package layout

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
)

// ErrContractMismatch rejects measurements from any other full renderer contract.
var ErrContractMismatch = errors.New("layout: measurements were taken with a different renderer contract")

// ErrFontsNotReady rejects measurements taken before the renderer loaded its fonts.
var ErrFontsNotReady = errors.New("layout: renderer fonts were not ready when measuring")

// Arrange produces a presentation-only candidate from captured graph and measured
// renderer text. Bounded search compares complete routes, not layer inversions.
// Graphs over the arrangement caps fail with ErrGraphTooLarge; ctx is checked
// between candidates and crossing-reduction passes, returning ctx.Err().
func Arrange(ctx context.Context, g Graph, m Measurements, c RendererContract, o Options) (Geometry, error) {
	if err := validateArrangeGraph(g); err != nil {
		return Geometry{}, err
	}
	if err := validateGeometryContract(c); err != nil {
		return Geometry{}, err
	}
	if err := validateGeometryOptions(o); err != nil {
		return Geometry{}, err
	}
	if m.Contract != c {
		return Geometry{}, ErrContractMismatch
	}
	if !m.FontsReady {
		return Geometry{}, ErrFontsNotReady
	}
	if g.Groups != nil && len(g.Groups.Order) > 0 {
		return arrangeGroups(ctx, g, m, c, o)
	}
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	labels, err := edgeLabelSizes(g, m, c, o)
	if err != nil {
		return Geometry{}, err
	}
	sizes, err := nodeSizes(g, kinds, m, c, o, labels)
	if err != nil {
		return Geometry{}, err
	}
	initial := buildLayering(g, kinds, ranks)
	var best Geometry
	var bestCost routeCost
	var orders [][][]int
	for prefix := 0; prefix <= o.CrossingSweeps; prefix++ {
		if err := ctx.Err(); err != nil {
			return Geometry{}, err
		}
		l := *initial
		l.rows = cloneRows(initial.rows)
		if err := orderLayers(ctx, &l, prefix); err != nil {
			return Geometry{}, err
		}
		duplicate := false
		for _, rows := range orders {
			if reflect.DeepEqual(rows, l.rows) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		orders = append(orders, cloneRows(l.rows))
		candidate := newPlacement(g, kinds, ranks, &l, sizes, labels, c, o).route()
		if err := candidate.Validate(g); err != nil {
			return Geometry{}, fmt.Errorf("layout: routed candidate: %w", err)
		}
		cost := costOf(candidate)
		if len(best.Nodes) == 0 || cost.less(bestCost) {
			best, bestCost = candidate, cost
		}
		if bestCost.contacts == 0 && bestCost.crossings == 0 {
			break
		}
	}
	return best, nil
}

func validateArrangeGraph(g Graph) error {
	if len(g.Nodes) == 0 {
		return ErrEmptyGraph
	}
	if len(g.index) != len(g.Nodes) {
		return fmt.Errorf("%w: graph must come from BuildGraph (index coverage)", ErrInvalidGeometry)
	}
	if err := g.validateGroups(); err != nil {
		return err
	}
	seen := make(map[string]bool, len(g.Nodes))
	for i, n := range g.Nodes {
		if n.ID == "" {
			return ErrEmptyNodeID
		}
		if seen[n.ID] {
			return fmt.Errorf("%w: %s", ErrDuplicateNode, n.ID)
		}
		seen[n.ID] = true
		if index, ok := g.NodeIndex(n.ID); !ok || index != i {
			return fmt.Errorf("%w: graph index for %q", ErrInvalidGeometry, n.ID)
		}
	}
	for i, e := range g.Edges {
		if e.Ordinal != i {
			return fmt.Errorf("%w: graph ordinal %d at %d", ErrInvalidGeometry, e.Ordinal, i)
		}
		if _, ok := g.NodeIndex(e.Source); !ok {
			return fmt.Errorf("%w: source %q", ErrDanglingEdge, e.Source)
		}
		if _, ok := g.NodeIndex(e.Target); !ok {
			return fmt.Errorf("%w: target %q", ErrDanglingEdge, e.Target)
		}
	}
	return checkArrangeBounds(g)
}

func roundPx(v float64) float64 { return quantizedPoint(Point{X: v}).X }

// normalize includes stroke and the complete triangular destination marker, not
// just node/path centre lines. Rounding happens before proper crossing discovery.
func normalize(geo Geometry) Geometry {
	left, top, right, bottom := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	include := func(r Rect) {
		left = math.Min(left, r.X)
		top = math.Min(top, r.Y)
		right = math.Max(right, r.Right())
		bottom = math.Max(bottom, r.Bottom())
	}
	for i := range geo.Nodes {
		n := &geo.Nodes[i]
		n.Box.X = roundPx(n.Box.X)
		n.Box.Y = roundPx(n.Box.Y)
		n.Box.W = roundPx(n.Box.W)
		n.Box.H = roundPx(n.Box.H)
		include(n.Box)
	}
	for i := range geo.Edges {
		e := &geo.Edges[i]
		e.Points = simplify(e.Points)
		for _, p := range e.Points {
			include(Rect{X: p.X, Y: p.Y}.Inflate(geo.Contract.StrokePx / 2))
		}
		if len(e.Points) >= 2 {
			include(arrowBounds(e.Points, geo.Contract.ArrowPx).Inflate(geo.Contract.StrokePx / 2))
		}
		if e.Text != nil {
			b := &e.Text.Box
			b.X = roundPx(b.X)
			b.Y = roundPx(b.Y)
			b.W = roundPx(b.W)
			b.H = roundPx(b.H)
			include(*b)
		}
	}
	// Ceiling the shift preserves Margin even when bounds contain half-pixels.
	dx := math.Ceil((geo.Options.Margin-left)/geomEps) * geomEps
	dy := math.Ceil((geo.Options.Margin-top)/geomEps) * geomEps
	move := func(p Point) Point { return quantizedPoint(Point{p.X + dx, p.Y + dy}) }
	for i := range geo.Nodes {
		b := &geo.Nodes[i].Box
		p := move(Point{b.X, b.Y})
		b.X, b.Y = p.X, p.Y
	}
	for i := range geo.Edges {
		e := &geo.Edges[i]
		for j := range e.Points {
			e.Points[j] = move(e.Points[j])
		}
		if len(e.Points) > 0 {
			e.SourceAttach.Point = e.Points[0]
			e.TargetAttach.Point = e.Points[len(e.Points)-1]
		}
		if e.Text != nil {
			b := &e.Text.Box
			p := move(Point{b.X, b.Y})
			b.X, b.Y = p.X, p.Y
		}
	}
	geo.Canvas = Rect{W: math.Ceil((right+dx+geo.Options.Margin)/geomEps) * geomEps, H: math.Ceil((bottom+dy+geo.Options.Margin)/geomEps) * geomEps}
	geo.Crossings = findCrossings(geo.Edges)
	if geo.Crossings == nil {
		geo.Crossings = []Crossing{}
	}
	return geo
}
