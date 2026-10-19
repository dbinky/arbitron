package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var gcT0 = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

// memWaker implements instance.NodeAddressedWaker over the mock instance repo
// with the real predicate: state = waiting AND current_node_id = nodeID.
type memWaker struct {
	instances *mockInstanceRepo
	mu        sync.Mutex
	calls     []string
	woken     []uuid.UUID
	failNext  error
}

var _ instance.NodeAddressedWaker = (*memWaker)(nil)

func (w *memWaker) SetQueuedIfWaitingAtNode(_ context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error) {
	w.mu.Lock()
	w.calls = append(w.calls, id.String()+"|"+nodeID)
	err := w.failNext
	w.failNext = nil
	w.mu.Unlock()
	if err != nil {
		return false, err
	}
	w.instances.mu.Lock()
	inst, ok := w.instances.instances[id]
	match := ok && inst.State == instance.StateWaiting && inst.CurrentNodeID != nil && *inst.CurrentNodeID == nodeID
	if match {
		q := queuedAt
		inst.State = instance.StateQueued
		inst.QueuedAt = &q
	}
	w.instances.mu.Unlock()
	if !match {
		return false, nil
	}
	w.mu.Lock()
	w.woken = append(w.woken, id)
	w.mu.Unlock()
	return true, nil
}

func (w *memWaker) wokenCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.woken)
}

func (w *memWaker) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

type coordFixture struct {
	groups     *memGroupRuns
	instances  *mockInstanceRepo
	nodeExecs  *mockNodeExecutionRepo
	waker      *memWaker
	canceller  *recordingCanceller
	identities *fakeIdentityResolver
	bus        *mockEventBus
	qm         *mockQueueManager
	coord      *engine.GroupCoordinator
	owner      uuid.UUID
	parentID   uuid.UUID
}

func newCoordFixture(t *testing.T) *coordFixture {
	t.Helper()
	owner := uuid.New()
	instances := newMockInstanceRepo()
	g := "g"
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "parent-wf", State: instance.StateWaiting,
		Priority: instance.PriorityNormal, CurrentNodeID: &g,
	}
	require.NoError(t, instances.Create(context.Background(), parent))
	f := &coordFixture{
		groups:    newMemGroupRuns(),
		instances: instances,
		nodeExecs: newMockNodeExecutionRepo(),
		waker:     &memWaker{instances: instances},
		canceller: &recordingCanceller{instances: instances},
		identities: &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
			owner: {UserID: owner, Role: identity.RoleUser, Departments: []string{"engineering"}},
		}},
		bus:      newMockEventBus(),
		qm:       newMockQueueManager(),
		owner:    owner,
		parentID: parent.ID,
	}
	f.canceller.onTerminal = func(id uuid.UUID) { f.captureReceipt(t, id) }
	f.coord = engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, f.identities, f.bus, f.qm)
	return f
}

// seedRunWith persists a running group run whose tasks are all launched, plus
// one running child row per task; mutate may adjust the run before it is stored.
func (f *coordFixture) seedRunWith(t *testing.T, rule, behavior string, mutate func(*instance.GroupRun), keys ...string) *instance.GroupRun {
	t.Helper()
	run := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: f.parentID, NodeID: "g", Attempt: 1,
		Form: workflow.GroupFormBranches, CompletionRule: rule, UnfinishedBehavior: behavior,
		GatherMode: workflow.GroupGatherResults, State: instance.GroupRunRunning,
		StartVariables: map[string]any{}, CreatedAt: gcT0, UpdatedAt: gcT0,
	}
	for i, k := range keys {
		childID := uuid.New()
		parentID, parentNode, runID, key := f.parentID, "g", run.ID, k
		require.NoError(t, f.instances.Create(context.Background(), &instance.Instance{
			ID: childID, OwnerID: f.owner, WorkflowName: "parent-wf", State: instance.StateRunning,
			ParentInstanceID: &parentID, ParentNodeID: &parentNode, SpawnMode: instance.SpawnModeGroupTask,
			GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
			Variables: map[string]any{"workspace": "/ws/" + k}, CreatedAt: gcT0, UpdatedAt: gcT0,
		}))
		run.Tasks = append(run.Tasks, instance.GroupTask{
			GroupRunID: run.ID, Position: i, Key: k, Kind: instance.GroupTaskKindBranch, Origin: k + "1",
			ChildInstanceID: childID, LaunchState: instance.GroupTaskLaunched,
			LiveStatus: instance.GroupTaskStatusRunning, Blocking: true,
		})
	}
	if mutate != nil {
		mutate(run)
	}
	f.groups.seedRun(run)
	return f.groups.get(run.ID)
}

func (f *coordFixture) seedRun(t *testing.T, rule, behavior string, keys ...string) *instance.GroupRun {
	t.Helper()
	return f.seedRunWith(t, rule, behavior, nil, keys...)
}

func gcChild(run *instance.GroupRun, key string) uuid.UUID {
	for _, task := range run.Tasks {
		if task.Key == key {
			return task.ChildInstanceID
		}
	}
	panic("no task " + key)
}

func gcTask(run *instance.GroupRun, key string) instance.GroupTask {
	for _, task := range run.Tasks {
		if task.Key == key {
			return task
		}
	}
	panic("no task " + key)
}

