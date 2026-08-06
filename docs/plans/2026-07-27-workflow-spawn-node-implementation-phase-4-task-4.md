# Phase 4 — Task 4: the child-terminal wake

**Goal:** When a child reaches a terminal state, re-queue the parent that is waiting on *that specific spawn node* — and no other parent, and no other node.

**Architecture:** New file `domain/engine/spawn_wake.go` holds `Engine.OnChildTerminal`. The match condition is the correctness core of this phase and has **three** conjuncts, all required. The wake is invoked from a small set of consolidated points — `failInstance`, the run loop's successful completion, and `Cancel` — and the run loop's three direct `SetFailed` calls are routed through the `failInstance` helper that exists specifically to consolidate that pattern, shrinking the hook surface. The webhook-timeout path lives in `domain/queue` and cannot import `domain/engine`, so it receives an **optional, nil-safe hook setter** — the pattern `Manager` already uses for `SetWebhookRegistry` and `SetScheduler`.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 4: `OnChildTerminal` + invocation sites + queue hook

**Files:**
- Create: `domain/engine/spawn_wake.go`
- Modify: `domain/engine/engine.go:685`, `:719`, `:739` (route through `failInstance`), `:854-868` (`failInstance` calls the wake), `:833-850` (run completion), `:1385-1424` (`Cancel`)
- Modify: `domain/queue/manager.go:33-68` (hook field + setter), `:183-195` (webhook-timeout invocation)
- Modify: `cmd/arbitron/main.go` (wire the hook)
- Test:   `domain/engine/spawn_wake_test.go`
- Test:   `domain/queue/manager_child_hook_test.go`

---

- [x] **Step 1: Write the failing engine test**

