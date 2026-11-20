package engine_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
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
	return nil, errors.New("unexpected spawn lookup in group executor")
}

func (c *nodeClaims) ListWaitingOnTerminalChild(context.Context) ([]instance.SpawnWaitCandidate, error) {
	return nil, errors.New("unexpected spawn sweep in group executor")
}

func (c *nodeClaims) ListRunningOnTerminalChild(context.Context) ([]instance.SpawnWaitCandidate, error) {
	return nil, errors.New("unexpected spawn sweep in group executor")
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

// SuspendRunning models the real conditional UPDATE (state = running) and goes
// through the mock's UpdateState so its hooks and injected errors still fire.
func (c *nodeClaims) SuspendRunning(ctx context.Context, id uuid.UUID) (bool, error) {
	c.instances.mu.Lock()
	inst, ok := c.instances.instances[id]
	running := ok && inst.State == instance.StateRunning
	c.instances.mu.Unlock()
	if !running {
		return false, nil
	}
	if err := c.instances.UpdateState(ctx, id, instance.StateWaiting); err != nil {
		return false, err
	}
	return true, nil
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
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(), OwnerID: owner, WorkflowName: wf.Name, WorkflowSnapshot: gtSnapshot(t, wf),
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
	f.canceller.onTerminal = func(id uuid.UUID) { f.captureReceipt(t, id) }
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
	inst := f.instances.instances[id]
	require.NotNil(t, inst)
	now := time.Now()
	inst.State = state
	inst.Variables = vars
	inst.CompletedAt = &now
	inst.UpdatedAt = now
	f.instances.mu.Unlock()
	f.captureReceipt(t, id)
}

// captureReceipt models the terminal writer, not coordinator callback intake.
// The engine integration below uses SQLite's actual atomic terminal trigger.
func (f *gxFixture) captureReceipt(t *testing.T, id uuid.UUID) {
	t.Helper()
	writer := &coordFixture{instances: f.instances, groups: f.groups, nodeExecs: f.nodeExecs}
	writer.captureReceipt(t, id)
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

func TestParallelGroupExecutor_LostWakeReadErrorDegradesToWaiting(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	f.instances.onUpdateState = func(_ uuid.UUID, st instance.State) {
		if st == instance.StateWaiting {
			f.groups.failOnce("Get", errors.New("database is locked"))
		}
	}

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait)
	assert.Equal(t, instance.StateWaiting, f.parentState())
	assert.Zero(t, f.claims.attemptCount())
	assert.Equal(t, 1, f.qm.notifyCount())
}

func TestParallelGroupExecutor_LostWakeGatherErrorDegradesToWaiting(t *testing.T) {
	cfg := gxBranches()
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles
	cfg.ResolverProfile = "resolver"
	f := newGxFixture(t, gxWorkflowWith(cfg, false), "g")
	ws := newDirWorkspaces(t)
	parentDir, err := ws.EnsureInstanceDir(f.parent.ID)
	require.NoError(t, err)
	f.launcher = engine.NewGroupLauncher(f.groups, f.starter, nil, ws, nil, fixedIdentityResolver{}, f.canceller, f.bus)
	f.launcher.SetFileWorkingArea(launchFileArea{newCopyGroupWorkspace(t)})
	f.x = engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, f.coord, f.claims, f.qm)
	vars := gxVars(t)
	vars.SetReserved("workspace", parentDir)
	g := &fakeGatherer{err: errors.New("temporary gather failure")}
	f.x.SetGatherer(g)
	f.nodeExecs.onCreate = func(*instance.NodeExecution) {
		f.finishAll(t, instance.StateCompleted, instance.StateCompleted)
	}

	res, err := f.x.Execute(engine.ContextWithInstanceID(context.Background(), f.parent.ID), gxNode(t, f.wf, "g"), vars, f.wf.Name)
	require.NoError(t, err, "errors in lost-wake recovery must leave a recoverable wait")
	require.NotNil(t, res)
	assert.True(t, res.IsWait)
	assert.Equal(t, instance.StateWaiting, f.parentState())
	assert.Equal(t, 1, f.claims.wonCount())
	assert.Equal(t, 1, f.qm.notifyCount())
	require.Len(t, g.calls, 1)
	assert.Equal(t, gatherCall{RunID: f.run(t).ID, ParentID: f.parent.ID}, g.calls[0])
	assert.False(t, f.run(t).ContinuationDelivered)
}

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
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(), OwnerID: f.owner, WorkflowName: wf.Name, WorkflowSnapshot: gtSnapshot(t, wf),
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
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	repo := adaptsqlite.NewInstanceRepo(db)
	nodeExecs := adaptsqlite.NewNodeExecutionRepo(db)
	users := adaptsqlite.NewUserRepo(db)
	bus := newMockEventBus()
	exec := newMockExecutor()
	qm := newMockQueueManager()
	eng := wireModelPins(engine.NewEngine(wfRepo, repo, nodeExecs, adaptsqlite.NewVariableSnapshotRepo(db), users,
		map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: exec}, bus, qm, adaptsqlite.NewQueueRepo(db), ""))
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

	groups := adaptsqlite.NewGroupRunRepo(db)
	ids := fixedIdentityResolver{}
	launcher := engine.NewGroupLauncher(groups, eng, eng, nil, nil, ids, eng, bus)
	coord := engine.NewGroupCoordinator(groups, repo, adaptsqlite.NewQueueRepo(db), eng, ids, bus, qm)
	eng.RegisterExecutor(workflow.NodeTypeParallelGroup,
		engine.NewParallelGroupExecutor(repo, nodeExecs, groups, launcher, coord, repo, qm))
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

	owner, err := users.GetDefaultUser(ctx)
	require.NoError(t, err)
	parentID, _, err := eng.Trigger(ctxAs(owner), wf.Name, map[string]any{"repo": "acme/widgets"}, "normal")
	require.NoError(t, err)

	gxWaitForInstance(t, repo, parentID, instance.StateWaiting)
	run, err := groups.GetByParentNode(ctx, parentID, "g")
	require.NoError(t, err)
	require.NotNil(t, run)
	for _, task := range run.Tasks {
		child, err := repo.Get(ctx, task.ChildInstanceID)
		require.NoError(t, err, "task %s was created under its reserved id", task.Key)
		assert.Equal(t, instance.GroupTaskKindBranch, child.GroupTaskKind)
		assert.Equal(t, owner, child.OwnerID)
	}

	releaseOnce.Do(func() { close(release) })
	for _, task := range run.Tasks {
		gxWaitForInstance(t, repo, task.ChildInstanceID, instance.StateCompleted)
	}

	// The sweep is the correctness authority (P2-T13 adds in-process dispatch).
	require.NoError(t, coord.Reconcile(ctx))
	gxWaitForInstance(t, repo, parentID, instance.StateQueued)
	require.NoError(t, eng.StartInstance(ctx, parentID)) // what the queue evaluator does
	parent := gxWaitForInstance(t, repo, parentID, instance.StateCompleted)

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
	delivered, err := groups.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, delivered.ContinuationDelivered)
	afterCalls := 0
	for _, call := range exec.getCalls() {
		if call.NodeID == "after" {
			afterCalls++
		}
	}
	assert.Equal(t, 1, afterCalls, "the parent continued exactly once")
}

