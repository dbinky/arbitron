package instance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fullGatherReport() GatherReport {
	started := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	return GatherReport{
		Mode: GatherModeResultsAndFiles, State: GatherStateResolving,
		Handle: map[string]any{"snapshotHandle": "h", "nested": map[string]any{"k": []any{"v"}}}, MergeIndex: 1,
		Tasks: []GatherTaskReport{{Key: "a", Merged: true, Reason: "merged cleanly"}, {Key: "b", Reason: "conflicts", ConflictPaths: []string{"c.txt"}}},
		Attempts: []ResolutionAttempt{
			{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"c.txt"}, Outcome: ResolutionOutcomeStillConflicted, StartedAt: started, EndedAt: &ended},
			{MergeIndex: 1, Attempt: 2, TaskKey: "b", ConflictPaths: []string{"c.txt"}, StartedAt: ended},
		},
		ActiveSession: &ResolutionSessionRef{GroupRunID: uuid.New(), MergeIndex: 1, Attempt: 2, InstanceID: uuid.New(), NodeID: "grp"},
	}
}

func TestGatherReport_ValidReportPasses(t *testing.T) {
	require.NoError(t, fullGatherReport().Validate())
	require.NoError(t, GatherReport{Mode: GatherModeResults, State: GatherStateNotApplicable}.Validate())
}

func TestGatherReport_ValidateListsEveryProblem(t *testing.T) {
	ended := time.Now()
	r := GatherReport{Mode: "files", State: "half-done", MergeIndex: -1, RollbackStatus: "maybe",
		Tasks:         []GatherTaskReport{{Key: ""}, {Key: "a"}, {Key: "a"}},
		Attempts:      []ResolutionAttempt{{TaskKey: "", Attempt: 0, MergeIndex: -1, Outcome: "won"}, {TaskKey: "a", Attempt: 1, EndedAt: &ended, Outcome: "won"}},
		ActiveSession: &ResolutionSessionRef{},
	}
	err := r.Validate()
	require.ErrorIs(t, err, ErrInvalidGatherReport)
	for _, want := range []string{`mode "files"`, `state "half-done"`, "mergeIndex is negative", `rollbackStatus "maybe"`,
		"tasks[0] has no key", `task "a" appears twice`, "attempts[0] has no taskKey", "attempts[0].attempt must be >= 1",
		"attempts[0].mergeIndex is negative", `attempts[0].outcome "won"`, `attempts[1].outcome "won"`, "activeSession needs"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestGatherReport_CloneIsDeep(t *testing.T) {
	orig := fullGatherReport()
	cp := orig.Clone()
	require.Equal(t, orig, cp)
	cp.Handle["nested"].(map[string]any)["k"].([]any)[0] = "mutated"
	cp.Tasks[1].ConflictPaths[0] = "mutated"
	cp.Attempts[0].ConflictPaths[0] = "mutated"
	*cp.Attempts[0].EndedAt = time.Time{}
	cp.ActiveSession.NodeID = "mutated"
	assert.Equal(t, fullGatherReport().Tasks, orig.Tasks)
	assert.Equal(t, "v", orig.Handle["nested"].(map[string]any)["k"].([]any)[0])
	assert.Equal(t, "c.txt", orig.Attempts[0].ConflictPaths[0])
	assert.False(t, orig.Attempts[0].EndedAt.IsZero())
	assert.Equal(t, "grp", orig.ActiveSession.NodeID)
}

func TestGatherReport_ClonePreservesHandleTypes(t *testing.T) {
	orig := fullGatherReport()
	orig.Handle["index"] = 2
	orig.Handle["paths"] = []string{"c.txt"}
	cp := orig.Clone()
	require.Equal(t, orig, cp, "a clone must not coerce integers or typed containers through JSON")
	cp.Handle["paths"].([]string)[0] = "changed"
	assert.Equal(t, []string{"c.txt"}, orig.Handle["paths"])
}

func TestGatherReport_NormalizedNeverNullLists(t *testing.T) {
	r := GatherReport{Mode: GatherModeResults, State: GatherStateNotApplicable, Attempts: []ResolutionAttempt{{TaskKey: "a", Attempt: 1}}}.Normalized()
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	assert.JSONEq(t, `{"mode":"results","state":"not_applicable","mergeIndex":0,"tasks":[],"attempts":[{"mergeIndex":0,"attempt":1,"taskKey":"a","conflictPaths":[],"outcome":"","startedAt":"0001-01-01T00:00:00Z"}]}`, string(raw))
}

func TestGatherReport_JSONShape(t *testing.T) {
	r := fullGatherReport()
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"mode", "state", "handle", "mergeIndex", "tasks", "attempts", "activeSession"} {
		assert.Contains(t, m, k)
	}
	assert.NotContains(t, m, "rollbackStatus")
	assert.NotContains(t, m, "error")
	task := m["tasks"].([]any)[1].(map[string]any)
	assert.Equal(t, []any{"c.txt"}, task["conflictPaths"])
	session := m["activeSession"].(map[string]any)
	for _, k := range []string{"groupRunId", "mergeIndex", "attempt", "instanceId", "nodeId"} {
		assert.Contains(t, session, k)
	}
	var back GatherReport
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, r.Tasks, back.Tasks)
	assert.Equal(t, r.ActiveSession, back.ActiveSession)
	assert.True(t, errors.Is(GatherReport{}.Validate(), ErrInvalidGatherReport))
}
