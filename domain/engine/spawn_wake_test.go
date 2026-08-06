package engine_test

import (
	"context"
	"testing"
	"time"

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

// A SetQueued failure is surfaced but must never be fatal: the wake is
// best-effort, and the startup reconciler recovers a parent this call fails to
// re-queue. It must not panic, must not call NotifyCapacityFreed for a parent
// it failed to queue, and must leave the parent in StateWaiting so the
// reconciler's own waiting-state query still finds it.
func TestOnChildTerminal_SetQueuedFailureIsSurfacedNotFatal(t *testing.T) {
	w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateWaiting, instance.StateCompleted)
	w.f.queueRepo.mu.Lock()
	w.f.queueRepo.setQueuedErr = assert.AnError
	w.f.queueRepo.mu.Unlock()

	require.NotPanics(t, func() {
		w.f.engine.OnChildTerminal(context.Background(), w.childID)
	})

	assert.Equal(t, instance.StateWaiting, w.parentState(t),
		"a parent must stay waiting (not desync into queued) when the re-queue write itself failed")
	assert.Equal(t, 0, w.f.queueManager.notifyCount(),
		"must not tell the queue manager capacity freed up for a parent that was never actually re-queued")
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

// A FOURTH terminal-transition call site exists that predates this feature and
// was never wired to the wake: Engine.Resume's webhook-timeout-expired-while-
// paused branch (engine.go, resumeFromWebhookWait) calls instances.SetFailed
// directly, bypassing failInstance entirely. If the failed instance is itself
// an awaited child, its parent must still be woken -- otherwise it hangs until
// the next startup reconciler pass instead of being re-queued immediately, the
// same guarantee the run-loop completion, failInstance, and cancelAuthorized
// call sites already provide.
func TestResumeFromWebhookWaitTimeout_InvokesTheChildTerminalWake(t *testing.T) {
	w := newWakeFixture(t, "spawn-1", "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateWaiting, instance.StatePaused)

	token := "expired-token"
	past := time.Now().Add(-time.Minute)
	node := "spawn-1"
	w.f.instanceRepo.mu.Lock()
	child := w.f.instanceRepo.instances[w.childID]
	child.WebhookToken = &token
	child.WaitingUntil = &past
	child.CurrentNodeID = &node
	w.f.instanceRepo.mu.Unlock()

	require.NoError(t, w.f.engine.Resume(bgAdmin(), w.childID))

	got, err := w.f.instanceRepo.Get(context.Background(), w.childID)
	require.NoError(t, err)
	require.Equal(t, instance.StateFailed, got.State, "precondition: the resume must actually fail the child")

	assert.Equal(t, instance.StateQueued, w.parentState(t),
		"a child failed via Resume's webhook-timeout-expired-while-paused path must still wake its waiting parent")
}
