# Parallel Workflow Gathering — Phase 2 Task 9: Pure group decision and output builder

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the execution core ONE pure, deterministic implementation of the completion rules (`DecideGroup`), of the launch-failure decision (`DecideLaunchFailure`), and of the group node's output shape (`BuildGroupOutput`), so the launcher (P2-T8), the coordinator (P2-T10) and the executor (P2-T11) can never disagree about who won, what is frozen, or what `{{group-1.tasks.0.result.x}}` resolves to.
**Architecture:** Two side-effect-free files in `domain/engine`: `group_decision.go` (rule evaluation over the persisted roster, frozen author-order task snapshot, terminal-order tie-break) and `group_output.go` (the frozen decision rendered as a JSON-shaped `map[string]any` identical on the `success` and `failure` ports). No repository, clock, or event access — `now` is a parameter.
**Tech Stack:** Go 1.25.5, standard library (`fmt`, `math`, `sort`, `time`), `github.com/google/uuid`, testify; consumes `domain/variable.DeepCopyMap` (P2-T6).

**Prerequisites (must be merged first):** P1-T1 (workflow group constants), P2-T1 (`instance.GroupRun`, `GroupTask`, `FrozenTask`, `GroupDecision`, status/launch-state constants), P2-T6 (`variable.DeepCopyMap`). This task does NOT depend on P2-T7/P2-T8; **P2-T8 depends on this task** (`DecideLaunchFailure`) and must be executed after it.

---

## Final-outcome contract and TDD

`BuildGroupOutput` always includes `taskOutcome`, initialized alongside `outcome` from
`run.Decision.Outcome` (both empty for an undecided/nil run). Add that key to the exact
shape assertion and the persisted-variable-store test before changing the builder.
Results-only routes keep both equal. P3-T6 may change ONLY the final output's `outcome`
to failure when gathering fails, leaving `taskOutcome` and the stored `GroupDecision`
unchanged. P4 panels expose the task decision and final outcome separately.


## Interfaces

**Consumes (exact, existing or earlier-task):**
- `domain/instance` (P2-T1): `type GroupRun struct{ ID uuid.UUID; CompletionRule, UnfinishedBehavior, GatherMode string; Decision *GroupDecision; Tasks []GroupTask; ... }`, `type GroupTask struct{ Position int; Key string; Kind GroupTaskKind; Origin string; ChildInstanceID uuid.UUID; LaunchState GroupTaskLaunchState; LaunchError string; LiveStatus GroupTaskStatus; TerminalSeq *int64; TerminalAt *time.Time; Result, Failure map[string]any; CancelRequested bool; ... }`, `type FrozenTask`, `type GroupDecision`, constants `GroupTaskLaunched`, `GroupTaskPending`, `GroupTaskLaunchFailed`, `GroupTaskStatusNotStarted|Queued|Running|Waiting|Paused|Completed|Failed|Cancelled|Unfinished`, `GroupTaskKindBranch|Workflow`, `GroupRunRunning`.
- `domain/workflow` (P1-T1): `GroupRuleAllSucceed`, `GroupRuleFirstSuccess`, `GroupUnfinishedWaitForRemaining`, `GroupUnfinishedCancel`, `GroupUnfinishedFinishIndependently`, `GroupGatherResults`, `GroupPortSuccess`, `GroupPortFailure`, `GroupFormBranches`.
- `domain/variable` (P2-T6): `func DeepCopyMap(m map[string]any) map[string]any`.
- `domain/variable/variable.go:38` `NewStore`, `:115` `SetNodeOutput`, `:48` `Get` (tests only: prove the output shape resolves through the real store).

**Produces (exact signatures):**
```go
func DecideGroup(run *instance.GroupRun, now time.Time) (*instance.GroupDecision, bool) // nil,false = undecided
func DecideLaunchFailure(run *instance.GroupRun, reason string, now time.Time) *instance.GroupDecision
func BuildGroupOutput(run *instance.GroupRun) map[string]any
```
Unexported helpers later tasks in `package engine` reuse: `isTerminalGroupStatus(instance.GroupTaskStatus) bool`, `tasksInAuthorOrder([]instance.GroupTask) []instance.GroupTask`, `groupOutcomeSuccess`, `groupOutcomeFailure`.

Semantics later tasks rely on (pinned by the tests below):
- `DecideGroup` reads ONLY the roster (`run.Tasks`, `CompletionRule`, `UnfinishedBehavior`); it ignores `run.Decision` (callers check `Decision == nil` first). It returns `nil,false` while ANY task has `LaunchState != launched` (the launch barrier: launch failures are decided by `DecideLaunchFailure`, never by the rules).
- A task is terminal iff its `LiveStatus` is `completed|failed|cancelled` (written atomically with the production child terminal transition by P2-T16; `RecordTaskTerminal` replays that receipt). Terminal ORDER is `TerminalSeq` ascending; a nil seq sorts last; equal seqs (impossible from the repository, which assigns a global monotonic sequence) tie-break on `Position` ascending — deterministic.
- `all_succeed`: success only when every task is `completed`. Any `failed`/`cancelled` task ⇒ under `cancel_unfinished` failure immediately; under `wait_for_remaining` (and any other value — never decide early on an unknown behaviour) failure only once every task is terminal.
- `first_success`: the completed task with the earliest terminal order wins (NOT author order, NOT callback order); failures/cancellations never stop other candidates; failure only when every task is terminal without a completion; a cancelled task never wins.
- Frozen tasks are in author order (`Position`), statuses `completed|failed|cancelled|unfinished`. Terminal tasks carry a deep copy of `Result` (also for failed/cancelled tasks); failed/cancelled carry a deep copy of `Failure`; unfinished tasks carry no result and, when the decision will cancel them (`cancel_unfinished`), `CancelRequested=true` — status stays `unfinished`, never a false `cancelled`.
- Reason strings: `"all tasks completed"`, `"task <key> <status>"` (+ `" (<n> of <total> tasks did not succeed)"` when more than one), `"task <key> succeeded first"`, `"no task succeeded (<f> failed, <c> cancelled)"`, `"group has no tasks"` (empty roster decides failure defensively so a parent can never hang), `"unknown completion rule \"<rule>\""`.
- `DecideLaunchFailure`: outcome `failure`, reason verbatim (callers pass `"launch failed: ..."` or `"parent run cancelled ..."`), no winner; launched+terminal ⇒ its terminal status/result/failure; launched+non-terminal ⇒ `unfinished`, `CancelRequested=true`; never launched (`pending`/`launch_failed`) ⇒ `unfinished`, `ChildInstanceID ""`, `Failure {"state":"not_started","failedNodeId":"","error":"not launched: <task LaunchError, else reason>","attempt":0}`.
- `BuildGroupOutput` shape (identical keys on both ports; every nested value is a fresh copy):
  `{"groupRunId","outcome","taskOutcome","rule","behavior","winner","reason","tasks":[]any{map{"key","kind","origin","childInstanceId","status","result","failure","cancelRequested","winner"}},"byKey":map[key]<copy of the same task map>,"gather":map{"mode":"results"}}`. `result`/`failure` are always maps (empty when absent) so a template path into them resolves as missing rather than hitting nil. An undecided or nil run yields the same keys with empty values.

