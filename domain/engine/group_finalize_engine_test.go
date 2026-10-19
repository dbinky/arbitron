package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderedInstanceRepo logs SetCompleted / SetFailed into the shared call log.
type orderedInstanceRepo struct {
	*mockInstanceRepo
	log *callLog
}

func (r *orderedInstanceRepo) SetCompleted(ctx context.Context, id uuid.UUID) error {
	r.log.add("SetCompleted")
	return r.mockInstanceRepo.SetCompleted(ctx, id)
}

func (r *orderedInstanceRepo) SetFailed(ctx context.Context, id uuid.UUID, node string) error {
	r.log.add("SetFailed:" + node)
	return r.mockInstanceRepo.SetFailed(ctx, id, node)
}

// finalizeGroupHandler is the P2-T13 GroupTerminalHandler seam.
type finalizeGroupHandler struct{ log *callLog }

func (h finalizeGroupHandler) OnTaskTerminal(_ context.Context, childID uuid.UUID) {
	h.log.add("OnTaskTerminal:" + childID.String())
}

// finalizeSpawnLinks serves ListChildren from a parent -> children map; every
// other SpawnLinkRepository method comes from the Null Object.
type finalizeSpawnLinks struct {
	engine.NoopSpawnLinks
	children map[uuid.UUID][]instance.ChildRef
	err      error
}

func (l finalizeSpawnLinks) ListChildren(_ context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.children[parentID], nil
}

type finalizeFixture struct {
	*testFixtures
	log       *callLog
	groups    *finalizeGroupRepo
	area      *fakeWorkingArea
	ws        *fakeWorkspaceManager
	notifs    *mockNotificationRepo
	instances *orderedInstanceRepo
	run       *instance.GroupRun
	taskID    uuid.UUID
}

// newFinalizeFixture builds an engine (same mocks as newTestFixtures, with the
// ordering instance repo) carrying a group run with one task "a" whose child
// instance is pre-seeded at state queued, ready for StartInstance.
func newFinalizeFixture(t *testing.T, gatherMode string, state instance.GroupRunState, blocking bool) *finalizeFixture {
	t.Helper()
	base := newTestFixtures()
	log := &callLog{}
	ordered := &orderedInstanceRepo{mockInstanceRepo: base.instanceRepo, log: log}
	executors := map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: base.executor}
	eng := engine.NewEngine(base.workflowRepo, ordered, base.nodeExecRepo, base.snapshotRepo, base.userRepo,
		executors, base.eventBus, base.queueManager, base.queueRepo, "")
	base.engine = eng
	t.Cleanup(eng.Shutdown)
	// newTestFixtures bound Enqueue to ITS engine; re-bind it to this one.
	base.queueManager.mu.Lock()
	base.queueManager.enqueueFn = func(ctx context.Context, id uuid.UUID, _ uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	base.queueManager.mu.Unlock()

	ws := &fakeWorkspaceManager{}
	eng.SetWorkspaceManager(ws)
	notifs := newMockNotificationRepo()
	eng.SetNotifications(notifs)
	eng.SetGroupTerminalHandler(finalizeGroupHandler{log: log})

	parentID, runID, taskID := uuid.New(), uuid.New(), uuid.New()
	run := &instance.GroupRun{
		ID: runID, ParentInstanceID: parentID, NodeID: "grp", Attempt: 1,
		GatherMode: gatherMode, State: state, WorkspaceSnapshot: runID.String(),
		Tasks: []instance.GroupTask{{GroupRunID: runID, Key: "a", Kind: instance.GroupTaskKindBranch,
			ChildInstanceID: taskID, Blocking: blocking}},
	}
	groups := newFinalizeGroupRepo(log, run)
	area := &fakeWorkingArea{log: log}
	eng.SetGroupTaskFinalizer(engine.NewGroupFileFinalizer(groups, area, ws))

	seedGroupTask(t, base.instanceRepo, linearWorkflow(), taskID, parentID, runID, "a")
	return &finalizeFixture{testFixtures: base, log: log, groups: groups, area: area, ws: ws,
		notifs: notifs, instances: ordered, run: run, taskID: taskID}
}

func seedGroupTask(t *testing.T, repo *mockInstanceRepo, wf *workflow.Workflow, id, parentID, runID uuid.UUID, key string) {
	t.Helper()
	raw, err := json.Marshal(wf)
	require.NoError(t, err)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(raw, &snap))
	node := "grp"
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: uuid.New(), WorkflowName: wf.Name, WorkflowSnapshot: snap,
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		ParentInstanceID: &parentID, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func (f *finalizeFixture) start(t *testing.T, states ...instance.State) *instance.Instance {
	t.Helper()
	require.NoError(t, f.engine.StartInstance(context.Background(), f.taskID))
	return waitForInstance(t, f.instanceRepo, f.taskID, 5*time.Second, states...)
}

