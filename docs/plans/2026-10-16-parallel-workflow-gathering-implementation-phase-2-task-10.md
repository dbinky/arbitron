# Parallel Workflow Gathering — Phase 2 Task 10: GroupCoordinator

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Own every parallel-group decision durably: take in task terminals from the persisted child rows (in true terminal order, whatever order callbacks arrive in), decide exactly once by compare-and-set, apply the unfinished-task actions under the parent owner's identity, freeze and finalize the results-mode output, and wake the parent only if it is waiting at THIS group node — with every step idempotent and resumable by a sweep after a crash at any point, and with a cancelled or failed parent settled without ever being re-queued.
**Architecture:** One file, `domain/engine/group_coordinator.go`. `Evaluate(runID)` is a single pass over persisted state — intake → (dead-parent settlement | launch-barrier completion) → `DecideGroup` + `Decide` CAS → unfinished actions → `SetFinalOutput` CAS → node-addressed wake — that reads the run back after every write, so losing a CAS is never permission to skip the follow-up work. A per-run in-process gate coalesces re-entrant calls (a cancellation's own terminal callback arriving while the pass that cancelled it is still running) into one more pass. `OnTaskTerminal` maps a child to its run; `Reconcile` runs `Evaluate` over `ListUnsettled`.
**Tech Stack:** Go 1.25.5, standard library (`errors`, `sync`, `log/slog`), `github.com/google/uuid`, testify.

**Prerequisites (must be merged first):** P2-T16 (atomic terminal receipts and the shared durable clock), P1-T1 (workflow group constants), P2-T1 (`instance.GroupRun`, `GroupRunRepository`, `NodeAddressedWaker`, `GroupTaskStatusFromState`, constants), P2-T6 (`variable.DeepCopyMap`), P2-T8 (`publishGroupEvent`, `decisionEventData`, `EventTypeGroup*` in `group_events.go`; test fakes `memGroupRuns`, `recordingCanceller` in `group_fakes_test.go`), P2-T9 (`DecideGroup`, `DecideLaunchFailure`, `BuildGroupOutput`, `isTerminalGroupStatus`, `groupOutcomeSuccess`).

---

## Binding implementation revision: terminal intake

The intake implementation below consumes P2-T16 receipts in
`group_tasks`. Keep nonterminal live-state refresh, and return an error for a
terminal child with no receipt after reloading its roster (a child may have committed
between the first roster read and its child read). On any intake error, `evaluateOnce`
returns BEFORE deciding or applying actions; the periodic sweep retries.

Update the fixture `finish` to capture available variables/failure trace and call the
shared fake's receipt method when state changes, not during coordinator evaluation.
`failedExec` setup must precede `finish`. The real SQLite tests in P2-T16/P2-T14 remain
authoritative for receipt timing; mocks cannot prove commit-order correctness.


## Coordinator constructor after atomic capture

`NewGroupCoordinator(groups, instances, waker, canceller, identities, bus, queueMgr)`
has seven collaborators. The NodeExecutionRepository argument/field is removed because
P2-T16's atomic receipt writer now owns failure-trace reads. P2-T11, P2-T14 and P3-T10
use the same revised signature. Test fixtures still keep `nodeExecs` to populate receipts,
not as an unused runtime injection. Every retained collaborator has a production consumer.


## Interfaces

**Consumes (exact):**
- `domain/instance/ports.go:14-29` `InstanceRepository.Get` (child `State`, `Variables`, `CompletedAt`, `UpdatedAt`, `FailedNodeID`; parent `State`, `CurrentNodeID`, `OwnerID`); `:57-69` `NodeExecutionRepository.ListByInstance` (`NodeExecution.Status`, `Error["error"]`, `Attempt`, `NodeID`, `StartedAt`, `domain/instance/instance.go:240-258`).
- `domain/instance/instance.go:47` `State.IsTerminal`; P2-T1 `GroupTaskStatusFromState(State) GroupTaskStatus`, `NodeAddressedWaker.SetQueuedIfWaitingAtNode(ctx, id, nodeID, queuedAt) (bool, error)`, `GroupRunRepository` (all methods).
- `domain/engine/executor_spawn.go:367` `childOutput(vars map[string]any) map[string]any` (final variables minus reserved names — the SAME helper the awaited spawn uses).
- `domain/engine/orphan_reconciler.go:20` `RunCanceller`; `domain/engine/ports.go:214` `QueueManager.NotifyCapacityFreed`, `:300` `IdentityResolver`, `:279` `EventBus`.
- P2-T9 `DecideGroup`, `DecideLaunchFailure`, `BuildGroupOutput`; P2-T8 `publishGroupEvent`, `decisionEventData`, `EventTypeGroupTaskUpdated`, `EventTypeGroupDecided`, `EventTypeGroupCompleted`.

**Produces (exact):**
```go
type GroupCoordinator struct{ /* groups, instances, waker, canceller, identities, bus, queueMgr, now; in-process gate */ }
func NewGroupCoordinator(groups instance.GroupRunRepository, instances instance.InstanceRepository,
	waker instance.NodeAddressedWaker, canceller RunCanceller, identities IdentityResolver, bus EventBus, queueMgr QueueManager) *GroupCoordinator
func (c *GroupCoordinator) OnTaskTerminal(ctx context.Context, childID uuid.UUID)       // void, best-effort
func (c *GroupCoordinator) Evaluate(ctx context.Context, runID uuid.UUID) error
func (c *GroupCoordinator) Reconcile(ctx context.Context) error
func (c *GroupCoordinator) WakeParent(ctx context.Context, run *instance.GroupRun) bool
```
`*GroupCoordinator` structurally satisfies P2-T13's `GroupTerminalHandler` (`OnTaskTerminal(ctx, childID)`).

`Evaluate` contract (pinned by the tests):
1. **Intake.** Production terminal state and receipt are committed together by P2-T16.
   Reload the full roster transactionally before decision; `TerminalSeq` is that commit
   order, never an order reconstructed from timestamp seconds or callbacks. Read children
   without receipts only for live-state refresh. A terminal child missing its receipt is
   an actionable persistence/wiring error and blocks decision, not permission to allocate
   a callback-time sequence. `RecordTaskTerminal` replays the atomic receipt. Its direct
   allocation form exists for childless adapter fixtures only. Receipt result/failure data
   is captured from persisted variables/latest failure trace at the terminal transition.
   Intake runs in every state; later terminals change live inspection only.

2. **Dead parent** (`failed` or `cancelled`, including crash-recovered): undecided ⇒ `Decide(DecideLaunchFailure(run, "parent run <state> before the group decided"))`; cancel EVERY non-terminal task (blocking or independent; `MarkTaskCancelRequested` for launched ones; `RunCanceller.Cancel` under the owner identity, `ErrCannotCancel`/`ErrNotFound` tolerated); `MarkActionsApplied`; finalize from `decided`; `MarkContinuationDelivered`. Never woken.
3. **Launch barrier.** `launching` with every task launched ⇒ `TransitionState(launching→running)` and continue. `launching` with a pending/failed task while the parent is `running`/`paused` AT this node ⇒ the launcher is still working: do nothing. Otherwise (the launch was interrupted) ⇒ `Decide(DecideLaunchFailure(run, "launch failed: the launch was interrupted before every task started"))`.
4. **Decide.** `running` and undecided ⇒ `DecideGroup`; when decided, `Decide` CAS; `group.decided` only for the winner. The run is re-read either way.
5. **Unfinished actions** (decided, `!ActionsApplied`): a launch-failure decision (any task not launched) or `cancel_unfinished` ⇒ cancel every non-terminal task; `finish_independently` ⇒ `MarkTaskContinuingIndependently` for every launched non-terminal task; `wait_for_remaining` never decides early so has nothing to do. Then `MarkActionsApplied`. Any error is RETURNED (the run stays unsettled; `Reconcile` finishes it).
6. **Finalize** (results mode, state `decided`): `SetFinalOutput(run, <outcome port>, BuildGroupOutput(run), GroupRunDecided)` CAS; `group.completed` for the winner. Files mode (`results_and_files`) does NOT finalize; the parent is woken at `decided` so the executor runs the gatherer.
7. **Delivery bookkeeping.** `done`, undelivered, and the parent has moved past this node (terminal, or at another node) ⇒ `MarkContinuationDelivered` (so the run leaves `ListUnsettled`).
8. **Wake.** `WakeParent`: only when (`done` ∧ `ContinuationReady` ∧ `!ContinuationDelivered`) or (files mode ∧ `decided` ∧ `ActionsApplied`), and the parent is `waiting` with `CurrentNodeID == run.NodeID`; then `NodeAddressedWaker.SetQueuedIfWaitingAtNode` (the CAS that loses benignly to the executor's in-process claim) and `QueueManager.NotifyCapacityFreed`. A wake error is logged and the sweep retries (the run is still unsettled).

## Wiring & data provenance

- **Composition root:** **P2-T14** (`cmd/arbitron/main.go:initDomain`) constructs `engine.NewGroupCoordinator(groupRunRepo, instanceRepo, queueRepo, eng, identityResolver, bus, queueMgr)`, passes it to `engine.NewParallelGroupExecutor` (P2-T11), registers it as the engine's terminal handler via `eng.SetGroupTerminalHandler(groupCoord)` (P2-T13), and runs `groupCoord.Reconcile` at startup after `eng.RecoverCrashedInstances()` and on its own ticker goroutine `go sweepGroupReconciler(groupSweepCtx, groupCoord, groupSweepInterval, logger)` (P2-T14 adds `sweepGroupReconciler` next to `sweepSpawnReconcilers`, main.go:77-102). In main.go the coordinator variable is `groupCoord` and the repository `groupRunRepo`. Exact producers:
  - `groups` ← `adapter/sqlite.NewGroupRunRepo(db)` (P2-T3).
  - `instances` ← `instanceRepo := adaptsqlite.NewInstanceRepo(db)` (main.go:373).
  - `waker` ← `queueRepo := adaptsqlite.NewQueueRepo(db)` (main.go:797), whose `SetQueuedIfWaitingAtNode` is P2-T4.
  - `canceller` ← `eng` (`engine.go:1530 Engine.Cancel`, authorized, cascading).
  - `identities` ← `identityResolver` = `userIdentityResolver` (main.go:926, `IdentityForUser` main.go:1455) — the same value the spawn executor and orphan sweep use.
  - `bus` ← `eventbus.New()` (main.go:618); `queueMgr` ← the `queue.Manager` the engine uses (`SetQueueManager`).
- **Event producers:** `childID` for `OnTaskTerminal` ← `domain/engine/spawn_wake.go:Engine.OnChildTerminal` dispatch for `SpawnModeGroupTask` children (P2-T13), itself called from `engine.go:976` (completion), `:1000` (`failInstance`), `:1595` (`cancelAuthorized`), `:1457` (webhook timeout).
- **Data producers:** child final variables ← `Engine.run` `UpdateVariables` (engine.go:1115); failure details ← `Engine.executeWithRetry` `nodeExecutions.Create` with `Error{"error": err.Error()}` and per-attempt `Attempt` (engine.go:1160-1191); parent `State`/`CurrentNodeID` ← `Engine.run` `UpdateCurrentNode` (engine.go:923) and the executor's `UpdateState(waiting)` (P2-T11); roster ← `GroupLauncher.Launch` (P2-T8).
- **Provenance probes:** `TestGroupCoordinator_AllSucceed_WaitForRemaining_FailureKeepsResultsAndDetails` fails if the result is not read from the child's persisted `Variables` (minus `workspace`) or the failure details not from its `NodeExecution` rows; `TestGroupCoordinator_OwnerIdentityFailureLeavesActionsForTheSweep` fails if cancellation runs without the reconstructed owner identity; `TestNewGroupCoordinator_PanicsOnAMissingCollaborator` fails if a collaborator can be wired as nil.

**Deviations (backward-compatible):** the `EventTypeGroup*` constants are defined by P2-T8 (their first publisher) instead of here; this task consumes them. `NewGroupCoordinator` panics on any nil collaborator.

---

### Task 10: GroupCoordinator

**Files:**
- Create: `domain/engine/group_coordinator.go`
- Test: `domain/engine/group_coordinator_test.go` (package `engine_test`; reuses `memGroupRuns`, `recordingCanceller` from `group_fakes_test.go` (P2-T8), `newMockInstanceRepo`, `newMockNodeExecutionRepo`, `newMockEventBus`, `newMockQueueManager` from `engine_test.go:95-725`, `fakeIdentityResolver` from `executor_spawn_test.go:25-43`)

- [x] Step 1: Write the failing tests. Create `domain/engine/group_coordinator_test.go`:

```go
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
	if state.IsTerminal() { f.captureReceipt(t, id) }
}

// captureReceipt models the database terminal trigger, BEFORE any callback.
// The real SQLite integration test proves the two writes share a transaction.
func (f *coordFixture) captureReceipt(t *testing.T, id uuid.UUID) {
	t.Helper()
	child, err := f.instances.Get(context.Background(), id)
	require.NoError(t, err)
	if !child.State.IsTerminal() || child.GroupRunID == nil || child.GroupTaskKey == nil { return }
	result := map[string]any{}
	for key, value := range child.Variables {
		if key != "workspace" { result[key] = value }
	}
	var failure map[string]any
	if child.State != instance.StateCompleted {
		node, message, attempt := "", "", 0
		if child.FailedNodeID != nil { node = *child.FailedNodeID }
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
	f.finish(t, run, "a", instance.StateCompleted, map[string]any{"v": "a"}, gcT0.Add(1*time.Second))
	f.finish(t, run, "b", instance.StateCompleted, map[string]any{"v": "b"}, gcT0.Add(2*time.Second))

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

func TestNewGroupCoordinator_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newCoordFixture(t)
	build := []func(){
		func() { engine.NewGroupCoordinator(nil, f.instances, f.waker, f.canceller, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, nil, f.waker, f.canceller, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, nil, f.canceller, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, f.waker, nil, f.identities, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, nil, f.bus, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, f.identities, nil, f.qm) },
		func() { engine.NewGroupCoordinator(f.groups, f.instances, f.waker, f.canceller, f.identities, f.bus, nil) },
	}
	for i, b := range build {
		assert.Panics(t, b, "collaborator %d", i)
	}
}
```

- [x] Step 2: Run it, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestGroupCoordinator|TestNewGroupCoordinator' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_coordinator_test.go:81:22: undefined: engine.GroupCoordinator
domain/engine/group_coordinator_test.go:106:17: undefined: engine.NewGroupCoordinator
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement. Create `domain/engine/group_coordinator.go`:

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupCoordinator owns every parallel-group decision.
//
// THE PERSISTED STATE IS THE AUTHORITY. Evaluate reads the run, the parent and
// every task child from storage on every pass and moves the run forward with
// compare-and-set writes, re-reading after each one. In-process terminal
// callbacks (OnTaskTerminal) only make the common case immediate; Reconcile
// over ListUnsettled is what guarantees that a crash between any two steps —
// decision, cancellation, finalization, wake — is finished later. Losing a CAS
// is never permission to skip the work that follows it.
type GroupCoordinator struct {
	groups     instance.GroupRunRepository
	instances  instance.InstanceRepository
	waker      instance.NodeAddressedWaker
	canceller  RunCanceller
	identities IdentityResolver
	bus        EventBus
	queueMgr   QueueManager
	now        func() time.Time

	mu    sync.Mutex
	gates map[uuid.UUID]*evalGate
}

// evalGate coalesces in-process re-entry for one run: a call that arrives
// while a pass for the same run is running marks it dirty, and the running
// call makes one more pass instead of recursing.
type evalGate struct {
	dirty bool
}

// NewGroupCoordinator constructs the coordinator. Every collaborator is
// required; a nil one panics, because each silently disables a guarantee
// (decisions, results, failure details, wakes, cancellation, authorization,
// visibility, or queue capacity).
func NewGroupCoordinator(
	groups instance.GroupRunRepository,
	instances instance.InstanceRepository,
	waker instance.NodeAddressedWaker,
	canceller RunCanceller,
	identities IdentityResolver,
	bus EventBus,
	queueMgr QueueManager,
) *GroupCoordinator {
	switch {
	case groups == nil:
		panic("engine: NewGroupCoordinator requires a non-nil GroupRunRepository")
	case instances == nil:
		panic("engine: NewGroupCoordinator requires a non-nil InstanceRepository")
	case waker == nil:
		panic("engine: NewGroupCoordinator requires a non-nil NodeAddressedWaker")
	case canceller == nil:
		panic("engine: NewGroupCoordinator requires a non-nil RunCanceller")
	case identities == nil:
		panic("engine: NewGroupCoordinator requires a non-nil IdentityResolver")
	case bus == nil:
		panic("engine: NewGroupCoordinator requires a non-nil EventBus")
	case queueMgr == nil:
		panic("engine: NewGroupCoordinator requires a non-nil QueueManager")
	}
	return &GroupCoordinator{
		groups:     groups,
		instances:  instances,
		waker:      waker,
		canceller:  canceller,
		identities: identities,
		bus:        bus,
		queueMgr:   queueMgr,
		now:        time.Now,
		gates:      map[uuid.UUID]*evalGate{},
	}
}

// OnTaskTerminal is the in-process intake of one task child's terminal
// transition. It is VOID and best-effort, like Engine.OnChildTerminal: a lost
// or failed call is recovered by Reconcile.
func (c *GroupCoordinator) OnTaskTerminal(ctx context.Context, childID uuid.UUID) {
	if ctx == nil {
		ctx = context.Background()
	}
	run, err := c.groups.GetByChild(ctx, childID)
	if err != nil {
		slog.Error("group coordinator: resolving a task child's group run failed; the group sweep retries",
			"childID", childID, "error", err)
		return
	}
	if run == nil {
		return
	}
	if err := c.Evaluate(ctx, run.ID); err != nil {
		slog.Warn("group coordinator: evaluation incomplete; the group sweep retries",
			"groupRunID", run.ID, "childID", childID, "error", err)
	}
}

// Reconcile evaluates every unsettled run. The listing error is returned; a
// per-run error is logged and the sweep continues.
func (c *GroupCoordinator) Reconcile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ids, err := c.groups.ListUnsettled(ctx)
	if err != nil {
		return fmt.Errorf("group reconciler: listing unsettled group runs: %w", err)
	}
	for _, id := range ids {
		if err := c.Evaluate(ctx, id); err != nil {
			slog.Warn("group reconciler: group run not settled this pass", "groupRunID", id, "error", err)
		}
	}
	return nil
}

// Evaluate moves one run as far forward as its persisted state allows:
// intake terminals -> decide (CAS) -> apply unfinished actions -> finalize the
// results-mode output -> wake the parent. See the task contract for each step.
func (c *GroupCoordinator) Evaluate(ctx context.Context, runID uuid.UUID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !c.enter(runID) {
		return nil
	}
	for {
		err := c.evaluateOnce(ctx, runID)
		if !c.again(runID, err) {
			return err
		}
	}
}

func (c *GroupCoordinator) enter(runID uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if g, busy := c.gates[runID]; busy {
		g.dirty = true
		return false
	}
	c.gates[runID] = &evalGate{}
	return true
}

func (c *GroupCoordinator) again(runID uuid.UUID, err error) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.gates[runID]
	if err == nil && g != nil && g.dirty {
		g.dirty = false
		return true
	}
	delete(c.gates, runID)
	return false
}

