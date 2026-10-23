package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gatherParentDir = "/ws/parent-1"

func taskRef(key string) string { return "refs/arbitron/tasks/" + key }

type gatherFixture struct {
	t        *testing.T
	groups   *memGroupRuns
	area     *fakeGatherArea
	starter  *fakeResolutionStarter
	bus      *mockEventBus
	gatherer *engine.FileGatherer
	runID    uuid.UUID
	parent   *instance.Instance
}

// newGatherFixture persists a DECIDED results_and_files run whose tasks are
// keys in AUTHOR order. Each task's finalized ref changes "<key>.txt" to
// "from-<key>". Tasks complete in the REVERSE of author order (TerminalSeq),
// so any gatherer that merges by completion order fails the order assertions.
// The parent's working area starts with base.txt = "base".
func newGatherFixture(t *testing.T, rule, outcome, winner string, keys ...string) *gatherFixture {
	t.Helper()
	groups := newMemGroupRuns()
	area := newFakeGatherArea()
	starter := &fakeResolutionStarter{}
	bus := newMockEventBus()

	parentID := uuid.New()
	behavior := workflow.GroupUnfinishedWaitForRemaining
	if rule == workflow.GroupRuleFirstSuccess {
		behavior = workflow.GroupUnfinishedFinishIndependently
	}
	run := &instance.GroupRun{
		ID:                 uuid.New(),
		ParentInstanceID:   parentID,
		NodeID:             "g1",
		Attempt:            1,
		Form:               workflow.GroupFormBranches,
		CompletionRule:     rule,
		UnfinishedBehavior: behavior,
		GatherMode:         workflow.GroupGatherResultsAndFiles,
		Config: map[string]any{
			"form":               workflow.GroupFormBranches,
			"completionRule":     rule,
			"unfinishedBehavior": behavior,
			"gatherMode":         workflow.GroupGatherResultsAndFiles,
			"resolverProfile":    "resolver",
		},
		WorkspaceSnapshot: "snap-1",
		State:             instance.GroupRunDecided,
		ActionsApplied:    true,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	frozen := make([]instance.FrozenTask, 0, len(keys))
	for i, k := range keys {
		seq := int64(len(keys) - i)
		at := time.Now().UTC()
		child := uuid.New()
		run.Tasks = append(run.Tasks, instance.GroupTask{
			GroupRunID:      run.ID,
			Position:        i,
			Key:             k,
			Kind:            instance.GroupTaskKindBranch,
			Origin:          k + "-entry",
			ChildInstanceID: child,
			LaunchState:     instance.GroupTaskLaunched,
			LiveStatus:      instance.GroupTaskStatusCompleted,
			TerminalSeq:     &seq,
			TerminalAt:      &at,
			Result:          map[string]any{"k": k},
			FinalizedRef:    taskRef(k),
		})
		frozen = append(frozen, instance.FrozenTask{
			Key:             k,
			Kind:            instance.GroupTaskKindBranch,
			Origin:          k + "-entry",
			ChildInstanceID: child.String(),
			Status:          instance.GroupTaskStatusCompleted,
			Result:          map[string]any{"k": k},
			Winner:          k == winner,
		})
		area.refs[taskRef(k)] = map[string]string{k + ".txt": "from-" + k}
	}
	run.Decision = &instance.GroupDecision{
		Outcome:   outcome,
		Rule:      rule,
		Behavior:  behavior,
		WinnerKey: winner,
		Reason:    "decided for test",
		DecidedAt: time.Now().UTC(),
		Tasks:     frozen,
	}
	groups.put(run)
	area.parents[gatherParentDir] = map[string]string{"base.txt": "base"}

	parent := &instance.Instance{
		ID:           parentID,
		OwnerID:      uuid.New(),
		WorkflowName: "wf",
		State:        instance.StateRunning,
		Variables:    map[string]any{"workspace": gatherParentDir},
	}
	return &gatherFixture{
		t: t, groups: groups, area: area, starter: starter, bus: bus,
		gatherer: engine.NewGroupGatherer(groups, area, starter, bus),
		runID:    run.ID, parent: parent,
	}
}

func (f *gatherFixture) gather() (*engine.GatherStep, error) {
	f.t.Helper()
	return f.gatherer.Gather(context.Background(), f.groups.snapshot(f.runID), f.parent)
}

func (f *gatherFixture) report() *instance.GatherReport {
	f.t.Helper()
	run := f.groups.snapshot(f.runID)
	require.NotNil(f.t, run.Gather, "the gather report must be persisted")
	return run.Gather
}

// endOpenAttempts stands in for the completer (P3-T8): it records that the
// open resolution attempt ended, with outcome ("" = an ordinary end that the
// gatherer must verify).
func (f *gatherFixture) endOpenAttempts(outcome string) {
	f.t.Helper()
	run := f.groups.snapshot(f.runID)
	require.NotNil(f.t, run.Gather)
	rep := *run.Gather
	now := time.Now().UTC()
	for i := range rep.Attempts {
		if rep.Attempts[i].EndedAt == nil {
			rep.Attempts[i].EndedAt = &now
			rep.Attempts[i].Outcome = outcome
		}
	}
	require.NoError(f.t, f.groups.SetGatherReport(context.Background(), f.runID, rep))
}

// conflictOnB scripts tasks a and b both editing shared.txt, with b's merge
// conflicting (a is merged first, cleanly, in author order).
func (f *gatherFixture) conflictOnB() {
	f.area.parents[gatherParentDir]["shared.txt"] = "base"
	f.area.refs[taskRef("a")]["shared.txt"] = "from-a"
	f.area.refs[taskRef("b")]["shared.txt"] = "from-b"
	f.area.conflicts[taskRef("b")] = []string{"shared.txt"}
}

func gatherSection(t *testing.T, step *engine.GatherStep) map[string]any {
	t.Helper()
	require.NotNil(t, step)
	g, ok := step.Output["gather"].(map[string]any)
	require.True(t, ok, "output.gather must be the gather report map, got %T", step.Output["gather"])
	return g
}

func taskReportByKey(t *testing.T, r *instance.GatherReport, key string) instance.GatherTaskReport {
	t.Helper()
	for _, tr := range r.Tasks {
		if tr.Key == key {
			return tr
		}
	}
	t.Fatalf("no task report for %q", key)
	return instance.GatherTaskReport{}
}

// --- Happy path -------------------------------------------------------------

func TestFileGatherer_AllSucceed_MergesEveryTaskInAuthorOrderAndApplies(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)

	assert.Equal(t, []string{taskRef("a"), taskRef("b"), taskRef("c")}, f.area.mergeOrder(),
		"merges follow AUTHOR order even though tasks completed c, b, a")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b", "c.txt": "from-c"},
		f.area.parentFiles(gatherParentDir), "SINK: the parent's working area holds every task's change")
	assert.Equal(t, 1, f.area.applyCount())
	assert.Zero(t, f.area.rollbackCount())

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortSuccess, run.ContinuationPort)
	assert.True(t, run.ContinuationReady)
	rep := f.report()
	assert.Equal(t, "applied", rep.State)
	assert.Equal(t, workflow.GroupGatherResultsAndFiles, rep.Mode)
	assert.Equal(t, 3, rep.MergeIndex)
	require.Len(t, rep.Tasks, 3)
	for i, key := range []string{"a", "b", "c"} {
		assert.Equal(t, key, rep.Tasks[i].Key, "report tasks are in author order")
		assert.True(t, rep.Tasks[i].Merged)
		assert.Equal(t, "merged cleanly", rep.Tasks[i].Reason)
	}

	g := gatherSection(t, step)
	assert.Equal(t, "applied", g["state"])
	assert.Equal(t, workflow.GroupGatherResultsAndFiles, g["mode"])
	assert.Equal(t, f.runID.String(), fmt.Sprint(step.Output["groupRunId"]), "the rest of the output is BuildGroupOutput")

	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupGathering), 1)
	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupCompleted), 1)
	assert.Empty(t, f.starter.calls(), "a clean gather opens no resolution session")
}

