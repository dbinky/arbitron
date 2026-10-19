package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// gpTaskSpec describes one roster task of a group scenario on the fanout
// workflow (group node "g"). A launched task gets a real child instance in
// state; an unlaunched (or purged) one gets an id no store knows.
type gpTaskSpec struct {
	key         string
	launch      instance.GroupTaskLaunchState // "" => launched
	state       instance.State                // child instance state (launched tasks)
	purged      bool                          // launched, but the child row is gone
	roster      instance.GroupTaskStatus      // roster LiveStatus ("" => derived from state)
	blocking    bool
	cancel      bool
	independent bool
	launchErr   string
}

// groupScenario registers a root waiting at g, children for launched tasks and
// ONE group run; it returns the root and a pointer to the stored run so a test
// can set decision / gather / continuation fields before reading.
func (f *gsFixture) groupScenario(runState instance.GroupRunState, specs ...gpTaskSpec) (*instance.Instance, *instance.GroupRun, map[string]*instance.Instance) {
	snap := gsSnapshot(f.t, gsFanoutWF())
	root := f.add(gsRootInstance(f.owner, "fanout", snap, instance.StateWaiting, "g"))
	f.exec(root, "start", instance.NodeStatusSucceeded, "")
	f.exec(root, "g", instance.NodeStatusSucceeded, "")
	runID := uuid.New()
	children := map[string]*instance.Instance{}
	tasks := make([]instance.GroupTask, 0, len(specs))
	for i, s := range specs {
		task := instance.GroupTask{
			Position: i, Key: s.key, Kind: instance.GroupTaskKindBranch, Origin: s.key + "1",
			LaunchState: instance.GroupTaskLaunched, LaunchError: s.launchErr,
			Blocking: s.blocking, CancelRequested: s.cancel, ContinuingIndependently: s.independent,
		}
		if s.launch != "" {
			task.LaunchState = s.launch
		}
		switch {
		case s.launch != "" && s.launch != instance.GroupTaskLaunched, s.purged:
			task.ChildInstanceID = uuid.New()
		default:
			child := f.add(gsGroupTask(root, "g", runID, s.key, instance.GroupTaskKindBranch, "fanout", snap, s.state, s.key+"1"))
			children[s.key] = child
			task.ChildInstanceID = child.ID
		}
		task.LiveStatus = s.roster
		if task.LiveStatus == "" && s.state != "" {
			task.LiveStatus = instance.GroupTaskStatusFromState(s.state)
		}
		tasks = append(tasks, task)
	}
	f.addRun(gsRun(runID, root, "g", workflow.GroupFormBranches, runState, tasks...))
	return root, &f.groups.byParent[root.ID][0], children
}

func gpPanel(t *testing.T, gs *engine.GraphState, instanceID uuid.UUID, nodeID string) engine.GroupPanel {
	t.Helper()
	for _, p := range gs.Groups {
		if p.InstanceID == instanceID && p.NodeID == nodeID {
			return p
		}
	}
	t.Fatalf("no group panel for %s/%s (have %d panels)", instanceID, nodeID, len(gs.Groups))
	return engine.GroupPanel{}
}

func gpCard(t *testing.T, p engine.GroupPanel, key string) engine.GroupPanelTask {
	t.Helper()
	for _, c := range p.Tasks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("no task card %s", key)
	return engine.GroupPanelTask{}
}

// ---------------------------------------------------------------------------
// Waiting reasons (table-driven; exact vocabulary)
// ---------------------------------------------------------------------------

