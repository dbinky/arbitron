# Parallel Workflow Gathering — Phase 2 Task 14: Composition-root wiring and end-to-end results gathering

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make Phase 2 live: construct the group-run repository, group workspace, coordinator, launcher and `parallel_group` executor in `cmd/arbitron/main.go:initDomain`, route group-task terminals to the coordinator, enable `results` execution, reconcile groups at startup and on a ticker — and prove every one of those lines through the real composition root with both group forms.
**Architecture:** All new wiring sits next to the existing spawn wiring: the group objects are built right after the `spawn_workflow` executor (so they are registered before `FreezeExecutors`), the startup `Reconcile` runs right after `RecoverCrashedInstances` and the spawn-wait reconciler (so crashed tasks are already terminal), and a sibling ticker goroutine (`sweepGroupReconciler`) re-runs it with its own cancel func stopped in `domainStack.Close` before the DB closes. Tests live in `package main` and boot the real stack with `initDomain`.
**Tech Stack:** Go 1.25.5, SQLite, real `sh` tool_call nodes for timing and file I/O, testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (Parallel groups; Completion rules — all succeed; Gathering results); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §4 "Register `NodeTypeParallelGroup` before `FreezeExecutors`; enable results execution only when all Phase 2 dependencies are wired", §3 "a new group sweep alongside `main.go:sweepSpawnReconcilers`", §Integration "wire repository/launcher/coordinator/executor/reconciler in `cmd/arbitron/main.go`".

## Additional prerequisite and terminal-order sink proof

P2-T16 (atomic terminal receipts) and P2-T17 (results workspace metadata isolation) must
finish before this wiring task enables `GroupSupportResults`. The production producer is
`RunMigrations(db)` installing the terminal triggers on the SAME SQLite database used by
`instanceRepo` and `groupRunRepo`; no optional setter can disable terminal capture.

Add/run red `TestGroupE2E_FirstSuccessUsesCommitOrderWithinOneTimestamp`: boot
`initDomain`, launch a first-success group, hold both task processes at test-controlled
gates, commit task b completed before task a while their stored timestamps are equal,
delay/reverse their callbacks, then resume the coordinator. Assert the downstream
persisted variable resolves winner b, not merely that a roster has two sequence numbers.
Drop the trigger in a second booted fixture: the task-terminal/receipt assertion must fail
or the adapter must return a missing-receipt error; it must never synthesize author order.
Also prove a cancelled task cannot win and a trigger/storage failure cannot publish success.

For restart tests, replace elapsed-time SKIP branches with bounded process gates: confirm
the child reached the gate before closing/rebooting the stack, and release it during
cleanup. Test-runner overload must cause a useful timeout failure, never an accepted skip.


**Interfaces**
- Consumes (contract names; produced by earlier tasks):
  - P2-T3 `adaptsqlite.NewGroupRunRepo(db *sql.DB) *adaptsqlite.GroupRunRepo` implementing `instance.GroupRunRepository` (`GetByParentNode`, `CreateWithTasks`, …).
  - P2-T4 `(*adaptsqlite.QueueRepo).SetQueuedIfWaitingAtNode` ⇒ `*adaptsqlite.QueueRepo` satisfies `instance.NodeAddressedWaker`.
  - P2-T5 `filesystem.NewGroupWorkspace(root string) (*filesystem.GroupWorkspaceAdapter, error)` implementing `engine.GroupWorkspace`.
  - P2-T7 `(*engine.Engine).StartGroupTask` ⇒ `*Engine` satisfies `engine.GroupTaskStarter`; `*Engine` already satisfies `engine.ChildWorkflowReader` (`ChildInputs`, `domain/engine/engine_spawn.go`) and `engine.RunCanceller` (`Cancel`).
  - P2-T8 `engine.NewGroupLauncher(groups instance.GroupRunRepository, starter GroupTaskStarter, contracts ChildWorkflowReader, workspaces WorkspaceManager, groupWS GroupWorkspace, identities IdentityResolver, canceller RunCanceller, bus EventBus) *GroupLauncher`.
  - P2-T10 `engine.NewGroupCoordinator(groups instance.GroupRunRepository, instances instance.InstanceRepository, waker instance.NodeAddressedWaker, canceller RunCanceller, identities IdentityResolver, bus EventBus, queueMgr QueueManager) *GroupCoordinator`; `(*GroupCoordinator).Reconcile(ctx) error`; `(*GroupCoordinator).OnTaskTerminal`; event constants `engine.EventTypeGroupLaunched`, `engine.EventTypeGroupDecided` (`domain/engine/group_events.go`), published on the PARENT id with `Data` map keys `"groupRunId"`, `"nodeId"`.
  - P2-T11 `engine.NewParallelGroupExecutor(instances instance.InstanceRepository, nodeExecs instance.NodeExecutionRepository, groups instance.GroupRunRepository, launcher *GroupLauncher, coord *GroupCoordinator, claims instance.SpawnWaitRepository, queueMgr QueueManager) *ParallelGroupExecutor`.
  - P2-T13 `(*engine.Engine).SetGroupTerminalHandler(h engine.GroupTerminalHandler)`.
  - P1-T1 `workflow.NodeTypeParallelGroup`; P1-T9 `(*engine.Engine).SetGroupSupport(engine.GroupSupport)`, `GroupSupportLevel()`, `engine.GroupSupportResults`, `engine.ErrGroupExecutionUnsupported`.
  - P2-T1 `instance.GroupRun`, `instance.GroupTask`, `instance.GroupRunRunning`, `instance.GroupTaskKindBranch`, `instance.GroupTaskKindWorkflow`, `instance.GroupTaskLaunched`, `instance.GroupTaskStatusRunning`, `instance.SpawnModeGroupTask`, `Instance.GroupRunID/GroupTaskKey/GroupTaskKind`.
  - Existing composition-root locals in `initDomain`: `db` (`*sql.DB`), `cfg`, `instanceRepo` (`*adaptsqlite.InstanceRepo`, also `instance.SpawnWaitRepository`), `nodeExecRepo`, `queueRepo` (`*adaptsqlite.QueueRepo`, main.go:796), `eng` (main.go:799), `wsMgr` (main.go:813), `queueMgr` (main.go:829), `bus` (eventbus), `identityResolver` (`userIdentityResolver`, main.go:921), `logger`.
  - P1-T8 test helpers (`cmd/arbitron/group_authoring_e2e_test.go`): `bootGroupAuthoringStack`, `filesModeGroup`, `newGroupMCPServer`, `callMCPTool`, `mcpText`; P1-T9's `instancesNamed` and `TestGroupTriggerE2E_Phase1DefaultRefusesEveryGroupWorkflow` (`cmd/arbitron/group_trigger_e2e_test.go`), the latter replaced in Step 3.
  - Existing test helpers in `package main`: `writeWorkflow` (spawn_e2e_test.go:22), `adminCtxForLocalUser` (spawn_e2e_test.go:87), `childrenOf` (spawn_e2e_test.go:99), `localDefaultUser` (spawn_e2e_test.go:19), `waitForState` and `branchVariable` (spawn_wait_e2e_test.go:131/140).
