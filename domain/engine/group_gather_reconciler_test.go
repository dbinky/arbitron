package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errCrashAfterApply = errors.New("process died after applying")

// fanoutBus is an EventBus whose SubscribeAll really delivers (the shared
// mockEventBus's channel never does), so Watch is exercised end to end.
type fanoutBus struct {
	mu   sync.Mutex
	subs []chan engine.ScopedEvent
}

var _ engine.EventBus = (*fanoutBus)(nil)

func (b *fanoutBus) Publish(id uuid.UUID, ev engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- engine.ScopedEvent{InstanceID: id, Event: ev}:
		default:
		}
	}
}

func (b *fanoutBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	return ch, func() {}
}

func (b *fanoutBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) {
	ch := make(chan engine.ScopedEvent, 64)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			for i, s := range b.subs {
				if s == ch {
					b.subs = append(b.subs[:i], b.subs[i+1:]...)
					break
				}
			}
			close(ch)
		})
	}
}

type closeCall struct {
	ref    instance.ResolutionSessionRef
	reason string
}

// recordingCloser is a GroupSessionCloser recording which session (by its
// full identity) was closed and why.
type recordingCloser struct {
	mu       sync.Mutex
	calls    []closeCall
	notified []instance.ResolutionSessionRef
	err      error
}

var _ engine.GroupSessionCloser = (*recordingCloser)(nil)

func (c *recordingCloser) CloseSession(_ context.Context, ref instance.ResolutionSessionRef, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, closeCall{ref: ref, reason: reason})
	return c.err
}

func (c *recordingCloser) NotifyResolutionRequired(_ context.Context, ref instance.ResolutionSessionRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref.GroupRunID == uuid.Nil || ref.InstanceID == uuid.Nil || ref.NodeID == "" {
		return errors.New("missing notification identity")
	}
	c.notified = append(c.notified, ref)
	return c.err
}

func (c *recordingCloser) all() []closeCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]closeCall(nil), c.calls...)
}

// fakeGatherLocker models the executor's gate: busy runs refuse TryLockGather.
type fakeGatherLocker struct {
	mu   sync.Mutex
	busy map[uuid.UUID]bool
}

var _ engine.GatherLocker = (*fakeGatherLocker)(nil)

func (l *fakeGatherLocker) TryLockGather(id uuid.UUID) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy == nil {
		l.busy = map[uuid.UUID]bool{}
	}
	if l.busy[id] {
		return nil, false
	}
	l.busy[id] = true
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.busy, id)
	}, true
}

func (l *fakeGatherLocker) setBusy(id uuid.UUID, busy bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy == nil {
		l.busy = map[uuid.UUID]bool{}
	}
	l.busy[id] = busy
}

// reconcileFixture: P3-T6's gatherFixture (all_succeed success, tasks in
// author order) whose snapshot handle is the run id, an instance repo holding
// the parent at g1 plus every task row (completed), and a REAL FileGatherer as
// the aborter and a REAL pruner over P3-T6's fakeGatherArea.
type reconcileFixture struct {
	*gatherFixture
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	closer    *recordingCloser
	completer *recordingCompleter
	waker     *fakeNodeWaker
	qm        *mockQueueManager
	locker    *fakeGatherLocker
	events    *fanoutBus
	inventory *fakeInventory
	rec       *engine.GroupGatherReconciler
}

func newReconcileFixture(t *testing.T, keys ...string) *reconcileFixture {
	t.Helper()
	g := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", keys...)
	g.groups.update(g.runID, func(r *instance.GroupRun) { r.WorkspaceSnapshot = r.ID.String() })
	ctx := context.Background()
	instances := newMockInstanceRepo()
	nodeID := "g1"
	parent := *g.parent
	parent.CurrentNodeID = &nodeID
	require.NoError(t, instances.Create(ctx, &parent))
	for _, task := range g.groups.snapshot(g.runID).Tasks {
		require.NoError(t, instances.Create(ctx, &instance.Instance{ID: task.ChildInstanceID, OwnerID: g.parent.OwnerID, State: instance.StateCompleted}))
	}
	f := &reconcileFixture{
		gatherFixture: g, instances: instances, nodeExecs: newMockNodeExecutionRepo(),
		closer: &recordingCloser{}, completer: &recordingCompleter{}, waker: &fakeNodeWaker{instances: instances},
		qm: newMockQueueManager(), locker: &fakeGatherLocker{}, events: &fanoutBus{},
	}
	f.inventory = newFakeInventory(g.area)
	f.inventory.add(g.runID.String(), time.Now())
	pruner := engine.NewGroupResourcePruner(g.groups, instances, &fakeChildLister{}, newDirWorkspaces(t), g.area, f.inventory)
	f.rec = engine.NewGroupGatherReconciler(g.groups, instances, f.nodeExecs, g.gatherer, f.closer, f.completer,
		f.waker, f.qm, f.locker, f.events, pruner)
	return f
}