// finish moves a child row to a terminal (or any) state with final variables,
// as Engine.run's SetCompleted / SetFailed / Cancel would.
func (f *coordFixture) finish(t *testing.T, run *instance.GroupRun, key string, state instance.State, vars map[string]any, at time.Time) {
	t.Helper()
	id := gcChild(run, key)
	f.instances.mu.Lock()
	inst := f.instances.instances[id]
	require.NotNil(t, inst)
	inst.State = state
	if vars != nil {
		inst.Variables = vars
	}
	inst.UpdatedAt = at
	if state.IsTerminal() {
		a := at
		inst.CompletedAt = &a
	}
	if state == instance.StateFailed {
		n := key + "1"
		inst.FailedNodeID = &n
	}
	f.instances.mu.Unlock()
	if state.IsTerminal() {
		f.captureReceipt(t, id)
	}
}

// captureReceipt models the database terminal trigger, BEFORE any callback.
// The real SQLite integration test proves the two writes share a transaction.
func (f *coordFixture) captureReceipt(t *testing.T, id uuid.UUID) {
	t.Helper()
	child, err := f.instances.Get(context.Background(), id)
	require.NoError(t, err)
	if !child.State.IsTerminal() || child.GroupRunID == nil || child.GroupTaskKey == nil {
		return
	}
	result := map[string]any{}
	for key, value := range child.Variables {
		if key != "workspace" {
			result[key] = value
		}
	}
	var failure map[string]any
	if child.State != instance.StateCompleted {
		node, message, attempt := "", "", 0
		if child.FailedNodeID != nil {
			node = *child.FailedNodeID
		}
		rows, err := f.nodeExecs.ListByInstance(context.Background(), id)
		require.NoError(t, err)
		for _, row := range rows {
			if row.Status == instance.NodeStatusFailed && (node == "" || node == row.NodeID) && row.Attempt >= attempt {
				attempt = row.Attempt
				message, _ = row.Error["error"].(string)
			}
		}
		failure = map[string]any{"state": string(child.State), "failedNodeId": node, "error": message, "attempt": attempt}
	}
	_, _, err = f.groups.RecordTaskTerminal(context.Background(), *child.GroupRunID, *child.GroupTaskKey, instance.GroupTaskStatusFromState(child.State), result, failure, child.UpdatedAt)
	require.NoError(t, err)
}

func (f *coordFixture) failedExec(t *testing.T, childID uuid.UUID, nodeID string, attempt int, msg string) {
	t.Helper()
	require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: childID, NodeID: nodeID, Attempt: attempt,
		Status: instance.NodeStatusFailed, Error: map[string]any{"error": msg},
		StartedAt: gcT0.Add(time.Duration(attempt) * time.Second),
	}))
}

func (f *coordFixture) setParent(t *testing.T, state instance.State, node string) {
	t.Helper()
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	p := f.instances.instances[f.parentID]
	p.State = state
	p.CurrentNodeID = &node
}

func (f *coordFixture) parentState() instance.State {
	return f.instances.get(f.parentID).State
}

func (f *coordFixture) events(typ string) []engine.Event {
	return f.bus.eventsOfType(f.parentID, typ)
}

func (f *coordFixture) unsettled(t *testing.T) []uuid.UUID {
	t.Helper()
	ids, err := f.groups.ListUnsettled(context.Background())
	require.NoError(t, err)
	return ids
}

// ---------- happy path ----------

func TestGroupCoordinator_AllSucceed_DecidesFinalizesAndWakesOnce(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a", "b")

	f.finish(t, run, "a", instance.StateCompleted, map[string]any{"x": "from-a", "workspace": "/ws/a"}, gcT0.Add(time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))

	mid := f.groups.get(run.ID)
	assert.Nil(t, mid.Decision, "b is still running")
	a := gcTask(mid, "a")
	require.NotNil(t, a.TerminalSeq)
	assert.Equal(t, instance.GroupTaskStatusCompleted, a.LiveStatus)
	assert.Equal(t, map[string]any{"x": "from-a"}, a.Result, "the child's final variables minus the reserved workspace")
	assert.Equal(t, instance.StateWaiting, f.parentState())
	assert.Equal(t, 0, f.waker.wokenCount())

	f.finish(t, run, "b", instance.StateCompleted, map[string]any{"y": float64(2)}, gcT0.Add(2*time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "b"))

	got := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupRunDone, got.State)
	require.NotNil(t, got.Decision)
	assert.Equal(t, "success", got.Decision.Outcome)
	assert.Equal(t, "all tasks completed", got.Decision.Reason)
	assert.True(t, got.ActionsApplied)
	assert.True(t, got.ContinuationReady)
	assert.False(t, got.ContinuationDelivered, "delivery is the executor's to record")
	assert.Equal(t, workflow.GroupPortSuccess, got.ContinuationPort)

	tasks := got.FinalOutput["tasks"].([]any)
	require.Len(t, tasks, 2)
	assert.Equal(t, "a", tasks[0].(map[string]any)["key"])
	assert.Equal(t, map[string]any{"x": "from-a"}, tasks[0].(map[string]any)["result"])
	assert.Equal(t, "b", tasks[1].(map[string]any)["key"])

	assert.Equal(t, instance.StateQueued, f.parentState())
	assert.Equal(t, 1, f.waker.wokenCount())
	assert.Equal(t, 1, f.qm.notifyCount())
	assert.NotNil(t, gcTask(f.groups.get(run.ID), "b").TerminalSeq, "terminal progress comes from the durable receipt")
	require.Len(t, f.events(engine.EventTypeGroupDecided), 1)
	require.Len(t, f.events(engine.EventTypeGroupCompleted), 1)
	data := f.events(engine.EventTypeGroupDecided)[0].Data.(map[string]any)
	assert.Equal(t, run.ID.String(), data["groupRunId"])
	assert.Equal(t, "g", data["nodeId"])
	assert.Equal(t, "success", data["outcome"])
}

