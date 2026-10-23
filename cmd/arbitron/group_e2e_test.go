package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Manifests. Every timing knob is a real `sh` sleep inside a tool_call so the
// interleavings are produced by the real queue and run loop, not by fakes.
// ---------------------------------------------------------------------------

// groupBranchesManifest: a parent writes seed.txt into its working area, then
// a branches/all_succeed/results group runs two branches. Each branch writes
// branch.txt (its own label) into ITS working area, sleeps, then reports what
// it reads back plus the parent's seed. Branch a sleeps 2s, b 1s, so b
// finishes FIRST while a is authored first. Both writes happen before either
// read, so a shared working area would make one branch read the other's label.
// After the group, check-parent inspects the PARENT's working area.
const groupBranchesManifest = `{
  "name": "grp-branches",
  "description": "branches all_succeed results",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "printf parent-seed > \"$1/seed.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [
         {"key": "a", "entry": "a-run", "inputs": {"label": "alpha", "hold": "2"}},
         {"key": "b", "entry": "b-run", "inputs": {"label": "beta", "hold": "1"}}
       ],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '%s' \"$2\" > \"$1/branch.txt\"; sleep \"$3\"; printf '{\"seed\":\"%s\",\"own\":\"%s\"}' \"$(cat \"$1/seed.txt\")\" \"$(cat \"$1/branch.txt\")\"", "sh", "{{workspace}}", "{{label}}", "{{hold}}"]}},
    {"id": "a-set", "type": "transform",
     "config": {"operations": [{"variable": "branch_value", "operation": "set", "value": "{{label}}-done"}]}},
    {"id": "b-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '%s' \"$2\" > \"$1/branch.txt\"; sleep \"$3\"; printf '{\"seed\":\"%s\",\"own\":\"%s\"}' \"$(cat \"$1/seed.txt\")\" \"$(cat \"$1/branch.txt\")\"", "sh", "{{workspace}}", "{{label}}", "{{hold}}"]}},
    {"id": "b-set", "type": "transform",
     "config": {"operations": [{"variable": "branch_value", "operation": "set", "value": "{{label}}-done"}]}},
    {"id": "check-parent", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/branch.txt\" ]; then l=true; else l=false; fi; if [ \"$(cat \"$1/seed.txt\" 2>/dev/null)\" = parent-seed ]; then s=true; else s=false; fi; printf '{\"leaked\":%s,\"seedKept\":%s}' \"$l\" \"$s\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [
       {"variable": "branch", "operation": "set", "value": "success"},
       {"variable": "consumedFirst", "operation": "set", "value": "{{group-1.tasks.0.result.branch_value}}"},
       {"variable": "consumedSecond", "operation": "set", "value": "{{group-1.tasks.1.result.branch_value}}"},
       {"variable": "consumedByKey", "operation": "set", "value": "{{group-1.byKey.b.result.branch_value}}"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "group-1"},
    {"source": "group-1", "target": "a-run", "output": "task", "label": "a"},
    {"source": "a-run", "target": "a-set"},
    {"source": "group-1", "target": "b-run", "output": "task", "label": "b"},
    {"source": "b-run", "target": "b-set"},
    {"source": "group-1", "target": "check-parent", "output": "success"},
    {"source": "check-parent", "target": "onSuccess"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// groupEchoChildManifest: a child workflow with two declared string inputs.
// It sleeps `hold` seconds, echoes `label`, and sets child_value.
const groupEchoChildManifest = `{
  "name": "grp-echo-child",
  "description": "echo child",
  "inputs": [
    {"name": "label", "type": "string", "required": true},
    {"name": "hold", "type": "string", "required": true}
  ],
  "nodes": [
    {"id": "echo", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "sleep \"$2\"; printf '{\"label\":\"%s\"}' \"$1\"", "sh", "{{label}}", "{{hold}}"]}},
    {"id": "mark", "type": "transform",
     "config": {"operations": [{"variable": "child_value", "operation": "set", "value": "{{label}}-child"}]}}
  ],
  "edges": [
    {"source": "echo", "target": "mark"}
  ]
}`

// groupWorkflowsManifest: the SAME child workflow twice with different inputs.
// "first" holds 2s, "second" 0s, so completion order is the reverse of author
// order. prep sets a parent variable both children must inherit.
const groupWorkflowsManifest = `{
  "name": "grp-workflows",
  "description": "workflows all_succeed results",
  "inputs": [],
  "nodes": [
    {"id": "prep", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "shared_note", "operation": "set", "value": "from-parent"}]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "workflows",
       "tasks": [
         {"key": "first", "workflow": "grp-echo-child", "inputs": {"label": "one", "hold": "2"}},
         {"key": "second", "workflow": "grp-echo-child", "inputs": {"label": "two", "hold": "0"}}
       ],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "prep", "target": "group-1"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// groupEntryManifest: the group IS the entrypoint, with a single branch task.
const groupEntryManifest = `{
  "name": "grp-entry",
  "description": "entrypoint group with one task",
  "inputs": [],
  "nodes": [
    {"id": "group-1", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "only", "entry": "only-set"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "only-set", "type": "transform",
     "config": {"operations": [{"variable": "only_value", "operation": "set", "value": "x"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "group-1", "target": "only-set", "output": "task", "label": "only"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// groupCrashManifest: the slow task is held by a test-controlled process gate
// until after the database has been severed and the stack restarted.
const groupCrashManifest = `{
  "name": "grp-crash",
  "description": "crash mid-group",
  "inputs": [],
  "nodes": [
    {"id": "group-1", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "quick", "entry": "quick-set"}, {"key": "slow", "entry": "slow-hold"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "wait_for_remaining",
       "gatherMode": "results"}}},
    {"id": "quick-set", "type": "transform",
     "config": {"operations": [{"variable": "quick_value", "operation": "set", "value": "q"}]}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "touch \"$1/slow.reached\"; n=0; while [ ! -f \"$1/slow.release\" ]; do n=$((n+1)); [ \"$n\" -lt 1200 ] || exit 91; sleep .05; done; echo '{}'", "sh", "{{gateDir}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "group-1", "target": "quick-set", "output": "task", "label": "quick"},
    {"source": "group-1", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// ---------------------------------------------------------------------------
// Helpers (also used by group_behaviors_e2e_test.go, P2-T15).
// ---------------------------------------------------------------------------

type groupStackPaths struct {
	root         string
	workspaceDir string
	configPath   string
}

// writeGroupStack writes the manifests and a local-mode config WITHOUT booting,
// so restart tests can boot the same database twice. globalcap 10 never binds;
// perUserCap is the knob under test.
func writeGroupStack(t *testing.T, perUserCap int, manifests map[string]string) groupStackPaths {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	for name, manifest := range manifests {
		writeWorkflow(t, workspaceDir, name, manifest)
	}
	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 10\n  perusercap: " + strconv.Itoa(perUserCap) + "\n" +
		groupTestProfilesYAML
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	return groupStackPaths{root: root, workspaceDir: workspaceDir, configPath: configPath}
}

// bootGroupExecStack boots the REAL composition root against the manifests.
func bootGroupExecStack(t *testing.T, perUserCap int, manifests map[string]string) (*domainStack, groupStackPaths) {
	t.Helper()
	paths := writeGroupStack(t, perUserCap, manifests)
	ds, err := initStartedDomain(context.Background(), paths.configPath)
	require.NoError(t, err, "the composition root must boot with parallel_group workflows present")
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds, paths
}

func triggerGroupWorkflow(t *testing.T, ds *domainStack, name string) uuid.UUID {
	t.Helper()
	id, _, err := ds.eng.Trigger(context.Background(), name, map[string]any{}, "normal")
	require.NoError(t, err, "a results-mode group workflow must be accepted by the trigger")
	return id
}

// waitForTerminalState returns the terminal state id settles in. 40s matches
// waitForState and is deliberately SHORTER than the 60s group sweep, so a
// missing in-process path cannot be masked by the periodic reconciler.
func waitForTerminalState(t *testing.T, ds *domainStack, id uuid.UUID) instance.State {
	t.Helper()
	var st instance.State
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(context.Background(), id)
		if err != nil {
			return false
		}
		st = inst.State
		return st.IsTerminal()
	}, 40*time.Second, 150*time.Millisecond, "instance %s never reached a terminal state", id)
	return st
}

func parentVariables(t *testing.T, ds *domainStack, id uuid.UUID) map[string]any {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return inst.Variables
}

// groupOutputOf reads the group's output from the parent's PERSISTED variables
// — the sink downstream nodes and authors read ({{group-1.tasks.0.result...}}).
func groupOutputOf(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID string) map[string]any {
	t.Helper()
	vars := parentVariables(t, ds, parentID)
	out, ok := vars[nodeID].(map[string]any)
	require.True(t, ok, "parent variables must carry the group output under %q, got %#v", nodeID, vars[nodeID])
	return out
}

func groupTaskAt(t *testing.T, out map[string]any, i int) map[string]any {
	t.Helper()
	tasks, ok := out["tasks"].([]any)
	require.True(t, ok, "group output must carry an ordered tasks array, got %#v", out["tasks"])
	require.Greater(t, len(tasks), i)
	task, ok := tasks[i].(map[string]any)
	require.True(t, ok)
	return task
}

func groupTaskByKey(t *testing.T, out map[string]any, key string) map[string]any {
	t.Helper()
	byKey, ok := out["byKey"].(map[string]any)
	require.True(t, ok, "group output must carry byKey, got %#v", out["byKey"])
	task, ok := byKey[key].(map[string]any)
	require.True(t, ok, "byKey must contain %q", key)
	return task
}

func resultOf(t *testing.T, task map[string]any) map[string]any {
	t.Helper()
	res, ok := task["result"].(map[string]any)
	require.True(t, ok, "task %v must carry a result map, got %#v", task["key"], task["result"])
	return res
}

func nodeExecutionCount(t *testing.T, ds *domainStack, id uuid.UUID, nodeID string) int {
	t.Helper()
	execs, err := ds.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	n := 0
	for _, e := range execs {
		if e.NodeID == nodeID {
			n++
		}
	}
	return n
}

func groupRunOf(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID string) *instance.GroupRun {
	t.Helper()
	require.NotNil(t, ds.groupRunRepo, "domainStack must expose the group-run repository")
	run, err := ds.groupRunRepo.GetByParentNode(context.Background(), parentID, nodeID)
	require.NoError(t, err)
	require.NotNil(t, run, "no group run persisted for %s/%s", parentID, nodeID)
	return run
}

func groupRosterTask(t *testing.T, run *instance.GroupRun, key string) instance.GroupTask {
	t.Helper()
	for _, task := range run.Tasks {
		if task.Key == key {
			return task
		}
	}
	t.Fatalf("group run %s has no task %q", run.ID, key)
	return instance.GroupTask{}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// HAPPY PATH, branches form. Proves through the real root: both branches start
// from a COPY of the parent's pre-group working area and variables, write into
// isolated areas, finish in reverse author order, are gathered in AUTHOR order
// on the success port, never merge variables or files into the parent, and the
// parent continues exactly once — decided in-process by the coordinator.
func TestGroupE2E_BranchesAllSucceedGatherResultsInAuthorOrder(t *testing.T) {
	gates := newGroupProcessGates(t)
	manifest := gateOrderedManifest(t, groupBranchesManifest, []string{"a-run", "b-run"}, gates, false)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-branches": manifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-branches")
	releaseOrderedTasks(t, ds, parentID, gates, "b", "beta", "alpha")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID),
		"the parent must complete (a failed parent here means the parallel_group executor is not registered)")

	vars := parentVariables(t, ds, parentID)
	assert.Equal(t, "success", vars["branch"], "all tasks succeeded, so the SUCCESS port is taken")

	out := groupOutputOf(t, ds, parentID, "group-1")
	assert.Equal(t, "success", out["outcome"])
	assert.Equal(t, "all_succeed", out["rule"])
	assert.Equal(t, "", out["winner"], "all_succeed has no winner")

	a := groupTaskAt(t, out, 0)
	b := groupTaskAt(t, out, 1)
	assert.Equal(t, "a", a["key"], "tasks are presented in AUTHOR order")
	assert.Equal(t, "b", b["key"])
	assert.Equal(t, "a-run", a["origin"])
	assert.Equal(t, "b-run", b["origin"])
	for _, task := range []map[string]any{a, b} {
		assert.Equal(t, "completed", task["status"])
		assert.Equal(t, "branch", task["kind"])
	}

	ra, rb := resultOf(t, a), resultOf(t, b)
	assert.Equal(t, "alpha-done", ra["branch_value"])
	assert.Equal(t, "beta-done", rb["branch_value"], "results are distinct per task")
	aRun, ok := ra["a-run"].(map[string]any)
	require.True(t, ok, "branch a's node output must be part of its result")
	bRun, ok := rb["b-run"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "parent-seed", aRun["seed"], "branch a starts from a copy of the parent's working area")
	assert.Equal(t, "parent-seed", bRun["seed"], "branch b starts from a copy of the parent's working area")
	assert.Equal(t, "alpha", aRun["own"], "branch a's area is its own: b's write must not reach it")
	assert.Equal(t, "beta", bRun["own"], "branch b's area is its own: a's write must not reach it")
	assert.Equal(t, a, groupTaskByKey(t, out, "a"))
	assert.Equal(t, b, groupTaskByKey(t, out, "b"))

	for _, k := range []string{"branch_value", "label", "hold", "a-run", "b-run"} {
		_, present := vars[k]
		assert.False(t, present, "branch variable %q must NOT be merged into the parent", k)
	}

	check, ok := vars["check-parent"].(map[string]any)
	require.True(t, ok, "check-parent must have run in the parent after the group")
	assert.Equal(t, false, check["leaked"], "results-only: a branch's file must never appear in the parent's working area")
	assert.Equal(t, true, check["seedKept"], "the parent's own pre-group file is untouched")
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "check-parent"), "the parent continues exactly once")
	// SINK: the success-port continuation itself resolved the indexed results,
	// in author order (a is first although b finished first).
	assert.Equal(t, "alpha-done", vars["consumedFirst"], "{{group-1.tasks.0.result...}} resolves to the first AUTHORED task")
	assert.Equal(t, "beta-done", vars["consumedSecond"], "{{group-1.tasks.1.result...}} resolves to the second authored task")
	assert.Equal(t, "beta-done", vars["consumedByKey"], "{{group-1.byKey.b.result...}} agrees with the indexed view")
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"), "the success continuation runs exactly once")
	assert.Equal(t, 0, nodeExecutionCount(t, ds, parentID, "onFailure"), "the failure port is not taken")

	run := groupRunOf(t, ds, parentID, "group-1")
	ta, tb := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	require.NotNil(t, ta.TerminalSeq)
	require.NotNil(t, tb.TerminalSeq)
	assert.Less(t, *tb.TerminalSeq, *ta.TerminalSeq,
		"b is released first, yet the gathered tasks above are in author order")

	// HANDLER PROVENANCE: the decision is driven by the in-process task
	// terminal (SetGroupTerminalHandler), not by the 60s periodic sweep.
	require.NotNil(t, run.Decision)
	var lastDone time.Time
	for _, task := range run.Tasks {
		child, err := ds.instanceRepo.Get(ctx, task.ChildInstanceID)
		require.NoError(t, err)
		require.NotNil(t, child.CompletedAt)
		if child.CompletedAt.After(lastDone) {
			lastDone = *child.CompletedAt
		}
	}
	assert.Less(t, run.Decision.DecidedAt.Sub(lastDone), 10*time.Second,
		"the group must be decided right after its last task finished")
}

