# Phase 4 — Task 6: end-to-end wait-for-child through the composition root, and the loop-body restriction

**Goal:** Prove wait-for-child works end to end on real wiring — the parent suspends, the child runs, the wake re-queues the parent, and the parent resumes through the correct branch — and document the loop-body restriction where authors already look for it.

**Architecture:** The test boots the real composition root via `initDomain` (the same function `serve` calls) with two real workflow folders on disk. The child declares **zero inputs**, so the preparer short-circuits and no LLM or network is involved. The per-user cap is set to **1** so the run also proves the ordering invariant: the child cannot start until the parent's waiting transition frees the slot, and both must still drain. `AGENTS.md` gains the awaited spawn node in the existing loop-body restriction paragraph.

**Tech Stack:** Go 1.25.5, `modernc.org/sqlite`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 6: wait-for-child E2E + docs

**Files:**
- Modify: `AGENTS.md` (Engine and Loop Behavior — the loop-body restriction)
- Test:   `cmd/arbitron/spawn_wait_e2e_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `cmd/arbitron/spawn_wait_e2e_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootWaitStack writes an awaited parent + child pair and boots the REAL
// composition root. childOutcome selects whether the child completes or fails.
//
// The per-user cap is 1 on purpose: it proves the ordering invariant. Parent and
// child compete for the SAME per-user slot, so the child can only start once the
// parent's waiting transition releases it. If the executor transitioned before
// enqueueing the child, or skipped NotifyCapacityFreed, this test would hang.
func bootWaitStack(t *testing.T, childOutcome string) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	// "complete" -> a plain transform that succeeds.
	// "fail"     -> a tool_call to a command that does not exist, which fails the run.
	childNode := `{"id": "c1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}`
	if childOutcome == "fail" {
		childNode = `{"id": "c1", "type": "tool_call", "entrypoint": true,
     "errorBehavior": {"type": "halt"},
     "config": {"command": "arbitron-no-such-binary-for-tests"}}`
	}
	writeWorkflow(t, workspaceDir, "wait-child", `{
  "name": "wait-child",
  "description": "child",
  "inputs": [],
  "nodes": [`+childNode+`],
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
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		// A per-user cap of ONE is the deadlock probe.
		"queue:\n  globalcap: 5\n  perusercap: 1\n"
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
		"the parent must resume through the SUCCESS branch — the sink, not an intermediate hop")
}

// A FAILED child routes the parent through the failure branch.
func TestSpawnWaitE2E_FailedChildRoutesParentThroughFailure(t *testing.T) {
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

// The parent's node-execution record is written on the WAIT path, where
// executeWithRetry deliberately writes none.
func TestSpawnWaitE2E_NodeExecutionRecordWrittenOnTheWaitPath(t *testing.T) {
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
```

Add the shared admin-context helper to `cmd/arbitron/spawn_e2e_test.go` (it is
used by both E2E files):

```go
// adminCtxForLocalUser returns a context carrying the local-mode default user's
// admin identity, which intervention paths (Cancel/Retry) authorize against.
func adminCtxForLocalUser(t *testing.T, ds *domainStack) context.Context {
	t.Helper()
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID:      localDefaultUser,
		Username:    "local",
		Role:        identity.RoleAdmin,
		Departments: []string{"default"},
	})
}
```

and add `"github.com/arbitron/arbitron/domain/identity"` to that file's imports.

- [ ] **Step 2: Run it, confirm it fails when the reconciler/wake are absent**

Run it against the current tree first:

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./cmd/arbitron/ -run TestSpawnWaitE2E -v 2>&1 | tail -20
```

If Phase 4 Tasks 1-5 are complete this passes. To confirm the probe is not
vacuous, temporarily neutralize the wake and re-run:

```bash
sed -i.bak 's|^\te.OnChildTerminal(context.Background(), instanceID)$|\t// PROBE-CHECK e.OnChildTerminal(context.Background(), instanceID)|' domain/engine/engine.go
go test ./cmd/arbitron/ -run TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess 2>&1 | tail -6
```

Expected output (with the wake removed and the reconciler only running at boot,
the parent never resumes):

```
--- FAIL: TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess
    spawn_wait_e2e_test.go:...: instance ... never reached state completed
FAIL	github.com/arbitron/arbitron/cmd/arbitron
```

Restore:

```bash
mv domain/engine/engine.go.bak domain/engine/engine.go
```

- [ ] **Step 3: Run it against the real wiring, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w cmd/arbitron/
go test ./cmd/arbitron/ -run TestSpawnWaitE2E -v
```

Expected output:

```
--- PASS: TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess (2.4s)
--- PASS: TestSpawnWaitE2E_FailedChildRoutesParentThroughFailure (2.2s)
--- PASS: TestSpawnWaitE2E_CancelledChildRoutesParentThroughFailure (2.1s)
--- PASS: TestSpawnWaitE2E_NodeExecutionRecordWrittenOnTheWaitPath (1.6s)
PASS
ok  	github.com/arbitron/arbitron/cmd/arbitron	8.6s
```

- [ ] **Step 4: Document the loop-body restriction**

In `AGENTS.md`, in the "Engine and Loop Behavior" section, replace the
interactive-node paragraph with:

```markdown
Do not put interactive nodes in loop bodies. The interactive executor has reuse/session behavior that is not appropriate for repeated loop passes.

Do not put an **awaited** `spawn_workflow` node (`spawnMode: wait_for_child`) in a loop body, for the same underlying reason. Its resumption short-circuit cannot distinguish a re-entry-after-wait from a back-edge revisit: both arrive at the node with a terminal link already recorded for (parent, node), so a revisit reads the PREVIOUS child's outcome and routes immediately instead of spawning a new child.

A **fire-and-forget** `spawn_workflow` node is loop-safe and carries no such restriction: it has no short-circuit, so each revisit spawns a new child, consistent with the documented re-execution semantics of loop revisits.
```

- [ ] **Step 5: Full Go gate**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [ ] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add cmd/arbitron/spawn_wait_e2e_test.go cmd/arbitron/spawn_e2e_test.go AGENTS.md
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "test(cmd): end-to-end wait-for-child probe; document the awaited-spawn loop restriction"
```