Create `domain/engine/spawn_wake_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wakeFixture builds a waiting parent at `parentNode` plus a child linked to
// `childNode` with `mode`, in state `childState`.
type wakeFixture struct {
	f        *testFixtures
	parentID uuid.UUID
	childID  uuid.UUID
}

func newWakeFixture(t *testing.T, parentNode, childNode string,
	mode instance.SpawnMode, parentState, childState instance.State) *wakeFixture {
	t.Helper()
	f := newTestFixtures()
	ctx := context.Background()
	owner := uuid.New()

	node := parentNode
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "parent",
		State: parentState, Priority: instance.PriorityNormal, CurrentNodeID: &node,
	}
	require.NoError(t, f.instanceRepo.Create(ctx, parent))

	pid := parent.ID
	cn := childNode
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "child",
		State: childState, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &cn, SpawnMode: mode,
	}
	require.NoError(t, f.instanceRepo.Create(ctx, child))

	return &wakeFixture{f: f, parentID: parent.ID, childID: child.ID}
}

func (w *wakeFixture) parentState(t *testing.T) instance.State {
	t.Helper()
	got, err := w.f.instanceRepo.Get(context.Background(), w.parentID)
	require.NoError(t, err)
	return got.State
}

// ---------- happy path ----------

func TestOnChildTerminal_WakesTheWaitingParent(t *testing.T) {
	w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateWaiting, instance.StateCompleted)

	w.f.engine.OnChildTerminal(context.Background(), w.childID)

	assert.Equal(t, instance.StateQueued, w.parentState(t),
		"a terminal awaited child must re-queue its waiting parent")
	assert.GreaterOrEqual(t, w.f.queueManager.notifyCount(), 1,
		"the wake must notify the queue so the parent is actually picked up")
}

// ---------- success ----------

func TestOnChildTerminal_WakesOnFailedAndCancelledChildren(t *testing.T) {
	for _, st := range []instance.State{instance.StateFailed, instance.StateCancelled} {
		w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeWaitForChild,
			instance.StateWaiting, st)
		w.f.engine.OnChildTerminal(context.Background(), w.childID)
		assert.Equal(t, instance.StateQueued, w.parentState(t), "child state %s", st)
	}
}

// ---------- the three-part match: each conjunct is required ----------

// THE CENTRAL MIS-WAKE GUARD. A fire-and-forget child launched earlier from
// node A reaching a terminal state while the parent waits at node B must NOT
// wake the parent — it would resume at node B and route by an event that had
// nothing to do with it.
func TestOnChildTerminal_NodeMismatchDoesNotWake(t *testing.T) {
	w := newWakeFixture(t, "spawn-B", "spawn-A", instance.SpawnModeFireAndForget,
		instance.StateWaiting, instance.StateCompleted)

	w.f.engine.OnChildTerminal(context.Background(), w.childID)

	assert.Equal(t, instance.StateWaiting, w.parentState(t),
		"node A's child must not wake a parent waiting at node B")
}

func TestOnChildTerminal_FireAndForgetModeDoesNotWake(t *testing.T) {
	w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateWaiting, instance.StateCompleted)

	w.f.engine.OnChildTerminal(context.Background(), w.childID)

	assert.Equal(t, instance.StateWaiting, w.parentState(t),
		"fire-and-forget child outcomes must never affect the parent")
}

// A parent that is no longer waiting is a NO-OP. This is what makes
// cancel-then-cascade safe: a parent cancelled first cannot be resurrected by
// its dying children.
func TestOnChildTerminal_NonWaitingParentIsANoOp(t *testing.T) {
	for _, st := range []instance.State{
		instance.StateCancelled, instance.StateCompleted, instance.StateFailed,
		instance.StateRunning, instance.StatePaused, instance.StateQueued,
	} {
		w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeWaitForChild,
			st, instance.StateCompleted)
		w.f.engine.OnChildTerminal(context.Background(), w.childID)
		assert.Equal(t, st, w.parentState(t), "parent state %s must be untouched", st)
	}
}

// ---------- edge cases ----------

func TestOnChildTerminal_RootRunIsANoOp(t *testing.T) {
	f := newTestFixtures()
	root := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "root",
		State: instance.StateCompleted, Priority: instance.PriorityNormal,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), root))

	// Must not panic and must not touch anything.
	f.engine.OnChildTerminal(context.Background(), root.ID)
	assert.Equal(t, 0, f.queueManager.notifyCount())
}

func TestOnChildTerminal_UnknownChildIsANoOp(t *testing.T) {
	f := newTestFixtures()
	f.engine.OnChildTerminal(context.Background(), uuid.New())
	assert.Equal(t, 0, f.queueManager.notifyCount())
}

// A DUPLICATE wake (the in-process hook plus the startup reconciler) is a no-op
// on the second call, because the parent is no longer in the waiting state.
func TestOnChildTerminal_DuplicateWakeIsANoOp(t *testing.T) {
	w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateWaiting, instance.StateCompleted)

	w.f.engine.OnChildTerminal(context.Background(), w.childID)
	first := w.f.queueManager.notifyCount()
	w.f.engine.OnChildTerminal(context.Background(), w.childID)

	assert.Equal(t, instance.StateQueued, w.parentState(t))
	assert.Equal(t, first, w.f.queueManager.notifyCount(),
		"the second wake must be a no-op: the parent is no longer waiting")
}

// A parent whose CurrentNodeID is nil (never advanced) cannot match.
func TestOnChildTerminal_ParentWithNoCurrentNodeIsANoOp(t *testing.T) {
	f := newTestFixtures()
	ctx := context.Background()
	owner := uuid.New()

	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "parent",
		State: instance.StateWaiting, Priority: instance.PriorityNormal,
	}
	require.NoError(t, f.instanceRepo.Create(ctx, parent))

	pid := parent.ID
	node := "spawn-1"
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "child",
		State: instance.StateCompleted, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeWaitForChild,
	}
	require.NoError(t, f.instanceRepo.Create(ctx, child))

	f.engine.OnChildTerminal(ctx, child.ID)
	got, err := f.instanceRepo.Get(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, got.State)
}

// ---------- invocation sites ----------

// The run loop's successful completion path invokes the wake, so a completed
// child re-queues its parent with no reconciler pass needed.
func TestRunCompletion_InvokesTheChildTerminalWake(t *testing.T) {
	w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateWaiting, instance.StateRunning)

	// Mark the child completed via the same repository call the run loop uses,
	// then invoke the wake as the run loop does.
	require.NoError(t, w.f.instanceRepo.SetCompleted(context.Background(), w.childID))
	w.f.engine.OnChildTerminal(context.Background(), w.childID)

	assert.Equal(t, instance.StateQueued, w.parentState(t))
}
```

