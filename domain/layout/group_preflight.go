package layout

import (
	"fmt"
	"math"
)

func preflightGroups(g Graph, geo Geometry, m Measurements) []Finding {
	fs := []Finding{}
	add := func(criterion Criterion, gid, evidence string) {
		fs = append(fs, Finding{Criterion: criterion, Severity: SeverityBlocking, Source: SourcePreflight, NodeIDs: []string{gid}, Evidence: evidence})
	}
	blocks, err := measureGroupBlocks(g, m, geo.Contract)
	if err != nil {
		add(CriterionCoverage, g.Groups.Order[0], err.Error())
	} else {
		for _, e := range geo.GroupElements {
			block := blocks[e.ID]
			for i, t := range e.Texts {
				if i >= len(block.texts) || t.Box.W+geomEps < block.texts[i].Box.W || t.Box.H+geomEps < block.texts[i].Box.H {
					add(CriterionTextClipped, e.GroupNodeID, "measured text does not fit "+t.Key)
				}
			}
			if block.overlay != nil && (e.OverlayBox == nil || e.OverlayBox.W+geomEps < block.overlay.W || e.OverlayBox.H+geomEps < block.overlay.H) {
				add(CriterionTextClipped, e.GroupNodeID, "combined live rows do not fit "+e.ID)
			}
		}
	}
	fs = append(fs, groupContacts(g, geo)...)
	return fs
}

