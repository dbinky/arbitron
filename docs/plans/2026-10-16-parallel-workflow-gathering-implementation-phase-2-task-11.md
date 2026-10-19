# Parallel Workflow Gathering — Phase 2 Task 11: ParallelGroupExecutor

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Execute a `parallel_group` node: on first entry re-check the definition from the run's own snapshot, launch the group exactly once, record the wait trace, suspend the parent WITHOUT holding a queue slot, and close the fast-task lost-wake window with a node-addressed claim; on every re-entry never launch again, and either route the frozen result (identical shape on `success` and `failure`) exactly once or suspend again. File gathering is refused with an actionable error unless a `GroupGatherer` (Phase 3) is wired.
**Architecture:** One file, `domain/engine/executor_group.go`, modelled on `SpawnWorkflowExecutor.executeWait` (`executor_spawn.go:101-245`): first entry = `ParseGroupConfig` + `AnalyzeGroups` re-check → `GroupLauncher.Launch` → `GroupCoordinator.Evaluate` → wait `NodeExecution` → `UpdateState(waiting)` → lost-wake check (`SpawnWaitRepository.ClaimWaitingParent`) → `QueueManager.NotifyCapacityFreed` → `IsWait`. Re-entry = existing run ⇒ `done` delivers `{ContinuationPort, FinalOutput}` and marks delivery; files-mode decided/gathering/resolving ⇒ gatherer; otherwise `Evaluate` and suspend again.
**Tech Stack:** Go 1.25.5, standard library (`log/slog`, `strings`, `time`), `github.com/google/uuid`, testify.

**Prerequisites (must be merged first):** P1-T1 (`workflow.NodeTypeParallelGroup`, `ParseGroupConfig`, sentinels, constants), P1-T2 (`workflow.AnalyzeGroups`, `GroupGraph`, `GroupScope`), P1-T9 (`ErrGroupExecutionUnsupported`, `SetGroupSupport`, `GroupSupportResults`), P2-T1 (`instance.GroupRun` & repository port), P2-T7 (`Engine.StartGroupTask`; test helpers `sentinelInstanceRepo`, `gtSnapshot`), P2-T8 (`GroupLauncher`, `GroupLaunchRequest`, `ErrGroupPreLaunch`; test fakes `memGroupRuns`, `fakeGroupTaskStarter`, `recordingCanceller`, `fakeChildContracts`), P2-T9, P2-T10 (`GroupCoordinator`; test fake `memWaker`).

---

## Interfaces

**Consumes (exact):**
- `domain/engine/executor.go:12` `NodeExecutor`; `domain/engine/result.go:16-30` `NodeResult{Output, OutputPort, IsWait}`; `domain/engine/executor_timer.go:21-26` `ContextWithInstanceID` / `InstanceIDFromContext`.
- `domain/engine/engine.go:1289` `parseWorkflowGraph(snapshot)`.
- `domain/instance/spawn_wait.go:64` `SpawnWaitRepository.ClaimWaitingParent(ctx, parentID, nodeID) (bool, error)` (waiting→running CAS keyed by state AND node).
- `domain/instance/ports.go` `InstanceRepository.Get/UpdateState`, `NodeExecutionRepository.Create`; `domain/engine/ports.go:214` `QueueManager.NotifyCapacityFreed`.
- P1-T1 `workflow.ParseGroupConfig(NodeConfig) (GroupConfig, error)`; P1-T2 `workflow.AnalyzeGroups(*Workflow) (*GroupGraph, []ValidationError)`; `ValidationError.Severity` (`domain/workflow/validation.go:76-82`, `"warning"` = advisory).
- P1-T9 `ErrGroupExecutionUnsupported`.
- P2-T8 `(*GroupLauncher).Launch`; P2-T10 `(*GroupCoordinator).Evaluate`; P2-T1 `GroupRunRepository.GetByParentNode`, `Get`, `MarkContinuationDelivered`.

**Produces (exact):**
```go
type GroupGatherer interface { // implemented in Phase 3 (P3-T6); nil in Phase 2
	Gather(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (*GatherStep, error)
}
type GatherStep struct { Done bool; Port string; Output map[string]any } // Done=false ⇒ executor returns IsWait
type ParallelGroupExecutor struct{ /* instances, nodeExecs, groups, launcher, coord, claims, queueMgr, gatherer */ }
func NewParallelGroupExecutor(instances instance.InstanceRepository, nodeExecs instance.NodeExecutionRepository, groups instance.GroupRunRepository,
	launcher *GroupLauncher, coord *GroupCoordinator, claims instance.SpawnWaitRepository, queueMgr QueueManager) *ParallelGroupExecutor
func (x *ParallelGroupExecutor) SetGatherer(g GroupGatherer) // Phase 3 wiring, before FreezeExecutors
func (x *ParallelGroupExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error)
```

`Execute` contract (pinned by the tests):
1. `InstanceIDFromContext` is the PARENT of this group — for a nested group, the enclosing task's own instance.
2. `ParseGroupConfig(node.Config)`; error ⇒ plain error. `gatherMode == results_and_files` with no gatherer ⇒ error wrapping `ErrGroupExecutionUnsupported` (never silently results-only), before any lookup.
3. `GetByParentNode(instance, node)`: error ⇒ plain error (never mistaken for "no run", which would launch a second roster).
4. **First entry** (no run): load parent → `AnalyzeGroups` over `parent.WorkflowSnapshot` (any non-warning error ⇒ plain error naming the codes; a snapshot may predate validation) → `Launch` (pre-launch error ⇒ plain error, node `ErrorBehavior` applies, nothing persisted) → `coord.Evaluate(run.ID)` (error logged) → re-read run → write the wait `NodeExecution` (`Status succeeded`, `Output {"groupRunId","state"}`) → `UpdateState(waiting)` → lost-wake check → `NotifyCapacityFreed` → `NodeResult{IsWait: true, Output}`.
5. **Lost-wake check** (after the waiting write is durable): re-read the run; if `done` (or files-mode gatherable with a gatherer) and `ClaimWaitingParent(instance, node)` wins ⇒ return the routed result in-process (no slot was released). Losing the claim, or any error, degrades to waiting.
6. **Re-entry** (run exists): never launches. `done` ⇒ `MarkContinuationDelivered` (logged on error) and `NodeResult{OutputPort: run.ContinuationPort, Output: DeepCopyMap(run.FinalOutput)}` — the same frozen output every replay. A continuation port that is neither `success` nor `failure` ⇒ error. Files mode and state `decided|gathering|resolving_conflicts` ⇒ `Gather`; `Done` ⇒ routed + delivered, else suspend. Otherwise ⇒ `coord.Evaluate`, re-read, route if now done, else suspend again (`UpdateState(waiting)` + lost-wake check + `NotifyCapacityFreed`, no second trace).

## Wiring & data provenance

- **Composition root:** **P2-T14** (`cmd/arbitron/main.go:initDomain`) constructs `engine.NewParallelGroupExecutor(instanceRepo, nodeExecRepo, groupRunRepo, groupLauncher, groupCoord, instanceRepo, queueMgr)` and registers it with `eng.RegisterExecutor(workflow.NodeTypeParallelGroup, ...)` next to the spawn executor (main.go:940-949), BEFORE `eng.FreezeExecutors()` (main.go:1071), then calls `eng.SetGroupSupport(engine.GroupSupportResults)`. Exact producers:
  - `instances` ← `adaptsqlite.NewInstanceRepo(db)` (main.go:373); `nodeExecs` ← `adaptsqlite.NewNodeExecutionRepo(db)` (main.go:376).
  - `groups` ← `adaptsqlite.NewGroupRunRepo(db)` (P2-T3).
  - `launcher` ← `engine.NewGroupLauncher(...)` (P2-T8 wiring); `coord` ← `engine.NewGroupCoordinator(...)` (P2-T10 wiring).
  - `claims` ← `instanceRepo` (`*adaptsqlite.InstanceRepo` satisfies `instance.SpawnWaitRepository`, as main.go:943 already relies on; `ClaimWaitingParent` is the state-and-node CAS at `adapter/sqlite/instance_repo.go:769-790`).
  - `queueMgr` ← the `queue.Manager` (`eng.SetQueueManager(queueMgr)`).
  - `gatherer` ← nil in Phase 2; P3-T11 calls `SetGatherer`.
