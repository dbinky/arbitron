package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Live state vocabulary for nodes and execution contexts. A context's State is
// the owning instance's State verbatim, or LiveStateUnknown for a redacted
// descendant (Redacted=true: hidden by authorization or unreadable).
const (
	LiveStatePending   = "pending"
	LiveStateQueued    = "queued"
	LiveStateRunning   = "running"
	LiveStateWaiting   = "waiting"
	LiveStatePaused    = "paused"
	LiveStateCompleted = "completed"
	LiveStateFailed    = "failed"
	LiveStateSkipped   = "skipped"
	LiveStateCancelled = "cancelled"
	// LiveStateUnknown: the state could not be read or may not be shown (a
	// redacted descendant). Never rendered as pending or completed.
	LiveStateUnknown = "unknown"
)

// Execution context kinds.
const (
	ContextKindRoot     = "root"
	ContextKindBranch   = "branch"
	ContextKindWorkflow = "workflow"
	ContextKindSpawn    = "spawn"
)

// NodeStateKey is a live node's identity. Child workflows reuse definition node
// ids and several tasks may run the same child workflow, so a node is ALWAYS
// addressed by (instance, node) — never by node id alone.
type NodeStateKey struct {
	InstanceID uuid.UUID `json:"instanceId"`
	NodeID     string    `json:"nodeId"`
}

// LiveNodeState is one definition node as executed by one instance.
type LiveNodeState struct {
	InstanceID uuid.UUID `json:"instanceId"`
	NodeID     string    `json:"nodeId"`
	State      string    `json:"state"` // pending|queued|running|waiting|paused|completed|failed|skipped|cancelled|unknown
	Reason     string    `json:"reason,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
}

// Key returns the node's namespaced identity.
func (s LiveNodeState) Key() NodeStateKey {
	return NodeStateKey{InstanceID: s.InstanceID, NodeID: s.NodeID}
}

// ExecutionContext is one instance in the selected subtree: the root, a branch
// task, a workflow task or a legacy spawn child.
type ExecutionContext struct {
	InstanceID       uuid.UUID       `json:"instanceId"` // uuid.Nil when Redacted
	ParentInstanceID *uuid.UUID      `json:"parentInstanceId,omitempty"`
	GroupRunID       *uuid.UUID      `json:"groupRunId,omitempty"`
	TaskKey          string          `json:"taskKey,omitempty"`
	Kind             string          `json:"kind"` // root|branch|workflow|spawn
	WorkflowName     string          `json:"workflowName,omitempty"`
	State            string          `json:"state"`
	Blocking         bool            `json:"blocking"`
	Redacted         bool            `json:"redacted"`
	Nodes            []LiveNodeState `json:"nodes"`
	// Graph is the instance's workflow snapshot; omitted for branch contexts
	// (they run the parent's graph) and for redacted contexts.
	Graph map[string]any `json:"graph,omitempty"`
	// Layout is the ownership analysis of a workflow/spawn context's own graph,
	// so nested groups inside a child workflow can be laid out.
	Layout *workflow.GroupGraph `json:"layout,omitempty"`
}

// GroupPanel is the live inspection view of one parallel group run (P4-T2).
type GroupPanel struct {
	GroupRunID         uuid.UUID                      `json:"groupRunId"`
	InstanceID         uuid.UUID                      `json:"instanceId"`
	NodeID             string                         `json:"nodeId"`
	Form               string                         `json:"form"`
	CompletionRule     string                         `json:"completionRule"`
	UnfinishedBehavior string                         `json:"unfinishedBehavior"`
	GatherMode         string                         `json:"gatherMode"`
	State              string                         `json:"state"`
	Tasks              []GroupPanelTask               `json:"tasks"`
	Progress           GroupProgress                  `json:"progress"`
	WaitingReasons     []string                       `json:"waitingReasons"`
	Winner             string                         `json:"winner,omitempty"`
	Decision           *instance.GroupDecision        `json:"decision,omitempty"`
	FinalOutput        map[string]any                 `json:"finalOutput,omitempty"`
	Gather             *instance.GatherReport         `json:"gather,omitempty"`
	ActiveSession      *instance.ResolutionSessionRef `json:"activeSession,omitempty"`
}

// GroupPanelTask is one roster task card in author order.
type GroupPanelTask struct {
	Key                     string     `json:"key"`
	Kind                    string     `json:"kind"`
	Origin                  string     `json:"origin"`
	ChildInstanceID         *uuid.UUID `json:"childInstanceId,omitempty"`
	LiveStatus              string     `json:"liveStatus"`
	FrozenStatus            string     `json:"frozenStatus"` // "" until decided
	Blocking                bool       `json:"blocking"`
	ContinuingIndependently bool       `json:"continuingIndependently"`
	CancelRequested         bool       `json:"cancelRequested"`
	LaunchError             string     `json:"launchError,omitempty"`
	Redacted                bool       `json:"redacted"`
}

// GroupProgress counts task cards by live status.
type GroupProgress struct {
	Total      int `json:"total"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
	Active     int `json:"active"`
	NotStarted int `json:"notStarted"`
}