// ---------- success ----------

func TestGroupCoordinator_FirstSuccess_EarliestTerminalWinsRegardlessOfCallbackOrder(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, "a", "b")
	// Wall-clock timestamps can disagree with commit order; receipts win.
	f.finish(t, run, "a", instance.StateCompleted, map[string]any{"v": "a"}, gcT0.Add(2*time.Second))
	f.finish(t, run, "b", instance.StateCompleted, map[string]any{"v": "b"}, gcT0.Add(time.Second))

	// b's callback is delivered first, a's is delayed.
	f.coord.OnTaskTerminal(context.Background(), gcChild(run, "b"))

	got := f.groups.get(run.ID)
	require.NotNil(t, got.Decision)
	assert.Equal(t, "a", got.Decision.WinnerKey, "the earliest TERMINAL wins, not the earliest callback")
	assert.Less(t, *gcTask(got, "a").TerminalSeq, *gcTask(got, "b").TerminalSeq)
	assert.Equal(t, "a", got.FinalOutput["winner"])
}

func TestGroupCoordinator_FirstSuccess_ALaterCompletionNeverChangesTheOutcome(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, "a", "b")

	f.finish(t, run, "b", instance.StateCompleted, map[string]any{"v": "b"}, gcT0.Add(time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "b"))
	first := f.groups.get(run.ID)
	require.NotNil(t, first.Decision)
	assert.Equal(t, "b", first.Decision.WinnerKey)
	assert.True(t, gcTask(first, "a").ContinuingIndependently, "the non-winner keeps running, no longer blocking")
	assert.False(t, gcTask(first, "a").CancelRequested)
	assert.Equal(t, instance.GroupTaskStatusUnfinished, first.Decision.Tasks[0].Status)

	f.finish(t, run, "a", instance.StateCompleted, map[string]any{"v": "a"}, gcT0.Add(2*time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))

	later := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupTaskStatusCompleted, gcTask(later, "a").LiveStatus, "live status follows the late terminal")
	require.NotNil(t, gcTask(later, "a").TerminalSeq)
	assert.Equal(t, first.Decision, later.Decision, "the frozen decision is immutable")
	assert.Equal(t, first.FinalOutput, later.FinalOutput, "the gathered outcome is immutable")
	assert.Len(t, f.events(engine.EventTypeGroupDecided), 1)
	assert.Len(t, f.events(engine.EventTypeGroupCompleted), 1)
	assert.NotNil(t, gcTask(f.groups.get(run.ID), "b").TerminalSeq, "terminal progress comes from the durable receipt")
	assert.Equal(t, 1, f.waker.wokenCount(), "a late terminal never triggers another continuation")
}

func TestGroupCoordinator_FirstSuccess_CancelUnfinishedCancelsOnlyTheUnfinished(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, "a", "b", "c")
	f.finish(t, run, "a", instance.StateCancelled, nil, gcT0.Add(1*time.Second))
	f.finish(t, run, "b", instance.StateCompleted, map[string]any{"v": "b"}, gcT0.Add(2*time.Second))

	f.coord.OnTaskTerminal(context.Background(), gcChild(run, "b"))

	got := f.groups.get(run.ID)
	assert.Equal(t, "b", got.Decision.WinnerKey, "a cancelled task never wins, even when it finished first")
	assert.Empty(t, f.canceller.callsFor(gcChild(run, "a")), "a terminal task is not cancelled again")
	calls := f.canceller.callsFor(gcChild(run, "c"))
	require.Len(t, calls, 1)
	assert.Equal(t, f.owner, calls[0].Identity.UserID, "cancellation runs under the reconstructed owner identity")
	assert.True(t, gcTask(got, "c").CancelRequested)
	assert.Equal(t, instance.GroupTaskStatusUnfinished, got.Decision.Tasks[2].Status)
	assert.True(t, got.Decision.Tasks[2].CancelRequested)
	assert.True(t, got.ActionsApplied)
	assert.Equal(t, instance.GroupRunDone, got.State)
}

// ---------- failure ----------

func TestGroupCoordinator_AllSucceed_WaitForRemaining_FailureKeepsResultsAndDetails(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a", "b")

	f.failedExec(t, gcChild(run, "a"), "a1", 1, "first try")
	f.failedExec(t, gcChild(run, "a"), "a1", 2, "boom")
	f.finish(t, run, "a", instance.StateFailed, map[string]any{"partial": "data", "workspace": "/ws/a"}, gcT0.Add(time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))

	mid := f.groups.get(run.ID)
	assert.Nil(t, mid.Decision, "wait_for_remaining waits for b")
	assert.Equal(t, instance.GroupRunRunning, mid.State)
	assert.Equal(t, map[string]any{"partial": "data"}, gcTask(mid, "a").Result, "a failed task keeps its available variables")
	assert.Equal(t, map[string]any{"state": "failed", "failedNodeId": "a1", "error": "boom", "attempt": 2}, gcTask(mid, "a").Failure,
		"failure details come from the LATEST failed node execution")
	assert.Equal(t, 0, f.canceller.count())

	f.finish(t, run, "b", instance.StateCompleted, map[string]any{"ok": true}, gcT0.Add(2*time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "b"))

	got := f.groups.get(run.ID)
	assert.Equal(t, "failure", got.Decision.Outcome)
	assert.Equal(t, "task a failed", got.Decision.Reason)
	assert.Equal(t, workflow.GroupPortFailure, got.ContinuationPort)
	first := got.FinalOutput["tasks"].([]any)[0].(map[string]any)
	assert.Equal(t, "failed", first["status"])
	assert.Equal(t, "boom", first["failure"].(map[string]any)["error"])
	assert.Equal(t, "data", first["result"].(map[string]any)["partial"])
	assert.Equal(t, instance.StateQueued, f.parentState())
}

