package layouttest

import (
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

func copyRetained(r workflow.RetainedLayout) workflow.RetainedLayout {
	if r.Positions != nil {
		p := make(map[string]workflow.NodePosition, len(r.Positions))
		for k, v := range r.Positions {
			p[k] = v
		}
		r.Positions = p
	}
	return r
}
func copyRequest(r workflow.ArrangementRequest) workflow.ArrangementRequest {
	r.Manifest = slices.Clone(r.Manifest)
	r.Retained = copyRetained(r.Retained)
	return r
}
func copyFindings(fs []layout.Finding) []layout.Finding {
	out := slices.Clone(fs)
	for i := range out {
		out[i].NodeIDs = slices.Clone(out[i].NodeIDs)
		out[i].EdgeOrdinals = slices.Clone(out[i].EdgeOrdinals)
	}
	return out
}
func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}
func copyRecord(r layout.Record) layout.Record {
	r.Manifest = slices.Clone(r.Manifest)
	r.Retained = copyRetained(r.Retained)
	r.Findings = copyFindings(r.Findings)
	r.LeaseExpiresAt = copyTime(r.LeaseExpiresAt)
	r.StartedAt = copyTime(r.StartedAt)
	r.FinishedAt = copyTime(r.FinishedAt)
	return r
}
func copyGeometry(g layout.Geometry) layout.Geometry {
	g.Nodes = slices.Clone(g.Nodes)
	g.Crossings = slices.Clone(g.Crossings)
	g.GroupCrossings = slices.Clone(g.GroupCrossings)
	g.GroupElements = slices.Clone(g.GroupElements)
	for i := range g.GroupElements {
		e := &g.GroupElements[i]
		e.Texts = slices.Clone(e.Texts)
		if e.OverlayBox != nil {
			box := *e.OverlayBox
			e.OverlayBox = &box
		}
	}
	g.GroupLanes = slices.Clone(g.GroupLanes)
	for i := range g.GroupLanes {
		g.GroupLanes[i].NodeIDs = slices.Clone(g.GroupLanes[i].NodeIDs)
	}
	g.GroupConnectors = slices.Clone(g.GroupConnectors)
	for i := range g.GroupConnectors {
		g.GroupConnectors[i].Points = slices.Clone(g.GroupConnectors[i].Points)
	}
	g.Edges = slices.Clone(g.Edges)
	for i := range g.Edges {
		e := &g.Edges[i]
		e.Points = slices.Clone(e.Points)
		if e.Text != nil {
			label := *e.Text
			e.Text = &label
		}
	}
	return g
}
func copyResult(r layout.StoredResult) layout.StoredResult {
	r.Geometry = copyGeometry(r.Geometry)
	r.Preflight = copyFindings(r.Preflight)
	if r.Review != nil {
		review := *r.Review
		review.Findings = copyFindings(review.Findings)
		review.Evidence.Artifacts = slices.Clone(review.Evidence.Artifacts)
		r.Review = &review
	}
	return r
}
func copyGroups(g *workflow.GroupGraph) (*workflow.GroupGraph, error) {
	if g == nil {
		return nil, nil
	}
	out := &workflow.GroupGraph{Order: slices.Clone(g.Order), Owner: maps.Clone(g.Owner), Groups: map[string]*workflow.GroupScope{}}
	for id, s := range g.Groups {
		if s == nil {
			return nil, errors.New("unit render: nil group scope")
		}
		v := *s
		var err error
		v.Config, err = workflow.ParseGroupConfig(workflow.NodeConfig{Group: &s.Config})
		if err != nil {
			return nil, err
		}
		v.Bodies = map[string][]string{}
		for key, body := range s.Bodies {
			v.Bodies[key] = slices.Clone(body)
		}
		v.Nested = slices.Clone(s.Nested)
		if s.Parent != nil {
			p := *s.Parent
			v.Parent = &p
		}
		out.Groups[id] = &v
	}
	return out, nil
}

func copyCache(e layout.CacheEntry) layout.CacheEntry {
	e.Geometry = copyGeometry(e.Geometry)
	return e
}
