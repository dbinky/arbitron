# Parallel Workflow Gathering — Phase 2 Task 13: Group-task terminal dispatch and crash-recovery contract

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Route every terminal transition of a parallel-group task child — completion, failure, cancellation, webhook timeout (live or at startup) — from `Engine.OnChildTerminal` to the group coordinator, BEFORE (and instead of) the legacy wait-for-child wake, and pin down how crash-recovered group tasks and parents are settled.
**Architecture:** One new file `domain/engine/group_dispatch.go` declares the `GroupTerminalHandler` port, `Engine.SetGroupTerminalHandler`, and an unexported `dispatchGroupTerminal`. `OnChildTerminal` (`spawn_wake.go`) gains one early branch for `SpawnModeGroupTask` children; the legacy three-part match below it is untouched. All terminal writers already funnel through `OnChildTerminal`, so no call site changes. `RecoverCrashedInstances` is deliberately NOT changed to dispatch: the coordinator's `Reconcile` (run by `main.go` right after it, P2-T14) reads persisted child state and is the authority, exactly as `SpawnWaitReconciler` is for legacy waits.
**Tech Stack:** Go 1.25.5, testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Each group continues its parent once its completion rule ... is satisfied"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §3 "Feed terminals from `Engine.OnChildTerminal` (explicit new group-task dispatch) and a new group sweep", Data Provenance row "Terminal notifications and recovery sweep" (P2-A dispatch half), §Error handling "Crash recovery can mark running children/parents failed ... Recovery must also clean up partially launched/decided groups under failed parents".

## Atomic-terminal dependency

P2-T16 is the owner of production terminal capture: it extends migration 019 with a
single durable clock and terminal-state triggers and adapts `RecordTaskTerminal` to the
same allocator. Finish it before P2-T10/T13/T14. The callback is a wake accelerator;
wall-clock timestamp sorting and callback receipt order cannot determine first success.
No extra public field or port is introduced. P3-T5 still finalizes file refs BEFORE the
successful terminal write, so the atomic receipt cannot advertise success without files.


**Interfaces**
- Consumes (existing code):
  - `domain/engine/spawn_wake.go:35-85` `func (e *Engine) OnChildTerminal(ctx context.Context, childID uuid.UUID)` — void, best-effort.
  - Every terminal writer that already calls it: `domain/engine/engine.go:976` (run completion after `SetCompleted`), `:1000` (`failInstance`, used by halt/route/panic/persistence failures), `:1457` (`resumeFromWebhookWait` timeout-while-paused), `:1595` (`cancelAuthorized`, so `Cancel` and the cancel cascade); `cmd/arbitron/main.go:859` `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)`, invoked by `domain/queue/manager.go:222` (live webhook timeout) and `domain/queue/recovery.go:82` (webhook expired during downtime).
  - `domain/engine/engine.go:1765-1787` `func (e *Engine) RecoverCrashedInstances() error` — marks `ListRunning` instances failed and does NOT call `OnChildTerminal`.
  - `domain/engine/engine.go:23-64` `Engine` struct (`mu sync.Mutex` guards executor map and run contexts; it is never held at any `OnChildTerminal` call site).
  - P2-T1 (contract): `instance.SpawnModeGroupTask`, `Instance.GroupRunID *uuid.UUID`, `Instance.GroupTaskKey *string`, `Instance.GroupTaskKind`, `instance.GroupTaskKindBranch`.
- Produces (exact signatures):
  - `type GroupTerminalHandler interface { OnTaskTerminal(ctx context.Context, childID uuid.UUID) }` — `*GroupCoordinator` (P2-T10) satisfies it.
  - `func (e *Engine) SetGroupTerminalHandler(h GroupTerminalHandler)` — panics on nil (same posture as `SetSpawnLinks`).
  - `OnChildTerminal` contract: `child.SpawnMode == instance.SpawnModeGroupTask` ⇒ if the child is terminal, `handler.OnTaskTerminal(ctx, childID)` exactly once per call, synchronously; then return. Never `SetQueuedIfWaiting`, never `NotifyCapacityFreed` for a group task. No handler wired ⇒ `slog.Warn` and return.
  - Re-entrancy guarantee (load-bearing for P2-T10): the engine holds no lock while calling the handler, so a handler that calls `RunCanceller.Cancel` on a sibling task (whose `cancelAuthorized` calls `OnChildTerminal` again) is re-entered on the same goroutine and must not deadlock on its own in-process locks. Pinned by `TestGroupDispatch_HandlerMayBeReenteredThroughCancel`.
  - Events: this task publishes none. Every `group.*` event (`EventTypeGroupTaskUpdated`, `EventTypeGroupDecided`, … defined by P2-T8 in `domain/engine/group_events.go`) is published by the coordinator, the single writer of group-run state, so a task terminal is announced once, with the persisted live status, rather than once here and again there.

