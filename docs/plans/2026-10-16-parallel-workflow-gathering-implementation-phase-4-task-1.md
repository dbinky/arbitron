# Parallel Workflow Gathering — Phase 4 Task 1: GraphStateReader aggregate read model

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the run views ONE authorized read that returns the selected run's graph plus every descendant execution context (branch tasks, workflow tasks and legacy spawn children, recursively), with each live node identified by `(instanceID, nodeID)` and never by node id alone.
**Architecture:** A new domain read service `engine.GraphStateReader` (file `domain/engine/graph_state.go`) authorizes the root and EACH descendant through `InstanceViewer.GetInstance` (production: `*engine.Engine`), walks descendants through `SpawnLinkRepository.ListChildren`, derives node states from the owning instance's state/cursor combined with its latest `NodeExecution` per node, redacts inaccessible descendants, and returns a `GraphState` with a stable `Version` hash and an `Active` flag computed over visible contexts, conservatively true when a storage error prevents proving inactivity. Group panels are produced by P4-T2 through a hook (`groups := []GroupPanel{}`) this task leaves in place.
**Tech Stack:** Go 1.25.5, standard library (`crypto/sha256`, `encoding/hex`, `encoding/json`), testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("The queue's live run view reflects every concurrently active node across all fanned-out paths", "Task states distinguish queued, running, waiting, completed, failed, and cancelled work"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §1 "Authorized aggregate read model" and Data Provenance row "Aggregate state, namespaced node identities, subtree activity and version" (PLANNED DEPENDENCY P4-A — resolved here for the read service; the endpoint is P4-T3).

## Required complete-roster TDD cycle

The spec puts no 1,000-context limit on a fixed group. Add
`TestGraphStateReader_MoreThan1000ContextsKeepsLastActiveTask`: a terminal root,
1,001 visible children, and only the LAST child running. Assert every authorized child
and its qualified node state is present, `Active == true`, and the last task remains
blocking/independent as persisted. Repeat after that child completes: `Active == false`.
Observe red with the old count cutoff, remove that cutoff, and retain visited-ID and
`MaxSpawnDepth` cycle/depth checks. P4-T5 exercises the same large subtree over the real
router and must keep the socket open until the last task finishes; P4-T10 inspects the
last task after hierarchy pagination. No warning-plus-truncation may report inactivity.


## Required transient-read recovery TDD cycle

Add a terminal root with its last child's lookup (and separately child-list/group-record
read) temporarily failing. The response shows unknown/unavailable and a warning and
keeps `Active=true` for resync; it must not close the subtree socket or disable polling.
Clear the injected error: a running child appears on the next fetch, and a completed
child permits `Active=false`. An authorization-denied child alone does NOT set this
uncertainty flag or reveal hidden activity. P4-T5/P4-T6 drive the same case through the
socket and poll sinks. This refines `active` as a refresh-liveness signal when storage
is unavailable; individual task states still honestly say unknown.


**Interfaces**
- Consumes (existing code):
  - `domain/engine/engine.go:658-672` `func (e *Engine) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error)` — returns an error wrapping `instance.ErrNotFound` for both an absent row and a row the caller may not view (`canView`, `domain/engine/authz.go:14-26`); infra errors pass through unwrapped.
  - `domain/engine/engine.go:714-720` `Engine.GetTrace` — the trace-access rule this reader reproduces: authorize the instance first, then `NodeExecutionRepository.ListByInstance`.
  - `domain/engine/engine.go:1288-1312` `func parseWorkflowGraph(snapshot map[string]any) (*workflow.Workflow, map[string]workflow.Node, map[string][]edgeTarget, error)` — snapshot → definition.
  - `domain/instance/ports.go:58-69` `NodeExecutionRepository.ListByInstance` (sqlite orders by `started_at`, `adapter/sqlite/queries/node_executions.sql:10-11`).
  - `domain/instance/instance.go:43-49` `State.IsTerminal`; `:230-258` `NodeExecution`, `NodeStatusSucceeded/Failed/Skipped`.
  - `domain/workflow/workflow.go` `workflow.IsInteractive(NodeType) bool` (used by `adapter/http/instance_handler.go:158-164`), node type constants.
