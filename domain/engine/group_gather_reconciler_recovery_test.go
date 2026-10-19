package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reconcileWithPorts(t *testing.T, f *reconcileFixture, nodes instance.NodeExecutionRepository, closer engine.GroupSessionCloser, completer engine.GroupResolutionCompleter) *engine.GroupGatherReconciler {
	t.Helper()
	pruner := engine.NewGroupResourcePruner(f.groups, f.instances, &fakeChildLister{}, newDirWorkspaces(t), f.area, f.inventory)
	return engine.NewGroupGatherReconciler(f.groups, f.instances, nodes, f.gatherer, closer, completer, f.waker, f.qm, f.locker, f.events, pruner)
}

func TestGroupGatherReconciler_ActiveWriterBlocksSessionRestoration(t *testing.T) {
	for _, open := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "open"}[open], func(t *testing.T) {
			f := newReconcileFixture(t, "a", "b")
			active := f.openSession(t)
			f.addSessionRecord(t, active, open, "completed")
			f.locker.setBusy(f.runID, true)
			require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
			assert.Empty(t, f.closer.notified, "a writer may still be verifying automatic resolution")
			assert.Empty(t, f.completer.all(), "do not close the writer's attempt under it")
			assert.Empty(t, f.waker.wakeCalls())
			f.locker.setBusy(f.runID, false)
			require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
			if open {
				assert.Equal(t, []instance.ResolutionSessionRef{active}, f.closer.notified)
			} else {
				assert.Equal(t, []completerCall{{instanceID: active.InstanceID, nodeID: active.NodeID}}, f.completer.all())
			}
		})
	}
}

func TestGroupGatherReconciler_OpenSessionRetriesMissingNotification(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	active := f.openSession(t)
	f.addSessionRecord(t, active, true, "")
	ctx := context.Background()
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, []instance.ResolutionSessionRef{active}, f.closer.notified)
	assert.Empty(t, f.waker.wakeCalls())
	assert.Empty(t, f.completer.all())

	row, err := f.nodeExecs.GetByInstanceAndNode(ctx, active.InstanceID, active.NodeID)
	require.NoError(t, err)
	row.LLMConversation["notificationSent"] = true
	require.NoError(t, f.nodeExecs.Update(ctx, row))
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Len(t, f.closer.notified, 1, "a durable notification marker suppresses replay")
}

func TestGroupGatherReconciler_NotificationAndLookupErrorsLeaveSessionRetryable(t *testing.T) {
	for _, lookup := range []bool{false, true} {
		t.Run(map[bool]string{false: "notification", true: "lookup"}[lookup], func(t *testing.T) {
			f := newReconcileFixture(t, "a", "b")
			active := f.openSession(t)
			f.addSessionRecord(t, active, true, "")
			failure := errors.New("notification persistence unavailable")
			rec := f.rec
			if lookup {
				rec = reconcileWithPorts(t, f, resolutionSessionRepo{NodeExecutionRepository: f.nodeExecs, listErr: failure}, f.closer, f.completer)
			} else {
				f.closer.err = failure
			}
			require.ErrorIs(t, rec.RecoverRun(context.Background(), f.runID), failure)
			assert.Equal(t, instance.StateWaiting, f.parentState())
			assert.Empty(t, f.completer.all())
			assert.Empty(t, f.waker.wakeCalls())
			assert.Nil(t, f.report().Attempts[0].EndedAt)
			if lookup {
				assert.Empty(t, f.closer.notified)
			} else {
				assert.Equal(t, []instance.ResolutionSessionRef{active}, f.closer.notified)
			}
		})
	}
}

var errRecoveryBoundary = errors.New("process interrupted at a session persistence boundary")

// Fail at the durable boundary AFTER the real bridge closed the automatic
// session, BEFORE the gatherer could record that the attempt ended.
type crashAfterSessionClose struct {
	*engine.GroupResolutionBridge
	groups *memGroupRuns
}

func (b crashAfterSessionClose) CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error {
	if err := b.GroupResolutionBridge.CloseSession(ctx, ref, reason); err != nil {
		return err
	}
	b.groups.failOnce("ReplaceGatherReport", errRecoveryBoundary)
	return nil
}

