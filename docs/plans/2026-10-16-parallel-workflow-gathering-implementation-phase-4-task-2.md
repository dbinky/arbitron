# Parallel Workflow Gathering — Phase 4 Task 2: Group panels (live vs frozen, waiting reasons, gather history)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Fill `GraphState.Groups` with one authorized `GroupPanel` per parallel group run in every visible execution context, so operators can see each gather's rule, ordered live task statuses, progress, blocking vs independent work, winner, frozen gathered outcome, merge/resolution activity and EXACTLY why the gather is waiting.
**Architecture:** One new file `domain/engine/graph_state_groups.go` adds `(*graphTraversal).buildGroupPanels()` over the traversal P4-T1 already built: the group records it loaded per visible context (`GroupRunRepository.ListByParent`), the authorized child instances (`visible`) and the redacted ones (`hidden`). Live task status comes from the child INSTANCE (roster only as a terminal fallback); frozen status comes from the immutable `GroupDecision`; inaccessible children become redacted cards and their results are scrubbed from the decision and final output. A one-line edit in `graph_state.go` calls it.
**Tech Stack:** Go 1.25.5, standard library (`sort`, `strings`), testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Each gather shows its completion rule, task progress, outstanding dependencies, winner when applicable, and file-gathering or interactive-resolution activity"; "Tasks continuing independently after a first-success decision remain visible and are distinguished from work blocking the parent"; "Completed runs retain task outcomes, gathered results, and merge-resolution outcomes"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §1 (group panels; "Keep live statuses separate from the immutable decision/final report"; "zero outstanding tasks does not imply ready to continue") and Error handling ("An unavailable group record cannot be represented as a completed gather").