func TestFileGatherer_FirstSuccess_MergesOnlyTheWinner(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleFirstSuccess, workflow.GroupPortSuccess, "b", "a", "b", "c")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)

	assert.Equal(t, []string{taskRef("b")}, f.area.mergeOrder(), "first_success merges the winner only")
	assert.Equal(t, map[string]string{"base.txt": "base", "b.txt": "from-b"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.True(t, taskReportByKey(t, rep, "b").Merged)
	for _, loser := range []string{"a", "c"} {
		tr := taskReportByKey(t, rep, loser)
		assert.False(t, tr.Merged)
		assert.Contains(t, tr.Reason, "not the winner")
	}
}

// --- Failure ----------------------------------------------------------------

func TestFileGatherer_FailedDecision_MergesNothingAndRoutesFailure(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortFailure, "", "a", "b")
	decisionBefore := f.groups.snapshot(f.runID).Decision

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)

	assert.Empty(t, f.area.begins(), "a failed group never stages a gather")
	assert.Empty(t, f.area.mergeOrder())
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, "not_applicable", rep.State)
	for _, tr := range rep.Tasks {
		assert.False(t, tr.Merged)
		assert.Equal(t, "group failed: no tasks merged", tr.Reason)
	}
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.Equal(t, decisionBefore, run.Decision, "task-decision history is never rewritten")
	assert.Equal(t, "not_applicable", gatherSection(t, step)["state"])
}

