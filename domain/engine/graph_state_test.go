package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ---------------------------------------------------------------------------
// Fakes. Every fake keys its data by the load-bearing argument (instance id /
// parent id) and records which ids were read, so tests can prove that an
// inaccessible descendant was never traced or traversed.
// ---------------------------------------------------------------------------

// gsInstanceStore backs the REAL Engine.GetInstance. Only Get is exercised;
// any other InstanceRepository call panics (nil embedded interface) so an
// unexpected dependency is loud.
type gsInstanceStore struct {
	instance.InstanceRepository
	mu     sync.Mutex
	rows   map[uuid.UUID]*instance.Instance
	getErr map[uuid.UUID]error
}

func (s *gsInstanceStore) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.getErr[id]; err != nil {
		return nil, err
	}
	inst, ok := s.rows[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	cp := *inst
	return &cp, nil
}

// gsViewer wraps the production authorization (Engine.GetInstance -> canView)
// and records every id it was asked to authorize.
type gsViewer struct {
	inner engine.InstanceViewer
	mu    sync.Mutex
	asked []uuid.UUID
}

func (v *gsViewer) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	v.mu.Lock()
	v.asked = append(v.asked, id)
	v.mu.Unlock()
	return v.inner.GetInstance(ctx, id)
}

func (v *gsViewer) wasAsked(id uuid.UUID) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return gsContainsID(v.asked, id)
}

type gsNodeExecs struct {
	instance.NodeExecutionRepository
	mu    sync.Mutex
	rows  map[uuid.UUID][]instance.NodeExecution
	errs  map[uuid.UUID]error
	reads []uuid.UUID
}

func (n *gsNodeExecs) ListByInstance(_ context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reads = append(n.reads, id)
	if err := n.errs[id]; err != nil {
		return nil, err
	}
	return append([]instance.NodeExecution(nil), n.rows[id]...), nil
}

type gsGroups struct {
	instance.GroupRunRepository
	mu       sync.Mutex
	byParent map[uuid.UUID][]instance.GroupRun
	errs     map[uuid.UUID]error
	reads    []uuid.UUID
}

func (g *gsGroups) ListByParent(_ context.Context, parentID uuid.UUID) ([]instance.GroupRun, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reads = append(g.reads, parentID)
	if err := g.errs[parentID]; err != nil {
		return nil, err
	}
	return append([]instance.GroupRun(nil), g.byParent[parentID]...), nil
}

type gsLinks struct {
	instance.SpawnLinkRepository
	mu       sync.Mutex
	children map[uuid.UUID][]instance.ChildRef
	errs     map[uuid.UUID]error
	reads    []uuid.UUID
}

func (l *gsLinks) ListChildren(_ context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads = append(l.reads, parentID)
	if err := l.errs[parentID]; err != nil {
		return nil, err
	}
	return append([]instance.ChildRef(nil), l.children[parentID]...), nil
}

func gsContainsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type gsFixture struct {
	t      *testing.T
	store  *gsInstanceStore
	viewer *gsViewer
	execs  *gsNodeExecs
	groups *gsGroups
	links  *gsLinks
	reader *engine.GraphStateReader
	owner  uuid.UUID
	clock  time.Time
}

func newGSFixture(t *testing.T) *gsFixture {
	t.Helper()
	store := &gsInstanceStore{rows: map[uuid.UUID]*instance.Instance{}, getErr: map[uuid.UUID]error{}}
	// The viewer is the REAL engine authorization predicate over the fake store,
	// so redaction tests exercise production visibility rules.
	eng := engine.NewEngine(nil, store, nil, nil, nil, nil, newMockEventBus(), nil, nil, "")
	f := &gsFixture{
		t:      t,
		store:  store,
		viewer: &gsViewer{inner: eng},
		execs:  &gsNodeExecs{rows: map[uuid.UUID][]instance.NodeExecution{}, errs: map[uuid.UUID]error{}},
		groups: &gsGroups{byParent: map[uuid.UUID][]instance.GroupRun{}, errs: map[uuid.UUID]error{}},
		links:  &gsLinks{children: map[uuid.UUID][]instance.ChildRef{}, errs: map[uuid.UUID]error{}},
		owner:  uuid.New(),
		clock:  time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	f.reader = engine.NewGraphStateReader(f.viewer, f.execs, f.groups, f.links)
	return f
}

// as returns a request context carrying a plain (non-admin) user identity.
func (f *gsFixture) as(user uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: user, Username: "viewer", Role: identity.RoleUser,
	})
}

func (f *gsFixture) ownerCtx() context.Context { return f.as(f.owner) }

// add stores inst and, when it has a parent, links it the way the sqlite
// ListChildren query would return it.
func (f *gsFixture) add(inst *instance.Instance) *instance.Instance {
	f.store.mu.Lock()
	f.store.rows[inst.ID] = inst
	f.store.mu.Unlock()
	if inst.ParentInstanceID != nil {
		pid := *inst.ParentInstanceID
		f.links.children[pid] = append(f.links.children[pid], instance.ChildRef{ID: inst.ID, State: inst.State})
	}
	return inst
}

func (f *gsFixture) mutate(id uuid.UUID, fn func(*instance.Instance)) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	fn(f.store.rows[id])
}

// exec appends a node execution one second after the previous one, so
// started_at order is the insertion order.
func (f *gsFixture) exec(inst *instance.Instance, nodeID string, status instance.NodeExecutionStatus, errMsg string) {
	f.clock = f.clock.Add(time.Second)
	ne := instance.NodeExecution{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: nodeID, Attempt: 1,
		Status: status, StartedAt: f.clock,
	}
	if errMsg != "" {
		ne.Error = map[string]any{"error": errMsg}
	}
	f.execs.rows[inst.ID] = append(f.execs.rows[inst.ID], ne)
}

func (f *gsFixture) addRun(run instance.GroupRun) {
	f.groups.byParent[run.ParentInstanceID] = append(f.groups.byParent[run.ParentInstanceID], run)
}

func (f *gsFixture) graphState(ctx context.Context, id uuid.UUID) *engine.GraphState {
	f.t.Helper()
	gs, err := f.reader.GraphState(ctx, id)
	require.NoError(f.t, err)
	require.NotNil(f.t, gs)
	return gs
}

// ---------------------------------------------------------------------------
// Builders
// ---------------------------------------------------------------------------

func gsStr(s string) *string { return &s }

func gsSnapshot(t *testing.T, wf workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func gsRootInstance(owner uuid.UUID, name string, snap map[string]any, st instance.State, cursor string) *instance.Instance {
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: name, WorkflowSnapshot: snap,
		State: st, Priority: instance.PriorityNormal, Visibility: instance.VisibilityPrivate,
		Variables: map[string]any{},
	}
	if cursor != "" {
		inst.CurrentNodeID = gsStr(cursor)
	}
	return inst
}

// gsGroupTask builds a group-task child exactly as Engine.StartGroupTask
// (P2-T7) persists one: parent link at the group node, group run id, task key
// and kind, owner/visibility inherited from the parent.
func gsGroupTask(parent *instance.Instance, groupNode string, runID uuid.UUID, key string, kind instance.GroupTaskKind,
	name string, snap map[string]any, st instance.State, cursor string) *instance.Instance {
	inst := gsRootInstance(parent.OwnerID, name, snap, st, cursor)
	inst.Visibility = parent.Visibility
	inst.Department = parent.Department
	pid := parent.ID
	inst.ParentInstanceID = &pid
	inst.ParentNodeID = gsStr(groupNode)
	inst.SpawnMode = instance.SpawnModeGroupTask
	inst.SpawnDepth = parent.SpawnDepth + 1
	rid := runID
	inst.GroupRunID = &rid
	inst.GroupTaskKey = gsStr(key)
	inst.GroupTaskKind = kind
	return inst
}

