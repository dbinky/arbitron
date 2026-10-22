package layout

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/arbitron/arbitron/domain/workflow"
)

// ValidateGeometry validates review geometry against the independently captured
// ownership. A review request is not a workflow manifest: it has no entrypoint,
// so re-running AnalyzeGroups on its presentation nodes would lose branch bodies.
func (r ReviewRequest) ValidateGeometry() error {
	g := Graph{index: map[string]int{}}
	for i, n := range r.Geometry.Nodes {
		if n.TypeCaption == "parallel group" && r.WorkflowGroups == nil {
			return fmt.Errorf("%w: missing captured ownership", ErrInvalidGroups)
		}
		if n.ID == "" {
			return ErrEmptyNodeID
		}
		if _, ok := g.index[n.ID]; ok {
			return ErrDuplicateNode
		}
		g.index[n.ID] = i
		g.Nodes = append(g.Nodes, GraphNode{ID: n.ID, Text: n.Text, TypeCaption: n.TypeCaption})
	}
	for _, e := range r.Geometry.Edges {
		if _, ok := g.index[e.Source]; !ok {
			return ErrDanglingEdge
		}
		if _, ok := g.index[e.Target]; !ok {
			return ErrDanglingEdge
		}
		text := e.Label
		if text == "" {
			text = e.Output
		}
		g.Edges = append(g.Edges, GraphEdge{Ordinal: e.Ordinal, Source: e.Source, Target: e.Target, Output: e.Output, Label: e.Label, Text: text})
	}
	if r.WorkflowGroups != nil {
		owner := map[string]workflow.BodyOwner{}
		var order []string
		for _, n := range g.Nodes {
			if n.TypeCaption != "parallel group" {
				continue
			}
			scope := r.WorkflowGroups.Groups[n.ID]
			if scope == nil || scope.NodeID != n.ID {
				return fmt.Errorf("%w: missing scope %q", ErrInvalidGroups, n.ID)
			}
			order = append(order, n.ID)
			for key, body := range scope.Bodies {
				for _, id := range body {
					if _, ok := g.index[id]; !ok {
						return ErrDanglingEdge
					}
					if _, dup := owner[id]; dup {
						return fmt.Errorf("%w: duplicate owner %q", ErrInvalidGroups, id)
					}
					owner[id] = workflow.BodyOwner{GroupNodeID: n.ID, TaskKey: key}
				}
			}
		}
		if !reflect.DeepEqual(owner, r.WorkflowGroups.Owner) || !reflect.DeepEqual(order, r.WorkflowGroups.Order) || len(order) != len(r.WorkflowGroups.Groups) {
			return fmt.Errorf("%w: captured ownership differs from bodies/headers", ErrInvalidGroups)
		}
		for _, id := range order {
			scope := r.WorkflowGroups.Groups[id]
			parent, owned := owner[id]
			if owned != (scope.Parent != nil) || owned && *scope.Parent != parent {
				return fmt.Errorf("%w: parent differs for %q", ErrInvalidGroups, id)
			}
			var nested []string
			for _, child := range order {
				if p, ok := owner[child]; ok && p.GroupNodeID == id {
					nested = append(nested, child)
				}
			}
			if !slices.Equal(nested, scope.Nested) {
				return fmt.Errorf("%w: nested ownership differs for %q", ErrInvalidGroups, id)
			}
			success, failure := "", ""
			for _, e := range g.Edges {
				if e.Source == id {
					if e.Output == workflow.GroupPortSuccess {
						success = e.Target
					}
					if e.Output == workflow.GroupPortFailure {
						failure = e.Target
					}
				}
			}
			if scope.SuccessTarget != success || scope.FailureTarget != failure {
				return fmt.Errorf("%w: continuation differs for %q", ErrInvalidGroups, id)
			}
		}
		var err error
		g.Groups, err = cloneGroupGraph(r.WorkflowGroups)
		if err != nil {
			return err
		}
		g.capturedGroups = r.WorkflowGroups
	}
	return r.Geometry.Validate(g)
}