func TestGroupCoordinator_AllSucceed_CancelUnfinishedRequestsCancellationWithoutClaimingIt(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, "a", "b")
	f.finish(t, run, "a", instance.StateFailed, map[string]any{}, gcT0.Add(time.Second))

	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))

	got := f.groups.get(run.ID)
	assert.Equal(t, "failure", got.Decision.Outcome, "cancel_unfinished decides at the first non-success")
	assert.Equal(t, instance.GroupTaskStatusUnfinished, got.Decision.Tasks[1].Status, "requested, not confirmed")
	assert.True(t, got.Decision.Tasks[1].CancelRequested)
	assert.True(t, gcTask(got, "b").CancelRequested)
	require.Len(t, f.canceller.callsFor(gcChild(run, "b")), 1)
	assert.Equal(t, instance.GroupRunDone, got.State)
	assert.Equal(t, instance.StateQueued, f.parentState())

	// The confirmed cancellation arrives later and only moves live status.
	f.coord.OnTaskTerminal(ctx, gcChild(run, "b"))
	after := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupTaskStatusCancelled, gcTask(after, "b").LiveStatus)
	assert.Equal(t, got.Decision, after.Decision)
	assert.Equal(t, got.FinalOutput, after.FinalOutput)
}

func TestGroupCoordinator_FirstSuccess_FailsOnlyOnceEveryCandidateFinished(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, "a", "b")

	f.finish(t, run, "a", instance.StateFailed, nil, gcT0.Add(time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))
	assert.Nil(t, f.groups.get(run.ID).Decision, "a failure does not stop the other candidate")
	assert.Equal(t, 0, f.canceller.count())

	f.finish(t, run, "b", instance.StateCancelled, nil, gcT0.Add(2*time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "b"))
	got := f.groups.get(run.ID)
	assert.Equal(t, "failure", got.Decision.Outcome)
	assert.Equal(t, "no task succeeded (1 failed, 1 cancelled)", got.Decision.Reason)
	assert.Equal(t, instance.StateQueued, f.parentState())
}

func TestGroupCoordinator_CancelledParentIsSettledAndNeverRequeued(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, "a", "b")
	f.setParent(t, instance.StateCancelled, "g")

	require.NoError(t, f.coord.Reconcile(context.Background()))

	got := f.groups.get(run.ID)
	require.NotNil(t, got.Decision)
	assert.Equal(t, "failure", got.Decision.Outcome)
	assert.Equal(t, "parent run cancelled before the group decided", got.Decision.Reason)
	assert.Equal(t, instance.GroupRunDone, got.State)
	assert.True(t, got.ActionsApplied)
	assert.True(t, got.ContinuationDelivered, "settled without any continuation")
	for _, key := range []string{"a", "b"} {
		calls := f.canceller.callsFor(gcChild(run, key))
		require.Len(t, calls, 1, "every non-terminal task is cancelled, independent ones included")
		assert.Equal(t, f.owner, calls[0].Identity.UserID)
		assert.True(t, gcTask(got, key).CancelRequested)
	}
	assert.Equal(t, instance.StateCancelled, f.parentState(), "a cancelled parent is never resurrected")
	assert.Equal(t, 0, f.waker.callCount())
	assert.Empty(t, f.unsettled(t))
}

func TestGroupCoordinator_IndependentTasksAreCancelledWhenTheParentDiesLater(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, "a", "b")
	f.finish(t, run, "a", instance.StateCompleted, map[string]any{"v": "a"}, gcT0.Add(time.Second))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))
	require.NoError(t, f.groups.MarkContinuationDelivered(ctx, run.ID))
	decided := f.groups.get(run.ID).Decision

	f.setParent(t, instance.StateCancelled, "after")
	require.NoError(t, f.coord.Evaluate(ctx, run.ID))

	assert.Len(t, f.canceller.callsFor(gcChild(run, "b")), 1)
	assert.Equal(t, decided, f.groups.get(run.ID).Decision, "stopping retained work never reopens the outcome")
}

// ---------- error: crash between steps, reconciliation finishes the work ----------

