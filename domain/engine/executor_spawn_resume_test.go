package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resumeWith seeds an existing awaited-child link in the given state and
// re-enters the node, exactly as the queue does after a wake.
func resumeWith(t *testing.T, childState instance.State) (*spawnWaitFixture, *engine.NodeResult, error) {
	t.Helper()
	return resumeWithChildVars(t, childState, map[string]any{})
}

// resumeWithChildVars is resumeWith with the child's persisted final variables
// under the test's control. The child instance is seeded in the repository
// because a terminal resume reads those variables back as childOutput.
func resumeWithChildVars(t *testing.T, childState instance.State, childVars map[string]any) (*spawnWaitFixture, *engine.NodeResult, error) {
	t.Helper()
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	childID := seedChild(t, f, childState, childVars)
	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: childID, ChildState: childState})

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	return f, res, err
}

// ---------- happy path ----------

func TestSpawnResume_CompletedChildRoutesSuccess(t *testing.T) {
	f, res, err := resumeWith(t, instance.StateCompleted)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsWait, "a terminal child must produce a NORMAL result so the engine routes")
	assert.Equal(t, workflow.SpawnPortSuccess, res.OutputPort)
	assert.Empty(t, f.spawner.spawned, "resumption must NEVER create a second child")
}

// ---------- success ----------

func TestSpawnResume_FailedChildRoutesFailure(t *testing.T) {
	f, res, err := resumeWith(t, instance.StateFailed)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflow.SpawnPortFailure, res.OutputPort)
	assert.Empty(t, f.spawner.spawned)
}

// The spec is explicit: there is NO separate cancellation path. A cancelled
// child routes through the SAME failure port as a failed one.
func TestSpawnResume_CancelledChildRoutesFailure(t *testing.T) {
	f, res, err := resumeWith(t, instance.StateCancelled)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflow.SpawnPortFailure, res.OutputPort,
		"a cancelled child routes through failure — there is no separate cancellation path")
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnResume_ResultCarriesChildOutcome(t *testing.T) {
	_, res, err := resumeWith(t, instance.StateCompleted)
	require.NoError(t, err)
	out, ok := res.Output.(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, out["childInstanceId"])
	assert.Equal(t, "child", out["childWorkflow"])
	assert.Equal(t, string(instance.StateCompleted), out["childState"],
		"the node output must record HOW the child ended so the trace is self-explanatory")
}

// The "childState in the output" claim was only ever pinned for a Completed
// child (above). resumeFromLink writes output["childState"] BEFORE the
// terminal check, unconditionally, for every one of the seven possible link
// states — but a regression that moved the write to after the terminal
// short-circuit, or hard-coded it in the terminal branch only, would have
// shipped undetected: the Failed/Cancelled routing tests only assert
// OutputPort, and the still-active edge-case test only asserts IsWait. This
// is the same shape of gap fixed repeatedly elsewhere in this loop — a claim
// proven at one call site (Completed) and silently assumed at every sibling
// one.
func TestSpawnResume_ChildStateReflectsExactLinkStateAcrossAllOutcomes(t *testing.T) {
	for _, st := range []instance.State{
		instance.StateCompleted, instance.StateFailed, instance.StateCancelled,
		instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused,
	} {
		_, res, err := resumeWith(t, st)
		require.NoError(t, err, "state %s", st)
		require.NotNil(t, res, "state %s", st)
		out, ok := res.Output.(map[string]any)
		require.True(t, ok, "state %s", st)
		assert.Equal(t, string(st), out["childState"], "state %s must be recorded verbatim", st)
	}
}

// ---------- edge cases ----------

// Re-entering while the child is STILL ACTIVE returns to waiting without
// re-spawning and without re-transitioning state. This is the idempotent path
// that makes a spurious re-entry harmless.
func TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning(t *testing.T) {
	for _, st := range []instance.State{
		instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused,
	} {
		f, res, err := resumeWith(t, st)
		require.NoError(t, err, "state %s", st)
		require.NotNil(t, res)
		assert.True(t, res.IsWait, "state %s must keep the parent waiting", st)
		assert.Empty(t, res.OutputPort, "state %s must not route", st)
		assert.Empty(t, f.spawner.spawned, "state %s must not create a second child", st)
	}
}

