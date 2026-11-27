package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are integration regression tests for spawn_workflow's explicit-inputs
// path on the REAL composition root. They drive the real spawner, trigger path
// and instance repository; the executor-level behaviour they rely on is also
// covered in domain/engine.
//
// A chat profile backed by a counting fake upstream is configured, so the input
// preparer could run. The call count is asserted literally: 0 for fully mapped
// spawns, 1 for a spawn with one unmapped required input (which also proves the
// counter is wired).

// inputsStack is the booted root plus the counting chat upstream behind the
// "proof" profile (the spawn node's model_profile). Every chat call the input
// preparer makes lands in calls.
type inputsStack struct {
	*domainStack
	calls *atomic.Int32
}

// bootSpawnInputsStack boots the real composition root over a parent whose
// awaited spawn node maps the given inputs JSON to a child declaring a required
// string `branch`, a required number `n` and an optional array `items`. A chat
// profile backed by a counting fake upstream is configured so the preparer
// COULD run; preparedJSON is what the upstream answers with.
func bootSpawnInputsStack(t *testing.T, parentInputs, spawnInputs, preparedJSON string) *inputsStack {
	t.Helper()
	calls := &atomic.Int32{}
	const model = "deepseek/deepseek-v4-flash"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":%q}}]}`, model, preparedJSON)
	}))
	t.Cleanup(upstream.Close)

	ds := bootSinkRoot(t, "", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "inputs-child", `{
  "name": "inputs-child",
  "description": "child that echoes its inputs",
  "inputs": [
    {"name": "branch", "type": "string", "description": "b", "required": true},
    {"name": "n", "type": "number", "description": "n", "required": true},
    {"name": "items", "type": "array", "description": "i", "required": false}
  ],
  "nodes": [
    {"id": "echo", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "echoed", "operation": "set", "value": "{{branch}}"}]}}
  ],
  "edges": []
}`)

		writeWorkflow(t, workspaceDir, "inputs-parent", `{
  "name": "inputs-parent",
  "description": "parent",
  "inputs": `+parentInputs+`,
  "nodes": [
    {"id": "spawn", "type": "spawn_workflow", "entrypoint": true,
     "config": {"childWorkflow": "inputs-child", "spawnMode": "wait_for_child", "model_profile": "proof", "inputs": `+spawnInputs+`}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [
       {"variable": "branchOut", "operation": "set", "value": "success"},
       {"variable": "childBranch", "operation": "set", "value": "{{spawn.childOutput.branch}}"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branchOut", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "spawn", "target": "onSuccess", "output": "success"},
    {"source": "spawn", "target": "onFailure", "output": "failure"}
  ]
}`)
	})

	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "inputs-api", Provider: "openrouter", BaseURL: upstream.URL}, "inputs-secret"))
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "proof", Kind: provider.ProfileKindChat, ConnectionRef: "inputs-api", Model: model})
	require.NoError(t, err)
	return &inputsStack{domainStack: ds, calls: calls}
}

const spawnInputsParentDecl = `[
    {"name": "branch", "type": "string", "description": "b", "required": true},
    {"name": "items", "type": "array", "description": "i", "required": false}
  ]`

// HAPPY PATH + INTEGRATION: a fully mapped awaited spawn hands the child typed
// values (3 stays a number), routes the parent through success, exposes the
// child's variables as childOutput, and never needs the input preparer.
func TestSpawnInputsE2E_FullyMappedAwaitedSpawn_NoChatCalls(t *testing.T) {
	ds := bootSpawnInputsStack(t, spawnInputsParentDecl, `{"branch": "{{branch}}", "n": 3}`, `{}`)

	parentID, _, err := ds.eng.Trigger(context.Background(), "inputs-parent",
		map[string]any{"branch": "feat/x"}, "normal")
	require.NoError(t, err)
	waitForState(t, ds.domainStack, parentID, instance.StateCompleted)

	parent, err := ds.instanceRepo.Get(context.Background(), parentID)
	require.NoError(t, err)
	assert.Equal(t, "success", parent.Variables["branchOut"])
	assert.Equal(t, "feat/x", parent.Variables["childBranch"],
		"{{spawn.childOutput.branch}} must carry the mapped value back")

	children := childrenOf(t, ds.domainStack, parentID)
	require.Len(t, children, 1)
	waitForState(t, ds.domainStack, children[0].ID, instance.StateCompleted)
	child, err := ds.instanceRepo.Get(context.Background(), children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "feat/x", child.Variables["branch"])
	assert.Equal(t, 3.0, child.Variables["n"], "a literal number must stay a number")
	assert.Equal(t, "feat/x", child.Variables["echoed"])

	assert.Equal(t, int32(0), ds.calls.Load(), "a fully mapped spawn must make zero chat calls")
	// No input may be sourced "prepared".
	exec := firstSpawnExecution(t, ds.domainStack, parentID)
	assert.Equal(t, map[string]any{"branch": "mapped", "n": "mapped"}, exec.Output["inputSources"])
}

