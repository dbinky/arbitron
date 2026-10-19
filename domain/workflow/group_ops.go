package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// GroupUpsert is the complete author-supplied definition of ONE parallel_group
// node: its settings, ordered tasks (with inputs) and continuation targets.
// Body nodes and edges INTO the group are authored with workflow_create /
// workflow_update; UpsertGroup owns only the group node and its outgoing edges.
type GroupUpsert struct {
	NodeID        string      `json:"nodeId"`
	Label         string      `json:"label,omitempty"`
	Description   string      `json:"description,omitempty"`
	Entrypoint    *bool       `json:"entrypoint,omitempty"` // nil => keep existing (false for new)
	Config        GroupConfig `json:"config"`
	SuccessTarget string      `json:"successTarget"`
	FailureTarget string      `json:"failureTarget"`
}

// GroupView is the read model of one parallel_group node.
type GroupView struct {
	NodeID        string              `json:"nodeId"`
	Label         string              `json:"label,omitempty"`
	Entrypoint    bool                `json:"entrypoint"`
	Config        GroupConfig         `json:"config"` // tasks in author order
	SuccessTarget string              `json:"successTarget"`
	FailureTarget string              `json:"failureTarget"`
	Bodies        map[string][]string `json:"bodies"`
	Nested        []string            `json:"nested"`
	Parent        *BodyOwner          `json:"parent,omitempty"`
	Advisories    []ValidationError   `json:"advisories,omitempty"`
}

// GroupDeleteResult reports what a DeleteGroup left behind.
type GroupDeleteResult struct {
	NodeID        string   `json:"nodeId"`
	RetainedNodes []string `json:"retainedNodes"` // former body nodes + continuation targets left in the manifest, for reconnection/removal
}

// ErrGroupNotFound: the workflow exists and is visible, but has no
// parallel_group node with the requested id.
var ErrGroupNotFound = errors.New("parallel group not found")

// ErrGroupUpsertInvalid: the upsert ARGUMENTS cannot produce a well-formed
// group (missing/colliding node id, missing/unknown/self continuation target).
// These are rejected here because Validate would only report them with
// non-group codes, which the group write gate does not block on.
var ErrGroupUpsertInvalid = errors.New("invalid parallel group upsert")

// UpsertGroup creates or revises one parallel_group node. It loads the
// caller-visible workflow, rewrites the node and ALL its outgoing edges in one
// in-memory replacement (task edges in author order, then success, then
// failure), and persists through Update — so authorization (private first,
// admin shared-in-place) and group validation apply, and a rejected upsert
// persists nothing.
func (s *Service) UpsertGroup(ctx context.Context, workflowName string, up GroupUpsert) (*GroupView, error) {
	loaded, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	wf, err := cloneWorkflow(loaded)
	if err != nil {
		return nil, err
	}
	wf.Name = workflowName
	if err := applyGroupUpsert(wf, up); err != nil {
		return nil, err
	}
	findings, err := s.updateWithGroupFindings(ctx, workflowName, wf)
	if err != nil {
		return nil, err
	}
	graph, _ := AnalyzeGroups(wf)
	view, _ := buildGroupView(wf, graph, up.NodeID, findings)
	return view, nil
}

// GetGroup returns the view of one group. It is a pure read: it never writes
// the folder or the registry.
func (s *Service) GetGroup(ctx context.Context, workflowName, nodeID string) (*GroupView, error) {
	wf, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	if !isGroupNode(wf, nodeID) {
		return nil, fmt.Errorf("%w: %q in workflow %q", ErrGroupNotFound, nodeID, workflowName)
	}
	graph, _ := AnalyzeGroups(wf)
	view, _ := buildGroupView(wf, graph, nodeID, s.ValidateGroups(ctx, wf))
	return view, nil
}

// ListGroups returns every group of the workflow in definition (wf.Nodes)
// order — the same order as GroupGraph.Order. A group-free workflow yields an
// empty, non-nil slice. Pure read.
func (s *Service) ListGroups(ctx context.Context, workflowName string) ([]GroupView, error) {
	wf, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	out := []GroupView{}
	if !HasGroups(wf) {
		return out, nil
	}
	graph, _ := AnalyzeGroups(wf)
	findings := s.ValidateGroups(ctx, wf)
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeParallelGroup {
			continue
		}
		if v, ok := buildGroupView(wf, graph, n.ID, findings); ok {
			out = append(out, *v)
		}
	}
	return out, nil
}