- **Runtime inputs:** `ctx` instance id ← `engine.go:946 ContextWithInstanceID(ctx, instanceID)`; `node` ← `engine.go:905` `nodeMap[currentNodeID]` parsed from `Instance.WorkflowSnapshot`; `vars` ← `engine.go:868 variable.FromSnapshotWithNodeIDs(inst.Variables, nodeIDs)` + `workspace.go:32` seeding; the parent's `CurrentNodeID` (which the wake and the claim match) ← `engine.go:923 UpdateCurrentNode` before the executor runs. The executor's routed `NodeResult.Output` lands in the parent's variables via `engine.go:1112-1118 handleNodeSuccess` (`SetNodeOutput(node.ID, output)` + `UpdateVariables`) and its port drives `engine.go:1329 findNextNode`.
- **Provenance probes:** `TestParallelGroupExecutor_EngineIntegration_ParentVariablesHoldOrderedResultsWithoutMerging` (real `Engine` + real `StartGroupTask` + real launcher/coordinator; asserts the SINK: the parent's persisted variables and completion) fails if the executor is not registered, if the instance id/vars do not flow from `Engine.run`, or if the output is not routed through `handleNodeSuccess`; `TestParallelGroupExecutor_NestedGroupUsesTheEnclosingTasksInstance` fails if the parent id is taken from anywhere but the run context.

---

### Task 11: ParallelGroupExecutor

**Files:**
- Create: `domain/engine/executor_group.go`
- Test: `domain/engine/executor_group_test.go` (package `engine_test`; reuses `memGroupRuns`, `fakeGroupTaskStarter`, `recordingCanceller`, `fakeChildContracts` (P2-T8 `group_fakes_test.go`), `memWaker` (P2-T10 `group_coordinator_test.go`), `sentinelInstanceRepo`, `gtSnapshot` (P2-T7 `engine_group_task_test.go`), `fixedIdentityResolver` (`orphan_reconciler_test.go:19-29`), `ctxAs` (`engine_trigger_scope_test.go:129`), and `newMockWorkflowRepo`, `newMockInstanceRepo`, `newMockNodeExecutionRepo`, `newMockSnapshotRepo`, `newMockUserRepo`, `newMockEventBus`, `newMockExecutor`, `newMockQueueManager`, `mockQueueRepo`, `waitForInstance` (`engine_test.go:26-869`))

- [x] Step 1: Write the failing tests. Create `domain/engine/executor_group_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodeClaims implements instance.SpawnWaitRepository's ClaimWaitingParent with
// the real predicate (state = waiting AND current_node_id = nodeID); the two
// spawn lookups are unused by the group executor.
type nodeClaims struct {
	instances *mockInstanceRepo
	mu        sync.Mutex
	attempts  []string
	won       []string
	failNext  error
}

var _ instance.SpawnWaitRepository = (*nodeClaims)(nil)

func (c *nodeClaims) FindChildBySpawnNode(context.Context, uuid.UUID, string, instance.SpawnMode) (*instance.ChildLink, error) {
	return nil, nil
}

func (c *nodeClaims) ListWaitingOnTerminalChild(context.Context) ([]instance.SpawnWaitCandidate, error) {
	return nil, nil
}

func (c *nodeClaims) ClaimWaitingParent(_ context.Context, parentID uuid.UUID, nodeID string) (bool, error) {
	c.mu.Lock()
	c.attempts = append(c.attempts, parentID.String()+"|"+nodeID)
	err := c.failNext
	c.failNext = nil
	c.mu.Unlock()
	if err != nil {
		return false, err
	}
	c.instances.mu.Lock()
	inst, ok := c.instances.instances[parentID]
	match := ok && inst.State == instance.StateWaiting && inst.CurrentNodeID != nil && *inst.CurrentNodeID == nodeID
	if match {
		inst.State = instance.StateRunning
	}
	c.instances.mu.Unlock()
	if match {
		c.mu.Lock()
		c.won = append(c.won, parentID.String()+"|"+nodeID)
		c.mu.Unlock()
	}
	return match, nil
}

func (c *nodeClaims) wonCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.won)
}

func (c *nodeClaims) attemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.attempts)
}

type gatherCall struct{ RunID, ParentID uuid.UUID }

type fakeGatherer struct {
	mu    sync.Mutex
	step  *engine.GatherStep
	err   error
	calls []gatherCall
}

var _ engine.GroupGatherer = (*fakeGatherer)(nil)

func (g *fakeGatherer) Gather(_ context.Context, run *instance.GroupRun, parent *instance.Instance) (*engine.GatherStep, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, gatherCall{RunID: run.ID, ParentID: parent.ID})
	return g.step, g.err
}

// ---------- workflows ----------

func gxGroupNode(id string, entry bool, cfg workflow.GroupConfig) workflow.Node {
	c := cfg
	return workflow.Node{ID: id, Type: workflow.NodeTypeParallelGroup, Entrypoint: entry, Config: workflow.NodeConfig{Group: &c}}
}

func gxBranches() workflow.GroupConfig {
	return workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
		GatherMode:         workflow.GroupGatherResults,
	}
}

// gxWorkflow: init -> g; g fans out to a1 / b1; success -> after, failure -> fail.
func gxWorkflowWith(cfg workflow.GroupConfig, groupIsEntry bool) workflow.Workflow {
	wf := workflow.Workflow{
		Name: "gx-parent",
		Nodes: []workflow.Node{
			gxGroupNode("g", groupIsEntry, cfg),
			{ID: "a1", Type: workflow.NodeTypeScript},
			{ID: "b1", Type: workflow.NodeTypeScript},
			{ID: "after", Type: workflow.NodeTypeScript},
			{ID: "fail", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "g", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "g", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "g", Target: "after", Output: workflow.GroupPortSuccess},
			{Source: "g", Target: "fail", Output: workflow.GroupPortFailure},
		},
	}
	if !groupIsEntry {
		wf.Nodes = append([]workflow.Node{{ID: "init", Type: workflow.NodeTypeScript, Entrypoint: true}}, wf.Nodes...)
		wf.Edges = append([]workflow.Edge{{Source: "init", Target: "g"}}, wf.Edges...)
	}
	return wf
}

func gxWorkflow() workflow.Workflow { return gxWorkflowWith(gxBranches(), false) }

// gxNestedWorkflow: task a of g is itself a group "inner" (x1, y1) whose
// continuations stay inside a's body.
func gxNestedWorkflow() workflow.Workflow {
	outer := workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		Tasks:              []workflow.GroupTask{{Key: "a", Entry: "inner"}, {Key: "b", Entry: "b1"}},
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
		GatherMode:         workflow.GroupGatherResults,
	}
	inner := workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		Tasks:              []workflow.GroupTask{{Key: "x", Entry: "x1"}, {Key: "y", Entry: "y1"}},
		CompletionRule:     workflow.GroupRuleFirstSuccess,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel,
		GatherMode:         workflow.GroupGatherResults,
	}
	return workflow.Workflow{
		Name: "gx-nested",
		Nodes: []workflow.Node{
			gxGroupNode("g", true, outer),
			gxGroupNode("inner", false, inner),
			{ID: "x1", Type: workflow.NodeTypeScript},
			{ID: "y1", Type: workflow.NodeTypeScript},
			{ID: "a-ok", Type: workflow.NodeTypeScript},
			{ID: "a-bad", Type: workflow.NodeTypeScript},
			{ID: "b1", Type: workflow.NodeTypeScript},
			{ID: "after", Type: workflow.NodeTypeScript},
			{ID: "fail", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "g", Target: "inner", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "g", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "g", Target: "after", Output: workflow.GroupPortSuccess},
			{Source: "g", Target: "fail", Output: workflow.GroupPortFailure},
			{Source: "inner", Target: "x1", Output: workflow.GroupPortTask, Label: "x"},
			{Source: "inner", Target: "y1", Output: workflow.GroupPortTask, Label: "y"},
			{Source: "inner", Target: "a-ok", Output: workflow.GroupPortSuccess},
			{Source: "inner", Target: "a-bad", Output: workflow.GroupPortFailure},
		},
	}
}

// ---------- fixture ----------

type gxFixture struct {
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	groups    *memGroupRuns
	starter   *fakeGroupTaskStarter
	canceller *recordingCanceller
	waker     *memWaker
	claims    *nodeClaims
	qm        *mockQueueManager
	bus       *mockEventBus
	launcher  *engine.GroupLauncher
	coord     *engine.GroupCoordinator
	x         *engine.ParallelGroupExecutor
	parent    *instance.Instance
	owner     uuid.UUID
	wf        workflow.Workflow

	orderMu sync.Mutex
	order   []string
}

func (f *gxFixture) record(s string) {
	f.orderMu.Lock()
	defer f.orderMu.Unlock()
	f.order = append(f.order, s)
}

func (f *gxFixture) recorded() []string {
	f.orderMu.Lock()
	defer f.orderMu.Unlock()
	return append([]string(nil), f.order...)
}

func (f *gxFixture) resetOrder() {
	f.orderMu.Lock()
	defer f.orderMu.Unlock()
	f.order = nil
}

func newGxFixture(t *testing.T, wf workflow.Workflow, currentNode string) *gxFixture {
	t.Helper()
	owner := uuid.New()
	instances := newMockInstanceRepo()
	cur := currentNode
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: wf.Name, WorkflowSnapshot: gtSnapshot(t, wf),
		State: instance.StateRunning, Priority: instance.PriorityNormal, CurrentNodeID: &cur,
	}
	require.NoError(t, instances.Create(context.Background(), parent))

	f := &gxFixture{
		instances: instances,
		nodeExecs: newMockNodeExecutionRepo(),
		groups:    newMemGroupRuns(),
		starter:   &fakeGroupTaskStarter{instances: instances},
		canceller: &recordingCanceller{instances: instances},
		waker:     &memWaker{instances: instances},
		claims:    &nodeClaims{instances: instances},
		qm:        newMockQueueManager(),
		bus:       newMockEventBus(),
		parent:    parent,
		owner:     owner,
		wf:        wf,
	}
	ids := fixedIdentityResolver{}
	contracts := &fakeChildContracts{byName: map[string][]workflow.InputField{"child-wf": {}}}
	f.launcher = engine.NewGroupLauncher(f.groups, f.starter, contracts, nil, nil, ids, f.canceller, f.bus)
	f.coord = engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, ids, f.bus, f.qm)
	f.x = engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, f.coord, f.claims, f.qm)

	f.starter.onStart = func(req engine.GroupTaskStartRequest) { f.record("start:" + req.TaskKey) }
	f.nodeExecs.onCreate = func(*instance.NodeExecution) { f.record("nodeExec") }
	f.instances.onUpdateState = func(_ uuid.UUID, st instance.State) { f.record("state:" + string(st)) }
	f.qm.onNotify = func() { f.record("notify") }
	return f
}

func gxNode(t *testing.T, wf workflow.Workflow, id string) workflow.Node {
	t.Helper()
	for _, n := range wf.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %q", id)
	return workflow.Node{}
}

func gxVars(t *testing.T) *variable.VariableStore {
	t.Helper()
	s := variable.NewStore()
	require.NoError(t, s.SetInput("repo", "acme/widgets"))
	return s
}

func (f *gxFixture) executeAs(t *testing.T, instanceID uuid.UUID, nodeID string) (*engine.NodeResult, error) {
	t.Helper()
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	return f.x.Execute(ctx, gxNode(t, f.wf, nodeID), gxVars(t), f.wf.Name)
}

func (f *gxFixture) execute(t *testing.T) (*engine.NodeResult, error) {
	t.Helper()
	return f.executeAs(t, f.parent.ID, "g")
}

func (f *gxFixture) run(t *testing.T) *instance.GroupRun {
	t.Helper()
	run, err := f.groups.GetByParentNode(context.Background(), f.parent.ID, "g")
	require.NoError(t, err)
	require.NotNil(t, run)
	return run
}

// finishChild moves a task child to a terminal state with final variables.
func (f *gxFixture) finishChild(t *testing.T, id uuid.UUID, state instance.State, vars map[string]any) {
	t.Helper()
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	inst := f.instances.instances[id]
	require.NotNil(t, inst)
	now := time.Now()
	inst.State = state
	inst.Variables = vars
	inst.CompletedAt = &now
	inst.UpdatedAt = now
}

// finishAll completes every task of the parent's run with the given states (in
// author order) and delivers each terminal to the coordinator.
func (f *gxFixture) finishAll(t *testing.T, states ...instance.State) {
	t.Helper()
	run := f.run(t)
	for i, task := range run.Tasks {
		f.finishChild(t, task.ChildInstanceID, states[i], map[string]any{"from": task.Key})
	}
	for _, task := range run.Tasks {
		f.coord.OnTaskTerminal(context.Background(), task.ChildInstanceID)
	}
}

func (f *gxFixture) parentState() instance.State {
	return f.instances.get(f.parent.ID).State
}

func gxKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- happy path ----------

func TestParallelGroupExecutor_FirstEntryLaunchesOnceAndWaitsWithoutHoldingASlot(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")

	res, err := f.execute(t)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsWait)

	run := f.run(t)
	assert.Equal(t, map[string]any{"groupRunId": run.ID.String(), "state": "running"}, res.Output)
	assert.Equal(t, 1, f.groups.count())
	assert.Len(t, f.starter.requests(), 2)
	assert.Equal(t, instance.StateWaiting, f.parentState())

	// ORDER IS THE DEADLOCK GUARD: every task is started before the parent
	// suspends, and capacity is released only after the waiting write.
	assert.Equal(t, []string{"start:a", "start:b", "nodeExec", "state:waiting", "notify"}, f.recorded())

	trace, err := f.nodeExecs.GetByInstanceAndNode(context.Background(), f.parent.ID, "g")
	require.NoError(t, err)
	assert.Equal(t, instance.NodeStatusSucceeded, trace.Status)
	assert.Equal(t, run.ID.String(), trace.Output["groupRunId"])
	assert.Equal(t, 1, trace.Attempt)
}

func TestParallelGroupExecutor_EntrypointGroup(t *testing.T) {
	f := newGxFixture(t, gxWorkflowWith(gxBranches(), true), "g")

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait)
	assert.Len(t, f.starter.requests(), 2)
	assert.Equal(t, instance.StateWaiting, f.parentState())
}

// ---------- success ----------

func TestParallelGroupExecutor_DoneRoutesBothPortsWithTheSameOutputShape(t *testing.T) {
	outputs := map[string]map[string]any{}
	for _, c := range []struct {
		port   string
		states []instance.State
	}{
		{workflow.GroupPortSuccess, []instance.State{instance.StateCompleted, instance.StateCompleted}},
		{workflow.GroupPortFailure, []instance.State{instance.StateFailed, instance.StateCompleted}},
	} {
		t.Run(c.port, func(t *testing.T) {
			f := newGxFixture(t, gxWorkflow(), "g")
			_, err := f.execute(t)
			require.NoError(t, err)
			f.finishAll(t, c.states...)
			require.Equal(t, instance.StateQueued, f.parentState(), "the coordinator woke the parent at this node")

			// The queue starts the parent again; it re-enters the group node.
			require.NoError(t, f.instances.UpdateState(context.Background(), f.parent.ID, instance.StateRunning))
			res, err := f.execute(t)
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.False(t, res.IsWait)
			assert.Equal(t, c.port, res.OutputPort)

			run := f.run(t)
			assert.Equal(t, run.FinalOutput, res.Output)
			assert.True(t, run.ContinuationDelivered)
			assert.Len(t, f.starter.requests(), 2, "re-entry never launches again")
			outputs[c.port] = res.Output.(map[string]any)
		})
	}
	require.Len(t, outputs, 2)
	assert.Equal(t, gxKeys(outputs["success"]), gxKeys(outputs["failure"]))
	st := outputs["success"]["tasks"].([]any)[0].(map[string]any)
	ft := outputs["failure"]["tasks"].([]any)[0].(map[string]any)
	assert.Equal(t, gxKeys(st), gxKeys(ft))
	assert.Equal(t, "failed", ft["status"])
}

func TestParallelGroupExecutor_ReplayDeliversTheSameFrozenResult(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	_, err := f.execute(t)
	require.NoError(t, err)
	f.finishAll(t, instance.StateCompleted, instance.StateCompleted)
	require.NoError(t, f.instances.UpdateState(context.Background(), f.parent.ID, instance.StateRunning))

	first, err := f.execute(t)
	require.NoError(t, err)
	second, err := f.execute(t)
	require.NoError(t, err, "a replay (retry, crash recovery at this node) is idempotent")
	assert.Equal(t, first.OutputPort, second.OutputPort)
	assert.Equal(t, first.Output, second.Output)
	assert.Len(t, f.starter.requests(), 2)
}

func TestParallelGroupExecutor_ReEntryBeforeDoneNeverRelaunchesAndSuspendsAgain(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	_, err := f.execute(t)
	require.NoError(t, err)
	require.NoError(t, f.instances.UpdateState(context.Background(), f.parent.ID, instance.StateRunning)) // e.g. resumed after a pause
	f.resetOrder()

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait)
	assert.Len(t, f.starter.requests(), 2, "never a second roster")
	assert.Equal(t, 1, f.groups.count())
	assert.Equal(t, instance.StateWaiting, f.parentState(), "a re-entered parent must not be left running with no goroutine")
	assert.Equal(t, []string{"state:waiting", "notify"}, f.recorded(), "no second wait trace")
}

// ---------- edge: lost wake ----------

func TestParallelGroupExecutor_FastTasksLostWakeIsClaimedInProcess(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	var once sync.Once
	f.nodeExecs.onCreate = func(*instance.NodeExecution) {
		// Both tasks finish while the parent is still running: the coordinator
		// finalizes but cannot wake a parent that is not waiting yet.
		once.Do(func() { f.finishAll(t, instance.StateCompleted, instance.StateCompleted) })
	}

	res, err := f.execute(t)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsWait, "the executor claims the parent back and routes in-process")
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)
	assert.Equal(t, instance.StateRunning, f.parentState())
	assert.Equal(t, 1, f.claims.wonCount())
	assert.Equal(t, 0, f.waker.wokenCount())
	assert.Equal(t, 0, f.qm.notifyCount(), "the slot was never released, so nothing to notify")
	assert.True(t, f.run(t).ContinuationDelivered)
}

func TestParallelGroupExecutor_LosingTheClaimFallsBackToWaiting(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	var once sync.Once
	f.instances.onUpdateState = func(_ uuid.UUID, st instance.State) {
		if st == instance.StateWaiting {
			// The tasks finish right after the waiting write: the coordinator's
			// wake wins the race and re-queues the parent.
			once.Do(func() { f.finishAll(t, instance.StateCompleted, instance.StateCompleted) })
		}
	}

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait, "the wake won; the re-queued parent re-enters and delivers")
	assert.Equal(t, instance.StateQueued, f.parentState())
	assert.Equal(t, 1, f.claims.attemptCount())
	assert.Equal(t, 0, f.claims.wonCount())
	assert.Equal(t, 1, f.waker.wokenCount())
}

func TestParallelGroupExecutor_ClaimErrorDegradesToWaiting(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	var once sync.Once
	f.nodeExecs.onCreate = func(*instance.NodeExecution) {
		once.Do(func() { f.finishAll(t, instance.StateCompleted, instance.StateCompleted) })
	}
	f.claims.failNext = errors.New("database is locked")

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait, "the coordinator's sweep wakes it later")
	assert.Equal(t, instance.StateWaiting, f.parentState())
	assert.Equal(t, 1, f.qm.notifyCount())
}

// ---------- failure ----------

func TestParallelGroupExecutor_PartialLaunchFailureRoutesFailureOnce(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	f.starter.errByKey = map[string]error{"b": errors.New("queue full")}

	res, err := f.execute(t)
	require.NoError(t, err, "a post-persist launch failure is a group FAILURE, not a node error")
	require.NotNil(t, res)
	assert.False(t, res.IsWait)
	assert.Equal(t, workflow.GroupPortFailure, res.OutputPort)
	out := res.Output.(map[string]any)
	assert.Equal(t, "failure", out["outcome"])
	assert.Contains(t, out["reason"], "launch failed")

	run := f.run(t)
	assert.Equal(t, instance.StateCancelled, f.instances.get(run.Tasks[0].ChildInstanceID).State)
	assert.True(t, run.ContinuationDelivered)
	assert.Equal(t, instance.StateRunning, f.parentState())
	assert.Len(t, f.starter.requests(), 2)
}

// ---------- error ----------

func TestParallelGroupExecutor_PreLaunchErrorIsAPlainNodeError(t *testing.T) {
	cfg := gxBranches()
	cfg.Tasks[1].Inputs = map[string]any{"v": "{{missing.value}}"}
	f := newGxFixture(t, gxWorkflowWith(cfg, false), "g")

	res, err := f.execute(t)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Equal(t, 0, f.groups.count(), "nothing persisted, so the node's ErrorBehavior can retry cleanly")
	assert.Empty(t, f.starter.requests())
	assert.Equal(t, instance.StateRunning, f.parentState())
	assert.Empty(t, f.recorded())
}

func TestParallelGroupExecutor_ResultsAndFilesWithoutAGathererIsRefused(t *testing.T) {
	cfg := gxBranches()
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles
	cfg.ResolverProfile = "resolver"
	f := newGxFixture(t, gxWorkflowWith(cfg, false), "g")

	_, err := f.execute(t)
	require.ErrorIs(t, err, engine.ErrGroupExecutionUnsupported, "never silently degraded to results-only")
	assert.Contains(t, err.Error(), workflow.GroupGatherResults, "the error names the mode that works")
	assert.Equal(t, 0, f.groups.count())
	assert.Empty(t, f.starter.requests())
}

func TestParallelGroupExecutor_SnapshotPredatingValidationIsRechecked(t *testing.T) {
	t.Run("missing failure port", func(t *testing.T) {
		wf := gxWorkflow()
		wf.Edges = wf.Edges[:len(wf.Edges)-1] // drop g -> fail (failure)
		f := newGxFixture(t, wf, "g")

		_, err := f.execute(t)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fails parallel-group validation")
		assert.Contains(t, err.Error(), "GROUP_MISSING_PORT")
		assert.Equal(t, 0, f.groups.count())
		assert.Empty(t, f.starter.requests())
	})
	t.Run("no group config", func(t *testing.T) {
		wf := gxWorkflow()
		for i := range wf.Nodes {
			if wf.Nodes[i].ID == "g" {
				wf.Nodes[i].Config.Group = nil
			}
		}
		f := newGxFixture(t, wf, "g")

		_, err := f.execute(t)
		require.ErrorIs(t, err, workflow.ErrGroupConfigMissing)
		assert.Equal(t, 0, f.groups.count())
	})
}

func TestParallelGroupExecutor_GroupRunLookupErrorNeverLaunches(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	f.groups.failOnce("GetByParentNode", errors.New("database is locked"))

	_, err := f.execute(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is locked")
	assert.Empty(t, f.starter.requests(), "a lookup failure must not be mistaken for 'no run yet'")
}

func TestParallelGroupExecutor_MissingInstanceIDErrors(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	_, err := f.x.Execute(context.Background(), gxNode(t, f.wf, "g"), gxVars(t), f.wf.Name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance ID not found in context")
}

func TestParallelGroupExecutor_WaitTraceWriteFailureIsANodeError(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	f.nodeExecs.createErr = errors.New("disk full")

	_, err := f.execute(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write node execution record")
	assert.Equal(t, instance.StateRunning, f.parentState(), "the parent is not suspended without a trace")
}

// ---------- edge: Phase 3 seam ----------

func TestParallelGroupExecutor_FilesModeReEntryUsesTheGatherer(t *testing.T) {
	cfg := gxBranches()
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles
	cfg.ResolverProfile = "resolver"
	wf := gxWorkflowWith(cfg, false)
	seed := func(f *gxFixture) uuid.UUID {
		run := &instance.GroupRun{
			ID: uuid.New(), ParentInstanceID: f.parent.ID, NodeID: "g", Attempt: 1,
			Form: cfg.Form, CompletionRule: cfg.CompletionRule, UnfinishedBehavior: cfg.UnfinishedBehavior,
			GatherMode: cfg.GatherMode, State: instance.GroupRunDecided, ActionsApplied: true,
			Decision: &instance.GroupDecision{Outcome: "success", Rule: cfg.CompletionRule, Behavior: cfg.UnfinishedBehavior},
		}
		f.groups.seedRun(run)
		return run.ID
	}

	t.Run("gather done routes", func(t *testing.T) {
		f := newGxFixture(t, wf, "g")
		g := &fakeGatherer{step: &engine.GatherStep{Done: true, Port: workflow.GroupPortSuccess, Output: map[string]any{"gather": "applied"}}}
		f.x.SetGatherer(g)
		runID := seed(f)

		res, err := f.execute(t)
		require.NoError(t, err)
		assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)
		assert.Equal(t, map[string]any{"gather": "applied"}, res.Output)
		require.Len(t, g.calls, 1)
		assert.Equal(t, gatherCall{RunID: runID, ParentID: f.parent.ID}, g.calls[0])
		assert.True(t, f.groups.get(runID).ContinuationDelivered)
	})
	t.Run("gather in progress suspends", func(t *testing.T) {
		f := newGxFixture(t, wf, "g")
		f.x.SetGatherer(&fakeGatherer{step: &engine.GatherStep{Done: false}})
		seed(f)

		res, err := f.execute(t)
		require.NoError(t, err)
		assert.True(t, res.IsWait)
		assert.Equal(t, instance.StateWaiting, f.parentState())
	})
	t.Run("gather error is a node error", func(t *testing.T) {
		f := newGxFixture(t, wf, "g")
		f.x.SetGatherer(&fakeGatherer{err: errors.New("merge exploded")})
		seed(f)

		_, err := f.execute(t)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "merge exploded")
	})
}

// ---------- edge: nesting ----------

func TestParallelGroupExecutor_NestedGroupUsesTheEnclosingTasksInstance(t *testing.T) {
	wf := gxNestedWorkflow()
	f := newGxFixture(t, wf, "g")
	// The branch task instance for task "a" of the outer group runs the same
	// snapshot from its entry, the nested group node "inner".
	rootID, rootNode, outerRun, key := f.parent.ID, "g", uuid.New(), "a"
	inner := "inner"
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: f.owner, WorkflowName: wf.Name, WorkflowSnapshot: gtSnapshot(t, wf),
		State: instance.StateRunning, Priority: instance.PriorityNormal, CurrentNodeID: &inner, SpawnDepth: 1,
		ParentInstanceID: &rootID, ParentNodeID: &rootNode, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &outerRun, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
	}
	require.NoError(t, f.instances.Create(context.Background(), child))

	res, err := f.executeAs(t, child.ID, "inner")
	require.NoError(t, err)
	assert.True(t, res.IsWait)

	run, err := f.groups.GetByParentNode(context.Background(), child.ID, "inner")
	require.NoError(t, err)
	require.NotNil(t, run, "the nested group's run belongs to the enclosing TASK instance")
	assert.Equal(t, child.ID, run.ParentInstanceID)
	reqs := f.starter.requests()
	require.Len(t, reqs, 2)
	for i, want := range []string{"x1", "y1"} {
		assert.Equal(t, child.ID, reqs[i].ParentInstanceID)
		assert.Equal(t, "inner", reqs[i].ParentNodeID)
		assert.Equal(t, want, reqs[i].BranchEntry)
	}
	assert.Equal(t, instance.StateWaiting, f.instances.get(child.ID).State)
	assert.Equal(t, instance.StateRunning, f.parentState(), "the root is untouched by its task's nested group")
}

// ---------- integration: real Engine, sink assertions ----------

func TestParallelGroupExecutor_EngineIntegration_ParentVariablesHoldOrderedResultsWithoutMerging(t *testing.T) {
	ctx := context.Background()
	wfRepo := newMockWorkflowRepo()
	instRepo := newMockInstanceRepo()
	repo := sentinelInstanceRepo{instRepo}
	nodeExecs := newMockNodeExecutionRepo()
	bus := newMockEventBus()
	exec := newMockExecutor()
	qm := newMockQueueManager()
	eng := engine.NewEngine(wfRepo, repo, nodeExecs, newMockSnapshotRepo(), newMockUserRepo(),
		map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: exec}, bus, qm, &mockQueueRepo{instRepo: instRepo}, "")
	qm.mu.Lock()
	qm.enqueueFn = func(ctx context.Context, id uuid.UUID, _ uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	qm.mu.Unlock()
	eng.SetGroupSupport(engine.GroupSupportResults)
	t.Cleanup(eng.Shutdown)

	groups := newMemGroupRuns()
	ids := fixedIdentityResolver{}
	launcher := engine.NewGroupLauncher(groups, eng, eng, nil, nil, ids, eng, bus)
	coord := engine.NewGroupCoordinator(groups, repo, &memWaker{instances: instRepo}, eng, ids, bus, qm)
	eng.RegisterExecutor(workflow.NodeTypeParallelGroup,
		engine.NewParallelGroupExecutor(repo, nodeExecs, groups, launcher, coord, &nodeClaims{instances: instRepo}, qm))
	eng.FreezeExecutors()

	wf := gxWorkflow()
	wfRepo.add(&wf)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	exec.onNode("a1", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		<-release
		return &engine.NodeResult{Output: map[string]any{"x": "from-a"}}, nil
	})
	exec.onNode("b1", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		<-release
		return &engine.NodeResult{Output: map[string]any{"x": "from-b"}}, nil
	})

	owner := uuid.New()
	parentID, _, err := eng.Trigger(ctxAs(owner), wf.Name, nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, instRepo, parentID, 5*time.Second, instance.StateWaiting)
	run, err := groups.GetByParentNode(ctx, parentID, "g")
	require.NoError(t, err)
	require.NotNil(t, run)
	for _, task := range run.Tasks {
		child := instRepo.get(task.ChildInstanceID)
		require.NotNil(t, child, "task %s was created under its reserved id", task.Key)
		assert.Equal(t, instance.GroupTaskKindBranch, child.GroupTaskKind)
		assert.Equal(t, owner, child.OwnerID)
	}

	releaseOnce.Do(func() { close(release) })
	for _, task := range run.Tasks {
		waitForInstance(t, instRepo, task.ChildInstanceID, 5*time.Second, instance.StateCompleted)
	}

	// The sweep is the correctness authority (P2-T13 adds in-process dispatch).
	require.NoError(t, coord.Reconcile(ctx))
	waitForInstance(t, instRepo, parentID, time.Second, instance.StateQueued)
	require.NoError(t, eng.StartInstance(ctx, parentID)) // what the queue evaluator does
	parent := waitForInstance(t, instRepo, parentID, 5*time.Second, instance.StateCompleted)

	out, ok := parent.Variables["g"].(map[string]any)
	require.True(t, ok, "the group's output is the node output of g in the parent's persisted variables")
	assert.Equal(t, "success", out["outcome"])
	tasks := out["tasks"].([]any)
	require.Len(t, tasks, 2)
	assert.Equal(t, "a", tasks[0].(map[string]any)["key"], "author order")
	assert.Equal(t, "b", tasks[1].(map[string]any)["key"])

	store := variable.FromSnapshotWithNodeIDs(parent.Variables, map[string]bool{"init": true, "g": true, "after": true})
	v, ok := store.Get("g.tasks.0.result.a1.x")
	require.True(t, ok)
	assert.Equal(t, "from-a", v)
	v, ok = store.Get("g.byKey.b.result.b1.x")
	require.True(t, ok)
	assert.Equal(t, "from-b", v)

	assert.NotContains(t, parent.Variables, "a1", "branch variables are never merged into the parent")
	assert.NotContains(t, parent.Variables, "b1")
	assert.Contains(t, parent.Variables, "after", "the parent continued along the success port")
	assert.NotContains(t, parent.Variables, "fail")
	assert.True(t, groups.get(run.ID).ContinuationDelivered)
	afterCalls := 0
	for _, call := range exec.getCalls() {
		if call.NodeID == "after" {
			afterCalls++
		}
	}
	assert.Equal(t, 1, afterCalls, "the parent continued exactly once")
}

func TestNewParallelGroupExecutor_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	build := []func(){
		func() { engine.NewParallelGroupExecutor(nil, f.nodeExecs, f.groups, f.launcher, f.coord, f.claims, f.qm) },
		func() { engine.NewParallelGroupExecutor(f.instances, nil, f.groups, f.launcher, f.coord, f.claims, f.qm) },
		func() { engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, nil, f.launcher, f.coord, f.claims, f.qm) },
		func() { engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, nil, f.coord, f.claims, f.qm) },
		func() { engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, nil, f.claims, f.qm) },
		func() { engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, f.coord, nil, f.qm) },
		func() { engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, f.coord, f.claims, nil) },
	}
	for i, b := range build {
		assert.Panics(t, b, "collaborator %d", i)
	}
}
```

- [x] Step 2: Run it, confirm it fails.

  Confirmed missing executor/gatherer types. Adapted the test harness to the P2-T16 atomic receipt contract: unit fixtures record terminal receipts before callbacks; engine integration uses real SQLite instance/group/trace/snapshot repositories and node-addressed wake/claim ports, asserting persisted parent variables.

```bash
go test ./domain/engine/ -run 'TestParallelGroupExecutor|TestNewParallelGroupExecutor' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/executor_group_test.go:83:22: undefined: engine.GatherStep
domain/engine/executor_group_test.go:89:22: undefined: engine.GroupGatherer
domain/engine/executor_group_test.go:206:22: undefined: engine.ParallelGroupExecutor
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement. Create `domain/engine/executor_group.go`:

```go
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupGatherer merges a decided file-gathering group into the parent's
// working area (Phase 3, P3-T6). The executor is the single writer of the
// parent's area, so gathering runs here, in the parent's own goroutine.
type GroupGatherer interface {
	Gather(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (*GatherStep, error)
}

// GatherStep is one gather pass's outcome. Done=false means gathering is not
// finished (e.g. waiting on interactive conflict resolution): the parent waits.
type GatherStep struct {
	Done   bool
	Port   string
	Output map[string]any
}

// ParallelGroupExecutor executes one parallel_group node. Every collaborator
// is a port or a group component; the executor holds no engine.
type ParallelGroupExecutor struct {
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	groups    instance.GroupRunRepository
	launcher  *GroupLauncher
	coord     *GroupCoordinator
	claims    instance.SpawnWaitRepository
	queueMgr  QueueManager
	gatherer  GroupGatherer
}

var _ NodeExecutor = (*ParallelGroupExecutor)(nil)

// NewParallelGroupExecutor constructs the executor. Every argument is
// required; a nil one panics (a missing claim port, for instance, would
// silently reopen the lost-wake window).
func NewParallelGroupExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	groups instance.GroupRunRepository,
	launcher *GroupLauncher,
	coord *GroupCoordinator,
	claims instance.SpawnWaitRepository,
	queueMgr QueueManager,
) *ParallelGroupExecutor {
	switch {
	case instances == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil InstanceRepository")
	case nodeExecs == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil NodeExecutionRepository")
	case groups == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil GroupRunRepository")
	case launcher == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil GroupLauncher")
	case coord == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil GroupCoordinator")
	case claims == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil SpawnWaitRepository (ClaimWaitingParent)")
	case queueMgr == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil QueueManager")
	}
	return &ParallelGroupExecutor{
		instances: instances,
		nodeExecs: nodeExecs,
		groups:    groups,
		launcher:  launcher,
		coord:     coord,
		claims:    claims,
		queueMgr:  queueMgr,
	}
}

// SetGatherer enables file gathering (Phase 3 wiring). It must be called during
// composition, before FreezeExecutors, never concurrently with Execute.
func (x *ParallelGroupExecutor) SetGatherer(g GroupGatherer) {
	x.gatherer = g
}

// Execute runs one parallel_group node. Every failure before the group run is
// persisted returns a plain error so the node's ErrorBehavior applies.
func (x *ParallelGroupExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("parallel_group %q: instance ID not found in context", node.ID)
	}

	// The SAME parser author-time validation uses: an instance executes from a
	// snapshot that may predate that validation.
	cfg, err := workflow.ParseGroupConfig(node.Config)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: %w", node.ID, err)
	}
	filesMode := cfg.GatherMode == workflow.GroupGatherResultsAndFiles
	if filesMode && x.gatherer == nil {
		return nil, fmt.Errorf("parallel_group %q: gatherMode %q: %w; this server gathers task results only — "+
			"use gatherMode %q until file gathering is enabled", node.ID, cfg.GatherMode, ErrGroupExecutionUnsupported, workflow.GroupGatherResults)
	}

	run, err := x.groups.GetByParentNode(ctx, instanceID, node.ID)
	if err != nil {
		// Never mistaken for "no run yet": that is how a second roster is born.
		return nil, fmt.Errorf("parallel_group %q: look up the group run: %w", node.ID, err)
	}
	if run != nil {
		return x.reenter(ctx, node, filesMode, run, instanceID)
	}
	return x.firstEntry(ctx, node, cfg, vars, instanceID, filesMode)
}

