# Parallel Workflow Gathering — Phase 2 Task 15: End-to-end group behaviours through the composition root

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Prove, through the real `initDomain` stack, every Phase 2 behaviour the spec promises beyond the all-succeed happy path: both `first_success` unfinished behaviours, both `all_succeed` failure behaviours, all candidates failing, a cancelled candidate never winning, nesting in both directions, a per-user cap of one, parent cancellation through nested groups and after parent completion, restart with a queued task, and the Phase 2 refusal of `results_and_files` (top-level-nested and via a child workflow).
**Architecture:** One test file, `cmd/arbitron/group_behaviors_e2e_test.go`, in `package main`. Each test writes its manifests, boots the real stack with P2-T14's `bootGroupExecStack`/`writeGroupStack`, drives it with `Engine.Trigger`/`Engine.Cancel`, and asserts the SINKS: the parent's persisted state and variables (the frozen group output), the child instance rows, the node-execution trace, and the persisted group run (live statuses). No production code changes; failures here are defects in the producer task named in each test's comment.
**Tech Stack:** Go 1.25.5, SQLite, real `sh` tool_call nodes for timing, testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (Completion rules; Gathering results; Existing behavior and cancellation); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §Testing (Success / Failure / Edge-case lists), §3 coordinator rules, §5 cancellation.

## Required deterministic scheduling fixtures

Before writing the runtime assertions, replace elapsed-sleep race/crash windows with
bounded test-controlled process gates in a separate test temp directory. Wait for the
specific task to announce that it reached its gate, then cancel, decide or restart; always
release gates in cleanup. If task order itself is under test, control queue admission
explicitly instead of depending on goroutine timing. A race precondition is an assertion,
never a SKIP. Run happy/success/failure/error/edge cases with `-race`; lost-wake, independent
late-result and cancellation checks must reach the downstream persisted outcome sink.


**Interfaces**
- Consumes (from P2-T14, `cmd/arbitron/group_e2e_test.go` / `main.go`): `writeGroupStack`, `bootGroupExecStack`, `triggerGroupWorkflow`, `waitForTerminalState`, `parentVariables`, `groupOutputOf`, `groupTaskAt`, `groupTaskByKey`, `resultOf`, `nodeExecutionCount`, `groupRunOf`, `groupRosterTask`, `domainStack.groupRunRepo`, `domainStack.db`.
- Consumes (existing `package main` helpers): `waitForState`, `branchVariable` (`spawn_wait_e2e_test.go:131,140`), `childrenOf`, `adminCtxForLocalUser` (`spawn_e2e_test.go:99,87`), `initDomain`.
- Consumes (contract): `instance.GroupTaskLaunched`, `instance.GroupTaskStatus*`, `instance.SpawnModeGroupTask`, `Instance.GroupTaskKey`, `GroupTask.{Key, ChildInstanceID, LaunchState, LiveStatus, CancelRequested, ContinuingIndependently}`, `GroupRun.{Decision, ContinuationDelivered}`, `GroupDecision.{WinnerKey, Reason}`, `engine.ErrGroupExecutionUnsupported`; output shape from P2-T9 `BuildGroupOutput` (`outcome`, `rule`, `behavior`, `winner`, `reason`, `tasks[]`, `byKey{}` with `key`, `kind`, `origin`, `childInstanceId`, `status`, `result`, `failure`, `cancelRequested`, `winner`); `DecideLaunchFailure` reason prefix `"launch failed: "` and never-launched task `failure` `{"state":"not_started","error":"not launched: …"}` (P2-T9); P1-T9 refusal text `node "<id>"` + `"file gathering is not available yet"` at `GroupSupportResults`.
- Produces: test-only helpers `taskChildID(t, ds, parentID, nodeID, key) uuid.UUID`, `stateOf(t, ds, id) instance.State`, `waitForLiveStatus(t, ds, parentID, nodeID, key, want)`, and 18 manifest constants (below).

**Wiring & data provenance**
- Composition root: none added here; every behaviour runs on the wiring P2-T14 adds (`RegisterExecutor(NodeTypeParallelGroup, …)`, `SetGroupTerminalHandler(groupCoord)`, `SetGroupSupport(GroupSupportResults)`, startup `groupCoord.Reconcile`, the group sweep) plus the existing `SetSpawnLinks(instanceRepo)` cascade (P2-T12).
- Which producer each test exercises (and therefore which task to fix if it fails):