func gsRosterTask(pos int, key string, kind instance.GroupTaskKind, origin string, child uuid.UUID, live instance.GroupTaskStatus) instance.GroupTask {
	return instance.GroupTask{
		Position: pos, Key: key, Kind: kind, Origin: origin, ChildInstanceID: child,
		LaunchState: instance.GroupTaskLaunched, LiveStatus: live, Blocking: true,
	}
}

func gsRun(id uuid.UUID, parent *instance.Instance, nodeID, form string, state instance.GroupRunState, tasks ...instance.GroupTask) instance.GroupRun {
	for i := range tasks {
		tasks[i].GroupRunID = id
	}
	return instance.GroupRun{
		ID: id, ParentInstanceID: parent.ID, NodeID: nodeID, Attempt: 1,
		Form: form, CompletionRule: workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
		GatherMode:         workflow.GroupGatherResults,
		State:              state, Tasks: tasks,
	}
}

func gsNode(id string, typ workflow.NodeType) workflow.Node { return workflow.Node{ID: id, Type: typ} }

func gsEntry(n workflow.Node) workflow.Node { n.Entrypoint = true; return n }

func gsGroupNode(id, form string, tasks ...workflow.GroupTask) workflow.Node {
	return workflow.Node{ID: id, Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
		Form: form, Tasks: tasks, CompletionRule: workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining, GatherMode: workflow.GroupGatherResults,
	}}}
}

func gsEdge(src, dst, port, label string) workflow.Edge {
	return workflow.Edge{Source: src, Target: dst, Output: port, Label: label}
}

// fanout: start -> g{ a: a1 -> a2 | b: b1 } -> done | fail
func gsFanoutWF() workflow.Workflow {
	return workflow.Workflow{Name: "fanout", Nodes: []workflow.Node{
		gsEntry(gsNode("start", workflow.NodeTypeTransform)),
		gsGroupNode("g", workflow.GroupFormBranches, workflow.GroupTask{Key: "a", Entry: "a1"}, workflow.GroupTask{Key: "b", Entry: "b1"}),
		gsNode("a1", workflow.NodeTypeToolCall), gsNode("a2", workflow.NodeTypeTransform),
		gsNode("b1", workflow.NodeTypeToolCall),
		gsNode("done", workflow.NodeTypeTransform), gsNode("fail", workflow.NodeTypeTransform),
	}, Edges: []workflow.Edge{
		gsEdge("start", "g", "", ""),
		gsEdge("g", "a1", workflow.GroupPortTask, "a"), gsEdge("g", "b1", workflow.GroupPortTask, "b"),
		gsEdge("a1", "a2", "", ""),
		gsEdge("g", "done", workflow.GroupPortSuccess, ""), gsEdge("g", "fail", workflow.GroupPortFailure, ""),
	}}
}

// child workflow: c1 -> c2 (stepID lets a test plant a distinctive node id).
func gsChildWF(name, stepID string) workflow.Workflow {
	return workflow.Workflow{Name: name, Nodes: []workflow.Node{
		gsEntry(gsNode("c1", workflow.NodeTypeToolCall)), gsNode(stepID, workflow.NodeTypeTransform),
	}, Edges: []workflow.Edge{gsEdge("c1", stepID, "", "")}}
}

// workflows-form fan-out: start -> w{ t1: wf1 | t2: wf2 } -> done | fail
func gsWorkflowsWF(wf1, wf2 string) workflow.Workflow {
	return workflow.Workflow{Name: "fan-workflows", Nodes: []workflow.Node{
		gsEntry(gsNode("start", workflow.NodeTypeTransform)),
		gsGroupNode("w", workflow.GroupFormWorkflows, workflow.GroupTask{Key: "t1", Workflow: wf1}, workflow.GroupTask{Key: "t2", Workflow: wf2}),
		gsNode("done", workflow.NodeTypeTransform), gsNode("fail", workflow.NodeTypeTransform),
	}, Edges: []workflow.Edge{
		gsEdge("start", "w", "", ""),
		gsEdge("w", "done", workflow.GroupPortSuccess, ""), gsEdge("w", "fail", workflow.GroupPortFailure, ""),
	}}
}

// nested outer: start -> g{ a: a1 -> h{t: inner-wf} -> a3 | a4 ; b: b1 } -> done | fail
func gsNestedOuterWF() workflow.Workflow {
	return workflow.Workflow{Name: "nested-outer", Nodes: []workflow.Node{
		gsEntry(gsNode("start", workflow.NodeTypeTransform)),
		gsGroupNode("g", workflow.GroupFormBranches, workflow.GroupTask{Key: "a", Entry: "a1"}, workflow.GroupTask{Key: "b", Entry: "b1"}),
		gsNode("a1", workflow.NodeTypeTransform),
		gsGroupNode("h", workflow.GroupFormWorkflows, workflow.GroupTask{Key: "t", Workflow: "inner-wf"}),
		gsNode("a3", workflow.NodeTypeTransform), gsNode("a4", workflow.NodeTypeTransform),
		gsNode("b1", workflow.NodeTypeTransform),
		gsNode("done", workflow.NodeTypeTransform), gsNode("fail", workflow.NodeTypeTransform),
	}, Edges: []workflow.Edge{
		gsEdge("start", "g", "", ""),
		gsEdge("g", "a1", workflow.GroupPortTask, "a"), gsEdge("g", "b1", workflow.GroupPortTask, "b"),
		gsEdge("a1", "h", "", ""),
		gsEdge("h", "a3", workflow.GroupPortSuccess, ""), gsEdge("h", "a4", workflow.GroupPortFailure, ""),
		gsEdge("g", "done", workflow.GroupPortSuccess, ""), gsEdge("g", "fail", workflow.GroupPortFailure, ""),
	}}
}

// inner workflow (a group as entrypoint): k{ p: p1 | q: q1 } -> fin | bad
func gsInnerWF() workflow.Workflow {
	return workflow.Workflow{Name: "inner-wf", Nodes: []workflow.Node{
		gsEntry(gsGroupNode("k", workflow.GroupFormBranches, workflow.GroupTask{Key: "p", Entry: "p1"}, workflow.GroupTask{Key: "q", Entry: "q1"})),
		gsNode("p1", workflow.NodeTypeToolCall), gsNode("q1", workflow.NodeTypeToolCall),
		gsNode("fin", workflow.NodeTypeTransform), gsNode("bad", workflow.NodeTypeTransform),
	}, Edges: []workflow.Edge{
		gsEdge("k", "p1", workflow.GroupPortTask, "p"), gsEdge("k", "q1", workflow.GroupPortTask, "q"),
		gsEdge("k", "fin", workflow.GroupPortSuccess, ""), gsEdge("k", "bad", workflow.GroupPortFailure, ""),
	}}
}

// legacy spawner: p1 -> s(spawn_workflow, wait_for_child) -> p2
func gsSpawnerWF() workflow.Workflow {
	return workflow.Workflow{Name: "spawner", Nodes: []workflow.Node{
		gsEntry(gsNode("p1", workflow.NodeTypeTransform)),
		{ID: "s", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{ChildWorkflow: "child-wf", SpawnMode: "wait_for_child"}},
		gsNode("p2", workflow.NodeTypeTransform),
	}, Edges: []workflow.Edge{gsEdge("p1", "s", "", ""), gsEdge("s", "p2", "success", "")}}
}

// fanout registers a waiting root at group g with two branch children and the
// group run that owns them. The root carries the provisional succeeded wait
// record the parallel_group executor writes (P2-T11).
type gsFanout struct {
	root, a, b *instance.Instance
	runID      uuid.UUID
}