// A node-level RETRY on an awaited spawn re-invokes Execute, which finds the
// existing link and short-circuits rather than spawning again — so retry
// semantics on an awaited spawn are IDEMPOTENT, unlike fire-and-forget.
func TestSpawnResume_RetryIsIdempotent(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	_, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	require.NoError(t, err)
	require.Len(t, f.spawner.spawned, 1)

	// Simulate the link the first pass created, then re-invoke.
	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: f.spawner.nextID, ChildState: instance.StateRunning})

	res, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	require.NoError(t, err)
	assert.True(t, res.IsWait)
	assert.Len(t, f.spawner.spawned, 1, "a retry must NOT create a second child")
}

// An AWAITED spawn node revisited through a graph back-edge reads its existing
// TERMINAL link and routes without spawning again. This pins the documented
// loop-body limitation as TESTED behaviour rather than an untested claim: an
// awaited spawn node is not supported inside a loop body, for the same reason
// interactive nodes are not.
func TestSpawnResume_BackEdgeRevisitRoutesFromPreviousChild(t *testing.T) {
	f, res, err := resumeWith(t, instance.StateCompleted)
	require.NoError(t, err)
	assert.Equal(t, workflow.SpawnPortSuccess, res.OutputPort)
	assert.Empty(t, f.spawner.spawned,
		"documented limitation: a back-edge revisit reads the PREVIOUS child's outcome "+
			"instead of spawning a new child (see AGENTS.md loop-body restriction)")
}

// A SECOND awaited spawn node in the same parent is matched independently: the
// link key includes the node id.
func TestSpawnResume_SecondSpawnNodeMatchedIndependently(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	// Node A already has a completed child; node B has none.
	f.spawnWaits.set(f.parentID, "spawn-A", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: uuid.New(), ChildState: instance.StateCompleted})

	nodeB := waitNode() // id "spawn-1"
	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, nodeB, variable.NewStore(), "parent")
	require.NoError(t, err)
	assert.True(t, res.IsWait, "node B must suspend on its OWN first execution, "+
		"not route off node A's completed child")
	assert.Len(t, f.spawner.spawned, 1)
}

// A fire-and-forget link for the same node must not satisfy an awaited lookup.
func TestSpawnResume_FireAndForgetLinkDoesNotSatisfyAwaitedLookup(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeFireAndForget,
		&instance.ChildLink{ChildID: uuid.New(), ChildState: instance.StateCompleted})

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	require.NoError(t, err)
	assert.True(t, res.IsWait, "the mode is part of the link key")
	assert.Len(t, f.spawner.spawned, 1)
}

// seedChild stores a child instance with the given final variables and returns
// its id.
func seedChild(t *testing.T, f *spawnWaitFixture, st instance.State, vars map[string]any) uuid.UUID {
	t.Helper()
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: f.ownerID, WorkflowName: "child",
		State: st, Priority: instance.PriorityNormal, Variables: vars,
	}
	require.NoError(t, f.instances.Create(context.Background(), child))
	return child.ID
}

// ---------- child output reintegration ----------

func childOutputOf(t *testing.T, res *engine.NodeResult) map[string]any {
	t.Helper()
	require.NotNil(t, res)
	out, ok := res.Output.(map[string]any)
	require.True(t, ok)
	co, ok := out["childOutput"].(map[string]any)
	require.True(t, ok, "a terminal resume must carry the child's final variables as childOutput, got %#v", out["childOutput"])
	return co
}

func TestSpawnResume_CompletedChildOutputReturnsToParent(t *testing.T) {
	vars := map[string]any{
		"ticket": "ARB-1",
		"review": map[string]any{"verdict": "approve"},
	}
	_, res, err := resumeWithChildVars(t, instance.StateCompleted, vars)
	require.NoError(t, err)
	assert.Equal(t, workflow.SpawnPortSuccess, res.OutputPort)
	assert.Equal(t, vars, childOutputOf(t, res))
}