- Consumes (contract, earlier tasks):
  - P1-T1/P1-T2: `workflow.NodeTypeParallelGroup`, `workflow.AnalyzeGroups(wf *Workflow) (*GroupGraph, []ValidationError)`, `workflow.GroupGraph{Groups, Owner, Order}`, `workflow.BodyOwner{GroupNodeID, TaskKey}`, `workflow.GroupConfig`, `workflow.GroupTask`, `workflow.GroupForm*`, `GroupRule*`, `GroupUnfinished*`, `GroupGather*`, `GroupPort*`.
  - P2-T1: `instance.Instance.GroupRunID *uuid.UUID`, `GroupTaskKey *string`, `GroupTaskKind instance.GroupTaskKind`; `instance.GroupTaskKindBranch/Workflow`; `instance.SpawnModeGroupTask`; `instance.GroupRun`, `instance.GroupTask`, `instance.GroupRunRepository.ListByParent(ctx, parentID) ([]GroupRun, error)`; `instance.GroupTaskStatus*`, `instance.GroupTaskStatusFromState(State) GroupTaskStatus`, `instance.GroupTaskLaunched`, `instance.GroupDecision`.
  - P2-T2: a group-task child row carries `parent_instance_id` AND `parent_node_id` = the group node id (so `Instance.ParentNodeID` names the owning group node).
  - P2-T12: `instance.ChildRef{ID uuid.UUID; State State}`, `SpawnLinkRepository.ListChildren(ctx, parentID uuid.UUID) ([]ChildRef, error)` (ALL children incl. terminal, created_at order).
  - P3-T4: `instance.GatherReport`, `instance.ResolutionSessionRef` (type references in `GroupPanel`).
- Produces (exact; JSON field names are the wire contract):
  - Constants: `LiveStatePending="pending"`, `LiveStateQueued="queued"`, `LiveStateRunning="running"`, `LiveStateWaiting="waiting"`, `LiveStatePaused="paused"`, `LiveStateCompleted="completed"`, `LiveStateFailed="failed"`, `LiveStateSkipped="skipped"`, `LiveStateCancelled="cancelled"`, `LiveStateUnknown="unknown"`; `ContextKindRoot="root"`, `ContextKindBranch="branch"`, `ContextKindWorkflow="workflow"`, `ContextKindSpawn="spawn"`.
  - `type NodeStateKey struct { InstanceID uuid.UUID "instanceId"; NodeID string "nodeId" }`
  - `type LiveNodeState struct { InstanceID "instanceId"; NodeID "nodeId"; State "state"; Reason "reason,omitempty"; Attempt "attempt,omitempty" }` + `func (s LiveNodeState) Key() NodeStateKey`
  - `type ExecutionContext struct { InstanceID "instanceId"; ParentInstanceID *uuid.UUID "parentInstanceId,omitempty"; GroupRunID *uuid.UUID "groupRunId,omitempty"; TaskKey "taskKey,omitempty"; Kind "kind"; WorkflowName "workflowName,omitempty"; State "state"; Blocking "blocking"; Redacted "redacted"; Nodes []LiveNodeState "nodes"; Graph map[string]any "graph,omitempty"; Layout *workflow.GroupGraph "layout,omitempty" }`
  - `type GroupPanel struct { GroupRunID "groupRunId"; InstanceID "instanceId"; NodeID "nodeId"; Form "form"; CompletionRule "completionRule"; UnfinishedBehavior "unfinishedBehavior"; GatherMode "gatherMode"; State "state"; Tasks []GroupPanelTask "tasks"; Progress GroupProgress "progress"; WaitingReasons []string "waitingReasons"; Winner "winner,omitempty"; Decision *instance.GroupDecision "decision,omitempty"; FinalOutput map[string]any "finalOutput,omitempty"; Gather *instance.GatherReport "gather,omitempty"; ActiveSession *instance.ResolutionSessionRef "activeSession,omitempty" }`
  - `type GroupPanelTask struct { Key "key"; Kind "kind"; Origin "origin"; ChildInstanceID *uuid.UUID "childInstanceId,omitempty"; LiveStatus "liveStatus"; FrozenStatus "frozenStatus" ("" until decided); Blocking "blocking"; ContinuingIndependently "continuingIndependently"; CancelRequested "cancelRequested"; LaunchError "launchError,omitempty"; Redacted "redacted" }`
  - `type GroupProgress struct { Total "total"; Completed "completed"; Failed "failed"; Cancelled "cancelled"; Active "active"; NotStarted "notStarted" }`
  - `type GraphState struct { RootInstanceID "rootInstanceId"; Version "version"; Active "active"; Contexts []ExecutionContext "contexts"; Groups []GroupPanel "groups"; Layout *workflow.GroupGraph "layout,omitempty"; Warnings []string "warnings,omitempty" }`
  - `type InstanceViewer interface { GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) }`
  - `func NewGraphStateReader(viewer InstanceViewer, nodeExecs instance.NodeExecutionRepository, groups instance.GroupRunRepository, links instance.SpawnLinkRepository) *GraphStateReader` (panics on any nil dependency)
  - `func (r *GraphStateReader) GraphState(ctx context.Context, id uuid.UUID) (*GraphState, error)`
  - Unexported hook consumed by P4-T2: `graphTraversal` (fields `contexts`, `entries`, `visible`, `hidden`, `warnings`, method `warn`) and `graphEntry` (fields `index`, `inst`, `wf`, `gg`, `nodeSet`, `runs`, `runsErr`, `runsRead`).
  - Semantics later tasks rely on: an unauthorized/absent root returns an error wrapping `instance.ErrNotFound` and performs NO repository read beyond the viewer; contexts are ordered root first then depth-first pre-order in `ListChildren` order; node lists follow `wf.Nodes` order; `Version` is 32 lowercase hex chars, identical for identical inputs; `Active` is true iff some non-redacted context is `queued|running|waiting|paused`.