func TestGroupCoordinator_CrashBetweenDecideAndCancel_ReconcileFinishesTheActions(t *testing.T) {
	for _, c := range []struct {
		name   string
		inject func(f *coordFixture)
	}{
		{"recording the cancellation request fails", func(f *coordFixture) {
			f.groups.failOnce("MarkTaskCancelRequested", errors.New("database is locked"))
		}},
		{"the cancellation itself fails", func(f *coordFixture) {
			f.canceller.failNext = errors.New("database is locked")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newCoordFixture(t)
			ctx := context.Background()
			run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, "a", "b")
			f.finish(t, run, "a", instance.StateCompleted, map[string]any{"v": "a"}, gcT0.Add(time.Second))
			c.inject(f)

			err := f.coord.Evaluate(ctx, run.ID)
			require.Error(t, err)

			mid := f.groups.get(run.ID)
			require.NotNil(t, mid.Decision, "the decision is durable even though its actions are not")
			assert.Equal(t, instance.GroupRunDecided, mid.State)
			assert.False(t, mid.ActionsApplied)
			assert.Nil(t, mid.FinalOutput, "nothing is finalized before the actions are applied")
			assert.Equal(t, instance.StateWaiting, f.parentState())
			assert.Contains(t, f.unsettled(t), run.ID)

			require.NoError(t, f.coord.Reconcile(ctx))

			got := f.groups.get(run.ID)
			assert.Equal(t, instance.GroupRunDone, got.State)
			assert.True(t, got.ActionsApplied)
			assert.True(t, gcTask(got, "b").CancelRequested)
			assert.Equal(t, instance.StateCancelled, f.instances.get(gcChild(run, "b")).State)
			assert.Equal(t, mid.Decision, got.Decision, "losing nothing: the same decision, finished")
			assert.Equal(t, instance.StateQueued, f.parentState())
			assert.Len(t, f.events(engine.EventTypeGroupDecided), 1)
		})
	}
}

func TestGroupCoordinator_CrashBetweenFinalizeAndWake_ReconcileWakes(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a")
	f.finish(t, run, "a", instance.StateCompleted, map[string]any{"v": "a"}, gcT0.Add(time.Second))
	f.waker.failNext = errors.New("database is locked")

	require.NoError(t, f.coord.Evaluate(ctx, run.ID), "a failed wake is logged; the sweep retries it")
	mid := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupRunDone, mid.State)
	assert.False(t, mid.ContinuationDelivered)
	assert.Equal(t, instance.StateWaiting, f.parentState())
	assert.Contains(t, f.unsettled(t), run.ID)

	require.NoError(t, f.coord.Reconcile(ctx))
	assert.Equal(t, instance.StateQueued, f.parentState())
	assert.Equal(t, 1, f.waker.wokenCount())
	assert.Len(t, f.events(engine.EventTypeGroupCompleted), 1, "finalization is never repeated")
}

func TestGroupCoordinator_OwnerIdentityFailureLeavesActionsForTheSweep(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, "a", "b")
	f.finish(t, run, "a", instance.StateFailed, nil, gcT0.Add(time.Second))
	f.identities.err = errors.New("identity provider unavailable")

	require.Error(t, f.coord.Evaluate(ctx, run.ID))
	assert.Equal(t, 0, f.canceller.count(), "never cancel without the owner's identity (no system fallback)")
	assert.False(t, f.groups.get(run.ID).ActionsApplied)

	f.identities.err = nil
	require.NoError(t, f.coord.Reconcile(ctx))
	assert.True(t, f.groups.get(run.ID).ActionsApplied)
	assert.Len(t, f.canceller.callsFor(gcChild(run, "b")), 1)
}

func TestGroupCoordinator_FailedParentWithAnInterruptedLaunchIsCleanedUp(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRunWith(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, func(r *instance.GroupRun) {
		r.State = instance.GroupRunLaunching
		r.Tasks[1].LaunchState = instance.GroupTaskPending
		r.Tasks[1].LiveStatus = instance.GroupTaskStatusNotStarted
	}, "a", "b")
	// Crash recovery marked the running parent failed (Engine.RecoverCrashedInstances).
	f.setParent(t, instance.StateFailed, "g")

	require.NoError(t, f.coord.Reconcile(context.Background()))

	got := f.groups.get(run.ID)
	require.NotNil(t, got.Decision)
	assert.Equal(t, "parent run failed before the group decided", got.Decision.Reason)
	assert.Equal(t, "not launched: parent run failed before the group decided", got.Decision.Tasks[1].Failure["error"])
	assert.Equal(t, instance.StateCancelled, f.instances.get(gcChild(run, "a")).State)
	assert.Equal(t, instance.StateCancelled, f.instances.get(gcChild(run, "b")).State,
		"a reserved child that exists although its launch was never recorded is stopped too")
	assert.Equal(t, instance.GroupRunDone, got.State)
	assert.True(t, got.ContinuationDelivered)
	assert.Empty(t, f.unsettled(t))
	assert.Equal(t, instance.StateFailed, f.parentState())
}

// ---------- edge: the launch barrier ----------

func TestGroupCoordinator_InterruptedLaunchUnderAWaitingParentRoutesFailure(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRunWith(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, func(r *instance.GroupRun) {
		r.State = instance.GroupRunLaunching
		r.Tasks[1].LaunchState = instance.GroupTaskPending
		r.Tasks[1].LiveStatus = instance.GroupTaskStatusNotStarted
	}, "a", "b")
	f.instances.mu.Lock()
	delete(f.instances.instances, gcChild(run, "b"))
	f.instances.mu.Unlock()

	require.NoError(t, f.coord.Evaluate(context.Background(), run.ID))

	got := f.groups.get(run.ID)
	require.NotNil(t, got.Decision)
	assert.Equal(t, "launch failed: the launch was interrupted before every task started", got.Decision.Reason)
	assert.Equal(t, instance.StateCancelled, f.instances.get(gcChild(run, "a")).State,
		"a launch failure cancels launched tasks whatever the unfinished behaviour")
	assert.Equal(t, instance.GroupRunDone, got.State)
	assert.Equal(t, workflow.GroupPortFailure, got.ContinuationPort)
	assert.Equal(t, instance.StateQueued, f.parentState())
}