func (f *reconcileFixture) setParent(state instance.State) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[f.parent.ID].State = state
}

func (f *reconcileFixture) parentState() instance.State {
	return f.instances.get(f.parent.ID).State
}

// openSession runs the gather into a conflict on b (a merged into staging, a
// session opened) and suspends the parent the way the executor does.
func (f *reconcileFixture) openSession(t *testing.T) instance.ResolutionSessionRef {
	t.Helper()
	f.conflictOnB()
	step, err := f.gather()
	require.NoError(t, err)
	require.False(t, step.Done)
	f.setParent(instance.StateWaiting)
	rep := f.report()
	require.NotNil(t, rep.ActiveSession)
	return *rep.ActiveSession
}

// crashAfterApply runs a clean gather whose process "dies" right after Apply
// wrote the parent's area: the run stays gathering, the area is changed.
func (f *reconcileFixture) crashAfterApply(t *testing.T) {
	t.Helper()
	f.groups.failCall("ReplaceGatherReport", 5, errCrashAfterApply)
	_, err := f.gather()
	require.ErrorIs(t, err, errCrashAfterApply)
	require.Equal(t, instance.GroupRunGathering, f.groups.snapshot(f.runID).State)
	require.Contains(t, f.area.parentFiles(gatherParentDir), "a.txt", "precondition: the crash happened after Apply")
}

func (f *reconcileFixture) addSessionRecord(t *testing.T, ref instance.ResolutionSessionRef, open bool, endReason string) {
	t.Helper()
	conv := map[string]any{
		engine.ConversationKeyKind:       engine.ConversationKindGroupResolution,
		engine.ConversationKeyGroupRunID: ref.GroupRunID.String(),
		engine.ConversationKeyMergeIndex: ref.MergeIndex,
		engine.ConversationKeyAttempt:    ref.Attempt,
		engine.ConversationKeyTaskKey:    "b",
		engine.ConversationKeyMessages:   []map[string]any{},
	}
	if !open {
		conv[engine.ConversationKeyEndedAt] = time.Now().UTC().Format(time.RFC3339Nano)
		conv[engine.ConversationKeyEndReason] = endReason
	}
	require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: ref.InstanceID, NodeID: ref.NodeID, Attempt: 2,
		Status: instance.NodeStatusSucceeded, LLMConversation: conv, StartedAt: time.Now(),
	}))
}

// Named by P3-T9 (TestGroupAbandon_Chain_CancelledParentMidSessionStaysCancelled).
func TestGroupGatherReconciler_CancelledParentMidSession_RollsBackAndSettles(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	active := f.openSession(t)
	f.setParent(instance.StateCancelled)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))

	assert.Equal(t, []closeCall{{ref: active, reason: "cancelled"}}, f.closer.all(), "the open session is closed")
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir),
		"SINK: no task change reached the parent")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.True(t, run.ContinuationDelivered, "a cancelled parent never consumes the continuation")
	require.NotNil(t, run.Gather)
	assert.Equal(t, engine.ResolutionOutcomeCancelled, run.Gather.Attempts[0].Outcome)
	assert.Nil(t, run.Gather.ActiveSession)
	assert.Contains(t, run.Gather.Error, "cancelled")
	assert.Equal(t, instance.StateCancelled, f.parentState(), "the parent stays cancelled")
	assert.Empty(t, f.waker.wakeCalls(), "a cancelled parent is never woken")
	assert.Empty(t, f.completer.all())
}

func TestGroupGatherReconciler_DecidedRunUnderCancelledParentSettlesWithoutStaging(t *testing.T) {
	f := newReconcileFixture(t, "a")
	f.setParent(instance.StateCancelled)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Empty(t, f.area.begins(), "nothing is staged for a parent that will never continue")
	assert.Empty(t, f.closer.all())
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
}

// A crash mid-gather leaves the parent `running`; RecoverCrashedInstances marks
// it failed; the startup pass restores its exact pre-gather area.
func TestGroupGatherReconciler_FailedParentMidGather_RestoresThePreGatherArea(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateFailed)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir), "SINK: partial work is rolled back")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.Equal(t, "rolled_back", run.Gather.State)
	assert.Contains(t, run.Gather.Error, "failed")
	assert.Equal(t, instance.StateFailed, f.parentState())
}