func TestFileGatherer_MissingFinalizedRef_RoutesFailureWithReason(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Tasks[1].FinalizedRef = "" })

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Empty(t, f.area.begins(), "nothing is staged when an eligible task has no finalized ref")

	rep := f.report()
	assert.Equal(t, "failed", rep.State)
	assert.Contains(t, rep.Error, "task b has no finalized working-area ref")
	assert.Contains(t, taskReportByKey(t, rep, "a").Reason, "not merged")
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
}

func TestFileGatherer_ParentWithoutWorkspace_RoutesFailure(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.parent.Variables = map[string]any{}

	step, err := f.gather()
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Empty(t, f.area.begins())
	assert.Contains(t, f.report().Error, "no working area")
}

func TestFileGatherer_MergeError_RollsBackAndRoutesFailure(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	f.area.failMerge[taskRef("b")] = errors.New("index.lock exists")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Zero(t, f.area.applyCount())
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, "rolled_back", rep.State)
	assert.Equal(t, "rolled_back", rep.RollbackStatus)
	assert.Contains(t, rep.Error, "merge task b: index.lock exists")
	assert.Equal(t, false, taskReportByKey(t, rep, "a").Merged)
	assert.Contains(t, taskReportByKey(t, rep, "a").Reason, "rolled back:")
	assert.Contains(t, taskReportByKey(t, rep, "c").Reason, "not merged:")
}

func TestFileGatherer_ApplyError_RollsBackPartialApply(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failApplyAfterWrite = errors.New("disk full")

	step, err := f.gather()
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.applyCount())
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir),
		"SINK: a half-written apply is restored to the exact pre-gather content")
	assert.Contains(t, f.report().Error, "apply gathered changes to the parent working area: disk full")
}

// --- Conflicts / resolution sessions ---------------------------------------

func TestFileGatherer_Conflict_SuspendsInResolvingConflictsWithSession(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	f.conflictOnB()

	step, err := f.gather()
	require.NoError(t, err)
	require.False(t, step.Done, "a conflict suspends the gather")

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunResolvingConflicts, run.State)
	assert.Nil(t, run.FinalOutput)
	rep := f.report()
	assert.Equal(t, "resolving", rep.State)
	assert.Equal(t, 1, rep.MergeIndex, "a is merged; b is the conflicting merge")
	require.Len(t, rep.Attempts, 1)
	att := rep.Attempts[0]
	assert.Equal(t, 1, att.MergeIndex)
	assert.Equal(t, 1, att.Attempt)
	assert.Equal(t, "b", att.TaskKey)
	assert.Equal(t, []string{"shared.txt"}, att.ConflictPaths)
	assert.Nil(t, att.EndedAt)
	require.NotNil(t, rep.ActiveSession)
	assert.Equal(t, instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: f.parent.ID, NodeID: "g1"}, *rep.ActiveSession)
	assert.Equal(t, []string{"shared.txt"}, taskReportByKey(t, rep, "b").ConflictPaths)

	calls := f.starter.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, f.runID, calls[0].runID)
	assert.Equal(t, f.parent.ID, calls[0].parentID)
	assert.Equal(t, "b", calls[0].req.TaskKey)
	assert.Equal(t, taskRef("b"), calls[0].req.TaskRef)
	assert.Equal(t, []string{"shared.txt"}, calls[0].req.ConflictPaths)
	begin := f.area.begins()[0]
	assert.Equal(t, "/staging/"+begin.gatherID.String(), calls[0].req.Handle.StagingDir, "the session works in the STAGING dir")
	assert.Equal(t, gatherParentDir, calls[0].req.Handle.ParentDir)

	assert.Zero(t, f.area.applyCount(), "nothing reaches the parent while conflicts are open")
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir))
}

func TestFileGatherer_ReEntryWhileSessionOpen_StaysWaiting(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)
	before := f.report()

	step, err := f.gather()
	require.NoError(t, err)
	assert.False(t, step.Done)
	assert.Len(t, f.starter.calls(), 1, "an open session is never reopened")
	assert.Equal(t, before, f.report())
}