func TestGroupCoordinator_LaunchInProgressIsLeftAlone(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRunWith(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, func(r *instance.GroupRun) {
		r.State = instance.GroupRunLaunching
		r.Tasks[1].LaunchState = instance.GroupTaskPending
		r.Tasks[1].LiveStatus = instance.GroupTaskStatusNotStarted
	}, "a", "b")
	f.setParent(t, instance.StateRunning, "g")
	f.finish(t, run, "a", instance.StateFailed, nil, gcT0.Add(time.Second))

	require.NoError(t, f.coord.Evaluate(context.Background(), run.ID))

	got := f.groups.get(run.ID)
	assert.Nil(t, got.Decision, "no decision before the launch barrier opens")
	assert.Equal(t, instance.GroupRunLaunching, got.State)
	assert.Equal(t, instance.GroupTaskStatusFailed, gcTask(got, "a").LiveStatus, "the terminal is still taken in")
	assert.Equal(t, 0, f.canceller.count())
}

func TestGroupCoordinator_CompletesAnOpenLaunchBarrier(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRunWith(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, func(r *instance.GroupRun) {
		r.State = instance.GroupRunLaunching
	}, "a", "b")
	f.finish(t, run, "a", instance.StateCompleted, nil, gcT0.Add(time.Second))
	f.finish(t, run, "b", instance.StateCompleted, nil, gcT0.Add(2*time.Second))

	require.NoError(t, f.coord.Evaluate(context.Background(), run.ID))

	assert.Equal(t, 1, f.groups.callCount("TransitionState:launching->running"))
	got := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupRunDone, got.State)
	assert.Equal(t, "success", got.Decision.Outcome)
}

// ---------- edge: idempotency, coalescing, delivery ----------

func TestGroupCoordinator_DuplicateTerminalNotificationsAreIdempotent(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a", "b")
	f.finish(t, run, "a", instance.StateCompleted, nil, gcT0.Add(time.Second))
	f.finish(t, run, "b", instance.StateCompleted, nil, gcT0.Add(2*time.Second))

	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "a"))
	f.coord.OnTaskTerminal(ctx, gcChild(run, "b"))
	require.NoError(t, f.coord.Reconcile(ctx))

	assert.Equal(t, 1, f.groups.callCount("RecordTaskTerminal:a"))
	assert.Equal(t, 1, f.groups.callCount("RecordTaskTerminal:b"))
	assert.NotNil(t, gcTask(f.groups.get(run.ID), "b").TerminalSeq, "duplicate callbacks do not replace the receipt")
	assert.Len(t, f.events(engine.EventTypeGroupDecided), 1)
	assert.Len(t, f.events(engine.EventTypeGroupCompleted), 1)
	assert.Equal(t, 1, f.waker.wokenCount(), "continued exactly once")
	assert.Equal(t, 1, f.qm.notifyCount())
}

func TestGroupCoordinator_NestedNotificationDuringCancellationIsCoalesced(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, "a", "b", "c")
	f.finish(t, run, "a", instance.StateCompleted, nil, gcT0.Add(time.Second))
	// Engine.Cancel -> OnChildTerminal -> group dispatch re-enters the
	// coordinator synchronously while it is still applying its actions.
	f.canceller.onCancel = func(id uuid.UUID) { f.coord.OnTaskTerminal(context.Background(), id) }

	require.NoError(t, f.coord.Evaluate(context.Background(), run.ID))

	got := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupRunDone, got.State)
	for _, key := range []string{"b", "c"} {
		assert.Equal(t, instance.GroupTaskStatusCancelled, gcTask(got, key).LiveStatus,
			"the deferred notification is replayed by one more pass, not lost")
		assert.NotNil(t, gcTask(got, key).TerminalSeq)
	}
	assert.Len(t, f.events(engine.EventTypeGroupDecided), 1)
	assert.Len(t, f.events(engine.EventTypeGroupCompleted), 1)
	assert.Equal(t, 1, f.waker.wokenCount())
}

func TestGroupCoordinator_LiveStatusTracksTheChild(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a")

	f.finish(t, run, "a", instance.StatePaused, nil, gcT0.Add(time.Second))
	require.NoError(t, f.coord.Evaluate(ctx, run.ID))
	assert.Equal(t, instance.GroupTaskStatusPaused, gcTask(f.groups.get(run.ID), "a").LiveStatus,
		"a paused task is shown paused, never completed")

	f.finish(t, run, "a", instance.StateWaiting, nil, gcT0.Add(2*time.Second))
	require.NoError(t, f.coord.Evaluate(ctx, run.ID))
	assert.Equal(t, instance.GroupTaskStatusWaiting, gcTask(f.groups.get(run.ID), "a").LiveStatus)

	updates := f.events(engine.EventTypeGroupTaskUpdated)
	require.Len(t, updates, 2)
	assert.Equal(t, "a", updates[0].Data.(map[string]any)["taskKey"])
	assert.Equal(t, "paused", updates[0].Data.(map[string]any)["status"])
	assert.Nil(t, f.groups.get(run.ID).Decision)
}

func TestGroupCoordinator_ParentThatMovedOnMarksTheContinuationDelivered(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a")
	f.finish(t, run, "a", instance.StateCompleted, nil, gcT0.Add(time.Second))
	f.setParent(t, instance.StateRunning, "after")

	require.NoError(t, f.coord.Evaluate(context.Background(), run.ID))

	got := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupRunDone, got.State)
	assert.True(t, got.ContinuationDelivered)
	assert.Equal(t, 0, f.waker.wokenCount())
	assert.Empty(t, f.unsettled(t))
}