// firstEntry launches the group and suspends the parent.
//
// ORDER IS LOAD-BEARING, exactly as for an awaited spawn (executor_spawn.go:127-193):
// launch every task -> write the wait trace -> transition to waiting ->
// lost-wake check -> release capacity. A waiting parent holds no slot, so with
// a per-owner cap of one the tasks can still run.
func (x *ParallelGroupExecutor) firstEntry(ctx context.Context, node workflow.Node, cfg workflow.GroupConfig, vars *variable.VariableStore, instanceID uuid.UUID, filesMode bool) (*NodeResult, error) {
	parent, err := x.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: load parent instance: %w", node.ID, err)
	}
	scope, err := revalidateGroup(node.ID, parent.WorkflowSnapshot)
	if err != nil {
		return nil, err
	}

	startedAt := time.Now()
	run, err := x.launcher.Launch(ctx, GroupLaunchRequest{Parent: parent, Node: node, Config: cfg, Ownership: scope, Vars: vars})
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: %w", node.ID, err)
	}
	if err := x.coord.Evaluate(ctx, run.ID); err != nil {
		slog.Warn("parallel_group: evaluating the freshly launched group failed; the group sweep retries",
			"nodeID", node.ID, "groupRunID", run.ID, "error", err)
	}
	run = x.reread(ctx, run)
	output := groupWaitOutput(run)

	// executeWithRetry skips writing a node execution on the wait path, so the
	// executor writes its own — otherwise the group node leaves no trace.
	completedAt := time.Now()
	if err := x.nodeExecs.Create(ctx, &instance.NodeExecution{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		NodeID:      node.ID,
		Attempt:     1,
		Status:      instance.NodeStatusSucceeded,
		Output:      output,
		StartedAt:   startedAt,
		CompletedAt: &completedAt,
	}); err != nil {
		return nil, fmt.Errorf("parallel_group %q: write node execution record: %w", node.ID, err)
	}
	return x.suspend(ctx, node, filesMode, run.ID, instanceID, output)
}