| Test | Producers under test |
|---|---|
| `…FirstSuccessCancelUnfinished` | P2-T9 decision/output (winner, `unfinished`, `cancelRequested`), P2-T10 cancel action + later live status, P2-T11 continuation |
| `…FirstSuccessFinishIndependently` | P2-T10 `MarkTaskContinuingIndependently`, immutable frozen output, wake-once (`WakeParent` never re-wakes) |
| `…AllSucceedCancelUnfinishedFailsFast` | P2-T10 immediate failure decision + cancel |
| `…AllSucceedWaitForRemaining` | P2-T9 "decide failure only when all terminal" |
| `…FirstSuccessEveryCandidateFails` | P2-T9 all-terminal failure |
| `…FirstSuccessCancelledTaskNeverWins` | P2-T9 cancelled ≠ success, terminal ordering |
| `…NestedBranchesInsideWorkflowChild`, `…NestedWorkflowsInsideBranch` | P2-T7 depth/links for nested parents, P2-T8 typed template inputs inside a branch, P2-T11 re-entry in a task instance |
| `…PerUserCapOneDoesNotDeadlock` | P2-T11 `waiting` + `NotifyCapacityFreed` at two nesting levels |
| `…ParentCancellationCascadesThroughNestedGroups` | P2-T12 cascade, P2-T10 "parent cancelled ⇒ cancel every task, never wake" |
| `…CancellingCompletedParentStopsIndependentTask` | P2-T12 terminal-parent cancel; P2-T10 live status after decision |
| `…CancelReachesIndependentTaskBelowCompletedBranch` | P2-T12 traversal through a completed branch below a completed root; P2-T10 nested `finish_independently` |
| `…RestartQueuedTaskStillWinsAfterCrash` | P2-T14 startup reconcile ordering, P2-T10 intake of a crash-failed task, queue recovery |
| `…ResultsAndFilesRefusedAtTrigger` | P1-T9 `checkGroupSupport` scanning nested group nodes |
| `…ResultsAndFilesChildWorkflowFailsLaunch` | P2-T7 group-support guard in the workflow-task trigger path, P2-T8 post-persist launch failure |

- Timing choices (all real `sh` sleeps; every wait is bounded at 40s like `waitForState`, which is shorter than the 60s group sweep so the sweep can never mask a broken in-process path):
  - 30s sleeps are used ONLY for tasks the test expects to be CANCELLED. Cancellation SIGKILLs the process group (`adapter/process/runner.go:killProcGroup`), so they never actually run 30s, and 30s is far beyond any wait, so "completed" can never be mistaken for "cancelled".
  - 3–5s sleeps are used for tasks that must still be RUNNING when something else happens (a decision, a crash, an operator cancel) and must then finish on their own; `ds.Close` drains them, so they bound test duration.
  - "Fast" tasks are `transform` nodes (milliseconds) or a nonexistent command (fails at exec).

### Task 15: Group behaviours end-to-end

**Files:**
- Create: `cmd/arbitron/group_behaviors_e2e_test.go`
- Producer regression (discovered by the required downstream race gate): `domain/queue/manager.go`, `domain/queue/manager_test.go`, `domain/queue/manager_enqueue_test.go`

- [x] Step 1: Write the tests. Create `cmd/arbitron/group_behaviors_e2e_test.go`:

> RESOLVED: P2-T14 (`fd8d000`) now supplies the real root wiring and helpers, with P2-T16/T17 producers present. Added all 15 planned tests and 18 manifest fixtures. All race/crash windows use bounded external process gates, released before stack cleanup; restart admission explicitly prioritizes task a through a test-only insert trigger and the real queue. No scheduling precondition is skipped. Adapted the plan's `nil` unfinished-result assertion to P2-T9's pinned empty-map output contract (`group_output_test.go`); both represent no final result, and the test requires the exact established shape.

Initial TDD red: temporarily removed only `eng.SetGroupTerminalHandler(groupCoord)` before the first run. First-success cancellation timed out at the persisted parent terminal sink (40s), and all-succeed cancellation failed to route while slow remained gated (15s). Restored wiring before running green; no production change is retained.