func (f *gsFixture) fanout(aState, bState instance.State, aCursor, bCursor string) gsFanout {
	snap := gsSnapshot(f.t, gsFanoutWF())
	root := f.add(gsRootInstance(f.owner, "fanout", snap, instance.StateWaiting, "g"))
	f.exec(root, "start", instance.NodeStatusSucceeded, "")
	f.exec(root, "g", instance.NodeStatusSucceeded, "")
	runID := uuid.New()
	a := f.add(gsGroupTask(root, "g", runID, "a", instance.GroupTaskKindBranch, "fanout", snap, aState, aCursor))
	b := f.add(gsGroupTask(root, "g", runID, "b", instance.GroupTaskKindBranch, "fanout", snap, bState, bCursor))
	f.addRun(gsRun(runID, root, "g", workflow.GroupFormBranches, instance.GroupRunRunning,
		gsRosterTask(0, "a", instance.GroupTaskKindBranch, "a1", a.ID, instance.GroupTaskStatusFromState(aState)),
		gsRosterTask(1, "b", instance.GroupTaskKindBranch, "b1", b.ID, instance.GroupTaskStatusFromState(bState)),
	))
	return gsFanout{root: root, a: a, b: b, runID: runID}
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

func gsNodeMap(gs *engine.GraphState) map[engine.NodeStateKey]engine.LiveNodeState {
	out := map[engine.NodeStateKey]engine.LiveNodeState{}
	for _, c := range gs.Contexts {
		for _, n := range c.Nodes {
			out[n.Key()] = n
		}
	}
	return out
}

func gsKey(id uuid.UUID, node string) engine.NodeStateKey {
	return engine.NodeStateKey{InstanceID: id, NodeID: node}
}

func gsContext(t *testing.T, gs *engine.GraphState, id uuid.UUID) engine.ExecutionContext {
	t.Helper()
	for _, c := range gs.Contexts {
		if c.InstanceID == id && !c.Redacted {
			return c
		}
	}
	t.Fatalf("no visible context for instance %s", id)
	return engine.ExecutionContext{}
}

func gsContextByTask(t *testing.T, gs *engine.GraphState, key string) engine.ExecutionContext {
	t.Helper()
	for _, c := range gs.Contexts {
		if c.TaskKey == key {
			return c
		}
	}
	t.Fatalf("no context for task %s", key)
	return engine.ExecutionContext{}
}

func gsNodeIDs(c engine.ExecutionContext) []string {
	ids := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		ids = append(ids, n.NodeID)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

func TestGraphState_TwoRunningBranchesHaveNamespacedNodeStates(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")

	gs := f.graphState(f.ownerCtx(), fo.root.ID)

	require.Len(t, gs.Contexts, 3)
	assert.Equal(t, fo.root.ID, gs.RootInstanceID)
	assert.True(t, gs.Active)
	assert.Len(t, gs.Version, 32)
	require.NotNil(t, gs.Layout, "the root's ownership analysis is returned for layout")
	assert.Contains(t, gs.Layout.Groups, "g")
	assert.Empty(t, gs.Warnings)

	root := gs.Contexts[0]
	assert.Equal(t, engine.ContextKindRoot, root.Kind)
	assert.Equal(t, fo.root.ID, root.InstanceID)
	assert.Equal(t, []string{"start", "g", "done", "fail"}, gsNodeIDs(root), "the root context holds main-path nodes only")
	assert.NotNil(t, root.Graph)

	nodes := gsNodeMap(gs)
	assert.Equal(t, engine.LiveStateCompleted, nodes[gsKey(fo.root.ID, "start")].State)
	g := nodes[gsKey(fo.root.ID, "g")]
	assert.Equal(t, engine.LiveStateWaiting, g.State, "the current wait overrides the provisional succeeded wait trace")
	assert.Equal(t, "waiting for parallel group", g.Reason)
	assert.Equal(t, engine.LiveStatePending, nodes[gsKey(fo.root.ID, "done")].State)

	for _, tc := range []struct {
		inst  *instance.Instance
		key   string
		entry string
		body  []string
	}{
		{fo.a, "a", "a1", []string{"a1", "a2"}},
		{fo.b, "b", "b1", []string{"b1"}},
	} {
		c := gsContext(t, gs, tc.inst.ID)
		assert.Equal(t, engine.ContextKindBranch, c.Kind)
		assert.Equal(t, tc.key, c.TaskKey)
		require.NotNil(t, c.GroupRunID)
		assert.Equal(t, fo.runID, *c.GroupRunID)
		require.NotNil(t, c.ParentInstanceID)
		assert.Equal(t, fo.root.ID, *c.ParentInstanceID)
		assert.True(t, c.Blocking)
		assert.False(t, c.Redacted)
		assert.Nil(t, c.Graph, "branch contexts reuse the parent's graph")
		assert.Equal(t, tc.body, gsNodeIDs(c), "a branch context holds exactly its task's body")
		assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(tc.inst.ID, tc.entry)].State)
	}
	assert.Equal(t, engine.LiveStatePending, nodes[gsKey(fo.a.ID, "a2")].State, "a never-reached body node is pending")
	_, leaked := nodes[gsKey(fo.root.ID, "a1")]
	assert.False(t, leaked, "body nodes are never attributed to the parent instance")
}

func TestGraphState_AuthorizesRootAndEveryDescendant(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")

	f.graphState(f.ownerCtx(), fo.root.ID)

	for _, id := range []uuid.UUID{fo.root.ID, fo.a.ID, fo.b.ID} {
		assert.True(t, f.viewer.wasAsked(id), "instance %s must be authorized through the viewer", id)
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestGraphState_DuplicateNodeIDsAcrossTasksOfSameChildWorkflowStayDistinct(t *testing.T) {
	f := newGSFixture(t)
	parentSnap := gsSnapshot(t, gsWorkflowsWF("child-wf", "child-wf"))
	childSnap := gsSnapshot(t, gsChildWF("child-wf", "c2"))
	root := f.add(gsRootInstance(f.owner, "fan-workflows", parentSnap, instance.StateWaiting, "w"))
	runID := uuid.New()
	w1 := f.add(gsGroupTask(root, "w", runID, "t1", instance.GroupTaskKindWorkflow, "child-wf", childSnap, instance.StateRunning, "c1"))
	w2 := f.add(gsGroupTask(root, "w", runID, "t2", instance.GroupTaskKindWorkflow, "child-wf", childSnap, instance.StateRunning, "c2"))
	f.exec(w2, "c1", instance.NodeStatusSucceeded, "")
	f.addRun(gsRun(runID, root, "w", workflow.GroupFormWorkflows, instance.GroupRunRunning,
		gsRosterTask(0, "t1", instance.GroupTaskKindWorkflow, "child-wf", w1.ID, instance.GroupTaskStatusRunning),
		gsRosterTask(1, "t2", instance.GroupTaskKindWorkflow, "child-wf", w2.ID, instance.GroupTaskStatusRunning)))

	gs := f.graphState(f.ownerCtx(), root.ID)
	nodes := gsNodeMap(gs)

	assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(w1.ID, "c1")].State)
	assert.Equal(t, engine.LiveStateCompleted, nodes[gsKey(w2.ID, "c1")].State)
	assert.Equal(t, engine.LiveStatePending, nodes[gsKey(w1.ID, "c2")].State)
	assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(w2.ID, "c2")].State)

	c1Count := 0
	for k := range nodes {
		if k.NodeID == "c1" {
			c1Count++
		}
	}
	assert.Equal(t, 2, c1Count, "the same definition node id in two tasks yields two distinct live nodes")

	for _, id := range []uuid.UUID{w1.ID, w2.ID} {
		c := gsContext(t, gs, id)
		assert.Equal(t, engine.ContextKindWorkflow, c.Kind)
		assert.Equal(t, "child-wf", c.WorkflowName)
		assert.NotNil(t, c.Graph, "workflow-task contexts carry their own graph")
	}
}

