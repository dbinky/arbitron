# Phase 3 — Task 5: end-to-end spawn provenance through the composition root

**Goal:** Prove the feature is not inert: boot the real composition root, trigger a real parent workflow whose `spawn_workflow` node runs, and assert at the SINK — the persisted child instance row — that a linked child exists under the parent's owner.

**Architecture:** The test calls `initDomain` (the same function `serve` and `mcp` call) against a temp config, a temp SQLite file, and a temp workspace holding two real workflow folders. The child workflow declares **zero inputs**, which makes `LLMChildInputPreparer.Prepare` short-circuit before any provider call — so the whole spawn path exercises real wiring with no LLM and no network. This test fails if the executor is not registered at the composition root ("no executor registered for node type: spawn_workflow"), if the identity resolver is not wired, or if the link is not stamped.

**Tech Stack:** Go 1.25.5, `modernc.org/sqlite`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 5: composition-root provenance probe

**Files:**
- Test:   `cmd/arbitron/spawn_e2e_test.go`

---

- [x] **Step 1: Write the failing test**

Create `cmd/arbitron/spawn_e2e_test.go`:

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

// localDefaultUser is the admin seeded by migration 001; in local auth mode it
// owns every run and every private workflow.
var localDefaultUser = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// writeWorkflow writes a workflow.json into the default user's private scope.
func writeWorkflow(t *testing.T, workspaceDir, name, manifest string) {
	t.Helper()
	dir := filepath.Join(workspaceDir, "users", localDefaultUser.String(), name)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(manifest), 0o600))
}