// HAPPY PATH, workflows form: the SAME child workflow twice with different
// inputs. Results are distinct, keyed, in author order (despite reversed
// completion), inherit the parent's starting variables, and the children are
// linked group tasks of this run.
func TestGroupE2E_WorkflowsFormRepeatsOneChildWithDistinctOrderedResults(t *testing.T) {
	gates := newGroupProcessGates(t)
	childManifest := gateOrderedManifest(t, groupEchoChildManifest, []string{"echo"}, gates, true)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-workflows":  groupWorkflowsManifest,
		"grp-echo-child": childManifest,
	})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-workflows")
	releaseOrderedTasks(t, ds, parentID, gates, "second", "two", "one")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	vars := parentVariables(t, ds, parentID)
	assert.Equal(t, "success", vars["branch"])

	out := groupOutputOf(t, ds, parentID, "group-1")
	first, second := groupTaskAt(t, out, 0), groupTaskAt(t, out, 1)
	assert.Equal(t, "first", first["key"])
	assert.Equal(t, "second", second["key"])
	for _, task := range []map[string]any{first, second} {
		assert.Equal(t, "workflow", task["kind"])
		assert.Equal(t, "grp-echo-child", task["origin"])
		assert.Equal(t, "completed", task["status"])
	}

	r1, r2 := resultOf(t, first), resultOf(t, second)
	assert.Equal(t, "one", r1["label"])
	assert.Equal(t, "one-child", r1["child_value"])
	assert.Equal(t, "two", r2["label"])
	assert.Equal(t, "two-child", r2["child_value"])
	echo1, ok := r1["echo"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "one", echo1["label"])
	assert.Equal(t, "from-parent", r1["shared_note"], "workflow children inherit the group's starting variables")
	assert.Equal(t, "from-parent", r2["shared_note"])

	kids := childrenOf(t, ds, parentID)
	require.Len(t, kids, 2, "exactly one child per task — never a duplicate launch")
	run := groupRunOf(t, ds, parentID, "group-1")
	keyByChild := map[string]string{}
	for _, k := range kids {
		full, err := ds.instanceRepo.Get(ctx, k.ID)
		require.NoError(t, err)
		assert.Equal(t, "grp-echo-child", full.WorkflowName)
		assert.Equal(t, instance.SpawnModeGroupTask, full.SpawnMode)
		assert.Equal(t, instance.GroupTaskKindWorkflow, full.GroupTaskKind)
		require.NotNil(t, full.GroupRunID)
		assert.Equal(t, run.ID, *full.GroupRunID)
		require.NotNil(t, full.GroupTaskKey)
		keyByChild[full.ID.String()] = *full.GroupTaskKey
	}
	assert.Equal(t, "first", keyByChild[fmt.Sprint(first["childInstanceId"])])
	assert.Equal(t, "second", keyByChild[fmt.Sprint(second["childInstanceId"])])

	for _, k := range []string{"child_value", "label", "echo"} {
		_, present := vars[k]
		assert.False(t, present, "child variable %q must NOT be merged into the parent", k)
	}

	t1, t2 := groupRosterTask(t, run, "first"), groupRosterTask(t, run, "second")
	require.NotNil(t, t1.TerminalSeq)
	require.NotNil(t, t2.TerminalSeq)
	assert.Less(t, *t2.TerminalSeq, *t1.TerminalSeq, "second is released and completes before first")
}

