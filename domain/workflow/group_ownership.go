package workflow

import (
	"sort"
	"strconv"
	"strings"
)

// BodyOwner identifies one task body: the (group node, task key) pair.
type BodyOwner struct {
	GroupNodeID string `json:"groupNodeId"`
	TaskKey     string `json:"taskKey"`
}

// GroupScope is the analysed structure of one parallel_group node.
type GroupScope struct {
	NodeID string      `json:"nodeId"`
	Config GroupConfig `json:"config"`
	// Parent is the task body that contains this group node; nil when the
	// group sits on the main path (or is unreachable from the entrypoint).
	Parent *BodyOwner `json:"parent,omitempty"`
	// Bodies maps task key -> the node ids exclusively owned by that task's
	// body, in wf.Nodes order (branches form only; empty for workflows form).
	Bodies map[string][]string `json:"bodies"`
	// Nested lists the group node ids whose Parent.GroupNodeID == NodeID, in
	// wf.Nodes order.
	Nested        []string `json:"nested"`
	SuccessTarget string   `json:"successTarget"`
	FailureTarget string   `json:"failureTarget"`
}

// GroupGraph is the ownership analysis of every parallel_group in a workflow.
// It is the ONE structural model shared by validation, the MCP group view, the
// Phase 2 branch launcher and the Phase 4 layout.
type GroupGraph struct {
	Groups map[string]*GroupScope `json:"groups"`
	// Owner maps a body node id to its IMMEDIATE owning (group, task). Nested
	// group nodes are owned by their enclosing task. Main-path nodes are absent.
	Owner map[string]BodyOwner `json:"owner"`
	// Order lists every group node id in wf.Nodes order.
	Order []string `json:"order"`
}

// HasGroups reports whether wf contains any parallel_group node.
func HasGroups(wf *Workflow) bool {
	if wf == nil {
		return false
	}
	for _, n := range wf.Nodes {
		if n.Type == NodeTypeParallelGroup {
			return true
		}
	}
	return false
}

// AnalyzeGroups computes the exclusive ownership of every branch body and
// checks the group graph rules: ports, task edges, body ownership and the
// no-group-on-a-cycle rule. It reports ONLY graph findings; config findings
// (ParseGroupConfig sentinels) are mapped by Validate. It never panics on a
// malformed graph and always returns a non-nil graph.
//
// # Ownership algorithm
//
// The workflow is partitioned into REGIONS: the main path, and one body per
// branches-form task. A region grows by BFS from its seed (the entrypoint for
// the main path, the task's entry for a body) along CONTINUATION edges only:
// every edge of an ordinary node, and the success/failure edges of a group
// node. A nested group is atomic — its task edges are never followed while
// growing the region that contains it; its bodies become regions of their own.
//
// Regions are grown in waves: the main path first, then the bodies of groups
// on the main path, then the bodies of groups inside those bodies, and so on.
// Growth STOPS (recording a "crossing" edge) at a node already claimed by an
// earlier wave and at the entry of any other body. Regions within one wave
// grow independently; a node reached by two of them is an overlap.
//
// Why not define a body as "everything reachable from its entry"? A body edge
// back to the group's success target (or anywhere after it) would make the
// main-path continuation reachable from the entry, so naive reachability would
// silently absorb the escaped main-path node — and everything after it — into
// the body, and the launcher would run it inside the task. Growing the
// enclosing region FIRST and stopping at its claims keeps the continuation on
// the main path and turns the stray edge into a GROUP_BODY_ESCAPE finding.
//
// Crossings are classified once the region tree is known:
//   - into an enclosing group node (the body loops back into its own group or
//     an ancestor group): GROUP_BODY_OVERLAP;
//   - from a body to a region that is not inside it (the enclosing path, a
//     continuation, a sibling or an unrelated body): GROUP_BODY_ESCAPE;
//   - otherwise (from the main path, or from a body into a body nested inside
//     it, bypassing the owning group's task edge): GROUP_BODY_EXTERNAL_EDGE.
func AnalyzeGroups(wf *Workflow) (*GroupGraph, []ValidationError) {
	graph := &GroupGraph{
		Groups: map[string]*GroupScope{},
		Owner:  map[string]BodyOwner{},
		Order:  []string{},
	}
	if wf == nil {
		return graph, nil
	}
	a := newGroupAnalysis(wf, graph)
	a.collectGroups()
	if len(graph.Order) == 0 {
		return graph, nil
	}
	a.checkPortsAndTaskEdges()
	a.buildAnchors()
	a.growRegions()
	a.classifyCrossings()
	a.linkNested()
	a.checkCycles()
	return graph, a.errs
}