Add a `notifyCount` accessor to `mockQueueManager` in
`domain/engine/engine_test.go`:

```go
// notifyCount reports how many times NotifyCapacityFreed was called. Test-only.
func (q *mockQueueManager) notifyCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.notifyCalls
}
```

and increment `notifyCalls` inside `NotifyCapacityFreed` if the existing mock
does not already track it (add the `notifyCalls int` field beside the mutex).

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run 'TestOnChildTerminal|TestRunCompletion_Invokes'
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/spawn_wake_test.go:...: w.f.engine.OnChildTerminal undefined
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] **Step 3: Implement the wake**

Create `domain/engine/spawn_wake.go`:

```go
package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// OnChildTerminal re-queues the parent that is waiting on childID's spawn node.
//
// THE MATCH CONDITION IS THE CORRECTNESS CORE OF WAIT-FOR-CHILD. A terminal
// child wakes its parent ONLY IF all three hold:
//
//	the parent is in the WAITING state
//	∧ the parent's CURRENT node id == the child's recorded parent node id
//	∧ the link's mode is wait_for_child
//
// Every conjunct is required:
//
//   - Matching only on "this child's parent is waiting" is wrong: a parent can
//     be waiting at spawn node B while a fire-and-forget child launched earlier
//     from node A reaches a terminal state. That child would re-queue the
//     parent, which would resume at node B, short-circuit against B's
//     still-running child, and — worse, if B's child had by then finished —
//     route node B by an event that had nothing to do with it.
//   - The mode conjunct alone is not sufficient either, because a parent may
//     carry two awaited spawn nodes in sequence, each with its own child.
//
// It is deliberately VOID and best-effort. A missed wake is recovered by
// SpawnWaitReconciler at the next startup; a wake that fails must never fail the
// terminal transition that triggered it.
func (e *Engine) OnChildTerminal(ctx context.Context, childID uuid.UUID) {
	if ctx == nil {
		ctx = context.Background()
	}
	child, err := e.instances.Get(ctx, childID)
	if err != nil {
		return // an unreadable child cannot identify a parent; the reconciler covers it
	}
	if child.ParentInstanceID == nil || child.ParentNodeID == nil {
		return // a root run has nobody to wake
	}
	if child.SpawnMode != instance.SpawnModeWaitForChild {
		return // fire-and-forget child outcomes never affect the parent
	}
	if !child.State.IsTerminal() {
		return // called too early; the real terminal write will call again
	}

	parent, err := e.instances.Get(ctx, *child.ParentInstanceID)
	if err != nil {
		return
	}
	// A parent that is no longer waiting is a NO-OP. This mirrors OnTimerFired's
	// guard and is what makes cancel-then-cascade safe: a parent cancelled first
	// cannot be resurrected through its own failure port by the very children
	// being torn down.
	if parent.State != instance.StateWaiting {
		return
	}
	if parent.CurrentNodeID == nil || *parent.CurrentNodeID != *child.ParentNodeID {
		return
	}

	if err := e.queueRepo.SetQueued(ctx, parent.ID, time.Now()); err != nil {
		// Surfaced but not fatal: the reconciler recovers this parent on the next
		// startup.
		slog.Error("spawn wake: failed to re-queue parent",
			"parentID", parent.ID, "childID", childID, "error", err)
		return
	}
	e.queueManager.NotifyCapacityFreed(ctx)
}
```

- [x] **Step 4: Invoke the wake from the consolidated terminal points**

**(a) `failInstance`** — replace the body of `failInstance`
(`domain/engine/engine.go:861-869`):

```go
func (e *Engine) failInstance(ctx context.Context, instanceID uuid.UUID, nodeID string) bool {
	if err := e.instances.SetFailed(ctx, instanceID, nodeID); err != nil {
		slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
		return false
	}
	e.warnWorkspaceRetained(instanceID, "instance_failed")
	// A failed run may be somebody's awaited child. Use a background context: the
	// run context is often already cancelled at this point.
	e.OnChildTerminal(context.Background(), instanceID)
	return true
}
```

