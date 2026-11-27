package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// childHoldSeconds is how long the child lingers before it reaches its outcome
// node, keeping the parent observably suspended for several poll intervals.
const childHoldSeconds = "2"

// bootWaitStack boots the cap-1 deadlock probe: parent and child compete for
// the SAME single per-user slot, so the child can only start once the parent's
// waiting transition releases it. If the executor transitioned before enqueueing
// the child, or skipped NotifyCapacityFreed, this hangs.
//
// A cap of 1 also SERIALIZES the pair, which is why it cannot be the only
// configuration under test: it makes the child's terminal write strictly later
// than the parent's waiting write, hiding the lost-wake race entirely. See
// bootWaitStackWith and the cap-5 test below.
func bootWaitStack(t *testing.T, childOutcome string) *domainStack {
	t.Helper()
	return bootWaitStackWith(t, childOutcome, 1, childHoldSeconds)
}

// bootWaitStackWith writes an awaited parent + child pair and boots the REAL
// composition root, with the per-user cap and the child's opening delay both
// explicit — the two knobs that decide whether the child can reach a terminal
// state BEFORE its parent is durably waiting.
//
// holdSeconds == "" removes the child's opening sleep entirely, so the child is
// a single transform that finishes almost immediately.
func bootWaitStackWith(t *testing.T, childOutcome string, perUserCap int, holdSeconds string) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	// "complete" -> a plain transform that succeeds.
	// "fail"     -> a tool_call to a command that does not exist, which fails the run.
	childNode := `{"id": "c1", "type": "transform",
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}`
	if childOutcome == "fail" {
		childNode = `{"id": "c1", "type": "tool_call",
     "errorBehavior": {"type": "halt"},
     "config": {"command": "arbitron-no-such-binary-for-tests"}}`
	}
	// The child opens with a short sleep so its run outlives a poll interval.
	// Without it the whole spawn -> child -> wake -> resume cycle finishes in
	// well under a second and the parent's WAITING state — the state these tests
	// assert on — is gone before any poll can observe it.
	// With a hold, the child opens with a short sleep so its run outlives a poll
	// interval and the parent's WAITING state is observable. WITHOUT one, the
	// child is a single node that finishes almost immediately — which is the
	// point of the lost-wake test.
	childNodes := childNode + `,
    {"id": "hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "sleep ` + holdSeconds + `; echo '{}'"]}}`
	childEdges := `{"source": "hold", "target": "c1"}`
	if holdSeconds == "" {
		childNodes = strings.Replace(childNode, `"id": "c1",`, `"id": "c1", "entrypoint": true,`, 1)
		childEdges = ""
	}
	writeWorkflow(t, workspaceDir, "wait-child", `{
  "name": "wait-child",
  "description": "child",
  "inputs": [],
  "nodes": [
    `+childNodes+`
  ],
  "edges": [
    `+childEdges+`
  ]
}`)

	writeWorkflow(t, workspaceDir, "wait-parent", `{
  "name": "wait-parent",
  "description": "parent",
  "inputs": [],
  "nodes": [
    {"id": "p1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "spawn-1", "type": "spawn_workflow",
     "config": {"childWorkflow": "wait-child", "spawnMode": "wait_for_child"}},
    {"id": "checkOutput", "type": "conditional",
     "config": {"conditions": [{"field": "spawn-1.childOutput.done", "operator": "eq", "value": true, "output": "got"}],
                "defaultOutput": "missing"}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onNoChildOutput", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success-without-child-output"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "p1", "target": "spawn-1"},
    {"source": "spawn-1", "target": "checkOutput", "output": "success"},
    {"source": "checkOutput", "target": "onSuccess", "output": "got"},
    {"source": "checkOutput", "target": "onNoChildOutput", "output": "missing"},
    {"source": "spawn-1", "target": "onFailure", "output": "failure"}
  ]
}`)

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: " + strconv.Itoa(perUserCap) + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

func waitForState(t *testing.T, ds *domainStack, id uuid.UUID, want instance.State) {
	t.Helper()
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(context.Background(), id)
		return err == nil && inst.State == want
	}, 40*time.Second, 150*time.Millisecond,
		"instance %s never reached state %s", id, want)
}

func branchVariable(t *testing.T, ds *domainStack, id uuid.UUID) any {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return inst.Variables["branch"]
}

// HAPPY PATH — the whole wait-for-child cycle on real wiring: the parent
// suspends with a linked child, the child completes, the wake re-queues the
// parent, the parent re-enters, short-circuits, and follows the SUCCESS edge.
//
// This test also proves the ordering invariant: with a per-user cap of 1 the
// child cannot start until the parent's waiting transition frees the slot.
func TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess(t *testing.T) {
	t.Parallel()
	ds := bootWaitStack(t, "complete")

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	// The parent suspends and a linked child appears.
	waitForState(t, ds, parentID, instance.StateWaiting)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)
	childID := children[0].ID

	full, err := ds.instanceRepo.Get(context.Background(), childID)
	require.NoError(t, err)
	assert.Equal(t, instance.SpawnModeWaitForChild, full.SpawnMode)
	require.NotNil(t, full.ParentNodeID)
	assert.Equal(t, "spawn-1", *full.ParentNodeID)

	// The child drains despite the per-user cap of 1, then the parent completes.
	waitForState(t, ds, childID, instance.StateCompleted)
	waitForState(t, ds, parentID, instance.StateCompleted)

	assert.Equal(t, "success", branchVariable(t, ds, parentID),
		"the parent must resume through the SUCCESS branch and read the child's output "+
			"({{spawn-1.childOutput.done}}) set by the child's own transform")
}