func TestGroupGatherReconciler_CompletedParentSettlesWithoutTouchingItsVanishedArea(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateCompleted)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Zero(t, f.area.rollbackCount(), "a completed parent's workspace was removed; nothing is written to it")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, "failed", run.Gather.State)
	assert.Contains(t, run.Gather.Error, "nothing was restored")
	assert.True(t, run.ContinuationDelivered)
}

func TestGroupGatherReconciler_RollbackFailureStaysVisibleUntilRepaired(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateFailed)
	f.area.failRollback = errors.New("read-only file system")
	ctx := context.Background()

	err := f.rec.RecoverRun(ctx, f.runID)
	require.ErrorIs(t, err, engine.ErrGroupGatherRollbackFailed)
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunGathering, run.State, "continuation stays blocked")
	assert.Equal(t, "rollback_failed", run.Gather.RollbackStatus)

	f.area.failRollback = nil
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
}

func TestGroupGatherReconciler_WaitingParentWithAnEndedAttemptIsWoken(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	active := f.openSession(t)
	f.endOpenAttempts("")

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []wakeCall{{id: f.parent.ID, nodeID: "g1"}}, f.waker.wakeCalls())
	assert.Equal(t, instance.StateQueued, f.parentState(), "the lost wake is recovered")
	assert.Equal(t, 1, f.qm.notifyCount())
	assert.Equal(t, []closeCall{{ref: active, reason: "ended"}}, f.closer.all(), "the record of the ended session is closed")
}

func TestGroupGatherReconciler_WaitingParentWithAnOpenSessionIsRestoredUntouched(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	active := f.openSession(t)
	f.addSessionRecord(t, active, true, "")

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Empty(t, f.waker.wakeCalls())
	assert.Empty(t, f.completer.all())
	assert.Empty(t, f.closer.all())
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State)
	assert.Equal(t, instance.StateWaiting, f.parentState(), "the operator keeps resolving in the persisted session")
}

func TestGroupGatherReconciler_MissingSessionRecordEndsTheAttemptThroughTheCompleter(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []completerCall{{instanceID: f.parent.ID, nodeID: "g1", abandoned: false}}, f.completer.all(),
		"the gatherer then verifies the staging area and reopens a fresh attempt if needed")
}

func TestGroupGatherReconciler_ClosedRecordWithAnOpenAttemptIsCompleted(t *testing.T) {
	for _, tc := range []struct {
		reason    string
		abandoned bool
	}{{"user_ended", false}, {"abandoned", true}} {
		t.Run(tc.reason, func(t *testing.T) {
			f := newReconcileFixture(t, "a", "b")
			active := f.openSession(t)
			f.addSessionRecord(t, active, false, tc.reason)

			require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
			assert.Equal(t, []completerCall{{instanceID: f.parent.ID, nodeID: "g1", abandoned: tc.abandoned}}, f.completer.all())
		})
	}
}

func TestGroupGatherReconciler_SessionNeverOpenedWakesTheParent(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.ActiveSession = nil })

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []wakeCall{{id: f.parent.ID, nodeID: "g1"}}, f.waker.wakeCalls(),
		"the re-entered gatherer opens the recorded attempt's session")
}

func TestGroupGatherReconciler_WaitingParentOfAGatheringRunIsWokenToRetry(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateWaiting)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []wakeCall{{id: f.parent.ID, nodeID: "g1"}}, f.waker.wakeCalls())
	assert.Equal(t, instance.GroupRunGathering, f.groups.snapshot(f.runID).State, "the parent's executor resumes it; nothing is aborted")
	assert.Zero(t, f.area.rollbackCount())
}

func TestGroupGatherReconciler_RunningParentIsLeftToItsExecutor(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.setParent(instance.StateRunning)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Empty(t, f.waker.wakeCalls())
	assert.Empty(t, f.closer.all())
	assert.Zero(t, f.area.rollbackCount())
}

func TestGroupGatherReconciler_ActiveWriterIsSkippedUntilItReleases(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.setParent(instance.StateCancelled)
	f.locker.setBusy(f.runID, true)
	ctx := context.Background()

	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State, "the writer's gather is never aborted under it")
	assert.Zero(t, f.area.rollbackCount())

	f.locker.setBusy(f.runID, false)
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
}