func gxWaitForInstance(t *testing.T, repo instance.InstanceRepository, id uuid.UUID, state instance.State) *instance.Instance {
	t.Helper()
	var got *instance.Instance
	matched := assert.Eventually(t, func() bool {
		var err error
		got, err = repo.Get(context.Background(), id)
		return err == nil && got.State == state
	}, 5*time.Second, 10*time.Millisecond, "instance %s did not reach %s", id, state)
	require.True(t, matched, "last instance: %+v", got)
	return got
}

func TestNewParallelGroupExecutor_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newGxFixture(t, gxWorkflow(), "g")
	build := []func(){
		func() {
			engine.NewParallelGroupExecutor(nil, f.nodeExecs, f.groups, f.launcher, f.coord, f.claims, f.qm)
		},
		func() {
			engine.NewParallelGroupExecutor(f.instances, nil, f.groups, f.launcher, f.coord, f.claims, f.qm)
		},
		func() {
			engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, nil, f.launcher, f.coord, f.claims, f.qm)
		},
		func() {
			engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, nil, f.coord, f.claims, f.qm)
		},
		func() {
			engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, nil, f.claims, f.qm)
		},
		func() {
			engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, f.coord, nil, f.qm)
		},
		func() {
			engine.NewParallelGroupExecutor(f.instances, f.nodeExecs, f.groups, f.launcher, f.coord, f.claims, nil)
		},
	}
	for i, b := range build {
		assert.Panics(t, b, "collaborator %d", i)
	}
}