// A FAILED child routes the parent through the failure branch.
func TestSpawnWaitE2E_FailedChildRoutesParentThroughFailure(t *testing.T) {
	t.Parallel()
	ds := bootWaitStack(t, "fail")

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	waitForState(t, ds, parentID, instance.StateWaiting)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)

	waitForState(t, ds, children[0].ID, instance.StateFailed)
	waitForState(t, ds, parentID, instance.StateCompleted)

	assert.Equal(t, "failure", branchVariable(t, ds, parentID))
}

// A CANCELLED child routes through the SAME failure branch — the spec's
// explicit no-separate-cancellation-path requirement, asserted at the sink.
func TestSpawnWaitE2E_CancelledChildRoutesParentThroughFailure(t *testing.T) {
	t.Parallel()
	ds := bootWaitStack(t, "complete")

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	waitForState(t, ds, parentID, instance.StateWaiting)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)
	childID := children[0].ID

	// Cancel the child directly (as an operator would). Admin identity is
	// required for the intervention authorization; local mode's default user is
	// an admin.
	ctx := adminCtxForLocalUser(t, ds)
	// The child may already have completed on a fast machine; treat that as a
	// skip rather than a failure of this assertion's premise.
	if cerr := ds.eng.Cancel(ctx, childID); cerr != nil {
		t.Skipf("child reached a terminal state before it could be cancelled: %v", cerr)
	}

	waitForState(t, ds, parentID, instance.StateCompleted)
	assert.Equal(t, "failure", branchVariable(t, ds, parentID),
		"a cancelled child routes through failure — there is no separate cancellation path")
}

// COMPOSITION-ROOT WIRING PROBE for eng.SetSpawnLinks(instanceRepo)
// (cmd/arbitron/main.go). cascadeCancel is a documented no-op when
// e.spawnLinks is nil (domain/engine/cancel_cascade.go), and every existing
// cascade test drives that behavior through a fake SpawnLinkRepository — none
// of them prove main.go actually wires the real one. Cancelling the PARENT
// while it is suspended (StateWaiting, a cancellable state) and its child is
// still active proves the cascade reaches the real, still-running child
// through the composition root, not just a fake in a unit test.
func TestSpawnWaitE2E_CancellingParentCascadesToActiveChildThroughCompositionRoot(t *testing.T) {
	t.Parallel()
	ds := bootWaitStack(t, "complete")

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	waitForState(t, ds, parentID, instance.StateWaiting)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)
	childID := children[0].ID

	child, err := ds.instanceRepo.Get(context.Background(), childID)
	require.NoError(t, err)
	if child.State != instance.StateRunning {
		t.Skipf("child reached a terminal state before the parent could be cancelled: %v", child.State)
	}

	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.eng.Cancel(ctx, parentID))

	waitForState(t, ds, parentID, instance.StateCancelled)
	waitForState(t, ds, childID, instance.StateCancelled)
}

// The parent's node-execution record is written on the WAIT path, where
// executeWithRetry deliberately writes none.
func TestSpawnWaitE2E_NodeExecutionRecordWrittenOnTheWaitPath(t *testing.T) {
	t.Parallel()
	ds := bootWaitStack(t, "complete")

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, parentID, instance.StateWaiting)

	exec, err := ds.nodeExecRepo.GetByInstanceAndNode(context.Background(), parentID, "spawn-1")
	require.NoError(t, err)
	require.NotNil(t, exec, "the awaited spawn node must leave a trace entry while suspended")
	assert.Equal(t, instance.NodeStatusSucceeded, exec.Status)
	assert.NotEmpty(t, exec.Output["childInstanceId"])
}