// EDGE: the group is the workflow's entrypoint and has a single task.
func TestGroupE2E_EntrypointGroupWithSingleTask(t *testing.T) {
	gates := newGroupProcessGates(t)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(groupEntryManifest), &manifest))
	nodes := manifest["nodes"].([]any)
	nodes[0].(map[string]any)["config"].(map[string]any)["group"].(map[string]any)["tasks"].([]any)[0].(map[string]any)["entry"] = "gate"
	nodes = append(nodes, map[string]any{"id": "gate", "type": "tool_call", "config": map[string]any{"command": "sh", "args": []string{"-c", groupGateScript, "sh", gates.dir, "only"}}})
	manifest["nodes"] = nodes
	edges := manifest["edges"].([]any)
	edges[0].(map[string]any)["target"] = "gate"
	manifest["edges"] = append(edges, map[string]any{"source": "gate", "target": "only-set"})
	b, err := json.Marshal(manifest)
	require.NoError(t, err)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-entry": string(b)})
	t.Cleanup(func() { gates.release(t, "only") })
	parentID := triggerGroupWorkflow(t, ds, "grp-entry")
	gates.reached(t, "only")
	waitForState(t, ds, parentID, instance.StateWaiting)
	gates.release(t, "only")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "group-1")
	tasks, ok := out["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, tasks, 1)
	only := groupTaskAt(t, out, 0)
	assert.Equal(t, "only", only["key"])
	assert.Equal(t, "completed", only["status"])
	assert.Equal(t, "x", resultOf(t, only)["only_value"])
}