// waitForLog waits until the call log holds want (the OnChildTerminal
// dispatch runs right after the terminal write).
func (f *finalizeFixture) waitForLog(t *testing.T, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, c := range f.log.snapshot() {
			if c == want {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "missing %q in %v", want, f.log.snapshot())
}

func TestGroupFinalize_FilesTaskFinalizesAndPersistsRefBeforeCompleting(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())

	ref := "refs/arbitron/tasks/" + f.taskID.String()
	assert.Equal(t, []string{
		"FinalizeTask",
		"SetTaskFinalizedRef:a=" + ref,
		"SetCompleted",
		"OnTaskTerminal:" + f.taskID.String(),
	}, f.log.snapshot(), "the ref is durable before the task completes and before its group is notified")
	assert.Equal(t, ref, f.groups.finalizedRef(f.run.ID, "a"))
	assert.Empty(t, f.ws.removedIDs(), "a files-mode task keeps its workspace until its run is done")

	require.Len(t, f.area.finalizeCalls(), 1)
	call := f.area.finalizeCalls()[0]
	assert.Equal(t, f.run.WorkspaceSnapshot, call.Handle, "finalized against the run's snapshot handle")
	assert.Equal(t, f.taskID, call.TaskID)
	assert.Equal(t, "/tmp/arbitron-test-ws/"+f.taskID.String(), call.Dir, "the task's own workspace from WorkspaceManager")
}

func TestGroupFinalize_FinalizeFailureFailsTaskAndStillNotifiesGroup(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.area.finalizeErr = errors.New("disk full")
	inst := f.start(t, instance.StateFailed, instance.StateCompleted)
	require.Equal(t, instance.StateFailed, inst.State)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())

	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "C", *inst.FailedNodeID, "recorded against the last node that ran")
	assert.NotContains(t, f.log.snapshot(), "SetCompleted")
	assert.Empty(t, f.groups.finalizedRef(f.run.ID, "a"), "no candidate ref is published")

	var finalizeExec *instance.NodeExecution
	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), f.taskID)
	require.NoError(t, err)
	for i := range execs {
		if execs[i].Status == instance.NodeStatusFailed {
			finalizeExec = &execs[i]
		}
	}
	require.NotNil(t, finalizeExec)
	assert.Equal(t, "C", finalizeExec.NodeID)
	assert.Equal(t, 2, finalizeExec.Attempt, "one attempt after C's successful run")
	assert.Equal(t, "group_file_finalization", finalizeExec.Error["phase"])
	msg, _ := finalizeExec.Error["error"].(string)
	assert.True(t, strings.HasPrefix(msg, "group file finalization failed: "), msg)
	assert.Contains(t, msg, "disk full")

	assert.Len(t, f.eventBus.eventsOfType(f.taskID, engine.EventTypeInstanceFailed), 1)
	assert.Empty(t, f.eventBus.eventsOfType(f.taskID, engine.EventTypeInstanceCompleted))
	var types []notification.Type
	for _, n := range f.notifs.getCreated() {
		types = append(types, n.Type)
	}
	assert.Equal(t, []notification.Type{notification.TypeFailed}, types)
	assert.Empty(t, f.ws.removedIDs(), "a failed task's workspace is retained for inspection")
}

func TestGroupFinalize_PersistFailureFailsTask(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.groups.setRefErr = errors.New("database is locked")
	inst := f.start(t, instance.StateFailed, instance.StateCompleted)
	assert.Equal(t, instance.StateFailed, inst.State)
	assert.NotContains(t, f.log.snapshot(), "SetCompleted")
}

func TestGroupFinalize_MissingSnapshotHandleFailsTask(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.groups.runs[f.run.ID].WorkspaceSnapshot = ""
	inst := f.start(t, instance.StateFailed, instance.StateCompleted)
	assert.Equal(t, instance.StateFailed, inst.State)
	assert.Empty(t, f.area.finalizeCalls())
	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), f.taskID)
	require.NoError(t, err)
	var msgs []string
	for _, e := range execs {
		if m, ok := e.Error["error"].(string); ok {
			msgs = append(msgs, m)
		}
	}
	assert.Contains(t, strings.Join(msgs, "|"), "no workspace snapshot")
}

func TestGroupFinalize_ResultsOnlyGroupIsUntouched(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.area.finalizeCalls())
	assert.Equal(t, []uuid.UUID{f.taskID}, f.ws.removedIDs(), "results-only tasks are cleaned up as before")
}

func TestGroupFinalize_TaskFinishingAfterDecisionIsNotFinalized(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunDecided, false)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.area.finalizeCalls(), "a late/independent task can never become a merge candidate")
	assert.Empty(t, f.groups.finalizedRef(f.run.ID, "a"))
}