// regionID names one region: the main path is the zero value; a body is
// (group node id, task key).
type regionID struct {
	group string
	task  string
}

func (r regionID) isMain() bool { return r.group == "" }

type flowEdge struct {
	target string
	idx    int
}

type crossing struct {
	from    regionID
	src     string
	dst     string
	edgeIdx int
}

type groupAnalysis struct {
	wf    *Workflow
	graph *GroupGraph

	nodeIndex map[string]int         // node id -> index of its FIRST occurrence in wf.Nodes
	flow      map[string][]flowEdge  // continuation adjacency (task edges of groups excluded)
	tasks     map[string][]GroupTask // group id -> analysable branch tasks
	anchors   map[string]regionID    // body entry node id -> the body it seeds
	skipBody  map[regionID]bool      // bodies that must not be grown (bad entry)
	claimed   map[string]regionID    // node id -> owning region
	parentOf  map[regionID]regionID  // body -> region containing its group node
	crossings []crossing
	errs      []ValidationError
}

func newGroupAnalysis(wf *Workflow, graph *GroupGraph) *groupAnalysis {
	a := &groupAnalysis{
		wf:        wf,
		graph:     graph,
		nodeIndex: make(map[string]int, len(wf.Nodes)),
		flow:      map[string][]flowEdge{},
		tasks:     map[string][]GroupTask{},
		anchors:   map[string]regionID{},
		skipBody:  map[regionID]bool{},
		claimed:   map[string]regionID{},
		parentOf:  map[regionID]regionID{},
	}
	for i, n := range wf.Nodes {
		if _, dup := a.nodeIndex[n.ID]; !dup {
			a.nodeIndex[n.ID] = i
		}
	}
	for i, e := range wf.Edges {
		if _, ok := a.nodeIndex[e.Source]; !ok {
			continue
		}
		if _, ok := a.nodeIndex[e.Target]; !ok {
			continue
		}
		if a.isGroup(e.Source) && e.Output != GroupPortSuccess && e.Output != GroupPortFailure {
			continue // task edges (and invalid extra ports) never extend a region
		}
		a.flow[e.Source] = append(a.flow[e.Source], flowEdge{target: e.Target, idx: i})
	}
	return a
}

func (a *groupAnalysis) isGroup(id string) bool {
	i, ok := a.nodeIndex[id]
	return ok && a.wf.Nodes[i].Type == NodeTypeParallelGroup
}

// analysisConfig returns the config to analyse: the parsed config when valid,
// otherwise a deep copy of the raw config so a partially invalid group is
// still analysed (its config problems are reported separately by Validate).
func analysisConfig(n Node) GroupConfig {
	if cfg, err := ParseGroupConfig(n.Config); err == nil {
		return cfg
	}
	if n.Config.Group == nil {
		return GroupConfig{}
	}
	return cloneGroupConfig(*n.Config.Group)
}

// collectGroups creates one scope per group node (first occurrence of an id)
// and selects its analysable branch tasks: nonempty key and entry, first
// occurrence of each key, entry naming an existing node. Shared entries are
// rejected by buildAnchors.
func (a *groupAnalysis) collectGroups() {
	for i, n := range a.wf.Nodes {
		if n.Type != NodeTypeParallelGroup || a.nodeIndex[n.ID] != i {
			continue
		}
		scope := &GroupScope{
			NodeID: n.ID,
			Config: analysisConfig(n),
			Bodies: map[string][]string{},
			Nested: []string{},
		}
		a.graph.Groups[n.ID] = scope
		a.graph.Order = append(a.graph.Order, n.ID)

		if scope.Config.Form != GroupFormBranches {
			continue
		}
		seenKey := map[string]bool{}
		for _, t := range scope.Config.Tasks {
			if t.Key == "" || t.Entry == "" || seenKey[t.Key] {
				continue
			}
			seenKey[t.Key] = true
			if _, exists := a.nodeIndex[t.Entry]; !exists {
				a.errs = append(a.errs, ValidationError{
					Code: "GROUP_UNKNOWN_ENTRY",
					Message: "parallel_group " + n.ID + " task " + strconv.Quote(t.Key) + " names entry " +
						strconv.Quote(t.Entry) + ", which is not a node in this workflow; add the node or fix the entry",
					NodeID:  n.ID,
					TaskKey: t.Key,
				})
				continue
			}
			a.tasks[n.ID] = append(a.tasks[n.ID], t)
		}
	}
}