## Wiring & data provenance

- **Composition root:** none of its own (pure functions). They become live through `GroupLauncher` (P2-T8: `DecideLaunchFailure`), `GroupCoordinator.Evaluate` (P2-T10: `DecideGroup`, `DecideLaunchFailure`, `BuildGroupOutput`), all constructed and injected in `cmd/arbitron/main.go` by **P2-T14**.
- **Producers of every input field:**
  - `run.Tasks[*].LiveStatus`, `TerminalSeq`, `TerminalAt`, `Result`, `Failure` ← `instance.GroupRunRepository.RecordTaskTerminal` (P2-T1 port, `adapter/sqlite/group_run_repo.go:GroupRunRepo.RecordTaskTerminal` P2-T3), populated at the production terminal transition by P2-T16; the callback/recovery intake (P2-T10) replays those receipts, whose values come from `domain/instance/ports.go:InstanceRepository.Get` (child `State`, `Variables`, `CompletedAt`) and `NodeExecutionRepository.ListByInstance` (`NodeExecution.Error["error"]`, `Attempt`).
  - `run.Tasks[*].LaunchState`, `LaunchError`, `ChildInstanceID`, `Position`, `Key`, `Kind`, `Origin` ← `GroupRunRepository.CreateWithTasks` / `MarkTaskLaunched` / `MarkTaskLaunchFailed`, written by `GroupLauncher.Launch` (P2-T8) from `workflow.ParseGroupConfig` (P1-T1) task order.
  - `run.CompletionRule`, `UnfinishedBehavior`, `GatherMode`, `ID` ← `GroupLauncher.Launch` (P2-T8) from the parsed `workflow.GroupConfig`.
  - `run.Decision` (input of `BuildGroupOutput`) ← `GroupRunRepository.Decide` (P2-T3) storing a decision produced here.
  - `now` ← `time.Now` held by the coordinator/launcher (`GroupCoordinator.now`, `GroupLauncher.now`).
- **Provenance probes:** `TestDecideGroup_FirstSuccess/reversed_delivery_order_picks_earliest_terminal_seq` fails if the decision reads `Position`/slice order instead of the persisted `TerminalSeq`; `TestBuildGroupOutput_ResolvesThroughTheVariableStoreAfterPersistence` fails if the output is not reachable through `VariableStore.Get` after the JSON round trip the instance repository performs.

---

### Task 9: Pure group decision and output builder

**Files:**
- Create: `domain/engine/group_decision.go`
- Create: `domain/engine/group_output.go`
- Test: `domain/engine/group_decision_test.go` (package `engine_test`, the package every other engine test file uses; testify `assert`/`require`)
- Test: `domain/engine/group_output_test.go`

No existing file is modified.

- [x] Step 1: Write the failing decision tests. Create `domain/engine/group_decision_test.go`:

```go
package engine_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var gdT0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// gdTask describes one roster row. seq 0 means "no terminal recorded yet".
type gdTask struct {
	key             string
	launch          instance.GroupTaskLaunchState
	status          instance.GroupTaskStatus
	seq             int64
	result          map[string]any
	failure         map[string]any
	cancelRequested bool
	launchErr       string
}

func gdLive(key string, status instance.GroupTaskStatus) gdTask {
	return gdTask{key: key, status: status}
}

func gdDone(key string, status instance.GroupTaskStatus, seq int64) gdTask {
	return gdTask{key: key, status: status, seq: seq, result: map[string]any{"from": key}}
}

// gdRun builds a running group run whose roster positions follow argument
// order. A zero launch state defaults to launched.
func gdRun(rule, behavior string, tasks ...gdTask) *instance.GroupRun {
	run := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: uuid.New(), NodeID: "group-1", Attempt: 1,
		Form: workflow.GroupFormBranches, CompletionRule: rule, UnfinishedBehavior: behavior,
		GatherMode: workflow.GroupGatherResults, State: instance.GroupRunRunning,
	}
	for i, tk := range tasks {
		gt := instance.GroupTask{
			GroupRunID: run.ID, Position: i, Key: tk.key, Kind: instance.GroupTaskKindBranch,
			Origin: tk.key + "-entry", ChildInstanceID: uuid.New(), LaunchState: tk.launch,
			LaunchError: tk.launchErr, LiveStatus: tk.status, Result: tk.result, Failure: tk.failure,
			CancelRequested: tk.cancelRequested, Blocking: true,
		}
		if gt.LaunchState == "" {
			gt.LaunchState = instance.GroupTaskLaunched
		}
		if gt.LiveStatus == "" {
			gt.LiveStatus = instance.GroupTaskStatusRunning
		}
		if tk.seq > 0 {
			s := tk.seq
			at := gdT0.Add(time.Duration(s) * time.Second)
			gt.TerminalSeq = &s
			gt.TerminalAt = &at
		}
		run.Tasks = append(run.Tasks, gt)
	}
	return run
}

func gdStatuses(d *instance.GroupDecision) []instance.GroupTaskStatus {
	out := make([]instance.GroupTaskStatus, len(d.Tasks))
	for i, ft := range d.Tasks {
		out[i] = ft.Status
	}
	return out
}

func gdKeys(d *instance.GroupDecision) []string {
	out := make([]string, len(d.Tasks))
	for i, ft := range d.Tasks {
		out[i] = ft.Key
	}
	return out
}

const (
	stCompleted = instance.GroupTaskStatusCompleted
	stFailed    = instance.GroupTaskStatusFailed
	stCancelled = instance.GroupTaskStatusCancelled
	stRunning   = instance.GroupTaskStatusRunning
	stQueued    = instance.GroupTaskStatusQueued
	stUnfinish  = instance.GroupTaskStatusUnfinished
)

func TestDecideGroup_AllSucceed(t *testing.T) {
	cases := []struct {
		name        string
		run         *instance.GroupRun
		decided     bool
		outcome     string
		reason      string
		statuses    []instance.GroupTaskStatus
		cancelFlags []bool
	}{
		{
			name:     "every task completed succeeds",
			run:      gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stCompleted, 2), gdDone("b", stCompleted, 1)),
			decided:  true, outcome: "success", reason: "all tasks completed",
			statuses: []instance.GroupTaskStatus{stCompleted, stCompleted}, cancelFlags: []bool{false, false},
		},
		{
			name:    "a running task keeps the group undecided",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stCompleted, 1), gdLive("b", stRunning)),
			decided: false,
		},
		{
			name:     "cancel_unfinished decides failure at the first non-success",
			run:      gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, gdDone("a", stFailed, 1), gdLive("b", stRunning)),
			decided:  true, outcome: "failure", reason: "task a failed",
			statuses: []instance.GroupTaskStatus{stFailed, stUnfinish}, cancelFlags: []bool{false, true},
		},
		{
			name:    "wait_for_remaining does not decide early",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stFailed, 1), gdLive("b", stQueued)),
			decided: false,
		},
		{
			name:     "wait_for_remaining decides once all are terminal, naming the earliest non-success",
			run:      gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stFailed, 2), gdDone("b", stCancelled, 1)),
			decided:  true, outcome: "failure", reason: "task b cancelled (2 of 2 tasks did not succeed)",
			statuses: []instance.GroupTaskStatus{stFailed, stCancelled}, cancelFlags: []bool{false, false},
		},
		{
			name:     "single completed task",
			run:      gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, gdDone("only", stCompleted, 1)),
			decided:  true, outcome: "success", reason: "all tasks completed",
			statuses: []instance.GroupTaskStatus{stCompleted}, cancelFlags: []bool{false},
		},
		{
			name:     "single failed task",
			run:      gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("only", stFailed, 1)),
			decided:  true, outcome: "failure", reason: "task only failed",
			statuses: []instance.GroupTaskStatus{stFailed}, cancelFlags: []bool{false},
		},
		{
			name: "an unlaunched task holds the launch barrier",
			run: gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel,
				gdDone("a", stFailed, 1), gdTask{key: "b", launch: instance.GroupTaskPending, status: instance.GroupTaskStatusNotStarted}),
			decided: false,
		},
		{
			name:    "unknown behaviour never decides early",
			run:     gdRun(workflow.GroupRuleAllSucceed, "eventually", gdDone("a", stFailed, 1), gdLive("b", stRunning)),
			decided: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, ok := engine.DecideGroup(c.run, gdT0)
			require.Equal(t, c.decided, ok)
			if !c.decided {
				assert.Nil(t, d)
				return
			}
			require.NotNil(t, d)
			assert.Equal(t, c.outcome, d.Outcome)
			assert.Equal(t, c.reason, d.Reason)
			assert.Equal(t, workflow.GroupRuleAllSucceed, d.Rule)
			assert.Equal(t, c.run.UnfinishedBehavior, d.Behavior)
			assert.Empty(t, d.WinnerKey, "all_succeed never names a winner")
			assert.Equal(t, c.statuses, gdStatuses(d))
			for i, want := range c.cancelFlags {
				assert.Equal(t, want, d.Tasks[i].CancelRequested, "task %s", d.Tasks[i].Key)
				assert.False(t, d.Tasks[i].Winner)
			}
		})
	}
}

func TestDecideGroup_FirstSuccess(t *testing.T) {
	cases := []struct {
		name     string
		run      *instance.GroupRun
		decided  bool
		outcome  string
		winner   string
		reason   string
		statuses []instance.GroupTaskStatus
	}{
		{
			name:    "reversed delivery order picks earliest terminal seq",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, gdDone("a", stCompleted, 2), gdDone("b", stCompleted, 1)),
			decided: true, outcome: "success", winner: "b", reason: "task b succeeded first",
			statuses: []instance.GroupTaskStatus{stCompleted, stCompleted},
		},
		{
			name:    "a failure does not stop other candidates",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, gdDone("a", stFailed, 1), gdLive("b", stRunning)),
			decided: false,
		},
		{
			name:    "a later success after a failure wins",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, gdDone("a", stFailed, 1), gdDone("b", stCompleted, 2)),
			decided: true, outcome: "success", winner: "b", reason: "task b succeeded first",
			statuses: []instance.GroupTaskStatus{stFailed, stCompleted},
		},
		{
			name:    "every candidate finished without success",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, gdDone("a", stFailed, 1), gdDone("b", stCancelled, 2)),
			decided: true, outcome: "failure", reason: "no task succeeded (1 failed, 1 cancelled)",
			statuses: []instance.GroupTaskStatus{stFailed, stCancelled},
		},
		{
			name:    "cancelled-only roster fails",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, gdDone("a", stCancelled, 1), gdDone("b", stCancelled, 2)),
			decided: true, outcome: "failure", reason: "no task succeeded (0 failed, 2 cancelled)",
			statuses: []instance.GroupTaskStatus{stCancelled, stCancelled},
		},
		{
			name:    "a cancelled task never wins even when it is earliest",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, gdDone("a", stCancelled, 1), gdDone("b", stCompleted, 2)),
			decided: true, outcome: "success", winner: "b", reason: "task b succeeded first",
			statuses: []instance.GroupTaskStatus{stCancelled, stCompleted},
		},
		{
			name:    "simultaneous terminals tie-break on author position",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, gdDone("a", stCompleted, 5), gdDone("b", stCompleted, 5)),
			decided: true, outcome: "success", winner: "a", reason: "task a succeeded first",
			statuses: []instance.GroupTaskStatus{stCompleted, stCompleted},
		},
		{
			name:    "single task success",
			run:     gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, gdDone("only", stCompleted, 1)),
			decided: true, outcome: "success", winner: "only", reason: "task only succeeded first",
			statuses: []instance.GroupTaskStatus{stCompleted},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, ok := engine.DecideGroup(c.run, gdT0)
			require.Equal(t, c.decided, ok)
			if !c.decided {
				assert.Nil(t, d)
				return
			}
			require.NotNil(t, d)
			assert.Equal(t, c.outcome, d.Outcome)
			assert.Equal(t, c.winner, d.WinnerKey)
			assert.Equal(t, c.reason, d.Reason)
			assert.Equal(t, c.statuses, gdStatuses(d))
			for _, ft := range d.Tasks {
				assert.Equal(t, c.winner != "" && ft.Key == c.winner, ft.Winner, "winner flag on %s", ft.Key)
			}
		})
	}
}

func TestDecideGroup_FirstSuccessUnfinishedBehaviour(t *testing.T) {
	cancel := gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel,
		gdDone("a", stCompleted, 1), gdLive("b", stRunning))
	d, ok := engine.DecideGroup(cancel, gdT0)
	require.True(t, ok)
	assert.Equal(t, stUnfinish, d.Tasks[1].Status, "a cancellation REQUEST is not a confirmed cancellation")
	assert.True(t, d.Tasks[1].CancelRequested)
	assert.Nil(t, d.Tasks[1].Result, "an unfinished task has no final result")

	independent := gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently,
		gdDone("a", stCompleted, 1), gdLive("b", stRunning))
	d, ok = engine.DecideGroup(independent, gdT0)
	require.True(t, ok)
	assert.Equal(t, stUnfinish, d.Tasks[1].Status)
	assert.False(t, d.Tasks[1].CancelRequested, "finish_independently never requests cancellation")
}

func TestDecideGroup_FrozenTasksAreInAuthorOrder(t *testing.T) {
	run := gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining,
		gdDone("a", stCompleted, 3), gdDone("b", stCompleted, 1), gdDone("c", stCompleted, 2))
	// The repository returns the roster ordered by Position, but the decision
	// must not depend on that: scramble the slice.
	run.Tasks[0], run.Tasks[2] = run.Tasks[2], run.Tasks[0]

	d, ok := engine.DecideGroup(run, gdT0)
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b", "c"}, gdKeys(d))
	assert.Equal(t, run.Tasks[2].ChildInstanceID.String(), d.Tasks[0].ChildInstanceID)
	assert.Equal(t, "a-entry", d.Tasks[0].Origin)
	assert.Equal(t, instance.GroupTaskKindBranch, d.Tasks[0].Kind)
}

func TestDecideGroup_FailedTaskRetainsResultAndFailureDetails(t *testing.T) {
	failed := gdDone("a", stFailed, 1)
	failed.result = map[string]any{"partial": map[string]any{"lines": []any{"x"}}}
	failed.failure = map[string]any{"state": "failed", "failedNodeId": "a1", "error": "boom", "attempt": 2}
	run := gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, failed, gdDone("b", stCompleted, 2))

	d, ok := engine.DecideGroup(run, gdT0)
	require.True(t, ok)
	assert.Equal(t, failed.result, d.Tasks[0].Result)
	assert.Equal(t, failed.failure, d.Tasks[0].Failure)
	assert.Nil(t, d.Tasks[1].Failure, "a completed task carries no failure")
	assert.Equal(t, map[string]any{"from": "b"}, d.Tasks[1].Result)
}

func TestDecideGroup_FrozenCopiesShareNothingWithTheRoster(t *testing.T) {
	a := gdDone("a", stCompleted, 1)
	a.result = map[string]any{"nested": map[string]any{"k": "v"}}
	run := gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, a)

	d, ok := engine.DecideGroup(run, gdT0)
	require.True(t, ok)
	d.Tasks[0].Result["nested"].(map[string]any)["k"] = "mutated"

	assert.Equal(t, "v", run.Tasks[0].Result["nested"].(map[string]any)["k"],
		"freezing must deep-copy: a later live update can never reach the frozen outcome and vice versa")
}

func TestDecideGroup_EmptyRosterDecidesFailureNilRunIsUndecided(t *testing.T) {
	d, ok := engine.DecideGroup(gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel), gdT0)
	require.True(t, ok, "an empty roster must not leave the parent waiting forever")
	assert.Equal(t, "failure", d.Outcome)
	assert.Equal(t, "group has no tasks", d.Reason)
	assert.Empty(t, d.Tasks)

	d, ok = engine.DecideGroup(nil, gdT0)
	assert.False(t, ok)
	assert.Nil(t, d)
}

func TestDecideGroup_UnknownRuleFailsActionably(t *testing.T) {
	d, ok := engine.DecideGroup(gdRun("most_succeed", workflow.GroupUnfinishedCancel, gdDone("a", stCompleted, 1)), gdT0)
	require.True(t, ok)
	assert.Equal(t, "failure", d.Outcome)
	assert.Equal(t, `unknown completion rule "most_succeed"`, d.Reason)
}

func TestDecideGroup_StampsDecidedAt(t *testing.T) {
	d, ok := engine.DecideGroup(gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, gdDone("a", stCompleted, 1)), gdT0)
	require.True(t, ok)
	assert.Equal(t, gdT0, d.DecidedAt)
}

func TestDecideLaunchFailure_FreezesEveryTaskByLaunchState(t *testing.T) {
	run := gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining,
		gdLive("a", stRunning),
		gdDone("b", stCompleted, 1),
		gdTask{key: "c", launch: instance.GroupTaskLaunchFailed, status: instance.GroupTaskStatusNotStarted, launchErr: `task "c": queue full`},
		gdTask{key: "d", launch: instance.GroupTaskPending, status: instance.GroupTaskStatusNotStarted},
	)
	reason := `launch failed: task "c": queue full`

	d := engine.DecideLaunchFailure(run, reason, gdT0)
	require.NotNil(t, d)
	assert.Equal(t, "failure", d.Outcome)
	assert.Equal(t, reason, d.Reason)
	assert.Empty(t, d.WinnerKey)
	assert.Equal(t, gdT0, d.DecidedAt)
	assert.Equal(t, []string{"a", "b", "c", "d"}, gdKeys(d))

	assert.Equal(t, stUnfinish, d.Tasks[0].Status, "a launched, still-running task is unfinished")
	assert.True(t, d.Tasks[0].CancelRequested, "launched tasks are cancelled after a launch failure")
	assert.NotEmpty(t, d.Tasks[0].ChildInstanceID)

	assert.Equal(t, stCompleted, d.Tasks[1].Status, "a task that already finished keeps its outcome")
	assert.Equal(t, map[string]any{"from": "b"}, d.Tasks[1].Result)

	assert.Equal(t, stUnfinish, d.Tasks[2].Status)
	assert.Empty(t, d.Tasks[2].ChildInstanceID, "a never-launched task has no confirmed child")
	assert.Equal(t, map[string]any{
		"state": "not_started", "failedNodeId": "", "error": `not launched: task "c": queue full`, "attempt": 0,
	}, d.Tasks[2].Failure)

	assert.Equal(t, `not launched: `+reason, d.Tasks[3].Failure["error"],
		"a task that was never attempted reports the group's launch failure")
}

func TestDecideLaunchFailure_NilRunStillDecides(t *testing.T) {
	d := engine.DecideLaunchFailure(nil, "launch failed: x", gdT0)
	require.NotNil(t, d)
	assert.Equal(t, "failure", d.Outcome)
	assert.Empty(t, d.Tasks)
}
```

