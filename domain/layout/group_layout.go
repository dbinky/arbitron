package layout

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/arbitron/arbitron/domain/workflow"
)

// groupPlacer treats an already laid-out group as one atomic rectangle in its
// parent's ordinary rank/order search. Runtime state is never an input.
type groupPlacer struct {
	ctx        context.Context
	g          Graph
	m          Measurements
	c          RendererContract
	o          Options
	sizes      []nodeSize
	labels     map[int]labelSize
	blocks     map[string]groupBlock
	prefix     int
	portWidths map[string]float64
}

func arrangeGroups(ctx context.Context, g Graph, m Measurements, c RendererContract, o Options) (Geometry, error) {
	labels, err := edgeLabelSizes(g, m, c, o)
	if err != nil {
		return Geometry{}, err
	}
	sizes, err := nodeSizes(g, classifyEdges(g), m, c, o, labels)
	if err != nil {
		return Geometry{}, err
	}
	blocks, err := measureGroupBlocks(g, m, c)
	if err != nil {
		return Geometry{}, err
	}
	portWidths := groupPortWidths(g, labels, o)
	for i, n := range g.Nodes {
		id := GroupElementID(n.ID, "header", "")
		sizes[i].W = math.Max(sizes[i].W, math.Max(portWidths[groupIDJSON(id, "top")], portWidths[groupIDJSON(id, "bottom")]))
	}
	p := groupPlacer{ctx: ctx, g: g, m: m, c: c, o: o, sizes: sizes, labels: labels, blocks: blocks, portWidths: portWidths}
	var best Geometry
	var bestCost routeCost
	var routeErr error
	for prefix := 0; prefix <= o.CrossingSweeps; prefix++ {
		if err := ctx.Err(); err != nil {
			return Geometry{}, err
		}
		p.prefix = prefix
		ids := []string{}
		for _, n := range g.Nodes {
			if _, owned := g.Groups.Owner[n.ID]; !owned {
				ids = append(ids, n.ID)
			}
		}
		geo, err := p.region(ids)
		if err != nil {
			return Geometry{}, err
		}
		// Restore manifest order after recursive expansion.
		nodes := map[string]NodeBox{}
		for _, n := range geo.Nodes {
			nodes[n.ID] = n
		}
		geo.Nodes = nil
		for _, n := range g.Nodes {
			geo.Nodes = append(geo.Nodes, nodes[n.ID])
		}
		geo.Edges = make([]EdgeRoute, len(g.Edges))
		for _, e := range g.Edges {
			geo.Edges[e.Ordinal] = EdgeRoute{Ordinal: e.Ordinal, Source: e.Source, Target: e.Target, Output: e.Output, Label: e.Label, Kind: classifyEdges(g)[e.Ordinal]}
		}
		// Route real occurrences and presentation connectors in the same obstacle
		// field. No execution edge is added, replaced, or coalesced.
		geo.GroupConnectors = expectedGroupConnectors(g)
		if err := routeGroupGeometry(g, &geo, m); err != nil {
			routeErr = err
			continue
		}
		geo.Format = FormatV2
		geo.Contract = c
		geo.Options = o
		if err := geo.Validate(g); err != nil {
			return Geometry{}, fmt.Errorf("layout: grouped candidate: %w", err)
		}
		cost := groupRouteCost(g, geo)
		if len(best.Nodes) == 0 || cost.less(bestCost) {
			best, bestCost = geo, cost
		}
		if cost.contacts == 0 && cost.crossings == 0 {
			break
		}
	}
	if len(best.Nodes) == 0 {
		return Geometry{}, routeErr
	}
	return best, nil
}