func TestGraphState_NestedBothForms(t *testing.T) {
	f := newGSFixture(t)
	outer := gsSnapshot(t, gsNestedOuterWF())
	inner := gsSnapshot(t, gsInnerWF())

	root := f.add(gsRootInstance(f.owner, "nested-outer", outer, instance.StateWaiting, "g"))
	f.exec(root, "start", instance.NodeStatusSucceeded, "")
	f.exec(root, "g", instance.NodeStatusSucceeded, "")
	gRun := uuid.New()
	a := f.add(gsGroupTask(root, "g", gRun, "a", instance.GroupTaskKindBranch, "nested-outer", outer, instance.StateWaiting, "h"))
	f.exec(a, "a1", instance.NodeStatusSucceeded, "")
	f.exec(a, "h", instance.NodeStatusSucceeded, "")
	hRun := uuid.New()
	tTask := f.add(gsGroupTask(a, "h", hRun, "t", instance.GroupTaskKindWorkflow, "inner-wf", inner, instance.StateWaiting, "k"))
	f.exec(tTask, "k", instance.NodeStatusSucceeded, "")
	kRun := uuid.New()
	p := f.add(gsGroupTask(tTask, "k", kRun, "p", instance.GroupTaskKindBranch, "inner-wf", inner, instance.StateRunning, "p1"))
	q := f.add(gsGroupTask(tTask, "k", kRun, "q", instance.GroupTaskKindBranch, "inner-wf", inner, instance.StateQueued, "q1"))
	b := f.add(gsGroupTask(root, "g", gRun, "b", instance.GroupTaskKindBranch, "nested-outer", outer, instance.StateCompleted, "b1"))
	f.exec(b, "b1", instance.NodeStatusSucceeded, "")

	f.addRun(gsRun(gRun, root, "g", workflow.GroupFormBranches, instance.GroupRunRunning,
		gsRosterTask(0, "a", instance.GroupTaskKindBranch, "a1", a.ID, instance.GroupTaskStatusWaiting),
		gsRosterTask(1, "b", instance.GroupTaskKindBranch, "b1", b.ID, instance.GroupTaskStatusCompleted)))
	f.addRun(gsRun(hRun, a, "h", workflow.GroupFormWorkflows, instance.GroupRunRunning,
		gsRosterTask(0, "t", instance.GroupTaskKindWorkflow, "inner-wf", tTask.ID, instance.GroupTaskStatusWaiting)))
	f.addRun(gsRun(kRun, tTask, "k", workflow.GroupFormBranches, instance.GroupRunRunning,
		gsRosterTask(0, "p", instance.GroupTaskKindBranch, "p1", p.ID, instance.GroupTaskStatusRunning),
		gsRosterTask(1, "q", instance.GroupTaskKindBranch, "q1", q.ID, instance.GroupTaskStatusQueued)))

	gs := f.graphState(f.ownerCtx(), root.ID)

	var order []uuid.UUID
	var kinds []string
	for _, c := range gs.Contexts {
		order = append(order, c.InstanceID)
		kinds = append(kinds, c.Kind)
	}
	assert.Equal(t, []uuid.UUID{root.ID, a.ID, tTask.ID, p.ID, q.ID, b.ID}, order, "depth-first pre-order in link order")
	assert.Equal(t, []string{"root", "branch", "workflow", "branch", "branch", "branch"}, kinds)

	ac := gsContext(t, gs, a.ID)
	assert.Equal(t, []string{"a1", "h", "a3", "a4"}, gsNodeIDs(ac), "the nested group node belongs to its enclosing task's body")
	tc := gsContext(t, gs, tTask.ID)
	assert.Equal(t, []string{"k", "fin", "bad"}, gsNodeIDs(tc))
	require.NotNil(t, tc.Layout, "a workflow task with its own groups carries its own ownership analysis")
	assert.Contains(t, tc.Layout.Groups, "k")
	assert.Equal(t, []string{"p1"}, gsNodeIDs(gsContext(t, gs, p.ID)))

	nodes := gsNodeMap(gs)
	assert.Equal(t, engine.LiveStateWaiting, nodes[gsKey(a.ID, "h")].State)
	assert.Equal(t, engine.LiveStateCompleted, nodes[gsKey(a.ID, "a1")].State)
	assert.Equal(t, engine.LiveStateWaiting, nodes[gsKey(tTask.ID, "k")].State)
	assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(p.ID, "p1")].State)
	assert.Equal(t, engine.LiveStateQueued, nodes[gsKey(q.ID, "q1")].State)
	assert.Equal(t, engine.LiveStateCompleted, nodes[gsKey(b.ID, "b1")].State)
	assert.True(t, gs.Active)
}

func TestGraphState_PausedTask(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StatePaused, "a1", "b1")

	gs := f.graphState(f.ownerCtx(), fo.root.ID)

	bc := gsContext(t, gs, fo.b.ID)
	assert.Equal(t, string(instance.StatePaused), bc.State)
	n := gsNodeMap(gs)[gsKey(fo.b.ID, "b1")]
	assert.Equal(t, engine.LiveStatePaused, n.State)
	assert.Equal(t, "run paused", n.Reason)
	assert.True(t, gs.Active, "a paused task is still active work")
}

func TestGraphState_CapLimitedQueuedTasks(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateQueued, "a1", "b1")

	gs := f.graphState(f.ownerCtx(), fo.root.ID)
	n := gsNodeMap(gs)[gsKey(fo.b.ID, "b1")]
	assert.Equal(t, engine.LiveStateQueued, n.State, "a branch held back by the execution cap is queued at its entry")
	assert.Equal(t, "waiting for execution capacity", n.Reason)
	assert.Equal(t, string(instance.StateQueued), gsContext(t, gs, fo.b.ID).State)

	// A queued workflow task has no cursor yet: its entrypoint is the queued node.
	f2 := newGSFixture(t)
	root := f2.add(gsRootInstance(f2.owner, "fan-workflows", gsSnapshot(t, gsWorkflowsWF("child-wf", "child-wf")), instance.StateWaiting, "w"))
	runID := uuid.New()
	w := f2.add(gsGroupTask(root, "w", runID, "t1", instance.GroupTaskKindWorkflow, "child-wf",
		gsSnapshot(t, gsChildWF("child-wf", "c2")), instance.StateQueued, ""))
	f2.addRun(gsRun(runID, root, "w", workflow.GroupFormWorkflows, instance.GroupRunRunning,
		gsRosterTask(0, "t1", instance.GroupTaskKindWorkflow, "child-wf", w.ID, instance.GroupTaskStatusQueued)))
	gs2 := f2.graphState(f2.ownerCtx(), root.ID)
	nodes := gsNodeMap(gs2)
	assert.Equal(t, engine.LiveStateQueued, nodes[gsKey(w.ID, "c1")].State)
	assert.Equal(t, engine.LiveStatePending, nodes[gsKey(w.ID, "c2")].State)
}