func TestFileGatherer_ReEntryStillConflicted_OpensFreshAttempt(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)
	f.endOpenAttempts("") // the operator ended the session without fixing shared.txt

	step, err := f.gather()
	require.NoError(t, err)
	assert.False(t, step.Done, "still-conflicted completion keeps the parent blocked")
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State)

	rep := f.report()
	require.Len(t, rep.Attempts, 2)
	assert.Equal(t, engine.ResolutionOutcomeStillConflicted, rep.Attempts[0].Outcome)
	assert.Equal(t, 2, rep.Attempts[1].Attempt)
	assert.Equal(t, 1, rep.Attempts[1].MergeIndex)
	assert.Nil(t, rep.Attempts[1].EndedAt)
	require.NotNil(t, rep.ActiveSession)
	assert.Equal(t, 2, rep.ActiveSession.Attempt)
	calls := f.starter.calls()
	require.Len(t, calls, 2)
	assert.Equal(t, 2, calls[1].req.Attempt)
	assert.Zero(t, f.area.applyCount())
}

func TestFileGatherer_ReEntryResolved_CommitsAndContinuesToApply(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)
	f.area.resolveStaged("shared.txt", "merged-a-and-b")
	f.endOpenAttempts("")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, []string{taskRef("a"), taskRef("b"), taskRef("c")}, f.area.mergeOrder(),
		"after the resolution the gather continues with the NEXT task")
	assert.Equal(t, map[string]string{
		"base.txt": "base", "shared.txt": "merged-a-and-b",
		"a.txt": "from-a", "b.txt": "from-b", "c.txt": "from-c",
	}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, engine.ResolutionOutcomeResolved, rep.Attempts[0].Outcome)
	assert.Equal(t, "merged after conflict resolution (attempt 1)", taskReportByKey(t, rep, "b").Reason)
	assert.Nil(t, rep.ActiveSession)
	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupResolved), 1)
}

func TestFileGatherer_Abandoned_RollsBackAndRoutesFailureKeepingDecision(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	decisionBefore := f.groups.snapshot(f.runID).Decision
	_, err := f.gather()
	require.NoError(t, err)
	f.endOpenAttempts(engine.ResolutionOutcomeAbandoned)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, "rolled_back", rep.State)
	assert.Equal(t, "rolled_back", rep.RollbackStatus)
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, rep.Attempts[0].Outcome)
	assert.Contains(t, rep.Error, "conflict resolution for task b was abandoned")
	a := taskReportByKey(t, rep, "a")
	assert.False(t, a.Merged)
	assert.True(t, strings.HasPrefix(a.Reason, "rolled back:"))

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, decisionBefore, run.Decision)
	assert.Equal(t, workflow.GroupPortFailure, step.Output["outcome"])
	assert.Equal(t, workflow.GroupPortSuccess, step.Output["taskOutcome"], "the task decision remains successful")
}

func TestFileGatherer_SessionStartError_RollsBackAndRecordsErrorAttempt(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	f.starter.err = errors.New("resolver profile offline")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	rep := f.report()
	require.Len(t, rep.Attempts, 1)
	assert.Equal(t, engine.ResolutionOutcomeError, rep.Attempts[0].Outcome)
	assert.NotNil(t, rep.Attempts[0].EndedAt)
	assert.Contains(t, rep.Error, "start conflict resolution for task b: resolver profile offline")
}

func TestFileGatherer_NilBridge_ConflictRollsBack(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	g := engine.NewGroupGatherer(f.groups, f.area, nil, f.bus)

	step, err := g.Gather(context.Background(), f.groups.snapshot(f.runID), f.parent)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Contains(t, f.report().Error, "no conflict-resolution bridge is configured")
}

// --- Error: rollback failure blocks continuation ---------------------------

func TestFileGatherer_RollbackFailure_ReportsAndBlocksContinuationUntilRepaired(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failMerge[taskRef("b")] = errors.New("merge exploded")
	f.area.failRollback = errors.New("permission denied")

	step, err := f.gather()
	require.Error(t, err)
	assert.Nil(t, step)
	assert.ErrorIs(t, err, engine.ErrGroupGatherRollbackFailed)

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunGathering, run.State, "no continuation while the parent's area is unrestored")
	assert.Nil(t, run.FinalOutput)
	rep := f.report()
	assert.Equal(t, "rollback_failed", rep.RollbackStatus)
	assert.Equal(t, "failed", rep.State)
	assert.Contains(t, rep.Error, "rollback failed: permission denied")

	// Repaired: the next re-entry retries the rollback and only then routes failure.
	f.area.failRollback = nil
	step, err = f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 2, f.area.rollbackCount())
	rep = f.report()
	assert.Equal(t, "rolled_back", rep.RollbackStatus)
	assert.Equal(t, "merge task b: merge exploded", rep.Error, "the original abort reason survives the retry")
}

// --- Edge: crash boundaries -------------------------------------------------

