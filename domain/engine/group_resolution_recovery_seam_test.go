package engine_test

// Seam: Area 3 conflict resolution ↔ Area 7 group recovery (paired:7).
// Recovery (GroupGatherReconciler.restore/abort, under the gather gate) and a
// live operator end/abandon (conversationHandler.completeGroupSession, outside
// it) both write the same session record. A recovery writer that read the
// record before the live end landed must never write it back open, nor
// replace the live end reason.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endAfterRead lets the next ListByInstance return its rows, then closes the
// open group session it returned with endReason, as a live end/abandon
// landing between a recovery writer's read and its write.
type endAfterRead struct {
	*mockNodeExecutionRepo
	mu        sync.Mutex
	endReason string
}

func (r *endAfterRead) arm(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endReason = reason
}

func (r *endAfterRead) ListByInstance(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	rows, err := r.mockNodeExecutionRepo.ListByInstance(ctx, id)
	r.mu.Lock()
	reason := r.endReason
	r.endReason = ""
	r.mu.Unlock()
	if err != nil || reason == "" {
		return rows, err
	}
	for _, row := range rows {
		if engine.IsOpenGroupResolutionSession(row.LLMConversation) {
			live := row
			conv := make(map[string]any, len(row.LLMConversation)+2)
			for k, v := range row.LLMConversation {
				conv[k] = v
			}
			conv[engine.ConversationKeyEndedAt] = time.Now().UTC().Format(time.RFC3339Nano)
			conv[engine.ConversationKeyEndReason] = reason
			live.LLMConversation = conv
			if err := r.mockNodeExecutionRepo.Update(ctx, &live); err != nil {
				return nil, err
			}
		}
	}
	return rows, nil
}

func newSeamRecoveryBridge(t *testing.T) (*bridgeFixture, *endAfterRead, *engine.GroupResolutionBridge) {
	t.Helper()
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"still conflicted"}, "persisted-session")
	_, err := f.start(1)
	require.NoError(t, err)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")
	repo := &endAfterRead{mockNodeExecutionRepo: f.nodeExecs}
	bridge := engine.NewGroupResolutionBridge(f.groups, f.instances, repo, groupResolverResolver(), f.selector, f.files, f.notifier, f.bus, f.waker, f.queue, groupResolverTimeout)
	return f, repo, bridge
}

func TestSeamResolutionRecovery_RestoredNotificationNeverReopensALiveEnd(t *testing.T) {
	f, repo, bridge := newSeamRecoveryBridge(t)
	repo.arm("abandoned")

	// restore's open-session branch: retry the missing notification.
	require.NoError(t, bridge.NotifyResolutionRequired(context.Background(), f.activeRef()))

	rows := f.sessionRows()
	require.Len(t, rows, 1)
	assert.False(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation), "the live end must survive the recovery write")
	assert.Equal(t, "abandoned", rows[0].LLMConversation[engine.ConversationKeyEndReason])
}

func TestSeamResolutionRecovery_RecoveryCloseKeepsTheLiveAbandonReason(t *testing.T) {
	f, repo, bridge := newSeamRecoveryBridge(t)
	repo.arm("abandoned")
	before := len(f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded))

	// abort's close for a cancelled parent, racing the operator's abandon.
	require.NoError(t, bridge.CloseSession(context.Background(), f.activeRef(), "cancelled"))

	rows := f.sessionRows()
	require.Len(t, rows, 1)
	assert.False(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation))
	assert.Equal(t, "abandoned", rows[0].LLMConversation[engine.ConversationKeyEndReason], "the first close wins")
	assert.Len(t, f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded), before, "a lost close publishes no second interactive.ended")
}

func TestSeamResolutionRecovery_UncontendedRecoveryWritesStillApply(t *testing.T) {
	f, _, bridge := newSeamRecoveryBridge(t)
	ctx := context.Background()

	require.NoError(t, bridge.NotifyResolutionRequired(ctx, f.activeRef()))
	rows := f.sessionRows()
	require.Len(t, rows, 1)
	assert.Equal(t, true, rows[0].LLMConversation["notificationSent"])
	assert.True(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation))

	require.NoError(t, bridge.CloseSession(ctx, f.activeRef(), "cancelled"))
	rows = f.sessionRows()
	assert.Equal(t, "cancelled", rows[0].LLMConversation[engine.ConversationKeyEndReason])
}