- [x] Step 2: Run it, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestDecideGroup|TestDecideLaunchFailure' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_decision_test.go:175:23: undefined: engine.DecideGroup
domain/engine/group_decision_test.go:330:14: undefined: engine.DecideLaunchFailure
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement. Create `domain/engine/group_decision.go`:

```go
package engine

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Decision outcomes. They are deliberately the same strings as the group's
// continuation ports: a decision's Outcome IS the port the parent resumes
// through, so there is no mapping that could drift.
const (
	groupOutcomeSuccess = workflow.GroupPortSuccess
	groupOutcomeFailure = workflow.GroupPortFailure
)

// DecideGroup applies the run's completion rule to its persisted roster.
//
// It is PURE: it reads run.Tasks, run.CompletionRule and run.UnfinishedBehavior
// only, never run.Decision (callers check Decision == nil first) and never a
// clock (now is a parameter). It returns (nil, false) while the group is
// undecided.
//
// THE LAUNCH BARRIER. While any task is not yet launched the group is
// undecided: a fast early success must not decide (and cancel) a group whose
// later tasks are still being started. A launch that fails is decided by
// DecideLaunchFailure, never by the completion rules.
//
// TERMINAL ORDER is the persisted TerminalSeq (assigned once, at terminal
// intake, by the repository's global sequence) — never callback order, never
// author order. A nil seq sorts last; equal seqs (impossible from the
// repository) tie-break on Position so the result is deterministic.
func DecideGroup(run *instance.GroupRun, now time.Time) (*instance.GroupDecision, bool) {
	if run == nil {
		return nil, false
	}
	tasks := tasksInAuthorOrder(run.Tasks)
	if len(tasks) == 0 {
		// Unreachable through ParseGroupConfig (ErrGroupNoTasks), but an
		// undecidable group would leave its parent waiting forever.
		return groupDecision(run, groupOutcomeFailure, "", "group has no tasks", []instance.FrozenTask{}, now), true
	}
	for _, t := range tasks {
		if t.LaunchState != instance.GroupTaskLaunched {
			return nil, false
		}
	}
	switch run.CompletionRule {
	case workflow.GroupRuleAllSucceed:
		return decideAllSucceed(run, tasks, now)
	case workflow.GroupRuleFirstSuccess:
		return decideFirstSuccess(run, tasks, now)
	default:
		return groupDecision(run, groupOutcomeFailure, "",
			fmt.Sprintf("unknown completion rule %q", run.CompletionRule),
			freezeTasks(tasks, "", false), now), true
	}
}

// decideAllSucceed: success only when every task completed. A failed or
// cancelled task (after its own error handling) is a non-success; under
// cancel_unfinished that decides failure immediately, under wait_for_remaining
// — and under any unrecognized behaviour, which must never decide early —
// only once every task is terminal.
func decideAllSucceed(run *instance.GroupRun, tasks []instance.GroupTask, now time.Time) (*instance.GroupDecision, bool) {
	allTerminal := true
	var nonSuccess []instance.GroupTask
	for _, t := range tasks {
		if !isTerminalGroupStatus(t.LiveStatus) {
			allTerminal = false
			continue
		}
		if t.LiveStatus != instance.GroupTaskStatusCompleted {
			nonSuccess = append(nonSuccess, t)
		}
	}
	if len(nonSuccess) == 0 {
		if !allTerminal {
			return nil, false
		}
		return groupDecision(run, groupOutcomeSuccess, "", "all tasks completed", freezeTasks(tasks, "", false), now), true
	}
	cancelUnfinished := run.UnfinishedBehavior == workflow.GroupUnfinishedCancel
	if !cancelUnfinished && !allTerminal {
		return nil, false
	}
	sortByTerminalOrder(nonSuccess)
	first := nonSuccess[0]
	reason := fmt.Sprintf("task %s %s", first.Key, first.LiveStatus)
	if len(nonSuccess) > 1 {
		reason += fmt.Sprintf(" (%d of %d tasks did not succeed)", len(nonSuccess), len(tasks))
	}
	return groupDecision(run, groupOutcomeFailure, "", reason, freezeTasks(tasks, "", cancelUnfinished), now), true
}

// decideFirstSuccess: the completed task with the earliest terminal order
// wins. Failures and cancellations never stop the other candidates, and a
// cancellation is never a success. The group fails only when every task is
// terminal without a completion.
func decideFirstSuccess(run *instance.GroupRun, tasks []instance.GroupTask, now time.Time) (*instance.GroupDecision, bool) {
	allTerminal := true
	var completed []instance.GroupTask
	failed, cancelled := 0, 0
	for _, t := range tasks {
		switch t.LiveStatus {
		case instance.GroupTaskStatusCompleted:
			completed = append(completed, t)
		case instance.GroupTaskStatusFailed:
			failed++
		case instance.GroupTaskStatusCancelled:
			cancelled++
		default:
			allTerminal = false
		}
	}
	cancelUnfinished := run.UnfinishedBehavior == workflow.GroupUnfinishedCancel
	if len(completed) > 0 {
		sortByTerminalOrder(completed)
		winner := completed[0]
		return groupDecision(run, groupOutcomeSuccess, winner.Key,
			fmt.Sprintf("task %s succeeded first", winner.Key),
			freezeTasks(tasks, winner.Key, cancelUnfinished), now), true
	}
	if !allTerminal {
		return nil, false
	}
	return groupDecision(run, groupOutcomeFailure, "",
		fmt.Sprintf("no task succeeded (%d failed, %d cancelled)", failed, cancelled),
		freezeTasks(tasks, "", false), now), true
}

// DecideLaunchFailure freezes a group whose launch could not complete (or
// whose parent died before it decided) as a failure with reason verbatim.
//
//   - launched and terminal  => its terminal status, result and failure;
//   - launched, non-terminal => unfinished with CancelRequested (the caller
//     cancels it — a cancellation REQUEST, never a confirmed "cancelled");
//   - never launched         => unfinished, no child id, and a failure naming
//     why it was not launched (its own LaunchError, else the group reason).
func DecideLaunchFailure(run *instance.GroupRun, reason string, now time.Time) *instance.GroupDecision {
	if run == nil {
		return &instance.GroupDecision{Outcome: groupOutcomeFailure, Reason: reason, DecidedAt: now, Tasks: []instance.FrozenTask{}}
	}
	tasks := tasksInAuthorOrder(run.Tasks)
	frozen := make([]instance.FrozenTask, 0, len(tasks))
	for _, t := range tasks {
		ft := frozenBase(t)
		switch {
		case t.LaunchState == instance.GroupTaskLaunched && isTerminalGroupStatus(t.LiveStatus):
			freezeTerminal(&ft, t)
		case t.LaunchState == instance.GroupTaskLaunched:
			ft.Status = instance.GroupTaskStatusUnfinished
			ft.CancelRequested = true
		default:
			ft.Status = instance.GroupTaskStatusUnfinished
			ft.ChildInstanceID = ""
			msg := t.LaunchError
			if msg == "" {
				msg = reason
			}
			ft.Failure = map[string]any{
				"state":        string(instance.GroupTaskStatusNotStarted),
				"failedNodeId": "",
				"error":        "not launched: " + msg,
				"attempt":      0,
			}
		}
		frozen = append(frozen, ft)
	}
	return groupDecision(run, groupOutcomeFailure, "", reason, frozen, now)
}

// isTerminalGroupStatus is the ONE definition of "this task is done" over the
// live roster vocabulary; RecordTaskTerminal is the only writer of these
// three values.
func isTerminalGroupStatus(s instance.GroupTaskStatus) bool {
	return s == instance.GroupTaskStatusCompleted ||
		s == instance.GroupTaskStatusFailed ||
		s == instance.GroupTaskStatusCancelled
}

// tasksInAuthorOrder returns a copy of tasks sorted by Position. The input is
// never reordered.
func tasksInAuthorOrder(tasks []instance.GroupTask) []instance.GroupTask {
	out := append([]instance.GroupTask(nil), tasks...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

func terminalOrderKey(t instance.GroupTask) int64 {
	if t.TerminalSeq == nil {
		return math.MaxInt64
	}
	return *t.TerminalSeq
}

// sortByTerminalOrder orders tasks by TerminalSeq, tie-breaking on Position.
func sortByTerminalOrder(tasks []instance.GroupTask) {
	sort.SliceStable(tasks, func(i, j int) bool {
		ki, kj := terminalOrderKey(tasks[i]), terminalOrderKey(tasks[j])
		if ki != kj {
			return ki < kj
		}
		return tasks[i].Position < tasks[j].Position
	})
}

// freezeTasks builds the immutable author-order task snapshot of a decision.
func freezeTasks(tasks []instance.GroupTask, winner string, cancelUnfinished bool) []instance.FrozenTask {
	out := make([]instance.FrozenTask, 0, len(tasks))
	for _, t := range tasks {
		ft := frozenBase(t)
		ft.Winner = winner != "" && t.Key == winner
		if isTerminalGroupStatus(t.LiveStatus) {
			freezeTerminal(&ft, t)
		} else {
			ft.Status = instance.GroupTaskStatusUnfinished
			ft.CancelRequested = ft.CancelRequested || cancelUnfinished
		}
		out = append(out, ft)
	}
	return out
}

func frozenBase(t instance.GroupTask) instance.FrozenTask {
	ft := instance.FrozenTask{Key: t.Key, Kind: t.Kind, Origin: t.Origin, CancelRequested: t.CancelRequested}
	if t.ChildInstanceID != uuid.Nil {
		ft.ChildInstanceID = t.ChildInstanceID.String()
	}
	return ft
}

// freezeTerminal copies a terminal task's outcome. Failed and cancelled tasks
// keep their available variables as well as their failure details.
func freezeTerminal(ft *instance.FrozenTask, t instance.GroupTask) {
	ft.Status = t.LiveStatus
	ft.Result = variable.DeepCopyMap(t.Result)
	if t.LiveStatus != instance.GroupTaskStatusCompleted {
		ft.Failure = variable.DeepCopyMap(t.Failure)
	}
}

func groupDecision(run *instance.GroupRun, outcome, winner, reason string, tasks []instance.FrozenTask, now time.Time) *instance.GroupDecision {
	return &instance.GroupDecision{
		Outcome:   outcome,
		Rule:      run.CompletionRule,
		Behavior:  run.UnfinishedBehavior,
		WinnerKey: winner,
		Reason:    reason,
		DecidedAt: now,
		Tasks:     tasks,
	}
}
```