**(b) Route the run loop's three direct `SetFailed` calls through the helper.**
These call `SetFailed` directly instead of the `failInstance` helper that exists
specifically to consolidate the pattern and that the rest of the loop uses.
Routing them through it reduces the wake surface and is a focused cleanup of
code this feature touches.

- `domain/engine/engine.go:685` (panic recovery):

```go
			if e.failInstance(ctx, instanceID, "") {
				e.emitNotification(context.Background(), instanceID, workflowName, notification.TypeFailed)
			}
```

- `domain/engine/engine.go:719` (snapshot parse failure):

```go
		e.failInstance(ctx, instanceID, "")
		return
```

- `domain/engine/engine.go:739` (no entrypoint / no current node):

```go
		e.failInstance(ctx, instanceID, "")
		return
```

**(c) Run-loop completion** — in `run()`, in the success branch after
`SetCompleted` succeeds (`domain/engine/engine.go:836-844`), add the wake after
`e.cleanupWorkspace(instanceID)`:

```go
	} else {
		e.emitNotification(ctx, instanceID, workflowName, notification.TypeCompleted)
		// Workspace is removed only after SetCompleted succeeds.
		e.cleanupWorkspace(instanceID)
		// A completed run may be somebody's awaited child.
		e.OnChildTerminal(context.Background(), instanceID)
	}
```

**(d) `Cancel`** — at the end of `Cancel`
(`domain/engine/engine.go:1421-1424`), after `recordIntervention`:

```go
	e.recordIntervention(ctx, inst, instance.ActionCancel, nil)
	// A cancelled run may be somebody's awaited child; a cancelled child routes
	// its parent through the failure port (there is no separate cancellation
	// path).
	e.OnChildTerminal(context.Background(), instanceID)
	return nil
```

