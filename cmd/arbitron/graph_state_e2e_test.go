package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Two branch tasks that overlap for ~3s (both observably running at once) and
// finish 3s apart, so a terminal branch is always observed while the other is
// still active (P4-T5 relies on that ordering).
const gsFanoutManifest = `{
  "name": "gs-fanout",
  "description": "two concurrent branches",
  "inputs": [],
  "nodes": [
    {"id": "start", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "g", "type": "parallel_group",
     "config": {"group": {"form": "branches",
       "tasks": [{"key": "a", "entry": "a1"}, {"key": "b", "entry": "b1"}],
       "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining", "gatherMode": "results"}}},
    {"id": "a1", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "sleep 3; echo '{\"branch\":\"alpha\"}'"]}},
    {"id": "b1", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "sleep 6; echo '{\"branch\":\"beta\"}'"]}},
    {"id": "done", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "success"}]}},
    {"id": "fail", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "start", "target": "g"},
    {"source": "g", "target": "a1", "output": "task", "label": "a"},
    {"source": "g", "target": "b1", "output": "task", "label": "b"},
    {"source": "g", "target": "done", "output": "success"},
    {"source": "g", "target": "fail", "output": "failure"}
  ]
}`

// A parent that completes immediately while its fire-and-forget child holds.
const gsAncestorParentManifest = `{
  "name": "gs-ada-parent",
  "description": "parent finishes before its child",
  "inputs": [],
  "nodes": [
    {"id": "p1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "spawn-1", "type": "spawn_workflow",
     "config": {"childWorkflow": "gs-ada-child", "spawnMode": "fire_and_forget"}},
    {"id": "p2", "type": "transform",
     "config": {"operations": [{"variable": "finished", "operation": "set", "value": true}]}}
  ],
  "edges": [
    {"source": "p1", "target": "spawn-1"},
    {"source": "spawn-1", "target": "p2"}
  ]
}`

const gsAncestorChildManifest = `{
  "name": "gs-ada-child",
  "description": "child that outlives its parent",
  "inputs": [],
  "nodes": [
    {"id": "hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "sleep 4; echo '{}'"]}},
    {"id": "c1", "type": "transform",
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}
  ],
  "edges": [{"source": "hold", "target": "c1"}]
}`

// bootGraphStateStack boots the REAL composition root with the Phase 4 fixtures.
func bootGraphStateStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	writeWorkflow(t, workspaceDir, "gs-fanout", gsFanoutManifest)
	writeWorkflow(t, workspaceDir, "gs-branch-body", gsBranchBodyManifest)
	writeWorkflow(t, workspaceDir, "gs-ada-parent", gsAncestorParentManifest)
	writeWorkflow(t, workspaceDir, "gs-ada-child", gsAncestorChildManifest)

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

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// gsServeAPI serves EXACTLY the router runServe builds from the domain stack
// (minus the per-process dashboard/MCP/notification/webhook handlers).
func gsServeAPI(t *testing.T, ds *domainStack) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.routerDeps()))
	t.Cleanup(srv.Close)
	return srv
}

// gsFetchGraphState never calls require, so it is safe inside Eventually.
func gsFetchGraphState(srv *httptest.Server, id uuid.UUID) (int, *engine.GraphState, error) {
	resp, err := http.Get(srv.URL + "/api/v1/instances/" + id.String() + "/graph-state")
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, nil
	}
	var gs engine.GraphState
	if err := json.NewDecoder(resp.Body).Decode(&gs); err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, &gs, nil
}