func TestGraphState_CurrentWaitCancelFailureOverrideProvisionalSucceededTrace(t *testing.T) {
	cases := []struct {
		name       string
		state      instance.State
		cursor     string
		failedNode string
		execs      []struct {
			node   string
			status instance.NodeExecutionStatus
			err    string
		}
		node       string
		wantState  string
		wantReason string
	}{
		{name: "waiting overrides succeeded wait record", state: instance.StateWaiting, cursor: "g",
			execs: []struct {
				node   string
				status instance.NodeExecutionStatus
				err    string
			}{{"start", instance.NodeStatusSucceeded, ""}, {"g", instance.NodeStatusSucceeded, ""}},
			node: "g", wantState: engine.LiveStateWaiting, wantReason: "waiting for parallel group"},
		{name: "cancel overrides succeeded wait record", state: instance.StateCancelled, cursor: "g",
			execs: []struct {
				node   string
				status instance.NodeExecutionStatus
				err    string
			}{{"g", instance.NodeStatusSucceeded, ""}},
			node: "g", wantState: engine.LiveStateCancelled, wantReason: "run cancelled"},
		{name: "failure at the group node shows its error", state: instance.StateFailed, cursor: "g", failedNode: "g",
			execs: []struct {
				node   string
				status instance.NodeExecutionStatus
				err    string
			}{{"g", instance.NodeStatusSucceeded, ""}, {"g", instance.NodeStatusFailed, "launch exploded"}},
			node: "g", wantState: engine.LiveStateFailed, wantReason: "launch exploded"},
		{name: "failure without a failed node id fails the cursor node", state: instance.StateFailed, cursor: "g",
			node: "g", wantState: engine.LiveStateFailed, wantReason: "run failed"},
		{name: "completed run reads its trace", state: instance.StateCompleted, cursor: "done",
			execs: []struct {
				node   string
				status instance.NodeExecutionStatus
				err    string
			}{{"start", instance.NodeStatusSucceeded, ""}, {"g", instance.NodeStatusSucceeded, ""}, {"done", instance.NodeStatusSucceeded, ""}},
			node: "g", wantState: engine.LiveStateCompleted},
		{name: "running overrides an older trace of the same node", state: instance.StateRunning, cursor: "start",
			execs: []struct {
				node   string
				status instance.NodeExecutionStatus
				err    string
			}{{"start", instance.NodeStatusSucceeded, ""}},
			node: "start", wantState: engine.LiveStateRunning},
		{name: "skipped trace", state: instance.StateRunning, cursor: "g",
			execs: []struct {
				node   string
				status instance.NodeExecutionStatus
				err    string
			}{{"start", instance.NodeStatusSkipped, ""}},
			node: "start", wantState: engine.LiveStateSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGSFixture(t)
			root := gsRootInstance(f.owner, "fanout", gsSnapshot(t, gsFanoutWF()), tc.state, tc.cursor)
			if tc.failedNode != "" {
				root.FailedNodeID = gsStr(tc.failedNode)
			}
			f.add(root)
			for _, e := range tc.execs {
				f.exec(root, e.node, e.status, e.err)
			}
			// The group record exists (P4-T2 forces a reached group node with NO
			// record to "unknown"; this table is about trace/cursor precedence).
			f.addRun(gsRun(uuid.New(), root, "g", workflow.GroupFormBranches, instance.GroupRunRunning))
			n := gsNodeMap(f.graphState(f.ownerCtx(), root.ID))[gsKey(root.ID, tc.node)]
			assert.Equal(t, tc.wantState, n.State)
			assert.Equal(t, tc.wantReason, n.Reason)
		})
	}
}

func TestGraphState_LegacySpawnChildIsSpawnContext(t *testing.T) {
	f := newGSFixture(t)
	root := f.add(gsRootInstance(f.owner, "spawner", gsSnapshot(t, gsSpawnerWF()), instance.StateWaiting, "s"))
	child := gsRootInstance(f.owner, "child-wf", gsSnapshot(t, gsChildWF("child-wf", "c2")), instance.StateRunning, "c1")
	pid := root.ID
	child.ParentInstanceID = &pid
	child.ParentNodeID = gsStr("s")
	child.SpawnMode = instance.SpawnModeWaitForChild
	f.add(child)

	gs := f.graphState(f.ownerCtx(), root.ID)

	c := gsContext(t, gs, child.ID)
	assert.Equal(t, engine.ContextKindSpawn, c.Kind)
	assert.True(t, c.Blocking, "an awaited, still-active spawn child blocks its parent")
	assert.NotNil(t, c.Graph)
	assert.Nil(t, c.GroupRunID)
	nodes := gsNodeMap(gs)
	assert.Equal(t, "waiting for child run", nodes[gsKey(root.ID, "s")].Reason)
	assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(child.ID, "c1")].State)
}

// ---------------------------------------------------------------------------
// Authorization and redaction (error cases)
// ---------------------------------------------------------------------------

func TestGraphState_RedactsDescendantWhoseChildWorkflowVisibilityDiffers(t *testing.T) {
	f := newGSFixture(t)
	viewer := uuid.New()
	root := gsRootInstance(f.owner, "fan-workflows", gsSnapshot(t, gsWorkflowsWF("child-wf", "secret-wf")), instance.StateWaiting, "w")
	root.Visibility = instance.VisibilityPublic
	f.add(root)
	runID := uuid.New()
	w1 := gsGroupTask(root, "w", runID, "t1", instance.GroupTaskKindWorkflow, "child-wf",
		gsSnapshot(t, gsChildWF("child-wf", "c2")), instance.StateRunning, "c1")
	w1.Visibility = instance.VisibilityPublic
	f.add(w1)
	// Same owner as the parent, but the child workflow is PRIVATE: shared
	// ownership is not a visibility decision.
	w2 := gsGroupTask(root, "w", runID, "t2", instance.GroupTaskKindWorkflow, "secret-wf",
		gsSnapshot(t, gsChildWF("secret-wf", "classified-step")), instance.StateRunning, "c1")
	w2.Visibility = instance.VisibilityPrivate
	f.add(w2)
	w2c := gsRootInstance(f.owner, "deeper", gsSnapshot(t, gsChildWF("deeper", "c2")), instance.StateRunning, "c1")
	w2pid := w2.ID
	w2c.ParentInstanceID = &w2pid
	w2c.ParentNodeID = gsStr("c1")
	w2c.SpawnMode = instance.SpawnModeFireAndForget
	f.add(w2c)
	f.addRun(gsRun(runID, root, "w", workflow.GroupFormWorkflows, instance.GroupRunRunning,
		gsRosterTask(0, "t1", instance.GroupTaskKindWorkflow, "child-wf", w1.ID, instance.GroupTaskStatusRunning),
		gsRosterTask(1, "t2", instance.GroupTaskKindWorkflow, "secret-wf", w2.ID, instance.GroupTaskStatusRunning)))

	gs := f.graphState(f.as(viewer), root.ID)

	require.Len(t, gs.Contexts, 3, "root, visible task, redacted task — nothing beneath the redacted one")
	visible := gsContext(t, gs, w1.ID)
	assert.False(t, visible.Redacted)
	assert.NotNil(t, visible.Graph)

	hidden := gsContextByTask(t, gs, "t2")
	assert.True(t, hidden.Redacted)
	assert.Equal(t, uuid.Nil, hidden.InstanceID)
	assert.Equal(t, engine.LiveStateUnknown, hidden.State, "a hidden descendant's state is not disclosed")
	assert.Equal(t, engine.ContextKindWorkflow, hidden.Kind)
	assert.Empty(t, hidden.Nodes)
	assert.Nil(t, hidden.Graph)
	assert.Nil(t, hidden.Layout)
	assert.Empty(t, hidden.WorkflowName)
	require.NotNil(t, hidden.GroupRunID, "the parent's own group id stays as the generic hidden-dependency anchor")
	assert.Equal(t, runID, *hidden.GroupRunID)
	assert.True(t, hidden.Blocking)

	assert.False(t, gsContainsID(f.execs.reads, w2.ID), "no trace read for an inaccessible descendant")
	assert.False(t, gsContainsID(f.links.reads, w2.ID), "traversal stops at an inaccessible descendant")
	assert.False(t, f.viewer.wasAsked(w2c.ID))

	body, err := json.Marshal(gs)
	require.NoError(t, err)
	assert.NotContains(t, string(body), w2.ID.String())
	assert.NotContains(t, string(body), w2c.ID.String())
	assert.NotContains(t, string(body), "classified-step", "the hidden child's definition never reaches the wire")
	assert.Empty(t, gs.Warnings, "an authorization redaction is not a warning")

	// The owner sees the same task in full.
	owner := f.graphState(f.ownerCtx(), root.ID)
	assert.False(t, gsContext(t, owner, w2.ID).Redacted)
}