// PROVENANCE PROBE for the composition root's startup ORDER, not just its
// presence. main.go runs spawnReconciler.Reconcile immediately after
// pool-aware recovery (RecoverRunningInstances) specifically so a parent left waiting on a child
// that was running mid-flight is resumed in the SAME startup pass, rather than being
// stranded until some other wake arrives (there is none, since a crashed
// child never reaches a terminal state through any OnChildTerminal call
// site). Every existing reconciler test drives this ordering claim through
// fakes (domain/engine/spawn_reconciler_test.go); this one boots the REAL
// composition root twice against the SAME database file to prove main.go
// itself gets the order right, not just the reconciler's own logic.
func TestSpawnWaitE2E_RestartReconcilesParentAfterCrash(t *testing.T) {
	gates := newGroupProcessGates(t)
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	writeWorkflow(t, workspaceDir, "wait-child", `{
  "name": "wait-child",
  "description": "child",
  "inputs": [],
  "nodes": [
    {"id": "hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", `+strconv.Quote(groupGateScript)+`, "sh", `+strconv.Quote(gates.dir)+`, "hold"]}}
  ],
  "edges": []
}`)
	writeWorkflow(t, workspaceDir, "wait-parent", `{
  "name": "wait-parent",
  "description": "parent",
  "inputs": [],
  "nodes": [
    {"id": "p1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "spawn-1", "type": "spawn_workflow",
     "config": {"childWorkflow": "wait-child", "spawnMode": "wait_for_child"}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "p1", "target": "spawn-1"},
    {"source": "spawn-1", "target": "onSuccess", "output": "success"},
    {"source": "spawn-1", "target": "onFailure", "output": "failure"}
  ]
}`)

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	// --- First boot: crash while the child is still mid-flight. ---
	ds1, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)

	parentID, _, err := ds1.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds1, parentID, instance.StateWaiting)

	children := childrenOf(t, ds1, parentID)
	require.Len(t, children, 1)
	childID := children[0].ID
	child, err := ds1.instanceRepo.Get(context.Background(), childID)
	require.NoError(t, err)
	if child.State != instance.StateRunning {
		t.Skipf("child reached a terminal state before the simulated crash: %v", child.State)
	}

	// StateRunning alone precedes dispatch; the native launch is the crash boundary.
	gates.reached(t, "hold")
	require.NoError(t, os.Remove(filepath.Join(gates.dir, "hold.reached")))

	// Simulate a hard crash: sever the database out from under the live
	// process with no graceful drain, so the child's row is stranded at
	// StateRunning exactly as a real process kill would leave it. (A real
	// ds1.Close() would instead wait for the sleeping child to finish and
	// shut down cleanly, which is not the scenario this test targets.)
	require.NoError(t, ds1.db.Close())
	// The abandoned process's walkers still finish against the closed database
	// and log through the process-wide logger; drain them inside this test so
	// their errors never land in a later test's log.
	t.Cleanup(func() { gates.release(t, "hold"); ds1.queueMgr.Stop(); ds1.eng.Shutdown() })

	// --- Second boot: the real restart path. ---
	ds2, err := initRecoveryTestDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { gates.release(t, "hold"); ds2.Close(context.Background()) })
	gates.reached(t, "hold")
	gates.release(t, "hold")

	// Pool-aware recovery re-enters the running child's step on its checkpoint
	// (it repeats, carrying the recovery notice); the child then completes and
	// wakes the waiting parent through the success port.
	waitForState(t, ds2, childID, instance.StateCompleted)
	waitForState(t, ds2, parentID, instance.StateCompleted)
	assert.Equal(t, "success", branchVariable(t, ds2, parentID),
		"the restart's recover-then-reconcile ordering must resume the waiting parent")
	assert.Positive(t, p3NoticeCount(t, ds2, childID, "hold"), "the repeated child step carries the recovery notice")
}

// The wait cycle at the SHIPPED default per-user cap, with a child that has no
// artificial delay — the configuration every cap-1 test above structurally
// cannot exercise.
//
// A cap of 1 SERIALIZES parent and child: the child cannot start until the
// parent's `waiting` transition frees the slot, so the child's terminal write is
// always strictly later and OnChildTerminal always finds a waiting parent. Those
// tests prove the deadlock guard and nothing about the wake race. At a cap of 5
// the pair runs CONCURRENTLY and the ordering is genuinely undetermined: if the
// child reaches terminal first, the wake finds a still-`running` parent and is
// lost.
//
// HONEST SCOPE. This is a real-wiring regression, not a deterministic probe: on
// an idle machine the parent's two remaining writes reliably beat the child to
// the finish, so it passes with or without the fix. The deterministic proof —
// the interleaving forced, failing without the repair — is
// TestSpawnWait_ChildTerminalDuringSuspend_RoutesInsteadOfHanging in
// domain/engine. What this test adds is that the recovery path, when it does
// engage, engages through the REAL composition root: it would hang here (and
// under a loaded machine's inverted write ordering) if the executor's
// post-suspend re-read were absent or unwired.
func TestSpawnWaitE2E_ChildFinishingBeforeTheParentSuspends_StillWakesIt(t *testing.T) {
	t.Parallel()
	ds := bootWaitStackWith(t, "complete", 5, "")

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	// No intermediate assertion on the parent reaching `waiting`: whether it is
	// ever observably waiting is exactly the race. The contract is only that the
	// parent RESUMES and routes.
	waitForState(t, ds, parentID, instance.StateCompleted)

	assert.Equal(t, "success", branchVariable(t, ds, parentID),
		"a child that finished before its parent suspended must still route the parent through SUCCESS")

	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1, "exactly one child — the recovery path must not re-spawn")
}

// The same configuration on the FAILURE port. Same scope caveat as above.
func TestSpawnWaitE2E_ChildFailingBeforeTheParentSuspends_StillRoutesFailure(t *testing.T) {
	ds := bootWaitStackWith(t, "fail", 5, "")

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	waitForState(t, ds, parentID, instance.StateCompleted)
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))
	require.Len(t, childrenOf(t, ds, parentID), 1)
}