func TestFileGatherer_ResumeAfterCrashMidMerge_DoesNotDoubleApply(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	// SetGatherReport calls: #1 merging, #2 handle, #3 after a, #4 after b.
	// Failing #4 models a crash after b entered the staged tree but before its
	// progress was persisted.
	f.groups.failCall("ReplaceGatherReport", 4, errors.New("database is locked"))

	_, err := f.gather()
	require.Error(t, err)
	assert.Equal(t, 1, f.report().MergeIndex, "persisted progress lags the staged tree by one merge")
	assert.Equal(t, instance.GroupRunGathering, f.groups.snapshot(f.runID).State)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Len(t, f.area.begins(), 1, "resume reuses the persisted handle")
	assert.Equal(t, []string{taskRef("a"), taskRef("b"), taskRef("b"), taskRef("c")}, f.area.mergeOrder())
	assert.Equal(t, 1, f.area.timesApplied(taskRef("b")), "b's delta entered the staged tree exactly once")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b", "c.txt": "from-c"},
		f.area.parentFiles(gatherParentDir))
	assert.Equal(t, 1, f.area.applyCount())
}

func TestFileGatherer_ResumeAfterCrashBeforeHandlePersisted_StagesAfresh(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.groups.failCall("ReplaceGatherReport", 2, errors.New("database is locked"))

	_, err := f.gather()
	require.Error(t, err)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Len(t, f.area.begins(), 2, "with no persisted handle the gather starts a fresh staging area")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}, f.area.parentFiles(gatherParentDir))
}

func TestFileGatherer_ResumeAfterApplyBeforeFinalOutput_FinishesWithoutReapplying(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.groups.failCall("SetFinalOutput", 1, errors.New("database is locked"))

	_, err := f.gather()
	require.Error(t, err)
	assert.Equal(t, "applied", f.report().State)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, 1, f.area.applyCount(), "an applied report finishes without another Apply")
}

func TestFileGatherer_DoneRunReplaysStoredOutput(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.groups.update(f.runID, func(r *instance.GroupRun) {
		r.State = instance.GroupRunDone
		r.ContinuationPort = workflow.GroupPortSuccess
		r.FinalOutput = map[string]any{"replayed": true}
	})

	step, err := f.gather()
	require.NoError(t, err)
	assert.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, true, step.Output["replayed"])
	assert.Empty(t, f.area.begins())
}

func TestFileGatherer_RejectsResultsModeAndUndecidedRuns(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")

	results := f.groups.snapshot(f.runID)
	results.GatherMode = workflow.GroupGatherResults
	_, err := f.gatherer.Gather(context.Background(), results, f.parent)
	assert.ErrorContains(t, err, "only \"results_and_files\" gathers files")

	running := f.groups.snapshot(f.runID)
	running.State = instance.GroupRunRunning
	running.Decision = nil
	_, err = f.gatherer.Gather(context.Background(), running, f.parent)
	assert.ErrorContains(t, err, "has no decision")

	_, err = f.gatherer.Gather(context.Background(), nil, f.parent)
	assert.Error(t, err)
}

// --- AbortGather (reconciler entry point) ----------------------------------

func TestFileGatherer_AbortGather_CancelledParentMidSessionRollsBackAndSettles(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)

	step, err := f.gatherer.AbortGather(context.Background(), f.groups.snapshot(f.runID),
		engine.ResolutionOutcomeCancelled, "parent instance was cancelled")
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	rep := f.report()
	assert.Equal(t, engine.ResolutionOutcomeCancelled, rep.Attempts[0].Outcome)
	assert.NotNil(t, rep.Attempts[0].EndedAt)
	assert.Nil(t, rep.ActiveSession)
	assert.Equal(t, "parent instance was cancelled", rep.Error)
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
}

func TestFileGatherer_AbortGather_FromDecidedStagesNothing(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")

	step, err := f.gatherer.AbortGather(context.Background(), f.groups.snapshot(f.runID),
		engine.ResolutionOutcomeCancelled, "parent instance was cancelled")
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Empty(t, f.area.begins())
	assert.Zero(t, f.area.rollbackCount())
	assert.Equal(t, "failed", f.report().State)
}

// --- Provenance -------------------------------------------------------------

func TestFileGatherer_Provenance_ParentDirComesFromParentWorkspaceVariable(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.parent.Variables["workspace"] = "/ws/other-parent"
	f.area.parents["/ws/other-parent"] = map[string]string{"x.txt": "x"}

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	require.Len(t, f.area.begins(), 1)
	assert.Equal(t, "/ws/other-parent", f.area.begins()[0].parentDir)
	assert.Equal(t, "snap-1", f.area.begins()[0].snapshot, "the run's persisted snapshot handle is the baseline")
	assert.Equal(t, map[string]string{"x.txt": "x", "a.txt": "from-a"}, f.area.parentFiles("/ws/other-parent"))
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir), "no other directory is touched")
}