// BOUNDARY: a 10,000-element mapped array reaches the child intact through the
// real spawner and trigger path.
func TestSpawnInputsE2E_LargeMappedArray_ReachesChildIntact(t *testing.T) {
	ds := bootSpawnInputsStack(t, spawnInputsParentDecl,
		`{"branch": "{{branch}}", "n": 1, "items": "{{items}}"}`, `{}`)

	const size = 10000
	items := make([]any, size)
	for i := range items {
		items[i] = float64(i)
	}
	parentID, _, err := ds.eng.Trigger(context.Background(), "inputs-parent",
		map[string]any{"branch": "feat/x", "items": items}, "normal")
	require.NoError(t, err)
	waitForState(t, ds.domainStack, parentID, instance.StateCompleted)

	children := childrenOf(t, ds.domainStack, parentID)
	require.Len(t, children, 1)
	waitForState(t, ds.domainStack, children[0].ID, instance.StateCompleted)
	child, err := ds.instanceRepo.Get(context.Background(), children[0].ID)
	require.NoError(t, err)

	got, ok := child.Variables["items"].([]any)
	require.True(t, ok, "items must arrive as an array, got %T", child.Variables["items"])
	require.Len(t, got, size)
	assert.Equal(t, 0.0, got[0])
	assert.Equal(t, 5000.0, got[5000])
	assert.Equal(t, float64(size-1), got[size-1])
	assert.Equal(t, items, got, "every element must arrive intact and in order")
	assert.Equal(t, int32(0), ds.calls.Load())
}

// ERROR: a mapping that resolves to nothing for a required child input fails
// closed. The parent fails and no child is ever created.
func TestSpawnInputsE2E_UnresolvedMapping_NoChildCreated(t *testing.T) {
	ds := bootSpawnInputsStack(t, spawnInputsParentDecl, `{"branch": "{{missing}}", "n": 3}`, `{}`)

	parentID, _, err := ds.eng.Trigger(context.Background(), "inputs-parent",
		map[string]any{"branch": "feat/x"}, "normal")
	require.NoError(t, err)
	waitForState(t, ds.domainStack, parentID, instance.StateFailed)

	assert.Empty(t, childrenOf(t, ds.domainStack, parentID), "no child may be linked to a parent whose mapping failed")

	exec, err := ds.nodeExecRepo.GetByInstanceAndNode(context.Background(), parentID, "spawn")
	require.NoError(t, err)
	require.NotNil(t, exec)
	assert.Equal(t, instance.NodeStatusFailed, exec.Status)
	assert.Contains(t, fmt.Sprint(exec.Error), "resolve inputs", "the failure must name the input-resolution step")
	assert.Contains(t, fmt.Sprint(exec.Error), "missing", "the failure must name the unresolved reference")
	assert.Equal(t, int32(0), ds.calls.Load(), "an unresolved mapping must never fall back to the LLM")
}

// INTEGRATION: one required child input is left unmapped and has no default, so
// the preparer is asked for exactly that input and makes exactly one chat call.
// This proves the counter in the tests above is wired to the real call path.
func TestSpawnInputsE2E_PartiallyMappedSpawn_PreparesOnlyUnmappedInput(t *testing.T) {
	ds := bootSpawnInputsStack(t, spawnInputsParentDecl, `{"branch": "{{branch}}"}`, `{"n": 7}`)

	parentID, _, err := ds.eng.Trigger(context.Background(), "inputs-parent",
		map[string]any{"branch": "feat/x"}, "normal")
	require.NoError(t, err)
	waitForState(t, ds.domainStack, parentID, instance.StateCompleted)

	assert.Equal(t, int32(1), ds.calls.Load(), "exactly one chat call for the one unmapped input")

	children := childrenOf(t, ds.domainStack, parentID)
	require.Len(t, children, 1)
	waitForState(t, ds.domainStack, children[0].ID, instance.StateCompleted)
	child, err := ds.instanceRepo.Get(context.Background(), children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "feat/x", child.Variables["branch"])
	assert.Equal(t, 7.0, child.Variables["n"])

	exec := firstSpawnExecution(t, ds.domainStack, parentID)
	assert.Equal(t, map[string]any{"branch": "mapped", "n": "prepared"}, exec.Output["inputSources"])
}

// firstSpawnExecution returns the awaited spawn node's FIRST execution record:
// inputSources is trace-only there; the resume record is the latest visit.
func firstSpawnExecution(t *testing.T, ds *domainStack, parentID uuid.UUID) *instance.NodeExecution {
	t.Helper()
	execs, err := ds.nodeExecRepo.ListByInstance(context.Background(), parentID)
	require.NoError(t, err)
	for i := range execs {
		if execs[i].NodeID == "spawn" {
			return &execs[i]
		}
	}
	t.Fatalf("no spawn execution for %s", parentID)
	return nil
}