// EVENT PROVENANCE: the launcher and coordinator publish group lifecycle
// events on the PARENT through the stack's real bus.
func TestGroupE2E_PublishesGroupLifecycleEventsOnTheParent(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-entry": groupEntryManifest})

	ch, unsub := ds.bus.SubscribeAll()
	stop := sync.OnceFunc(unsub)
	t.Cleanup(stop)
	var mu sync.Mutex
	var seen []engine.ScopedEvent
	go func() {
		for ev := range ch {
			mu.Lock()
			seen = append(seen, ev)
			mu.Unlock()
		}
	}()

	parentID := triggerGroupWorkflow(t, ds, "grp-entry")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	run := groupRunOf(t, ds, parentID, "group-1")

	has := func(eventType string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range seen {
			if ev.InstanceID != parentID || ev.Event.Type != eventType {
				continue
			}
			data, ok := ev.Event.Data.(map[string]any)
			if ok && fmt.Sprint(data["groupRunId"]) == run.ID.String() && data["nodeId"] == "group-1" {
				return true
			}
		}
		return false
	}
	require.Eventually(t, func() bool {
		return has(engine.EventTypeGroupLaunched) && has(engine.EventTypeGroupDecided)
	}, 10*time.Second, 50*time.Millisecond,
		"group.launched (launcher) and group.decided (coordinator) must reach the parent's subscribers")
}

