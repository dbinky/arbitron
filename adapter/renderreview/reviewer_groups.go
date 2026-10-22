package renderreview

import (
	"slices"

	"github.com/arbitron/arbitron/domain/layout"
)

func checkGroupCoverage(geo layout.Geometry, rep layout.RenderReport, f *findingSet) {
	if len(rep.GroupElements) != len(geo.GroupElements) || len(rep.GroupConnectors) != len(geo.GroupConnectors) || len(rep.GroupCrossings) != len(geo.GroupCrossings) {
		f.block(layout.CriterionCoverage, nil, nil, "group element/connector/crossing coverage differs from candidate")
	}
	for i, e := range rep.GroupElements {
		if i >= len(geo.GroupElements) {
			break
		}
		w := geo.GroupElements[i]
		if e.ID != w.ID || len(e.Texts) != len(w.Texts) {
			f.block(layout.CriterionCoverage, []string{w.GroupNodeID}, nil, "group element %s identity/text coverage differs", w.ID)
			continue
		}
		for j, t := range e.Texts {
			if t.Key != w.Texts[j].Key {
				f.block(layout.CriterionCoverage, []string{w.GroupNodeID}, nil, "group text %s identity differs", w.Texts[j].Key)
			}
		}
	}
	for i, e := range rep.GroupConnectors {
		if i < len(geo.GroupConnectors) && e.ID != geo.GroupConnectors[i].ID {
			f.block(layout.CriterionCoverage, []string{geo.GroupConnectors[i].GroupNodeID}, nil, "connector %s identity differs", geo.GroupConnectors[i].ID)
		}
	}
	for i, c := range rep.GroupCrossings {
		if i < len(geo.GroupCrossings) {
			w := geo.GroupCrossings[i]
			if c.Over != w.Over || c.Under != w.Under || !pointNear(c.Point, w.Point, .01) {
				f.block(layout.CriterionRenderMismatch, crossingOwners(geo, w), nil, "group crossing %s / %s identity/location differs", w.Over, w.Under)
			}
		}
	}
}

func crossingOwners(geo layout.Geometry, c layout.GroupCrossing) []string {
	var ids []string
	for _, e := range geo.GroupConnectors {
		if (e.ID == c.Over || e.ID == c.Under) && !slices.Contains(ids, e.GroupNodeID) {
			ids = append(ids, e.GroupNodeID)
		}
	}
	return ids
}

func groupFill(role string) string {
	if role == "container" || role == "lane" {
		return "#1a1a2e"
	}
	return "#1e1e38"
}
func sameColor(a, b string) bool {
	x, ok := colorRGB(a)
	y, ok2 := colorRGB(b)
	return ok && ok2 && x == y
}