func TestGroupGatherReconciler_ResultsModeAndDoneRunsAreIgnored(t *testing.T) {
	f := newReconcileFixture(t, "a")
	f.setParent(instance.StateCancelled)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.GatherMode = workflow.GroupGatherResults })
	ctx := context.Background()

	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunDecided, f.groups.snapshot(f.runID).State)

	f.groups.update(f.runID, func(r *instance.GroupRun) {
		r.GatherMode = workflow.GroupGatherResultsAndFiles
		r.State = instance.GroupRunDone
	})
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Empty(t, f.area.begins())
}

func TestGroupGatherReconciler_ReconcileRecoversUnsettledRunsThenSweepsResources(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.setParent(instance.StateCancelled)
	orphan := uuid.New().String()
	f.inventory.add(orphan, time.Now().Add(-2*engine.GroupOrphanSnapshotGrace))

	require.NoError(t, f.rec.Reconcile(context.Background()))
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
	f.area.mu.Lock()
	released := append([]string(nil), f.area.releasedSnapshots...)
	f.area.mu.Unlock()
	assert.ElementsMatch(t, []string{f.runID.String(), orphan}, released,
		"the settled run's resources and an old orphan are released in the same pass")
}

func TestGroupGatherReconciler_WatchAbortsAsSoonAsTheParentIsCancelled(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := f.rec.Watch(ctx)

	f.setParent(instance.StateCancelled)
	f.events.Publish(f.parent.ID, engine.Event{Type: engine.EventTypeInstanceCancelled, Timestamp: time.Now()})

	require.Eventually(t, func() bool {
		return f.groups.snapshot(f.runID).State == instance.GroupRunDone
	}, 5*time.Second, 10*time.Millisecond, "the cancellation event settles the gather without waiting for a sweep")
	assert.Equal(t, instance.StateCancelled, f.parentState())

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not stop when its context was cancelled")
	}
}

func TestGroupGatherReconciler_WatchPrunesWhenTheLastWriterStops(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	loser := f.groups.snapshot(f.runID).Tasks[1].ChildInstanceID
	f.instances.mu.Lock()
	f.instances.instances[loser].State = instance.StateRunning
	f.instances.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.rec.Watch(ctx)
	f.events.Publish(f.parent.ID, engine.Event{Type: engine.EventTypeGroupCompleted, Data: map[string]any{"groupRunId": f.runID.String(), "nodeId": "g1"}})
	time.Sleep(100 * time.Millisecond)
	has, err := f.inventory.HasSnapshot(ctx, f.runID.String())
	require.NoError(t, err)
	assert.True(t, has, "a running task still pins the snapshot")

	f.instances.mu.Lock()
	f.instances.instances[loser].State = instance.StateCompleted
	f.instances.mu.Unlock()
	f.events.Publish(loser, engine.Event{Type: engine.EventTypeInstanceCompleted})
	require.Eventually(t, func() bool {
		has, _ := f.inventory.HasSnapshot(ctx, f.runID.String())
		return !has
	}, 5*time.Second, 10*time.Millisecond, "the last task's completion releases the run's resources")
}

func TestNewGroupGatherReconciler_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newReconcileFixture(t, "a")
	pruner := engine.NewGroupResourcePruner(f.groups, f.instances, &fakeChildLister{}, newDirWorkspaces(t), f.area, f.inventory)
	build := func(mut func(*[11]any)) func() {
		return func() {
			args := [11]any{f.groups, f.instances, f.nodeExecs, f.gatherer, f.closer, f.completer, f.waker, f.qm, f.locker, f.events, pruner}
			mut(&args)
			groups, _ := args[0].(instance.GroupRunRepository)
			instances, _ := args[1].(instance.InstanceRepository)
			nodeExecs, _ := args[2].(instance.NodeExecutionRepository)
			aborter, _ := args[3].(engine.GatherAborter)
			closer, _ := args[4].(engine.GroupSessionCloser)
			completer, _ := args[5].(engine.GroupResolutionCompleter)
			waker, _ := args[6].(instance.NodeAddressedWaker)
			qm, _ := args[7].(engine.QueueManager)
			locker, _ := args[8].(engine.GatherLocker)
			bus, _ := args[9].(engine.EventBus)
			p, _ := args[10].(*engine.GroupResourcePruner)
			engine.NewGroupGatherReconciler(groups, instances, nodeExecs, aborter, closer, completer, waker, qm, locker, bus, p)
		}
	}
	for i := 0; i < 11; i++ {
		idx := i
		assert.Panics(t, build(func(a *[11]any) { a[idx] = nil }), "argument %d is required", idx)
	}
	assert.NotPanics(t, build(func(*[11]any) {}))
}