func TestGraphState_UnauthorizedRootIsNotFoundWithoutReads(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")

	gs, err := f.reader.GraphState(f.as(uuid.New()), fo.root.ID)

	require.Error(t, err)
	assert.True(t, errors.Is(err, instance.ErrNotFound), "an unauthorized root is indistinguishable from an absent one")
	assert.Nil(t, gs)
	assert.Empty(t, f.execs.reads)
	assert.Empty(t, f.links.reads)
	assert.Empty(t, f.groups.reads)

	_, err = f.reader.GraphState(f.ownerCtx(), uuid.New())
	assert.True(t, errors.Is(err, instance.ErrNotFound))
}

func TestGraphState_UnreadableDescendantIsUnknownWithWarning(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")
	f.store.getErr[fo.b.ID] = errors.New("database is locked")

	gs := f.graphState(f.ownerCtx(), fo.root.ID)

	c := gsContextByTask(t, gs, "b")
	assert.True(t, c.Redacted)
	assert.Equal(t, uuid.Nil, c.InstanceID)
	assert.Equal(t, engine.LiveStateUnknown, c.State, "an unreadable descendant is unknown, never pending or completed")
	require.Len(t, gs.Warnings, 1)
	assert.Equal(t, "a descendant run could not be read", gs.Warnings[0])
	for _, w := range gs.Warnings {
		assert.NotContains(t, w, fo.b.ID.String())
	}
	assert.True(t, gs.Active, "the visible running branch keeps the view active")
}

func TestGraphState_SelectedBranchRunShowsItsBodyAndHidesUnviewableParentLink(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")
	f.mutate(fo.a.ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPublic })

	gs := f.graphState(f.as(uuid.New()), fo.a.ID)

	require.Len(t, gs.Contexts, 1)
	c := gs.Contexts[0]
	assert.Equal(t, engine.ContextKindRoot, c.Kind)
	assert.Equal(t, []string{"a1", "a2"}, gsNodeIDs(c), "a selected branch run shows its own body")
	assert.NotNil(t, c.Graph, "the selected run always carries a graph")
	assert.Nil(t, c.ParentInstanceID, "a parent the caller may not view is not linked")
	assert.Nil(t, c.GroupRunID)
	assert.Empty(t, c.TaskKey)

	owner := f.graphState(f.ownerCtx(), fo.a.ID)
	require.NotNil(t, owner.Contexts[0].ParentInstanceID)
	assert.Equal(t, fo.root.ID, *owner.Contexts[0].ParentInstanceID)
	assert.Equal(t, "a", owner.Contexts[0].TaskKey)
}

func TestGraphState_ChildListingErrorWarnsAndKeepsRoot(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")
	f.links.errs[fo.root.ID] = errors.New("disk I/O error")

	gs := f.graphState(f.ownerCtx(), fo.root.ID)

	require.Len(t, gs.Contexts, 1)
	assert.Contains(t, gs.Warnings, fmt.Sprintf("child runs of run %s could not be listed", fo.root.ID))
}

func TestGraphState_TraceReadErrorMarksUnreachedNodesUnknown(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")
	f.execs.errs[fo.a.ID] = errors.New("disk I/O error")

	gs := f.graphState(f.ownerCtx(), fo.root.ID)
	nodes := gsNodeMap(gs)

	assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(fo.a.ID, "a1")].State, "the cursor is known without the trace")
	assert.Equal(t, engine.LiveStateUnknown, nodes[gsKey(fo.a.ID, "a2")].State, "without a trace a non-current node is unknown, not pending")
	assert.Contains(t, gs.Warnings, fmt.Sprintf("execution history of run %s could not be read", fo.a.ID))
}

// ---------------------------------------------------------------------------
// Activity and version
// ---------------------------------------------------------------------------

func TestGraphState_ActiveOnlyCountsVisibleContexts(t *testing.T) {
	t.Run("terminal parent with a running visible descendant is active", func(t *testing.T) {
		f := newGSFixture(t)
		fo := f.fanout(instance.StateCompleted, instance.StateRunning, "a1", "b1")
		f.mutate(fo.root.ID, func(i *instance.Instance) { i.State = instance.StateCompleted })
		assert.True(t, f.graphState(f.ownerCtx(), fo.root.ID).Active)
	})
	t.Run("a running hidden descendant does not make the view active", func(t *testing.T) {
		f := newGSFixture(t)
		fo := f.fanout(instance.StateCompleted, instance.StateRunning, "a1", "b1")
		f.mutate(fo.root.ID, func(i *instance.Instance) {
			i.State = instance.StateCompleted
			i.Visibility = instance.VisibilityPublic
		})
		f.mutate(fo.a.ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPublic })
		gs := f.graphState(f.as(uuid.New()), fo.root.ID)
		assert.True(t, gsContextByTask(t, gs, "b").Redacted)
		assert.False(t, gs.Active)
	})
	t.Run("everything terminal is inactive", func(t *testing.T) {
		f := newGSFixture(t)
		fo := f.fanout(instance.StateCompleted, instance.StateFailed, "a1", "b1")
		f.mutate(fo.root.ID, func(i *instance.Instance) { i.State = instance.StateCompleted })
		assert.False(t, f.graphState(f.ownerCtx(), fo.root.ID).Active)
	})
}

func TestGraphState_VersionIsStableAndChangesWithState(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")

	v1 := f.graphState(f.ownerCtx(), fo.root.ID).Version
	v2 := f.graphState(f.ownerCtx(), fo.root.ID).Version
	assert.Equal(t, v1, v2, "identical state hashes identically")
	assert.Len(t, v1, 32)

	f.mutate(fo.a.ID, func(i *instance.Instance) { i.CurrentNodeID = gsStr("a2") })
	f.exec(fo.a, "a1", instance.NodeStatusSucceeded, "")
	v3 := f.graphState(f.ownerCtx(), fo.root.ID).Version
	assert.NotEqual(t, v1, v3, "a node transition changes the version")
}

// ---------------------------------------------------------------------------
// Provenance
// ---------------------------------------------------------------------------

func TestGraphState_DescendantsComeOnlyFromLinks(t *testing.T) {
	f := newGSFixture(t)
	root := f.add(gsRootInstance(f.owner, "spawner", gsSnapshot(t, gsSpawnerWF()), instance.StateWaiting, "s"))
	orphan := gsRootInstance(f.owner, "child-wf", gsSnapshot(t, gsChildWF("child-wf", "c2")), instance.StateRunning, "c1")
	pid := root.ID
	orphan.ParentInstanceID = &pid
	orphan.ParentNodeID = gsStr("s")
	orphan.SpawnMode = instance.SpawnModeWaitForChild
	// Stored but deliberately NOT linked: the link repository is the only
	// producer of descendants.
	f.store.rows[orphan.ID] = orphan

	gs := f.graphState(f.ownerCtx(), root.ID)

	require.Len(t, gs.Contexts, 1)
	assert.True(t, gsContainsID(f.links.reads, root.ID), "descendants are discovered through ListChildren")
	assert.False(t, f.viewer.wasAsked(orphan.ID))
}