func TestGroupCoordinator_WakeParentIsNodeAddressed(t *testing.T) {
	f := newCoordFixture(t)
	ctx := context.Background()
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a")
	assert.False(t, f.coord.WakeParent(ctx, f.groups.get(run.ID)), "an undecided run never wakes its parent")

	f.setParent(t, instance.StateRunning, "g")
	f.finish(t, run, "a", instance.StateCompleted, nil, gcT0.Add(time.Second))
	require.NoError(t, f.coord.Evaluate(ctx, run.ID))
	done := f.groups.get(run.ID)
	require.Equal(t, instance.GroupRunDone, done.State)

	f.setParent(t, instance.StateWaiting, "spawn-2")
	assert.False(t, f.coord.WakeParent(ctx, done), "waiting at ANOTHER node is not this group's wait")
	assert.Equal(t, instance.StateWaiting, f.parentState())

	f.setParent(t, instance.StateWaiting, "g")
	assert.True(t, f.coord.WakeParent(ctx, done))
	assert.Equal(t, instance.StateQueued, f.parentState())
	assert.False(t, f.coord.WakeParent(ctx, done), "the second mover loses the CAS")
	assert.False(t, f.coord.WakeParent(ctx, nil))
}

func TestGroupCoordinator_ReconcileContinuesPastAFailingRun(t *testing.T) {
	f := newCoordFixture(t)
	broken := f.seedRunWith(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, func(r *instance.GroupRun) {
		r.ParentInstanceID = uuid.New() // parent row is unreadable
		r.NodeID = "g-broken"
	}, "x")
	good := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a")
	f.finish(t, good, "a", instance.StateCompleted, nil, gcT0.Add(time.Second))

	require.NoError(t, f.coord.Reconcile(context.Background()), "per-run failures are logged, not returned")
	assert.Equal(t, instance.GroupRunDone, f.groups.get(good.ID).State)
	assert.Equal(t, instance.GroupRunRunning, f.groups.get(broken.ID).State)

	f.groups.failOnce("ListUnsettled", errors.New("database is locked"))
	err := f.coord.Reconcile(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is locked")
}

func TestGroupCoordinator_OnTaskTerminalForAnUnknownChildIsANoOp(t *testing.T) {
	f := newCoordFixture(t)
	assert.NotPanics(t, func() { f.coord.OnTaskTerminal(context.Background(), uuid.New()) })
	assert.Equal(t, 0, f.waker.callCount())
	f.groups.failOnce("GetByChild", errors.New("database is locked"))
	assert.NotPanics(t, func() { f.coord.OnTaskTerminal(context.Background(), uuid.New()) })
}

func TestGroupCoordinator_FilesModeWakesAtDecidedWithoutFinalizing(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRunWith(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, func(r *instance.GroupRun) {
		r.GatherMode = workflow.GroupGatherResultsAndFiles
	}, "a")
	f.finish(t, run, "a", instance.StateCompleted, nil, gcT0.Add(time.Second))

	require.NoError(t, f.coord.Evaluate(context.Background(), run.ID))

	got := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupRunDecided, got.State, "the executor (single writer of the parent's area) gathers")
	assert.Nil(t, got.FinalOutput)
	assert.True(t, got.ActionsApplied)
	assert.Equal(t, instance.StateQueued, f.parentState())
}

func TestGroupCoordinator_MissingAtomicReceiptBlocksDecisionAndActions(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, "a", "b")
	// Simulate broken terminal persistence: a terminal row with no receipt.
	f.instances.mu.Lock()
	f.instances.instances[gcChild(run, "a")].State = instance.StateCompleted
	f.instances.mu.Unlock()

	err := f.coord.Evaluate(context.Background(), run.ID)
	require.ErrorContains(t, err, "no atomic receipt")
	got := f.groups.get(run.ID)
	assert.Nil(t, got.Decision)
	assert.Nil(t, got.FinalOutput)
	assert.Zero(t, f.canceller.count())
	assert.Zero(t, f.waker.callCount())
	assert.Zero(t, f.groups.callCount("RecordTaskTerminal:a"), "intake must not invent a receipt")
}

func TestGroupCoordinator_ReceiptCommittedAfterFirstRosterReadIsConsumed(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, "a")
	reads := 0
	f.groups.onCall = func(method string) {
		if method == "Get" {
			reads++
			if reads == 2 { // first run read was already returned without a receipt
				f.captureReceipt(t, gcChild(run, "a"))
			}
		}
	}
	f.instances.mu.Lock()
	child := f.instances.instances[gcChild(run, "a")]
	child.State = instance.StateCompleted
	child.Variables = map[string]any{"persisted": "value", "workspace": "/private"}
	f.instances.mu.Unlock()

	require.NoError(t, f.coord.Evaluate(context.Background(), run.ID))
	got := f.groups.get(run.ID)
	require.Equal(t, instance.GroupRunDone, got.State)
	tasks := got.FinalOutput["tasks"].([]any)
	assert.Equal(t, map[string]any{"persisted": "value"}, tasks[0].(map[string]any)["result"])
	assert.Equal(t, instance.StateQueued, f.parentState())
}

