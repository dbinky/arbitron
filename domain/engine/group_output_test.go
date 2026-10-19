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
	assert.Equal(t, "success", out["taskOutcome"])
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
		"status":          "completed", "result": map[string]any{"x": "from-a"},
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
	assert.Equal(t, success["outcome"], success["taskOutcome"])
	assert.Equal(t, failure["outcome"], failure["taskOutcome"])
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
	v, ok = store.Get("group-1.taskOutcome")
	require.True(t, ok)
	assert.Equal(t, "success", v)
	v, ok = store.Get("group-1.outcome")
	require.True(t, ok)
	assert.Equal(t, "success", v)
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
	assert.Equal(t, "", out["taskOutcome"])
	assert.Equal(t, []any{}, out["tasks"])

	nilOut := engine.BuildGroupOutput(nil)
	assert.Equal(t, goKeys(out), goKeys(nilOut))
	assert.Equal(t, "", nilOut["groupRunId"])
	assert.Equal(t, "", nilOut["taskOutcome"])
}