// GraphState is the authorized aggregate view of a run and its descendants.
type GraphState struct {
	RootInstanceID uuid.UUID            `json:"rootInstanceId"`
	Version        string               `json:"version"` // hash of states
	Active         bool                 `json:"active"`  // visible active work, or activity unknown after a read failure
	Contexts       []ExecutionContext   `json:"contexts"`
	Groups         []GroupPanel         `json:"groups"`
	Layout         *workflow.GroupGraph `json:"layout,omitempty"` // AnalyzeGroups of the root snapshot
	Warnings       []string             `json:"warnings,omitempty"`
}

// InstanceViewer is the authorized single-instance read: an error wrapping
// instance.ErrNotFound for both an absent instance and one the caller may not
// view. *Engine satisfies it (Engine.GetInstance).
type InstanceViewer interface {
	GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
}

// GraphStateReader builds GraphState. Every instance it includes — the root
// AND each descendant — passes through viewer under the caller's identity;
// shared ownership is not a visibility decision, because public/department
// visibility is stamped per child workflow.
type GraphStateReader struct {
	viewer    InstanceViewer
	nodeExecs instance.NodeExecutionRepository
	groups    instance.GroupRunRepository
	links     instance.SpawnLinkRepository
}

// NewGraphStateReader wires the reader. A nil dependency is a programming
// error (a missing link repository would silently hide every descendant), so
// it panics.
func NewGraphStateReader(viewer InstanceViewer, nodeExecs instance.NodeExecutionRepository, groups instance.GroupRunRepository, links instance.SpawnLinkRepository) *GraphStateReader {
	if viewer == nil || nodeExecs == nil || groups == nil || links == nil {
		panic("engine: NewGraphStateReader requires non-nil viewer, nodeExecs, groups and links")
	}
	return &GraphStateReader{viewer: viewer, nodeExecs: nodeExecs, groups: groups, links: links}
}

// graphEntry is the traversal's private record of one context.
type graphEntry struct {
	index    int                // position in graphTraversal.contexts
	inst     *instance.Instance // nil for a redacted / unreadable descendant
	wf       *workflow.Workflow // nil when the snapshot could not be read
	gg       *workflow.GroupGraph
	nodeSet  map[string]bool // definition nodes this context executes
	runs     []instance.GroupRun
	runsErr  error
	runsRead bool
}

type rosterMember struct {
	runID uuid.UUID
	task  instance.GroupTask
}

type graphTraversal struct {
	r          *GraphStateReader
	ctx        context.Context
	contexts   []ExecutionContext
	entries    []*graphEntry
	visible    map[uuid.UUID]*instance.Instance
	hidden     map[uuid.UUID]bool // redacted or unreadable descendants; never exposed
	unreadable map[uuid.UUID]bool // transient read failures, distinct from authorization denial
	uncertain  bool               // storage/traversal failure: keep refreshing until activity is known
	visited    map[uuid.UUID]bool
	warnings   []string
}

// GraphState returns the authorized aggregate state of id and its descendants.
// An unauthorized or absent root returns the viewer's error (wrapping
// instance.ErrNotFound) before any other read, so existence is never leaked.
func (r *GraphStateReader) GraphState(ctx context.Context, id uuid.UUID) (*GraphState, error) {
	root, err := r.viewer.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	t := &graphTraversal{
		r:          r,
		ctx:        ctx,
		visible:    map[uuid.UUID]*instance.Instance{},
		hidden:     map[uuid.UUID]bool{},
		unreadable: map[uuid.UUID]bool{},
		visited:    map[uuid.UUID]bool{root.ID: true},
	}
	rootEntry := t.addVisible(root, ContextKindRoot)
	t.hideUnviewableParentLink(rootEntry)
	t.walk(rootEntry, 1)

	groups := t.buildGroupPanels()
	gs := &GraphState{
		RootInstanceID: root.ID,
		Contexts:       t.contexts,
		Groups:         groups,
		Layout:         rootEntry.gg,
	}
	gs.Warnings = t.warnings
	gs.Active = t.anyActive()
	gs.Version = graphStateVersion(gs)
	return gs, nil
}