- [x] Step 4: Run it, confirm it passes.

```bash
go test ./domain/engine/ -run 'TestDecideGroup|TestDecideLaunchFailure' -count=1 -v
```
Expected:
```
--- PASS: TestDecideGroup_AllSucceed (0.00s)
--- PASS: TestDecideGroup_FirstSuccess (0.00s)
...
--- PASS: TestDecideLaunchFailure_NilRunStillDecides (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 5: Commit the decision half.

```bash
git add domain/engine/group_decision.go domain/engine/group_decision_test.go
git commit -m "feat(engine): pure parallel-group completion rules and launch-failure decision" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [x] Step 6: Write the failing output tests. Create `domain/engine/group_output_test.go`:

```go
package engine_test

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goDecided(t *testing.T, run *instance.GroupRun) *instance.GroupRun {
	t.Helper()
	d, ok := engine.DecideGroup(run, gdT0)
	require.True(t, ok)
	run.Decision = d
	return run
}

func goKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestBuildGroupOutput_ExactShape(t *testing.T) {
	a := gdDone("a", stCompleted, 2)
	a.result = map[string]any{"x": "from-a"}
	b := gdDone("b", stCompleted, 1)
	b.result = map[string]any{"x": "from-b"}
	run := goDecided(t, gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel, a, b))

	out := engine.BuildGroupOutput(run)

	assert.Equal(t, []string{"behavior", "byKey", "gather", "groupRunId", "outcome", "reason", "rule", "taskOutcome", "tasks", "winner"}, goKeys(out))
	assert.Equal(t, run.ID.String(), out["groupRunId"])
	assert.Equal(t, "success", out["outcome"])
	assert.Equal(t, workflow.GroupRuleFirstSuccess, out["rule"])
	assert.Equal(t, workflow.GroupUnfinishedCancel, out["behavior"])
	assert.Equal(t, "b", out["winner"])
	assert.Equal(t, "task b succeeded first", out["reason"])
	assert.Equal(t, map[string]any{"mode": "results"}, out["gather"])

	tasks, ok := out["tasks"].([]any)
	require.True(t, ok, "tasks must be []any so integer path segments index it")
	require.Len(t, tasks, 2)
	first, ok := tasks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"key": "a", "kind": "branch", "origin": "a-entry",
		"childInstanceId": run.Tasks[0].ChildInstanceID.String(),
		"status": "completed", "result": map[string]any{"x": "from-a"},
		"failure": map[string]any{}, "cancelRequested": false, "winner": false,
	}, first, "author order: a first although b finished first")
	second := tasks[1].(map[string]any)
	assert.Equal(t, "b", second["key"])
	assert.Equal(t, true, second["winner"])

	byKey, ok := out["byKey"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, first, byKey["a"])
	assert.Equal(t, second, byKey["b"])
}

func TestBuildGroupOutput_IdenticalShapeOnSuccessAndFailure(t *testing.T) {
	success := engine.BuildGroupOutput(goDecided(t, gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel,
		gdDone("a", stCompleted, 1), gdDone("b", stCompleted, 2))))
	failure := engine.BuildGroupOutput(goDecided(t, gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel,
		gdDone("a", stFailed, 1), gdLive("b", stRunning))))

	assert.Equal(t, "success", success["outcome"])
	assert.Equal(t, "failure", failure["outcome"])
	assert.Equal(t, goKeys(success), goKeys(failure))
	st := success["tasks"].([]any)
	ft := failure["tasks"].([]any)
	require.Len(t, ft, 2)
	assert.Equal(t, goKeys(st[0].(map[string]any)), goKeys(ft[0].(map[string]any)))
	assert.Equal(t, goKeys(st[1].(map[string]any)), goKeys(ft[1].(map[string]any)))
	assert.Equal(t, "unfinished", ft[1].(map[string]any)["status"])
	assert.Equal(t, true, ft[1].(map[string]any)["cancelRequested"])
	assert.Equal(t, map[string]any{}, ft[1].(map[string]any)["result"], "an unfinished task's result is an empty map, never nil")
}

// The parent's variables are persisted as JSON and reconstructed with
// FromSnapshotWithNodeIDs before the next node runs. The output must survive
// that round trip and resolve the documented template paths.
func TestBuildGroupOutput_ResolvesThroughTheVariableStoreAfterPersistence(t *testing.T) {
	a := gdDone("a", stCompleted, 1)
	a.result = map[string]any{"x": "from-a", "n": float64(3)}
	run := goDecided(t, gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently, a, gdLive("b", stRunning)))

	raw, err := json.Marshal(map[string]any{"group-1": engine.BuildGroupOutput(run)})
	require.NoError(t, err)
	var persisted map[string]any
	require.NoError(t, json.Unmarshal(raw, &persisted))
	store := variable.FromSnapshotWithNodeIDs(persisted, map[string]bool{"group-1": true})

	v, ok := store.Get("group-1.tasks.0.result.x")
	require.True(t, ok)
	assert.Equal(t, "from-a", v)
	v, ok = store.Get("group-1.tasks.0.result.n")
	require.True(t, ok)
	assert.Equal(t, float64(3), v)
	v, ok = store.Get("group-1.byKey.b.status")
	require.True(t, ok)
	assert.Equal(t, "unfinished", v)
	v, ok = store.Get("group-1.winner")
	require.True(t, ok)
	assert.Equal(t, "a", v)
	_, ok = store.Get("group-1.tasks.1.result.x")
	assert.False(t, ok, "a path into an unfinished task's result resolves as missing")
	assert.Equal(t, "from-a", variable.Resolve("{{group-1.byKey.a.result.x}}", store))
}

func TestBuildGroupOutput_SharesNothingWithTheRun(t *testing.T) {
	a := gdDone("a", stCompleted, 1)
	a.result = map[string]any{"nested": map[string]any{"k": "v"}}
	run := goDecided(t, gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, a))

	out := engine.BuildGroupOutput(run)
	out["tasks"].([]any)[0].(map[string]any)["result"].(map[string]any)["nested"].(map[string]any)["k"] = "mutated"

	assert.Equal(t, "v", run.Decision.Tasks[0].Result["nested"].(map[string]any)["k"])
	assert.Equal(t, "v", out["byKey"].(map[string]any)["a"].(map[string]any)["result"].(map[string]any)["nested"].(map[string]any)["k"],
		"byKey holds its own copy, not an alias of tasks[i]")
}

func TestBuildGroupOutput_ReadsTheFrozenDecisionNotTheLiveRoster(t *testing.T) {
	run := goDecided(t, gdRun(workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently,
		gdDone("a", stCompleted, 1), gdLive("b", stRunning)))
	// A late terminal updates the live roster only.
	seq := int64(9)
	run.Tasks[1].LiveStatus = stCompleted
	run.Tasks[1].TerminalSeq = &seq
	run.Tasks[1].Result = map[string]any{"late": true}

	out := engine.BuildGroupOutput(run)
	b := out["byKey"].(map[string]any)["b"].(map[string]any)
	assert.Equal(t, "unfinished", b["status"])
	assert.Equal(t, map[string]any{}, b["result"])
	assert.Equal(t, "a", out["winner"])
}

func TestBuildGroupOutput_UndecidedAndNilRunsAreWellFormed(t *testing.T) {
	undecided := gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, gdLive("a", stRunning))
	out := engine.BuildGroupOutput(undecided)
	assert.Equal(t, []string{"behavior", "byKey", "gather", "groupRunId", "outcome", "reason", "rule", "taskOutcome", "tasks", "winner"}, goKeys(out))
	assert.Equal(t, undecided.ID.String(), out["groupRunId"])
	assert.Equal(t, "", out["outcome"])
	assert.Equal(t, []any{}, out["tasks"])

	nilOut := engine.BuildGroupOutput(nil)
	assert.Equal(t, goKeys(out), goKeys(nilOut))
	assert.Equal(t, "", nilOut["groupRunId"])
}
```

