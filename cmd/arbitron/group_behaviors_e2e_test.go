package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Manifests. Timing placeholders below are replaced by bounded process gates
// before boot; no test relies on their sleeps for ordering or crash windows.
// ---------------------------------------------------------------------------

const fsCancelManifest = `{
  "name": "grp-fs-cancel",
  "description": "first_success + cancel_unfinished",
  "inputs": [],
  "nodes": [
    {"id": "race", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "slow", "entry": "slow-hold"}, {"key": "fast", "entry": "fast-set"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}},
    {"id": "fast-set", "type": "transform",
     "config": {"operations": [{"variable": "fast_value", "operation": "set", "value": "won"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "race", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "race", "target": "fast-set", "output": "task", "label": "fast"},
    {"source": "race", "target": "onSuccess", "output": "success"},
    {"source": "race", "target": "onFailure", "output": "failure"}
  ]
}`

// Slow remains gated until the parent continues, then produces a late result.
const fsIndependentManifest = `{
  "name": "grp-fs-indep",
  "description": "first_success + finish_independently",
  "inputs": [],
  "nodes": [
    {"id": "race", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "slow", "entry": "slow-hold"}, {"key": "fast", "entry": "fast-set"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "finish_independently",
       "gatherMode": "results"}}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 5; echo '{}'"]}},
    {"id": "slow-set", "type": "transform",
     "config": {"operations": [{"variable": "slow_value", "operation": "set", "value": "late"}]}},
    {"id": "fast-set", "type": "transform",
     "config": {"operations": [{"variable": "fast_value", "operation": "set", "value": "won"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "race", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "slow-hold", "target": "slow-set"},
    {"source": "race", "target": "fast-set", "output": "task", "label": "fast"},
    {"source": "race", "target": "onSuccess", "output": "success"},
    {"source": "race", "target": "onFailure", "output": "failure"}
  ]
}`

// Same shape, with the loser gated while cancelling the completed parent.
const fsIndependentLongManifest = `{
  "name": "grp-indep-long",
  "description": "first_success + finish_independently, long loser",
  "inputs": [],
  "nodes": [
    {"id": "race", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "slow", "entry": "slow-hold"}, {"key": "fast", "entry": "fast-set"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "finish_independently",
       "gatherMode": "results"}}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}},
    {"id": "fast-set", "type": "transform",
     "config": {"operations": [{"variable": "fast_value", "operation": "set", "value": "won"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "race", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "race", "target": "fast-set", "output": "task", "label": "fast"},
    {"source": "race", "target": "onSuccess", "output": "success"},
    {"source": "race", "target": "onFailure", "output": "failure"}
  ]
}`

const asCancelManifest = `{
  "name": "grp-as-cancel",
  "description": "all_succeed + cancel_unfinished",
  "inputs": [],
  "nodes": [
    {"id": "all", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "slow", "entry": "slow-hold"}, {"key": "bad", "entry": "bad-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}},
    {"id": "bad-run", "type": "tool_call", "errorBehavior": {"type": "halt"},
     "config": {"command": "arbitron-no-such-binary-for-tests"}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "all", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "all", "target": "bad-run", "output": "task", "label": "bad"},
    {"source": "all", "target": "onSuccess", "output": "success"},
    {"source": "all", "target": "onFailure", "output": "failure"}
  ]
}`

// Slow remains gated after bad fails: the group must not decide yet.
const asWaitManifest = `{
  "name": "grp-as-wait",
  "description": "all_succeed + wait_for_remaining",
  "inputs": [],
  "nodes": [
    {"id": "all", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "slow", "entry": "slow-hold"}, {"key": "bad", "entry": "bad-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "wait_for_remaining",
       "gatherMode": "results"}}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 5; echo '{}'"]}},
    {"id": "slow-set", "type": "transform",
     "config": {"operations": [{"variable": "slow_value", "operation": "set", "value": "late"}]}},
    {"id": "bad-run", "type": "tool_call", "errorBehavior": {"type": "halt"},
     "config": {"command": "arbitron-no-such-binary-for-tests"}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [
       {"variable": "branch", "operation": "set", "value": "failure"},
       {"variable": "consumedFirstResult", "operation": "set", "value": "{{all.tasks.0.result.slow_value}}"},
       {"variable": "consumedSecondStatus", "operation": "set", "value": "{{all.tasks.1.status}}"},
       {"variable": "consumedByKeyStatus", "operation": "set", "value": "{{all.byKey.slow.status}}"}]}}
  ],
  "edges": [
    {"source": "all", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "slow-hold", "target": "slow-set"},
    {"source": "all", "target": "bad-run", "output": "task", "label": "bad"},
    {"source": "all", "target": "onSuccess", "output": "success"},
    {"source": "all", "target": "onFailure", "output": "failure"}
  ]
}`

const fsAllFailManifest = `{
  "name": "grp-fs-allfail",
  "description": "first_success, every candidate fails",
  "inputs": [],
  "nodes": [
    {"id": "race", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "bad1", "entry": "bad1-run"}, {"key": "bad2", "entry": "bad2-run"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "bad1-run", "type": "tool_call", "errorBehavior": {"type": "halt"},
     "config": {"command": "arbitron-no-such-binary-for-tests"}},
    {"id": "bad2-run", "type": "tool_call", "errorBehavior": {"type": "halt"},
     "config": {"command": "arbitron-no-such-binary-for-tests"}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "race", "target": "bad1-run", "output": "task", "label": "bad1"},
    {"source": "race", "target": "bad2-run", "output": "task", "label": "bad2"},
    {"source": "race", "target": "onSuccess", "output": "success"},
    {"source": "race", "target": "onFailure", "output": "failure"}
  ]
}`

