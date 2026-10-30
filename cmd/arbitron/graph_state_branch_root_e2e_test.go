package main

import (
	"context"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Branch "a" has a two-node body (a1 → a2) so a body node other than the entry
// must be attributed to the branch instance; "b" outlives it.
const gsBranchBodyManifest = `{
  "name": "gs-branch-body",
  "description": "a multi-node branch body",
  "inputs": [],
  "nodes": [
    {"id": "start", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "g", "type": "parallel_group",
     "config": {"group": {"form": "branches",
       "tasks": [{"key": "a", "entry": "a1"}, {"key": "b", "entry": "b1"}],
       "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining", "gatherMode": "results"}}},
    {"id": "a1", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "sleep 2; echo '{\"step\":1}'"]}},
    {"id": "a2", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "sleep 2; echo '{\"step\":2}'"]}},
    {"id": "b1", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "sleep 6; echo '{\"step\":1}'"]}},
    {"id": "done", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "success"}]}},
    {"id": "fail", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "start", "target": "g"},
    {"source": "g", "target": "a1", "output": "task", "label": "a"},
    {"source": "a1", "target": "a2"},
    {"source": "g", "target": "b1", "output": "task", "label": "b"},
    {"source": "g", "target": "done", "output": "success"},
    {"source": "g", "target": "fail", "output": "failure"}
  ]
}`

func gsNodeIDs(c engine.ExecutionContext) []string {
	ids := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		ids = append(ids, n.NodeID)
	}
	sort.Strings(ids)
	return ids
}

func gsNodeState(c engine.ExecutionContext, nodeID string) (engine.LiveNodeState, bool) {
	for _, n := range c.Nodes {
		if n.NodeID == nodeID {
			return n, true
		}
	}
	return engine.LiveNodeState{}, false
}

// gsExpectedBodyState derives a body node's state from the branch instance's
// persisted cursor and trace — the truth the run view must mirror.
func gsExpectedBodyState(inst *instance.Instance, execs []instance.NodeExecution, nodeID string) string {
	if inst.State == instance.StateRunning && inst.CurrentNodeID != nil && *inst.CurrentNodeID == nodeID {
		return engine.LiveStateRunning
	}
	for i := len(execs) - 1; i >= 0; i-- {
		if execs[i].NodeID == nodeID && execs[i].Status == instance.NodeStatusSucceeded {
			return engine.LiveStateCompleted
		}
	}
	return engine.LiveStatePending
}