func (c *GroupCoordinator) evaluateOnce(ctx context.Context, runID uuid.UUID) error {
	run, err := c.reload(ctx, runID)
	if err != nil {
		return err
	}
	parent, err := c.instances.Get(ctx, run.ParentInstanceID)
	if err != nil {
		return fmt.Errorf("group coordinator: run %s: load parent %s: %w", runID, run.ParentInstanceID, err)
	}

	intakeErr := c.intake(ctx, run)
	if intakeErr != nil {
		return intakeErr
	}
	if run, err = c.reload(ctx, runID); err != nil {
		return errors.Join(intakeErr, err)
	}

	if parent.State == instance.StateFailed || parent.State == instance.StateCancelled {
		return errors.Join(intakeErr, c.settleUnderDeadParent(ctx, run, parent))
	}

	if run.State == instance.GroupRunLaunching {
		advanced, err := c.completeLaunch(ctx, run, parent)
		if err != nil {
			return errors.Join(intakeErr, err)
		}
		if !advanced {
			return intakeErr
		}
		if run, err = c.reload(ctx, runID); err != nil {
			return errors.Join(intakeErr, err)
		}
	}

	if run.Decision == nil && run.State == instance.GroupRunRunning {
		if d, ok := DecideGroup(run, c.now()); ok {
			won, err := c.groups.Decide(ctx, run.ID, *d)
			if err != nil {
				return errors.Join(intakeErr, fmt.Errorf("group coordinator: record the decision of run %s: %w", run.ID, err))
			}
			if won {
				publishGroupEvent(c.bus, run, EventTypeGroupDecided, decisionEventData(d), c.now())
			}
			if run, err = c.reload(ctx, runID); err != nil {
				return errors.Join(intakeErr, err)
			}
		}
	}
	if run.Decision == nil {
		return intakeErr
	}

	if !run.ActionsApplied {
		if err := c.applyActions(ctx, run, parent); err != nil {
			return errors.Join(intakeErr, err)
		}
	}
	if run.State == instance.GroupRunDecided && run.GatherMode != workflow.GroupGatherResultsAndFiles {
		if err := c.finalize(ctx, run); err != nil {
			return errors.Join(intakeErr, err)
		}
	}
	if run, err = c.reload(ctx, runID); err != nil {
		return errors.Join(intakeErr, err)
	}

	if run.State == instance.GroupRunDone && !run.ContinuationDelivered && parentMovedOn(parent, run) {
		if err := c.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
			return errors.Join(intakeErr, fmt.Errorf("group coordinator: settle the delivered continuation of run %s: %w", run.ID, err))
		}
		return intakeErr
	}
	c.WakeParent(ctx, run)
	return intakeErr
}