// addVisible records an authorized instance as a context with derived node
// states. Its trace is read only here, i.e. only after authorization.
func (t *graphTraversal) addVisible(inst *instance.Instance, kind string) *graphEntry {
	t.visible[inst.ID] = inst
	ec := ExecutionContext{
		InstanceID:       inst.ID,
		ParentInstanceID: copyUUIDPtr(inst.ParentInstanceID),
		GroupRunID:       copyUUIDPtr(inst.GroupRunID),
		Kind:             kind,
		WorkflowName:     inst.WorkflowName,
		State:            string(inst.State),
		Nodes:            []LiveNodeState{},
	}
	if inst.GroupTaskKey != nil {
		ec.TaskKey = *inst.GroupTaskKey
	}
	e := &graphEntry{index: len(t.contexts), inst: inst, nodeSet: map[string]bool{}}
	wf, _, _, err := parseWorkflowGraph(inst.WorkflowSnapshot)
	if err != nil {
		t.warn(fmt.Sprintf("workflow snapshot of run %s could not be read", inst.ID))
	} else {
		gg, _ := workflow.AnalyzeGroups(wf)
		if gg == nil {
			gg = &workflow.GroupGraph{}
		}
		e.wf, e.gg = wf, gg
		ids := contextNodeIDs(wf, gg, inst)
		for _, nid := range ids {
			e.nodeSet[nid] = true
		}
		execs, terr := t.r.nodeExecs.ListByInstance(t.ctx, inst.ID)
		if terr != nil {
			t.warn(fmt.Sprintf("execution history of run %s could not be read", inst.ID))
		}
		ec.Nodes = deriveNodeStates(inst, wf, ids, latestExecutions(execs), terr == nil)
		if kind != ContextKindBranch {
			ec.Graph = inst.WorkflowSnapshot
		}
		if kind == ContextKindWorkflow || kind == ContextKindSpawn {
			ec.Layout = gg
		}
	}
	t.entries = append(t.entries, e)
	t.contexts = append(t.contexts, ec)
	return e
}

// hideUnviewableParentLink drops the selected run's breadcrumb (parent id and
// the parent-owned group identifiers) when the caller may not view the parent.
func (t *graphTraversal) hideUnviewableParentLink(e *graphEntry) {
	ec := &t.contexts[e.index]
	if ec.ParentInstanceID == nil {
		return
	}
	if _, err := t.r.viewer.GetInstance(t.ctx, *ec.ParentInstanceID); err == nil {
		return
	}
	ec.ParentInstanceID = nil
	ec.GroupRunID = nil
	ec.TaskKey = ""
}

// walk visits parent's children (depth-first, link order). It also loads the
// parent's group records once; P4-T2 builds panels from them.
func (t *graphTraversal) walk(parent *graphEntry, depth int) {
	pid := parent.inst.ID
	runs, rerr := t.r.groups.ListByParent(t.ctx, pid)
	parent.runs, parent.runsErr, parent.runsRead = runs, rerr, true
	if rerr != nil {
		t.uncertain = true
		t.warn(fmt.Sprintf("parallel group records of run %s could not be read", pid))
	}
	roster := rosterByChild(runs)

	refs, err := t.r.links.ListChildren(t.ctx, pid)
	if err != nil {
		t.uncertain = true
		t.warn(fmt.Sprintf("child runs of run %s could not be listed", pid))
		return
	}
	if depth >= instance.MaxSpawnDepth+1 && len(refs) > 0 {
		t.uncertain = true
		t.warn(fmt.Sprintf("descendants of run %s exceed the maximum nesting depth and are not shown", pid))
		return
	}
	for _, ref := range refs {
		if t.visited[ref.ID] {
			continue
		}
		t.visited[ref.ID] = true
		member, inRoster := roster[ref.ID]
		child, gerr := t.r.viewer.GetInstance(t.ctx, ref.ID)
		switch {
		case gerr == nil && child != nil:
			e := t.addVisible(child, childKind(child, member, inRoster))
			ec := &t.contexts[e.index]
			ec.Blocking = blockingOf(child, member, inRoster)
			if inRoster {
				gid := member.runID
				ec.GroupRunID = &gid
				ec.TaskKey = member.task.Key
			}
			t.walk(e, depth+1)
		case gerr == nil || errors.Is(gerr, instance.ErrNotFound):
			t.addRedacted(pid, ref.ID, member, inRoster)
		default:
			// The id came from an unauthorized link read: never echo it.
			t.uncertain = true
			t.warn("a descendant run could not be read")
			t.unreadable[ref.ID] = true
			t.addRedacted(pid, ref.ID, member, inRoster)
		}
	}
}