func gsListIDs(srv *httptest.Server, query string) (map[uuid.UUID]bool, error) {
	resp, err := http.Get(srv.URL + "/api/v1/instances" + query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list status %d", resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]bool{}
	for _, r := range rows {
		if s, ok := r["id"].(string); ok {
			out[uuid.MustParse(s)] = true
		}
	}
	return out, nil
}

// PROVENANCE PROBE for the graph-state wiring: if initDomain does not build the
// reader, routerDeps does not pass it, or the route is not registered, this GET
// is a 404 and the test fails. The sink is the HTTP JSON body.
func TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "gs-fanout", map[string]any{}, "normal")
	require.NoError(t, err)

	code, initial, err := gsFetchGraphState(srv, parentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, "the production graph-state route must be wired")
	require.NotNil(t, initial)

	var gs *engine.GraphState
	require.Eventually(t, func() bool {
		_, got, ferr := gsFetchGraphState(srv, parentID)
		if ferr != nil || got == nil {
			return false
		}
		running := 0
		for _, c := range got.Contexts {
			if c.Kind == engine.ContextKindBranch && c.State == string(instance.StateRunning) {
				running++
			}
		}
		if running >= 2 && len(got.Groups) == 1 && len(got.Groups[0].WaitingReasons) == 1 &&
			got.Groups[0].WaitingReasons[0] == "waiting_for_tasks:a,b" {
			gs = got
			return true
		}
		return false
	}, 30*time.Second, 100*time.Millisecond,
		"GET graph-state must show both branches running concurrently")

	assert.Equal(t, parentID, gs.RootInstanceID)
	assert.True(t, gs.Active)
	require.NotNil(t, gs.Layout)
	assert.Contains(t, gs.Layout.Groups, "g")
	assert.Equal(t, parentID, gs.Contexts[0].InstanceID)

	byKey := map[string]uuid.UUID{}
	for _, c := range gs.Contexts[1:] {
		require.NotNil(t, c.ParentInstanceID)
		assert.Equal(t, parentID, *c.ParentInstanceID)
		byKey[c.TaskKey] = c.InstanceID
	}
	require.Contains(t, byKey, "a")
	require.Contains(t, byKey, "b")
	assert.NotEqual(t, byKey["a"], byKey["b"])

	nodes := map[engine.NodeStateKey]string{}
	for _, c := range gs.Contexts {
		for _, n := range c.Nodes {
			nodes[n.Key()] = n.State
		}
	}
	assert.Equal(t, engine.LiveStateRunning, nodes[engine.NodeStateKey{InstanceID: byKey["a"], NodeID: "a1"}])
	assert.Equal(t, engine.LiveStateRunning, nodes[engine.NodeStateKey{InstanceID: byKey["b"], NodeID: "b1"}])
	assert.Equal(t, engine.LiveStateWaiting, nodes[engine.NodeStateKey{InstanceID: parentID, NodeID: "g"}])

	// Completed-run inspection: the frozen gather survives completion.
	waitForState(t, ds, parentID, instance.StateCompleted)
	code, final, err := gsFetchGraphState(srv, parentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	assert.False(t, final.Active)
	require.Len(t, final.Groups, 1)
	assert.Equal(t, string(instance.GroupRunDone), final.Groups[0].State)
	assert.Empty(t, final.Groups[0].WaitingReasons)
	for _, card := range final.Groups[0].Tasks {
		assert.Equal(t, "completed", card.LiveStatus)
		assert.Equal(t, "completed", card.FrozenStatus)
	}
	require.NotNil(t, final.Groups[0].Decision)
	require.Len(t, final.Groups[0].Decision.Tasks, 2)
	out := final.Groups[0].FinalOutput
	require.NotEmpty(t, out)
	assert.Equal(t, "success", out["outcome"])
	for i, tc := range []struct{ key, node, value string }{
		{"a", "a1", "alpha"}, {"b", "b1", "beta"},
	} {
		task := groupTaskByKey(t, out, tc.key)
		assert.Equal(t, byKey[tc.key].String(), task["childInstanceId"])
		result := resultOf(t, task)
		nodeOutput, ok := result[tc.node].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, tc.value, nodeOutput["branch"], "actual tool output survives to HTTP inspection")
		frozen := final.Groups[0].Decision.Tasks[i]
		assert.Equal(t, tc.key, frozen.Key)
		assert.Equal(t, result, frozen.Result)
	}
}

func TestGraphStateE2E_UnknownInstanceIs404(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	code, _, err := gsFetchGraphState(srv, uuid.New())

	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestGroupLayoutE2E_ServesPhaseOneOwnershipThroughCompositionRoot(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	resp, err := http.Get(srv.URL + "/api/v1/workflows/gs-fanout/group-layout")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var layout workflow.GroupGraph
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&layout))

	require.Contains(t, layout.Groups, "g")
	assert.Equal(t, []string{"a1"}, layout.Groups["g"].Bodies["a"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g", TaskKey: "b"}, layout.Owner["b1"])
}

// The queue's ancestor discovery through the REAL list pipeline: HTTP param ->
// Engine.ListEnriched (service-set scope) -> sqlite recursive CTE.
func TestQueueAncestorsE2E_CompletedParentWithActiveChildIsDiscoverable(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "gs-ada-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, parentID, instance.StateCompleted)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)
	childID := children[0].ID

	ids, err := gsListIDs(srv, "?activeDescendantAncestors=true&includeDismissed=true")
	require.NoError(t, err)
	assert.True(t, ids[parentID], "a completed parent that still owns an active child is discoverable")
	assert.False(t, ids[childID], "the active child itself is listed by the ordinary state sections")

	waitForState(t, ds, childID, instance.StateCompleted)
	ids, err = gsListIDs(srv, "?activeDescendantAncestors=true&includeDismissed=true")
	require.NoError(t, err)
	assert.False(t, ids[parentID], "once the subtree is inactive the parent is no longer discovered")
}