// checkPortsAndTaskEdges enforces: exactly one success and one failure edge;
// no other outgoing port; for branches form exactly one "task" edge per task,
// matched by its unique entry target; for workflows form no task edges.
func (a *groupAnalysis) checkPortsAndTaskEdges() {
	for _, id := range a.graph.Order {
		scope := a.graph.Groups[id]
		var success, failure []int
		taskEdges := map[string][]int{} // target -> edge indexes
		var taskOrder []string
		for i, e := range a.wf.Edges {
			if e.Source != id {
				continue
			}
			switch e.Output {
			case GroupPortSuccess:
				success = append(success, i)
			case GroupPortFailure:
				failure = append(failure, i)
			case GroupPortTask:
				if _, seen := taskEdges[e.Target]; !seen {
					taskOrder = append(taskOrder, e.Target)
				}
				taskEdges[e.Target] = append(taskEdges[e.Target], i)
			default:
				a.errs = append(a.errs, ValidationError{
					Code: "GROUP_EXTRA_PORT",
					Message: "parallel_group " + id + " has an edge to " + e.Target + " on port " + strconv.Quote(e.Output) +
						"; a group only has \"task\", \"success\" and \"failure\" ports",
					NodeID:  id,
					EdgeIdx: i,
				})
			}
		}
		for _, p := range []struct {
			port  string
			edges []int
		}{{GroupPortSuccess, success}, {GroupPortFailure, failure}} {
			switch len(p.edges) {
			case 0:
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_MISSING_PORT",
					Message: "parallel_group " + id + " is missing its " + strconv.Quote(p.port) + " edge; connect it to the node that runs when the group ends in " + p.port,
					NodeID:  id,
				})
			case 1:
			default:
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_DUPLICATE_PORT",
					Message: "parallel_group " + id + " has " + strconv.Itoa(len(p.edges)) + " " + strconv.Quote(p.port) + " edges; keep exactly one",
					NodeID:  id,
					EdgeIdx: p.edges[1],
				})
			}
		}
		if len(success) > 0 {
			scope.SuccessTarget = a.wf.Edges[success[0]].Target
		}
		if len(failure) > 0 {
			scope.FailureTarget = a.wf.Edges[failure[0]].Target
		}

		switch scope.Config.Form {
		case GroupFormWorkflows:
			for _, tgt := range taskOrder {
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_TASK_EDGE_MISMATCH",
					Message: "parallel_group " + id + " (workflows form) has a \"task\" edge to " + tgt + "; workflow tasks run child workflows and take no task edges — remove it",
					NodeID:  id,
					EdgeIdx: taskEdges[tgt][0],
				})
			}
		case GroupFormBranches:
			declared := map[string]bool{}
			for _, t := range scope.Config.Tasks {
				if t.Entry != "" {
					declared[t.Entry] = true
				}
			}
			for _, t := range a.tasks[id] {
				switch n := len(taskEdges[t.Entry]); n {
				case 0:
					a.errs = append(a.errs, ValidationError{
						Code: "GROUP_TASK_EDGE_MISMATCH",
						Message: "parallel_group " + id + " task " + strconv.Quote(t.Key) + " has no \"task\" edge to its entry " +
							t.Entry + "; add {source: " + id + ", target: " + t.Entry + ", output: \"task\"}",
						NodeID:  id,
						TaskKey: t.Key,
					})
				case 1:
				default:
					a.errs = append(a.errs, ValidationError{
						Code:    "GROUP_TASK_EDGE_MISMATCH",
						Message: "parallel_group " + id + " task " + strconv.Quote(t.Key) + " has " + strconv.Itoa(n) + " \"task\" edges to " + t.Entry + "; keep exactly one",
						NodeID:  id,
						TaskKey: t.Key,
						EdgeIdx: taskEdges[t.Entry][1],
					})
				}
			}
			for _, tgt := range taskOrder {
				if declared[tgt] {
					continue
				}
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_TASK_EDGE_MISMATCH",
					Message: "parallel_group " + id + " has a \"task\" edge to " + tgt + ", which is not the entry of any of its tasks; remove the edge or add a task with entry " + tgt,
					NodeID:  id,
					EdgeIdx: taskEdges[tgt][0],
				})
			}
		}
	}
}

// entrypoint returns the first entrypoint node id, or "".
func (a *groupAnalysis) entrypoint() string {
	for _, n := range a.wf.Nodes {
		if n.Entrypoint {
			return n.ID
		}
	}
	return ""
}

