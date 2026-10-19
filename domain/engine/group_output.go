package engine

import (
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// BuildGroupOutput renders the frozen decision, never the live roster. Both
// ports have the same JSON-shaped output so tasks.0.result.x and byKey.a.status
// resolve through the variable store after persistence. Every nested value is
// copied, including the separate views in tasks and byKey.
//
// outcome and taskOutcome initially match the task decision; file gathering may
// later change only outcome to reflect a gather failure.
func BuildGroupOutput(run *instance.GroupRun) map[string]any {
	out := map[string]any{
		"groupRunId":  "",
		"outcome":     "",
		"taskOutcome": "",
		"rule":        "",
		"behavior":    "",
		"winner":      "",
		"reason":      "",
		"tasks":       []any{},
		"byKey":       map[string]any{},
		"gather":      map[string]any{"mode": workflow.GroupGatherResults},
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
	for _, task := range d.Tasks {
		m := frozenTaskOutput(task)
		tasks = append(tasks, m)
		byKey[task.Key] = variable.DeepCopyMap(m)
	}
	out["tasks"] = tasks
	out["byKey"] = byKey
	return out
}

func frozenTaskOutput(task instance.FrozenTask) map[string]any {
	result := variable.DeepCopyMap(task.Result)
	if result == nil {
		result = map[string]any{}
	}
	failure := variable.DeepCopyMap(task.Failure)
	if failure == nil {
		failure = map[string]any{}
	}
	return map[string]any{
		"key":             task.Key,
		"kind":            string(task.Kind),
		"origin":          task.Origin,
		"childInstanceId": task.ChildInstanceID,
		"status":          string(task.Status),
		"result":          result,
		"failure":         failure,
		"cancelRequested": task.CancelRequested,
		"winner":          task.Winner,
	}
}