func groupContacts(g Graph, geo Geometry) []Finding {
	fs := []Finding{}
	add := func(criterion Criterion, gid, evidence string) {
		fs = append(fs, Finding{Criterion: criterion, Severity: SeverityBlocking, Source: SourcePreflight, NodeIDs: []string{gid}, Evidence: evidence})
	}
	type obstacle struct {
		box            Rect
		id, gid, stack string
	}
	obstacles := []obstacle{}
	for _, n := range geo.Nodes {
		gid := g.Groups.Order[0]
		if g.Groups.Groups[n.ID] != nil {
			gid = n.ID
		}
		if owner, ok := g.Groups.Owner[n.ID]; ok {
			gid = owner.GroupNodeID
		}
		obstacles = append(obstacles, obstacle{n.Box, GroupElementID(n.ID, "header", ""), gid, GroupElementID(n.ID, "header", "")})
	}
	for _, e := range geo.GroupElements {
		if e.Role == "footer" || e.Role == "task" {
			obstacles = append(obstacles, obstacle{e.Box, e.ID, e.GroupNodeID, e.ID})
		}
		if e.Role == "container" || e.Role == "lane" {
			for _, t := range e.Texts {
				obstacles = append(obstacles, obstacle{t.Box, t.Key, e.GroupNodeID, e.ID})
			}
			if e.OverlayBox != nil {
				obstacles = append(obstacles, obstacle{*e.OverlayBox, e.ID + " live rows", e.GroupNodeID, e.ID})
			}
		}
	}
	for i, a := range obstacles {
		for _, b := range obstacles[i+1:] {
			clear := geo.Contract.MinClearancePx / 2
			if a.stack == b.stack {
				clear = 0
			}
			if a.box.Inflate(clear).Overlaps(b.box.Inflate(clear)) {
				add(CriterionNodeOverlap, a.gid, fmt.Sprintf("%s overlaps %s", a.id, b.id))
			}
		}
	}
	paths := []groupPath{}
	for _, e := range geo.Edges {
		paths = append(paths, groupPath{key: ManifestRouteKey(e.Ordinal), source: e.SourceAttach.ElementID, target: e.TargetAttach.ElementID, points: e.Points, label: e.Text, sa: e.SourceAttach, ta: e.TargetAttach, kind: e.Kind})
	}
	for _, e := range geo.GroupConnectors {
		paths = append(paths, groupPath{key: e.ID, source: e.SourceElementID, target: e.TargetElementID, points: e.Points, sa: e.SourceAttach, ta: e.TargetAttach, kind: EdgeForward})
	}
	boxes := groupElementBoxes(geo)
	for _, p := range paths {
		region := groupRouteRegion(geo, boxes[p.source], boxes[p.target])
		if !geo.Canvas.Contains(arrowBounds(p.points, geo.Contract.ArrowPx).Inflate(geo.Contract.StrokePx / 2)) {
			add(CriterionCoverage, g.Groups.Order[0], p.key+" arrow leaves canvas")
		}
		for _, point := range p.points {
			if !containsPoint(region, point) {
				add(CriterionCoverage, g.Groups.Order[0], p.key+" leaves its owning region")
			}
		}
		a, b, z, y := p.points[0], p.points[1], p.points[len(p.points)-1], p.points[len(p.points)-2]
		outward, inward := b.Y > a.Y && nearly(b.X, a.X), z.Y > y.Y && nearly(z.X, y.X)
		if p.sa.Side == SideRight {
			outward = b.X > a.X && nearly(b.Y, a.Y)
		}
		if p.ta.Side == SideRight {
			inward = z.X < y.X && nearly(z.Y, y.Y)
		}
		if !outward || !inward {
			add(CriterionArrowDirection, g.Groups.Order[0], p.key+" does not leave/enter "+p.source+" / "+p.target+" through its declared sides")
		}
		if p.kind == EdgeForward {
			for j := 1; j < len(p.points); j++ {
				if p.points[j].Y < p.points[j-1].Y-geomEps {
					add(CriterionUpwardProgression, g.Groups.Order[0], p.key+" climbs inside a forward route")
				}
			}
		}
		for j := 1; j < len(p.points); j++ {
			a, b := p.points[j-1], p.points[j]
			stroke := Rect{X: math.Min(a.X, b.X), Y: math.Min(a.Y, b.Y), W: math.Abs(a.X - b.X), H: math.Abs(a.Y - b.Y)}.Inflate(geo.Contract.StrokePx / 2)
			if !geo.Canvas.Contains(stroke) {
				add(CriterionCoverage, g.Groups.Order[0], p.key+" stroke leaves canvas")
			}
		}
	}
	for _, p := range paths {
		for _, o := range obstacles {
			for s := 1; s < len(p.points); s++ {
				if segmentHitsRect(p.points[s-1], p.points[s], o.box) {
					add(CriterionEdgeThroughNode, o.gid, p.key+" passes through "+o.id)
					break
				}
			}
		}
		for _, lane := range geo.GroupLanes {
			if pathOwnsLane(g, p, lane) {
				continue
			}
			for s := 1; s < len(p.points); s++ {
				if segmentHitsRect(p.points[s-1], p.points[s], lane.Box) {
					add(CriterionEdgeThroughNode, lane.GroupNodeID, p.key+" passes through unrelated lane "+GroupElementID(lane.GroupNodeID, "lane", lane.TaskKey))
					break
				}
			}
		}
		if p.label != nil {
			for _, o := range obstacles {
				if p.label.Box.Inflate(geo.Contract.MinClearancePx / 2).Overlaps(o.box.Inflate(geo.Contract.MinClearancePx / 2)) {
					add(CriterionTextCollision, o.gid, p.key+" label overlaps "+o.id)
				}
			}
		}
	}
	// Include both classes when checking merged strokes, arrow direction and gap
	// treatment, while keeping public findings tied to real groups (never invented
	// manifest ordinals).
	combined := geo
	combined.Edges = nil
	combined.Crossings = nil
	for i, p := range paths {
		combined.Edges = append(combined.Edges, EdgeRoute{Ordinal: i, Points: p.points, SourceAttach: p.sa, TargetAttach: p.ta, Text: p.label})
	}
	for i, p := range paths {
		if p.label != nil && !labelAssociated(combined, combined.Edges[i]) {
			add(CriterionIndistinctBranch, g.Groups.Order[0], p.key+" label is not closest to its own route")
		}
		for _, q := range paths {
			if q.label != nil {
				for j := 1; j < len(p.points); j++ {
					if segmentHitsRect(p.points[j-1], p.points[j], q.label.Box) {
						add(CriterionTextCollision, g.Groups.Order[0], p.key+" passes through "+q.key+" label")
					}
				}
			}
		}
	}
	for i, p := range paths {
		for _, q := range paths[i+1:] {
			for _, a := range []Attachment{p.sa, p.ta} {
				for _, b := range []Attachment{q.sa, q.ta} {
					if a.ElementID == b.ElementID && a.Side == b.Side && math.Hypot(a.Point.X-b.Point.X, a.Point.Y-b.Point.Y) < geo.Contract.StrokePx+geo.Contract.MinClearancePx {
						add(CriterionAmbiguousAttachment, g.Groups.Order[0], p.key+" shares a port on "+a.ElementID+" with "+q.key)
					}
				}
			}
			for a := 1; a < len(p.points); a++ {
				for b := 1; b < len(q.points); b++ {
					sa := segment{a: p.points[a-1], b: p.points[a], horiz: nearly(p.points[a-1].Y, p.points[a].Y)}
					sb := segment{a: q.points[b-1], b: q.points[b], horiz: nearly(q.points[b-1].Y, q.points[b].Y)}
					if sa.horiz == sb.horiz {
						gap, overlap := parallelDistance(sa, sb)
						if gap < geo.Contract.StrokePx+geo.Contract.MinClearancePx && overlap > geomEps {
							add(CriterionMergedLines, g.Groups.Order[0], p.key+" runs too close to "+q.key)
						}
					}
					point, ok := intersection(sa, sb)
					if !ok {
						continue
					}
					if !segmentInterior(point, sa) || !segmentInterior(point, sb) {
						add(CriterionAmbiguousCrossing, g.Groups.Order[0], p.key+" touches "+q.key+" at bend/endpoint")
						continue
					}
					reach := geo.Contract.CrossingGapPx/2 + geo.Contract.StrokePx
					for _, s := range []segment{sa, sb} {
						if math.Min(math.Hypot(point.X-s.a.X, point.Y-s.a.Y), math.Hypot(point.X-s.b.X, point.Y-s.b.Y)) < reach-geomEps {
							add(CriterionAmbiguousCrossing, g.Groups.Order[0], p.key+" crossing too close to "+q.key+" bend")
						}
					}
				}
			}
		}
	}
	return fs
}
