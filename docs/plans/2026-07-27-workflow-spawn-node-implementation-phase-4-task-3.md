# Phase 4 — Task 3: resumption short-circuit and branch routing

**Goal:** On re-entry at an awaited spawn node, route the parent through `success` when the child completed and through `failure` when the child failed **or was cancelled**, and never spawn a second child.

**Architecture:** Completes `resumeFromLink` in `domain/engine/executor_spawn.go`. The short-circuit keys on the **link**, not on node-execution status: the interactive node only needs to know *that* it finished, while the spawn node needs to know *how the child ended* to pick a port, and the child's persisted state is the single authority for that. Cancelled children route to `failure` — the spec is explicit that there is no separate cancellation path. The engine's run loop, `findNextNode`, and `handleNodeSuccess` are all unchanged: wait-for-child composes from behaviour that already exists.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 3: branch routing on resumption

**Files:**
- Modify: `domain/engine/executor_spawn.go` (`resumeFromLink`)
- Test:   `domain/engine/executor_spawn_resume_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/executor_spawn_resume_test.go`:

```go
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
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	childID := uuid.New()
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
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestSpawnResume
```

Expected output:

```
--- FAIL: TestSpawnResume_CompletedChildRoutesSuccess (0.00s)
    executor_spawn_resume_test.go:...: a terminal child must produce a NORMAL result so the engine routes
--- FAIL: TestSpawnResume_FailedChildRoutesFailure (0.00s)
    executor_spawn_resume_test.go:...: Not equal: expected: "failure" actual  : ""
--- FAIL: TestSpawnResume_CancelledChildRoutesFailure (0.00s)
--- FAIL: TestSpawnResume_ResultCarriesChildOutcome (0.00s)
FAIL	github.com/arbitron/arbitron/domain/engine	0.5s
```

- [ ] **Step 3: Minimal implementation**

Replace `resumeFromLink` in `domain/engine/executor_spawn.go` with:

```go
// resumeFromLink decides the parent's next move from an EXISTING link.
//
// Keying on the link rather than on node-execution status is deliberate: the
// interactive executor only needs to know THAT it finished, while the spawn
// node needs to know HOW the child ended in order to choose a port. The child's
// persisted state is the single authority for that; a second copy in the node
// record could disagree with it after a retry or a late cancellation.
func (e *SpawnWorkflowExecutor) resumeFromLink(node workflow.Node, link *instance.ChildLink) (*NodeResult, error) {
	output := spawnOutput(link.ChildID, node.Config.ChildWorkflow, node.Config.SpawnMode)
	output["childState"] = string(link.ChildState)

	if !link.ChildState.IsTerminal() {
		// Still active: return a wait result WITHOUT re-spawning and without
		// re-transitioning state. This is the idempotent path that makes a
		// spurious re-entry — a duplicate wake, a node-level retry — harmless.
		return &NodeResult{IsWait: true, Output: output}, nil
	}

	// Terminal: route by outcome. A CANCELLED child routes through failure, the
	// same port as a failed one — the spec is explicit that there is no separate
	// cancellation path.
	port := workflow.SpawnPortFailure
	if link.ChildState == instance.StateCompleted {
		port = workflow.SpawnPortSuccess
	}
	return &NodeResult{OutputPort: port, Output: output}, nil
}
```

- [ ] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run 'TestSpawnResume|TestSpawnWait' -v 2>&1 | tail -25
```

Expected output:

```
--- PASS: TestSpawnResume_CompletedChildRoutesSuccess (0.00s)
--- PASS: TestSpawnResume_FailedChildRoutesFailure (0.00s)
--- PASS: TestSpawnResume_CancelledChildRoutesFailure (0.00s)
--- PASS: TestSpawnResume_ResultCarriesChildOutcome (0.00s)
--- PASS: TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning (0.00s)
--- PASS: TestSpawnResume_RetryIsIdempotent (0.00s)
--- PASS: TestSpawnResume_BackEdgeRevisitRoutesFromPreviousChild (0.00s)
--- PASS: TestSpawnResume_SecondSpawnNodeMatchedIndependently (0.00s)
--- PASS: TestSpawnResume_FireAndForgetLinkDoesNotSatisfyAwaitedLookup (0.00s)
--- PASS: TestSpawnWait_... (all)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.6s
```

- [ ] **Step 5: Confirm the package still passes**

```bash
go test ./domain/engine/
```

Expected: `ok  	github.com/arbitron/arbitron/domain/engine`

- [ ] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/executor_spawn.go domain/engine/executor_spawn_resume_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): route an awaited spawn node by its child's outcome on resumption"
```