func TestGatherHandleFromReport_RoundTrip(t *testing.T) {
	id := uuid.New()
	rep := &instance.GatherReport{Handle: map[string]any{
		"snapshotHandle": "snap-1", "gatherId": id.String(), "stagingDir": "/staging/x",
		"rollbackRef": "refs/r", "parentDir": "/ws/p",
	}}
	h, ok := engine.GatherHandleFromReport(rep)
	require.True(t, ok)
	assert.Equal(t, engine.GatherHandle{SnapshotHandle: "snap-1", GatherID: id, StagingDir: "/staging/x", RollbackRef: "refs/r", ParentDir: "/ws/p"}, h)

	_, ok = engine.GatherHandleFromReport(&instance.GatherReport{})
	assert.False(t, ok)
	_, ok = engine.GatherHandleFromReport(nil)
	assert.False(t, ok)
}

func TestFileGatherer_EngineRoutesGatherFailureToDownstreamSink(t *testing.T) {
	cfg := gxBranches()
	cfg.GatherMode, cfg.ResolverProfile = workflow.GroupGatherResultsAndFiles, "resolver"
	f := newGxFixture(t, gxWorkflowWith(cfg, true), "g")
	gf := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	run := gf.groups.snapshot(gf.runID)
	run.ParentInstanceID, run.NodeID = f.parent.ID, "g"
	f.groups.put(run)
	gf.area.failApplyAfterWrite = errors.New("apply interrupted")
	f.x.SetGatherer(engine.NewGroupGatherer(f.groups, gf.area, gf.starter, f.bus))
	exec := newMockExecutor()
	exec.onNode("fail", func(_ context.Context, _ workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		outcome, _ := vars.Get("g.outcome")
		taskOutcome, _ := vars.Get("g.taskOutcome")
		return &engine.NodeResult{Output: map[string]any{"outcome": outcome, "taskOutcome": taskOutcome}}, nil
	})
	eng := wireModelPins(engine.NewEngine(newMockWorkflowRepo(), f.instances, f.nodeExecs, newMockSnapshotRepo(), newMockUserRepo(),
		map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: exec, workflow.NodeTypeParallelGroup: f.x},
		f.bus, f.qm, &mockQueueRepo{instRepo: f.instances}, ""))
	t.Cleanup(eng.Shutdown)
	require.NoError(t, f.instances.UpdateVariables(context.Background(), f.parent.ID, map[string]any{"workspace": gatherParentDir}))
	require.NoError(t, f.instances.UpdateState(context.Background(), f.parent.ID, instance.StateQueued))
	require.NoError(t, eng.StartInstance(context.Background(), f.parent.ID))
	parent := waitForInstance(t, f.instances, f.parent.ID, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, map[string]any{"outcome": "failure", "taskOutcome": "success"}, parent.Variables["fail"])
	assert.NotContains(t, parent.Variables, "after")
	assert.Equal(t, map[string]string{"base.txt": "base"}, gf.area.parentFiles(gatherParentDir))
}

func activeGatherRef(f *gatherFixture) instance.ResolutionSessionRef {
	return instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: f.parent.ID, NodeID: "g1"}
}

func TestFileGatherer_AutomaticResolutionContinuesWithoutHumanEnd(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	f.conflictOnB()
	f.starter.onStart = func(req engine.ResolutionRequest) {
		f.area.resolveStagedIn(req.Handle, "shared.txt", "automatically merged")
	}
	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, "automatically merged", f.area.parentFiles(gatherParentDir)["shared.txt"])
	assert.Equal(t, "from-c", f.area.parentFiles(gatherParentDir)["c.txt"])
	assert.Equal(t, 1, f.area.commits)
	assert.Equal(t, []instance.ResolutionSessionRef{activeGatherRef(f)}, f.starter.closed)
	assert.Empty(t, f.starter.notified)
	assert.Nil(t, f.report().ActiveSession)
	assert.Equal(t, engine.ResolutionOutcomeResolved, f.report().Attempts[0].Outcome)
}

func TestFileGatherer_UnverifiedClaimNeedsHumanThenContinuesOnce(t *testing.T) {
	// Different run/parent identities catch constant close/notification refs.
	for i := 0; i < 2; i++ {
		f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
		f.conflictOnB()
		f.starter.onStart = func(engine.ResolutionRequest) { /* an agent claim without a staged repair */ }
		before := f.area.parentFiles(gatherParentDir)
		step, err := f.gather()
		require.NoError(t, err)
		assert.False(t, step.Done)
		assert.Equal(t, before, f.area.parentFiles(gatherParentDir))
		assert.Nil(t, f.groups.snapshot(f.runID).FinalOutput)
		assert.Equal(t, []instance.ResolutionSessionRef{activeGatherRef(f)}, f.starter.notified)
		assert.Empty(t, f.starter.closed)
		_, err = f.gather()
		require.NoError(t, err)
		assert.Len(t, f.starter.notified, 1, "re-entry does not duplicate the human-needed notification")
		f.area.resolveStagedIn(f.starter.calls()[0].req.Handle, "shared.txt", "human repaired")
		f.endOpenAttempts("")
		step, err = f.gather()
		require.NoError(t, err)
		assert.True(t, step.Done)
		assert.Equal(t, "human repaired", f.area.parentFiles(gatherParentDir)["shared.txt"])
		_, err = f.gather()
		require.NoError(t, err)
		assert.Equal(t, 1, f.area.applyCount())
		assert.Len(t, f.starter.calls(), 1)
	}
}