**Contract refinements (backward-compatible, reported):** `ExecutionContext.Layout` (ownership analysis for workflow/spawn contexts that have their own graph), `GroupPanel.ActiveSession`, JSON tags for `GroupPanel`/`GroupPanelTask`/`GroupProgress`, and `LiveNodeState.Key()`. A redacted descendant's context `State` is `"unknown"` (matching the dashboard's `InstanceState | 'unknown'` union, P4-T6); `Redacted=true` marks it as withheld, and only the unreadable case adds a warning.

**Wiring & data provenance**
- Composition root: P4-T3 constructs `engine.NewGraphStateReader(eng, nodeExecRepo, groupRunRepo, instanceRepo)` in `cmd/arbitron/main.go:initDomain` and serves it at `GET /api/v1/instances/{id}/graph-state`; P4-T5 also uses it for subtree stream activity.
- Producers:
  - `viewer` ← `cmd/arbitron/main.go:800` `eng := engine.NewEngine(...)` (`*engine.Engine` satisfies `InstanceViewer` via `engine.go:658 GetInstance`).
  - `nodeExecs` ← `cmd/arbitron/main.go:376` `nodeExecRepo := adaptsqlite.NewNodeExecutionRepo(db)`; rows written by `domain/engine/engine.go:1131-1204 executeWithRetry` (and by wait executors that write their own provisional succeeded record, e.g. P2-T11 `ParallelGroupExecutor`).
  - `groups` ← P2-T3 `adapter/sqlite/group_run_repo.go` repo built by P2-T14 in `initDomain`; rows written by P2-T8 `GroupLauncher.Launch` / P2-T10 `GroupCoordinator`.
  - `links` ← `cmd/arbitron/main.go:373` `instanceRepo := adaptsqlite.NewInstanceRepo(db)` (`*sqlite.InstanceRepo` gains `ListChildren` in P2-T12); link columns written by `createInstanceSQL` (`adapter/sqlite/instance_repo.go:43`) for spawn children and by P2-T7 `Engine.StartGroupTask` for group tasks.
  - Instance state/cursor/owner/visibility ← `instance.Instance` via `Engine.GetInstance`; branch body membership ← `workflow.AnalyzeGroups` over the branch instance's own `WorkflowSnapshot` (a deep copy of the parent's, P2-T7) filtered by `BodyOwner{*inst.ParentNodeID, *inst.GroupTaskKey}`.
