package instance

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The wire values are persisted and exposed through group output/read models.
func TestGroupVocabularyWireValues(t *testing.T) {
	assert.Equal(t, SpawnMode("group_task"), SpawnModeGroupTask)
	assert.Equal(t, GroupTaskKind("branch"), GroupTaskKindBranch)
	assert.Equal(t, GroupTaskKind("workflow"), GroupTaskKindWorkflow)
	assert.Equal(t, GroupRunState("launching"), GroupRunLaunching)
	assert.Equal(t, GroupRunState("running"), GroupRunRunning)
	assert.Equal(t, GroupRunState("decided"), GroupRunDecided)
	assert.Equal(t, GroupRunState("gathering"), GroupRunGathering)
	assert.Equal(t, GroupRunState("resolving_conflicts"), GroupRunResolvingConflicts)
	assert.Equal(t, GroupRunState("done"), GroupRunDone)
	assert.Equal(t, GroupTaskLaunchState("pending"), GroupTaskPending)
	assert.Equal(t, GroupTaskLaunchState("launched"), GroupTaskLaunched)
	assert.Equal(t, GroupTaskLaunchState("launch_failed"), GroupTaskLaunchFailed)
	assert.Equal(t, GroupTaskStatus("not_started"), GroupTaskStatusNotStarted)
	assert.Equal(t, GroupTaskStatus("queued"), GroupTaskStatusQueued)
	assert.Equal(t, GroupTaskStatus("running"), GroupTaskStatusRunning)
	assert.Equal(t, GroupTaskStatus("waiting"), GroupTaskStatusWaiting)
	assert.Equal(t, GroupTaskStatus("paused"), GroupTaskStatusPaused)
	assert.Equal(t, GroupTaskStatus("completed"), GroupTaskStatusCompleted)
	assert.Equal(t, GroupTaskStatus("failed"), GroupTaskStatusFailed)
	assert.Equal(t, GroupTaskStatus("cancelled"), GroupTaskStatusCancelled)
	assert.Equal(t, GroupTaskStatus("unfinished"), GroupTaskStatusUnfinished)
}

func TestGroupTaskKindValid(t *testing.T) {
	assert.True(t, GroupTaskKindBranch.Valid())
	assert.True(t, GroupTaskKindWorkflow.Valid())
	for _, k := range []GroupTaskKind{"", "Branch", "workflows", "branches", "spawn"} {
		assert.False(t, k.Valid(), "kind %q must be invalid", k)
	}
}

func TestGroupRunStateValidAndDecidable(t *testing.T) {
	all := []GroupRunState{GroupRunLaunching, GroupRunRunning, GroupRunDecided,
		GroupRunGathering, GroupRunResolvingConflicts, GroupRunDone}
	for _, s := range all {
		assert.True(t, s.Valid(), "%q must be valid", s)
	}
	for _, s := range []GroupRunState{"", "Done", "cancelled", "failed"} {
		assert.False(t, s.Valid(), "%q must be invalid", s)
	}
	decidable := map[GroupRunState]bool{GroupRunLaunching: true, GroupRunRunning: true}
	for _, s := range all {
		assert.Equal(t, decidable[s], s.IsDecidable(), "IsDecidable(%q)", s)
	}
}

func TestGroupTaskLaunchStateValid(t *testing.T) {
	for _, s := range []GroupTaskLaunchState{GroupTaskPending, GroupTaskLaunched, GroupTaskLaunchFailed} {
		assert.True(t, s.Valid(), "%q", s)
	}
	for _, s := range []GroupTaskLaunchState{"", "failed", "LAUNCHED"} {
		assert.False(t, s.Valid(), "%q", s)
	}
}

func TestGroupTaskStatusPredicates(t *testing.T) {
	cases := []struct {
		s        GroupTaskStatus
		valid    bool
		live     bool
		terminal bool
	}{
		{GroupTaskStatusNotStarted, true, true, false},
		{GroupTaskStatusQueued, true, true, false},
		{GroupTaskStatusRunning, true, true, false},
		{GroupTaskStatusWaiting, true, true, false},
		{GroupTaskStatusPaused, true, true, false},
		{GroupTaskStatusCompleted, true, true, true},
		{GroupTaskStatusFailed, true, true, true},
		{GroupTaskStatusCancelled, true, true, true},
		// unfinished is a frozen verdict, not a live or terminal child status.
		{GroupTaskStatusUnfinished, true, false, false},
		{"", false, false, false},
		{"done", false, false, false},
		{"Completed", false, false, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.valid, c.s.Valid(), "Valid(%q)", c.s)
		assert.Equal(t, c.live, c.s.IsLive(), "IsLive(%q)", c.s)
		assert.Equal(t, c.terminal, c.s.IsTerminal(), "IsTerminal(%q)", c.s)
	}
}