// Both candidates are gated; victim is cancelled before late is released.
const fsVictimManifest = `{
  "name": "grp-fs-victim",
  "description": "first_success, a cancelled candidate never wins",
  "inputs": [],
  "nodes": [
    {"id": "race", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "victim", "entry": "victim-hold"}, {"key": "late", "entry": "late-hold"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "victim-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}},
    {"id": "late-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 3; echo '{}'"]}},
    {"id": "late-set", "type": "transform",
     "config": {"operations": [{"variable": "late_value", "operation": "set", "value": "won-late"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "race", "target": "victim-hold", "output": "task", "label": "victim"},
    {"source": "race", "target": "late-hold", "output": "task", "label": "late"},
    {"source": "late-hold", "target": "late-set"},
    {"source": "race", "target": "onSuccess", "output": "success"},
    {"source": "race", "target": "onFailure", "output": "failure"}
  ]
}`

// A child workflow whose entrypoint is a branches group.
const nestBranchChildManifest = `{
  "name": "grp-nest-branch-child",
  "description": "branches group inside a workflow task",
  "inputs": [],
  "nodes": [
    {"id": "bg", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "x", "entry": "x-set"}, {"key": "y", "entry": "y-set"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "x-set", "type": "transform",
     "config": {"operations": [{"variable": "xv", "operation": "set", "value": "X"}]}},
    {"id": "y-set", "type": "transform",
     "config": {"operations": [{"variable": "yv", "operation": "set", "value": "Y"}]}},
    {"id": "done", "type": "transform",
     "config": {"operations": [{"variable": "child_done", "operation": "set", "value": "{{bg.byKey.x.result.xv}}+{{bg.byKey.y.result.yv}}"}]}},
    {"id": "bad", "type": "transform",
     "config": {"operations": [{"variable": "child_done", "operation": "set", "value": "inner-failed"}]}}
  ],
  "edges": [
    {"source": "bg", "target": "x-set", "output": "task", "label": "x"},
    {"source": "bg", "target": "y-set", "output": "task", "label": "y"},
    {"source": "bg", "target": "done", "output": "success"},
    {"source": "bg", "target": "bad", "output": "failure"}
  ]
}`