- [x] Step 7: Run it, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestBuildGroupOutput' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_output_test.go:37:9: undefined: engine.BuildGroupOutput
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 8: Implement. Create `domain/engine/group_output.go`:

```go
package engine

import (
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// BuildGroupOutput renders a decided group as the parallel_group node's
// output. The SAME shape is produced for the success and the failure port, so
// downstream templates never branch on which path they are on:
//
//	{"groupRunId","outcome","taskOutcome","rule","behavior","winner","reason",
//	 "tasks":   []any{ {"key","kind","origin","childInstanceId","status",
//	                    "result","failure","cancelRequested","winner"}, ... }  // author order
//	 "byKey":   {"<key>": <copy of that task's map>},
//	 "gather":  {"mode":"results"}}                                             // Phase 3 replaces with the gather report
//
// Authors reference {{group-1.tasks.0.result.x}}, {{group-1.byKey.a.status}}
// and {{group-1.winner}}. "tasks" is []any and each element map[string]any
// because those are the shapes VariableStore.Get walks (integer segments index
// []any). "result"/"failure" are always maps so a path into them resolves as
// missing rather than hitting nil.
//
// The output is built from the FROZEN run.Decision, never the live roster:
// later task completions can change live status but never the gathered
// outcome. Every nested value is a fresh copy.
func BuildGroupOutput(run *instance.GroupRun) map[string]any {
	out := map[string]any{
		"groupRunId": "",
		"outcome":    "",
		"taskOutcome": "",
		"rule":       "",
		"behavior":   "",
		"winner":     "",
		"reason":     "",
		"tasks":      []any{},
		"byKey":      map[string]any{},
		"gather":     map[string]any{"mode": workflow.GroupGatherResults},
	}
	if run == nil {
		return out
	}
	out["groupRunId"] = run.ID.String()
	out["rule"] = run.CompletionRule
	out["behavior"] = run.UnfinishedBehavior
	d := run.Decision
	if d == nil {
		return out
	}
	out["outcome"] = d.Outcome
	out["taskOutcome"] = d.Outcome
	out["rule"] = d.Rule
	out["behavior"] = d.Behavior
	out["winner"] = d.WinnerKey
	out["reason"] = d.Reason

	tasks := make([]any, 0, len(d.Tasks))
	byKey := make(map[string]any, len(d.Tasks))
	for _, ft := range d.Tasks {
		m := frozenTaskOutput(ft)
		tasks = append(tasks, m)
		byKey[ft.Key] = variable.DeepCopyMap(m)
	}
	out["tasks"] = tasks
	out["byKey"] = byKey
	return out
}

func frozenTaskOutput(ft instance.FrozenTask) map[string]any {
	result := variable.DeepCopyMap(ft.Result)
	if result == nil {
		result = map[string]any{}
	}
	failure := variable.DeepCopyMap(ft.Failure)
	if failure == nil {
		failure = map[string]any{}
	}
	return map[string]any{
		"key":             ft.Key,
		"kind":            string(ft.Kind),
		"origin":          ft.Origin,
		"childInstanceId": ft.ChildInstanceID,
		"status":          string(ft.Status),
		"result":          result,
		"failure":         failure,
		"cancelRequested": ft.CancelRequested,
		"winner":          ft.Winner,
	}
}
```

- [x] Step 9: Run the new tests, the whole package, gofmt and vet.

Verification: both red cycles failed on the missing public functions before implementation. Focused tests, the whole engine package, `gofmt -l`, and `go vet ./domain/engine/` passed after implementation and again after self-review. Review order: (1) exact decision/output contracts, including `taskOutcome`; (2) nonempty roster/result assertions through the persisted JSON/real variable-store sink and frozen-vs-live checks; (3) idiomatic helpers, no added options or dependencies. Pure-function runtime consumers and composition wiring remain assigned to P2-T8/T10/T11/T14 as specified.

```bash
go test ./domain/engine/ -run 'TestDecideGroup|TestDecideLaunchFailure|TestBuildGroupOutput' -count=1
go test ./domain/engine/ -count=1
gofmt -l domain/engine/group_decision.go domain/engine/group_output.go domain/engine/group_decision_test.go domain/engine/group_output_test.go
go vet ./domain/engine/
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 10: Commit.

```bash
git add domain/engine/group_output.go domain/engine/group_output_test.go
git commit -m "feat(engine): parallel-group output shape identical on both ports" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
