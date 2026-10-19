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
			name:    "every task completed succeeds",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stCompleted, 2), gdDone("b", stCompleted, 1)),
			decided: true, outcome: "success", reason: "all tasks completed",
			statuses: []instance.GroupTaskStatus{stCompleted, stCompleted}, cancelFlags: []bool{false, false},
		},
		{
			name:    "a running task keeps the group undecided",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stCompleted, 1), gdLive("b", stRunning)),
			decided: false,
		},
		{
			name:    "cancel_unfinished decides failure at the first non-success",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, gdDone("a", stFailed, 1), gdLive("b", stRunning)),
			decided: true, outcome: "failure", reason: "task a failed",
			statuses: []instance.GroupTaskStatus{stFailed, stUnfinish}, cancelFlags: []bool{false, true},
		},
		{
			name:    "wait_for_remaining does not decide early",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stFailed, 1), gdLive("b", stQueued)),
			decided: false,
		},
		{
			name:    "wait_for_remaining decides once all are terminal, naming the earliest non-success",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("a", stFailed, 2), gdDone("b", stCancelled, 1)),
			decided: true, outcome: "failure", reason: "task b cancelled (2 of 2 tasks did not succeed)",
			statuses: []instance.GroupTaskStatus{stFailed, stCancelled}, cancelFlags: []bool{false, false},
		},
		{
			name:    "single completed task",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel, gdDone("only", stCompleted, 1)),
			decided: true, outcome: "success", reason: "all tasks completed",
			statuses: []instance.GroupTaskStatus{stCompleted}, cancelFlags: []bool{false},
		},
		{
			name:    "single failed task",
			run:     gdRun(workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining, gdDone("only", stFailed, 1)),
			decided: true, outcome: "failure", reason: "task only failed",
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