// reenter handles every later execution of the node: never a second launch.
func (x *ParallelGroupExecutor) reenter(ctx context.Context, node workflow.Node, filesMode bool, run *instance.GroupRun, instanceID uuid.UUID) (*NodeResult, error) {
	if res, err := x.resolve(ctx, node, filesMode, run, instanceID); err != nil || res != nil {
		return res, err
	}
	// Not ready: let the coordinator catch up (a terminal may have been missed).
	if err := x.coord.Evaluate(ctx, run.ID); err != nil {
		slog.Warn("parallel_group: re-evaluating the group on re-entry failed; the group sweep retries",
			"nodeID", node.ID, "groupRunID", run.ID, "error", err)
	}
	run = x.reread(ctx, run)
	if res, err := x.resolve(ctx, node, filesMode, run, instanceID); err != nil || res != nil {
		return res, err
	}
	return x.suspend(ctx, node, filesMode, run.ID, instanceID, groupWaitOutput(run))
}

// resolve returns the routed result for a run that can continue the parent
// now (the parent is running in this goroutine), or nil when it must wait.
func (x *ParallelGroupExecutor) resolve(ctx context.Context, node workflow.Node, filesMode bool, run *instance.GroupRun, instanceID uuid.UUID) (*NodeResult, error) {
	if run.State == instance.GroupRunDone {
		return x.deliver(ctx, node, run)
	}
	if !filesMode || !gatherableGroupState(run.State) {
		return nil, nil
	}
	if x.gatherer == nil {
		return nil, fmt.Errorf("parallel_group %q: group run %s is ready to gather files: %w", node.ID, run.ID, ErrGroupExecutionUnsupported)
	}
	parent, err := x.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: load parent instance: %w", node.ID, err)
	}
	step, err := x.gatherer.Gather(ctx, run, parent)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: gather: %w", node.ID, err)
	}
	if step == nil || !step.Done {
		return nil, nil
	}
	if step.Port != workflow.GroupPortSuccess && step.Port != workflow.GroupPortFailure {
		return nil, fmt.Errorf("parallel_group %q: the gatherer finished with port %q (want %q or %q)",
			node.ID, step.Port, workflow.GroupPortSuccess, workflow.GroupPortFailure)
	}
	if err := x.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
		slog.Warn("parallel_group: recording delivery failed; a replay re-delivers the same output",
			"nodeID", node.ID, "groupRunID", run.ID, "error", err)
	}
	return &NodeResult{OutputPort: step.Port, Output: variable.DeepCopyMap(step.Output)}, nil
}