- Produces:
  - `cmd/arbitron/main.go`: `var groupSweepInterval = 1 * time.Minute`; `type groupReconciler interface { Reconcile(ctx context.Context) error }`; `func sweepGroupReconciler(ctx context.Context, groups groupReconciler, interval time.Duration, log *slog.Logger)`; `domainStack` fields `groupRunRepo instance.GroupRunRepository`, `groupSweepCancel context.CancelFunc`.
  - Test helpers in `cmd/arbitron/group_e2e_test.go` (consumed by P2-T15): `type groupStackPaths struct{ root, workspaceDir, configPath string }`; `writeGroupStack(t, perUserCap int, manifests map[string]string) groupStackPaths`; `bootGroupExecStack(t, perUserCap int, manifests map[string]string) (*domainStack, groupStackPaths)`; `triggerGroupWorkflow(t, ds, name string) uuid.UUID`; `waitForTerminalState(t, ds, id) instance.State`; `parentVariables(t, ds, id) map[string]any`; `groupOutputOf(t, ds, parentID, nodeID) map[string]any`; `groupTaskAt(t, out, i) map[string]any`; `groupTaskByKey(t, out, key) map[string]any`; `resultOf(t, task) map[string]any`; `nodeExecutionCount(t, ds, id, nodeID) int`; `groupRunOf(t, ds, parentID, nodeID) *instance.GroupRun`; `groupRosterTask(t, run, key) instance.GroupTask`; manifest constants `groupBranchesManifest`, `groupWorkflowsManifest`, `groupEchoChildManifest`, `groupEntryManifest`, `groupCrashManifest`.

**Wiring & data provenance**
- This IS the composition-root task for P2-T3/T4/T5/T7/T8/T10/T11/T13 and P1-T9's support level. Exact lines added (all in `initDomain`):
  1. `groupRunRepo := adaptsqlite.NewGroupRunRepo(db)` — producer of every group-run read/write; injected into the coordinator, launcher, executor, and `domainStack.groupRunRepo`.
  2. `groupWS, err := filesystem.NewGroupWorkspace(cfg.Workspace.Path)` — same root `wsMgr` uses, so snapshots live under `<workspace>/.group-snapshots/<runID>` next to `<workspace>/instances/<id>`.
  3. `groupCoord := engine.NewGroupCoordinator(groupRunRepo, instanceRepo, queueRepo, eng, identityResolver, bus, queueMgr)` — `queueRepo` is the node-addressed waker, `eng` the authorized canceller, `identityResolver` the SAME value the spawn executor / schedule evaluator / orphan sweep use.
  4. `groupLauncher := engine.NewGroupLauncher(groupRunRepo, eng, eng, wsMgr, groupWS, identityResolver, eng, bus)` — `eng` as starter, child-contract reader, and canceller; `wsMgr` reserves each task's `<workspace>/instances/<taskID>` before fork.
  5. `groupExecutor := engine.NewParallelGroupExecutor(instanceRepo, nodeExecRepo, groupRunRepo, groupLauncher, groupCoord, instanceRepo, queueMgr)` + `eng.RegisterExecutor(workflow.NodeTypeParallelGroup, groupExecutor)` (the local `groupExecutor` is what P3-T11 calls `SetGatherer` on) — BEFORE `eng.FreezeExecutors()` (main.go:1071); `instanceRepo` second time is the `SpawnWaitRepository.ClaimWaitingParent` claimer.
  6. `eng.SetGroupTerminalHandler(groupCoord)` — group-task terminals from `OnChildTerminal` (and the queue webhook-timeout hook, main.go:859) reach the coordinator.
  7. `eng.SetGroupSupport(engine.GroupSupportResults)` — the trigger guard (P1-T9) admits `results` groups; `results_and_files` stays refused with `ErrGroupExecutionUnsupported`.
  8. Startup `groupCoord.Reconcile(context.Background())` — AFTER `eng.RecoverCrashedInstances()` (crashed tasks/parents already `failed`) and AFTER `spawnReconciler.Reconcile`, BEFORE queue timer/webhook recovery starts the queue.
  9. `go sweepGroupReconciler(groupSweepCtx, groupCoord, groupSweepInterval, logger)` with `groupSweepCancel` in `domainStack.Close` and in both later boot-failure paths.