func TestGroupPanels_WaitingReasons(t *testing.T) {
	cases := []struct {
		name      string
		runState  instance.GroupRunState
		gather    string
		report    *instance.GatherReport
		delivered bool
		specs     []gpTaskSpec
		want      []string
	}{
		{name: "launching", runState: instance.GroupRunLaunching,
			specs: []gpTaskSpec{{key: "a", state: instance.StateQueued, blocking: true}, {key: "b", launch: instance.GroupTaskPending, blocking: true}},
			want:  []string{engine.WaitingLaunching}},
		{name: "one outstanding task", runState: instance.GroupRunRunning,
			specs: []gpTaskSpec{{key: "a", state: instance.StateRunning, blocking: true}, {key: "b", state: instance.StateCompleted, blocking: true}},
			want:  []string{"waiting_for_tasks:a"}},
		{name: "outstanding keys in author order", runState: instance.GroupRunRunning,
			specs: []gpTaskSpec{{key: "a", state: instance.StateWaiting, blocking: true}, {key: "b", state: instance.StateQueued, blocking: true}},
			want:  []string{"waiting_for_tasks:a,b"}},
		{name: "zero outstanding tasks is not ready", runState: instance.GroupRunRunning,
			specs: []gpTaskSpec{{key: "a", state: instance.StateCompleted, blocking: true}, {key: "b", state: instance.StateFailed, blocking: true}},
			want:  []string{engine.WaitingContinuationPending}},
		{name: "decided results pending finalize", runState: instance.GroupRunDecided,
			specs: []gpTaskSpec{{key: "a", state: instance.StateCompleted}, {key: "b", state: instance.StateCompleted}},
			want:  []string{engine.WaitingContinuationPending}},
		{name: "decided with a cancellation in flight", runState: instance.GroupRunDecided,
			specs: []gpTaskSpec{{key: "a", state: instance.StateFailed}, {key: "b", state: instance.StateRunning, cancel: true}},
			want:  []string{"cancelling:b", engine.WaitingContinuationPending}},
		{name: "decided files mode prepares files", runState: instance.GroupRunDecided, gather: workflow.GroupGatherResultsAndFiles,
			specs: []gpTaskSpec{{key: "a", state: instance.StateCompleted}, {key: "b", state: instance.StateCompleted}},
			want:  []string{engine.WaitingPreparingFiles}},
		{name: "gathering", runState: instance.GroupRunGathering, gather: workflow.GroupGatherResultsAndFiles,
			report: &instance.GatherReport{Mode: workflow.GroupGatherResultsAndFiles, State: "merging"},
			specs:  []gpTaskSpec{{key: "a", state: instance.StateCompleted}, {key: "b", state: instance.StateCompleted}},
			want:   []string{engine.WaitingGathering}},
		{name: "resolving conflicts with zero outstanding tasks", runState: instance.GroupRunResolvingConflicts, gather: workflow.GroupGatherResultsAndFiles,
			report: &instance.GatherReport{Mode: workflow.GroupGatherResultsAndFiles, State: "resolving"},
			specs:  []gpTaskSpec{{key: "a", state: instance.StateCompleted}, {key: "b", state: instance.StateCompleted}},
			want:   []string{engine.WaitingResolvingConflicts}},
		{name: "rollback failed", runState: instance.GroupRunGathering, gather: workflow.GroupGatherResultsAndFiles,
			report: &instance.GatherReport{Mode: workflow.GroupGatherResultsAndFiles, State: "failed", RollbackStatus: "rollback_failed"},
			specs:  []gpTaskSpec{{key: "a", state: instance.StateCompleted}, {key: "b", state: instance.StateCompleted}},
			want:   []string{engine.WaitingGathering, engine.WaitingRollbackFailed}},
		{name: "done but continuation not delivered", runState: instance.GroupRunDone,
			specs: []gpTaskSpec{{key: "a", state: instance.StateCompleted}, {key: "b", state: instance.StateCompleted}},
			want:  []string{engine.WaitingContinuationPending}},
		{name: "done and delivered with independent work", runState: instance.GroupRunDone, delivered: true,
			specs: []gpTaskSpec{{key: "a", state: instance.StateCompleted}, {key: "b", state: instance.StateRunning, independent: true}},
			want:  []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGSFixture(t)
			root, run, _ := f.groupScenario(tc.runState, tc.specs...)
			if tc.gather != "" {
				run.GatherMode = tc.gather
			}
			run.Gather = tc.report
			run.ContinuationDelivered = tc.delivered

			p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")
			assert.Equal(t, tc.want, p.WaitingReasons)
			assert.Equal(t, string(tc.runState), p.State)
		})
	}
}

// ---------------------------------------------------------------------------
// Live vs frozen
// ---------------------------------------------------------------------------

func TestGroupPanels_LiveStatusFromChildInstanceNotRoster(t *testing.T) {
	f := newGSFixture(t)
	root, _, children := f.groupScenario(instance.GroupRunRunning,
		gpTaskSpec{key: "a", state: instance.StateCompleted, roster: instance.GroupTaskStatusRunning, blocking: true},
		gpTaskSpec{key: "b", state: instance.StateRunning, blocking: true})

	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")

	a := gpCard(t, p, "a")
	assert.Equal(t, string(instance.GroupTaskStatusCompleted), a.LiveStatus, "the child instance wins over a stale roster status")
	require.NotNil(t, a.ChildInstanceID)
	assert.Equal(t, children["a"].ID, *a.ChildInstanceID)
	assert.Equal(t, []string{"waiting_for_tasks:b"}, p.WaitingReasons)
}