// addRedacted records a generic hidden dependency: no instance id, definition,
// trace, state or results; only the parent-owned group anchor when one exists.
// Its subtree is not traversed.
func (t *graphTraversal) addRedacted(parentID, childID uuid.UUID, member rosterMember, inRoster bool) {
	t.hidden[childID] = true
	pid := parentID
	ec := ExecutionContext{
		ParentInstanceID: &pid,
		Kind:             ContextKindSpawn,
		State:            LiveStateUnknown,
		Redacted:         true,
		Nodes:            []LiveNodeState{},
	}
	if inRoster {
		ec.Kind = string(member.task.Kind)
		gid := member.runID
		ec.GroupRunID = &gid
		ec.TaskKey = member.task.Key
		ec.Blocking = member.task.Blocking
	}
	t.entries = append(t.entries, &graphEntry{index: len(t.contexts)})
	t.contexts = append(t.contexts, ec)
}

func (t *graphTraversal) warn(msg string) {
	for _, w := range t.warnings {
		if w == msg {
			return
		}
	}
	t.warnings = append(t.warnings, msg)
}

func (t *graphTraversal) anyActive() bool {
	// Inactivity cannot be proven after a failed read.
	if t.uncertain {
		return true
	}
	for _, c := range t.contexts {
		if !c.Redacted && isActiveContextState(c.State) {
			return true
		}
	}
	return false
}

func isActiveContextState(s string) bool {
	switch instance.State(s) {
	case instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused:
		return true
	}
	return false
}

func rosterByChild(runs []instance.GroupRun) map[uuid.UUID]rosterMember {
	out := map[uuid.UUID]rosterMember{}
	for _, run := range runs {
		for _, task := range run.Tasks {
			if task.ChildInstanceID != uuid.Nil {
				out[task.ChildInstanceID] = rosterMember{runID: run.ID, task: task}
			}
		}
	}
	return out
}

func childKind(child *instance.Instance, member rosterMember, inRoster bool) string {
	switch child.GroupTaskKind {
	case instance.GroupTaskKindBranch:
		return ContextKindBranch
	case instance.GroupTaskKindWorkflow:
		return ContextKindWorkflow
	}
	if inRoster {
		return string(member.task.Kind)
	}
	return ContextKindSpawn
}

func blockingOf(child *instance.Instance, member rosterMember, inRoster bool) bool {
	if inRoster {
		return member.task.Blocking
	}
	return child.SpawnMode == instance.SpawnModeWaitForChild && !child.State.IsTerminal()
}