```go
package main

import (
	"context"
	"fmt"
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
// Manifests
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

// slow sleeps 5s: long enough to still be running when the parent continues
// (the parent continues within ~1s of the fast task), short enough to finish
// within the test so its LATE completion can be observed.
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

// Same shape with a 30s loser, for cancelling the COMPLETED parent.
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

// slow sleeps 5s so that, after bad fails (~immediately), there is a multi-
// second window in which the group must NOT decide.
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
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
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

// victim (30s) is cancelled by an operator; late (3s) then succeeds. 3s gives
// the test ample time to cancel victim first.
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

// grp-cancel-inner: a workflow child whose branches group holds two 30s tasks.
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
// task wins, so the nested 30s loser keeps running BELOW A COMPLETED BRANCH of
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

// grp-restart (run at per-user cap 1): a holds the only slot for 4s while b
// waits queued behind it.
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
       "resolverProfile": "resolver"}}},
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
       "resolverProfile": "resolver"}}},
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
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-fs-cancel": fsCancelManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-cancel")
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
	assert.Equal(t, out, groupOutputOf(t, ds, parentID, "race"), "a later terminal never changes the frozen output")
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"))
	assert.Equal(t, 0, nodeExecutionCount(t, ds, parentID, "onFailure"))
}

// The loser keeps running after the parent has continued, then completes on
// its own. Its late result never reaches the frozen output, and the parent is
// continued EXACTLY once.
func TestGroupBehaviorsE2E_FirstSuccessFinishIndependently(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-fs-indep": fsIndependentManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-indep")
	slowID := taskChildID(t, ds, parentID, "race", "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	require.False(t, stateOf(t, ds, slowID).IsTerminal(),
		"finish_independently: the loser must still be running after the parent continued")

	frozen := groupOutputOf(t, ds, parentID, "race")
	assert.Equal(t, "fast", frozen["winner"])
	assert.Equal(t, "finish_independently", frozen["behavior"])

	waitForState(t, ds, slowID, instance.StateCompleted)
	waitForLiveStatus(t, ds, parentID, "race", "slow", instance.GroupTaskStatusCompleted)

	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, parentID))
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"),
		"a later task completion must never trigger another continuation")
	after := groupOutputOf(t, ds, parentID, "race")
	assert.Equal(t, frozen, after, "the frozen output is immutable")
	slow := groupTaskByKey(t, after, "slow")
	assert.Equal(t, "unfinished", slow["status"])
	assert.Equal(t, false, slow["cancelRequested"], "finish_independently never requests cancellation")
	assert.Nil(t, slow["result"], "an unfinished task has no gathered result, even after it finishes")

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
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-fs-victim": fsVictimManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-victim")
	victimID := taskChildID(t, ds, parentID, "race", "victim")
	waitForState(t, ds, victimID, instance.StateRunning)
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), victimID), "the victim process gate must remain held until cancellation")
	waitForState(t, ds, victimID, instance.StateCancelled)

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

// One task fails fast: cancel_unfinished routes FAILURE immediately (well
// inside the 30s the other task would take) and cancels the slow task.
func TestGroupBehaviorsE2E_AllSucceedCancelUnfinishedFailsFast(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-as-cancel": asCancelManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-as-cancel")
	slowID := taskChildID(t, ds, parentID, "all", "slow")
	require.Eventually(t, func() bool {
		return stateOf(t, ds, parentID) == instance.StateCompleted
	}, 15*time.Second, 100*time.Millisecond,
		"cancel_unfinished must take the failure port as soon as one task fails, not after the 30s task")
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

// wait_for_remaining: after the fast failure the group must NOT decide while
// the slow task runs; the failure port is taken only once it finishes, and its
// result is part of the frozen output.
func TestGroupBehaviorsE2E_AllSucceedWaitForRemaining(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-as-wait": asWaitManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-as-wait")
	slowID := taskChildID(t, ds, parentID, "all", "slow")
	badID := taskChildID(t, ds, parentID, "all", "bad")
	waitForState(t, ds, badID, instance.StateFailed)
	if !stateOf(t, ds, slowID).IsTerminal() {
		waitForState(t, ds, parentID, instance.StateWaiting)
		assert.Never(t, func() bool { return stateOf(t, ds, parentID).IsTerminal() },
			time.Second, 100*time.Millisecond,
			"wait_for_remaining must not decide while a task is still running")
	}

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
}

// ---------------------------------------------------------------------------
// nesting
// ---------------------------------------------------------------------------

// A workflows-form task whose child workflow contains a branches group: the
// nested group completes within its enclosing task, and the outer group
// gathers the child's final variables.
func TestGroupBehaviorsE2E_NestedBranchesInsideWorkflowChild(t *testing.T) {
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
// which is itself waiting on a nested group, and that group's running branches.
// The cancelled root is never resurrected through its failure port.
func TestGroupBehaviorsE2E_ParentCancellationCascadesThroughNestedGroups(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-cancel-outer": cancelOuterManifest,
		"grp-cancel-inner": cancelInnerManifest,
	})

	parentID := triggerGroupWorkflow(t, ds, "grp-cancel-outer")
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
	assert.Never(t, func() bool { return stateOf(t, ds, parentID) != instance.StateCancelled },
		2*time.Second, 100*time.Millisecond, "a cancelled parent must never be resurrected by its dying tasks")
	assert.Nil(t, branchVariable(t, ds, parentID), "neither port of the cancelled parent may run")
	assert.Nil(t, parentVariables(t, ds, innerID)["inner_branch"], "nor of the cancelled nested parent")
}

// Cancelling a COMPLETED parent stops its still-running independent loser and
// succeeds, without reopening the parent's outcome or its frozen output.
func TestGroupBehaviorsE2E_CancellingCompletedParentStopsIndependentTask(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-indep-long": fsIndependentLongManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-indep-long")
	slowID := taskChildID(t, ds, parentID, "race", "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	require.False(t, stateOf(t, ds, slowID).IsTerminal(), "precondition: the 30s loser is still running")
	frozen := groupOutputOf(t, ds, parentID, "race")

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), parentID),
		"cancelling a completed parent with independent work must succeed")

	waitForState(t, ds, slowID, instance.StateCancelled)
	waitForLiveStatus(t, ds, parentID, "race", "slow", instance.GroupTaskStatusCancelled)
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, parentID), "the parent's outcome is never reopened")
	assert.Equal(t, frozen, groupOutputOf(t, ds, parentID, "race"), "nor is its frozen output")
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"))
}

// THE P2-D SUBTREE CASE THROUGH THE REAL STACK. Root completed -> branch
// "mid" completed -> nested loser "slow" still running. Cancelling the root
// must traverse BOTH terminal levels and stop the loser, leaving both
// terminal ancestors exactly as they were.
func TestGroupBehaviorsE2E_CancelReachesIndependentTaskBelowCompletedBranch(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-deep-indep": deepIndependentManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-deep-indep")
	midID := taskChildID(t, ds, parentID, "outer", "mid")
	slowID := taskChildID(t, ds, midID, "inner", "slow")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	waitForState(t, ds, midID, instance.StateCompleted)
	require.False(t, stateOf(t, ds, slowID).IsTerminal(), "precondition: the nested 30s loser is still running")
	assert.Equal(t, "success", parentVariables(t, ds, midID)["inner_branch"])

	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), parentID))

	waitForState(t, ds, slowID, instance.StateCancelled)
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, midID), "a terminal intermediate is traversed, never rewritten")
	assert.Equal(t, instance.StateCompleted, stateOf(t, ds, parentID))
	waitForLiveStatus(t, ds, midID, "inner", "slow", instance.GroupTaskStatusCancelled)
}

// ---------------------------------------------------------------------------
// restart
// ---------------------------------------------------------------------------

// Crash with task a RUNNING (holding the only slot) and task b still QUEUED.
// On restart a is crash-failed (no wake), the startup reconcile takes that in
// without deciding (first_success: b may still win), the queue starts b, and b
// wins. A graceful ds.Close would instead drain both tasks to completion in the
// first process, so the crash is simulated by severing the DB, exactly like
// TestSpawnWaitE2E_RestartReconcilesParentAfterCrash.
func TestGroupBehaviorsE2E_RestartQueuedTaskStillWinsAfterCrash(t *testing.T) {
	paths := writeGroupStack(t, 1, map[string]string{"grp-restart": restartManifest})
	ctx := context.Background()

	ds1, err := initDomain(ctx, paths.configPath)
	require.NoError(t, err)
	parentID := triggerGroupWorkflow(t, ds1, "grp-restart")
	aID := taskChildID(t, ds1, parentID, "race", "a")
	bID := taskChildID(t, ds1, parentID, "race", "b")

	var aSt, bSt, pSt instance.State
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		aSt, bSt, pSt = stateOf(t, ds1, aID), stateOf(t, ds1, bID), stateOf(t, ds1, parentID)
		if aSt == instance.StateRunning && bSt == instance.StateQueued && pSt == instance.StateWaiting {
			break
		}
		if aSt.IsTerminal() || bSt.IsTerminal() || pSt.IsTerminal() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, instance.StateRunning, aSt, "running task must be held at its crash gate")
	require.Equal(t, instance.StateQueued, bSt)
	require.Equal(t, instance.StateWaiting, pSt)

	require.NoError(t, ds1.db.Close())

	ds2, err := initDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds2.Close(context.Background()) })

	waitForState(t, ds2, aID, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds2, parentID))
	assert.Equal(t, "success", branchVariable(t, ds2, parentID))

	out := groupOutputOf(t, ds2, parentID, "race")
	assert.Equal(t, "b", out["winner"])
	assert.Equal(t, "failed", groupTaskByKey(t, out, "a")["status"], "the crash-failed task is frozen as failed")
	b := groupTaskByKey(t, out, "b")
	assert.Equal(t, "completed", b["status"])
	assert.Equal(t, "b-won", resultOf(t, b)["b_value"])
	assert.Equal(t, 1, nodeExecutionCount(t, ds2, parentID, "onSuccess"), "exactly one continuation across the restart")
}

// ---------------------------------------------------------------------------
// Phase 2 refusals
// ---------------------------------------------------------------------------

// A results_and_files group nested inside a results-mode branch is refused at
// the trigger, before anything is created.
func TestGroupBehaviorsE2E_ResultsAndFilesRefusedAtTrigger(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-files-nested": filesNestedManifest,
		"grp-leaf-child":   leafChildManifest,
	})
	ctx := context.Background()

	_, _, err := ds.eng.Trigger(ctx, "grp-files-nested", map[string]any{}, "normal")
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupExecutionUnsupported)
	assert.Contains(t, err.Error(), `node "inner"`, "the refusal names the offending NESTED group node")
	assert.Contains(t, err.Error(), "file gathering is not available yet")

	name := "grp-files-nested"
	rows, lerr := ds.instanceRepo.List(ctx, instance.ListFilters{Unconstrained: true, WorkflowName: &name, Limit: 10})
	require.NoError(t, lerr)
	assert.Empty(t, rows)
}

// A results-mode parent whose workflow task is a results_and_files workflow:
// the task's trigger is refused by the same guard, which the launcher turns
// into a recorded launch failure routed through the group's FAILURE port —
// never a silent results-only run of a files group.
func TestGroupBehaviorsE2E_ResultsAndFilesChildWorkflowFailsLaunch(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-files-parent": filesParentManifest,
		"grp-files-child":  filesChildManifest,
	})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-parent")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "g")
	assert.Equal(t, "failure", out["outcome"])
	reason, _ := out["reason"].(string)
	assert.True(t, strings.Contains(reason, "launch failed"), "reason %q must record the launch failure", reason)
	assert.Contains(t, reason, "file gathering is not available yet", "the cause is the Phase 2 support guard")
	f := groupTaskByKey(t, out, "f")
	assert.Equal(t, "unfinished", f["status"], "a task that never launched is unfinished")
	failure, ok := f["failure"].(map[string]any)
	require.True(t, ok, "a never-launched task records why")
	assert.Equal(t, "not_started", failure["state"])
	assert.True(t, strings.HasPrefix(fmt.Sprint(failure["error"]), "not launched: "), "got %v", failure["error"])
	assert.Empty(t, childrenOf(t, ds, parentID), "the refused child workflow must never be created")
}

// stateVar reads one persisted variable of an instance.
func stateVar(t *testing.T, ds *domainStack, id uuid.UUID, key string) any {
	t.Helper()
	return parentVariables(t, ds, id)[key]
}
```