func TestGroupPanels_RosterTerminalFallbackWhenChildRowMissing(t *testing.T) {
	f := newGSFixture(t)
	root, _, _ := f.groupScenario(instance.GroupRunRunning,
		gpTaskSpec{key: "a", purged: true, roster: instance.GroupTaskStatusCompleted, blocking: true},
		gpTaskSpec{key: "b", purged: true, roster: instance.GroupTaskStatusRunning, blocking: true})

	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")

	assert.Equal(t, string(instance.GroupTaskStatusCompleted), gpCard(t, p, "a").LiveStatus, "a terminal roster status survives a purged child")
	b := gpCard(t, p, "b")
	assert.Equal(t, engine.LiveStateUnknown, b.LiveStatus, "a non-terminal roster status is not trusted without the child")
	assert.Nil(t, b.ChildInstanceID, "an unverified child id is not exposed")
}

func TestGroupPanels_FrozenStatusImmutableWhileLiveAdvances(t *testing.T) {
	f := newGSFixture(t)
	root, run, children := f.groupScenario(instance.GroupRunDone,
		gpTaskSpec{key: "a", state: instance.StateCompleted},
		gpTaskSpec{key: "b", state: instance.StateCompleted, independent: true})
	run.CompletionRule = workflow.GroupRuleFirstSuccess
	run.UnfinishedBehavior = workflow.GroupUnfinishedFinishIndependently
	run.ContinuationDelivered = true
	run.Decision = &instance.GroupDecision{
		Outcome: "success", Rule: workflow.GroupRuleFirstSuccess, Behavior: workflow.GroupUnfinishedFinishIndependently,
		WinnerKey: "a", Reason: "task a succeeded first", DecidedAt: time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC),
		Tasks: []instance.FrozenTask{
			{Key: "a", Kind: instance.GroupTaskKindBranch, Origin: "a1", ChildInstanceID: children["a"].ID.String(), Status: instance.GroupTaskStatusCompleted, Result: map[string]any{"x": float64(1)}, Winner: true},
			{Key: "b", Kind: instance.GroupTaskKindBranch, Origin: "b1", ChildInstanceID: children["b"].ID.String(), Status: instance.GroupTaskStatusUnfinished},
		},
	}
	bMap := map[string]any{"key": "b", "status": "unfinished", "childInstanceId": children["b"].ID.String()}
	run.FinalOutput = map[string]any{
		"winner": "a", "outcome": "success",
		"tasks": []any{map[string]any{"key": "a", "status": "completed", "result": map[string]any{"x": float64(1)}}, bMap},
		"byKey": map[string]any{"b": bMap},
	}

	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")

	b := gpCard(t, p, "b")
	assert.Equal(t, string(instance.GroupTaskStatusCompleted), b.LiveStatus, "live inspection shows the late completion")
	assert.Equal(t, string(instance.GroupTaskStatusUnfinished), b.FrozenStatus, "the gathered snapshot still says unfinished")
	assert.True(t, b.ContinuingIndependently)
	assert.False(t, b.Blocking)
	assert.Equal(t, "a", p.Winner)
	require.NotNil(t, p.Decision)
	assert.Equal(t, instance.GroupTaskStatusUnfinished, p.Decision.Tasks[1].Status)
	assert.Equal(t, "unfinished", p.FinalOutput["byKey"].(map[string]any)["b"].(map[string]any)["status"])
	assert.Empty(t, p.WaitingReasons)
	assert.Equal(t, "unfinished", run.FinalOutput["byKey"].(map[string]any)["b"].(map[string]any)["status"], "the stored record is never mutated")
}

func TestGroupPanels_ProgressCounts(t *testing.T) {
	f := newGSFixture(t)
	root, _, _ := f.groupScenario(instance.GroupRunRunning,
		gpTaskSpec{key: "a", state: instance.StateCompleted, blocking: true},
		gpTaskSpec{key: "b", state: instance.StateFailed, blocking: true},
		gpTaskSpec{key: "c", state: instance.StateCancelled, blocking: true},
		gpTaskSpec{key: "d", state: instance.StateRunning, blocking: true},
		gpTaskSpec{key: "e", state: instance.StatePaused, blocking: true},
		gpTaskSpec{key: "f", launch: instance.GroupTaskPending, blocking: true})

	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")

	assert.Equal(t, engine.GroupProgress{Total: 6, Completed: 1, Failed: 1, Cancelled: 1, Active: 2, NotStarted: 1}, p.Progress)
	assert.Equal(t, string(instance.GroupTaskStatusPaused), gpCard(t, p, "e").LiveStatus)
	assert.Equal(t, string(instance.GroupTaskStatusNotStarted), gpCard(t, p, "f").LiveStatus)
	assert.Equal(t, []string{"waiting_for_tasks:d,e,f"}, p.WaitingReasons)
	keys := []string{}
	for _, c := range p.Tasks {
		keys = append(keys, c.Key)
	}
	assert.Equal(t, []string{"a", "b", "c", "d", "e", "f"}, keys, "cards follow author order")
}