// contextNodeIDs returns, in wf.Nodes order, the definition nodes inst
// executes: a branch instance runs exactly the nodes IMMEDIATELY owned by its
// (group node, task key) — Phase 1 ownership, never a separate reachability
// walk; any other instance runs its main path (nodes owned by no task).
func contextNodeIDs(wf *workflow.Workflow, gg *workflow.GroupGraph, inst *instance.Instance) []string {
	var owner *workflow.BodyOwner
	if inst.GroupTaskKind == instance.GroupTaskKindBranch && inst.ParentNodeID != nil && inst.GroupTaskKey != nil {
		owner = &workflow.BodyOwner{GroupNodeID: *inst.ParentNodeID, TaskKey: *inst.GroupTaskKey}
	}
	ids := make([]string, 0, len(wf.Nodes))
	for _, n := range wf.Nodes {
		o, owned := gg.Owner[n.ID]
		if owner == nil {
			if !owned {
				ids = append(ids, n.ID)
			}
			continue
		}
		if owned && o == *owner {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

// deriveNodeStates combines the instance state/cursor with the latest trace
// per node. The live cursor (running / queued / waiting / paused / cancelled)
// and a failure override a provisional succeeded wait record; a node with no
// trace that is not current is pending (or unknown when the trace is
// unreadable).
func deriveNodeStates(inst *instance.Instance, wf *workflow.Workflow, ids []string, latest map[string]*instance.NodeExecution, tracesOK bool) []LiveNodeState {
	nodeTypes := make(map[string]workflow.NodeType, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodeTypes[n.ID] = n.Type
	}
	cursor := ""
	if inst.CurrentNodeID != nil {
		cursor = *inst.CurrentNodeID
	}
	if cursor == "" && inst.State == instance.StateQueued {
		for _, n := range wf.Nodes {
			if n.Entrypoint {
				cursor = n.ID
				break
			}
		}
	}
	activeCursor := cursor
	failedNode := ""
	switch inst.State {
	case instance.StateFailed:
		if inst.FailedNodeID != nil && *inst.FailedNodeID != "" {
			failedNode = *inst.FailedNodeID
		} else {
			failedNode = cursor
		}
		activeCursor = ""
	case instance.StateCompleted:
		activeCursor = ""
	}

	out := make([]LiveNodeState, 0, len(ids))
	for _, id := range ids {
		ls := LiveNodeState{InstanceID: inst.ID, NodeID: id}
		exec := latest[id]
		if exec != nil {
			ls.Attempt = exec.Attempt
		}
		switch {
		case failedNode != "" && id == failedNode:
			ls.State, ls.Reason = LiveStateFailed, failureReason(exec)
		case activeCursor != "" && id == activeCursor:
			ls.State, ls.Reason = cursorState(inst, nodeTypes[id])
		case exec != nil:
			ls.State, ls.Reason = traceState(exec)
		case !tracesOK:
			ls.State, ls.Reason = LiveStateUnknown, "execution history unavailable"
		default:
			ls.State = LiveStatePending
		}
		out = append(out, ls)
	}
	return out
}

func cursorState(inst *instance.Instance, nt workflow.NodeType) (string, string) {
	switch inst.State {
	case instance.StateRunning:
		return LiveStateRunning, ""
	case instance.StateQueued:
		return LiveStateQueued, "waiting for execution capacity"
	case instance.StateWaiting:
		return LiveStateWaiting, waitReason(nt, inst)
	case instance.StatePaused:
		return LiveStatePaused, "run paused"
	case instance.StateCancelled:
		return LiveStateCancelled, "run cancelled"
	}
	return LiveStateUnknown, ""
}

func traceState(exec *instance.NodeExecution) (string, string) {
	switch exec.Status {
	case instance.NodeStatusSucceeded:
		return LiveStateCompleted, ""
	case instance.NodeStatusFailed:
		return LiveStateFailed, failureReason(exec)
	case instance.NodeStatusSkipped:
		return LiveStateSkipped, ""
	}
	return LiveStateUnknown, ""
}

func failureReason(exec *instance.NodeExecution) string {
	if exec != nil && exec.Status == instance.NodeStatusFailed && exec.Error != nil {
		if s, ok := exec.Error["error"].(string); ok && s != "" {
			return s
		}
	}
	return "run failed"
}

func waitReason(nt workflow.NodeType, inst *instance.Instance) string {
	switch {
	case nt == workflow.NodeTypeParallelGroup:
		return "waiting for parallel group"
	case nt == workflow.NodeTypeSpawnWorkflow:
		return "waiting for child run"
	case workflow.IsInteractive(nt):
		return "waiting for interactive input"
	case nt == workflow.NodeTypeWebhookWait:
		return "waiting for webhook"
	case nt == workflow.NodeTypeTimerDelay:
		if inst.WaitingUntil != nil {
			return "waiting until " + inst.WaitingUntil.UTC().Format(time.RFC3339)
		}
		return "waiting for timer"
	}
	return "waiting"
}

// latestExecutions keeps, per node, the execution that started last (loops
// re-run nodes; attempts restart at 1 per visit, so started_at is the order).
func latestExecutions(execs []instance.NodeExecution) map[string]*instance.NodeExecution {
	out := make(map[string]*instance.NodeExecution, len(execs))
	for i := range execs {
		ex := &execs[i]
		cur, ok := out[ex.NodeID]
		if !ok || !ex.StartedAt.Before(cur.StartedAt) {
			out[ex.NodeID] = ex
		}
	}
	return out
}

// graphStateVersion hashes everything that can change while a tree runs. The
// graph snapshots and layouts are immutable per instance and are excluded so
// the hash stays cheap.
func graphStateVersion(gs *GraphState) string {
	ctxs := make([]ExecutionContext, len(gs.Contexts))
	for i, c := range gs.Contexts {
		c.Graph = nil
		c.Layout = nil
		ctxs[i] = c
	}
	b, err := json.Marshal(struct {
		Active   bool               `json:"active"`
		Contexts []ExecutionContext `json:"contexts"`
		Groups   []GroupPanel       `json:"groups"`
		Warnings []string           `json:"warnings"`
	}{gs.Active, ctxs, gs.Groups, gs.Warnings})
	if err != nil {
		slog.Warn("graph state: version hash failed", "rootInstanceId", gs.RootInstanceID, "error", err)
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func copyUUIDPtr(p *uuid.UUID) *uuid.UUID {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