- Which e2e assertion fails when each line is deleted (each test boots the real stack):

| Deleted line | Failing assertion |
|---|---|
| 5 `RegisterExecutor(NodeTypeParallelGroup, …)` | `TestGroupE2E_BranchesAllSucceedGatherResultsInAuthorOrder`, `…WorkflowsForm…`, `…EntrypointGroup…`: `require.Equal(t, instance.StateCompleted, waitForTerminalState(…))` sees `failed` (run loop: "no executor registered for node type: parallel_group"). |
| 6 `SetGroupTerminalHandler(groupCoord)` | Same three tests: tasks complete but nothing decides in-process, the parent stays `waiting`, and `waitForTerminalState` times out at 40s — sooner than the first 60s sweep tick. `…BranchesAllSucceed…` additionally asserts the decision landed within 10s of the last task's completion. |
| 7 `SetGroupSupport(GroupSupportResults)` | Every `triggerGroupWorkflow` (`require.NoError` on `Trigger`) fails with `ErrGroupExecutionUnsupported`; `TestGroupTriggerE2E_Phase2RunsResultsGroupsAndRefusesFileGathering` asserts `GroupSupportLevel() == GroupSupportResults` and runs `results-group` directly, over MCP, and as a spawned child. |
| 8 startup `groupCoord.Reconcile` | `TestGroupE2E_StartupReconcileSettlesGroupAfterCrash`: after the restart the parent stays `waiting` past 40s (the sweep's first tick is 60s away). |
| 9 `go sweepGroupReconciler(…)` | `TestGroupE2E_PeriodicSweepSettlesGroupWithoutAWake` (sweep shortened to 1s): a group whose only task is already terminal, created after boot with no in-process wake, is never settled. |
| `bus` argument replaced in 3/4 | `TestGroupE2E_PublishesGroupLifecycleEventsOnTheParent`. |
| `groupRunRepo: groupRunRepo` in the `domainStack` literal | every test using `groupRunOf` fails its `require.NotNil(t, ds.groupRunRepo)`. |
| 1–4 | compile errors (the values are used by 5/6/8/9). |

### Task 14: Wire parallel groups into the composition root

**Files:**
- Modify: `cmd/arbitron/main.go` — const block lines 56-72 (add `groupSweepInterval` var after it); after `sweepSpawnReconcilers` (ends line 100) add `groupReconciler` + `sweepGroupReconciler`; `domainStack` fields after line 230; `Close` after lines 270-272; group wiring after the `spawn_workflow` registration (lines 940-949); startup reconcile after lines 1109-1113; sweep goroutine after line 1136; `groupSweepCancel()` in the boot-failure paths at lines 1141 and 1153; `domainStack` literal lines 1192-1236.
- Modify: `cmd/arbitron/group_trigger_e2e_test.go` (created by P1-T9) — replace `TestGroupTriggerE2E_Phase1DefaultRefusesEveryGroupWorkflow` with its Phase 2 version.
- Create: `cmd/arbitron/group_e2e_test.go`
- Create: `cmd/arbitron/group_sweep_test.go`

- [x] Step 1: Write the failing sweep unit test. Create `cmd/arbitron/group_sweep_test.go`:

Selection: P2-T11/T13's remaining root-proof gates cannot complete without this task. P2-T16's atomic terminal producer and P2-T17's metadata isolation helper now exist; their root-proof steps depend on this wiring rather than missing production dependencies. This pass resumes T14.

```go
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coordinator is what main.go hands the sweep.
var _ groupReconciler = (*engine.GroupCoordinator)(nil)

type countingGroupReconciler struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingGroupReconciler) Reconcile(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.err
}

func (c *countingGroupReconciler) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSweepGroupReconciler_RunsOnEveryTickAndStopsOnCancel(t *testing.T) {
	rec := &countingGroupReconciler{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sweepGroupReconciler(ctx, rec, 10*time.Millisecond, discardLogger())
		close(done)
	}()

	require.Eventually(t, func() bool { return rec.count() >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep did not stop when its context was cancelled")
	}
	n := rec.count()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, n, rec.count(), "no reconciliation may run after cancellation")
}

// A failing pass (a transient DB error) is logged and the loop keeps going.
func TestSweepGroupReconciler_ErrorDoesNotStopTheLoop(t *testing.T) {
	rec := &countingGroupReconciler{err: errors.New("database is locked")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sweepGroupReconciler(ctx, rec, 10*time.Millisecond, discardLogger())

	require.Eventually(t, func() bool { return rec.count() >= 3 }, 2*time.Second, 5*time.Millisecond)
}

// The startup pass is main.go's explicit Reconcile call; the ticker's first run
// is one interval later.
func TestSweepGroupReconciler_FirstRunWaitsOneInterval(t *testing.T) {
	rec := &countingGroupReconciler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sweepGroupReconciler(ctx, rec, time.Hour, discardLogger())

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, rec.count())
}
```

- [x] Step 2: Write the failing composition-root e2e tests. Create `cmd/arbitron/group_e2e_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
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

// groupCrashManifest: all_succeed + wait_for_remaining with a quick task and a
// 4s task. 4s keeps "slow" observably running while the test simulates the
// crash, and is short enough not to matter if it ever does finish.
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
     "config": {"command": "sh", "args": ["-c", "sleep 4; echo '{}'"]}},
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
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 10\n  perusercap: " + strconv.Itoa(perUserCap) + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	return groupStackPaths{root: root, workspaceDir: workspaceDir, configPath: configPath}
}

// bootGroupExecStack boots the REAL composition root against the manifests.
func bootGroupExecStack(t *testing.T, perUserCap int, manifests map[string]string) (*domainStack, groupStackPaths) {
	t.Helper()
	paths := writeGroupStack(t, perUserCap, manifests)
	ds, err := initDomain(context.Background(), paths.configPath)
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
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-branches": groupBranchesManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-branches")
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

	run := groupRunOf(t, ds, parentID, "group-1")
	ta, tb := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	require.NotNil(t, ta.TerminalSeq)
	require.NotNil(t, tb.TerminalSeq)
	assert.Less(t, *tb.TerminalSeq, *ta.TerminalSeq,
		"b (1s) finishes before a (2s), yet the gathered tasks above are in author order")

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
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-workflows":  groupWorkflowsManifest,
		"grp-echo-child": groupEchoChildManifest,
	})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-workflows")
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
	assert.Less(t, *t2.TerminalSeq, *t1.TerminalSeq, "second (0s) finishes before first (2s)")
}

// EDGE: the group is the workflow's entrypoint and has a single task.
func TestGroupE2E_EntrypointGroupWithSingleTask(t *testing.T) {
	ds, _ := bootGroupExecStack(t, 5, map[string]string{"grp-entry": groupEntryManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-entry")
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
	paths := writeGroupStack(t, 5, map[string]string{"grp-crash": groupCrashManifest})
	ctx := context.Background()

	ds1, err := initDomain(ctx, paths.configPath)
	require.NoError(t, err)
	parentID := triggerGroupWorkflow(t, ds1, "grp-crash")
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

	ds2, err := initDomain(ctx, paths.configPath)
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
		ID: parentID, OwnerID: localDefaultUser, WorkflowName: "grp-entry", WorkflowSnapshot: snapshot,
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
			ChildInstanceID: childID, LaunchState: instance.GroupTaskLaunched,
			LiveStatus: instance.GroupTaskStatusRunning, Blocking: true,
		}},
	}))

	pid, node, rid, key := parentID, "group-1", runID, "only"
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: childID, OwnerID: localDefaultUser, WorkflowName: "grp-entry", WorkflowSnapshot: snapshot,
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
```

- [x] Step 3: Update the Phase 1 refusal e2e. P1-T9 created `cmd/arbitron/group_trigger_e2e_test.go` with `TestGroupTriggerE2E_Phase1DefaultRefusesEveryGroupWorkflow`, whose comment says "P2-T14 MUST UPDATE THIS TEST": it asserts `GroupSupportLevel() == GroupSupportNone` and that `results-group`, the MCP `instance_trigger` of `results-group`, and a spawned `results-group` are all REFUSED — every one of those becomes false once this task wires `GroupSupportResults`. Replace that whole function (keep the file's imports and its `instancesNamed` helper unchanged; it uses P1-T8's `bootGroupAuthoringStack`, `filesModeGroup`, `newGroupMCPServer`, `callMCPTool`, `mcpText` from `cmd/arbitron/group_authoring_e2e_test.go`) with:

```go
// PHASE 2 EXPECTATION (P2-T14 update of P1-T9's Phase 1 version): the
// composition root wires eng.SetGroupSupport(engine.GroupSupportResults), so
// "results-group" — triggered directly, over MCP, or spawned by a legacy
// spawn_workflow node — now CREATES and RUNS instances, while "files-group"
// keeps being refused with the file-gathering message until P3-T11 wires
// GroupSupportResultsAndFiles and updates this test again.
//
// PROVENANCE PROBE: remove eng.SetGroupSupport(engine.GroupSupportResults)
// from initDomain and the level assertion plus every results subtest fail;
// remove the checkGroupSupport call from TriggerWithDepartment and the
// files-group subtest fails (a row is created for a group nothing can gather).
func TestGroupTriggerE2E_Phase2RunsResultsGroupsAndRefusesFileGathering(t *testing.T) {
	ds, _ := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.Equal(t, engine.GroupSupportResults, ds.eng.GroupSupportLevel(),
		"the Phase 2 composition root enables results-mode execution")

	results := filesModeGroup("results-group", "")
	results.Nodes[0].Config.Group.GatherMode = workflow.GroupGatherResults
	require.NoError(t, ds.workflowSvc.Create(ctx, "results-group", results))
	require.NoError(t, ds.workflowSvc.Create(ctx, "files-group", filesModeGroup("files-group", "default-agent")))

	t.Run("results-group runs", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "results-group", map[string]any{}, "normal")
		require.NoError(t, err)
		waitForState(t, ds, id, instance.StateCompleted)
		inst, err := ds.instanceRepo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, true, inst.Variables["done"], "the group's SUCCESS target ran")
		for _, k := range []string{"failed", "a1", "b1"} {
			_, present := inst.Variables[k]
			assert.False(t, present, "%q must not be in the parent (failure port not taken; branch variables never merged)", k)
		}
	})

	t.Run("files-group still refused", func(t *testing.T) {
		id, started, err := ds.eng.Trigger(ctx, "files-group", map[string]any{}, "normal")
		require.Error(t, err)
		assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported), "got %v", err)
		assert.Contains(t, err.Error(), `node "fanout"`)
		assert.Contains(t, err.Error(), `gatherMode "results_and_files"`)
		assert.Contains(t, err.Error(), "file gathering is not available yet")
		assert.Equal(t, uuid.Nil, id)
		assert.False(t, started)
		assert.Empty(t, instancesNamed(t, ds, "files-group"), "no instance row may be created")
	})

	t.Run("instance_trigger over MCP runs results-group", func(t *testing.T) {
		srv := newGroupMCPServer(ds)
		res := callMCPTool(t, srv, ctx, "instance_trigger", map[string]any{"workflowName": "results-group"})
		require.False(t, res.IsError, mcpText(t, res))
		assert.Len(t, instancesNamed(t, ds, "results-group"), 2, "the direct run above plus this MCP run")
	})

	t.Run("spawned-results-group runs as a fire-and-forget child", func(t *testing.T) {
		parent := &workflow.Workflow{
			Name: "spawns-group", Description: "spawns a group workflow", Author: "e2e",
			Nodes: []workflow.Node{
				{ID: "p1", Type: workflow.NodeTypeTransform, Entrypoint: true, Config: workflow.NodeConfig{
					Operations: []workflow.Operation{{Variable: "started", Operation: "set", Value: true}}}},
				{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
					ChildWorkflow: "results-group", SpawnMode: "fire_and_forget"}},
			},
			Edges: []workflow.Edge{{Source: "p1", Target: "spawn-1"}},
		}
		require.NoError(t, ds.workflowSvc.Create(ctx, "spawns-group", parent))

		parentID, _, err := ds.eng.Trigger(ctx, "spawns-group", map[string]any{}, "normal")
		require.NoError(t, err)
		waitForState(t, ds, parentID, instance.StateCompleted)
		var kids []instance.InstanceSummary
		require.Eventually(t, func() bool {
			kids = childrenOf(t, ds, parentID)
			return len(kids) == 1
		}, 40*time.Second, 100*time.Millisecond, "the spawn path now admits a results-mode group child")
		assert.Equal(t, "results-group", kids[0].WorkflowName)
		waitForState(t, ds, kids[0].ID, instance.StateCompleted)
	})

	t.Run("non-group workflow still triggers", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "group-child", map[string]any{"topic": "x"}, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, gerr := ds.instanceRepo.Get(context.Background(), id)
			return gerr == nil && inst.State == instance.StateCompleted
		}, 20*time.Second, 100*time.Millisecond)
	})
}
```

The file's existing imports (`context`, `errors`, `testing`, `time`, `uuid`, `assert`, `require`, `engine`, `instance`, `workflow`) are all still used; nothing else in the file changes.

- [x] Step 4: Run, confirm they fail.

Confirmed the expected compile errors for the missing sweep, `domainStack.groupRunRepo` and interval before touching production wiring.

```bash
go test ./cmd/arbitron/ -run 'TestSweepGroupReconciler|TestGroupE2E|TestGroupTriggerE2E' -v -count=1
```
Expected (compile failure — the sweep and the new `domainStack` fields do not exist yet):
```
# github.com/arbitron/arbitron/cmd/arbitron [github.com/arbitron/arbitron/cmd/arbitron.test]
cmd/arbitron/group_sweep_test.go:17:7: undefined: groupReconciler
cmd/arbitron/group_sweep_test.go:47:3: undefined: sweepGroupReconciler
cmd/arbitron/group_e2e_test.go:...: ds.groupRunRepo undefined (type *domainStack has no field or method groupRunRepo)
cmd/arbitron/group_e2e_test.go:...: undefined: groupSweepInterval
FAIL	github.com/arbitron/arbitron/cmd/arbitron [build failed]
```

- [x] Step 5: Implement the wiring in `cmd/arbitron/main.go`.

(a) Immediately after the `const (...)` block that ends after `spawnSweepInterval = 1 * time.Minute` (line 72), add:

```go
// groupSweepInterval is how often the parallel-group reconciler re-runs after
// its startup pass. Like the spawn sweeps, its holes open in a HEALTHY process
// too: a task terminal whose in-process hop failed (a storage error between
// the task's terminal write and the coordinator's intake), or a crash between
// a group's decision and its cancel/wake actions. Each pass is one indexed
// query over unsettled runs that usually matches nothing.
//
// It is a var, not a const, ONLY so the composition-root test can shorten it
// before initDomain reads it; production code never assigns it.
var groupSweepInterval = 1 * time.Minute
```

(b) Immediately after `sweepSpawnReconcilers` (after line 100), add:

```go
// groupReconciler is the one operation the periodic group sweep needs.
// *engine.GroupCoordinator satisfies it.
type groupReconciler interface {
	Reconcile(ctx context.Context) error
}

// sweepGroupReconciler re-runs the parallel-group reconciler on a ticker until
// ctx is cancelled. The first run is one interval after start — the startup
// pass is initDomain's explicit Reconcile call. Errors are logged, not fatal:
// a transient DB error on one tick must not kill the loop. Mirrors
// sweepSpawnReconcilers.
func sweepGroupReconciler(ctx context.Context, groups groupReconciler, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := groups.Reconcile(ctx); err != nil {
				log.Error("parallel-group reconciliation incomplete", "error", err)
			}
		}
	}
}
```

(c) In `type domainStack struct`, immediately after `spawnSweepCancel context.CancelFunc` (line 230), add:

```go
	// groupSweepCancel stops the parallel-group reconciliation ticker. It is
	// stopped BEFORE the DB closes, like spawnSweepCancel.
	groupSweepCancel context.CancelFunc
	// groupRunRepo is the durable parallel-group store, exposed so
	// composition-root tests can assert on persisted group state.
	groupRunRepo instance.GroupRunRepository
```

(d) In `Close`, immediately after the `spawnSweepCancel` block (lines 270-272), add:

```go
	if ds.groupSweepCancel != nil {
		ds.groupSweepCancel()
	}
```

(e) Immediately after the `spawn_workflow` registration (after line 949, the closing `))` of `eng.RegisterExecutor(workflow.NodeTypeSpawnWorkflow, ...)`), add:

```go
	// 12c-group. Wire parallel groups (Phase 2: results gather mode).
	//
	// Everything is built here, BEFORE FreezeExecutors, for the same reason the
	// spawn executor is: the executor and coordinator depend on the engine
	// itself (starter, child-contract reader, authorized canceller). The group
	// run repository is the durable authority; the in-process terminal hop
	// (SetGroupTerminalHandler) and the reconciler (startup + ticker below)
	// both drive the same coordinator. The SAME identityResolver value the spawn
	// executor, schedule evaluator and orphan sweep use reconstructs the
	// parent owner's identity for task creation and cancellation.
	groupRunRepo := adaptsqlite.NewGroupRunRepo(db)
	groupWS, err := filesystem.NewGroupWorkspace(cfg.Workspace.Path)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("group workspace: %w", err)
	}
	groupCoord := engine.NewGroupCoordinator(
		groupRunRepo,
		instanceRepo,
		queueRepo, // *adaptsqlite.QueueRepo is the node-addressed waker
		eng,       // authorized RunCanceller
		identityResolver,
		bus,
		queueMgr,
	)
	groupLauncher := engine.NewGroupLauncher(
		groupRunRepo,
		eng, // GroupTaskStarter
		eng, // ChildWorkflowReader
		wsMgr,
		groupWS,
		identityResolver,
		eng, // RunCanceller (partial-launch cleanup)
		bus,
	)
	// Bound to a local (not built inline) so Phase 3 (P3-T11) can call
	// groupExecutor.SetGatherer(...) on this same value before FreezeExecutors.
	groupExecutor := engine.NewParallelGroupExecutor(
		instanceRepo,
		nodeExecRepo,
		groupRunRepo,
		groupLauncher,
		groupCoord,
		instanceRepo, // *adaptsqlite.InstanceRepo also satisfies instance.SpawnWaitRepository (ClaimWaitingParent)
		queueMgr,
	)
	eng.RegisterExecutor(workflow.NodeTypeParallelGroup, groupExecutor)
	// Group-task terminals (run completion, failure, cancellation, and the
	// queue's webhook-timeout hook wired at 12-wh-spawn) reach the coordinator.
	eng.SetGroupTerminalHandler(groupCoord)
	// Enable results-mode execution only now that every Phase 2 dependency is
	// wired; results_and_files stays refused until Phase 3.
	eng.SetGroupSupport(engine.GroupSupportResults)
```

Then search for any other support-level call and keep exactly one:
```bash
grep -n "SetGroupSupport" cmd/arbitron/main.go
```
If a line `eng.SetGroupSupport(engine.GroupSupportNone)` exists (an explicit Phase 1 default), delete it; the only remaining call must be the `GroupSupportResults` line above.

(f) Immediately after the spawn-wait startup reconcile (after line 1113, the closing `}` of `if err := spawnReconciler.Reconcile(...)`), add:

```go
	// 14-group. Reconcile parallel groups.
	//
	// ORDERING IS SIGNIFICANT, exactly as for the spawn reconciler: this runs
	// AFTER RecoverCrashedInstances, which marks crashed group TASKS (and
	// crashed group parents) failed without any in-process wake, so the
	// coordinator takes those terminals in, decides, cancels what a failed
	// parent left running, and resumes waiting parents in this same startup
	// pass. It runs BEFORE queue timer/webhook recovery starts the queue, so
	// queued tasks start against an already-reconciled group.
	//
	// A failure here is logged, NOT fatal; the ticker below retries.
	logger.Info("reconciling parallel groups")
	if err := groupCoord.Reconcile(context.Background()); err != nil {
		logger.Error("parallel-group reconciliation incomplete", "error", err)
	}
```

(g) Immediately after `go sweepSpawnReconcilers(spawnSweepCtx, spawnReconciler, orphanReconciler, spawnSweepInterval, logger)` (line 1136), add:

```go
	// 14-group-sweep. The group reconciler also runs CONTINUOUSLY: an
	// in-process task-terminal hop can fail in a healthy process, and a crash
	// between a decision and its cancel/wake actions leaves work only the
	// reconciler finishes. Its own cancel func is stopped in Close before the
	// DB closes.
	groupSweepCtx, groupSweepCancel := context.WithCancel(context.Background())
	go sweepGroupReconciler(groupSweepCtx, groupCoord, groupSweepInterval, logger)
```

(h) In the two boot-failure paths that follow (the `RecoverTimers` failure at line 1141 and the `RecoverWebhooks` failure at line 1153), add `groupSweepCancel()` on the line right after each `spawnSweepCancel()`:

```go
		spawnSweepCancel()
		groupSweepCancel()
```

(i) In the returned `&domainStack{...}` literal, immediately after `spawnSweepCancel:    spawnSweepCancel,` (line 1212), add:

```go
		groupSweepCancel:    groupSweepCancel,
		groupRunRepo:        groupRunRepo,
```

- [x] Step 6: Run, confirm everything passes.

Focused gates pass, including `group_provenance_e2e_test.go`: same-timestamp b-before-a commits with reversed real callbacks assert downstream `observedWinner == b`; missing-trigger and aborting-receipt fixtures refuse success; cancelled-to-success writes fail; both forms inherit real linked-worktree staged/dirty content and isolate commits/config/index from parent and sibling. Ordering, callback and restart preconditions use bounded external process gates, not elapsed-time skips. Updated the periodic fixture to use the repository's pending/not_started creation contract, and MCP counts only roots because branch task rows share the workflow name.

```bash
go build ./cmd/arbitron/
go test ./cmd/arbitron/ -run 'TestSweepGroupReconciler|TestGroupE2E|TestGroupTriggerE2E' -v -count=1
```
Expected:
```
--- PASS: TestSweepGroupReconciler_RunsOnEveryTickAndStopsOnCancel
--- PASS: TestSweepGroupReconciler_ErrorDoesNotStopTheLoop
--- PASS: TestSweepGroupReconciler_FirstRunWaitsOneInterval
--- PASS: TestGroupE2E_BranchesAllSucceedGatherResultsInAuthorOrder (3.XXs)
--- PASS: TestGroupE2E_WorkflowsFormRepeatsOneChildWithDistinctOrderedResults (2.XXs)
--- PASS: TestGroupE2E_EntrypointGroupWithSingleTask
--- PASS: TestGroupE2E_PublishesGroupLifecycleEventsOnTheParent
--- PASS: TestGroupE2E_StartupReconcileSettlesGroupAfterCrash (X.XXs)
--- PASS: TestGroupE2E_PeriodicSweepSettlesGroupWithoutAWake (1.XXs)
--- PASS: TestGroupTriggerE2E_Phase2RunsResultsGroupsAndRefusesFileGathering
ok  	github.com/arbitron/arbitron/cmd/arbitron	XX.XXXs
```
The restart fixture uses the required process gate; reaching the crash boundary is asserted, never skipped because a task happened to finish early.

- [x] Step 7: Provenance check — prove each wiring line is load-bearing (do NOT commit these edits; revert each before the next).

```bash
# 1. comment out eng.SetGroupTerminalHandler(groupCoord) in cmd/arbitron/main.go, then:
go test ./cmd/arbitron/ -run 'TestGroupE2E_EntrypointGroupWithSingleTask' -v -count=1
# Expected: --- FAIL ... instance <id> never reached a terminal state   (after ~40s)
# 2. restore it; comment out eng.SetGroupSupport(engine.GroupSupportResults), then:
go test ./cmd/arbitron/ -run 'TestGroupE2E_EntrypointGroupWithSingleTask' -v -count=1
# Expected: --- FAIL ... a results-mode group workflow must be accepted by the trigger ... parallel_group execution is not available on this server
# 3. restore it; comment out eng.RegisterExecutor(workflow.NodeTypeParallelGroup, groupExecutor) (add `_ = groupExecutor` so it compiles), then:
go test ./cmd/arbitron/ -run 'TestGroupE2E_EntrypointGroupWithSingleTask' -v -count=1
# Expected: --- FAIL ... expected: "completed" actual: "failed"
# 4. restore it; comment out the startup groupCoord.Reconcile block, then:
go test ./cmd/arbitron/ -run 'TestGroupE2E_StartupReconcileSettlesGroupAfterCrash' -v -count=1
# Expected: --- FAIL ... the startup group reconcile must decide the group and resume the waiting parent
# 5. restore it; comment out `go sweepGroupReconciler(...)` (keep the context line, use `_ = groupSweepCtx`), then:
go test ./cmd/arbitron/ -run 'TestGroupE2E_PeriodicSweepSettlesGroupWithoutAWake' -v -count=1
# Expected: --- FAIL ... only the periodic group sweep can settle this group
# restore it.
git diff --stat cmd/arbitron/main.go   # must show only the intended Step 5 changes
```

Provenance mutations were executed and restored individually: no terminal handler leaves the gated entrypoint parent waiting for 40s; no support call refuses the trigger; no executor prevents its task reaching the gate; no startup reconcile strands the crash-recovered parent for 40s; no periodic sweep strands the post-boot fixture for 20s. Bypassing `CopyGroupGitMetadata` makes both real linked-worktree forms route failure instead of success. All temporary changes were restored.

Self-review (before commit, in order):
1. Spec compliance: only the results-mode composition, recovery lifecycle and its specified e2e proofs were added; no file-mode enablement, extra flags, or alternate root. T16/T17 producers are mandatory production dependencies and now feed actual task runs.
2. Anti-green-theater: downstream persisted variables prove author-ordered distinct results, exact inherited bytes, first winner, cancellation exclusion and one continuation. Callback/ordering/restart assertions use explicit process gates. Real-root mutation tests fail without each load-bearing wiring line and without metadata reproduction. Missing receipts produce errors, never callback-order synthesis.
3. Code quality: followed neighboring spawn sweep/construction/close patterns; tests use temp directories, bounded gates and cleanup releases. Successful workspaces are deleted by the engine, so a downstream parent node inspects real bytes/refs/config/index before cleanup and its persisted output is asserted. MCP assertions distinguish root rows from branch task rows; failing helpers report through their own subtest.

- [x] Step 8: Full regression, gofmt, vet, commit.

Final verification after review and restored mutations:
- `go build -o /tmp/arbitron-t14 ./cmd/arbitron/` passes.
- Focused task tests and their `-race` run pass; full `go test ./cmd/arbitron/ -count=1` passes.
- `go test ./adapter/filesystem/ ./domain/engine/ -count=1` passes.
- `go test ./... -count=1` passes all packages except the pre-existing `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` UNKNOWN_PROFILE failure. Reproduced that exact focused failure in an unchanged `git archive HEAD` checkout of base `7955517`; no unrelated fixes included.
- `gofmt -l cmd/arbitron/`, `go vet ./cmd/arbitron/`, and `git diff --check` are clean.
- Each terminal trigger was independently disabled and restored: UPDATE capture makes the same-timestamp winner test fail on missing receipts; immutability makes late success after cancellation fail its required error assertion; INSERT capture strands the periodic-sweep fixture. Re-ran focused tests and race tests green after restoring all three. No migration or filesystem probe edits remain.

Commit includes production composition plus all three e2e/sweep test files, updated trigger expectations, and this completed checklist. Earlier tasks' remaining proof checkboxes are left for their own passes; this pass completes only T14.

```bash
go test ./cmd/arbitron/ -count=1
go test ./... -count=1
gofmt -l cmd/arbitron
go vet ./cmd/arbitron/
git add cmd/arbitron/main.go cmd/arbitron/group_e2e_test.go cmd/arbitron/group_sweep_test.go cmd/arbitron/group_trigger_e2e_test.go
git commit -m "feat(arbitron): wire parallel groups (results mode) into the composition root" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: every package `ok` except the PRE-EXISTING, unrelated `adapter/config` failure `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (UNKNOWN_PROFILE for the sample feature-pipeline; it fails identically on the base commit `885b3d8`, before any Phase 2 change); `gofmt -l` and `go vet` print nothing. Do not run this package with `-race` as a gate: `TestTrigger_SpawnLinkAndOriginCoexist` and `TestEngineChildInputs_ReturnsDeclaredContract` in `domain/engine` already report data races on the base commit.