func TestGroupPanels_LaunchFailedAndNotStartedTasks(t *testing.T) {
	f := newGSFixture(t)
	root, _, _ := f.groupScenario(instance.GroupRunDecided,
		gpTaskSpec{key: "a", state: instance.StateCancelled, cancel: true},
		gpTaskSpec{key: "b", launch: instance.GroupTaskLaunchFailed, launchErr: "child workflow archived"})

	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")

	b := gpCard(t, p, "b")
	assert.Equal(t, string(instance.GroupTaskStatusFailed), b.LiveStatus)
	assert.Equal(t, "child workflow archived", b.LaunchError, "a launch error is visible as a task reason")
	assert.Nil(t, b.ChildInstanceID)
	assert.Equal(t, []string{engine.WaitingContinuationPending}, p.WaitingReasons, "a cancelled task is no longer cancelling")
}

// ---------------------------------------------------------------------------
// Gather and resolution pass-through
// ---------------------------------------------------------------------------

func TestGroupPanels_GatherAndActiveSessionPassThrough(t *testing.T) {
	f := newGSFixture(t)
	root, run, _ := f.groupScenario(instance.GroupRunResolvingConflicts,
		gpTaskSpec{key: "a", state: instance.StateCompleted}, gpTaskSpec{key: "b", state: instance.StateCompleted})
	run.GatherMode = workflow.GroupGatherResultsAndFiles
	started := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	session := &instance.ResolutionSessionRef{GroupRunID: run.ID, MergeIndex: 1, Attempt: 2, InstanceID: root.ID, NodeID: "g"}
	run.Gather = &instance.GatherReport{
		Mode: workflow.GroupGatherResultsAndFiles, State: "resolving", MergeIndex: 1,
		Handle: map[string]any{"stagingDir": "/srv/arbitron/private/staging-7", "parentDir": "/srv/arbitron/ws/p"},
		Tasks:  []instance.GatherTaskReport{{Key: "a", Merged: true, Reason: "clean"}, {Key: "b", Merged: false, Reason: "conflict", ConflictPaths: []string{"main.go"}}},
		Attempts: []instance.ResolutionAttempt{
			{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"main.go"}, Outcome: "still_conflicted", StartedAt: started},
		},
		ActiveSession: session,
	}

	gs := f.graphState(f.ownerCtx(), root.ID)
	p := gpPanel(t, gs, root.ID, "g")

	require.NotNil(t, p.Gather)
	assert.Nil(t, p.Gather.Handle, "server paths never reach the wire")
	assert.Len(t, p.Gather.Attempts, 1)
	assert.Equal(t, "still_conflicted", p.Gather.Attempts[0].Outcome)
	assert.Equal(t, []string{"main.go"}, p.Gather.Tasks[1].ConflictPaths)
	require.NotNil(t, p.ActiveSession)
	assert.Equal(t, *session, *p.ActiveSession, "the session link is passed through")
	body, err := json.Marshal(gs)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "/srv/arbitron")
	require.NotNil(t, run.Gather.Handle, "the stored report is never mutated")
}

func TestGroupPanels_ActiveSessionOnInvisibleInstanceIsDropped(t *testing.T) {
	f := newGSFixture(t)
	root, run, _ := f.groupScenario(instance.GroupRunResolvingConflicts,
		gpTaskSpec{key: "a", state: instance.StateCompleted}, gpTaskSpec{key: "b", state: instance.StateCompleted})
	run.GatherMode = workflow.GroupGatherResultsAndFiles
	stranger := uuid.New()
	run.Gather = &instance.GatherReport{Mode: workflow.GroupGatherResultsAndFiles, State: "resolving",
		ActiveSession: &instance.ResolutionSessionRef{GroupRunID: run.ID, InstanceID: stranger, NodeID: "g"}}

	gs := f.graphState(f.ownerCtx(), root.ID)
	p := gpPanel(t, gs, root.ID, "g")

	assert.Nil(t, p.ActiveSession)
	assert.Nil(t, p.Gather.ActiveSession)
	body, _ := json.Marshal(gs)
	assert.NotContains(t, string(body), stranger.String())
}

// ---------------------------------------------------------------------------
// Redaction
// ---------------------------------------------------------------------------