// STARTUP RECONCILE PROVENANCE. Crash while one task is done and the other is
// still running; on restart RecoverCrashedInstances marks the running task
// failed WITHOUT any in-process wake, so only the startup groupCoord.Reconcile
// (run before the 60s sweep's first tick) can decide the group and resume the
// parent through the failure port. Mirrors
// TestSpawnWaitE2E_RestartReconcilesParentAfterCrash: the "crash" severs the DB
// from the live process, because a graceful Close would drain the running task
// to completion.
func TestGroupE2E_StartupReconcileSettlesGroupAfterCrash(t *testing.T) {
	gates := newGroupProcessGates(t)
	paths := writeGroupStack(t, 5, map[string]string{"grp-crash": groupCrashManifest})
	ctx := context.Background()

	ds1, err := initStartedDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { gates.release(t, "slow"); ds1.Close(context.Background()) })
	parentID, _, err := ds1.eng.Trigger(ctx, "grp-crash", map[string]any{"gateDir": gates.dir}, "normal")
	require.NoError(t, err)
	gates.reached(t, "slow")
	waitForState(t, ds1, parentID, instance.StateWaiting)

	var quickID, slowID uuid.UUID
	require.Eventually(t, func() bool {
		run, gerr := ds1.groupRunRepo.GetByParentNode(ctx, parentID, "group-1")
		if gerr != nil || run == nil || len(run.Tasks) != 2 {
			return false
		}
		for _, task := range run.Tasks {
			if task.LaunchState != instance.GroupTaskLaunched {
				return false
			}
			switch task.Key {
			case "quick":
				quickID = task.ChildInstanceID
			case "slow":
				slowID = task.ChildInstanceID
			}
		}
		q, qerr := ds1.instanceRepo.Get(ctx, quickID)
		return qerr == nil && q.State == instance.StateCompleted
	}, 20*time.Second, 100*time.Millisecond, "the quick task must complete before the simulated crash")
	slow, err := ds1.instanceRepo.Get(ctx, slowID)
	require.NoError(t, err)
	require.Equal(t, instance.StateRunning, slow.State, "bounded process gate must hold the slow task at the crash boundary")

	require.NoError(t, ds1.db.Close())
	// The abandoned process's walkers still finish against the closed database
	// and log through the process-wide logger; drain them inside this test so
	// their errors never land in a later test's log.
	t.Cleanup(func() { ds1.queueMgr.Stop(); ds1.eng.Shutdown() })

	ds2, err := initStartedDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds2.Close(context.Background()) })

	waitForState(t, ds2, slowID, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds2, parentID),
		"the startup group reconcile must decide the group and resume the waiting parent")
	assert.Equal(t, "failure", branchVariable(t, ds2, parentID))

	out := groupOutputOf(t, ds2, parentID, "group-1")
	assert.Equal(t, "failure", out["outcome"])
	quick := groupTaskByKey(t, out, "quick")
	assert.Equal(t, "completed", quick["status"])
	assert.Equal(t, "q", resultOf(t, quick)["quick_value"], "a completed task's result survives the crash")
	slowTask := groupTaskByKey(t, out, "slow")
	assert.Equal(t, "failed", slowTask["status"], "the crash-recovered task counts as failed")
	failure, ok := slowTask["failure"].(map[string]any)
	require.True(t, ok, "a failed task carries failure details")
	assert.Equal(t, "failed", failure["state"])
}