// Paired seam 17 (Area 4 roster launch ↔ Area 8 live run view). The launcher
// runs a branch task as an instance of the PARENT's definition, from the body
// entry, stamped with (parent node, task key). Graph-state attributes exactly
// that (group, task)'s immediately owned body to it, whether the branch is the
// route root (hierarchy row / task card "open run") or a nested context of its
// parent. Each body node's state must equal the branch instance's own cursor
// and trace, never pending and never the parent's.
func TestGraphStateE2E_LaunchedBranchTaskOwnsItsBodyAsRootAndAsNestedContext(t *testing.T) {
	t.Parallel()
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)
	ctx := context.Background()

	parentID, _, err := ds.eng.Trigger(ctx, "gs-branch-body", map[string]any{}, "normal")
	require.NoError(t, err)

	// Nested context: the parent's view links task "a" to its launched instance.
	var branchID uuid.UUID
	require.Eventually(t, func() bool {
		_, gs, ferr := gsFetchGraphState(srv, parentID)
		if ferr != nil || gs == nil || len(gs.Groups) != 1 {
			return false
		}
		for _, task := range gs.Groups[0].Tasks {
			if task.Key == "a" && task.ChildInstanceID != nil {
				branchID = *task.ChildInstanceID
				return true
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond, "the parent's panel must link task a to its launched instance")

	_, parentView, err := gsFetchGraphState(srv, parentID)
	require.NoError(t, err)
	require.NotNil(t, parentView)
	assert.Equal(t, []string{"done", "fail", "g", "start"}, gsNodeIDs(parentView.Contexts[0]),
		"the parent context must never carry branch body nodes")
	var nested *engine.ExecutionContext
	for i := range parentView.Contexts {
		if parentView.Contexts[i].InstanceID == branchID {
			nested = &parentView.Contexts[i]
		}
	}
	require.NotNil(t, nested, "the launched branch must appear as a nested context")
	assert.Equal(t, engine.ContextKindBranch, nested.Kind)
	assert.Equal(t, "a", nested.TaskKey)
	assert.Equal(t, []string{"a1", "a2"}, gsNodeIDs(*nested))
	for _, n := range nested.Nodes {
		assert.Equal(t, branchID, n.InstanceID, "body node %s must be keyed by the branch instance", n.NodeID)
	}

	// Route root: the branch's own body, compared with its persisted cursor and
	// trace. Observe the entry running and then the second body node running.
	seen := map[string]bool{}
	require.Eventually(t, func() bool {
		before, gerr := ds.instanceRepo.Get(ctx, branchID)
		if gerr != nil {
			return false
		}
		code, gs, ferr := gsFetchGraphState(srv, branchID)
		if ferr != nil || code != http.StatusOK || gs == nil {
			return false
		}
		after, gerr := ds.instanceRepo.Get(ctx, branchID)
		execs, xerr := ds.nodeExecRepo.ListByInstance(ctx, branchID)
		if gerr != nil || xerr != nil || before.State != after.State ||
			fmtPtr(before.CurrentNodeID) != fmtPtr(after.CurrentNodeID) {
			return false // moved while reading; retry for a stable comparison
		}

		if !assert.Equal(t, branchID, gs.RootInstanceID) ||
			!assert.Equal(t, engine.ContextKindRoot, gs.Contexts[0].Kind) ||
			!assert.NotEmpty(t, gs.Contexts[0].Graph, "the root branch carries the definition the view renders") ||
			!assert.NotNil(t, gs.Layout) ||
			!assert.Equal(t, []string{"a1", "a2"}, gsNodeIDs(gs.Contexts[0]),
				"a branch root executes exactly its own body") {
			return true
		}
		for _, p := range gs.Groups {
			assert.NotEqual(t, "g", p.NodeID, "the parent-owned group has no panel under the branch root")
		}
		want := workflow.BodyOwner{GroupNodeID: "g", TaskKey: "a"}
		for _, n := range gs.Contexts[0].Nodes {
			assert.Equal(t, want, gs.Layout.Owner[n.NodeID],
				"the dashboard derives the root's anchor from layout.owner of its nodes")
			assert.Equal(t, branchID, n.InstanceID)
			if exp := gsExpectedBodyState(after, execs, n.NodeID); n.State != exp {
				t.Errorf("node %s: graph-state %q, branch cursor/trace say %q", n.NodeID, n.State, exp)
				return true
			}
			if n.State == engine.LiveStateRunning {
				seen[n.NodeID] = true
			}
		}
		return seen["a1"] && seen["a2"]
	}, 30*time.Second, 50*time.Millisecond, "the branch root must show a1 then a2 running from its own cursor")
	if t.Failed() {
		return
	}

	// While a2 runs the entry is completed from the branch's trace, not pending.
	_, mid, err := gsFetchGraphState(srv, branchID)
	require.NoError(t, err)
	a1, ok := gsNodeState(mid.Contexts[0], "a1")
	require.True(t, ok)
	assert.Equal(t, engine.LiveStateCompleted, a1.State)

	waitForState(t, ds, parentID, instance.StateCompleted)
	_, final, err := gsFetchGraphState(srv, branchID)
	require.NoError(t, err)
	require.NotNil(t, final)
	assert.False(t, final.Active)
	for _, id := range []string{"a1", "a2"} {
		n, ok := gsNodeState(final.Contexts[0], id)
		require.True(t, ok)
		assert.Equal(t, engine.LiveStateCompleted, n.State, "completed body node %s", id)
	}
}

func fmtPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