func TestGroupPanels_RedactedTaskCardsScrubFrozenResults(t *testing.T) {
	f := newGSFixture(t)
	root, run, children := f.groupScenario(instance.GroupRunDone,
		gpTaskSpec{key: "a", state: instance.StateCompleted}, gpTaskSpec{key: "b", state: instance.StateCompleted})
	f.mutate(root.ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPublic })
	f.mutate(children["a"].ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPublic })
	// b stays private: its child workflow's visibility differs.
	bID := children["b"].ID.String()
	run.ContinuationDelivered = true
	run.Decision = &instance.GroupDecision{Outcome: "success", Rule: workflow.GroupRuleAllSucceed, Behavior: workflow.GroupUnfinishedWaitForRemaining,
		Reason: "all tasks completed", Tasks: []instance.FrozenTask{
			{Key: "a", Kind: instance.GroupTaskKindBranch, ChildInstanceID: children["a"].ID.String(), Status: instance.GroupTaskStatusCompleted, Result: map[string]any{"public": "ok"}},
			{Key: "b", Kind: instance.GroupTaskKindBranch, ChildInstanceID: bID, Status: instance.GroupTaskStatusCompleted, Result: map[string]any{"token": "s3cr3t"}},
		}}
	bOut := map[string]any{"key": "b", "status": "completed", "childInstanceId": bID, "result": map[string]any{"token": "s3cr3t"}}
	run.FinalOutput = map[string]any{
		"tasks": []any{map[string]any{"key": "a", "status": "completed", "childInstanceId": children["a"].ID.String(), "result": map[string]any{"public": "ok"}}, bOut},
		"byKey": map[string]any{"b": bOut},
	}

	gs := f.graphState(f.as(uuid.New()), root.ID)
	p := gpPanel(t, gs, root.ID, "g")

	b := gpCard(t, p, "b")
	assert.True(t, b.Redacted)
	assert.Nil(t, b.ChildInstanceID)
	assert.Empty(t, b.LaunchError)
	assert.Equal(t, string(instance.GroupTaskStatusCompleted), b.FrozenStatus, "the frozen status of a hidden task is the parent's own record")
	assert.False(t, gpCard(t, p, "a").Redacted)

	require.NotNil(t, p.Decision)
	assert.Nil(t, p.Decision.Tasks[1].Result)
	assert.Empty(t, p.Decision.Tasks[1].ChildInstanceID)
	assert.Equal(t, map[string]any{"public": "ok"}, p.Decision.Tasks[0].Result)
	tb := p.FinalOutput["tasks"].([]any)[1].(map[string]any)
	assert.NotContains(t, tb, "result")
	assert.Equal(t, "", tb["childInstanceId"])
	assert.NotContains(t, p.FinalOutput["byKey"].(map[string]any)["b"].(map[string]any), "result")

	body, err := json.Marshal(gs)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "s3cr3t")
	assert.NotContains(t, string(body), bID)
	assert.Equal(t, map[string]any{"token": "s3cr3t"}, run.Decision.Tasks[1].Result, "the stored decision is never mutated")
}

// ---------------------------------------------------------------------------
// Unavailable records (error cases)
// ---------------------------------------------------------------------------

func TestGroupPanels_MissingRecordForReachedGroupIsUnknownNeverCompleted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  instance.State
		cursor string
	}{
		{"waiting at the group", instance.StateWaiting, "g"},
		{"already past the group", instance.StateCompleted, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGSFixture(t)
			root := f.add(gsRootInstance(f.owner, "fanout", gsSnapshot(t, gsFanoutWF()), tc.state, tc.cursor))
			f.exec(root, "start", instance.NodeStatusSucceeded, "")
			f.exec(root, "g", instance.NodeStatusSucceeded, "")

			gs := f.graphState(f.ownerCtx(), root.ID)
			p := gpPanel(t, gs, root.ID, "g")

			assert.Equal(t, engine.GroupPanelStateUnknown, p.State)
			assert.Equal(t, uuid.Nil, p.GroupRunID)
			assert.Equal(t, workflow.GroupRuleAllSucceed, p.CompletionRule, "static config still explains the group")
			require.Len(t, p.Tasks, 2)
			assert.Equal(t, engine.LiveStateUnknown, p.Tasks[0].LiveStatus)
			assert.Equal(t, engine.LiveStateUnknown, gsNodeMap(gs)[gsKey(root.ID, "g")].State, "the group node is never shown as completed or waiting without its record")
			assert.Contains(t, gs.Warnings, fmt.Sprintf("parallel group record for node g of run %s is unavailable", root.ID))
		})
	}
}

func TestGroupPanels_GroupRecordReadErrorIsUnknown(t *testing.T) {
	f := newGSFixture(t)
	fo := f.fanout(instance.StateRunning, instance.StateRunning, "a1", "b1")
	f.groups.errs[fo.root.ID] = errors.New("disk I/O error")

	gs := f.graphState(f.ownerCtx(), fo.root.ID)

	assert.Equal(t, engine.GroupPanelStateUnknown, gpPanel(t, gs, fo.root.ID, "g").State)
	assert.Contains(t, gs.Warnings, fmt.Sprintf("parallel group records of run %s could not be read", fo.root.ID))
}