func (p *groupPlacer) region(ids []string) (Geometry, error) {
	local := Graph{index: map[string]int{}}
	children := map[string]Geometry{}
	sizes := []nodeSize{}
	for _, id := range ids {
		i, _ := p.g.NodeIndex(id)
		local.index[id] = len(local.Nodes)
		local.Nodes = append(local.Nodes, p.g.Nodes[i])
		s := p.sizes[i]
		if _, group := p.g.Groups.Groups[id]; group {
			child, err := p.group(id)
			if err != nil {
				return Geometry{}, err
			}
			children[id] = child
			s.W = child.Canvas.W
			s.H = child.Canvas.H
		}
		sizes = append(sizes, s)
	}
	labels := map[int]labelSize{}
	for _, e := range p.g.Edges {
		_, src := local.index[e.Source]
		_, dst := local.index[e.Target]
		if !src || !dst {
			continue
		}
		copy := e
		copy.Ordinal = len(local.Edges)
		labels[copy.Ordinal] = p.labels[e.Ordinal]
		if e.Text == "" {
			delete(labels, copy.Ordinal)
		}
		local.Edges = append(local.Edges, copy)
	}
	kinds := classifyEdges(local)
	ranks := assignRanks(local, kinds)
	layers := buildLayering(local, kinds, ranks)
	if err := orderLayers(p.ctx, layers, p.prefix); err != nil {
		return Geometry{}, err
	}
	// Parallel occurrences need independent entry/exit stubs even when their
	// endpoint columns line up. Reserve the complete port fan, not one gap.
	degree := make(map[string]int)
	maxDegree := 1
	for _, e := range p.g.Edges {
		degree["out"+e.Source]++
		degree["in"+e.Target]++
		maxDegree = max(maxDegree, degree["out"+e.Source], degree["in"+e.Target])
	}
	localOptions := p.o
	localOptions.RankSpacing = math.Max(localOptions.RankSpacing, float64(2*(maxDegree+1))*routingClearance(p.c, p.o)+maxLabelHeight(p.labels))
	placed := newPlacement(local, kinds, ranks, layers, sizes, labels, p.c, localOptions).route()
	out := Geometry{Format: FormatV2, Contract: p.c, Options: p.o, Canvas: placed.Canvas, Crossings: []Crossing{}}
	for _, n := range placed.Nodes {
		if child, ok := children[n.ID]; ok {
			translateGroupGeometry(&child, n.Box.X, n.Box.Y)
			out.Nodes = append(out.Nodes, child.Nodes...)
			out.GroupElements = append(out.GroupElements, child.GroupElements...)
			out.GroupLanes = append(out.GroupLanes, child.GroupLanes...)
		} else {
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out, nil
}

func (p *groupPlacer) element(gid, role, key string, box Rect) GroupElement {
	id := GroupElementID(gid, role, key)
	b := p.blocks[id]
	e := GroupElement{ID: id, GroupNodeID: gid, TaskKey: key, Role: role, Box: box, Texts: slices.Clone(b.texts)}
	pad := p.c.NodePaddingX + p.c.NodeBorderPx
	for i := range e.Texts {
		e.Texts[i].Box.X += box.X + pad
		e.Texts[i].Box.Y += box.Y + p.c.NodePaddingY + p.c.NodeBorderPx
	}
	if b.overlay != nil {
		r := *b.overlay
		r.X += box.X + pad
		r.Y += box.Y + p.c.NodePaddingY + p.c.NodeBorderPx
		e.OverlayBox = &r
	}
	return e
}

func (p *groupPlacer) group(id string) (Geometry, error) {
	scope := p.g.Groups.Groups[id]
	c, o := p.c, p.o
	pad := c.GroupPaddingPx + o.NodeTextSlack
	taskGap := c.GroupTaskGapPx + math.Max(0, o.NodeSpacing-DefaultOptions().NodeSpacing)
	rowGap := c.GroupRowGapPx + math.Max(0, o.RankSpacing-DefaultOptions().RankSpacing)
	i, _ := p.g.NodeIndex(id)
	hs := p.sizes[i]
	hs.H = math.Max(hs.H, c.GroupHeaderMinHeight)
	container := p.blocks[GroupElementID(id, "container", "")]
	footer := p.blocks[GroupElementID(id, "footer", "")]
	fw := math.Max(hs.W, math.Max(c.GroupMinWidth, footer.w+2*(c.NodePaddingX+c.NodeBorderPx)))
	footerID := GroupElementID(id, "footer", "")
	fw = math.Max(fw, math.Max(p.portWidths[groupIDJSON(footerID, "top")], p.portWidths[groupIDJSON(footerID, "bottom")]))
	fh := math.Max(c.GroupFooterMinHeight, footer.h+2*(c.NodePaddingY+c.NodeBorderPx))
	type lane struct {
		key         string
		w, h, headH float64
		body        Geometry
	}
	lanes := []lane{}
	totalW, maxH := 0.0, 0.0
	for _, task := range scope.Config.Tasks {
		role := "lane"
		if scope.Config.Form == workflow.GroupFormWorkflows {
			role = "task"
		}
		block := p.blocks[GroupElementID(id, role, task.Key)]
		l := lane{key: task.Key, w: math.Max(c.GroupMinWidth, block.w+2*pad), headH: block.h + 2*pad}
		l.h = math.Max(c.GroupTaskMinHeight, l.headH)
		if scope.Config.Form == workflow.GroupFormBranches {
			body, err := p.region(scope.Bodies[task.Key])
			if err != nil {
				return Geometry{}, err
			}
			l.body = body
			l.w = math.Max(l.w, body.Canvas.W+2*pad)
			l.h = l.headH + rowGap + body.Canvas.H + pad
		}
		totalW += l.w
		maxH = math.Max(maxH, l.h)
		lanes = append(lanes, l)
	}
	totalW += float64(len(lanes)-1) * taskGap
	// Keep the container caption out of the incoming header port corridor.
	width := math.Max(totalW+2*pad, math.Max(fw+2*pad, 2*(container.w+2*pad)+hs.W))
	hy := container.h + 2*pad
	headerBox := Rect{X: (width - hs.W) / 2, Y: hy, W: hs.W, H: hs.H}
	ly := headerBox.Bottom() + rowGap + maxLabelHeight(p.labels) + float64(len(scope.Config.Tasks)+1)*routingClearance(c, o)
	footerPorts := math.Max(float64(len(scope.Config.Tasks)+1), p.portWidths[groupIDJSON(footerID, "top")]/o.PortSpacing)
	fy := ly + maxH + rowGap + footerPorts*routingClearance(c, o)
	footerBox := Rect{X: (width - fw) / 2, Y: fy, W: fw, H: fh}
	out := Geometry{Format: FormatV2, Contract: c, Options: o, Canvas: Rect{W: width, H: footerBox.Bottom() + pad}, Crossings: []Crossing{}}
	out.GroupElements = append(out.GroupElements, p.element(id, "container", "", out.Canvas), p.element(id, "header", "", headerBox), p.element(id, "footer", "", footerBox))
	// Header supplements follow the primary label and type rows, not their origin.
	he := &out.GroupElements[1]
	label := p.m.Items[NodeLabelKey(id)]
	caption := p.m.Items[NodeTypeKey(id)]
	dy := c.NodeTypeBarPx + ceilPx(label.Height) + ceilPx(caption.Height) + 2*c.NodeTextGapPx
	for j := range he.Texts {
		he.Texts[j].Box.Y += dy
	}
	out.Nodes = append(out.Nodes, NodeBox{ID: id, Text: p.g.Nodes[i].Text, TypeCaption: p.g.Nodes[i].TypeCaption, TextLines: hs.TextLines, Box: headerBox, Rank: 0})
	x := (width - totalW) / 2
	for _, l := range lanes {
		box := Rect{X: x, Y: ly, W: l.w, H: maxH}
		ids := slices.Clone(scope.Bodies[l.key])
		if ids == nil {
			ids = []string{}
		}
		out.GroupLanes = append(out.GroupLanes, GroupLane{GroupNodeID: id, TaskKey: l.key, Box: box, NodeIDs: ids})
		role := "lane"
		if scope.Config.Form == workflow.GroupFormWorkflows {
			role = "task"
			box.H = math.Max(c.GroupTaskMinHeight, l.headH)
		}
		out.GroupElements = append(out.GroupElements, p.element(id, role, l.key, box))
		if len(l.body.Nodes) > 0 {
			body := l.body
			translateGroupGeometry(&body, x+(l.w-body.Canvas.W)/2, ly+l.headH+rowGap)
			out.Nodes = append(out.Nodes, body.Nodes...)
			out.GroupElements = append(out.GroupElements, body.GroupElements...)
			out.GroupLanes = append(out.GroupLanes, body.GroupLanes...)
		}
		x += l.w + taskGap
	}
	return out, nil
}

func groupPortWidths(g Graph, labels map[int]labelSize, o Options) map[string]float64 {
	widths := map[string]float64{}
	add := func(element, side string, slot float64) {
		key := groupIDJSON(element, side)
		if _, ok := widths[key]; !ok {
			widths[key] = o.PortSpacing
		}
		widths[key] += slot
	}
	for _, e := range g.Edges {
		if e.Source == e.Target {
			continue
		}
		source := GroupElementID(e.Source, "header", "")
		if g.Groups.Groups[e.Source] != nil && e.Output != "task" {
			source = GroupElementID(e.Source, "footer", "")
		}
		slot := o.PortSpacing
		if l, ok := labels[e.Ordinal]; ok {
			slot += o.LabelGap + l.W
		}
		add(source, "bottom", slot)
		add(GroupElementID(e.Target, "header", ""), "top", o.PortSpacing)
	}
	for _, e := range expectedGroupConnectors(g) {
		add(e.SourceElementID, "bottom", o.PortSpacing)
		add(e.TargetElementID, "top", o.PortSpacing)
	}
	return widths
}

func maxLabelHeight(labels map[int]labelSize) float64 {
	h := 0.0
	for _, l := range labels {
		h = math.Max(h, l.H)
	}
	return h
}

func translateGroupGeometry(g *Geometry, dx, dy float64) {
	move := func(r *Rect) { r.X = roundPx(r.X + dx); r.Y = roundPx(r.Y + dy) }
	for i := range g.Nodes {
		move(&g.Nodes[i].Box)
	}
	for i := range g.GroupElements {
		e := &g.GroupElements[i]
		move(&e.Box)
		for j := range e.Texts {
			move(&e.Texts[j].Box)
		}
		if e.OverlayBox != nil {
			move(e.OverlayBox)
		}
	}
	for i := range g.GroupLanes {
		move(&g.GroupLanes[i].Box)
	}
}

func expectedGroupConnectors(g Graph) []GroupConnector {
	var out []GroupConnector
	add := func(gid, key, kind, source, target string) {
		out = append(out, GroupConnector{ID: groupConnectorID(gid, kind, key, source, target), GroupNodeID: gid, TaskKey: key, Kind: kind, SourceElementID: source, TargetElementID: target})
	}
	for _, id := range g.Groups.Order {
		scope := g.Groups.Groups[id]
		footer := GroupElementID(id, "footer", "")
		for _, task := range scope.Config.Tasks {
			if scope.Config.Form == workflow.GroupFormWorkflows {
				card := GroupElementID(id, "task", task.Key)
				add(id, task.Key, "fanout", GroupElementID(id, "header", ""), card)
				add(id, task.Key, "gather", card, footer)
			} else {
				for _, node := range scope.Bodies[task.Key] {
					hasExit := false
					for _, e := range g.Edges {
						if e.Source == node && (g.Groups.Groups[node] == nil || e.Output != workflow.GroupPortTask) {
							hasExit = true
							break
						}
					}
					if !hasExit {
						source := GroupElementID(node, "header", "")
						if g.Groups.Groups[node] != nil {
							source = GroupElementID(node, "footer", "")
						}
						add(id, task.Key, "sink", source, footer)
					}
				}
			}
		}
	}
	return out
}