func TestNewGraphStateReader_PanicsOnNilDependency(t *testing.T) {
	f := newGSFixture(t)
	assert.Panics(t, func() { engine.NewGraphStateReader(nil, f.execs, f.groups, f.links) })
	assert.Panics(t, func() { engine.NewGraphStateReader(f.viewer, nil, f.groups, f.links) })
	assert.Panics(t, func() { engine.NewGraphStateReader(f.viewer, f.execs, nil, f.links) })
	assert.Panics(t, func() { engine.NewGraphStateReader(f.viewer, f.execs, f.groups, nil) })
}

// ---------------------------------------------------------------------------
// Wire shape
// ---------------------------------------------------------------------------

func TestGraphStateReader_MoreThan1000ContextsKeepsLastActiveTask(t *testing.T) {
	for _, independent := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent=%t", independent), func(t *testing.T) {
			f := newGSFixture(t)
			const childCount = 1001
			wf := gsWorkflowsWF("child-wf", "child-wf")
			tasks := make([]workflow.GroupTask, childCount)
			for i := range tasks {
				tasks[i] = workflow.GroupTask{Key: fmt.Sprintf("task-%d", i), Workflow: "child-wf"}
			}
			wf.Nodes[1] = gsGroupNode("w", workflow.GroupFormWorkflows, tasks...)
			root := f.add(gsRootInstance(f.owner, wf.Name, gsSnapshot(t, wf), instance.StateCompleted, "done"))
			runID := uuid.New()
			childSnap := gsSnapshot(t, gsChildWF("child-wf", "c2"))
			roster := make([]instance.GroupTask, childCount)
			var last *instance.Instance
			for i, task := range tasks {
				child := f.add(gsGroupTask(root, "w", runID, task.Key, instance.GroupTaskKindWorkflow,
					"child-wf", childSnap, instance.StateCompleted, "c2"))
				f.exec(child, "c1", instance.NodeStatusSucceeded, "")
				f.exec(child, "c2", instance.NodeStatusSucceeded, "")
				roster[i] = gsRosterTask(i, task.Key, instance.GroupTaskKindWorkflow, "child-wf", child.ID, instance.GroupTaskStatusCompleted)
				last = child
			}
			f.mutate(last.ID, func(i *instance.Instance) { i.State = instance.StateRunning })
			roster[childCount-1].LiveStatus = instance.GroupTaskStatusRunning
			roster[childCount-1].Blocking = !independent
			roster[childCount-1].ContinuingIndependently = independent
			f.addRun(gsRun(runID, root, "w", workflow.GroupFormWorkflows, instance.GroupRunDone, roster...))

			gs := f.graphState(f.ownerCtx(), root.ID)
			require.Len(t, gs.Contexts, childCount+1)
			nodes := gsNodeMap(gs)
			for i, task := range roster {
				assert.Equal(t, task.ChildInstanceID, gs.Contexts[i+1].InstanceID)
				assert.True(t, f.viewer.wasAsked(task.ChildInstanceID))
				assert.Contains(t, nodes, gsKey(task.ChildInstanceID, "c1"))
				assert.Contains(t, nodes, gsKey(task.ChildInstanceID, "c2"))
			}
			assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(last.ID, "c2")].State)
			assert.Equal(t, !independent, gs.Contexts[childCount].Blocking)
			assert.Equal(t, independent, f.groups.byParent[root.ID][0].Tasks[childCount-1].ContinuingIndependently)
			assert.True(t, gs.Active)
			assert.Empty(t, gs.Warnings)

			f.mutate(last.ID, func(i *instance.Instance) { i.State = instance.StateCompleted })
			final := f.graphState(f.ownerCtx(), root.ID)
			require.Len(t, final.Contexts, childCount+1)
			assert.Equal(t, engine.LiveStateCompleted, gsNodeMap(final)[gsKey(last.ID, "c2")].State)
			assert.False(t, final.Active)
			assert.NotEqual(t, gs.Version, final.Version)
		})
	}
}

func TestGraphStateReader_TransientReadFailureKeepsRefreshingUntilRecovery(t *testing.T) {
	for _, source := range []string{"descendant", "children", "groups"} {
		t.Run(source, func(t *testing.T) {
			f := newGSFixture(t)
			fo := f.fanout(instance.StateCompleted, instance.StateCompleted, "a1", "b1")
			f.mutate(fo.root.ID, func(i *instance.Instance) { i.State = instance.StateCompleted })
			assert.False(t, f.graphState(f.ownerCtx(), fo.root.ID).Active)
			var errs map[uuid.UUID]error
			id := fo.root.ID
			switch source {
			case "descendant":
				errs, id = f.store.getErr, fo.b.ID
			case "children":
				errs = f.links.errs
			case "groups":
				errs = f.groups.errs
			}
			errs[id] = errors.New("temporary storage failure")
			unavailable := f.graphState(f.ownerCtx(), fo.root.ID)
			assert.True(t, unavailable.Active, "a terminal prefix does not prove subtree inactivity")
			assert.NotEmpty(t, unavailable.Warnings)
			if source == "descendant" {
				c := gsContextByTask(t, unavailable, "b")
				assert.True(t, c.Redacted)
				assert.Equal(t, engine.LiveStateUnknown, c.State)
			}

			delete(errs, id)
			f.mutate(fo.b.ID, func(i *instance.Instance) { i.State = instance.StateRunning })
			recovered := f.graphState(f.ownerCtx(), fo.root.ID)
			assert.Empty(t, recovered.Warnings)
			assert.True(t, recovered.Active)
			assert.Equal(t, engine.LiveStateRunning, gsNodeMap(recovered)[gsKey(fo.b.ID, "b1")].State)
			f.mutate(fo.b.ID, func(i *instance.Instance) { i.State = instance.StateCompleted })
			assert.False(t, f.graphState(f.ownerCtx(), fo.root.ID).Active)
		})
	}
}

func TestGraphStateReader_CyclesAndDepthGuardDoNotReportFalseInactivity(t *testing.T) {
	t.Run("cycle visits each instance once", func(t *testing.T) {
		f := newGSFixture(t)
		fo := f.fanout(instance.StateCompleted, instance.StateRunning, "a1", "b1")
		f.links.children[fo.a.ID] = []instance.ChildRef{{ID: fo.root.ID}, {ID: fo.b.ID}}
		gs := f.graphState(f.ownerCtx(), fo.root.ID)
		require.Len(t, gs.Contexts, 3)
		assert.True(t, gs.Active)
		assert.Empty(t, gs.Warnings)
	})
	t.Run("maximum legal depth is included but deeper traversal stays unknown", func(t *testing.T) {
		f := newGSFixture(t)
		snap := gsSnapshot(t, gsChildWF("child-wf", "c2"))
		root := f.add(gsRootInstance(f.owner, "child-wf", snap, instance.StateCompleted, "c2"))
		parent := root
		for depth := 1; depth <= instance.MaxSpawnDepth; depth++ {
			child := gsRootInstance(f.owner, "child-wf", snap, instance.StateCompleted, "c2")
			pid := parent.ID
			child.ParentInstanceID, child.SpawnDepth = &pid, depth
			parent = f.add(child)
		}
		legal := f.graphState(f.ownerCtx(), root.ID)
		require.Len(t, legal.Contexts, instance.MaxSpawnDepth+1)
		assert.Empty(t, legal.Warnings)
		assert.False(t, legal.Active)

		tooDeep := gsRootInstance(f.owner, "child-wf", snap, instance.StateRunning, "c1")
		pid := parent.ID
		tooDeep.ParentInstanceID = &pid
		f.add(tooDeep)
		bounded := f.graphState(f.ownerCtx(), root.ID)
		require.Len(t, bounded.Contexts, instance.MaxSpawnDepth+1)
		assert.False(t, f.viewer.wasAsked(tooDeep.ID), "the depth guard prevents deeper reads")
		assert.NotEmpty(t, bounded.Warnings)
		assert.True(t, bounded.Active, "a depth-limited prefix cannot prove inactivity")
	})
}