func TestGroupPanels_UnreachedGroupWithoutRecordHasNoPanel(t *testing.T) {
	f := newGSFixture(t)
	root := f.add(gsRootInstance(f.owner, "fanout", gsSnapshot(t, gsFanoutWF()), instance.StateRunning, "start"))

	gs := f.graphState(f.ownerCtx(), root.ID)

	assert.Empty(t, gs.Groups)
	assert.Empty(t, gs.Warnings)
	assert.Equal(t, engine.LiveStatePending, gsNodeMap(gs)[gsKey(root.ID, "g")].State)
}

// ---------------------------------------------------------------------------
// Nesting and provenance
// ---------------------------------------------------------------------------

func TestGroupPanels_ComeFromEachVisibleContextsGroupRecords(t *testing.T) {
	f := newGSFixture(t)
	outer := gsSnapshot(t, gsNestedOuterWF())
	// The workflow task's own graph has no groups, so exactly two panels exist.
	leaf := gsSnapshot(t, gsChildWF("inner-wf", "c2"))
	root := f.add(gsRootInstance(f.owner, "nested-outer", outer, instance.StateWaiting, "g"))
	gRun := uuid.New()
	a := f.add(gsGroupTask(root, "g", gRun, "a", instance.GroupTaskKindBranch, "nested-outer", outer, instance.StateWaiting, "h"))
	hRun := uuid.New()
	tt := f.add(gsGroupTask(a, "h", hRun, "t", instance.GroupTaskKindWorkflow, "inner-wf", leaf, instance.StateRunning, "c1"))
	// The roster names only task "a" although the config has a and b: panels
	// must come from the record, not be synthesized from config.
	f.addRun(gsRun(gRun, root, "g", workflow.GroupFormBranches, instance.GroupRunRunning,
		gsRosterTask(0, "a", instance.GroupTaskKindBranch, "a1", a.ID, instance.GroupTaskStatusWaiting)))
	f.addRun(gsRun(hRun, a, "h", workflow.GroupFormWorkflows, instance.GroupRunRunning,
		gsRosterTask(0, "t", instance.GroupTaskKindWorkflow, "inner-wf", tt.ID, instance.GroupTaskStatusRunning)))

	gs := f.graphState(f.ownerCtx(), root.ID)

	require.Len(t, gs.Groups, 2, "one panel per group run, in context order")
	assert.Equal(t, root.ID, gs.Groups[0].InstanceID)
	assert.Equal(t, "g", gs.Groups[0].NodeID)
	assert.Len(t, gs.Groups[0].Tasks, 1)
	assert.Equal(t, a.ID, gs.Groups[1].InstanceID, "a nested group's panel belongs to its branch instance")
	assert.Equal(t, "h", gs.Groups[1].NodeID)
	assert.Equal(t, workflow.GroupFormWorkflows, gs.Groups[1].Form)
	assert.Equal(t, "inner-wf", gpCard(t, gs.Groups[1], "t").Origin)
	assert.Equal(t, []string{"waiting_for_tasks:t"}, gs.Groups[1].WaitingReasons)
	assert.True(t, gsContainsID(f.groups.reads, root.ID))
	assert.True(t, gsContainsID(f.groups.reads, a.ID))
}

func TestGroupPanels_LatestAttemptWins(t *testing.T) {
	f := newGSFixture(t)
	root, _, _ := f.groupScenario(instance.GroupRunDone, gpTaskSpec{key: "a", state: instance.StateCompleted})
	retry := gsRun(uuid.New(), root, "g", workflow.GroupFormBranches, instance.GroupRunRunning,
		gsRosterTask(0, "a", instance.GroupTaskKindBranch, "a1", uuid.New(), instance.GroupTaskStatusQueued))
	retry.Attempt = 2
	f.addRun(retry)

	gs := f.graphState(f.ownerCtx(), root.ID)

	require.Len(t, gs.Groups, 1)
	assert.Equal(t, retry.ID, gs.Groups[0].GroupRunID)
}

func TestGroupPanels_FrozenGatherOutputScrubsServerHandleAndInvisibleSession(t *testing.T) {
	f := newGSFixture(t)
	root, run, _ := f.groupScenario(instance.GroupRunDone,
		gpTaskSpec{key: "a", state: instance.StateCompleted})
	stranger := uuid.New()
	run.FinalOutput = map[string]any{"gather": map[string]any{
		"mode": workflow.GroupGatherResultsAndFiles, "state": instance.GatherStateApplied,
		"handle":        map[string]any{"stagingDir": "/srv/private/staging"},
		"activeSession": map[string]any{"instanceId": stranger.String(), "nodeId": "g"},
	}}
	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")
	g := p.FinalOutput["gather"].(map[string]any)
	assert.NotContains(t, g, "handle")
	assert.NotContains(t, g, "activeSession")
	assert.Equal(t, instance.GatherStateApplied, g["state"])
	assert.Contains(t, run.FinalOutput["gather"].(map[string]any), "handle", "stored output is immutable")
}