**Interfaces**
- Consumes (P4-T1, `domain/engine/graph_state.go`): `GroupPanel`, `GroupPanelTask`, `GroupProgress`, `LiveStateUnknown`, `LiveStatePending`; unexported `graphTraversal{contexts, entries, visible, hidden, warn}`, `graphEntry{index, inst, wf, gg, nodeSet, runs, runsErr, runsRead}`; the hook line `groups := []GroupPanel{}` in `(*GraphStateReader).GraphState`.
- Consumes (P4-T1 test helpers in `domain/engine/graph_state_test.go`, same `engine_test` package): `newGSFixture`, `gsFixture{store, viewer, execs, groups, links, owner, add, mutate, exec, addRun, graphState, as, ownerCtx}`, `gsFanoutWF`, `gsNestedOuterWF`, `gsInnerWF`, `gsSnapshot`, `gsRootInstance`, `gsGroupTask`, `gsRosterTask`, `gsRun`, `gsStr`, `gsKey`, `gsNodeMap`, `gsContainsID`.
- Consumes (contract): P2-T1 `instance.GroupRun{ID, ParentInstanceID, NodeID, Attempt, Form, CompletionRule, UnfinishedBehavior, GatherMode, State, Decision, FinalOutput, ContinuationDelivered, CreatedAt, Tasks}`, `instance.GroupTask{Position, Key, Kind, Origin, ChildInstanceID, LaunchState, LaunchError, LiveStatus, CancelRequested, Blocking, ContinuingIndependently}`, `instance.GroupRunLaunching/Running/Decided/Gathering/ResolvingConflicts/Done`, `instance.GroupTaskPending/GroupTaskLaunched/GroupTaskLaunchFailed`, `instance.GroupTaskStatus*`, `instance.GroupTaskStatusFromState`, `instance.GroupDecision`, `instance.FrozenTask`; P2-T9 `BuildGroupOutput` shape (`"tasks"` []any of maps with `"key","childInstanceId","result","failure",...`; `"byKey"` map[key]sameMap); P3-T4 `GroupRun.Gather *instance.GatherReport{Mode, State, Handle, MergeIndex, Tasks, Attempts, ActiveSession, RollbackStatus, Error}`, `instance.ResolutionSessionRef{GroupRunID, MergeIndex, Attempt, InstanceID, NodeID}`; P1-T1 `workflow.GroupGatherResultsAndFiles`, `workflow.GroupFormBranches`, `workflow.GroupConfig`, `workflow.NodeTypeParallelGroup`; P1-T2 `workflow.GroupGraph.Order/Groups`; P2-T6 `variable.DeepCopyMap`.
- Produces:
  - Waiting-reason vocabulary (exact strings, ordered as listed when several apply): `WaitingLaunching = "launching"`, `WaitingForTasksPrefix = "waiting_for_tasks:"` (+ comma-joined task keys in author order), `WaitingCancellingPrefix = "cancelling:"` (+ keys), `WaitingPreparingFiles = "preparing_files"`, `WaitingGathering = "gathering"`, `WaitingResolvingConflicts = "resolving_conflicts"`, `WaitingRollbackFailed = "rollback_failed"`, `WaitingContinuationPending = "continuation_pending"`.
  - `GroupPanelStateUnknown = "unknown"` (a panel whose group record is unavailable).
  - `func (t *graphTraversal) buildGroupPanels() []GroupPanel` (unexported; called by `GraphState`).
  - Semantics: panels appear in context order, then `GroupGraph.Order` within a context; only the latest attempt per group node; `GroupPanel.InstanceID` is the context instance (the group's parent); `Tasks` follow roster `Position`; `Gather.Handle` is always nil on the wire (server paths); `ActiveSession` is set only when it names a visible instance; a group node that was reached but has no readable record yields a panel with `State "unknown"`, a warning, and the node's live state forced to `unknown` (never `completed`).

**Wiring & data provenance**
- Composition root: none of its own — it runs inside `GraphStateReader.GraphState` (P4-T1), which P4-T3 wires in `cmd/arbitron/main.go` and serves over HTTP.
- Producers:
  - Group records ← P4-T1 `graphTraversal.walk` → `instance.GroupRunRepository.ListByParent` (P2-T3 sqlite repo); roster/decision/final output written by P2-T8 `GroupLauncher.Launch` (`CreateWithTasks`), P2-T10 `GroupCoordinator.Evaluate` (`RecordTaskTerminal`, `Decide`, `MarkTaskCancelRequested`, `MarkTaskContinuingIndependently`, `SetFinalOutput`, `MarkContinuationDelivered`); `Gather` written by P3-T6 `GroupGatherer` via `SetGatherReport` (P3-T4).
  - Live child state ← `Engine.GetInstance` results held in `graphTraversal.visible` (P4-T1).
  - Static group config for an unknown panel ← `workflow.AnalyzeGroups(wf).Groups[nodeID].Config` of the context's own snapshot.
- Provenance probe: `TestGroupPanels_ComeFromEachVisibleContextsGroupRecords` fails if panels are synthesized from config instead of `ListByParent`, or if nested contexts' records are not read.

### Task 2: Group panels
**Files:**
- Create: `domain/engine/graph_state_groups.go`
- Modify: `domain/engine/graph_state.go` — the single hook line `groups := []GroupPanel{}` inside `(*GraphStateReader).GraphState` (added by P4-T1; locate with `grep -n 'groups := \[\]GroupPanel{}' domain/engine/graph_state.go`).
- Test:   `domain/engine/graph_state_groups_test.go` (package `engine_test`)

- [x] Step 1: Write the failing tests. Create `domain/engine/graph_state_groups_test.go`:

```go
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
		"tasks":  []any{map[string]any{"key": "a", "status": "completed", "result": map[string]any{"x": float64(1)}}, bMap},
		"byKey":  map[string]any{"b": bMap},
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
```

- [x] Step 2: Run it, confirm it fails.

Observed the specified compile failure: waiting-reason constants are undefined.

```bash
go test ./domain/engine/ -run 'TestGroupPanels' -count=1
```
Expected (compile failure on the new vocabulary):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/graph_state_groups_test.go:104:24: undefined: engine.WaitingLaunching
domain/engine/graph_state_groups_test.go:114:24: undefined: engine.WaitingContinuationPending
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Minimal implementation. Create `domain/engine/graph_state_groups.go`:

```go
package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Waiting-reason vocabulary. These exact strings are the wire contract the
// dashboard renders; when several apply they appear in this order. Zero
// outstanding tasks never means "ready": a decision, a file gather, a
// resolution session or the continuation itself can still be pending.
const (
	WaitingLaunching           = "launching"
	WaitingForTasksPrefix      = "waiting_for_tasks:"
	WaitingCancellingPrefix    = "cancelling:"
	WaitingPreparingFiles      = "preparing_files"
	WaitingGathering           = "gathering"
	WaitingResolvingConflicts  = "resolving_conflicts"
	WaitingRollbackFailed      = "rollback_failed"
	WaitingContinuationPending = "continuation_pending"
)

// GroupPanelStateUnknown is the panel state when the group record is
// unavailable. An unavailable record is never presented as a completed gather.
const GroupPanelStateUnknown = "unknown"

// rollbackFailedStatus mirrors instance.GatherReport.RollbackStatus (P3-T4).
const rollbackFailedStatus = "rollback_failed"

// buildGroupPanels returns one panel per group run (latest attempt) owned by a
// visible context, in context order then GroupGraph.Order. A group node that
// was reached but has no readable record becomes an "unknown" panel with a
// warning, and its live node state is forced to unknown.
func (t *graphTraversal) buildGroupPanels() []GroupPanel {
	panels := []GroupPanel{}
	for _, e := range t.entries {
		if e.inst == nil || e.wf == nil || e.gg == nil {
			continue
		}
		latest := latestRunsByNode(e.runs)
		for _, gid := range e.gg.Order {
			if !e.nodeSet[gid] {
				continue // owned by another context (a branch body)
			}
			if run, ok := latest[gid]; ok && e.runsRead && e.runsErr == nil {
				panels = append(panels, t.panelFor(run))
				continue
			}
			if !t.nodeReached(e, gid) {
				continue
			}
			panels = append(panels, t.unknownPanel(e, gid))
		}
	}
	return panels
}

func latestRunsByNode(runs []instance.GroupRun) map[string]instance.GroupRun {
	out := map[string]instance.GroupRun{}
	for _, run := range runs {
		cur, ok := out[run.NodeID]
		if !ok || run.Attempt > cur.Attempt || (run.Attempt == cur.Attempt && run.CreatedAt.After(cur.CreatedAt)) {
			out[run.NodeID] = run
		}
	}
	return out
}

func (t *graphTraversal) nodeReached(e *graphEntry, nodeID string) bool {
	for _, n := range t.contexts[e.index].Nodes {
		if n.NodeID == nodeID {
			return n.State != LiveStatePending && n.State != ""
		}
	}
	return false
}

func (t *graphTraversal) markNodeUnknown(e *graphEntry, nodeID, reason string) {
	nodes := t.contexts[e.index].Nodes
	for i := range nodes {
		if nodes[i].NodeID == nodeID {
			nodes[i].State = LiveStateUnknown
			nodes[i].Reason = reason
		}
	}
}

func (t *graphTraversal) unknownPanel(e *graphEntry, gid string) GroupPanel {
	p := GroupPanel{
		InstanceID:     e.inst.ID,
		NodeID:         gid,
		State:          GroupPanelStateUnknown,
		Tasks:          []GroupPanelTask{},
		WaitingReasons: []string{},
	}
	if scope := e.gg.Groups[gid]; scope != nil {
		cfg := scope.Config
		p.Form, p.CompletionRule, p.UnfinishedBehavior, p.GatherMode = cfg.Form, cfg.CompletionRule, cfg.UnfinishedBehavior, cfg.GatherMode
		for _, task := range cfg.Tasks {
			kind, origin := string(instance.GroupTaskKindWorkflow), task.Workflow
			if cfg.Form == workflow.GroupFormBranches {
				kind, origin = string(instance.GroupTaskKindBranch), task.Entry
			}
			p.Tasks = append(p.Tasks, GroupPanelTask{Key: task.Key, Kind: kind, Origin: origin, LiveStatus: LiveStateUnknown})
		}
	}
	p.Progress.Total = len(p.Tasks)
	t.warn(fmt.Sprintf("parallel group record for node %s of run %s is unavailable", gid, e.inst.ID))
	t.markNodeUnknown(e, gid, "group record unavailable")
	return p
}

func (t *graphTraversal) panelFor(run instance.GroupRun) GroupPanel {
	tasks := append([]instance.GroupTask(nil), run.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Position < tasks[j].Position })
	frozen := map[string]instance.GroupTaskStatus{}
	if run.Decision != nil {
		for _, ft := range run.Decision.Tasks {
			frozen[ft.Key] = ft.Status
		}
	}
	p := GroupPanel{
		GroupRunID:         run.ID,
		InstanceID:         run.ParentInstanceID,
		NodeID:             run.NodeID,
		Form:               run.Form,
		CompletionRule:     run.CompletionRule,
		UnfinishedBehavior: run.UnfinishedBehavior,
		GatherMode:         run.GatherMode,
		State:              string(run.State),
		Tasks:              make([]GroupPanelTask, 0, len(tasks)),
	}
	hiddenKeys := map[string]bool{}
	for _, task := range tasks {
		card := t.taskCard(task)
		if fs, ok := frozen[task.Key]; ok {
			card.FrozenStatus = string(fs)
		}
		if card.Redacted {
			hiddenKeys[task.Key] = true
		}
		countProgress(&p.Progress, card.LiveStatus)
		p.Tasks = append(p.Tasks, card)
	}
	p.Progress.Total = len(p.Tasks)
	if run.Decision != nil {
		p.Winner = run.Decision.WinnerKey
		p.Decision = redactDecision(run.Decision, hiddenKeys)
	}
	if run.FinalOutput != nil {
		p.FinalOutput = redactGroupOutput(run.FinalOutput, hiddenKeys)
	}
	if run.Gather != nil {
		g := *run.Gather
		g.Handle = nil // staging / parent directories are server paths
		g.Tasks = append([]instance.GatherTaskReport(nil), g.Tasks...)
		g.Attempts = append([]instance.ResolutionAttempt(nil), g.Attempts...)
		g.ActiveSession = nil
		if s := run.Gather.ActiveSession; s != nil && t.sessionVisible(run, *s) {
			ref := *s
			g.ActiveSession = &ref
			link := ref
			p.ActiveSession = &link
		}
		p.Gather = &g
	}
	p.WaitingReasons = waitingReasons(run, p.Tasks)
	return p
}

// taskCard derives one card. Live status comes from the authorized child
// instance; a hidden child yields a redacted card carrying only the parent's
// own roster status; a child that was never traversed falls back to the
// roster only when the roster status is terminal.
func (t *graphTraversal) taskCard(task instance.GroupTask) GroupPanelTask {
	card := GroupPanelTask{
		Key:                     task.Key,
		Kind:                    string(task.Kind),
		Origin:                  task.Origin,
		Blocking:                task.Blocking,
		ContinuingIndependently: task.ContinuingIndependently,
		CancelRequested:         task.CancelRequested,
	}
	if task.ChildInstanceID != uuid.Nil {
		if child, ok := t.visible[task.ChildInstanceID]; ok {
			id := task.ChildInstanceID
			card.ChildInstanceID = &id
			card.LiveStatus = string(instance.GroupTaskStatusFromState(child.State))
			card.LaunchError = task.LaunchError
			return card
		}
		if t.hidden[task.ChildInstanceID] {
			card.Redacted = true
			card.LiveStatus = rosterLiveStatus(task)
			return card
		}
	}
	card.LiveStatus = rosterFallbackStatus(task)
	card.LaunchError = task.LaunchError
	return card
}

func rosterLiveStatus(task instance.GroupTask) string {
	switch task.LaunchState {
	case instance.GroupTaskPending:
		return string(instance.GroupTaskStatusNotStarted)
	case instance.GroupTaskLaunchFailed:
		return string(instance.GroupTaskStatusFailed)
	}
	if task.LiveStatus != "" {
		return string(task.LiveStatus)
	}
	return LiveStateUnknown
}

func rosterFallbackStatus(task instance.GroupTask) string {
	switch task.LaunchState {
	case instance.GroupTaskPending:
		return string(instance.GroupTaskStatusNotStarted)
	case instance.GroupTaskLaunchFailed:
		return string(instance.GroupTaskStatusFailed)
	}
	if isTerminalTaskStatus(string(task.LiveStatus)) {
		return string(task.LiveStatus)
	}
	return LiveStateUnknown
}

func isTerminalTaskStatus(s string) bool {
	switch instance.GroupTaskStatus(s) {
	case instance.GroupTaskStatusCompleted, instance.GroupTaskStatusFailed, instance.GroupTaskStatusCancelled:
		return true
	}
	return false
}

func countProgress(p *GroupProgress, status string) {
	switch instance.GroupTaskStatus(status) {
	case instance.GroupTaskStatusCompleted:
		p.Completed++
	case instance.GroupTaskStatusFailed:
		p.Failed++
	case instance.GroupTaskStatusCancelled:
		p.Cancelled++
	case instance.GroupTaskStatusNotStarted:
		p.NotStarted++
	case instance.GroupTaskStatusQueued, instance.GroupTaskStatusRunning, instance.GroupTaskStatusWaiting, instance.GroupTaskStatusPaused:
		p.Active++
	}
}

// waitingReasons explains exactly what the gather is waiting on.
func waitingReasons(run instance.GroupRun, cards []GroupPanelTask) []string {
	var outstanding, cancelling []string
	for _, c := range cards {
		terminal := isTerminalTaskStatus(c.LiveStatus)
		if c.Blocking && !terminal {
			outstanding = append(outstanding, c.Key)
		}
		if c.CancelRequested && !terminal {
			cancelling = append(cancelling, c.Key)
		}
	}
	files := run.GatherMode == workflow.GroupGatherResultsAndFiles
	reasons := []string{}
	if run.State == instance.GroupRunLaunching {
		reasons = append(reasons, WaitingLaunching)
	}
	if run.State == instance.GroupRunRunning && len(outstanding) > 0 {
		reasons = append(reasons, WaitingForTasksPrefix+strings.Join(outstanding, ","))
	}
	if len(cancelling) > 0 {
		reasons = append(reasons, WaitingCancellingPrefix+strings.Join(cancelling, ","))
	}
	switch run.State {
	case instance.GroupRunDecided:
		if files {
			reasons = append(reasons, WaitingPreparingFiles)
		}
	case instance.GroupRunGathering:
		reasons = append(reasons, WaitingGathering)
	case instance.GroupRunResolvingConflicts:
		reasons = append(reasons, WaitingResolvingConflicts)
	}
	if run.Gather != nil && run.Gather.RollbackStatus == rollbackFailedStatus {
		reasons = append(reasons, WaitingRollbackFailed)
	}
	pending := false
	switch run.State {
	case instance.GroupRunRunning:
		pending = len(outstanding) == 0 // every blocking task finished, no decision yet
	case instance.GroupRunDecided:
		pending = !files // results-mode finalize not yet applied
	case instance.GroupRunDone:
		pending = !run.ContinuationDelivered
	}
	if pending {
		reasons = append(reasons, WaitingContinuationPending)
	}
	return reasons
}

func (t *graphTraversal) sessionVisible(run instance.GroupRun, ref instance.ResolutionSessionRef) bool {
	if ref.InstanceID == run.ParentInstanceID {
		return true
	}
	_, ok := t.visible[ref.InstanceID]
	return ok
}

// redactDecision deep-copies the frozen decision and strips the results,
// failures and child ids of hidden tasks. The stored record is never mutated.
func redactDecision(d *instance.GroupDecision, hidden map[string]bool) *instance.GroupDecision {
	cp := *d
	cp.Tasks = make([]instance.FrozenTask, len(d.Tasks))
	for i, ft := range d.Tasks {
		ft.Result = variable.DeepCopyMap(ft.Result)
		ft.Failure = variable.DeepCopyMap(ft.Failure)
		if hidden[ft.Key] {
			ft.Result, ft.Failure, ft.ChildInstanceID = nil, nil, ""
		}
		cp.Tasks[i] = ft
	}
	return &cp
}

// redactGroupOutput deep-copies the frozen group output (BuildGroupOutput
// shape) and scrubs hidden tasks' entries in "tasks" and "byKey".
func redactGroupOutput(out map[string]any, hidden map[string]bool) map[string]any {
	cp := variable.DeepCopyMap(out)
	if len(hidden) == 0 {
		return cp
	}
	scrub := func(m map[string]any) {
		delete(m, "result")
		delete(m, "failure")
		m["childInstanceId"] = ""
	}
	if tasks, ok := cp["tasks"].([]any); ok {
		for _, raw := range tasks {
			if m, ok := raw.(map[string]any); ok {
				if key, _ := m["key"].(string); hidden[key] {
					scrub(m)
				}
			}
		}
	}
	if byKey, ok := cp["byKey"].(map[string]any); ok {
		for key, raw := range byKey {
			if m, ok := raw.(map[string]any); ok && hidden[key] {
				scrub(m)
			}
		}
	}
	return cp
}
```

- [x] Step 4: Call it from `GraphState`. In `domain/engine/graph_state.go`, replace the hook line

```go
	groups := []GroupPanel{}
```
with
```go
	groups := t.buildGroupPanels()
```
(It runs before `gs.Warnings`, `gs.Active` and `gs.Version` are computed, so its warnings and node-state corrections are included in all three.)

- [x] Step 5: Run the new tests, confirm they pass; then the whole package (P4-T1's tests must stay green).

Completion pass after P4-T3: the production wiring blocker is resolved. `initDomain` constructs the reader with the real engine and SQLite repositories; `routerDeps` exposes it to the graph-state HTTP endpoint. The existing `TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot` passes with `-race`, asserting concurrent branch panels, exact waiting reasons, frozen task statuses and nonempty alpha/beta tool results in the HTTP JSON sink. No new implementation or tests were needed; this pass completes the previously blocked verification step.

Re-ran every specified Step 5 command: focused panels, reader/panel tests, full engine package, formatting and vet all pass. Also ran the focused reader/panel suite with `-race`, green.

Completion self-review (in order):
1. Spec compliance: reviewed `graph_state_groups.go` against the task's ordered panels, latest attempts, live/frozen split, waiting vocabulary, unavailable records, redaction and gather/session contracts. No missing slice or extra feature was found; no production changes were made.
2. Anti-green-theater: temporarily removed only `GraphState`'s panel hook. The real composition-root HTTP sink test failed because the required live panels were absent. Restored the hook and reran the same test with `-race`, green; its completed-run assertions also verify actual child tool output in both the frozen decision and final report. No mutation remains.
3. Code quality: checked the neighboring reader, `BuildGroupOutput` and `GatherReport.Clone`; existing deep-copy and authorization patterns remain appropriate. After review, reran reader/panel race tests, the real-root race test, the whole engine package, formatting, vet and diff checks, all green.

> RESOLVED by P4-T3; historical blocker: Production-root completion depends on P4-T3: `initDomain` does not yet construct `NewGraphStateReader`, and the HTTP graph-state endpoint/router sink does not exist. The panel hook is connected to the aggregate reader and fed real Engine/SQLite records in the sink test, but that reader is constructed in the test, not the production root. Leave completion open until P4-T3 lands; do not duplicate its composition/router slice here. P4-T1's previously recorded wiring blocker remains unchanged; its verification was not re-failed. P4-T3 is the next independent task.

All specified test/format/vet commands passed. After self-review, reran focused graph-state/panel tests with `-race`, the full non-race engine package, formatting, vet and diff checks, all green.

Self-review (in order):
1. Spec compliance: implemented the planned panel hook, ordered/latest roster views, live/frozen split, exact waiting reasons, unknown reached groups, redaction and gather/session history. Added red-to-green regressions for transient child-read unknown status/recovery, missing-record refresh liveness, untraversed frozen child-data redaction, frozen gather handle/session redaction and deep-copy isolation. These close required read-model contracts, without adding options or taking P4-T3's wiring slice.
2. Anti-green-theater: strengthened `TestGraphStateReader_EngineAndSQLiteProduceAggregateSink` to assert panels from actual launched tasks, exact live IDs/status/progress/wait reasons, and nonempty frozen results produced by real child execution. Removing only the panel hook made this test fail (zero panels); restoring it passed. No mutation remains. Production-root proof remains explicitly blocked above.
3. Code quality: read neighboring reader, decision/output and gather-report implementations; reused `GatherReport.Clone`, `GroupTaskStatus.IsTerminal` and `RollbackStatusFailed` instead of shallow copies/duplicate vocabulary. Formatting and vet are clean; focused race and whole-package tests pass after fixes.

```bash
go test ./domain/engine/ -run 'TestGroupPanels' -count=1
go test ./domain/engine/ -run 'TestGraphState|TestNewGraphStateReader|TestGroupPanels' -count=1
go test ./domain/engine/ -count=1
gofmt -w domain/engine/graph_state.go domain/engine/graph_state_groups.go domain/engine/graph_state_groups_test.go
gofmt -l domain/engine/graph_state.go domain/engine/graph_state_groups.go domain/engine/graph_state_groups_test.go
go vet ./domain/engine/
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 6: Commit.

Initial pass committed the unblocked implementation, tests and precise completion blocker. This completion pass closes Step 5 after P4-T3's production wiring landed; the follow-up commit records verification and self-review only.

```bash
git add domain/engine/graph_state.go domain/engine/graph_state_groups.go domain/engine/graph_state_groups_test.go
git commit -m "feat(engine): group panels with live vs frozen status, waiting reasons and gather history" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