func TestGroupTaskStatusTerminalAgreesWithState(t *testing.T) {
	for _, s := range allStates() {
		assert.Equal(t, s.IsTerminal(), GroupTaskStatusFromState(s).IsTerminal(), "state %q", s)
	}
}

func allStates() []State {
	return []State{StateQueued, StateRunning, StateWaiting, StateCompleted,
		StateFailed, StateCancelled, StatePaused}
}

func TestGroupTaskStatusFromState(t *testing.T) {
	want := map[State]GroupTaskStatus{
		StateQueued: GroupTaskStatusQueued, StateRunning: GroupTaskStatusRunning,
		StateWaiting: GroupTaskStatusWaiting, StatePaused: GroupTaskStatusPaused,
		StateCompleted: GroupTaskStatusCompleted, StateFailed: GroupTaskStatusFailed,
		StateCancelled: GroupTaskStatusCancelled,
	}
	for s, w := range want {
		assert.Equal(t, w, GroupTaskStatusFromState(s), "state %q", s)
	}
	assert.Equal(t, GroupTaskStatusNotStarted, GroupTaskStatusFromState(""),
		"no child row yet reads as not_started")
	assert.Equal(t, GroupTaskStatusNotStarted, GroupTaskStatusFromState("bogus"),
		"an unknown state never claims activity")
}

func TestGroupTaskStatusFromStateTotality(t *testing.T) {
	// Read the declared vocabulary rather than only counting our own fixture:
	// adding a State in instance.go must fail until its mapping is covered.
	file, err := parser.ParseFile(token.NewFileSet(), "instance.go", nil, 0)
	require.NoError(t, err)
	var states []State
	for _, decl := range file.Decls {
		constants, ok := decl.(*ast.GenDecl)
		if !ok || constants.Tok != token.CONST {
			continue
		}
		for _, spec := range constants.Specs {
			value := spec.(*ast.ValueSpec)
			kind, ok := value.Type.(*ast.Ident)
			if !ok || kind.Name != "State" {
				continue
			}
			for _, expr := range value.Values {
				literal, ok := expr.(*ast.BasicLit)
				require.True(t, ok, "State constants must have explicit wire values")
				wire, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)
				states = append(states, State(wire))
			}
		}
	}
	assert.ElementsMatch(t, allStates(), states, "extend the State test vocabulary")
	if len(states) != 7 {
		t.Fatalf("instance.State vocabulary changed to %d values: extend GroupTaskStatusFromState and this test", len(states))
	}
	for _, s := range states {
		got := GroupTaskStatusFromState(s)
		assert.True(t, got.IsLive(), "state %q must map to a live status", s)
		assert.NotEqual(t, GroupTaskStatusNotStarted, got, "state %q must not map to not_started", s)
	}
}

func validGroupLink() SpawnLink {
	return SpawnLink{
		ParentInstanceID: uuid.New(), ParentNodeID: "group-1", Mode: SpawnModeGroupTask,
		GroupRunID: uuid.New(), GroupTaskKey: "a", GroupTaskKind: GroupTaskKindBranch,
	}
}

func TestSpawnLinkValid_GroupTaskComplete(t *testing.T) {
	l := validGroupLink()
	assert.True(t, l.Valid())
	l.GroupTaskKind = GroupTaskKindWorkflow
	assert.True(t, l.Valid())
}

func TestSpawnLinkValid_GroupTaskRequiresGroupFields(t *testing.T) {
	cases := map[string]func(*SpawnLink){
		"nil group run id":  func(l *SpawnLink) { l.GroupRunID = uuid.Nil },
		"empty task key":    func(l *SpawnLink) { l.GroupTaskKey = "" },
		"empty task kind":   func(l *SpawnLink) { l.GroupTaskKind = "" },
		"bogus task kind":   func(l *SpawnLink) { l.GroupTaskKind = "branches" },
		"nil parent":        func(l *SpawnLink) { l.ParentInstanceID = uuid.Nil },
		"empty parent node": func(l *SpawnLink) { l.ParentNodeID = "" },
	}
	for name, mutate := range cases {
		l := validGroupLink()
		mutate(&l)
		assert.False(t, l.Valid(), "%s: a group-task link must be invalid", name)
	}
}