func (c *GroupCoordinator) reload(ctx context.Context, runID uuid.UUID) (*instance.GroupRun, error) {
	run, err := c.groups.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("group coordinator: load run %s: %w", runID, err)
	}
	return run, nil
}

// intake refreshes nonterminal live state. Production terminal receipts are
// already in the roster, atomically committed by the terminal writer (P2-T16).
func (c *GroupCoordinator) intake(ctx context.Context, run *instance.GroupRun) error {
	for _, task := range run.Tasks {
		if task.LaunchState != instance.GroupTaskLaunched || task.TerminalSeq != nil { continue }
		child, err := c.instances.Get(ctx, task.ChildInstanceID)
		if err != nil { return fmt.Errorf("group intake child %s: %w", task.ChildInstanceID, err) }
		if child.State.IsTerminal() {
			// The child may have committed after our first roster read.
			fresh, err := c.groups.Get(ctx, run.ID)
			if err != nil { return err }
			receipt, ok := fresh.TaskByKey(task.Key)
			if !ok || receipt.TerminalSeq == nil {
				return fmt.Errorf("terminal group task %q has no atomic receipt; verify terminal persistence wiring", task.Key)
			}
			continue
		}
		status := instance.GroupTaskStatusFromState(child.State)
		if status != task.LiveStatus {
			if err := c.groups.UpdateTaskLiveStatus(ctx, run.ID, task.Key, status); err != nil { return err }
			publishGroupEvent(c.bus, run, EventTypeGroupTaskUpdated, map[string]any{"taskKey": task.Key, "status": string(status)}, c.now())
		}
	}
	return nil
}