func TestGraphStateReader_EngineAndSQLiteProduceAggregateSink(t *testing.T) {
	ctx := context.Background()
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	instances := adaptsqlite.NewInstanceRepo(db)
	execs := adaptsqlite.NewNodeExecutionRepo(db)
	groups := adaptsqlite.NewGroupRunRepo(db)
	users := adaptsqlite.NewUserRepo(db)
	workflows := newMockWorkflowRepo()
	bus, qm, script := newMockEventBus(), newMockQueueManager(), newMockExecutor()
	eng := engine.NewEngine(workflows, instances, execs, adaptsqlite.NewVariableSnapshotRepo(db), users,
		map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: script}, bus, qm, adaptsqlite.NewQueueRepo(db), "")
	t.Cleanup(eng.Shutdown)
	qm.enqueueFn = func(ctx context.Context, id, _ uuid.UUID) (bool, error) {
		err := eng.StartInstance(ctx, id)
		return err == nil, err
	}
	ids := fixedIdentityResolver{}
	launcher := engine.NewGroupLauncher(groups, eng, eng, nil, nil, ids, eng, bus)
	coord := engine.NewGroupCoordinator(groups, instances, adaptsqlite.NewQueueRepo(db), eng, ids, bus, qm)
	eng.SetGroupTerminalHandler(coord)
	eng.RegisterExecutor(workflow.NodeTypeParallelGroup,
		engine.NewParallelGroupExecutor(instances, execs, groups, launcher, coord, instances, qm))
	eng.SetGroupSupport(engine.GroupSupportResults)
	eng.FreezeExecutors()
	reader := engine.NewGraphStateReader(eng, execs, groups, instances)
	wf := gxWorkflow()
	workflows.add(&wf)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	for _, id := range []string{"a1", "b1"} {
		script.onNode(id, func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
			<-release
			return &engine.NodeResult{Output: map[string]any{"produced": "actual child output"}}, nil
		})
	}
	owner, err := users.GetDefaultUser(ctx)
	require.NoError(t, err)
	parentID, _, err := eng.Trigger(ctxAs(owner), wf.Name, nil, "normal")
	require.NoError(t, err)
	gxWaitForInstance(t, instances, parentID, instance.StateWaiting)
	run, err := groups.GetByParentNode(ctx, parentID, "g")
	require.NoError(t, err)
	require.NotNil(t, run)
	for _, task := range run.Tasks {
		gxWaitForInstance(t, instances, task.ChildInstanceID, instance.StateRunning)
	}
	live, err := reader.GraphState(ctxAs(owner), parentID)
	require.NoError(t, err)
	require.Len(t, live.Contexts, 3)
	assert.True(t, live.Active)
	assert.Empty(t, live.Warnings)
	require.Len(t, live.Groups, 1)
	livePanel := live.Groups[0]
	assert.Equal(t, run.ID, livePanel.GroupRunID)
	assert.Equal(t, workflow.GroupRuleAllSucceed, livePanel.CompletionRule)
	assert.Equal(t, []string{"waiting_for_tasks:a,b"}, livePanel.WaitingReasons)
	assert.Equal(t, engine.GroupProgress{Total: 2, Active: 2}, livePanel.Progress)
	for i, task := range run.Tasks {
		card := livePanel.Tasks[i]
		assert.Equal(t, task.Key, card.Key)
		require.NotNil(t, card.ChildInstanceID)
		assert.Equal(t, task.ChildInstanceID, *card.ChildInstanceID)
		assert.Equal(t, "running", card.LiveStatus)
		assert.True(t, card.Blocking)
	}
	nodes := gsNodeMap(live)
	assert.Equal(t, engine.LiveStateWaiting, nodes[gsKey(parentID, "g")].State)
	assert.Equal(t, engine.LiveStateCompleted, nodes[gsKey(parentID, "init")].State)
	for _, task := range run.Tasks {
		c := gsContext(t, live, task.ChildInstanceID)
		assert.Equal(t, task.Key, c.TaskKey)
		assert.True(t, c.Blocking)
		assert.Equal(t, []string{task.Origin}, gsNodeIDs(c))
		assert.Equal(t, engine.LiveStateRunning, nodes[gsKey(task.ChildInstanceID, task.Origin)].State)
	}

	once.Do(func() { close(release) })
	for _, task := range run.Tasks {
		gxWaitForInstance(t, instances, task.ChildInstanceID, instance.StateCompleted)
	}
	require.NoError(t, coord.Reconcile(ctx))
	gxWaitForInstance(t, instances, parentID, instance.StateQueued)
	require.NoError(t, eng.StartInstance(ctx, parentID))
	gxWaitForInstance(t, instances, parentID, instance.StateCompleted)
	final, err := reader.GraphState(ctxAs(owner), parentID)
	require.NoError(t, err)
	require.Len(t, final.Contexts, 3)
	assert.False(t, final.Active)
	assert.NotEqual(t, live.Version, final.Version)
	assert.Empty(t, final.Warnings)
	require.Len(t, final.Groups, 1)
	finalPanel := final.Groups[0]
	assert.Equal(t, string(instance.GroupRunDone), finalPanel.State)
	assert.Equal(t, engine.GroupProgress{Total: 2, Completed: 2}, finalPanel.Progress)
	assert.Empty(t, finalPanel.WaitingReasons)
	require.NotNil(t, finalPanel.Decision)
	for i, task := range run.Tasks {
		assert.Equal(t, "completed", finalPanel.Tasks[i].LiveStatus)
		assert.Equal(t, "completed", finalPanel.Tasks[i].FrozenStatus)
		result := finalPanel.FinalOutput["byKey"].(map[string]any)[task.Key].(map[string]any)["result"].(map[string]any)
		assert.Equal(t, "actual child output", result[task.Origin].(map[string]any)["produced"])
		assert.Equal(t, "actual child output", finalPanel.Decision.Tasks[i].Result[task.Origin].(map[string]any)["produced"])
	}
	for _, task := range run.Tasks {
		n := gsNodeMap(final)[gsKey(task.ChildInstanceID, task.Origin)]
		assert.Equal(t, engine.LiveStateCompleted, n.State)
		assert.Equal(t, 1, n.Attempt, "the sink contains the engine's persisted execution record")
	}
}

func TestGraphState_JSONFieldNames(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")
	gs := f.graphState(f.ownerCtx(), fo.root.ID)

	b, err := json.Marshal(gs)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	for _, k := range []string{"rootInstanceId", "version", "active", "contexts", "groups", "layout"} {
		assert.Contains(t, m, k)
	}
	ctxs := m["contexts"].([]any)
	branch := ctxs[1].(map[string]any)
	for _, k := range []string{"instanceId", "parentInstanceId", "groupRunId", "taskKey", "kind", "workflowName", "state", "blocking", "redacted", "nodes"} {
		assert.Contains(t, branch, k)
	}
	assert.NotContains(t, branch, "graph")
	node := branch["nodes"].([]any)[0].(map[string]any)
	assert.Equal(t, fo.a.ID.String(), node["instanceId"])
	assert.Equal(t, "a1", node["nodeId"])
	assert.Equal(t, "running", node["state"])
	assert.True(t, strings.HasPrefix(string(b), "{"))
}