func TestSpawnLinkValid_LegacyModeRejectsGroupFields(t *testing.T) {
	for _, mode := range []SpawnMode{SpawnModeFireAndForget, SpawnModeWaitForChild} {
		base := SpawnLink{ParentInstanceID: uuid.New(), ParentNodeID: "spawn-1", Mode: mode}
		require.True(t, base.Valid(), "legacy %q link without group fields stays valid", mode)
		withRun := base
		withRun.GroupRunID = uuid.New()
		assert.False(t, withRun.Valid(), "%q with a group run id", mode)
		withKey := base
		withKey.GroupTaskKey = "a"
		assert.False(t, withKey.Valid(), "%q with a task key", mode)
		withKind := base
		withKind.GroupTaskKind = GroupTaskKindWorkflow
		assert.False(t, withKind.Valid(), "%q with a task kind", mode)
	}
}

func TestInstanceIsGroupTask(t *testing.T) {
	parent, run := uuid.New(), uuid.New()
	node, key := "group-1", "a"
	inst := Instance{
		ID: uuid.New(), ParentInstanceID: &parent, ParentNodeID: &node,
		SpawnMode: SpawnModeGroupTask, GroupRunID: &run, GroupTaskKey: &key,
		GroupTaskKind: GroupTaskKindBranch,
	}
	assert.True(t, inst.IsGroupTask())
	assert.False(t, inst.IsRoot(), "a group task has a parent")
	noRun := inst
	noRun.GroupRunID = nil
	assert.False(t, noRun.IsGroupTask())
	noParent := inst
	noParent.ParentInstanceID = nil
	assert.False(t, noParent.IsGroupTask())
	noKey := inst
	noKey.GroupTaskKey = nil
	assert.False(t, noKey.IsGroupTask())
	emptyKey := inst
	e := ""
	emptyKey.GroupTaskKey = &e
	assert.False(t, emptyKey.IsGroupTask())
	legacy := inst
	legacy.SpawnMode = SpawnModeWaitForChild
	assert.False(t, legacy.IsGroupTask(), "a legacy awaited child is not a group task")
	assert.False(t, Instance{ID: uuid.New()}.IsGroupTask(), "a root is not a group task")
}

func TestGroupRunTaskLookups(t *testing.T) {
	ca, cb := uuid.New(), uuid.New()
	run := &GroupRun{Tasks: []GroupTask{
		{Position: 0, Key: "a", ChildInstanceID: ca},
		{Position: 1, Key: "b", ChildInstanceID: cb},
	}}
	got, ok := run.TaskByKey("b")
	require.True(t, ok)
	assert.Equal(t, cb, got.ChildInstanceID)
	got.CancelRequested = true
	assert.True(t, run.Tasks[1].CancelRequested, "TaskByKey returns a pointer INTO the roster")
	got, ok = run.TaskByChild(ca)
	require.True(t, ok)
	assert.Equal(t, "a", got.Key)
	got.ContinuingIndependently = true
	assert.True(t, run.Tasks[0].ContinuingIndependently, "TaskByChild returns a pointer INTO the roster")
	got, ok = run.TaskByKey("zz")
	assert.False(t, ok)
	assert.Nil(t, got)
	got, ok = run.TaskByChild(uuid.New())
	assert.False(t, ok)
	assert.Nil(t, got)
	_, ok = (&GroupRun{}).TaskByKey("a")
	assert.False(t, ok, "an empty roster finds nothing")
}

func TestGroupDecisionJSONShape(t *testing.T) {
	d := GroupDecision{
		Outcome: "success", Rule: "first_success", Behavior: "cancel_unfinished",
		WinnerKey: "b", Reason: "task b completed first",
		DecidedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		Tasks: []FrozenTask{{
			Key: "a", Kind: GroupTaskKindWorkflow, Origin: "child-wf",
			ChildInstanceID: "00000000-0000-0000-0000-0000000000aa",
			Status:          GroupTaskStatusUnfinished, CancelRequested: true,
		}, {
			Key: "b", Kind: GroupTaskKindWorkflow, Origin: "child-wf",
			ChildInstanceID: "00000000-0000-0000-0000-0000000000bb",
			Status:          GroupTaskStatusCompleted, Result: map[string]any{"x": float64(1)}, Winner: true,
		}},
	}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(b, &raw))
	for _, k := range []string{"outcome", "rule", "behavior", "winnerKey", "reason", "decidedAt", "tasks"} {
		assert.Contains(t, raw, k)
	}
	task0 := raw["tasks"].([]any)[0].(map[string]any)
	for _, k := range []string{"key", "kind", "origin", "childInstanceId", "status", "cancelRequested", "winner"} {
		assert.Contains(t, task0, k)
	}
	assert.NotContains(t, task0, "result", "an unfinished task has no result")
	var back GroupDecision
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, d, back)
}