func TestGroupPanels_UntraversedChildFrozenDataIsNotDisclosed(t *testing.T) {
	f := newGSFixture(t)
	root, run, children := f.groupScenario(instance.GroupRunDone,
		gpTaskSpec{key: "a", state: instance.StateCompleted})
	childID := children["a"].ID.String()
	run.Decision = &instance.GroupDecision{Tasks: []instance.FrozenTask{{
		Key: "a", Status: instance.GroupTaskStatusCompleted, ChildInstanceID: childID,
		Result: map[string]any{"secret": "withheld"}, Failure: map[string]any{"error": "private failure"},
	}}}
	run.FinalOutput = engine.BuildGroupOutput(run)
	// Only links produce descendants; an unvisited roster id has no authorization proof.
	delete(f.links.children, root.ID)
	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")
	assert.Equal(t, "completed", gpCard(t, p, "a").LiveStatus, "terminal roster fallback is retained")
	body, err := json.Marshal(p)
	require.NoError(t, err)
	assert.NotContains(t, string(body), childID)
	assert.NotContains(t, string(body), "withheld")
	assert.NotContains(t, string(body), "private failure")
}

func TestGroupPanels_TransientChildReadIsUnknownUntilRecovery(t *testing.T) {
	f := newGSFixture(t)
	root, run, children := f.groupScenario(instance.GroupRunDone,
		gpTaskSpec{key: "a", state: instance.StateRunning, independent: true})
	f.mutate(root.ID, func(i *instance.Instance) { i.State = instance.StateCompleted })
	run.ContinuationDelivered = true
	f.store.getErr[children["a"].ID] = errors.New("database unavailable")
	gs := f.graphState(f.ownerCtx(), root.ID)
	assert.True(t, gs.Active)
	assert.NotEmpty(t, gs.Warnings)
	assert.Equal(t, engine.LiveStateUnknown, gpCard(t, gpPanel(t, gs, root.ID, "g"), "a").LiveStatus)
	delete(f.store.getErr, children["a"].ID)
	gs = f.graphState(f.ownerCtx(), root.ID)
	assert.Equal(t, "running", gpCard(t, gpPanel(t, gs, root.ID, "g"), "a").LiveStatus)
	assert.True(t, gs.Active)
	f.mutate(children["a"].ID, func(i *instance.Instance) { i.State = instance.StateCompleted })
	assert.False(t, f.graphState(f.ownerCtx(), root.ID).Active)
}

func TestGroupPanels_MissingReachedRecordKeepsRefreshActive(t *testing.T) {
	f := newGSFixture(t)
	root := f.add(gsRootInstance(f.owner, "fanout", gsSnapshot(t, gsFanoutWF()), instance.StateCompleted, "done"))
	f.exec(root, "g", instance.NodeStatusSucceeded, "")
	gs := f.graphState(f.ownerCtx(), root.ID)
	assert.Equal(t, engine.GroupPanelStateUnknown, gpPanel(t, gs, root.ID, "g").State)
	assert.True(t, gs.Active, "an unavailable reached group cannot prove subtree inactivity")
}

func TestGroupPanels_GatherReportDoesNotAliasPersistedHistory(t *testing.T) {
	f := newGSFixture(t)
	root, run, _ := f.groupScenario(instance.GroupRunDone, gpTaskSpec{key: "a", state: instance.StateCompleted})
	ended := time.Now()
	run.Gather = &instance.GatherReport{
		Tasks:    []instance.GatherTaskReport{{Key: "a", ConflictPaths: []string{"original.go"}}},
		Attempts: []instance.ResolutionAttempt{{TaskKey: "a", ConflictPaths: []string{"original.go"}, EndedAt: &ended}},
	}
	p := gpPanel(t, f.graphState(f.ownerCtx(), root.ID), root.ID, "g")
	p.Gather.Tasks[0].ConflictPaths[0] = "mutated.go"
	p.Gather.Attempts[0].ConflictPaths[0] = "mutated.go"
	*p.Gather.Attempts[0].EndedAt = time.Time{}
	assert.Equal(t, "original.go", run.Gather.Tasks[0].ConflictPaths[0])
	assert.Equal(t, "original.go", run.Gather.Attempts[0].ConflictPaths[0])
	assert.False(t, run.Gather.Attempts[0].EndedAt.IsZero())
}