- Provenance probes: `TestGraphState_DescendantsComeOnlyFromLinks` (a child row that the link repository does not return never appears), `TestGraphState_AuthorizesRootAndEveryDescendant` (every included descendant passed through the viewer), `TestNewGraphStateReader_PanicsOnNilDependency`.

### Task 1: GraphStateReader
**Files:**
- Create: `domain/engine/graph_state.go`
- Test:   `domain/engine/graph_state_test.go` (package `engine_test`, like `domain/engine/engine_test.go`; it uses `newMockEventBus` from `engine_test.go:665-690`)

(No existing file is modified. All test helpers are prefixed `gs` to avoid collisions in the shared `engine_test` package.)

- [x] Step 1: Write the failing tests. Create `domain/engine/graph_state_test.go`:

```go
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

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
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
```

- [x] Step 2: Run it, confirm it fails.

Confirmed the specified focused command fails to compile: `InstanceViewer`, `GraphStateReader`, `GraphState`, and namespaced node-state types do not exist.

```bash
go test ./domain/engine/ -run 'TestGraphState|TestNewGraphStateReader' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/graph_state_test.go:50:15: undefined: engine.InstanceViewer
domain/engine/graph_state_test.go:139:15: undefined: engine.GraphStateReader
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Minimal implementation. Create `domain/engine/graph_state.go`:

```go
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
	nodeSet  map[string]bool    // definition nodes this context executes
	runs     []instance.GroupRun
	runsErr  error
	runsRead bool
}

type rosterMember struct {
	runID uuid.UUID
	task  instance.GroupTask
}