// buildAnchors registers every analysable task entry as the seed of its body.
// An entry that is the workflow entrypoint, the task's own group node, or the
// entry of any earlier task cannot seed an exclusive body.
func (a *groupAnalysis) buildAnchors() {
	entry := a.entrypoint()
	for _, id := range a.graph.Order {
		for _, t := range a.tasks[id] {
			r := regionID{group: id, task: t.Key}
			var problem string
			switch {
			case t.Entry == id:
				problem = "is the group node itself; a body must start at a node inside the task"
			case t.Entry == entry:
				problem = "is the workflow entrypoint, which belongs to the main path"
			default:
				if other, taken := a.anchors[t.Entry]; taken {
					problem = "is already the entry of task " + strconv.Quote(other.task) + " of parallel_group " + other.group
				}
			}
			if problem != "" {
				a.skipBody[r] = true
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_BODY_OVERLAP",
					Message: "parallel_group " + id + " task " + strconv.Quote(t.Key) + " entry " + t.Entry + " " + problem,
					NodeID:  id,
					TaskKey: t.Key,
				})
				continue
			}
			a.anchors[t.Entry] = r
		}
	}
}

type pendingRegion struct {
	id   regionID
	seed string
}

// growRegions runs the wave algorithm described on AnalyzeGroups.
func (a *groupAnalysis) growRegions() {
	entry := a.entrypoint()
	if entry == "" {
		return
	}
	wave := []pendingRegion{{id: regionID{}, seed: entry}}
	for len(wave) > 0 {
		members := make([]map[string]bool, len(wave))
		for i, pr := range wave {
			members[i] = a.grow(pr.id, pr.seed)
		}

		var placedGroups []string
		placedIn := map[string]regionID{}
		for i, pr := range wave {
			var shared []string
			var sharedWith regionID
			for _, id := range a.inNodeOrder(members[i]) {
				if other, taken := a.claimed[id]; taken {
					if len(shared) == 0 {
						sharedWith = other
					}
					shared = append(shared, id)
					continue
				}
				a.claimed[id] = pr.id
				if !pr.id.isMain() {
					scope := a.graph.Groups[pr.id.group]
					scope.Bodies[pr.id.task] = append(scope.Bodies[pr.id.task], id)
					a.graph.Owner[id] = BodyOwner{GroupNodeID: pr.id.group, TaskKey: pr.id.task}
				}
				if _, isScope := a.graph.Groups[id]; isScope {
					placedGroups = append(placedGroups, id)
					placedIn[id] = pr.id
				}
			}
			if len(shared) > 0 {
				a.errs = append(a.errs, ValidationError{
					Code: "GROUP_BODY_OVERLAP",
					Message: "parallel_group " + pr.id.group + " task " + strconv.Quote(pr.id.task) + " shares node(s) " +
						strings.Join(shared, ", ") + " with " + a.describe(sharedWith) +
						"; each node may belong to exactly one task body — give each task its own nodes",
					NodeID:  pr.id.group,
					TaskKey: pr.id.task,
				})
			}
		}

		var next []pendingRegion
		for _, gid := range placedGroups {
			parent := placedIn[gid]
			if !parent.isMain() {
				a.graph.Groups[gid].Parent = &BodyOwner{GroupNodeID: parent.group, TaskKey: parent.task}
			}
			for _, t := range a.tasks[gid] {
				r := regionID{group: gid, task: t.Key}
				if a.skipBody[r] {
					continue
				}
				a.parentOf[r] = parent
				if _, ok := a.graph.Groups[gid].Bodies[t.Key]; !ok {
					a.graph.Groups[gid].Bodies[t.Key] = []string{}
				}
				if owner, taken := a.claimed[t.Entry]; taken {
					a.errs = append(a.errs, ValidationError{
						Code:    "GROUP_BODY_OVERLAP",
						Message: "parallel_group " + gid + " task " + strconv.Quote(t.Key) + " entry " + t.Entry + " already belongs to " + a.describe(owner),
						NodeID:  gid,
						TaskKey: t.Key,
					})
					continue
				}
				next = append(next, pendingRegion{id: r, seed: t.Entry})
			}
		}
		wave = next
	}
}

// grow returns the members of region r grown from seed (see AnalyzeGroups),
// recording every crossing edge where growth stopped.
func (a *groupAnalysis) grow(r regionID, seed string) map[string]bool {
	members := map[string]bool{seed: true}
	queue := []string{seed}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, fe := range a.flow[cur] {
			t := fe.target
			if members[t] {
				continue
			}
			if owner, taken := a.claimed[t]; taken && owner != r {
				a.crossings = append(a.crossings, crossing{from: r, src: cur, dst: t, edgeIdx: fe.idx})
				continue
			}
			if anchor, isEntry := a.anchors[t]; isEntry && anchor != r {
				a.crossings = append(a.crossings, crossing{from: r, src: cur, dst: t, edgeIdx: fe.idx})
				continue
			}
			members[t] = true
			queue = append(queue, t)
		}
	}
	return members
}