// deliver routes a done run: the frozen output, on the frozen port, every time.
func (x *ParallelGroupExecutor) deliver(ctx context.Context, node workflow.Node, run *instance.GroupRun) (*NodeResult, error) {
	port := run.ContinuationPort
	if port != workflow.GroupPortSuccess && port != workflow.GroupPortFailure {
		return nil, fmt.Errorf("parallel_group %q: group run %s is done but carries continuation port %q",
			node.ID, run.ID, port)
	}
	if err := x.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
		slog.Warn("parallel_group: recording delivery failed; a replay re-delivers the same output",
			"nodeID", node.ID, "groupRunID", run.ID, "error", err)
	}
	return &NodeResult{OutputPort: port, Output: variable.DeepCopyMap(run.FinalOutput)}, nil
}

// suspend transitions the parent to waiting, closes the lost-wake window, and
// releases its queue slot.
func (x *ParallelGroupExecutor) suspend(ctx context.Context, node workflow.Node, filesMode bool, runID uuid.UUID, instanceID uuid.UUID, output map[string]any) (*NodeResult, error) {
	// waiting_until stays NULL: an untimed wait, invisible to the timer sweep.
	if err := x.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		return nil, fmt.Errorf("parallel_group %q: set waiting state: %w", node.ID, err)
	}
	res, claimed, err := x.recoverLostWake(ctx, node, filesMode, runID, instanceID)
	if claimed {
		if err != nil || res != nil {
			return res, err
		}
		// Claimed back, but the gatherer has more to do: wait again; the
		// gatherer owns the next wake.
		if err := x.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
			return nil, fmt.Errorf("parallel_group %q: set waiting state: %w", node.ID, err)
		}
	}
	x.queueMgr.NotifyCapacityFreed(ctx)
	return &NodeResult{IsWait: true, Output: output}, nil
}