- [x] Step 2: Run the suite. P2-T7…P2-T14 already implement every behaviour, so this run is EXPECTED TO PASS; any failure is a defect in the producer task the **Wiring & data provenance** table names for that test, to be fixed there (with a unit test in that task's package) before continuing.

```bash
go test ./cmd/arbitron/ -run 'TestGroupBehaviorsE2E' -v -count=1
```
Expected:
```
--- PASS: TestGroupBehaviorsE2E_FirstSuccessCancelUnfinished
--- PASS: TestGroupBehaviorsE2E_FirstSuccessFinishIndependently (5.XXs)
--- PASS: TestGroupBehaviorsE2E_FirstSuccessEveryCandidateFails
--- PASS: TestGroupBehaviorsE2E_FirstSuccessCancelledTaskNeverWins (3.XXs)
--- PASS: TestGroupBehaviorsE2E_AllSucceedCancelUnfinishedFailsFast
--- PASS: TestGroupBehaviorsE2E_AllSucceedWaitForRemaining (5.XXs)
--- PASS: TestGroupBehaviorsE2E_NestedBranchesInsideWorkflowChild
--- PASS: TestGroupBehaviorsE2E_NestedWorkflowsInsideBranch
--- PASS: TestGroupBehaviorsE2E_PerUserCapOneDoesNotDeadlock
--- PASS: TestGroupBehaviorsE2E_ParentCancellationCascadesThroughNestedGroups (2.XXs)
--- PASS: TestGroupBehaviorsE2E_CancellingCompletedParentStopsIndependentTask
--- PASS: TestGroupBehaviorsE2E_CancelReachesIndependentTaskBelowCompletedBranch
--- PASS: TestGroupBehaviorsE2E_RestartQueuedTaskStillWinsAfterCrash (X.XXs)
--- PASS: TestGroupBehaviorsE2E_ResultsAndFilesRefusedAtTrigger
--- PASS: TestGroupBehaviorsE2E_ResultsAndFilesChildWorkflowFailsLaunch
ok  	github.com/arbitron/arbitron/cmd/arbitron	XX.XXXs
```
All scheduling preconditions are asserted behind bounded external process gates; neither restart nor cancellation may skip due to scheduling. The gates replace the legacy sleep-based windows in the illustrative code above.

- [x] Step 3: Red check — because these tests are written after the behaviour exists, prove each can fail by breaking the producer it guards (do NOT commit; revert each before the next):

```bash
# (1) In cmd/arbitron/main.go comment out `eng.SetGroupTerminalHandler(groupCoord)`:
go test ./cmd/arbitron/ -run 'TestGroupBehaviorsE2E_FirstSuccessCancelUnfinished|TestGroupBehaviorsE2E_AllSucceedCancelUnfinishedFailsFast' -v -count=1
# Expected: --- FAIL ... never reached a terminal state / "cancel_unfinished must take the failure port as soon as one task fails"
# (2) Revert. In domain/engine/engine.go:Cancel change `if ownErr != nil && stopped == 0 {` to `if ownErr != nil {`:
go test ./cmd/arbitron/ -run 'TestGroupBehaviorsE2E_CancellingCompletedParentStopsIndependentTask' -v -count=1
# Expected: --- FAIL ... cancelling a completed parent with independent work must succeed
# (3) Revert. In domain/engine/cancel_cascade.go:cascadeCancel make the loop skip terminal children
#     (insert `if child.State.IsTerminal() { continue }` as the loop's first statement), i.e. the
#     pre-P2-D ListActiveChildren behaviour:
go test ./cmd/arbitron/ -run 'TestGroupBehaviorsE2E_CancelReachesIndependentTaskBelowCompletedBranch' -v -count=1
# Expected: --- FAIL ... Received unexpected error: ... cannot be cancelled (state: completed): instance cannot be cancelled
#     (the completed "mid" branch is never traversed, so nothing below it is reached)
# Revert.
git status --short   # must show only cmd/arbitron/group_behaviors_e2e_test.go
```

Mutation evidence: all three requested regressions failed at the intended persisted-state/API sinks, then passed after restoring the producer. Handler removal caused both cancellation outcomes to time out; terminal-parent error regression returned `ErrCannotCancel`; skipping terminal intermediates returned `ErrCannotCancel` with the nested loser still gated. The terminal-parent mutation also needs `_ = stopped` to compile (not retained). No mutation remains in production files.

Self-review found that terminal receipts precede terminal callbacks, so late-result/cancellation assertions now drain the real engine before inspecting the final persisted sinks. This stronger `-race -count=5` gate exposed a genuine producer defect: a newly committed queued child could be started by `Evaluate` before `createAndEnqueue` reached `Enqueue`, which then started a duplicate walker and overwrote its cancellation context. Bounded drain checks repeatedly failed with a still-gated cancelled process; temporary diagnostic logs confirmed two starts for the same child id (diagnostics removed).

Producer TDD: added real SQLite/queue tests that deterministically run `Evaluate` between persistence and `Enqueue`. Red showed duplicate starts, resurrection of completed/failed/cancelled/waiting/paused rows, and starting nonexistent rows. Minimal fix: under the queue's existing shared mutex, `Enqueue` reads the persisted state and starts only a still-queued row; an already-running row reports started without relaunching. No new port, option, or lifecycle state. Updated four queue fixtures to persist their load-bearing queued rows; error assertions now require their actual capacity/start failures. Focused unit tests, the whole queue race suite, and all 15 root behaviour tests with `-race -count=5` are green.

- [x] Step 4: Run the whole `cmd/arbitron` package (catches helper-name collisions with other `_e2e_test.go` files), vet, gofmt, commit.

```bash
go test ./cmd/arbitron/ -count=1
gofmt -l cmd/arbitron/group_behaviors_e2e_test.go
go vet ./cmd/arbitron/
git add cmd/arbitron/group_behaviors_e2e_test.go
git commit -m "test(arbitron): end-to-end parallel group behaviours through the composition root" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `ok  	github.com/arbitron/arbitron/cmd/arbitron`; `gofmt -l` and `go vet` print nothing.

Completion self-review (in order):
1. Spec compliance: all 15 planned root behaviours, both nesting directions, cap-one progress, three subtree cancellation shapes, restart with a truly queued candidate, and both file-mode refusal paths are covered. All 18 manifests remain test-only. Only the producer defect exposed by this task required a production change; no new feature, flag, port, or mode enablement was added. The unfinished-result assertion follows P2-T9's existing empty-map contract.
2. Anti-green-theater: real `initDomain`, repositories, queue, shell/transform producers and terminal callbacks feed exact nonempty persisted results, frozen parent variables, child/live statuses and once-only traces. The three required wiring/cascade mutations fail at those sinks. Late-result/cancellation checks drain actual walkers/callbacks before negative assertions, not merely observing an intermediate atomic receipt. That strengthening exposed and now guards duplicate admission. The new queue regression uses real SQLite and real queue evaluation with an id-addressed persisted-state starter; red/green was observed before implementation.
3. Code quality: followed adjacent composition-test/gate helpers and the queue's shared-mutex convention, reused bounded external process gates with cleanup release ordering, retained actionable storage errors and existing result shapes, and removed every temporary mutation/diagnostic. No timing-based skips, ignored load-bearing argument, or missing-data no-op was introduced.

Final verification after review:
- `go test ./cmd/arbitron/ ./domain/engine/ ./domain/queue/ -count=1` — green.
- `go test -race ./cmd/arbitron/ -run '^TestGroupBehaviorsE2E_' -count=5` — green (all 75 cases).
- `go test -race ./domain/queue/ -count=1` — green.
- `go test -race ./domain/queue/ ./cmd/arbitron/ -run 'TestEnqueue|TestGroupBehaviorsE2E' -count=1` — green after final review.
- `go vet ./cmd/arbitron/ ./domain/queue/`, `gofmt -l` for changed Go files, and `git diff --check` — clean.

Commit includes this plan record, the root behaviour tests and the minimal queue producer regression/fix; no other task was started.