// PERIODIC SWEEP PROVENANCE. A group left with its only task already terminal
// and NO in-process wake (the state a lost wake or a mid-action crash leaves),
// created AFTER boot so the startup pass never saw it. With the sweep shortened
// to 1s, only the ticker goroutine can settle it. The rows are written within
// milliseconds of boot, well before the first 1s tick.
func TestGroupE2E_PeriodicSweepSettlesGroupWithoutAWake(t *testing.T) {
	prev := groupSweepInterval
	groupSweepInterval = time.Second
	t.Cleanup(func() { groupSweepInterval = prev })

	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-entry": groupEntryManifest})
	ctx := context.Background()

	var snapshot map[string]any
	require.NoError(t, json.Unmarshal([]byte(groupEntryManifest), &snapshot))

	parentID, runID, childID := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               parentID, OwnerID: localDefaultUser, WorkflowName: "grp-entry", WorkflowSnapshot: snapshot,
		State: instance.StateWaiting, Priority: instance.PriorityNormal,
		Variables: map[string]any{}, Visibility: instance.VisibilityPrivate,
	}))
	require.NoError(t, ds.instanceRepo.UpdateCurrentNode(ctx, parentID, "group-1"))

	require.NoError(t, ds.groupRunRepo.CreateWithTasks(ctx, &instance.GroupRun{
		ID: runID, ParentInstanceID: parentID, NodeID: "group-1", Attempt: 1,
		Form: "branches", CompletionRule: "all_succeed", UnfinishedBehavior: "cancel_unfinished", GatherMode: "results",
		Config: map[string]any{
			"form":               "branches",
			"tasks":              []any{map[string]any{"key": "only", "entry": "only-set"}},
			"completionRule":     "all_succeed",
			"unfinishedBehavior": "cancel_unfinished",
			"gatherMode":         "results",
		},
		BodyOwnership:  map[string][]string{"only": {"only-set"}},
		StartVariables: map[string]any{},
		State:          instance.GroupRunRunning,
		Tasks: []instance.GroupTask{{
			GroupRunID: runID, Position: 0, Key: "only", Kind: instance.GroupTaskKindBranch, Origin: "only-set",
			ChildInstanceID: childID, LaunchState: instance.GroupTaskPending,
			LiveStatus: instance.GroupTaskStatusNotStarted, Blocking: true,
		}},
	}))

	require.NoError(t, ds.groupRunRepo.MarkTaskLaunched(ctx, runID, "only"))
	pid, node, rid, key := parentID, "group-1", runID, "only"
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               childID, OwnerID: localDefaultUser, WorkflowName: "grp-entry", WorkflowSnapshot: snapshot,
		State: instance.StateCompleted, Priority: instance.PriorityNormal,
		Variables: map[string]any{"only_value": "swept"}, Visibility: instance.VisibilityPrivate,
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask, SpawnDepth: 1,
		GroupRunID: &rid, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
	}))

	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, parentID)
		return err == nil && inst.State == instance.StateCompleted
	}, 20*time.Second, 150*time.Millisecond, "only the periodic group sweep can settle this group")
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	out := groupOutputOf(t, ds, parentID, "group-1")
	assert.Equal(t, "swept", resultOf(t, groupTaskAt(t, out, 0))["only_value"])
}