// recoverLostWake re-reads the run AFTER the waiting write is durable. If the
// group became ready while the parent was still running (the coordinator could
// not wake a parent that was not yet waiting), it claims the parent back to
// running with the state-and-node CAS and resolves in-process. Losing the
// claim means the coordinator's wake won and re-queued the parent; every error
// degrades to "keep waiting" — the group sweep wakes it.
func (x *ParallelGroupExecutor) recoverLostWake(ctx context.Context, node workflow.Node, filesMode bool, runID uuid.UUID, instanceID uuid.UUID) (*NodeResult, bool, error) {
	run, err := x.groups.Get(ctx, runID)
	if err != nil {
		slog.Error("parallel_group: re-reading the group after suspending failed; relying on the group sweep",
			"nodeID", node.ID, "groupRunID", runID, "error", err)
		return nil, false, nil
	}
	ready := run.State == instance.GroupRunDone ||
		(filesMode && x.gatherer != nil && gatherableGroupState(run.State) && run.ActionsApplied)
	if !ready {
		return nil, false, nil
	}
	claimed, err := x.claims.ClaimWaitingParent(ctx, instanceID, node.ID)
	if err != nil {
		slog.Error("parallel_group: claiming the parent back from waiting failed; relying on the group sweep",
			"nodeID", node.ID, "instanceID", instanceID, "error", err)
		return nil, false, nil
	}
	if !claimed {
		return nil, false, nil
	}
	res, err := x.resolve(ctx, node, filesMode, run, instanceID)
	return res, true, err
}