func checkGroups(geo layout.Geometry, rep layout.RenderReport, f *findingSet) {
	if len(geo.GroupElements) == 0 {
		return
	}
	boxes := map[string]layout.Rect{}
	type obstacle struct {
		box       layout.Rect
		id, owner string
	}
	var obstacles []obstacle
	var texts []layout.RenderedText
	var textOwners []string
	for _, n := range rep.Nodes {
		boxes[layout.GroupElementID(n.ID, "header", "")] = n.Box
		obstacles = append(obstacles, obstacle{n.Box, layout.GroupElementID(n.ID, "header", ""), n.ID})
		texts = append(texts, n.Label, n.Type)
		textOwners = append(textOwners, n.ID, n.ID)
	}
	for i, e := range rep.GroupElements {
		w := geo.GroupElements[i]
		ids := []string{w.GroupNodeID}
		boxes[e.ID] = e.Box
		if !e.Visible || !rectNear(e.Box, w.Box, boxTolerancePx) || !near(e.BorderPx, geo.Contract.NodeBorderPx, strokeTolerancePx) || !sameColor(e.BorderColor, geo.Contract.ForwardColor) || !sameColor(e.BackgroundColor, groupFill(w.Role)) {
			f.block(layout.CriterionRenderMismatch, ids, nil, "group element %s actual box/paint differs from baseline", e.ID)
		}
		if w.Role == "footer" || w.Role == "task" {
			obstacles = append(obstacles, obstacle{e.Box, e.ID, w.GroupNodeID})
		}
		for j, t := range e.Texts {
			want := w.Texts[j]
			if t.Text.Content != want.Text || !rectNear(t.Text.Box, want.Box, boxTolerancePx) {
				f.block(layout.CriterionRenderMismatch, ids, nil, "group text %s content/box differs", t.Key)
			}
			checkText(t.Text, geo.Contract, rep.Zoom, ids, nil, f)
			for _, g := range t.Text.Glyphs {
				if !e.Box.Inflate(boxTolerancePx).Contains(g) {
					f.block(layout.CriterionTextClipped, ids, nil, "group text %s escapes element", t.Key)
				}
			}
			texts = append(texts, t.Text)
			textOwners = append(textOwners, w.GroupNodeID)
			if w.Role == "container" || w.Role == "lane" {
				obstacles = append(obstacles, obstacle{t.Text.Box, t.Key, w.GroupNodeID})
			}
		}
	}
	for _, r := range rep.Edges {
		if r.Label != nil {
			texts = append(texts, *r.Label)
			textOwners = append(textOwners, geo.Edges[r.Ordinal].Source)
		}
	}
	for i, e := range rep.GroupElements {
		w := geo.GroupElements[i]
		if !boxes[layout.GroupElementID(w.GroupNodeID, "container", "")].Inflate(boxTolerancePx).Contains(e.Box) {
			f.block(layout.CriterionNodeOverlap, []string{w.GroupNodeID}, nil, "element %s escapes actual container", e.ID)
		}
	}
	for _, lane := range geo.GroupLanes {
		box, ok := boxes[layout.GroupElementID(lane.GroupNodeID, "lane", lane.TaskKey)]
		if !ok {
			box = boxes[layout.GroupElementID(lane.GroupNodeID, "task", lane.TaskKey)]
		}
		for _, id := range lane.NodeIDs {
			body := boxes[layout.GroupElementID(id, "header", "")]
			if container, ok := boxes[layout.GroupElementID(id, "container", "")]; ok {
				body = container
			}
			if !box.Inflate(boxTolerancePx).Contains(body) {
				f.block(layout.CriterionNodeOverlap, []string{lane.GroupNodeID}, nil, "body %s escapes actual lane %s", id, lane.TaskKey)
			}
		}
	}
	// Containment outlines are not solid obstacles. Their captions are; the header
	// is represented once by its real node, with supplements checked as text.
	for i, a := range obstacles {
		for _, b := range obstacles[i+1:] {
			if a.box.Inflate(geo.Contract.MinClearancePx / 2).Overlaps(b.box.Inflate(geo.Contract.MinClearancePx / 2)) {
				f.block(layout.CriterionNodeOverlap, []string{a.owner, b.owner}, nil, "%s overlaps %s", a.id, b.id)
			}
		}
	}
	for i, a := range texts {
		for j := i + 1; j < len(texts); j++ {
			if glyphOverlap(a, texts[j]) {
				f.block(layout.CriterionTextCollision, []string{textOwners[i], textOwners[j]}, nil, "group text glyphs overlap")
			}
		}
	}
	for i, e := range rep.GroupConnectors {
		w := geo.GroupConnectors[i]
		ids := []string{w.GroupNodeID}
		expected := expectedRouteSegments(geo, w.Points, w.ID, geo.Contract.GroupConnectorDash)
		match := len(expected) > 0 && len(e.Segments) == len(expected)
		for j, s := range e.Segments {
			if j >= len(expected) || !pointNear(s.Start, expected[j].Start, .05) || !pointNear(s.End, expected[j].End, .05) {
				match = false
			}
		}
		if !match || !e.Visible || !sameColor(e.StrokeColor, geo.Contract.ForwardColor) || !near(e.StrokePx, geo.Contract.StrokePx, strokeTolerancePx) || !dashEqual(e.DashPattern, geo.Contract.GroupConnectorDash) {
			f.block(layout.CriterionRenderMismatch, ids, nil, "connector %s actual path/style differs", w.ID)
		}
		if !pointNear(e.Start, w.Points[0], endpointTolerancePx) || !onBorder(e.Start, w.SourceAttach.Side, boxes[w.SourceElementID]) || !pointNear(e.End, w.Points[len(w.Points)-1], endpointTolerancePx) || !onBorder(e.End, w.TargetAttach.Side, boxes[w.TargetElementID]) {
			f.block(layout.CriterionAmbiguousAttachment, ids, nil, "connector %s attachment differs", w.ID)
		}
		arrow := layout.RenderedEdge{HasArrow: e.HasArrow, ArrowPoints: e.ArrowPoints}
		if !arrowMatches(arrow, layout.EdgeRoute{Points: w.Points}, geo.Contract.ArrowPx) {
			f.block(layout.CriterionArrowDirection, ids, nil, "connector %s arrow is missing/reversed/detached", w.ID)
		}
		for _, s := range e.Segments {
			for _, o := range obstacles {
				if segmentHitsRect(s, o.box) {
					f.block(layout.CriterionEdgeThroughNode, ids, nil, "connector %s crosses %s", w.ID, o.id)
				}
			}
			for _, t := range texts {
				for _, g := range t.Glyphs {
					if segmentHitsRect(s, g) {
						f.block(layout.CriterionTextCollision, ids, nil, "connector %s crosses text", w.ID)
					}
				}
			}
			for _, r := range rep.Edges {
				if r.Label != nil && segmentHitsRect(s, r.Label.Box) {
					f.block(layout.CriterionTextCollision, ids, nil, "connector %s crosses branch text", w.ID)
				}
			}
		}
		for _, r := range rep.Edges {
			if routesOverlap(e.Segments, r.Segments) {
				f.block(layout.CriterionMergedLines, ids, []int{r.Ordinal}, "connector %s conceals real route", w.ID)
			}
		}
		for _, r := range rep.GroupConnectors[i+1:] {
			if routesOverlap(e.Segments, r.Segments) {
				f.block(layout.CriterionMergedLines, ids, nil, "connectors %s / %s conceal routes", w.ID, r.ID)
			}
		}
	}
	// Real paths also avoid supplemental text and synthetic solid cards/footers.
	for _, r := range rep.Edges {
		for _, s := range r.Segments {
			for i, e := range rep.GroupElements {
				w := geo.GroupElements[i]
				if (w.Role == "footer" || w.Role == "task") && segmentHitsRect(s, e.Box) {
					f.block(layout.CriterionEdgeThroughNode, []string{w.GroupNodeID}, []int{r.Ordinal}, "edge crosses %s", e.ID)
				}
				for _, t := range e.Texts {
					for _, g := range t.Text.Glyphs {
						if segmentHitsRect(s, g) {
							f.block(layout.CriterionTextCollision, []string{w.GroupNodeID}, []int{r.Ordinal}, "edge crosses %s", t.Key)
						}
					}
				}
			}
		}
	}
}