// classifyCrossings turns every stopped edge into exactly one finding.
func (a *groupAnalysis) classifyCrossings() {
	sort.SliceStable(a.crossings, func(i, j int) bool { return a.crossings[i].edgeIdx < a.crossings[j].edgeIdx })
	seen := map[int]bool{}
	for _, c := range a.crossings {
		if seen[c.edgeIdx] {
			continue
		}
		seen[c.edgeIdx] = true

		to, known := a.claimed[c.dst]
		if !known {
			anchor, isEntry := a.anchors[c.dst]
			if !isEntry {
				continue
			}
			to = anchor
		}
		edge := c.src + " -> " + c.dst
		switch {
		case a.isEnclosingGroup(c.dst, c.from):
			a.errs = append(a.errs, ValidationError{
				Code: "GROUP_BODY_OVERLAP",
				Message: "edge " + edge + " in the body of parallel_group " + c.from.group + " task " + strconv.Quote(c.from.task) +
					" re-enters enclosing group node " + c.dst + "; a body must end at a sink, never loop back into its group",
				NodeID:  c.from.group,
				TaskKey: c.from.task,
				EdgeIdx: c.edgeIdx,
			})
		case !c.from.isMain() && !a.isInside(to, c.from):
			a.errs = append(a.errs, ValidationError{
				Code: "GROUP_BODY_ESCAPE",
				Message: "edge " + edge + " leaves the body of parallel_group " + c.from.group + " task " + strconv.Quote(c.from.task) +
					" into " + a.describe(to) + "; a body ends at a sink — the group continues through its success/failure edges",
				NodeID:  c.from.group,
				TaskKey: c.from.task,
				EdgeIdx: c.edgeIdx,
			})
		case !to.isMain():
			a.errs = append(a.errs, ValidationError{
				Code: "GROUP_BODY_EXTERNAL_EDGE",
				Message: "edge " + edge + " enters the body of parallel_group " + to.group + " task " + strconv.Quote(to.task) +
					" from " + a.describe(c.from) + "; only the group's own \"task\" edge may enter a body",
				NodeID:  to.group,
				TaskKey: to.task,
				EdgeIdx: c.edgeIdx,
			})
		}
	}
}

// isInside reports whether region r is anc or nested (at any depth) inside it.
func (a *groupAnalysis) isInside(r, anc regionID) bool {
	for steps := 0; steps <= len(a.parentOf)+1; steps++ {
		if r == anc {
			return true
		}
		if r.isMain() {
			return false
		}
		p, ok := a.parentOf[r]
		if !ok {
			return false
		}
		r = p
	}
	return false
}

// isEnclosingGroup reports whether node id is the group owning region r or
// the group owning any region that encloses r.
func (a *groupAnalysis) isEnclosingGroup(id string, r regionID) bool {
	for steps := 0; steps <= len(a.parentOf)+1 && !r.isMain(); steps++ {
		if r.group == id {
			return true
		}
		p, ok := a.parentOf[r]
		if !ok {
			return false
		}
		r = p
	}
	return false
}

func (a *groupAnalysis) describe(r regionID) string {
	if r.isMain() {
		return "the enclosing main path"
	}
	return "the body of parallel_group " + r.group + " task " + strconv.Quote(r.task)
}

// inNodeOrder returns set's ids sorted by their first position in wf.Nodes.
func (a *groupAnalysis) inNodeOrder(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return a.nodeIndex[out[i]] < a.nodeIndex[out[j]] })
	return out
}

// linkNested fills GroupScope.Nested in wf.Nodes order.
func (a *groupAnalysis) linkNested() {
	for _, id := range a.graph.Order {
		if p := a.graph.Groups[id].Parent; p != nil {
			parent := a.graph.Groups[p.GroupNodeID]
			parent.Nested = append(parent.Nested, id)
		}
	}
}

// checkCycles rejects a group on a cycle over ALL edges (task edges included):
// re-entering a group would reuse its persisted group run. Loops that do not
// pass through a group node are unaffected.
func (a *groupAnalysis) checkCycles() {
	adj := buildAdjacency(a.wf)
	for _, id := range a.graph.Order {
		if nodeOnCycle(id, adj) {
			a.errs = append(a.errs, ValidationError{
				Code:    "GROUP_ON_CYCLE",
				Message: "parallel_group " + id + " sits on a cycle; a group runs once per instance, so no path may lead from it back to itself — move the loop inside a task body or before the group",
				NodeID:  id,
			})
		}
	}
}