type crashBeforeNotification struct{ *engine.GroupResolutionBridge }

func (b crashBeforeNotification) NotifyResolutionRequired(context.Context, instance.ResolutionSessionRef) error {
	return errRecoveryBoundary
}

func TestGroupGatherReconciler_ClosedAutomaticSessionRecoversToFileSinkWithoutAnotherTurn(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.conflictOnB()
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = groupResolverProfile })
	agent := newMockStreamingChatProvider()
	turns := 0
	agent.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		turns++
		rep := f.report()
		if req.WorkDir != rep.Handle["stagingDir"] || req.Model != groupResolverModel {
			return nil, errors.New("automatic resolver received the wrong staging area or profile")
		}
		f.area.resolveStaged("shared.txt", "combined")
		return cannedStreamWithSession([]string{"combined both edits"}, "automatic-session")(ctx, req)
	}
	notifier := &recordingNotifier{}
	bridge := engine.NewGroupResolutionBridge(f.groups, f.instances, f.nodeExecs, groupResolverResolver(),
		&fakeStreamSelector{agentProv: agent}, newOwnerScopedFileReader(), notifier, f.bus, f.waker, f.qm, groupResolverTimeout)
	interrupted := engine.NewGroupGatherer(f.groups, f.area, crashAfterSessionClose{bridge, f.groups}, f.bus)
	_, err := interrupted.Gather(context.Background(), f.groups.snapshot(f.runID), f.parent)
	require.ErrorIs(t, err, errRecoveryBoundary)
	require.Nil(t, f.report().Attempts[0].EndedAt)
	row, err := f.nodeExecs.GetByInstanceAndNode(context.Background(), f.parent.ID, "g1")
	require.NoError(t, err)
	assert.Equal(t, "completed", row.LLMConversation[engine.ConversationKeyEndReason])
	f.setParent(instance.StateWaiting)

	rec := reconcileWithPorts(t, f, f.nodeExecs, bridge, bridge)
	require.NoError(t, rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, instance.StateQueued, f.parentState(), "real completer wakes the parent at the group node")
	require.NotNil(t, f.report().Attempts[0].EndedAt)
	f.setParent(instance.StateRunning)
	gatherer := engine.NewGroupGatherer(f.groups, f.area, bridge, f.bus)
	step, err := gatherer.Gather(context.Background(), f.groups.snapshot(f.runID), f.instances.get(f.parent.ID))
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "combined", "a.txt": "from-a", "b.txt": "from-b"}, f.area.parentFiles(gatherParentDir))
	assert.Equal(t, 1, turns)
	assert.Empty(t, notifier.all())
	assert.Equal(t, engine.ResolutionOutcomeResolved, f.report().Attempts[0].Outcome)
}

func TestGroupGatherReconciler_RealBridgeRetriesNotificationToSink(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"still conflicted"}, "persisted-session")
	ref, err := f.start(1)
	require.NoError(t, err)
	f.setActiveSession()
	f.groups.update(f.runID, func(r *instance.GroupRun) {
		r.Decision = &instance.GroupDecision{Outcome: workflow.GroupPortSuccess}
	})
	f.setParentState(instance.StateWaiting, "g1")
	area := newFakeGatherArea()
	inventory := newFakeInventory(area)
	inventory.add("snap-1", time.Now())
	pruner := engine.NewGroupResourcePruner(f.groups, f.instances, &fakeChildLister{}, newDirWorkspaces(t), area, inventory)
	rec := engine.NewGroupGatherReconciler(f.groups, f.instances, f.nodeExecs,
		engine.NewGroupGatherer(f.groups, area, f.bridge, f.bus), f.bridge, f.bridge,
		f.waker, f.queue, &fakeGatherLocker{}, f.bus, pruner)
	require.NoError(t, rec.RecoverRun(context.Background(), f.runID))
	require.NoError(t, rec.RecoverRun(context.Background(), f.runID))
	calls := f.notifier.all()
	require.Len(t, calls, 1)
	assert.Equal(t, ref.InstanceID, calls[0].instanceID)
	assert.Equal(t, true, f.sessionRows()[0].LLMConversation["notificationSent"])
	assert.Len(t, f.agent.getCapturedReqs(), 1, "restart never runs the first turn again")
}
