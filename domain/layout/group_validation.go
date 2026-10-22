package layout

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func validateGroupGeometry(g Graph, geo Geometry) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: group geometry: %s", ErrInvalidGeometry, fmt.Sprintf(format, args...))
	}
	if g.Groups == nil {
		if len(geo.GroupElements)+len(geo.GroupLanes)+len(geo.GroupConnectors)+len(geo.GroupCrossings) > 0 {
			return bad("unexpected group arrays")
		}
		return nil
	}
	elements := map[string]GroupElement{}
	for _, e := range geo.GroupElements {
		if _, dup := elements[e.ID]; dup {
			return bad("duplicate element %s", e.ID)
		}
		elements[e.ID] = e
	}
	wantTexts := map[string][]TextItem{}
	for _, item := range groupTextItems(g, geo.Contract) {
		var key []string
		if err := json.Unmarshal([]byte(item.Key), &key); err != nil {
			return bad("invalid text key")
		}
		if !strings.HasPrefix(key[2], "overlay:") && key[2] != "redactedTitle" {
			wantTexts[key[1]] = append(wantTexts[key[1]], item)
		}
	}
	expectedElements := 0
	expectedLanes := []GroupLane{}
	for _, gid := range g.Groups.Order {
		scope := g.Groups.Groups[gid]
		roles := [][2]string{{"container", ""}, {"header", ""}, {"footer", ""}}
		for _, task := range scope.Config.Tasks {
			role := "lane"
			if scope.Config.Form == "workflows" {
				role = "task"
			}
			roles = append(roles, [2]string{role, task.Key})
			expectedLanes = append(expectedLanes, GroupLane{GroupNodeID: gid, TaskKey: task.Key, NodeIDs: scope.Bodies[task.Key]})
		}
		container, ok := elements[GroupElementID(gid, "container", "")]
		if !ok {
			return bad("missing container %s", gid)
		}
		footer := elements[GroupElementID(gid, "footer", "")]
		for _, role := range roles {
			expectedElements++
			id := GroupElementID(gid, role[0], role[1])
			e, ok := elements[id]
			if !ok || e.GroupNodeID != gid || e.TaskKey != role[1] || e.Role != role[0] {
				return bad("missing/mis-owned element %s", id)
			}
			if !positiveRect(e.Box) || !geo.Canvas.Contains(e.Box) || !container.Box.Contains(e.Box) {
				return bad("element %s box", id)
			}
			if len(e.Texts) != len(wantTexts[id]) {
				return bad("element %s text coverage", id)
			}
			for i, t := range e.Texts {
				want := wantTexts[id][i]
				if t.Key != want.Key || t.Text != want.Text || t.Style != want.Style || t.Lines < 1 || !positiveRect(t.Box) || !e.Box.Contains(t.Box) {
					return bad("element %s text %s", id, t.Key)
				}
			}
			overlay := e.Role == "footer" || e.Role == "lane" || e.Role == "task"
			if overlay != (e.OverlayBox != nil) || overlay && (!positiveRect(*e.OverlayBox) || !e.Box.Contains(*e.OverlayBox)) {
				return bad("element %s live reservation", id)
			}
			if e.OverlayBox != nil {
				for _, t := range e.Texts {
					if t.Box.Overlaps(*e.OverlayBox) {
						return bad("element %s text/live overlap", id)
					}
				}
			}
			if e.Role == "header" {
				found := false
				for _, n := range geo.Nodes {
					if n.ID == gid {
						found = n.Box == e.Box
					}
				}
				if !found {
					return bad("header %s must share real NodeBox", gid)
				}
			}
		}
		header := elements[GroupElementID(gid, "header", "")]
		previousRight := -1.0
		for _, l := range geo.GroupLanes {
			if l.GroupNodeID != gid {
				continue
			}
			if l.Box.Y <= header.Box.Bottom() || l.Box.Bottom() >= footer.Box.Y || l.Box.X < previousRight {
				return bad("lane %s order/header/footer", l.TaskKey)
			}
			previousRight = l.Box.Right()
		}
	}
	if len(elements) != expectedElements || len(geo.GroupLanes) != len(expectedLanes) {
		return bad("element/lane coverage")
	}
	// Layout recursion may emit nested lanes before later outer lanes; compare
	// each group's subsequence to author task order, and require exact ownership.
	seen := map[string]bool{}
	for _, gid := range g.Groups.Order {
		scope := g.Groups.Groups[gid]
		index := 0
		for _, l := range geo.GroupLanes {
			if l.GroupNodeID != gid {
				continue
			}
			if l.NodeIDs == nil || index >= len(scope.Config.Tasks) || l.TaskKey != scope.Config.Tasks[index].Key || !slices.Equal(l.NodeIDs, scope.Bodies[l.TaskKey]) {
				return bad("lane %s ownership/order", l.TaskKey)
			}
			index++
			id := groupIDJSON(gid, l.TaskKey)
			if seen[id] {
				return bad("duplicate lane %s", id)
			}
			seen[id] = true
			container := elements[GroupElementID(gid, "container", "")]
			if !positiveRect(l.Box) || !container.Box.Contains(l.Box) {
				return bad("lane %s box", id)
			}
			role := "lane"
			if scope.Config.Form == "workflows" {
				role = "task"
			}
			e := elements[GroupElementID(gid, role, l.TaskKey)]
			if !l.Box.Contains(e.Box) {
				return bad("task %s outside lane", id)
			}
			for _, node := range l.NodeIDs {
				box := groupElementBoxes(geo)[GroupElementID(node, "header", "")]
				if g.Groups.Groups[node] != nil {
					box = elements[GroupElementID(node, "container", "")].Box
				}
				if !l.Box.Contains(box) {
					return bad("body %s outside owning lane %s", node, id)
				}
			}
		}
		if index != len(scope.Config.Tasks) {
			return bad("lane coverage %s", gid)
		}
	}
	for _, l := range geo.GroupLanes {
		if g.Groups.Groups[l.GroupNodeID] == nil {
			return bad("unknown lane owner")
		}
	}
	expected := expectedGroupConnectors(g)
	if len(expected) != len(geo.GroupConnectors) {
		return bad("connector coverage")
	}
	boxes := groupElementBoxes(geo)
	for i, e := range geo.GroupConnectors {
		want := expected[i]
		if e.ID != want.ID || e.GroupNodeID != want.GroupNodeID || e.TaskKey != want.TaskKey || e.Kind != want.Kind || e.SourceElementID != want.SourceElementID || e.TargetElementID != want.TargetElementID {
			return bad("connector %s identity/order", e.ID)
		}
		if e.SourceAttach.NodeID != "" || e.TargetAttach.NodeID != "" || e.SourceAttach.ElementID != e.SourceElementID || e.TargetAttach.ElementID != e.TargetElementID {
			return bad("connector %s attachments", e.ID)
		}
		fake := EdgeRoute{Source: e.SourceElementID, Target: e.TargetElementID, Kind: EdgeForward, Points: e.Points, SourceAttach: e.SourceAttach, TargetAttach: e.TargetAttach}
		fake.SourceAttach.NodeID = fake.Source
		fake.TargetAttach.NodeID = fake.Target
		if err := validateRoute(fake, boxes, geo.Canvas); err != nil {
			return bad("connector %s: %v", e.ID, err)
		}
	}
	actual := findGroupCrossings(geo)
	if !slices.Equal(actual, geo.GroupCrossings) {
		return bad("mixed/connector crossing coverage")
	}
	return nil
}