- [x] **Step 5: Run the engine tests, confirm they pass**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run 'TestOnChildTerminal|TestRunCompletion_Invokes' -v
```

Expected output:

```
--- PASS: TestOnChildTerminal_WakesTheWaitingParent (0.00s)
--- PASS: TestOnChildTerminal_WakesOnFailedAndCancelledChildren (0.00s)
--- PASS: TestOnChildTerminal_NodeMismatchDoesNotWake (0.00s)
--- PASS: TestOnChildTerminal_FireAndForgetModeDoesNotWake (0.00s)
--- PASS: TestOnChildTerminal_NonWaitingParentIsANoOp (0.00s)
--- PASS: TestOnChildTerminal_RootRunIsANoOp (0.00s)
--- PASS: TestOnChildTerminal_UnknownChildIsANoOp (0.00s)
--- PASS: TestOnChildTerminal_DuplicateWakeIsANoOp (0.00s)
--- PASS: TestOnChildTerminal_ParentWithNoCurrentNodeIsANoOp (0.00s)
--- PASS: TestRunCompletion_InvokesTheChildTerminalWake (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.6s
```

- [x] **Step 6: Write the failing queue-hook test**

Create `domain/queue/manager_child_hook_test.go`:

```go
package queue

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The hook is OPTIONAL and nil-safe, the same pattern SetWebhookRegistry and
// SetScheduler already use. domain/queue cannot import domain/engine, so the
// composition root supplies the engine's wake as a function value.
func TestManager_ChildTerminalHook_IsNilSafe(t *testing.T) {
	m := &Manager{}
	// Must not panic with no hook wired.
	require.NotPanics(t, func() { m.notifyChildTerminal(context.Background(), uuid.New()) })
}

func TestManager_SetChildTerminalHook_IsInvoked(t *testing.T) {
	var got uuid.UUID
	calls := 0
	m := &Manager{}
	m.SetChildTerminalHook(func(_ context.Context, id uuid.UUID) {
		calls++
		got = id
	})

	want := uuid.New()
	m.notifyChildTerminal(context.Background(), want)

	assert.Equal(t, 1, calls)
	assert.Equal(t, want, got, "the hook must receive the id of the instance that went terminal")
}
```

- [x] **Step 7: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/queue/ -run TestManager_
```

Expected output:

```
# github.com/arbitron/arbitron/domain/queue [github.com/arbitron/arbitron/domain/queue.test]
domain/queue/manager_child_hook_test.go:...: m.notifyChildTerminal undefined
domain/queue/manager_child_hook_test.go:...: m.SetChildTerminalHook undefined
FAIL	github.com/arbitron/arbitron/domain/queue [build failed]
```

- [x] **Step 8: Implement the queue hook**

In `domain/queue/manager.go`, add the field to `Manager` (after `scheduler`):

```go
	scheduler       TimerSchedulerPort
	// childTerminal is an OPTIONAL, nil-safe hook invoked when this Manager
	// drives an instance to a terminal state. It exists because the
	// webhook-timeout path below fails an instance from INSIDE domain/queue,
	// which cannot import domain/engine to call the spawn wake directly. Same
	// optional-collaborator pattern as SetWebhookRegistry / SetScheduler.
	childTerminal   func(ctx context.Context, instanceID uuid.UUID)
	mu              sync.Mutex
```

Add the setter beside `SetScheduler`:

```go
// SetChildTerminalHook wires the callback invoked when this Manager drives an
// instance to a terminal state, so a waiting spawn parent can be woken. Nil
// (unset) is valid: the startup reconciler is the authority for correctness and
// recovers any parent this hook misses.
func (m *Manager) SetChildTerminalHook(h func(ctx context.Context, instanceID uuid.UUID)) {
	m.childTerminal = h
}

// notifyChildTerminal invokes the optional hook. Nil-safe.
func (m *Manager) notifyChildTerminal(ctx context.Context, instanceID uuid.UUID) {
	if m.childTerminal == nil {
		return
	}
	m.childTerminal(ctx, instanceID)
}
```

In `OnTimerFired`'s `TimerTypeWebhook` arm, replace the final `return` with:

```go
	case TimerTypeWebhook:
		// Webhook timeout: deactivate endpoint, fail the instance.
		if m.webhookRegistry != nil && inst.WebhookToken != nil {
			m.webhookRegistry.Deactivate(*inst.WebhookToken)
		}
		if err := m.instances.SetWebhookFields(ctx, instanceID, nil, nil); err != nil {
			slog.Error("queue: failed to clear webhook fields on timeout", "instanceID", instanceID, "error", err)
		}
		failedNodeID := ""
		if inst.CurrentNodeID != nil {
			failedNodeID = *inst.CurrentNodeID
		}
		if err := m.instances.SetFailed(ctx, instanceID, failedNodeID); err != nil {
			return err
		}
		// This instance may be somebody's awaited child; this is the one terminal
		// transition that happens outside domain/engine.
		m.notifyChildTerminal(ctx, instanceID)
		return nil
	}
```

- [x] **Step 9: Wire the hook at the composition root**

In `cmd/arbitron/main.go`, immediately after `queueMgr.SetWebhookRegistry(webhookRegistry)`
(~line 802), add:

```go
	// 12-wh-spawn. The webhook-timeout path fails an instance from inside
	// domain/queue, which cannot import domain/engine. Wire the engine's spawn
	// wake as the optional child-terminal hook so a parent awaiting a
	// webhook-timed-out child is re-queued immediately instead of waiting for the
	// next startup reconciliation.
	queueMgr.SetChildTerminalHook(eng.OnChildTerminal)
```

- [x] **Step 10: Run everything, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/ domain/queue/ cmd/arbitron/
go build ./... && go test ./domain/queue/ -run TestManager_ -v && go test ./domain/engine/ ./domain/queue/ ./cmd/arbitron/
```

Expected output:

```
--- PASS: TestManager_ChildTerminalHook_IsNilSafe (0.00s)
--- PASS: TestManager_SetChildTerminalHook_IsInvoked (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/queue	0.3s
ok  	github.com/arbitron/arbitron/domain/engine	1.2s
ok  	github.com/arbitron/arbitron/domain/queue	0.3s
ok  	github.com/arbitron/arbitron/cmd/arbitron	4.0s
```

- [x] **Step 11: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [x] **Step 12: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/spawn_wake.go domain/engine/spawn_wake_test.go domain/engine/engine.go domain/engine/engine_test.go domain/queue/manager.go domain/queue/manager_child_hook_test.go cmd/arbitron/main.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): wake a waiting spawn parent when its awaited child goes terminal"
```