const nestWorkflowParentManifest = `{
  "name": "grp-nest-wf-parent",
  "description": "workflows group whose child holds a branches group",
  "inputs": [],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "workflows",
       "tasks": [{"key": "inner", "workflow": "grp-nest-branch-child"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "onSuccess", "output": "success"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

const leafChildManifest = `{
  "name": "grp-leaf-child",
  "description": "leaf workflow",
  "inputs": [{"name": "label", "type": "string", "required": true}],
  "nodes": [
    {"id": "leaf", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "leaf_value", "operation": "set", "value": "{{label}}-leaf"}]}}
  ],
  "edges": []
}`

// A branches group whose "left" body contains a workflows group (l2). The
// nested task's input is a template resolved against the BRANCH's variables
// (side=left at that point).
const nestBranchParentManifest = `{
  "name": "grp-nest-branch-parent",
  "description": "workflows group inside a branch",
  "inputs": [],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "left", "entry": "l1"}, {"key": "right", "entry": "r1"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "l1", "type": "transform",
     "config": {"operations": [{"variable": "side", "operation": "set", "value": "left"}]}},
    {"id": "l2", "type": "parallel_group",
     "config": {"group": {
       "form": "workflows",
       "tasks": [{"key": "w", "workflow": "grp-leaf-child", "inputs": {"label": "from-{{side}}"}}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "l3", "type": "transform",
     "config": {"operations": [{"variable": "inner_done", "operation": "set", "value": "{{l2.byKey.w.result.leaf_value}}"}]}},
    {"id": "lfail", "type": "transform",
     "config": {"operations": [{"variable": "inner_done", "operation": "set", "value": "inner-failed"}]}},
    {"id": "r1", "type": "transform",
     "config": {"operations": [{"variable": "side", "operation": "set", "value": "right"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "l1", "output": "task", "label": "left"},
    {"source": "l1", "target": "l2"},
    {"source": "l2", "target": "l3", "output": "success"},
    {"source": "l2", "target": "lfail", "output": "failure"},
    {"source": "outer", "target": "r1", "output": "task", "label": "right"},
    {"source": "outer", "target": "onSuccess", "output": "success"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

// grp-cancel-inner: a workflow child whose branches group holds two gated tasks.
const cancelInnerManifest = `{
  "name": "grp-cancel-inner",
  "description": "nested group with long tasks",
  "inputs": [],
  "nodes": [
    {"id": "inner", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "x", "entry": "x-hold"}, {"key": "y", "entry": "y-hold"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "x-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}},
    {"id": "y-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}},
    {"id": "inner-ok", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "success"}]}},
    {"id": "inner-bad", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "inner", "target": "x-hold", "output": "task", "label": "x"},
    {"source": "inner", "target": "y-hold", "output": "task", "label": "y"},
    {"source": "inner", "target": "inner-ok", "output": "success"},
    {"source": "inner", "target": "inner-bad", "output": "failure"}
  ]
}`

const cancelOuterManifest = `{
  "name": "grp-cancel-outer",
  "description": "parent of a nested group",
  "inputs": [],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "workflows",
       "tasks": [{"key": "c", "workflow": "grp-cancel-inner"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "onSuccess", "output": "success"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

// grp-deep-indep: the root's only branch ("mid") runs a NESTED first_success
// group with finish_independently. mid completes as soon as the nested fast
// task wins, so the nested gated loser keeps running BELOW A COMPLETED BRANCH of
// a COMPLETED root — the exact subtree shape ListActiveChildren could not walk.
const deepIndependentManifest = `{
  "name": "grp-deep-indep",
  "description": "independent nested loser below a completed branch",
  "inputs": [],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "mid", "entry": "inner"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "inner", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "slow", "entry": "slow-hold"}, {"key": "fast", "entry": "fast-set"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "finish_independently",
       "gatherMode": "results"}}},
    {"id": "slow-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 30; echo '{}'"]}},
    {"id": "fast-set", "type": "transform",
     "config": {"operations": [{"variable": "fast_value", "operation": "set", "value": "won"}]}},
    {"id": "inner-ok", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "success"}]}},
    {"id": "inner-bad", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "failure"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "inner", "output": "task", "label": "mid"},
    {"source": "inner", "target": "slow-hold", "output": "task", "label": "slow"},
    {"source": "inner", "target": "fast-set", "output": "task", "label": "fast"},
    {"source": "inner", "target": "inner-ok", "output": "success"},
    {"source": "inner", "target": "inner-bad", "output": "failure"},
    {"source": "outer", "target": "onSuccess", "output": "success"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

// grp-restart (run at per-user cap 1): a holds the only slot at its process
// gate while b waits queued behind it.
const restartManifest = `{
  "name": "grp-restart",
  "description": "crash with a queued task",
  "inputs": [],
  "nodes": [
    {"id": "race", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "a", "entry": "a-hold"}, {"key": "b", "entry": "b-set"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "a-hold", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 4; echo '{}'"]}},
    {"id": "b-set", "type": "transform",
     "config": {"operations": [{"variable": "b_value", "operation": "set", "value": "b-won"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "race", "target": "a-hold", "output": "task", "label": "a"},
    {"source": "race", "target": "b-set", "output": "task", "label": "b"},
    {"source": "race", "target": "onSuccess", "output": "success"},
    {"source": "race", "target": "onFailure", "output": "failure"}
  ]
}`

// A results_and_files group NESTED inside a results-mode branch: the trigger
// guard must scan every group node, not only the top-level one.
const filesNestedManifest = `{
  "name": "grp-files-nested",
  "description": "nested results_and_files group",
  "inputs": [],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "t", "entry": "inner"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "inner", "type": "parallel_group",
     "config": {"group": {
       "form": "workflows",
       "tasks": [{"key": "w", "workflow": "grp-leaf-child", "inputs": {"label": "z"}}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "default-agent"}}},
    {"id": "inner-ok", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "success"}]}},
    {"id": "inner-bad", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "failure"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "inner", "output": "task", "label": "t"},
    {"source": "inner", "target": "inner-ok", "output": "success"},
    {"source": "inner", "target": "inner-bad", "output": "failure"},
    {"source": "outer", "target": "onSuccess", "output": "success"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

const filesChildManifest = `{
  "name": "grp-files-child",
  "description": "child workflow with a results_and_files group",
  "inputs": [],
  "nodes": [
    {"id": "g", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "only", "entry": "only-set"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "default-agent"}}},
    {"id": "only-set", "type": "transform",
     "config": {"operations": [{"variable": "only_value", "operation": "set", "value": "x"}]}},
    {"id": "ok", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "bad", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "g", "target": "only-set", "output": "task", "label": "only"},
    {"source": "g", "target": "ok", "output": "success"},
    {"source": "g", "target": "bad", "output": "failure"}
  ]
}`

const filesParentManifest = `{
  "name": "grp-files-parent",
  "description": "results-mode parent whose workflow task is a files group",
  "inputs": [],
  "nodes": [
    {"id": "g", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "workflows",
       "tasks": [{"key": "f", "workflow": "grp-files-child"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results"}}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "g", "target": "onSuccess", "output": "success"},
    {"source": "g", "target": "onFailure", "output": "failure"}
  ]
}`

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// gateBehaviorManifest inserts process gates before the named producers. Gates
// live outside copied workspaces; every timed hold in these manifests is replaced
// before boot so elapsed time cannot decide which task wins or survives a crash.
func gateBehaviorManifest(t *testing.T, manifest string, gates groupProcessGates, nodeIDs ...string) string {
	t.Helper()
	var wf map[string]any
	require.NoError(t, json.Unmarshal([]byte(manifest), &wf))
	nodes := wf["nodes"].([]any)
	edges := wf["edges"].([]any)
	for _, id := range nodeIDs {
		found := false
		for _, raw := range nodes {
			n := raw.(map[string]any)
			if n["id"] != id {
				continue
			}
			found = true
			cfg := n["config"].(map[string]any)
			if args, ok := cfg["args"].([]any); ok {
				script := args[1].(string)
				for _, seconds := range []string{"30", "5", "4", "3"} {
					script = strings.ReplaceAll(script, "sleep "+seconds+"; ", "")
				}
				require.NotContains(t, script, "sleep ")
				args[1] = script
			}
		}
		require.True(t, found, "missing gated node %s", id)
		gateID := "gate-" + id
		for _, raw := range edges {
			e := raw.(map[string]any)
			if e["target"] == id {
				e["target"] = gateID
			}
		}
		for _, raw := range nodes {
			n := raw.(map[string]any)
			if n["type"] != "parallel_group" {
				continue
			}
			for _, rawTask := range n["config"].(map[string]any)["group"].(map[string]any)["tasks"].([]any) {
				task := rawTask.(map[string]any)
				if task["entry"] == id {
					task["entry"] = gateID
				}
			}
		}
		nodes = append(nodes, map[string]any{"id": gateID, "type": "tool_call", "config": map[string]any{
			"command": "sh", "args": []string{"-c", groupGateScript, "sh", gates.dir, id},
		}})
		edges = append(edges, map[string]any{"source": gateID, "target": id})
	}
	wf["nodes"], wf["edges"] = nodes, edges
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	return string(b)
}

func bootGatedBehaviorStack(t *testing.T, perUserCap int, manifests map[string]string, gated map[string][]string) (*domainStack, groupProcessGates) {
	t.Helper()
	gates := newGroupProcessGates(t)
	for name, nodes := range gated {
		manifests[name] = gateBehaviorManifest(t, manifests[name], gates, nodes...)
	}
	ds, _ := bootGroupExecStack(t, perUserCap, manifests)
	// Cleanup is LIFO: unblock processes before Close drains engine goroutines.
	t.Cleanup(func() {
		for _, nodes := range gated {
			for _, node := range nodes {
				gates.release(t, node)
			}
		}
	})
	return ds, gates
}

// A terminal row/receipt is committed before the engine's terminal callback.
// Drain the real producers before asserting absence of a late continuation or
// resurrection; observing only that row would leave those assertions premature.
func drainBehaviorEngine(t *testing.T, ds *domainStack) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		ds.eng.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("engine terminal callbacks did not drain")
	}
}

func stateOf(t *testing.T, ds *domainStack, id uuid.UUID) instance.State {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return inst.State
}

// taskChildID waits until task `key` of the group at nodeID under parentID has
// been launched, and returns its child instance id (reserved before enqueue).
func taskChildID(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID, key string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.Eventually(t, func() bool {
		run, err := ds.groupRunRepo.GetByParentNode(context.Background(), parentID, nodeID)
		if err != nil || run == nil {
			return false
		}
		for _, task := range run.Tasks {
			if task.Key == key && task.LaunchState == instance.GroupTaskLaunched {
				id = task.ChildInstanceID
				return true
			}
		}
		return false
	}, 40*time.Second, 100*time.Millisecond, "task %q of %s/%s was never launched", key, parentID, nodeID)
	return id
}

// waitForLiveStatus waits for the group run's LIVE status of a task — the
// operator-facing state that keeps moving after the decision froze the output.
func waitForLiveStatus(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID, key string, want instance.GroupTaskStatus) {
	t.Helper()
	require.Eventually(t, func() bool {
		run, err := ds.groupRunRepo.GetByParentNode(context.Background(), parentID, nodeID)
		if err != nil || run == nil {
			return false
		}
		for _, task := range run.Tasks {
			if task.Key == key {
				return task.LiveStatus == want
			}
		}
		return false
	}, 40*time.Second, 100*time.Millisecond, "task %q never reached live status %s", key, want)
}

// ---------------------------------------------------------------------------
// first_success
// ---------------------------------------------------------------------------

// The fast task wins although authored SECOND; the slow loser is cancelled.
// The frozen output says "unfinished" + cancelRequested (the decision boundary
// never claims a confirmed cancellation), while the LIVE status later becomes
// cancelled — and the frozen output does not change when it does.
func TestGroupBehaviorsE2E_FirstSuccessCancelUnfinished(t *testing.T) {
	t.Parallel()
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-fs-cancel": fsCancelManifest},
		map[string][]string{"grp-fs-cancel": {"slow-hold", "fast-set"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-cancel")
	gates.reached(t, "slow-hold")
	gates.reached(t, "fast-set")
	waitForState(t, ds, parentID, instance.StateWaiting)
	gates.release(t, "fast-set")
	slowID := taskChildID(t, ds, parentID, "race", "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "race")
	assert.Equal(t, "success", out["outcome"])
	assert.Equal(t, "first_success", out["rule"])
	assert.Equal(t, "cancel_unfinished", out["behavior"])
	assert.Equal(t, "fast", out["winner"])
	assert.Equal(t, "slow", groupTaskAt(t, out, 0)["key"], "author order, not completion order")
	assert.Equal(t, "fast", groupTaskAt(t, out, 1)["key"])

	fast := groupTaskByKey(t, out, "fast")
	assert.Equal(t, "completed", fast["status"])
	assert.Equal(t, true, fast["winner"])
	assert.Equal(t, "won", resultOf(t, fast)["fast_value"])
	slow := groupTaskByKey(t, out, "slow")
	assert.Equal(t, "unfinished", slow["status"], "at the decision the loser had not finished")
	assert.Equal(t, true, slow["cancelRequested"], "cancellation is shown as REQUESTED, separately from status")
	assert.Equal(t, false, slow["winner"])

	waitForState(t, ds, slowID, instance.StateCancelled)
	waitForLiveStatus(t, ds, parentID, "race", "slow", instance.GroupTaskStatusCancelled)
	drainBehaviorEngine(t, ds)
	assert.Equal(t, out, groupOutputOf(t, ds, parentID, "race"), "a later terminal never changes the frozen output")
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"))
	assert.Equal(t, 0, nodeExecutionCount(t, ds, parentID, "onFailure"))
}

// The loser keeps running after the parent has continued, then completes on
// its own. Its late result never reaches the frozen output, and the parent is
// continued EXACTLY once.
func TestGroupBehaviorsE2E_FirstSuccessFinishIndependently(t *testing.T) {
	t.Parallel()
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-fs-indep": fsIndependentManifest},
		map[string][]string{"grp-fs-indep": {"slow-hold", "fast-set"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-indep")
	gates.reached(t, "slow-hold")
	gates.reached(t, "fast-set")
	waitForState(t, ds, parentID, instance.StateWaiting)
	gates.release(t, "fast-set")
	slowID := taskChildID(t, ds, parentID, "race", "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	require.False(t, stateOf(t, ds, slowID).IsTerminal(),
		"finish_independently: the loser must still be running after the parent continued")

	frozen := groupOutputOf(t, ds, parentID, "race")
	assert.Equal(t, "fast", frozen["winner"])
	assert.Equal(t, "finish_independently", frozen["behavior"])

	gates.release(t, "slow-hold")
	waitForState(t, ds, slowID, instance.StateCompleted)
	waitForLiveStatus(t, ds, parentID, "race", "slow", instance.GroupTaskStatusCompleted)
	drainBehaviorEngine(t, ds)

	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, parentID))
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"),
		"a later task completion must never trigger another continuation")
	after := groupOutputOf(t, ds, parentID, "race")
	assert.Equal(t, frozen, after, "the frozen output is immutable")
	slow := groupTaskByKey(t, after, "slow")
	assert.Equal(t, "unfinished", slow["status"])
	assert.Equal(t, false, slow["cancelRequested"], "finish_independently never requests cancellation")
	assert.Equal(t, map[string]any{}, slow["result"], "an unfinished task has no gathered result, even after it finishes")

	run := groupRunOf(t, ds, parentID, "race")
	roster := groupRosterTask(t, run, "slow")
	assert.True(t, roster.ContinuingIndependently)
	assert.False(t, roster.CancelRequested)
	assert.True(t, run.ContinuationDelivered)
	assert.Equal(t, "late", stateVar(t, ds, slowID, "slow_value"), "the loser really ran to completion")
}

// Every candidate fails: the group fails only once all are terminal.
func TestGroupBehaviorsE2E_FirstSuccessEveryCandidateFails(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-fs-allfail": fsAllFailManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-allfail")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "race")
	assert.Equal(t, "failure", out["outcome"])
	assert.Equal(t, "", out["winner"])
	assert.NotEmpty(t, out["reason"])
	for _, key := range []string{"bad1", "bad2"} {
		task := groupTaskByKey(t, out, key)
		assert.Equal(t, "failed", task["status"], key)
		failure, ok := task["failure"].(map[string]any)
		require.True(t, ok, "%s must carry failure details", key)
		assert.Equal(t, "failed", failure["state"])
		assert.Equal(t, key+"-run", failure["failedNodeId"])
	}
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onFailure"))
}

// A task cancelled by an operator is not a success: the group keeps waiting
// for the other candidate, which then wins.
func TestGroupBehaviorsE2E_FirstSuccessCancelledTaskNeverWins(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-fs-victim": fsVictimManifest},
		map[string][]string{"grp-fs-victim": {"victim-hold", "late-hold"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-victim")
	gates.reached(t, "victim-hold")
	gates.reached(t, "late-hold")
	waitForState(t, ds, parentID, instance.StateWaiting)
	victimID := taskChildID(t, ds, parentID, "race", "victim")
	waitForState(t, ds, victimID, instance.StateRunning)
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), victimID), "the victim process gate must remain held until cancellation")
	waitForState(t, ds, victimID, instance.StateCancelled)
	waitForLiveStatus(t, ds, parentID, "race", "victim", instance.GroupTaskStatusCancelled)
	gates.release(t, "late-hold")

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	out := groupOutputOf(t, ds, parentID, "race")
	assert.Equal(t, "late", out["winner"], "the cancelled task terminated FIRST but must never win")
	victim := groupTaskByKey(t, out, "victim")
	assert.Equal(t, "cancelled", victim["status"])
	assert.Equal(t, false, victim["winner"])
	late := groupTaskByKey(t, out, "late")
	assert.Equal(t, "completed", late["status"])
	assert.Equal(t, "won-late", resultOf(t, late)["late_value"])
}

// ---------------------------------------------------------------------------
// all_succeed failure behaviours
// ---------------------------------------------------------------------------

// One task fails: cancel_unfinished routes failure without releasing slow's
// process gate and confirms its cancellation in the live roster.
func TestGroupBehaviorsE2E_AllSucceedCancelUnfinishedFailsFast(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-as-cancel": asCancelManifest},
		map[string][]string{"grp-as-cancel": {"slow-hold", "bad-run"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-as-cancel")
	gates.reached(t, "slow-hold")
	gates.reached(t, "bad-run")
	waitForState(t, ds, parentID, instance.StateWaiting)
	gates.release(t, "bad-run")
	slowID := taskChildID(t, ds, parentID, "all", "slow")
	require.Eventually(t, func() bool {
		return stateOf(t, ds, parentID) == instance.StateCompleted
	}, 15*time.Second, 100*time.Millisecond,
		"cancel_unfinished must take the failure port while slow remains gated")
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "all")
	assert.Equal(t, "failure", out["outcome"])
	assert.NotEmpty(t, out["reason"])
	bad := groupTaskByKey(t, out, "bad")
	assert.Equal(t, "failed", bad["status"])
	failure, ok := bad["failure"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bad-run", failure["failedNodeId"])
	assert.NotEmpty(t, failure["error"])
	slow := groupTaskByKey(t, out, "slow")
	assert.Equal(t, "unfinished", slow["status"])
	assert.Equal(t, true, slow["cancelRequested"])

	waitForState(t, ds, slowID, instance.StateCancelled)
	waitForLiveStatus(t, ds, parentID, "all", "slow", instance.GroupTaskStatusCancelled)
}

// wait_for_remaining: after the failure the group must not decide while slow
// is gated. Its eventual result must reach the failure port's frozen output.
func TestGroupBehaviorsE2E_AllSucceedWaitForRemaining(t *testing.T) {
	t.Parallel()
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-as-wait": asWaitManifest},
		map[string][]string{"grp-as-wait": {"slow-hold", "bad-run"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-as-wait")
	gates.reached(t, "slow-hold")
	gates.reached(t, "bad-run")
	waitForState(t, ds, parentID, instance.StateWaiting)
	gates.release(t, "bad-run")
	slowID := taskChildID(t, ds, parentID, "all", "slow")
	badID := taskChildID(t, ds, parentID, "all", "bad")
	waitForState(t, ds, badID, instance.StateFailed)
	waitForLiveStatus(t, ds, parentID, "all", "bad", instance.GroupTaskStatusFailed)
	require.Equal(t, instance.StateRunning, stateOf(t, ds, slowID), "slow is held until the failure has been processed")
	assert.Equal(t, instance.StateWaiting, stateOf(t, ds, parentID))
	assert.Nil(t, groupRunOf(t, ds, parentID, "all").Decision, "wait_for_remaining must not decide while slow is held")
	gates.release(t, "slow-hold")

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, slowID))

	out := groupOutputOf(t, ds, parentID, "all")
	assert.Equal(t, "failure", out["outcome"])
	slow := groupTaskByKey(t, out, "slow")
	assert.Equal(t, "completed", slow["status"],
		"the decision was taken only after the slow task finished, so it is frozen as completed")
	assert.Equal(t, "late", resultOf(t, slow)["slow_value"], "its result is available on the failure port")
	assert.Equal(t, "failed", groupTaskByKey(t, out, "bad")["status"])

	// SINK: the failure-port continuation itself resolved the indexed output in
	// author order (slow is authored first, bad second), exactly once.
	assert.Equal(t, "late", stateVar(t, ds, parentID, "consumedFirstResult"), "{{all.tasks.0.result...}} resolves on the failure port")
	assert.Equal(t, "failed", stateVar(t, ds, parentID, "consumedSecondStatus"), "{{all.tasks.1.status}} is the second authored task")
	assert.Equal(t, "completed", stateVar(t, ds, parentID, "consumedByKeyStatus"))
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onFailure"), "the failure continuation runs exactly once")
	assert.Equal(t, 0, nodeExecutionCount(t, ds, parentID, "onSuccess"), "the success port is not taken")
}

// ---------------------------------------------------------------------------
// nesting
// ---------------------------------------------------------------------------

// A workflows-form task whose child workflow contains a branches group: the
// nested group completes within its enclosing task, and the outer group
// gathers the child's final variables.
func TestGroupBehaviorsE2E_NestedBranchesInsideWorkflowChild(t *testing.T) {
	t.Parallel()
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-nest-wf-parent":    nestWorkflowParentManifest,
		"grp-nest-branch-child": nestBranchChildManifest,
	})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-nest-wf-parent")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "outer")
	inner := groupTaskByKey(t, out, "inner")
	assert.Equal(t, "completed", inner["status"])
	assert.Equal(t, "X+Y", resultOf(t, inner)["child_done"],
		"the nested branches group's ordered results reached the workflow child, then the outer group")

	childID, err := uuid.Parse(fmt.Sprint(inner["childInstanceId"]))
	require.NoError(t, err)
	grandkids := childrenOf(t, ds, childID)
	require.Len(t, grandkids, 2, "the nested group's two branches are children of the workflow task")
	for _, g := range grandkids {
		full, gerr := ds.instanceRepo.Get(ctx, g.ID)
		require.NoError(t, gerr)
		assert.Equal(t, instance.SpawnModeGroupTask, full.SpawnMode)
		require.NotNil(t, full.ParentNodeID)
		assert.Equal(t, "bg", *full.ParentNodeID)
		assert.Equal(t, 2, full.SpawnDepth, "grandchildren sit two hops below the root")
	}
}

// A branch whose body contains a workflows group: the nested task's templated
// input resolves against the BRANCH's own variables, the branch continues
// through the nested group's success port, and the two branches' variables
// stay independent.
func TestGroupBehaviorsE2E_NestedWorkflowsInsideBranch(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-nest-branch-parent": nestBranchParentManifest,
		"grp-leaf-child":         leafChildManifest,
	})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-nest-branch-parent")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "outer")
	left, right := groupTaskByKey(t, out, "left"), groupTaskByKey(t, out, "right")
	assert.Equal(t, "from-left-leaf", resultOf(t, left)["inner_done"])
	assert.Equal(t, "left", resultOf(t, left)["side"])
	assert.Equal(t, "right", resultOf(t, right)["side"], "each branch has its own copy of the variables")
	_, leaked := parentVariables(t, ds, parentID)["side"]
	assert.False(t, leaked, "branch variables are never merged into the parent")

	leftID, err := uuid.Parse(fmt.Sprint(left["childInstanceId"]))
	require.NoError(t, err)
	kids := childrenOf(t, ds, leftID)
	require.Len(t, kids, 1, "the nested workflow task is a child of the LEFT branch instance")
	full, err := ds.instanceRepo.Get(ctx, kids[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "grp-leaf-child", full.WorkflowName)
	require.NotNil(t, full.ParentNodeID)
	assert.Equal(t, "l2", *full.ParentNodeID)
	assert.Equal(t, instance.SpawnModeGroupTask, full.SpawnMode)
}

// ---------------------------------------------------------------------------
// capacity
// ---------------------------------------------------------------------------

// Per-user cap of ONE with two nesting levels: every waiting parent must
// release its slot, or the first queued task can never start and the tree
// deadlocks.
func TestGroupBehaviorsE2E_PerUserCapOneDoesNotDeadlock(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 1, map[string]string{
		"grp-nest-wf-parent":    nestWorkflowParentManifest,
		"grp-nest-branch-child": nestBranchChildManifest,
	})

	parentID := triggerGroupWorkflow(t, ds, "grp-nest-wf-parent")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID),
		"a waiting group parent must hold no slot at a per-user cap of one")
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	assert.Equal(t, "X+Y", resultOf(t, groupTaskByKey(t, groupOutputOf(t, ds, parentID, "outer"), "inner"))["child_done"])
}

// ---------------------------------------------------------------------------
// cancellation
// ---------------------------------------------------------------------------

// Cancelling the root stops every unfinished descendant: the workflow task,
// which is itself waiting on a nested group, and that group's gated branches.
// The cancelled root is never resurrected through its failure port.
func TestGroupBehaviorsE2E_ParentCancellationCascadesThroughNestedGroups(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{
		"grp-cancel-outer": cancelOuterManifest,
		"grp-cancel-inner": cancelInnerManifest,
	}, map[string][]string{"grp-cancel-inner": {"x-hold", "y-hold"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-cancel-outer")
	gates.reached(t, "x-hold")
	gates.reached(t, "y-hold")
	innerID := taskChildID(t, ds, parentID, "outer", "c")
	waitForState(t, ds, parentID, instance.StateWaiting)
	waitForState(t, ds, innerID, instance.StateWaiting)
	xID := taskChildID(t, ds, innerID, "inner", "x")
	yID := taskChildID(t, ds, innerID, "inner", "y")
	waitForState(t, ds, xID, instance.StateRunning)
	waitForState(t, ds, yID, instance.StateRunning)

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), parentID))

	for _, id := range []uuid.UUID{parentID, innerID, xID, yID} {
		waitForState(t, ds, id, instance.StateCancelled)
	}
	drainBehaviorEngine(t, ds)
	assert.Equal(t, instance.StateCancelled, stateOf(t, ds, parentID), "a cancelled parent must never be resurrected by its dying tasks")
	assert.Nil(t, branchVariable(t, ds, parentID), "neither port of the cancelled parent may run")
	assert.Nil(t, parentVariables(t, ds, innerID)["inner_branch"], "nor of the cancelled nested parent")
}

// Cancelling a COMPLETED parent stops its still-running independent loser and
// succeeds, without reopening the parent's outcome or its frozen output.
func TestGroupBehaviorsE2E_CancellingCompletedParentStopsIndependentTask(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-indep-long": fsIndependentLongManifest},
		map[string][]string{"grp-indep-long": {"slow-hold", "fast-set"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-indep-long")
	gates.reached(t, "slow-hold")
	gates.reached(t, "fast-set")
	waitForState(t, ds, parentID, instance.StateWaiting)
	gates.release(t, "fast-set")
	slowID := taskChildID(t, ds, parentID, "race", "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	require.False(t, stateOf(t, ds, slowID).IsTerminal(), "precondition: the gated loser is still running")
	frozen := groupOutputOf(t, ds, parentID, "race")

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), parentID),
		"cancelling a completed parent with independent work must succeed")

	waitForState(t, ds, slowID, instance.StateCancelled)
	waitForLiveStatus(t, ds, parentID, "race", "slow", instance.GroupTaskStatusCancelled)
	drainBehaviorEngine(t, ds)
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, parentID), "the parent's outcome is never reopened")
	assert.Equal(t, frozen, groupOutputOf(t, ds, parentID, "race"), "nor is its frozen output")
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"))
}

// THE P2-D SUBTREE CASE THROUGH THE REAL STACK. Root completed -> branch
// "mid" completed -> nested loser "slow" still gated. Cancelling the root
// must traverse BOTH terminal levels and stop the loser, leaving both
// terminal ancestors exactly as they were.
func TestGroupBehaviorsE2E_CancelReachesIndependentTaskBelowCompletedBranch(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-deep-indep": deepIndependentManifest},
		map[string][]string{"grp-deep-indep": {"slow-hold", "fast-set"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-deep-indep")
	gates.reached(t, "slow-hold")
	gates.reached(t, "fast-set")
	gates.release(t, "fast-set")
	midID := taskChildID(t, ds, parentID, "outer", "mid")
	slowID := taskChildID(t, ds, midID, "inner", "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	waitForState(t, ds, midID, instance.StateCompleted)
	require.False(t, stateOf(t, ds, slowID).IsTerminal(), "precondition: the nested gated loser is still running")
	assert.Equal(t, "success", parentVariables(t, ds, midID)["inner_branch"])
	rootOutput := groupOutputOf(t, ds, parentID, "outer")
	midOutput := groupOutputOf(t, ds, midID, "inner")

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), parentID))

	waitForState(t, ds, slowID, instance.StateCancelled)
	drainBehaviorEngine(t, ds)
	assert.Equal(t, rootOutput, groupOutputOf(t, ds, parentID, "outer"))
	assert.Equal(t, midOutput, groupOutputOf(t, ds, midID, "inner"))
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"))
	assert.Equal(t, 1, nodeExecutionCount(t, ds, midID, "inner-ok"))
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, midID), "a terminal intermediate is traversed, never rewritten")
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, parentID))
	waitForLiveStatus(t, ds, midID, "inner", "slow", instance.GroupTaskStatusCancelled)
}

// ---------------------------------------------------------------------------
// restart
// ---------------------------------------------------------------------------

// Crash with task a RUNNING (holding the only slot) and task b still QUEUED.
// On restart pool-aware recovery (P3 T10) re-enters a's held gate step, which
// repeats; a still holds the only slot, so b stays queued, a finishes first
// and wins, and cancel_unfinished cancels b. A graceful ds.Close would drain
// both tasks in the first process, so the crash severs the DB, exactly like
// TestSpawnWaitE2E_RestartReconcilesParentAfterCrash.
func TestGroupBehaviorsE2E_RestartRepeatsRunningTaskAfterCrash(t *testing.T) {
	t.Parallel()
	gates := newGroupProcessGates(t)
	manifest := gateBehaviorManifest(t, restartManifest, gates, "a-hold")
	paths := writeGroupStack(t, 1, map[string]string{"grp-restart": manifest})
	ctx := context.Background()

	ds1, err := initStartedDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { gates.release(t, "a-hold"); ds1.Close(context.Background()) })
	// Both children enqueue while their parent still owns the only slot. Give
	// a explicit admission priority, rather than depending on equal-second
	// queued_at values or SQLite's incidental tie order.
	_, err = ds1.db.Exec(`CREATE TRIGGER admit_restart_a_first AFTER INSERT ON instances
		WHEN NEW.workflow_name = 'grp-restart' AND NEW.group_task_key = 'a'
		BEGIN UPDATE instances SET priority = 'high' WHERE id = NEW.id; END`)
	require.NoError(t, err)
	parentID := triggerGroupWorkflow(t, ds1, "grp-restart")
	gates.reached(t, "a-hold")
	aID := taskChildID(t, ds1, parentID, "race", "a")
	bID := taskChildID(t, ds1, parentID, "race", "b")

	require.Equal(t, instance.StateRunning, stateOf(t, ds1, aID), "running task must be held at its crash gate")
	require.Equal(t, instance.StateQueued, stateOf(t, ds1, bID))
	require.Equal(t, instance.StateWaiting, stateOf(t, ds1, parentID))

	require.NoError(t, ds1.db.Close())
	// The abandoned process's walkers still finish against the closed database
	// and log through the process-wide logger; drain them inside this test so
	// their errors never land in a later test's log. Release the held gate
	// first: Shutdown waits on that walker, which otherwise idles out the
	// gate's full 60s bound before it can exit.
	t.Cleanup(func() { gates.release(t, "a-hold"); ds1.queueMgr.Stop(); ds1.eng.Shutdown() })
	require.NoError(t, os.Remove(filepath.Join(gates.dir, "a-hold.reached")))

	ds2, err := initStartedDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds2.Close(context.Background()) })

	gates.reached(t, "a-hold") // a's gate step repeats after the restart
	require.Equal(t, instance.StateQueued, stateOf(t, ds2, bID), "the recovered task still holds the only slot")
	gates.release(t, "a-hold")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds2, parentID))
	assert.Equal(t, "success", branchVariable(t, ds2, parentID))

	out := groupOutputOf(t, ds2, parentID, "race")
	assert.Equal(t, "a", out["winner"])
	assert.Equal(t, "completed", groupTaskByKey(t, out, "a")["status"], "the recovered task wins")
	b := groupTaskByKey(t, out, "b")
	assert.Equal(t, "unfinished", b["status"], "the decision freezes before cancellation is applied")
	assert.Equal(t, true, b["cancelRequested"])
	waitForState(t, ds2, bID, instance.StateCancelled)
	assert.Equal(t, 1, nodeExecutionCount(t, ds2, parentID, "onSuccess"), "exactly one continuation across the restart")
	assert.Positive(t, p3NoticeCount(t, ds2, aID, "gate-a-hold"))
}

// ---------------------------------------------------------------------------
// Phase 3: the former Phase 2 refusals now run
// ---------------------------------------------------------------------------

// A nested files group gathers into its enclosing results-mode branch.
func TestGroupBehaviorsE2E_NestedFilesGroupInsideResultsBranchRuns(t *testing.T) {
	t.Parallel()
	requireHostGit(t)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-files-nested": filesNestedManifest,
		"grp-leaf-child":   leafChildManifest,
	})
	require.Equal(t, engine.GroupSupportResultsAndFiles, ds.eng.GroupSupportLevel())
	parentID := triggerGroupWorkflow(t, ds, "grp-files-nested")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	out := groupOutputOf(t, ds, parentID, "outer")
	tTask := groupTaskByKey(t, out, "t")
	assert.Equal(t, "completed", tTask["status"])
	assert.Equal(t, "success", resultOf(t, tTask)["inner_branch"])
	branchID := uuid.MustParse(fmt.Sprint(tTask["childInstanceId"]))
	inner := groupRunOf(t, ds, branchID, "inner")
	require.NotNil(t, inner.Gather)
	assert.Equal(t, "applied", inner.Gather.State)
	require.NotEmpty(t, inner.Gather.Tasks)
	assert.True(t, strings.HasPrefix(inner.Gather.Tasks[0].Reason, "merged"), "reason %q", inner.Gather.Tasks[0].Reason)
}

// A results-mode parent can launch a workflow containing its own files group.
func TestGroupBehaviorsE2E_FilesChildWorkflowRunsUnderResultsParent(t *testing.T) {
	t.Parallel()
	requireHostGit(t)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-files-parent": filesParentManifest,
		"grp-files-child":  filesChildManifest,
	})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-parent")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	out := groupOutputOf(t, ds, parentID, "g")
	reason := fmt.Sprint(out["reason"])
	assert.NotContains(t, reason, "launch failed")
	f := groupTaskByKey(t, out, "f")
	assert.Equal(t, "completed", f["status"])
	assert.Equal(t, "success", resultOf(t, f)["branch"])
	kids := childrenOf(t, ds, parentID)
	require.Len(t, kids, 1)
	assert.Equal(t, "grp-files-child", kids[0].WorkflowName)
}

// stateVar reads one persisted variable of an instance.
func stateVar(t *testing.T, ds *domainStack, id uuid.UUID, key string) any {
	t.Helper()
	return parentVariables(t, ds, id)[key]
}