func TestFileGatherer_AutomaticResolutionPersistenceBoundariesAreRecoverable(t *testing.T) {
	for _, boundary := range []string{"verify", "close", "notify", "active identity", "attempt end", "verified", "merge progress", "applied"} {
		t.Run(boundary, func(t *testing.T) {
			f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
			f.conflictOnB()
			f.starter.onStart = func(req engine.ResolutionRequest) {
				if boundary != "notify" {
					f.area.resolveStagedIn(req.Handle, "shared.txt", "fixed")
				}
			}
			boom := errors.New("injected persistence/verification failure")
			switch boundary {
			case "verify":
				f.area.failConflictPaths = boom
			case "close":
				f.starter.closeErr = boom
			case "notify":
				f.starter.notifyErr = boom
			case "active identity":
				f.groups.failCall("ReplaceGatherReport", 5, boom)
			case "attempt end":
				f.groups.failCall("ReplaceGatherReport", 6, boom)
			case "verified":
				f.groups.failCall("ReplaceGatherReport", 7, boom)
			case "merge progress":
				f.groups.failCall("ReplaceGatherReport", 8, boom)
			case "applied":
				f.groups.failCall("ReplaceGatherReport", 9, boom)
			}
			step, err := f.gather()
			require.ErrorIs(t, err, boom)
			assert.Nil(t, step)
			assert.Nil(t, f.groups.snapshot(f.runID).FinalOutput, "no continuation on any failed boundary")
			if boundary == "attempt end" {
				assert.Equal(t, []instance.ResolutionSessionRef{activeGatherRef(f)}, f.starter.closed)
				assert.Nil(t, f.report().Attempts[0].EndedAt, "the closed record is ahead of the durable report")
			}
			f.area.failConflictPaths = nil
			f.starter.closeErr, f.starter.notifyErr = nil, nil
			if boundary == "notify" {
				step, err = f.gather()
				require.NoError(t, err)
				assert.False(t, step.Done)
				assert.NotEmpty(t, f.starter.notified)
				f.area.resolveStagedIn(f.starter.calls()[0].req.Handle, "shared.txt", "fixed")
				f.endOpenAttempts("")
			}
			// A new gatherer simulates process restart with persisted report and staging.
			f.gatherer = engine.NewGroupGatherer(f.groups, f.area, f.starter, f.bus)
			step, err = f.gather()
			require.NoError(t, err)
			require.True(t, step.Done)
			assert.Equal(t, workflow.GroupPortSuccess, step.Port)
			assert.Equal(t, "fixed", f.area.parentFiles(gatherParentDir)["shared.txt"])
			assert.Len(t, f.starter.calls(), 1, "closed/open automatic session is not relaunched after restart")
			assert.Equal(t, 1, f.area.timesApplied(taskRef("b")))
			if boundary != "notify" {
				assert.Empty(t, f.starter.notified)
			}
		})
	}
}

func TestFileGatherer_AbortReportPersistenceFailureCannotTurnFailureIntoSuccess(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.area.failApplyAfterWrite = errors.New("partial apply failed")
	injected := false
	f.groups.onCall = func(method string) {
		if method == "ReplaceGatherReport" && f.area.rollbackCount() > 0 && !injected {
			injected = true
			f.groups.failOnce(method, errors.New("save rollback result failed"))
		}
	}
	_, err := f.gather()
	require.Error(t, err)
	assert.Nil(t, f.groups.snapshot(f.runID).FinalOutput)
	f.area.failApplyAfterWrite = nil
	f.gatherer = engine.NewGroupGatherer(f.groups, f.area, f.starter, f.bus)
	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port, "abort intent survives losing the rollback-result write")
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
	assert.Contains(t, f.report().Error, "partial apply failed")
}