type graphTraversal struct {
	r           *GraphStateReader
	ctx         context.Context
	contexts    []ExecutionContext
	entries     []*graphEntry
	visible     map[uuid.UUID]*instance.Instance
	hidden      map[uuid.UUID]bool // redacted or unreadable descendants; never exposed
	uncertain   bool // storage/traversal failure: keep refreshing until activity is known
	visited     map[uuid.UUID]bool
	warnings    []string
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
		r:       r,
		ctx:     ctx,
		visible: map[uuid.UUID]*instance.Instance{},
		hidden:  map[uuid.UUID]bool{},
		visited: map[uuid.UUID]bool{root.ID: true},
	}
	rootEntry := t.addVisible(root, ContextKindRoot)
	t.hideUnviewableParentLink(rootEntry)
	t.walk(rootEntry, 1)

	groups := []GroupPanel{}
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

	if depth > instance.MaxSpawnDepth+1 {
		t.warn(fmt.Sprintf("descendants of run %s exceed the maximum nesting depth and are not shown", pid))
		return
	}
	refs, err := t.r.links.ListChildren(t.ctx, pid)
	if err != nil {
		t.uncertain = true
		t.warn(fmt.Sprintf("child runs of run %s could not be listed", pid))
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
	if t.uncertain { return true } // inactivity cannot be proven after a failed read
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
```

Design notes the implementer must keep (the tests pin them):
- Authorization happens BEFORE any trace/group/link read for an instance; `addVisible` is the only place `ListByInstance` is called.
- A redacted descendant is recorded with `uuid.Nil`, state `unknown`, no nodes/graph/layout/workflow name, and its subtree is NOT traversed. An `ErrNotFound` (or a `(nil, nil)`) from the viewer is an authorization redaction (no warning); any other error additionally adds the generic warning `"a descendant run could not be read"` (never the id).
- Branch body membership comes from `AnalyzeGroups.Owner` with `BodyOwner{*ParentNodeID, *GroupTaskKey}` (immediate ownership: a nested group node belongs to its enclosing task, its own bodies to the nested group's tasks).
- `groups := []GroupPanel{}` is the P4-T2 hook; keep it on its own line.

- [x] Step 4: Run the new tests, confirm they pass.

The focused tests pass, including the 1,001-child roster (both blocking and independent last-task metadata), transient descendant/list/group read recovery, and real Engine + SQLite producers driving the returned contexts/node states. A red depth-boundary regression exposed an off-by-one in the supplied implementation; traversal now includes the legal maximum depth and marks over-depth activity uncertain rather than inferring inactivity.

> RESOLVED (P4-T3, `8332f41` and `1374445`): `initDomain` now constructs `NewGraphStateReader(eng, nodeExecRepo, groupRunRepo, instanceRepo)` and passes it through `domainStack.routerDeps()` to the real HTTP route. `TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot` passes with `-race`, asserting concurrent namespaced branch cursors, parent waiting state, completion/activity changes, and actual tool outputs in the HTTP JSON sink. No production changes were needed in this completion pass.

Completion verification: focused reader tests passed both normally and with `-race`; `go test -race ./cmd/arbitron/ -run '^TestGraphStateE2E_' -count=1` and the full non-race engine package passed. Formatting, engine vet and diff checks were clean.

Completion self-review (in required order):
1. Spec compliance: checked the aggregate reader against this task's authorization, ordering, qualified node identity, complete-roster, transient-read liveness and depth contracts; the required implementation and production wiring are present. Added no features or downstream changes.
2. Anti-green-theater: reviewed the real composition-root HTTP sink assertions. Temporarily removed only `deps.GraphState` injection: the running-group root test failed at the HTTP sink (expected 200, got 404). Restored injection and reran the root race tests, focused reader race tests and full engine package successfully; no mutation remains.
3. Code quality: checked reader traversal/state derivation against neighboring authorization and HTTP error-handling conventions; request-local traversal and existing repository ports remain idiomatic. Formatting and vet are clean; no code cleanup was necessary.

Revision red evidence: before implementation the complete-roster and transient-read tests fail with the missing reader types. The supplied revised implementation already has no count cutoff and includes uncertainty propagation; behavioral mutation checks below additionally verify those protections are load-bearing.

```bash
go test ./domain/engine/ -run 'TestGraphState|TestNewGraphStateReader' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 5: Whole package, gofmt and vet.

Verification: `go test ./domain/engine/ -count=1` passes. Focused `TestGraphState|TestNewGraphStateReader` passes with `-race -count=3`; formatting, `go vet ./domain/engine/`, and `git diff --check` are clean.

Self-review (in required order):
1. Spec compliance: reviewed the read-model contract and Phase 4 design against the implementation. Kept P4-T2's empty panel hook and P4-T3's production wiring ownership; added no flags/options or downstream features. Fixed the supplied depth guard's off-by-one through a failing boundary test, retaining visited-ID protection and conservative liveness when traversal cannot finish.
2. Anti-green-theater: real Engine execution, StartGroupTask, launcher/coordinator and SQLite repositories produce the read model's nonempty namespaced contexts, live cursors, persisted attempts, completion states and activity/version changes. Tests key every repository lookup by the actual instance/parent. Temporarily restored a 1,000-context cutoff: both large-roster cases failed. Removed uncertainty from activity computation: all three transient-read cases failed. Restored both protections; no mutation remains. These checks do not substitute for the missing production-root sink, explicitly blocked in Step 4.
3. Code quality: checked neighboring executor/authorization/storage patterns, kept request-local traversal state, used the shared ownership analyzer, and formatted/vetted both new files. Reran the focused race tests and full engine package after review, green.

```bash
go test ./domain/engine/ -count=1
gofmt -w domain/engine/graph_state.go domain/engine/graph_state_test.go
gofmt -l domain/engine/graph_state.go domain/engine/graph_state_test.go
go vet ./domain/engine/
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 6: Commit.

The initial commit contained the unblocked read-model implementation, tests and production-root blocker. This completion pass closes Step 4 after P4-T3 supplied the real wiring, recording green verification and self-review without changing production behavior.

```bash
git add domain/engine/graph_state.go domain/engine/graph_state_test.go
git commit -m "feat(engine): authorized GraphStateReader aggregate read model for parallel runs" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