**Wiring & data provenance**
- Composition root: P2-T14 adds `eng.SetGroupTerminalHandler(groupCoord)` in `cmd/arbitron/main.go` (before `eng.FreezeExecutors()`), with `groupCoord := engine.NewGroupCoordinator(...)`. Its e2e `TestGroupE2E_BranchesAllSucceedGatherResultsInAuthorOrder` fails if that line is deleted (tasks finish, nothing decides in-process, the parent stays `waiting` past the 40s budget, which is shorter than the 60s group sweep).
- Producers:
  - `childID`: the instance that just reached a terminal state, passed by every call site listed above.
  - `child.SpawnMode == SpawnModeGroupTask`: read side of P2-T2 (`parent_instance_id` set ∧ `group_run_id` set ⇒ `SpawnModeGroupTask`), written when `Engine.StartGroupTask` (P2-T7) creates the task row.
  - `child.State`: persisted by the terminal write that precedes each call (`SetCompleted`, `SetFailed`, `UpdateState(cancelled)`).
  - handler: `*GroupCoordinator` built in `cmd/arbitron/main.go` (P2-T14).
- Crash recovery (the design's "terminals missed during crash recovery" and "groups under failed parents"):
  - A task child left `running` by a crash is marked `failed` by `RecoverCrashedInstances` WITHOUT a dispatch; a parent left `running` (e.g. mid-launch) is marked `failed` the same way.
  - `main.go` then runs `groupCoord.Reconcile` (P2-T14, after `RecoverCrashedInstances` and the spawn reconciler): `ListUnsettled` → `Evaluate` reads every task child's persisted state, so the crashed task is taken in as `failed`, and a failed parent makes `Evaluate` cancel every still-active task and settle the run (P2-T10 contract). The periodic group sweep repeats this.
  - Dispatching from `RecoverCrashedInstances` would add nothing (Reconcile reads the same rows moments later) and would run coordinator side effects (cancels, wakes) before the queue's own recovery has re-registered timers and webhooks. `TestRecoverCrashedInstances_DoesNotDispatchGroupTasks` pins this decision; the startup e2e `TestGroupE2E_StartupReconcileSettlesGroupAfterCrash` (P2-T14) proves the authority actually settles the group.
- Provenance probe: `TestGroupDispatch_UnsetHandlerIsALoggedNoOp` — with the handler constructed but not wired, a terminal group task reaches nobody and the parent is not woken by the engine.

### Task 13: Group-task terminal dispatch

**Files:**
- Create: `domain/engine/group_dispatch.go`
- Modify: `domain/engine/engine.go` (add field `groupHandler` to `Engine`, after the `spawnLinks` field, lines 43-49; replace the `RecoverCrashedInstances` doc comment, lines 1763-1764)
- Modify: `domain/engine/spawn_wake.go` (doc comment lines 12-34; body lines 43-48)
- Test: `domain/engine/group_dispatch_test.go`

- [x] Step 1: Write the failing tests. Create `domain/engine/group_dispatch_test.go` (package `engine_test`, reusing `newTestFixtures`, `waitForInstance`, `singleNodeWorkflow`, `bgAdmin` from the existing test files):

```go
package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingGroupHandler records every OnTaskTerminal call by child id. onCall,
// when set, runs AFTER the call is recorded and with no lock held, so a test
// can re-enter the engine from inside the handler.
type recordingGroupHandler struct {
	mu     sync.Mutex
	calls  []uuid.UUID
	onCall func(ctx context.Context, childID uuid.UUID)
}

func (h *recordingGroupHandler) OnTaskTerminal(ctx context.Context, childID uuid.UUID) {
	h.mu.Lock()
	h.calls = append(h.calls, childID)
	hook := h.onCall
	h.mu.Unlock()
	if hook != nil {
		hook(ctx, childID)
	}
}

func (h *recordingGroupHandler) called() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uuid.UUID(nil), h.calls...)
}

var _ engine.GroupTerminalHandler = (*recordingGroupHandler)(nil)

// groupDispatchFixture is a parent WAITING at group node "group-1" (the exact
// shape the legacy wake would match on node id) plus a recording handler.
type groupDispatchFixture struct {
	f        *testFixtures
	handler  *recordingGroupHandler
	owner    uuid.UUID
	parentID uuid.UUID
	runID    uuid.UUID
}

func newGroupDispatchFixture(t *testing.T, wireHandler bool) *groupDispatchFixture {
	t.Helper()
	f := newTestFixtures()
	owner := uuid.New()
	node := "group-1"
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "parent",
		State: instance.StateWaiting, Priority: instance.PriorityNormal, CurrentNodeID: &node,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), parent))
	h := &recordingGroupHandler{}
	if wireHandler {
		f.engine.SetGroupTerminalHandler(h)
	}
	return &groupDispatchFixture{f: f, handler: h, owner: owner, parentID: parent.ID, runID: uuid.New()}
}

// task creates a group-task child of the waiting parent, linked to "group-1".
func (g *groupDispatchFixture) task(t *testing.T, key string, state instance.State, snapshot map[string]any) uuid.UUID {
	t.Helper()
	pid := g.parentID
	node := "group-1"
	rid := g.runID
	k := key
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: g.owner, WorkflowName: "parent", WorkflowSnapshot: snapshot,
		State: state, Priority: instance.PriorityNormal, Variables: map[string]any{},
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask, SpawnDepth: 1,
		GroupRunID: &rid, GroupTaskKey: &k, GroupTaskKind: instance.GroupTaskKindBranch,
	}
	require.NoError(t, g.f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

// legacyChild creates a legacy spawn child of the waiting parent at "group-1".
func (g *groupDispatchFixture) legacyChild(t *testing.T, mode instance.SpawnMode, state instance.State) uuid.UUID {
	t.Helper()
	pid := g.parentID
	node := "group-1"
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: g.owner, WorkflowName: "child",
		State: state, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: mode, SpawnDepth: 1,
	}
	require.NoError(t, g.f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

func (g *groupDispatchFixture) stateOf(t *testing.T, id uuid.UUID) instance.State {
	t.Helper()
	got, err := g.f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return got.State
}

// snapshotOf renders a workflow the way TriggerWithDepartment stores it.
func snapshotOf(t *testing.T, wf *workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// ---------- happy path ----------

// A terminal group task goes to the handler — once, with its own id — and the
// engine NEVER wakes the parent itself, even though the parent is waiting at
// exactly the node the task is linked to (the legacy match's node conjunct).
// Only the coordinator may decide and wake.
func TestGroupDispatch_TerminalGroupTaskRoutesToHandlerNotLegacyWake(t *testing.T) {
	for _, st := range []instance.State{instance.StateCompleted, instance.StateFailed, instance.StateCancelled} {
		g := newGroupDispatchFixture(t, true)
		child := g.task(t, "a", st, nil)

		g.f.engine.OnChildTerminal(context.Background(), child)

		assert.Equal(t, []uuid.UUID{child}, g.handler.called(), "state %s", st)
		assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID),
			"state %s: the engine must not re-queue a group parent; only the coordinator wakes it", st)
		assert.Equal(t, 0, g.f.queueManager.notifyCount(), "state %s", st)
	}
}

// ---------- success: every real terminal path reaches the handler ----------

// Run completion: run() -> SetCompleted -> OnChildTerminal.
func TestGroupDispatch_RunCompletionReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateQueued, snapshotOf(t, singleNodeWorkflow()))

	require.NoError(t, g.f.engine.StartInstance(context.Background(), child))
	waitForInstance(t, g.f.instanceRepo, child, 5*time.Second, instance.StateCompleted)

	require.Eventually(t, func() bool { return len(g.handler.called()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID))
}

// Node failure with halt: run() -> handleNodeError -> failInstance -> OnChildTerminal.
func TestGroupDispatch_RunFailureReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	g.f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, errors.New("boom")
	})
	child := g.task(t, "a", instance.StateQueued, snapshotOf(t, singleNodeWorkflow()))

	require.NoError(t, g.f.engine.StartInstance(context.Background(), child))
	waitForInstance(t, g.f.instanceRepo, child, 5*time.Second, instance.StateFailed)

	require.Eventually(t, func() bool { return len(g.handler.called()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
}

// Cancellation: Cancel -> cancelAuthorized -> OnChildTerminal.
func TestGroupDispatch_CancelReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateRunning, nil)

	require.NoError(t, g.f.engine.Cancel(bgAdmin(), child))

	assert.Equal(t, instance.StateCancelled, g.stateOf(t, child))
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
}

// Webhook timeout fails the run from INSIDE domain/queue, which reaches the
// engine only through the child-terminal hook main.go wires
// (queueMgr.SetChildTerminalHook(eng.OnChildTerminal)). The real Manager is
// used so the hop is the production one.
func TestGroupDispatch_WebhookTimeoutFromQueueReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateWaiting, nil)
	token := "tok"
	require.NoError(t, g.f.instanceRepo.SetWebhookFields(context.Background(), child, &token, nil))

	mgr := queue.NewManager(g.f.queueRepo, g.f.instanceRepo, &config.Config{}, g.f.engine)
	mgr.SetChildTerminalHook(g.f.engine.OnChildTerminal)

	require.NoError(t, mgr.OnTimerFired(context.Background(), child, engine.TimerTypeWebhook))

	assert.Equal(t, instance.StateFailed, g.stateOf(t, child))
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
}

// LEGACY UNTOUCHED: an awaited legacy child of the same waiting parent at the
// same node still wakes it through the three-part match, and never reaches
// the group handler.
func TestGroupDispatch_LegacyWaitForChildStillWakesWithHandlerWired(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.legacyChild(t, instance.SpawnModeWaitForChild, instance.StateCompleted)

	g.f.engine.OnChildTerminal(context.Background(), child)

	assert.Equal(t, instance.StateQueued, g.stateOf(t, g.parentID))
	assert.GreaterOrEqual(t, g.f.queueManager.notifyCount(), 1)
	assert.Empty(t, g.handler.called())
}

func TestGroupDispatch_FireAndForgetChildNeverReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.legacyChild(t, instance.SpawnModeFireAndForget, instance.StateCompleted)

	g.f.engine.OnChildTerminal(context.Background(), child)

	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID))
	assert.Empty(t, g.handler.called())
}

// ---------- failure ----------

// A call that arrives before the terminal write (a non-terminal child) is not
// dispatched: the real terminal write calls again.
func TestGroupDispatch_NonTerminalGroupTaskIsNotDispatched(t *testing.T) {
	for _, st := range []instance.State{
		instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused,
	} {
		g := newGroupDispatchFixture(t, true)
		child := g.task(t, "a", st, nil)

		g.f.engine.OnChildTerminal(context.Background(), child)

		assert.Empty(t, g.handler.called(), "state %s", st)
	}
}

// ---------- error ----------

// An unreadable child cannot be classified, so nothing is dispatched; the
// coordinator's reconciler reads the row later.
func TestGroupDispatch_UnreadableChildIsNotDispatched(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateCompleted, nil)
	g.f.instanceRepo.mu.Lock()
	g.f.instanceRepo.getErr = errors.New("db down")
	g.f.instanceRepo.mu.Unlock()

	g.f.engine.OnChildTerminal(context.Background(), child)

	assert.Empty(t, g.handler.called())
}

func TestSetGroupTerminalHandler_PanicsOnNil(t *testing.T) {
	f := newTestFixtures()
	assert.Panics(t, func() { f.engine.SetGroupTerminalHandler(nil) })
}

// PROVENANCE PROBE: with the handler constructed but NOT wired, a terminal
// group task reaches nobody — no handler call, no legacy wake, no panic. This
// is the state P2-T14's e2e detects at the composition root.
func TestGroupDispatch_UnsetHandlerIsALoggedNoOp(t *testing.T) {
	g := newGroupDispatchFixture(t, false)
	child := g.task(t, "a", instance.StateCompleted, nil)

	require.NotPanics(t, func() { g.f.engine.OnChildTerminal(context.Background(), child) })

	assert.Empty(t, g.handler.called())
	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID))
	assert.Equal(t, 0, g.f.queueManager.notifyCount())
}

// ---------- edge cases ----------

// RE-ENTRANCY CONTRACT for the coordinator: a handler that cancels a sibling
// task (cancel_unfinished) re-enters OnChildTerminal on the SAME goroutine via
// cancelAuthorized. The engine holds no lock across the dispatch, so this must
// complete, recording [A, B] in that order.
func TestGroupDispatch_HandlerMayBeReenteredThroughCancel(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	a := g.task(t, "a", instance.StateRunning, nil)
	b := g.task(t, "b", instance.StateRunning, nil)
	g.handler.onCall = func(_ context.Context, childID uuid.UUID) {
		if childID == a {
			_ = g.f.engine.Cancel(bgAdmin(), b)
		}
	}

	done := make(chan error, 1)
	go func() { done <- g.f.engine.Cancel(bgAdmin(), a) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch deadlocked when the handler re-entered the engine through Cancel")
	}

	assert.Equal(t, []uuid.UUID{a, b}, g.handler.called())
	assert.Equal(t, instance.StateCancelled, g.stateOf(t, a))
	assert.Equal(t, instance.StateCancelled, g.stateOf(t, b))
}

// RECOVERY CONTRACT: crash recovery marks a crashed group task failed WITHOUT
// dispatching. The group coordinator's Reconcile — run by main.go immediately
// after RecoverCrashedInstances (P2-T14) — reads the persisted state and is the
// authority, exactly as SpawnWaitReconciler is for legacy waits. If this test
// starts failing because someone added a dispatch, they must also prove the
// coordinator tolerates side effects before queue timer/webhook recovery.
func TestRecoverCrashedInstances_DoesNotDispatchGroupTasks(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateRunning, nil)

	require.NoError(t, g.f.engine.RecoverCrashedInstances())

	assert.Equal(t, instance.StateFailed, g.stateOf(t, child))
	assert.Empty(t, g.handler.called())
	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID), "a waiting parent is untouched by crash recovery")
}
```

- [x] Step 2: Run them, confirm they fail.

  Confirmed the expected compile failure: `engine.GroupTerminalHandler` and `Engine.SetGroupTerminalHandler` are undefined.

```bash
go test ./domain/engine/ -run 'TestGroupDispatch|TestSetGroupTerminalHandler|TestRecoverCrashedInstances_DoesNotDispatchGroupTasks' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_dispatch_test.go:46:15: undefined: engine.GroupTerminalHandler
domain/engine/group_dispatch_test.go:71:14: f.engine.SetGroupTerminalHandler undefined (type *engine.Engine has no field or method SetGroupTerminalHandler)
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement.

(a) Create `domain/engine/group_dispatch.go`:

```go
package engine

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupTerminalHandler receives the terminal transition of a parallel-group
// task child. *GroupCoordinator satisfies it.
//
// It is VOID and best-effort, like OnChildTerminal itself: the coordinator's
// Reconcile (startup + periodic sweep) reads persisted child state and is the
// authority for correctness; this in-process hop only makes the common case
// immediate.
//
// RE-ENTRANCY: it is called synchronously from the goroutine that performed
// the terminal write, with no engine lock held. An implementation that cancels
// sibling tasks through RunCanceller.Cancel is re-entered (cancelAuthorized
// calls OnChildTerminal for each sibling it cancels), so it must not hold an
// in-process lock across those calls.
type GroupTerminalHandler interface {
	OnTaskTerminal(ctx context.Context, childID uuid.UUID)
}

// SetGroupTerminalHandler wires the parallel-group coordinator into
// OnChildTerminal. The composition root MUST call it before FreezeExecutors
// (i.e. before any goroutine can drive a run to a terminal state).
//
// h MUST be non-nil: a nil here is a programming error that would silently
// downgrade every group's in-process decision to "wait for the next sweep", so
// it panics rather than fails open — the posture SetSpawnLinks takes. Not
// calling it at all is the explicit "groups are settled by the reconciler
// only" state, which logs a warning per dropped terminal.
func (e *Engine) SetGroupTerminalHandler(h GroupTerminalHandler) {
	if h == nil {
		panic("engine: SetGroupTerminalHandler requires a non-nil GroupTerminalHandler")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.groupHandler = h
}

// groupTerminalHandler reads the wired handler under the same lock the setter
// takes. The lock is released before the handler runs.
func (e *Engine) groupTerminalHandler() GroupTerminalHandler {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.groupHandler
}

// dispatchGroupTerminal hands a group task's terminal transition to the
// coordinator. It is the ONLY thing OnChildTerminal does for a group task: the
// legacy wait-for-child wake (SetQueuedIfWaiting on the parent) must never run
// for one, because only the coordinator knows whether the group's completion
// rule is satisfied and owns the node-addressed wake.
func (e *Engine) dispatchGroupTerminal(ctx context.Context, child *instance.Instance) {
	if !child.State.IsTerminal() {
		return // called too early; the real terminal write will call again
	}
	h := e.groupTerminalHandler()
	if h == nil {
		runID, key := "", ""
		if child.GroupRunID != nil {
			runID = child.GroupRunID.String()
		}
		if child.GroupTaskKey != nil {
			key = *child.GroupTaskKey
		}
		slog.Warn("group task reached a terminal state but no group terminal handler is wired; only the group reconciler can settle its group",
			"childID", child.ID, "groupRunID", runID, "taskKey", key, "state", child.State)
		return
	}
	h.OnTaskTerminal(ctx, child.ID)
}
```

(b) `domain/engine/engine.go` — in the `Engine` struct, insert immediately after the `spawnLinks instance.SpawnLinkRepository` field (line 49):

```go
	// groupHandler settles parallel-group tasks: OnChildTerminal hands every
	// terminal group-task child to it instead of the legacy wake. Nil until
	// SetGroupTerminalHandler; guarded by mu (read via groupTerminalHandler).
	groupHandler GroupTerminalHandler
```

and replace the `RecoverCrashedInstances` doc comment (lines 1763-1764) with:

```go
// RecoverCrashedInstances finds instances that are in running state
// (from a previous crash) and sets them to failed.
//
// It deliberately does NOT call OnChildTerminal for the runs it fails. The
// startup reconcilers that the composition root runs IMMEDIATELY afterwards
// read the persisted states and are the authority: SpawnWaitReconciler resumes
// a legacy parent whose awaited child crashed, and GroupCoordinator.Reconcile
// takes in a crashed group task as failed (and, for a crashed group PARENT,
// cancels its still-active tasks and settles the group). Dispatching here would
// duplicate that work and run coordinator side effects before the queue has
// re-registered timers and webhooks.
```

(c) `domain/engine/spawn_wake.go` — replace the doc comment's first line (line 12) with the two paragraphs below (keep the rest of the comment, lines 13-34, unchanged):

```go
// OnChildTerminal reacts to childID reaching a terminal state.
//
// A PARALLEL-GROUP task (SpawnModeGroupTask) is handed to the group terminal
// handler and NOTHING ELSE happens here: whether its parent may continue
// depends on the group's completion rule and is decided, durably, by the
// coordinator, which also owns the node-addressed wake. A legacy spawn child
// re-queues the parent that is waiting on its spawn node, as follows.
```

and in the body, replace lines 43-48:

```go
	if child.IsRoot() {
		return // a root run has nobody to wake
	}
	if child.SpawnMode != instance.SpawnModeWaitForChild {
		return // fire-and-forget child outcomes never affect the parent
	}
```

with:

```go
	if child.IsRoot() {
		return // a root run has nobody to wake
	}
	if child.SpawnMode == instance.SpawnModeGroupTask {
		// Checked BEFORE the legacy match: a group task must never reach the
		// wait-for-child wake below, even when its parent happens to be waiting
		// at the very node the task is linked to.
		e.dispatchGroupTerminal(ctx, child)
		return
	}
	if child.SpawnMode != instance.SpawnModeWaitForChild {
		return // fire-and-forget child outcomes never affect the parent
	}
```

- [x] Step 4: Run the new tests, then the whole engine and queue suites.

Completion pass after P2-T14 (`fd8d000`): both previous blockers are resolved. The focused dispatch/legacy regression tests, full engine and queue suites, and focused dispatch race gate pass. Real `initDomain` branch/workflow/entrypoint tests and startup crash reconciliation pass with `-race`, asserting nonempty persisted parent results and continuation. No implementation change was needed; this pass closes the remaining verification gate only.

> RESOLVED (P2-T16 atomic receipts; historical evidence): Focused dispatch/legacy regression tests and `go test -race ./domain/engine/ -run 'TestGroupDispatch' -count=1` pass; the queue suite passes. The full engine suite fails the existing `TestParallelGroupExecutor_EngineIntegration_ParentVariablesHoldOrderedResultsWithoutMerging`. Reproduced on unchanged HEAD `b7b19e1` in a temporary `git archive` checkout: `terminal group task "a" has no atomic receipt; verify terminal persistence wiring`. P2-T16's receipt clock/triggers remain absent (its plan uses numbered steps rather than checkboxes). Do not replace real terminal persistence with fabricated receipts.

> RESOLVED (P2-T14 production wiring and sink tests; historical evidence): Wired-and-fed completion also awaits P2-T14's explicit composition-root `eng.SetGroupTerminalHandler(groupCoord)` and real sink e2e. `cmd/arbitron/main.go` currently constructs no group coordinator. This pass implements only P2-T13, not P2-T14 or P2-T16, and does not claim callback-recording unit tests prove parent continuation. Leave this gate unchecked until real production construction/input and persisted parent output are verified.

  Initial implementation self-review (historical; both blockers are now resolved), in order: (1) spec compliance: terminal group tasks dispatch synchronously once per call, before and instead of legacy wake; recovery behavior is unchanged, no duplicate events or new options. (2) anti-green-theater: completion/failure/cancel/queue-timeout tests drive actual terminal producers; re-entry asserts both siblings' persisted cancellation states using the dispatched child ID. These are port/producer tests, not an end-to-end coordinator sink proof; the missing production proof is explicitly blocked above. (3) code quality: checked neighboring spawn and engine patterns; handler access is mutex-protected and the callback runs after unlock; compile-time assertion checks the real coordinator satisfies the port. Re-ran focused tests and race checks after review.

```bash
go test ./domain/engine/ -run 'TestGroupDispatch|TestSetGroupTerminalHandler|TestRecoverCrashedInstances_DoesNotDispatchGroupTasks|TestOnChildTerminal|TestCancelCascade|TestSpawnWait' -count=1
go test ./domain/engine/ ./domain/queue/ -count=1
go test -race ./domain/engine/ -run 'TestGroupDispatch' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
ok  	github.com/arbitron/arbitron/domain/queue	X.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(The existing `spawn_wake_test.go` suite proves the legacy match is unchanged; `-race` proves the handler field is read and written under `mu`.)

Completion self-review, in the required order:
1. Spec compliance: reviewed dispatch, `OnChildTerminal`, all terminal call sites and crash recovery against this task's contracts. Group tasks dispatch synchronously before and instead of the legacy wake, with no engine lock held; recovery remains reconciler-driven. No extra feature, option or production behavior was added.
2. Anti-green-theater: the real-root tests drive terminal producers and assert the parent's persisted nonempty results and exactly-once continuation. Temporarily removed only `initDomain`'s `eng.SetGroupTerminalHandler(groupCoord)`: `TestGroupE2E_EntrypointGroupWithSingleTask` failed after 40s because the parent never reached a terminal state (before the 60s sweep). Restored that exact line and reran all four root/race tests green. No mutation remains; callback-recording tests are not used as a substitute for the downstream sink.
3. Code quality: checked neighboring spawn/executor and engine conventions, mutex scope, collaborator construction and formatting. No cleanup was necessary. `gofmt -l`, `go vet ./domain/engine/` and `git diff --check` are clean. Reran task gates after review, all green.

Additional real-root verification:
```bash
go test -race ./cmd/arbitron/ -run 'TestGroupE2E_(BranchesAllSucceedGatherResultsInAuthorOrder|WorkflowsFormRepeatsOneChildWithDistinctOrderedResults|EntrypointGroupWithSingleTask|StartupReconcileSettlesGroupAfterCrash)$' -count=1
```

- [x] Step 5: gofmt, vet, commit.

  Completion pass commits the resolved verification gate and self-review evidence only; implementation and wiring were already committed by their owning tasks.

  Initial implementation pass (historical): formatted/vetted and committed the unblocked dispatch implementation and tests together with blocker evidence, leaving Step 4 unchecked at that time. The completion pass above now closes it.

```bash
gofmt -l domain/engine/group_dispatch.go domain/engine/group_dispatch_test.go domain/engine/spawn_wake.go domain/engine/engine.go
go vet ./domain/engine/
git add domain/engine/group_dispatch.go domain/engine/group_dispatch_test.go domain/engine/spawn_wake.go domain/engine/engine.go
git commit -m "feat(engine): dispatch group-task terminals to the group coordinator" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
(`gofmt -l` and `go vet` print nothing.)