// The group pruner owns a results_and_files task's workspace: the engine never
// removes it at completion, even once the run is done.
func TestGroupFinalize_DoneFilesRunLeavesWorkspaceToThePruner(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunDone, false)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.ws.removedIDs())
}

// A first-success group whose gather failed and rolled back is done; a
// non-blocking task completing afterwards keeps its workspace for debugging
// (PruneRun retains completed workspaces of an unapplied gather).
func TestGroupFinalize_TaskCompletingAfterAFailedFirstSuccessGatherIsRetained(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunDone, false)
	f.run.CompletionRule = workflow.GroupRuleFirstSuccess
	f.run.Gather = &instance.GatherReport{Mode: workflow.GroupGatherResultsAndFiles, State: instance.GatherStateFailed}
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.ws.removedIDs(), "the unapplied gather's late task keeps its workspace")
}

func TestGroupFinalize_ActiveDescendantRetainsWorkspace(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	terminalChild, grandchild := uuid.New(), uuid.New()
	f.engine.SetSpawnLinks(finalizeSpawnLinks{children: map[uuid.UUID][]instance.ChildRef{
		f.taskID:      {{ID: terminalChild, State: instance.StateCompleted}},
		terminalChild: {{ID: grandchild, State: instance.StateRunning}},
	}})
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.ws.removedIDs(), "a running grandchild below a completed intermediate keeps the task workspace")
}

func TestGroupFinalize_DescendantLookupFailureRetainsWorkspace(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	f.engine.SetSpawnLinks(finalizeSpawnLinks{err: errors.New("db down")})
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.ws.removedIDs())
}

func TestGroupFinalize_AllDescendantsTerminalReleasesWorkspace(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	child := uuid.New()
	f.engine.SetSpawnLinks(finalizeSpawnLinks{children: map[uuid.UUID][]instance.ChildRef{
		f.taskID: {{ID: child, State: instance.StateFailed}},
	}})
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Equal(t, []uuid.UUID{f.taskID}, f.ws.removedIDs())
}

func TestGroupFinalize_CancelDuringFinalizationLeavesTaskCancelled(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.area.onFinalize = func(ctx context.Context) error {
		if err := f.engine.Cancel(bgAdmin(), f.taskID); err != nil {
			return err
		}
		<-ctx.Done()
		return fmt.Errorf("git working area: finalize task: %w", ctx.Err())
	}
	inst := f.start(t, instance.StateCancelled, instance.StateFailed, instance.StateCompleted)
	assert.Equal(t, instance.StateCancelled, inst.State)
	assert.NotContains(t, f.log.snapshot(), "SetCompleted")
	for _, c := range f.log.snapshot() {
		assert.False(t, strings.HasPrefix(c, "SetFailed"), "cancellation is not a finalization failure: %v", f.log.snapshot())
	}
}

func TestGroupFinalize_CancelAsFinalizerReturnsSuccessDoesNotCompleteTask(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.area.onFinalize = func(ctx context.Context) error {
		if err := f.engine.Cancel(bgAdmin(), f.taskID); err != nil {
			return err
		}
		<-ctx.Done()
		return nil // Cancellation need not make a successful snapshot fail.
	}
	f.start(t, instance.StateCancelled, instance.StateCompleted)
	f.engine.Shutdown() // Wait for the finalization tail, not only Cancel's write.
	assert.Equal(t, instance.StateCancelled, f.instanceRepo.get(f.taskID).State)
	assert.NotContains(t, f.log.snapshot(), "SetCompleted")
	assert.Empty(t, f.eventBus.eventsOfType(f.taskID, engine.EventTypeInstanceCompleted))
	assert.Empty(t, f.eventBus.eventsOfType(f.taskID, engine.EventTypeInstanceFailed))
	assert.Empty(t, f.ws.removedIDs())
}

// Regression: a run that is not a group task never reaches the finalizer.
func TestGroupFinalize_NonGroupInstanceUnchanged(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.workflowRepo.add(linearWorkflow())
	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Empty(t, f.area.finalizeCalls())
	assert.Equal(t, []uuid.UUID{id}, f.ws.removedIDs())
	assert.Equal(t, []string{"SetCompleted"}, f.log.snapshot())
}

// Provenance: with no finalizer wired, a files-mode task completes exactly as
// before (and nothing is finalized) — the hook is live only through
// SetGroupTaskFinalizer, which P3-T11 calls in main.go.
func TestGroupFinalize_NoFinalizerWiredKeepsPlainCompletion(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.engine.SetGroupTaskFinalizer(nil)
	f.start(t, instance.StateCompleted)
	assert.Empty(t, f.area.finalizeCalls())
	assert.Equal(t, []uuid.UUID{f.taskID}, f.ws.removedIDs())
}