func TestFileGatherer_AbortRecoveryWhileStillInResolvingState(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(fmt.Sprint(rollbackFails), func(t *testing.T) {
			f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
			f.conflictOnB()
			_, err := f.gather()
			require.NoError(t, err)
			if rollbackFails {
				f.area.failRollback = errors.New("restore permission denied")
			} else {
				f.groups.failCall("TransitionState", 1, errors.New("state update failed"))
			}
			_, err = f.gatherer.AbortGather(context.Background(), f.groups.snapshot(f.runID), engine.ResolutionOutcomeCancelled, "parent cancelled")
			require.Error(t, err)
			expectedState := instance.GroupRunResolvingConflicts
			if rollbackFails {
				expectedState = instance.GroupRunGathering
			}
			require.Equal(t, expectedState, f.groups.snapshot(f.runID).State)
			f.area.failRollback = nil
			f.gatherer = engine.NewGroupGatherer(f.groups, f.area, f.starter, f.bus)
			step, err := f.gather()
			require.NoError(t, err)
			require.True(t, step.Done)
			assert.Equal(t, workflow.GroupPortFailure, step.Port)
			assert.Equal(t, "parent cancelled", f.report().Error)
			assert.Equal(t, instance.RollbackStatusRolledBack, f.report().RollbackStatus)
			assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir))
		})
	}
}

func TestFileGatherer_DoneReplayDoesNotShareFrozenOutput(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	_, err := f.gather()
	require.NoError(t, err)
	run := f.groups.snapshot(f.runID)
	step, err := f.gatherer.Gather(context.Background(), run, f.parent)
	require.NoError(t, err)
	step.Output["gather"].(map[string]any)["state"] = "mutated"
	assert.Equal(t, "applied", run.FinalOutput["gather"].(map[string]any)["state"])
}

func TestGatherHandleFromReport_RejectsIncompleteHandles(t *testing.T) {
	full := map[string]any{"snapshotHandle": "s", "gatherId": uuid.New().String(), "stagingDir": "/stage", "rollbackRef": "refs/r", "parentDir": "/parent"}
	for key := range full {
		missing := map[string]any{}
		for k, v := range full {
			if k != key {
				missing[k] = v
			}
		}
		_, ok := engine.GatherHandleFromReport(&instance.GatherReport{Handle: missing})
		assert.False(t, ok, "missing %s", key)
	}
}

func TestFileGatherer_FinalFailureOutcomeMatchesRouteWithoutChangingDecision(t *testing.T) {
	for _, phase := range []string{"merge", "apply"} {
		t.Run(phase, func(t *testing.T) {
			f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
			decision := f.groups.snapshot(f.runID).Decision
			if phase == "merge" {
				f.area.failMerge[taskRef("b")] = errors.New("merge failed")
			} else {
				f.area.failApplyAfterWrite = errors.New("apply failed")
			}
			step, err := f.gather()
			require.NoError(t, err)
			require.True(t, step.Done)
			assert.Equal(t, workflow.GroupPortFailure, step.Port)
			assert.Equal(t, workflow.GroupPortFailure, step.Output["outcome"])
			assert.Equal(t, workflow.GroupPortSuccess, step.Output["taskOutcome"])
			assert.Equal(t, decision, f.groups.snapshot(f.runID).Decision)
			assert.Equal(t, instance.RollbackStatusRolledBack, f.report().RollbackStatus)
			assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
		})
	}
}

// Seam (paired:13): group.* frames are published on the PARENT and the subtree
// bridge (subtree_events.go:EventVisible) authorizes only that parent for
// them. A rollback-failed frame must therefore carry structured state only:
// the abort/rollback text names task diagnostics that graph_state_groups.go
// redactGatherReport withholds when any task is hidden. The frame still names
// the state so the client refetches the (redacted) panel instead of going stale.
func TestFileGatherer_RollbackFailedFrameCarriesNoTaskDiagnostics(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failMerge[taskRef("b")] = errors.New("merge exploded at secret/b-only.txt")
	f.area.failRollback = errors.New("permission denied on secret/b-only.txt")

	_, err := f.gather()
	require.ErrorIs(t, err, engine.ErrGroupGatherRollbackFailed)
	require.Contains(t, f.report().Error, "secret/b-only.txt", "the stored report keeps the diagnostic")

	var failed map[string]any
	for _, evt := range f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupGathering) {
		data, _ := evt.Data.(map[string]any)
		assert.NotContains(t, fmt.Sprint(data), "secret/b-only.txt", "parent-scoped frame leaks task diagnostics")
		if data["state"] == "failed" {
			failed = data
		}
	}
	require.NotNil(t, failed, "the rollback-failed transition is still announced")
	assert.Equal(t, "rollback_failed", failed["rollbackStatus"])
	assert.Equal(t, f.runID.String(), failed["groupRunId"])
	assert.Equal(t, "g1", failed["nodeId"])
	assert.NotContains(t, failed, "error")
}