func (x *ParallelGroupExecutor) reread(ctx context.Context, run *instance.GroupRun) *instance.GroupRun {
	fresh, err := x.groups.Get(ctx, run.ID)
	if err != nil {
		slog.Warn("parallel_group: re-reading the group run failed", "groupRunID", run.ID, "error", err)
		return run
	}
	return fresh
}

func gatherableGroupState(s instance.GroupRunState) bool {
	return s == instance.GroupRunDecided || s == instance.GroupRunGathering || s == instance.GroupRunResolvingConflicts
}

func groupWaitOutput(run *instance.GroupRun) map[string]any {
	return map[string]any{"groupRunId": run.ID.String(), "state": string(run.State)}
}

// revalidateGroup re-runs the Phase 1 ownership analysis over the run's own
// snapshot. Any non-advisory problem refuses the node before anything starts.
func revalidateGroup(nodeID string, snapshot map[string]any) (*workflow.GroupScope, error) {
	wf, _, _, err := parseWorkflowGraph(snapshot)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: parse workflow snapshot: %w", nodeID, err)
	}
	graph, problems := workflow.AnalyzeGroups(wf)
	var hard []string
	for _, p := range problems {
		if p.Severity == "warning" {
			continue
		}
		hard = append(hard, fmt.Sprintf("%s (node %q): %s", p.Code, p.NodeID, p.Message))
	}
	if len(hard) > 0 {
		return nil, fmt.Errorf("parallel_group %q: the run's workflow snapshot fails parallel-group validation, so no task was started: %s",
			nodeID, strings.Join(hard, "; "))
	}
	if graph == nil || graph.Groups[nodeID] == nil {
		return nil, fmt.Errorf("parallel_group %q: the run's workflow snapshot has no group analysis for this node", nodeID)
	}
	return graph.Groups[nodeID], nil
}
```

- [x] Step 4: Run it, confirm it passes (race detector on: the integration test runs real engine goroutines).

Completion pass after P2-T14 (`fd8d000`): the focused executor tests and real Engine + SQLite persisted-parent-output integration pass with `-race`. `initDomain` now constructs and registers the executor with real repositories, launcher, coordinator, claims and queue before freezing. Both group forms and the entrypoint group pass through the real composition root with `-race`, asserting nonempty persisted task results and parent continuation. The receipt and wiring blockers are resolved; no new implementation was needed in this verification pass.

Follow-up selection pass: atomic terminal-receipt triggers/clock remain absent. Skipped re-failing this task and completed the independent P2-T12 subtree-cancellation slice instead; its wider gate also encountered this existing integration failure.

Next selection pass: confirmed the receipt clock/triggers are still absent; skipped re-failing P2-T11 and implemented P2-T13's independent terminal-dispatch steps. Its focused dispatch and race tests pass; its wider gate reproduces the same missing-receipt failure on unchanged HEAD `b7b19e1`.

> RESOLVED (P2-T16 and P2-T14; historical evidence follows): The real Engine + SQLite sink test reaches terminal branch children, but `GroupCoordinator.Reconcile` reports `terminal group task "a" has no atomic receipt; verify terminal persistence wiring`. P2-T16 is required by the alignment index and coordinator but is NOT implemented: no `group_terminal_test.go`, `group_terminal_clock`, or `group_task_terminal_*` triggers exist. Its plan uses numbered steps, not `- [ ]`, so checkbox-based selection incorrectly treats it as complete. Implement P2-T16 before retrying this step; do not fabricate receipts in the integration test. All executor unit tests (including receipt-before-callback fixtures) pass with `-race`.

  The integration also exposed that `InstanceRepo.Create` discarded branch `CurrentNodeID`. Added a red INSERT-observer regression test, then included `current_node_id` in the creating statement and matching SQL source. This necessary runtime-input fix is green. Self-review found and fixed lost-wake gather errors incorrectly escaping as node errors (red → green regression); recovery now re-suspends and notifies capacity as required.

```bash
go test ./domain/engine/ -run 'TestParallelGroupExecutor|TestNewParallelGroupExecutor' -race -count=1 -v
```
Expected:
```
--- PASS: TestParallelGroupExecutor_FirstEntryLaunchesOnceAndWaitsWithoutHoldingASlot (0.00s)
--- PASS: TestParallelGroupExecutor_DoneRoutesBothPortsWithTheSameOutputShape (0.00s)
...
--- PASS: TestParallelGroupExecutor_EngineIntegration_ParentVariablesHoldOrderedResultsWithoutMerging (0.1Xs)
--- PASS: TestNewParallelGroupExecutor_PanicsOnAMissingCollaborator (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```

- [x] Step 5: Run the whole package and the repository gate, gofmt and vet.

Completion pass: ran both specified broad gates. The non-race engine suite passes; the full engine race gate still reports the existing spawn-stamping fixture races (`TestTrigger_PartialSpawnLinkIsNotStamped` / `TestTrigger_SpawnLinkAndOriginCoexist`). `go test ./... -count=1` passes every package except the existing `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` unknown-profile failure. These unrelated reds are tolerated by the mission; they do not block this task's green focused race and production-root sink proofs. Formatting, vet and diff checks are clean.

> RESOLVED (receipt failure); unrelated broad-suite concerns persist as recorded above. Historical evidence: Both engine and repository gates were run and include the Step 4 missing-receipt integration failure. `go test ./... -count=1` also fails `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (unknown sample profiles); the engine race gate also detects the existing `TestTrigger_NoSpawnLinkCreatesRoot` race reported in `TestTrigger_SpawnLinkAndOriginCoexist`. Both unrelated failures were reproduced from unchanged HEAD `1d996c3` in a temporary `git archive` checkout (config focused run; trigger race run with `-count=10`).

  Unblocked checks: executor unit tests passed `-race -count=10` with only the explicitly blocked engine integration excluded; all SQLite tests passed `-race -count=1`; `gofmt -l`, `go vet ./domain/engine/`, and `git diff --check` were clean.

  Initial implementation self-review (historical; receipt and root blockers are now resolved): (1) spec compliance: kept the executor slice and planned P2-T14/P3-T11 composition seams; corrected recovery-error fallback with red/green tests; only additional production change is preserving the required branch-entry input at INSERT. (2) anti-green-theater: real Engine, StartGroupTask, launcher/coordinator and SQLite repositories drive actual persisted parent-variable assertions; this test stays red, not skipped or supplied fabricated receipts, until P2-T16 exists; unused spawn fake operations now error. (3) code quality: checked neighboring spawn/coordinator executors and storage patterns, formatted/vetted changes, and reran unit/storage race tests.

```bash
go test ./domain/engine/ -race -count=1
go test ./... -count=1
gofmt -l domain/engine/executor_group.go domain/engine/executor_group_test.go
go vet ./domain/engine/
```
Expected: every package `ok`; `gofmt -l` and `go vet` print nothing.

Completion self-review, in the required order:
1. Spec compliance: reviewed `executor_group.go` against the six Execute contracts and neighboring `executor_spawn.go`; the planned results-only behavior, lost-wake fallback, frozen output and future gatherer seam remain intact. No extra features or production changes were added.
2. Anti-green-theater: reviewed the real Engine/SQLite sink and real-root branch/workflow tests: they assert exact nonempty persisted results, inherited parent data, isolated child data and exactly-once continuation. Temporarily removed only the production executor registration; the entrypoint root test failed because its real child never reached the gate. Restored registration and reran all three root sink tests with `-race`, green. No mutation remains.
3. Code quality: checked adjacent spawn/repository conventions, formatting and vet; no cleanup was necessary. Reran focused executor race tests and all three root tests after review, green; `git diff --check` is clean.

- [x] Step 6: Commit.

  The initial commit contained the unblocked implementation, regression tests and blocker evidence. This completion pass closes Steps 4–5 after their dependencies landed; the follow-up commit records verification and self-review, without changing production behavior.

```bash
git add domain/engine/executor_group.go domain/engine/executor_group_test.go
git commit -m "feat(engine): parallel_group executor launches once, waits without a slot, and routes the frozen result" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