func TestGroupCoordinator_ReloadFailureAfterActionWriteStopsFollowUp(t *testing.T) {
	for _, method := range []string{"MarkTaskCancelRequested", "MarkTaskContinuingIndependently", "MarkActionsApplied"} {
		t.Run(method, func(t *testing.T) {
			f := newCoordFixture(t)
			behavior := workflow.GroupUnfinishedCancel
			if method == "MarkTaskContinuingIndependently" {
				behavior = workflow.GroupUnfinishedFinishIndependently
			}
			run := f.seedRun(t, workflow.GroupRuleFirstSuccess, behavior, "a", "b")
			f.finish(t, run, "a", instance.StateCompleted, nil, gcT0)
			f.groups.onCall = func(called string) {
				if called == method {
					f.groups.failOnce("Get", errors.New("reload failed"))
				}
			}

			require.ErrorContains(t, f.coord.Evaluate(context.Background(), run.ID), "reload failed")
			assert.Nil(t, f.groups.get(run.ID).FinalOutput, "no finalization from stale state")
			assert.Zero(t, f.waker.wokenCount())
			if method == "MarkTaskCancelRequested" {
				assert.Zero(t, f.canceller.count(), "reload precedes the cancellation")
			}
			f.groups.onCall = nil
			require.NoError(t, f.coord.Reconcile(context.Background()))
			assert.Equal(t, instance.GroupRunDone, f.groups.get(run.ID).State)
			assert.Equal(t, instance.StateQueued, f.parentState())
		})
	}
}

func TestGroupCoordinator_LosingACASStillFinishesTheDurableWork(t *testing.T) {
	for _, method := range []string{"Decide", "SetFinalOutput"} {
		t.Run(method, func(t *testing.T) {
			f := newCoordFixture(t)
			ctx := context.Background()
			run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, "a", "b")
			f.finish(t, run, "a", instance.StateCompleted, map[string]any{"source": "child"}, gcT0)
			f.groups.onCall = func(called string) {
				if called != method {
					return
				}
				// Another process commits before this caller's CAS executes.
				f.groups.onCall = nil
				persisted := f.groups.get(run.ID)
				if method == "Decide" {
					d, ok := engine.DecideGroup(persisted, gcT0)
					require.True(t, ok)
					won, err := f.groups.Decide(ctx, run.ID, *d)
					require.NoError(t, err)
					require.True(t, won)
				} else {
					won, err := f.groups.SetFinalOutput(ctx, run.ID, workflow.GroupPortSuccess, engine.BuildGroupOutput(persisted), instance.GroupRunDecided)
					require.NoError(t, err)
					require.True(t, won)
				}
			}

			require.NoError(t, f.coord.Evaluate(ctx, run.ID))
			got := f.groups.get(run.ID)
			assert.Equal(t, instance.GroupRunDone, got.State)
			assert.True(t, got.ActionsApplied)
			assert.Equal(t, instance.StateCancelled, f.instances.get(gcChild(run, "b")).State)
			tasks := got.FinalOutput["tasks"].([]any)
			assert.Equal(t, map[string]any{"source": "child"}, tasks[0].(map[string]any)["result"])
			assert.Equal(t, instance.StateQueued, f.parentState())
			assert.Equal(t, 1, f.qm.notifyCount())
			event := engine.EventTypeGroupDecided
			if method == "SetFinalOutput" {
				event = engine.EventTypeGroupCompleted
			}
			assert.Empty(t, f.events(event), "the losing caller must not publish the winner's event")
		})
	}
}

func TestGroupCoordinator_ConcurrentEvaluationReplaysLiveRefresh(t *testing.T) {
	f := newCoordFixture(t)
	run := f.seedRun(t, workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, "a", "b")
	f.finish(t, run, "a", instance.StateCompleted, nil, gcT0)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var once sync.Once
	f.groups.onCall = func(method string) {
		if method == "SetFinalOutput" {
			once.Do(func() { close(entered); <-release })
		}
	}
	done := make(chan error, 1)
	go func() { done <- f.coord.Evaluate(context.Background(), run.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluation did not reach finalization")
	}
	f.finish(t, run, "b", instance.StatePaused, nil, gcT0)
	// This call cannot wait on the active pass: cancellation callbacks can
	// re-enter synchronously. It must instead request another pass.
	coalesced := make(chan error, 1)
	go func() { coalesced <- f.coord.Evaluate(context.Background(), run.ID) }()
	select {
	case err := <-coalesced:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("re-entrant evaluation blocked on the active pass")
	}
	unblock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("coalesced evaluation did not finish")
	}
	got := f.groups.get(run.ID)
	assert.Equal(t, instance.GroupTaskStatusPaused, gcTask(got, "b").LiveStatus, "only the replay can refresh a change arriving during finalization")
	assert.Equal(t, instance.GroupTaskStatusUnfinished, got.Decision.Tasks[1].Status)
	assert.Equal(t, instance.StateQueued, f.parentState())
	assert.Equal(t, 1, f.waker.wokenCount())
	assert.Len(t, f.events(engine.EventTypeGroupCompleted), 1)
}

func TestNewGroupCoordinator_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newCoordFixture(t)
	build := []func(){
		func() { engine.NewGroupCoordinator(nil, f.instances, f.waker, f.canceller, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, nil, f.waker, f.canceller, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, nil, f.canceller, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, f.waker, nil, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, nil, f.bus, f.qm) },
		func() {
			engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, f.identities, nil, f.qm)
		},
		func() {
			engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, f.identities, f.bus, nil)
		},
	}
	for i, b := range build {
		assert.Panics(t, b, "collaborator %d", i)
	}
}