// settleUnderDeadParent stops all of a failed or cancelled parent's group work
// and settles the run without any continuation.
func (c *GroupCoordinator) settleUnderDeadParent(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) error {
	if run.Decision == nil && (run.State == instance.GroupRunLaunching || run.State == instance.GroupRunRunning) {
		d := DecideLaunchFailure(run, fmt.Sprintf("parent run %s before the group decided", parent.State), c.now())
		won, err := c.groups.Decide(ctx, run.ID, *d)
		if err != nil {
			return fmt.Errorf("group coordinator: record the dead-parent decision of run %s: %w", run.ID, err)
		}
		if won {
			publishGroupEvent(c.bus, run, EventTypeGroupDecided, decisionEventData(d), c.now())
		}
		if run, err = c.reload(ctx, run.ID); err != nil {
			return err
		}
	}
	if err := c.cancelOpenTasks(ctx, run, parent); err != nil {
		return err
	}
	if !run.ActionsApplied {
		if err := c.groups.MarkActionsApplied(ctx, run.ID); err != nil {
			return fmt.Errorf("group coordinator: mark actions applied on run %s: %w", run.ID, err)
		}
	}
	if run.State == instance.GroupRunDecided && run.Decision != nil {
		if err := c.finalize(ctx, run); err != nil {
			return err
		}
	}
	if !run.ContinuationDelivered {
		if err := c.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
			return fmt.Errorf("group coordinator: settle run %s under a %s parent: %w", run.ID, parent.State, err)
		}
	}
	return nil
}

