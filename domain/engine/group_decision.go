package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

const (
	groupOutcomeSuccess = workflow.GroupPortSuccess
	groupOutcomeFailure = workflow.GroupPortFailure
)

// DecideGroup applies the completion rule to the persisted roster without
// changing it. While any task is unlaunched, only DecideLaunchFailure may
// decide the group. TerminalSeq, not callback or author order, determines which
// terminal happened first. The caller is responsible for persisting the decision.
func DecideGroup(run *instance.GroupRun, now time.Time) (*instance.GroupDecision, bool) {
	if run == nil {
		return nil, false
	}
	tasks := tasksInAuthorOrder(run.Tasks)
	if len(tasks) == 0 {
		return groupDecision(run, groupOutcomeFailure, "", "group has no tasks", []instance.FrozenTask{}, now), true
	}
	for _, task := range tasks {
		if task.LaunchState != instance.GroupTaskLaunched {
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

func decideAllSucceed(run *instance.GroupRun, tasks []instance.GroupTask, now time.Time) (*instance.GroupDecision, bool) {
	allTerminal := true
	var nonSuccess []instance.GroupTask
	for _, task := range tasks {
		if !isTerminalGroupStatus(task.LiveStatus) {
			allTerminal = false
			continue
		}
		if task.LiveStatus != instance.GroupTaskStatusCompleted {
			nonSuccess = append(nonSuccess, task)
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

func decideFirstSuccess(run *instance.GroupRun, tasks []instance.GroupTask, now time.Time) (*instance.GroupDecision, bool) {
	allTerminal := true
	var completed []instance.GroupTask
	failed, cancelled := 0, 0
	for _, task := range tasks {
		switch task.LiveStatus {
		case instance.GroupTaskStatusCompleted:
			completed = append(completed, task)
		case instance.GroupTaskStatusFailed:
			failed++
		case instance.GroupTaskStatusCancelled:
			cancelled++
		default:
			allTerminal = false
		}
	}
	if len(completed) > 0 {
		sortByTerminalOrder(completed)
		winner := completed[0]
		return groupDecision(run, groupOutcomeSuccess, winner.Key,
			fmt.Sprintf("task %s succeeded first", winner.Key),
			freezeTasks(tasks, winner.Key, run.UnfinishedBehavior == workflow.GroupUnfinishedCancel), now), true
	}
	if !allTerminal {
		return nil, false
	}
	return groupDecision(run, groupOutcomeFailure, "",
		fmt.Sprintf("no task succeeded (%d failed, %d cancelled)", failed, cancelled),
		freezeTasks(tasks, "", false), now), true
}

// DecideLaunchFailure freezes launched terminals as they are, requests
// cancellation of launched nonterminals, and records why unlaunched tasks did
// not start. A cancellation request never claims a confirmed cancellation.
func DecideLaunchFailure(run *instance.GroupRun, reason string, now time.Time) *instance.GroupDecision {
	if run == nil {
		return &instance.GroupDecision{Outcome: groupOutcomeFailure, Reason: reason, DecidedAt: now, Tasks: []instance.FrozenTask{}}
	}
	tasks := tasksInAuthorOrder(run.Tasks)
	frozen := make([]instance.FrozenTask, 0, len(tasks))
	for _, task := range tasks {
		ft := frozenBase(task)
		switch {
		case task.LaunchState == instance.GroupTaskLaunched && isTerminalGroupStatus(task.LiveStatus):
			freezeTerminal(&ft, task)
		case task.LaunchState == instance.GroupTaskLaunched:
			ft.Status = instance.GroupTaskStatusUnfinished
			ft.CancelRequested = true
		default:
			ft.Status = instance.GroupTaskStatusUnfinished
			ft.ChildInstanceID = ""
			message := task.LaunchError
			if message == "" {
				message = reason
			}
			ft.Failure = map[string]any{
				"state":        string(instance.GroupTaskStatusNotStarted),
				"failedNodeId": "",
				"error":        "not launched: " + message,
				"attempt":      0,
			}
		}
		frozen = append(frozen, ft)
	}
	return groupDecision(run, groupOutcomeFailure, "", reason, frozen, now)
}

// decidedOnPartialRoster reports whether the decision was frozen while some
// task was not yet launched: an interrupted launch, a launch failure or a dead
// parent. Such a decision stops every task regardless of the unfinished-task
// behaviour, and a launcher still starting tasks must stop what it started.
// A decision frozen from a complete roster is the group's own and keeps it.
func decidedOnPartialRoster(d *instance.GroupDecision) bool {
	if d == nil {
		return false
	}
	for _, ft := range d.Tasks {
		if ft.Failure != nil && ft.Failure["state"] == string(instance.GroupTaskStatusNotStarted) {
			return true
		}
	}
	return false
}

func isTerminalGroupStatus(status instance.GroupTaskStatus) bool {
	return status.IsTerminal()
}

// tasksInAuthorOrder returns a sorted copy, leaving the roster untouched.
func tasksInAuthorOrder(tasks []instance.GroupTask) []instance.GroupTask {
	out := append([]instance.GroupTask(nil), tasks...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

func sortByTerminalOrder(tasks []instance.GroupTask) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i].TerminalSeq, tasks[j].TerminalSeq
		if a == nil && b != nil {
			return false
		}
		if a != nil && b == nil {
			return true
		}
		if a != nil && b != nil && *a != *b {
			return *a < *b
		}
		return tasks[i].Position < tasks[j].Position
	})
}

func freezeTasks(tasks []instance.GroupTask, winner string, cancelUnfinished bool) []instance.FrozenTask {
	out := make([]instance.FrozenTask, 0, len(tasks))
	for _, task := range tasks {
		ft := frozenBase(task)
		ft.Winner = winner != "" && task.Key == winner
		if isTerminalGroupStatus(task.LiveStatus) {
			freezeTerminal(&ft, task)
		} else {
			ft.Status = instance.GroupTaskStatusUnfinished
			ft.CancelRequested = ft.CancelRequested || cancelUnfinished
		}
		out = append(out, ft)
	}
	return out
}

func frozenBase(task instance.GroupTask) instance.FrozenTask {
	ft := instance.FrozenTask{Key: task.Key, Kind: task.Kind, Origin: task.Origin, CancelRequested: task.CancelRequested}
	if task.ChildInstanceID != uuid.Nil {
		ft.ChildInstanceID = task.ChildInstanceID.String()
	}
	return ft
}

func freezeTerminal(ft *instance.FrozenTask, task instance.GroupTask) {
	ft.Status = task.LiveStatus
	ft.Result = variable.DeepCopyMap(task.Result)
	if task.LiveStatus != instance.GroupTaskStatusCompleted {
		ft.Failure = variable.DeepCopyMap(task.Failure)
	}
}

func groupDecision(run *instance.GroupRun, outcome, winner, reason string, tasks []instance.FrozenTask, now time.Time) *instance.GroupDecision {
	return &instance.GroupDecision{
		Outcome: outcome, Rule: run.CompletionRule, Behavior: run.UnfinishedBehavior,
		WinnerKey: winner, Reason: reason, DecidedAt: now, Tasks: tasks,
	}
}