// Exercise the sample's src/ + linked work/ layout through initDomain, not a
// substitute launcher. Gates keep the sibling idle while the first task commits.
func TestGroupE2E_ResultsLinkedWorktreeMetadataIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linked-worktree fixture uses Unix shell tools")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not installed on this host")
	}
	gitPath, err = filepath.Abs(gitPath)
	require.NoError(t, err)
	for _, form := range []string{"branches", "workflows"} {
		t.Run(form, func(t *testing.T) {
			gates := newGroupProcessGates(t)
			manifests := linkedWorktreeManifests(t, form, gitPath, gates)
			ds, paths := bootGroupExecStack(t, 5, manifests)
			t.Cleanup(func() {
				for _, key := range []string{"seed", "alpha", "beta", "parent"} {
					gates.release(t, key)
				}
			})
			parentID := triggerGroupWorkflow(t, ds, "grp-linked")
			gates.reached(t, "seed")
			area := func(id uuid.UUID) string {
				return filepath.Join(paths.workspaceDir, "instances", id.String())
			}
			parentDir := area(parentID)
			pointer, err := os.ReadFile(filepath.Join(parentDir, "work", ".git"))
			require.NoError(t, err)
			require.Contains(t, string(pointer), "gitdir:", "the producer must be a real linked worktree")
			parentBefore := linkedAreaTree(t, parentDir)
			gates.release(t, "seed")
			gates.reached(t, "alpha")
			gates.reached(t, "beta")
			waitForState(t, ds, parentID, instance.StateWaiting)
			run := groupRunOf(t, ds, parentID, "group-1")
			a := groupRosterTask(t, run, "a")
			b := groupRosterTask(t, run, "b")
			siblingBefore := linkedAreaTree(t, area(b.ChildInstanceID))
			gates.release(t, "alpha")
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, a.ChildInstanceID))
			assert.Equal(t, parentBefore, linkedAreaTree(t, parentDir), "task a cannot alter parent files, common refs/config/objects, or worktree index")
			assert.Equal(t, siblingBefore, linkedAreaTree(t, area(b.ChildInstanceID)), "task a cannot alter the idle sibling")
			gates.release(t, "beta")
			gates.reached(t, "parent")
			assert.Equal(t, parentBefore, linkedAreaTree(t, parentDir), "neither task's commit may reach the parent")
			gates.release(t, "parent")
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))

			// Read the actual downstream transform sink, not just a helper call or
			// a roster entry. Both forms must inherit staged AND dirty content.
			vars := parentVariables(t, ds, parentID)
			for _, key := range []string{"a", "b"} {
				assert.Equal(t, "dirty", vars[key+"Inherited"])
				assert.Equal(t, "staged", vars[key+"Index"])
			}
			assert.Equal(t, "alpha", vars["aOwn"])
			assert.Equal(t, "beta", vars["bOwn"])
			assert.NotEqual(t, vars["aCommit"], vars["bCommit"], "each task commits its own bytes")
			assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "after"))
			for _, key := range []string{"a", "b"} {
				task := groupTaskByKey(t, groupOutputOf(t, ds, parentID, "group-1"), key)
				producer := "probe"
				if form == "branches" {
					producer = key + "-run"
				}
				probe, ok := resultOf(t, task)[producer].(map[string]any)
				require.True(t, ok)
				assert.NotEmpty(t, probe["commit"])
				assert.NotEqual(t, probe["before"], probe["commit"], "a real commit advanced the task ref")
			}
		})
	}
}

// Hash every file (including Git objects, refs, config and binary index) and
// preserve symlink targets/modes in the comparison without invoking Git, which
// could refresh the very index being checked.
func linkedAreaTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		switch {
		case info.Mode().IsRegular():
			data, err = os.ReadFile(path)
		case info.Mode()&os.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(path)
			data = []byte(target)
		}
		if err != nil {
			return err
		}
		tree[rel] = fmt.Sprintf("%s:%x", info.Mode(), sha256.Sum256(data))
		return nil
	}))
	return tree
}