// DeleteGroup removes the group node and every edge incident to it, then
// persists through Update. When the remaining workflow still contains groups
// and any of them is now invalid (e.g. it lost a continuation), the write is
// rejected with *GroupValidationError and nothing is persisted.
func (s *Service) DeleteGroup(ctx context.Context, workflowName, nodeID string) (*GroupDeleteResult, error) {
	loaded, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	if !isGroupNode(loaded, nodeID) {
		return nil, fmt.Errorf("%w: %q in workflow %q", ErrGroupNotFound, nodeID, workflowName)
	}
	wf, err := cloneWorkflow(loaded)
	if err != nil {
		return nil, err
	}
	wf.Name = workflowName

	graph, _ := AnalyzeGroups(wf)
	retained := retainedAfterDelete(wf, graph, nodeID)

	nodes := make([]Node, 0, len(wf.Nodes))
	for _, n := range wf.Nodes {
		if n.ID != nodeID {
			nodes = append(nodes, n)
		}
	}
	wf.Nodes = nodes
	edges := make([]Edge, 0, len(wf.Edges))
	for _, e := range wf.Edges {
		if e.Source != nodeID && e.Target != nodeID {
			edges = append(edges, e)
		}
	}
	wf.Edges = edges

	if err := s.Update(ctx, workflowName, wf); err != nil {
		return nil, err
	}
	return &GroupDeleteResult{NodeID: nodeID, RetainedNodes: retained}, nil
}

// applyGroupUpsert mutates wf in memory. It never touches storage.
func applyGroupUpsert(wf *Workflow, up GroupUpsert) error {
	if up.NodeID == "" {
		return fmt.Errorf("%w: nodeId is required", ErrGroupUpsertInvalid)
	}
	idx := -1
	for i, n := range wf.Nodes {
		if n.ID == up.NodeID {
			idx = i
			break
		}
	}
	if idx >= 0 && wf.Nodes[idx].Type != NodeTypeParallelGroup {
		return fmt.Errorf("%w: node %q already exists as a %s node; choose another nodeId or remove that node with workflow_update",
			ErrGroupUpsertInvalid, up.NodeID, wf.Nodes[idx].Type)
	}
	if err := checkContinuationTarget(wf, up.NodeID, "successTarget", up.SuccessTarget); err != nil {
		return err
	}
	if err := checkContinuationTarget(wf, up.NodeID, "failureTarget", up.FailureTarget); err != nil {
		return err
	}
	cfg, err := copyGroupConfig(up.Config)
	if err != nil {
		return err
	}

	node := Node{ID: up.NodeID}
	if idx >= 0 {
		node = wf.Nodes[idx]
	}
	node.Type = NodeTypeParallelGroup
	node.Config = NodeConfig{Group: &cfg}
	if up.Label != "" {
		node.Label = up.Label
	}
	if up.Description != "" {
		node.Description = up.Description
	}
	if up.Entrypoint != nil {
		node.Entrypoint = *up.Entrypoint
		if *up.Entrypoint {
			for i := range wf.Nodes {
				if wf.Nodes[i].ID != up.NodeID {
					wf.Nodes[i].Entrypoint = false
				}
			}
		}
	}
	if idx >= 0 {
		wf.Nodes[idx] = node
	} else {
		wf.Nodes = append(wf.Nodes, node)
	}
	wf.Edges = rewriteGroupEdges(wf.Edges, up.NodeID, cfg, up.SuccessTarget, up.FailureTarget)
	return nil
}

func checkContinuationTarget(wf *Workflow, groupID, field, target string) error {
	switch {
	case target == "":
		return fmt.Errorf("%w: %s is required (the node the parent continues to)", ErrGroupUpsertInvalid, field)
	case target == groupID:
		return fmt.Errorf("%w: %s cannot be the group node %q itself", ErrGroupUpsertInvalid, field, groupID)
	}
	for _, n := range wf.Nodes {
		if n.ID == target {
			return nil
		}
	}
	return fmt.Errorf("%w: %s %q is not a node in workflow %q; add the node with workflow_update first",
		ErrGroupUpsertInvalid, field, target, wf.Name)
}

// rewriteGroupEdges drops every edge leaving groupID and inserts the fresh set
// where the first dropped edge was (appended when the group had none), so a
// revision keeps the manifest's edge order stable.
func rewriteGroupEdges(edges []Edge, groupID string, cfg GroupConfig, success, failure string) []Edge {
	var fresh []Edge
	if cfg.Form == GroupFormBranches {
		for _, t := range cfg.Tasks {
			if t.Entry == "" {
				continue // validation reports the missing entry
			}
			fresh = append(fresh, Edge{Source: groupID, Target: t.Entry, Output: GroupPortTask, Label: t.Key})
		}
	}
	fresh = append(fresh,
		Edge{Source: groupID, Target: success, Output: GroupPortSuccess},
		Edge{Source: groupID, Target: failure, Output: GroupPortFailure},
	)
	out := make([]Edge, 0, len(edges)+len(fresh))
	inserted := false
	for _, e := range edges {
		if e.Source == groupID {
			if !inserted {
				out = append(out, fresh...)
				inserted = true
			}
			continue
		}
		out = append(out, e)
	}
	if !inserted {
		out = append(out, fresh...)
	}
	return out
}