// bootSpawnStack writes a parent + child workflow pair and boots the REAL
// composition root against them.
func bootSpawnStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	// The child declares ZERO inputs, so the preparer short-circuits and no
	// provider call is ever made — the spawn path runs on real wiring with no LLM.
	writeWorkflow(t, workspaceDir, "spawn-child", `{
  "name": "spawn-child",
  "description": "child",
  "inputs": [],
  "nodes": [
    {"id": "c1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}
  ],
  "edges": []
}`)

	writeWorkflow(t, workspaceDir, "spawn-parent", `{
  "name": "spawn-parent",
  "description": "parent",
  "inputs": [],
  "nodes": [
    {"id": "p1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "spawn-1", "type": "spawn_workflow",
     "config": {"childWorkflow": "spawn-child", "spawnMode": "fire_and_forget"}},
    {"id": "p2", "type": "transform",
     "config": {"operations": [{"variable": "finished", "operation": "set", "value": true}]}}
  ],
  "edges": [
    {"source": "p1", "target": "spawn-1"},
    {"source": "spawn-1", "target": "p2"}
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
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err, "the composition root must boot with a spawn workflow present")
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// childrenOf returns the persisted children of parentID, read straight from the
// repository — the SINK, not an intermediate hop.
func childrenOf(t *testing.T, ds *domainStack, parentID uuid.UUID) []instance.InstanceSummary {
	t.Helper()
	got, err := ds.instanceRepo.List(context.Background(), instance.ListFilters{
		ParentScope:      instance.ParentScopeChildren,
		ParentInstanceID: &parentID,
		Limit:            50,
	})
	require.NoError(t, err)
	return got
}

// PROVENANCE PROBE. This is the test that fails when the feature is inert: if
// the spawn executor is not registered at the composition root, the engine
// reports "no executor registered for node type: spawn_workflow", the parent
// fails, and NO child row is ever written.
func TestSpawnE2E_FireAndForgetCreatesLinkedChildThroughCompositionRoot(t *testing.T) {
	ds := bootSpawnStack(t)

	parentID, _, err := ds.eng.Trigger(context.Background(), "spawn-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	var children []instance.InstanceSummary
	require.Eventually(t, func() bool {
		children = childrenOf(t, ds, parentID)
		return len(children) == 1
	}, 20*time.Second, 100*time.Millisecond,
		"exactly one linked child must be created by the spawn node running through the real engine")

	child := children[0]
	assert.Equal(t, "spawn-child", child.WorkflowName)
	require.NotNil(t, child.ParentInstanceID)
	assert.Equal(t, parentID, *child.ParentInstanceID)

	full, err := ds.instanceRepo.Get(context.Background(), child.ID)
	require.NoError(t, err)
	require.NotNil(t, full.ParentNodeID)
	assert.Equal(t, "spawn-1", *full.ParentNodeID,
		"the link must name the SPECIFIC spawn node, not just the parent run")
	assert.Equal(t, instance.SpawnModeFireAndForget, full.SpawnMode)

	parent, err := ds.instanceRepo.Get(context.Background(), parentID)
	require.NoError(t, err)
	assert.Equal(t, parent.OwnerID, full.OwnerID, "the child inherits the parent instance's owner")
}

// The parent CONTINUES past a fire-and-forget spawn and reaches its terminal
// state — the node returns a non-wait result and the engine follows the single
// unnamed edge.
func TestSpawnE2E_ParentContinuesAndCompletes(t *testing.T) {
	ds := bootSpawnStack(t)

	parentID, _, err := ds.eng.Trigger(context.Background(), "spawn-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(context.Background(), parentID)
		return gerr == nil && inst.State == instance.StateCompleted
	}, 20*time.Second, 100*time.Millisecond,
		"the parent must advance past the spawn node and complete")

	// The node output records the child id so downstream nodes and the trace can
	// reference the spawned run.
	exec, err := ds.nodeExecRepo.GetByInstanceAndNode(context.Background(), parentID, "spawn-1")
	require.NoError(t, err)
	require.NotNil(t, exec)
	assert.Equal(t, instance.NodeStatusSucceeded, exec.Status)
	assert.NotEmpty(t, exec.Output["childInstanceId"])
	assert.Equal(t, "spawn-child", exec.Output["childWorkflow"])
}

// A run with no spawn node is a ROOT and has no children — proving the parent
// filter is not vacuously matching everything.
func TestSpawnE2E_ChildlessRunIsARootWithNoChildren(t *testing.T) {
	ds := bootSpawnStack(t)

	childID, _, err := ds.eng.Trigger(context.Background(), "spawn-child", map[string]any{}, "normal")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(context.Background(), childID)
		return gerr == nil && inst.State == instance.StateCompleted
	}, 20*time.Second, 100*time.Millisecond)

	inst, err := ds.instanceRepo.Get(context.Background(), childID)
	require.NoError(t, err)
	assert.True(t, inst.IsRoot())
	assert.Empty(t, childrenOf(t, ds, childID))
}
```

- [x] **Step 2: Run it, confirm it fails when the wiring is absent**

First prove the probe would catch an inert feature. Temporarily comment out the
registration line added in Phase 3 Task 4:

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
sed -i.bak 's|^\texecutors\[workflow.NodeTypeSpawnWorkflow\] =|\t// PROBE-CHECK executors[workflow.NodeTypeSpawnWorkflow] =|' cmd/arbitron/main.go
go test ./cmd/arbitron/ -run TestSpawnE2E 2>&1 | head -20
```

Expected output — the build fails on the now-orphaned constructor call, or (if
the whole block is commented) the test fails:

```
--- FAIL: TestSpawnE2E_FireAndForgetCreatesLinkedChildThroughCompositionRoot
    spawn_e2e_test.go:...: exactly one linked child must be created by the spawn node running through the real engine
FAIL	github.com/arbitron/arbitron/cmd/arbitron
```

Restore the file:

```bash
mv cmd/arbitron/main.go.bak cmd/arbitron/main.go
```

- [x] **Step 3: Run it against the real wiring, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./cmd/arbitron/ -run TestSpawnE2E -v
```

Expected output:

```
--- PASS: TestSpawnE2E_FireAndForgetCreatesLinkedChildThroughCompositionRoot (1.2s)
--- PASS: TestSpawnE2E_ParentContinuesAndCompletes (1.1s)
--- PASS: TestSpawnE2E_ChildlessRunIsARootWithNoChildren (0.9s)
PASS
ok  	github.com/arbitron/arbitron/cmd/arbitron	3.6s
```

- [x] **Step 4: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [x] **Step 5: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add cmd/arbitron/spawn_e2e_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "test(cmd): end-to-end spawn provenance probe through the composition root"
```