// completeLaunch handles a run still in launching. It reports whether the run
// advanced (barrier opened, or an interrupted launch was decided).
func (c *GroupCoordinator) completeLaunch(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (bool, error) {
	allLaunched := true
	for _, t := range run.Tasks {
		if t.LaunchState != instance.GroupTaskLaunched {
			allLaunched = false
			break
		}
	}
	if allLaunched {
		if _, err := c.groups.TransitionState(ctx, run.ID, instance.GroupRunLaunching, instance.GroupRunRunning); err != nil {
			return false, fmt.Errorf("group coordinator: open the launch barrier of run %s: %w", run.ID, err)
		}
		return true, nil
	}
	atNode := parent.CurrentNodeID != nil && *parent.CurrentNodeID == run.NodeID
	if atNode && (parent.State == instance.StateRunning || parent.State == instance.StatePaused) {
		return false, nil // the launcher is still starting tasks in that goroutine
	}
	d := DecideLaunchFailure(run, "launch failed: the launch was interrupted before every task started", c.now())
	won, err := c.groups.Decide(ctx, run.ID, *d)
	if err != nil {
		return false, fmt.Errorf("group coordinator: record the interrupted-launch decision of run %s: %w", run.ID, err)
	}
	if won {
		publishGroupEvent(c.bus, run, EventTypeGroupDecided, decisionEventData(d), c.now())
	}
	return true, nil
}

// applyActions performs the decision's unfinished-task actions, then records
// that they are complete. Any error is returned so the sweep finishes them.
func (c *GroupCoordinator) applyActions(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) error {
	launchFailure := false
	for _, t := range run.Tasks {
		if t.LaunchState != instance.GroupTaskLaunched {
			launchFailure = true
			break
		}
	}
	switch {
	case launchFailure || run.Decision.Behavior == workflow.GroupUnfinishedCancel:
		if err := c.cancelOpenTasks(ctx, run, parent); err != nil {
			return err
		}
	case run.Decision.Behavior == workflow.GroupUnfinishedFinishIndependently:
		for _, t := range run.Tasks {
			if isTerminalGroupStatus(t.LiveStatus) || t.LaunchState != instance.GroupTaskLaunched || t.ContinuingIndependently {
				continue
			}
			if err := c.groups.MarkTaskContinuingIndependently(ctx, run.ID, t.Key); err != nil {
				return fmt.Errorf("group coordinator: run %s task %q: mark continuing independently: %w", run.ID, t.Key, err)
			}
		}
	}
	if err := c.groups.MarkActionsApplied(ctx, run.ID); err != nil {
		return fmt.Errorf("group coordinator: mark actions applied on run %s: %w", run.ID, err)
	}
	return nil
}

// cancelOpenTasks requests and performs cancellation of every non-terminal
// task under the parent OWNER's reconstructed identity. A never-launched task's
// reserved id is cancelled too: a crash between StartGroupTask and
// MarkTaskLaunched can leave a real child behind it. Already-terminal and
// absent children are the normal benign races.
func (c *GroupCoordinator) cancelOpenTasks(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) error {
	var open []instance.GroupTask
	for _, t := range run.Tasks {
		if !isTerminalGroupStatus(t.LiveStatus) {
			open = append(open, t)
		}
	}
	if len(open) == 0 {
		return nil
	}
	ownerCtx, err := c.ownerContext(ctx, parent.OwnerID)
	if err != nil {
		return err
	}
	for _, t := range open {
		if t.LaunchState == instance.GroupTaskLaunched && !t.CancelRequested {
			if err := c.groups.MarkTaskCancelRequested(ctx, run.ID, t.Key); err != nil {
				return fmt.Errorf("group coordinator: run %s task %q: record the cancellation request: %w", run.ID, t.Key, err)
			}
		}
		if err := c.canceller.Cancel(ownerCtx, t.ChildInstanceID); err != nil &&
			!errors.Is(err, instance.ErrCannotCancel) && !errors.Is(err, instance.ErrNotFound) {
			return fmt.Errorf("group coordinator: run %s task %q: cancel %s: %w", run.ID, t.Key, t.ChildInstanceID, err)
		}
	}
	return nil
}

func (c *GroupCoordinator) ownerContext(ctx context.Context, owner uuid.UUID) (context.Context, error) {
	id, err := c.identities.IdentityForUser(ctx, owner)
	if err != nil {
		// Never downgraded to a system identity: that would widen what the
		// cancellation may reach.
		return nil, fmt.Errorf("group coordinator: resolve owner identity %s: %w", owner, err)
	}
	return identity.WithIdentity(ctx, id), nil
}

// finalize freezes the results-mode output and makes the continuation ready.
func (c *GroupCoordinator) finalize(ctx context.Context, run *instance.GroupRun) error {
	port := workflow.GroupPortFailure
	if run.Decision.Outcome == groupOutcomeSuccess {
		port = workflow.GroupPortSuccess
	}
	won, err := c.groups.SetFinalOutput(ctx, run.ID, port, BuildGroupOutput(run), instance.GroupRunDecided)
	if err != nil {
		return fmt.Errorf("group coordinator: finalize run %s: %w", run.ID, err)
	}
	if won {
		publishGroupEvent(c.bus, run, EventTypeGroupCompleted, map[string]any{"outcome": run.Decision.Outcome, "port": port}, c.now())
	}
	return nil
}

// parentMovedOn reports whether the parent can no longer be continued by this
// run: it is terminal, or its cursor has left the group node.
func parentMovedOn(parent *instance.Instance, run *instance.GroupRun) bool {
	if parent.State.IsTerminal() {
		return true
	}
	return parent.CurrentNodeID == nil || *parent.CurrentNodeID != run.NodeID
}

// WakeParent re-queues the parent iff it is waiting at THIS group node and the
// run is ready to continue it (done with a ready, undelivered continuation; or,
// for file gathering, decided with its actions applied). It never wakes a
// cancelled, failed, paused or elsewhere-waiting parent: the node-addressed
// conditional update matches only state = waiting at run.NodeID, which is also
// what makes it lose benignly to the executor's in-process claim.
func (c *GroupCoordinator) WakeParent(ctx context.Context, run *instance.GroupRun) bool {
	if run == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ready := run.State == instance.GroupRunDone && run.ContinuationReady && !run.ContinuationDelivered
	gatherReady := run.GatherMode == workflow.GroupGatherResultsAndFiles && run.State == instance.GroupRunDecided && run.ActionsApplied
	if !ready && !gatherReady {
		return false
	}
	parent, err := c.instances.Get(ctx, run.ParentInstanceID)
	if err != nil {
		slog.Warn("group coordinator: reading the parent to wake failed; the group sweep retries",
			"groupRunID", run.ID, "parentID", run.ParentInstanceID, "error", err)
		return false
	}
	if parent.State != instance.StateWaiting || parent.CurrentNodeID == nil || *parent.CurrentNodeID != run.NodeID {
		return false
	}
	woke, err := c.waker.SetQueuedIfWaitingAtNode(ctx, parent.ID, run.NodeID, c.now())
	if err != nil {
		slog.Error("group coordinator: re-queueing the waiting parent failed; the group sweep retries",
			"groupRunID", run.ID, "parentID", parent.ID, "error", err)
		return false
	}
	if !woke {
		return false // the executor claimed it back in-process, or it moved
	}
	c.queueMgr.NotifyCapacityFreed(ctx)
	return true
}
```

- [x] Step 4: Run it, confirm it passes (with the race detector: the coalescing gate is concurrent code).

```bash
go test ./domain/engine/ -run 'TestGroupCoordinator|TestNewGroupCoordinator' -race -count=1 -v
```
Expected:
```
--- PASS: TestGroupCoordinator_AllSucceed_DecidesFinalizesAndWakesOnce (0.00s)
--- PASS: TestGroupCoordinator_FirstSuccess_EarliestTerminalWinsRegardlessOfCallbackOrder (0.00s)
...
--- PASS: TestNewGroupCoordinator_PanicsOnAMissingCollaborator (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 5: Run the whole package, gofmt and vet.

Verification: the focused coordinator suite passes with `-race`; `gofmt -l`,
`go vet ./domain/engine/`, and `git diff --check` are clean. The whole-package
race run reports an existing race in `TestTrigger_SpawnLinkAndOriginCoexist`
(`engine_spawn_stamp_test.go` reads instances while `mockQueueManager.Enqueue`
updates them). Reproduced the same race using an unchanged archive of base
commit `ead5f9a`; it is outside this task's slice.

Self-review, in order: (1) checked the contract and added post-write reloads
with red-to-green recovery tests; (2) added missing/concurrently committed
receipt, reversed-clock terminal order, CAS-loser, and concurrent coalescing
coverage asserting frozen output, child state, and parent queue state;
(3) removed obsolete nil-error joining and formatted/vetted the code.
Production composition and terminal dispatch are explicitly owned by P2-T14
and P2-T13 respectively; the persisted receipt producer is already P2-T16.

```bash
go test ./domain/engine/ -race -count=1
gofmt -l domain/engine/group_coordinator.go domain/engine/group_coordinator_test.go
go vet ./domain/engine/
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 6: Commit.

```bash
git add domain/engine/group_coordinator.go domain/engine/group_coordinator_test.go
git commit -m "feat(engine): GroupCoordinator decides once, applies unfinished actions, finalizes and wakes by node" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