func isGroupNode(wf *Workflow, nodeID string) bool {
	if wf == nil {
		return false
	}
	for _, n := range wf.Nodes {
		if n.ID == nodeID {
			return n.Type == NodeTypeParallelGroup
		}
	}
	return false
}

// buildGroupView assembles the view from the ownership analysis, falling back
// to the raw node/edges when AnalyzeGroups could not build a scope (a malformed
// legacy definition) so inspection still works.
func buildGroupView(wf *Workflow, graph *GroupGraph, nodeID string, findings []ValidationError) (*GroupView, bool) {
	var node *Node
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == nodeID {
			node = &wf.Nodes[i]
			break
		}
	}
	if node == nil || node.Type != NodeTypeParallelGroup {
		return nil, false
	}
	v := &GroupView{
		NodeID:     nodeID,
		Label:      node.Label,
		Entrypoint: node.Entrypoint,
		Bodies:     map[string][]string{},
		Nested:     []string{},
	}
	var scope *GroupScope
	if graph != nil {
		scope = graph.Groups[nodeID]
	}
	if scope != nil {
		if cfg, err := copyGroupConfig(scope.Config); err == nil {
			v.Config = cfg
		}
		v.SuccessTarget, v.FailureTarget = scope.SuccessTarget, scope.FailureTarget
		for k, ids := range scope.Bodies {
			v.Bodies[k] = append([]string{}, ids...)
		}
		v.Nested = append(v.Nested, scope.Nested...)
		if scope.Parent != nil {
			p := *scope.Parent
			v.Parent = &p
		}
	} else {
		if node.Config.Group != nil {
			if cfg, err := copyGroupConfig(*node.Config.Group); err == nil {
				v.Config = cfg
			}
		}
		v.SuccessTarget, v.FailureTarget = continuationTargets(wf, nodeID)
	}
	for _, f := range findings {
		if f.NodeID == nodeID {
			v.Advisories = append(v.Advisories, f)
		}
	}
	return v, true
}

func continuationTargets(wf *Workflow, nodeID string) (success, failure string) {
	for _, e := range wf.Edges {
		if e.Source != nodeID {
			continue
		}
		switch e.Output {
		case GroupPortSuccess:
			if success == "" {
				success = e.Target
			}
		case GroupPortFailure:
			if failure == "" {
				failure = e.Target
			}
		}
	}
	return success, failure
}

// retainedAfterDelete lists the nodes a deleted group leaves behind: its body
// nodes in task order, then its success and failure targets; de-duplicated,
// never the group itself.
func retainedAfterDelete(wf *Workflow, graph *GroupGraph, nodeID string) []string {
	out := []string{}
	seen := map[string]bool{nodeID: true}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	var scope *GroupScope
	if graph != nil {
		scope = graph.Groups[nodeID]
	}
	if scope != nil {
		for _, t := range scope.Config.Tasks {
			for _, id := range scope.Bodies[t.Key] {
				add(id)
			}
		}
		add(scope.SuccessTarget)
		add(scope.FailureTarget)
		return out
	}
	for _, e := range wf.Edges {
		if e.Source == nodeID {
			add(e.Target)
		}
	}
	return out
}

// cloneWorkflow deep-copies a definition through its JSON form (the form it
// is persisted in), so in-memory mutation can never reach a repository-owned
// value before validation has passed.
func cloneWorkflow(wf *Workflow) (*Workflow, error) {
	if wf == nil {
		return nil, fmt.Errorf("%w: empty workflow", ErrNotFound)
	}
	b, err := json.Marshal(wf)
	if err != nil {
		return nil, fmt.Errorf("copying workflow %q: %w", wf.Name, err)
	}
	var out Workflow
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("copying workflow %q: %w", wf.Name, err)
	}
	return &out, nil
}

// copyGroupConfig deep-copies a GroupConfig (task Inputs included) so the
// stored node never aliases caller-owned maps.
func copyGroupConfig(cfg GroupConfig) (GroupConfig, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return GroupConfig{}, fmt.Errorf("%w: group config is not JSON-serializable: %v", ErrGroupUpsertInvalid, err)
	}
	var out GroupConfig
	if err := json.Unmarshal(b, &out); err != nil {
		return GroupConfig{}, fmt.Errorf("%w: group config is not JSON-serializable: %v", ErrGroupUpsertInvalid, err)
	}
	return out, nil
}