// The failure path carries whatever the child produced before it stopped, so
// the parent can decide how to recover from it.
func TestSpawnResume_FailedAndCancelledChildOutputReturnsToParent(t *testing.T) {
	for _, st := range []instance.State{instance.StateFailed, instance.StateCancelled} {
		vars := map[string]any{"partial": map[string]any{"step": float64(2)}}
		_, res, err := resumeWithChildVars(t, st, vars)
		require.NoError(t, err, "state %s", st)
		assert.Equal(t, workflow.SpawnPortFailure, res.OutputPort, "state %s", st)
		assert.Equal(t, vars, childOutputOf(t, res), "state %s", st)
	}
}

// The child's {{workspace}} is ITS sandbox path. Handing it to the parent
// invites a parent template to confuse it with the parent's own workspace.
func TestSpawnResume_ChildWorkspaceIsNotReturned(t *testing.T) {
	_, res, err := resumeWithChildVars(t, instance.StateCompleted, map[string]any{
		"workspace": "/child/sandbox",
		"kept":      "yes",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"kept": "yes"}, childOutputOf(t, res))
}

// A child that recorded no variables still yields a MAP, so parent paths into
// childOutput resolve as "missing" rather than failing on a nil.
func TestSpawnResume_ChildWithNoVariablesYieldsEmptyOutput(t *testing.T) {
	_, res, err := resumeWithChildVars(t, instance.StateCompleted, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, childOutputOf(t, res))
}

// A child that cannot be read must NOT route as though it returned nothing: the
// node errors and the ordinary retry/error-behaviour machinery takes over.
func TestSpawnResume_ChildLoadFailureErrorsInsteadOfRouting(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: uuid.New(), ChildState: instance.StateCompleted}) // never stored

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
}

// A still-active child has no final variables yet, so the wait result carries
// no childOutput — and must not even try to read the child.
func TestSpawnResume_ActiveChildCarriesNoChildOutput(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: uuid.New(), ChildState: instance.StateRunning}) // never stored

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	require.NoError(t, err)
	assert.True(t, res.IsWait)
	out := res.Output.(map[string]any)
	assert.NotContains(t, out, "childOutput")
}

// ---------- re-entry of a RUNNING parent (resume after pause) ----------

// A parent paused while waiting on its child is resumed through the run loop,
// so it re-enters the spawn node in state `running`. With the child still
// active it must go back to `waiting`: returning a bare wait result would halt
// the walk with the parent stuck in `running`, where the child's terminal wake
// (which only acts on a WAITING parent) can never reach it.
func TestSpawnResume_ActiveChild_RunningParentReturnsToWaiting(t *testing.T) {
	f, res, err := resumeWith(t, instance.StateRunning)
	require.NoError(t, err)
	require.True(t, res.IsWait)

	got, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, got.State)
	assert.Contains(t, *f.order, "notify", "suspending again releases the parent's capacity")
	assert.Empty(t, f.spawner.spawned)
}

// The child can finish between the re-entry's link read and the parent's
// return to `waiting`; that wake is lost, so the re-entry must route it.
func TestSpawnResume_ActiveChild_FinishingDuringResuspend_Routes(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	childID := seedChild(t, f, instance.StateCompleted, map[string]any{"done": true})
	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: childID, ChildState: instance.StateRunning})
	f.spawnWaits.onFind = func(call int) {
		if call >= 2 {
			f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
				&instance.ChildLink{ChildID: childID, ChildState: instance.StateCompleted})
		}
	}

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	require.NoError(t, err)

	assert.False(t, res.IsWait)
	assert.Equal(t, workflow.SpawnPortSuccess, res.OutputPort)
	got, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateRunning, got.State)
}

// A parent that is no longer running at re-entry (paused or cancelled in the
// meantime) is not moved to `waiting`: that would revive it.
func TestSpawnResume_ActiveChild_NonRunningParentIsNotRevived(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	require.NoError(t, f.instances.UpdateState(context.Background(), f.parentID, instance.StatePaused))
	childID := seedChild(t, f, instance.StateRunning, map[string]any{})
	f.spawnWaits.set(f.parentID, "spawn-1", instance.SpawnModeWaitForChild,
		&instance.ChildLink{ChildID: childID, ChildState: instance.StateRunning})

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	res, err := f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
	require.NoError(t, err)

	assert.True(t, res.IsWait)
	got, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StatePaused, got.State)
}