func linkedWorktreeManifests(t *testing.T, form, gitPath string, gates groupProcessGates) map[string]string {
	t.Helper()
	encode := func(value any) string {
		b, err := json.Marshal(value)
		require.NoError(t, err)
		return string(b)
	}
	tool := func(id, script, label string, entry bool) map[string]any {
		return map[string]any{"id": id, "type": "tool_call", "entrypoint": entry,
			"config": map[string]any{"command": "sh", "args": []string{"-ec", script, "sh", "{{workspace}}", label, gates.dir, gitPath}}}
	}
	// Fixed identity and disabled hooks only affect fixture Git commands; the
	// production results-only copier must not need a Git process at all.
	git := `git_bin=$4; git() { "$git_bin" -c user.name=GroupTest -c user.email=group@example.test -c core.hooksPath=/dev/null -c commit.gpgSign=false "$@"; }; `
	gate := `touch "$3/$2.reached"; n=0; while [ ! -f "$3/$2.release" ]; do n=$((n+1)); [ "$n" -lt 1200 ] || exit 91; sleep .05; done; `
	seed := git + `git init -q -b main "$1/src"; printf base > "$1/src/tracked"; git -C "$1/src" add tracked; git -C "$1/src" commit -q -m base; git -C "$1/src" worktree add -q -b mission "$1/work"; printf staged > "$1/work/tracked"; git -C "$1/work" add tracked; printf dirty > "$1/work/tracked"; ` + gate + `echo '{}'`
	probe := git + gate + `cd "$1/work"; inherited=$(cat tracked); staged=$(git show :tracked); before=$(git rev-parse HEAD); printf '%s' "$2" > tracked; git add tracked; git commit -q -m "$2"; git config group.task "$2"; printf '{"inherited":"%s","staged":"%s","before":"%s","commit":"%s","own":"%s"}' "$inherited" "$staged" "$before" "$(git rev-parse HEAD)" "$(cat tracked)"`
	tasks := []map[string]any{}
	nodes := []any{tool("seed", seed, "seed", true)}
	edges := []any{map[string]any{"source": "seed", "target": "group-1"}}
	manifests := map[string]string{}
	for i, key := range []string{"a", "b"} {
		label := []string{"alpha", "beta"}[i]
		task := map[string]any{"key": key, "inputs": map[string]any{"label": label}}
		if form == "branches" {
			// Branch bodies have distinct nodes; workflow tasks below repeat
			// the same child definition with different inputs.
			id := key + "-run"
			task["entry"] = id
			n := tool(id, probe, "{{label}}", false)
			nodes = append(nodes, n)
			edges = append(edges, map[string]any{"source": "group-1", "target": id, "output": "task", "label": key})
		} else {
			task["workflow"] = "grp-linked-child"
		}
		tasks = append(tasks, task)
	}
	if form == "workflows" {
		manifests["grp-linked-child"] = encode(map[string]any{"name": "grp-linked-child", "inputs": []any{map[string]any{"name": "label", "type": "string", "required": true}}, "nodes": []any{tool("probe", probe, "{{label}}", true)}, "edges": []any{}})
	}
	nodes = append(nodes, map[string]any{"id": "group-1", "type": "parallel_group", "config": map[string]any{"group": map[string]any{"form": form, "tasks": tasks, "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining", "gatherMode": "results"}}})
	operations := []any{}
	for _, key := range []string{"a", "b"} {
		producer := "probe"
		if form == "branches" {
			producer = key + "-run"
		}
		for suffix, field := range map[string]string{"Inherited": "inherited", "Index": "staged", "Own": "own", "Commit": "commit"} {
			operations = append(operations, map[string]any{"variable": key + suffix, "operation": "set", "value": "{{group-1.byKey." + key + ".result." + producer + "." + field + "}}"})
		}
	}
	nodes = append(nodes, tool("retain-parent", gate+`echo '{}'`, "parent", false), map[string]any{"id": "after", "type": "transform", "config": map[string]any{"operations": operations}}, map[string]any{"id": "failed", "type": "transform", "config": map[string]any{"operations": []any{map[string]any{"variable": "failed", "operation": "set", "value": true}}}})
	edges = append(edges, map[string]any{"source": "after", "target": "retain-parent"}, map[string]any{"source": "group-1", "target": "after", "output": "success"}, map[string]any{"source": "group-1", "target": "failed", "output": "failure"})
	manifests["grp-linked"] = encode(map[string]any{"name": "grp-linked", "inputs": []any{}, "nodes": nodes, "edges": edges})
	return manifests
}