func TestGroupPanels_GatherDetailsRequireTaskVisibility(t *testing.T) {
	for _, visibility := range []string{"visible", "private", "read error", "purged", "untraversed"} {
		for _, source := range []string{"live report", "frozen output", "both"} {
			t.Run(visibility+"/"+source, func(t *testing.T) {
				f := newGSFixture(t)
				root, run, children := f.groupScenario(instance.GroupRunDone,
					gpTaskSpec{key: "a", state: instance.StateCompleted},
					gpTaskSpec{key: "b", state: instance.StateCompleted})
				f.mutate(root.ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPublic })
				f.mutate(children["a"].ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPublic })
				f.mutate(children["b"].ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPublic })
				switch visibility {
				case "private":
					f.mutate(children["b"].ID, func(i *instance.Instance) { i.Visibility = instance.VisibilityPrivate })
				case "read error":
					f.store.getErr[children["b"].ID] = errors.New("storage unavailable")
				case "purged":
					delete(f.store.rows, children["b"].ID)
				case "untraversed":
					f.links.children[root.ID] = f.links.children[root.ID][:1]
				}
				// Rollback can copy a task-derived error into EVERY task's reason.
				// Neither that free text nor the group-wide error has task provenance.
				report := instance.GatherReport{
					Mode: instance.GatherModeResultsAndFiles, State: instance.GatherStateFailed,
					RollbackStatus: instance.RollbackStatusFailed, Error: "cannot merge private-b.txt",
					Tasks: []instance.GatherTaskReport{
						{Key: "a", Reason: "rolled back: private-b.txt", ConflictPaths: []string{"public-a.txt"}},
						{Key: "b", Reason: "not merged: private-b.txt", ConflictPaths: []string{"private-b.txt"}},
					},
					Attempts: []instance.ResolutionAttempt{
						{TaskKey: "a", MergeIndex: 0, Attempt: 1, Outcome: instance.ResolutionOutcomeResolved, ConflictPaths: []string{"public-a.txt"}},
						{TaskKey: "b", MergeIndex: 1, Attempt: 1, Outcome: instance.ResolutionOutcomeError, ConflictPaths: []string{"private-b.txt"}},
					},
				}
				before, err := json.Marshal(report)
				require.NoError(t, err)
				if source != "frozen output" {
					run.Gather = &report
				}
				if source != "live report" {
					var frozen map[string]any
					require.NoError(t, json.Unmarshal(before, &frozen))
					run.FinalOutput = map[string]any{"gather": frozen}
				}
				storedOutput, err := json.Marshal(run.FinalOutput)
				require.NoError(t, err)

				gs := f.graphState(f.as(uuid.New()), root.ID)
				p := gpPanel(t, gs, root.ID, "g")
				body, err := json.Marshal(p)
				require.NoError(t, err)
				assert.Contains(t, string(body), "public-a.txt", "authorized paths remain visible")
				if visibility == "visible" {
					assert.Contains(t, string(body), "private-b.txt", "full visibility retains diagnostic details")
				} else {
					assert.NotContains(t, string(body), "private-b.txt", "no hidden path in either report, reasons or group error")
				}
				var reports []instance.GatherReport
				if p.Gather != nil {
					reports = append(reports, *p.Gather)
				}
				if p.FinalOutput != nil {
					frozen, err := json.Marshal(p.FinalOutput["gather"])
					require.NoError(t, err)
					var decoded instance.GatherReport
					require.NoError(t, json.Unmarshal(frozen, &decoded))
					reports = append(reports, decoded)
				}
				for _, got := range reports {
					assert.Equal(t, report.State, got.State)
					assert.Equal(t, report.RollbackStatus, got.RollbackStatus)
					require.Len(t, got.Tasks, 2)
					require.Len(t, got.Attempts, 2, "ordered resolution history survives redaction")
					assert.Equal(t, "b", got.Attempts[1].TaskKey)
					assert.Equal(t, instance.ResolutionOutcomeError, got.Attempts[1].Outcome)
					if visibility != "visible" {
						assert.Empty(t, got.Tasks[1].ConflictPaths)
						assert.Equal(t, []string{}, got.Attempts[1].ConflictPaths, "dashboard calls .length on history paths")
					}
				}
				after, err := json.Marshal(report)
				require.NoError(t, err)
				assert.JSONEq(t, string(before), string(after), "redaction cannot mutate persisted history")
				afterOutput, err := json.Marshal(run.FinalOutput)
				require.NoError(t, err)
				assert.JSONEq(t, string(storedOutput), string(afterOutput), "frozen output remains immutable")
			})
		}
	}
}
