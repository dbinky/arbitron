package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Errors are injected at the durable session seam, not at an ignored hop.
type resolutionSessionRepo struct {
	instance.NodeExecutionRepository
	updateErr error
	listErr   error
}

func (r resolutionSessionRepo) Update(ctx context.Context, e *instance.NodeExecution) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	return r.NodeExecutionRepository.Update(ctx, e)
}
func (r resolutionSessionRepo) UpdateIfSessionOpen(ctx context.Context, e *instance.NodeExecution) (bool, error) {
	if r.updateErr != nil {
		return false, r.updateErr
	}
	return r.NodeExecutionRepository.(instance.OpenSessionRecordUpdater).UpdateIfSessionOpen(ctx, e)
}
func (r resolutionSessionRepo) PatchSessionFields(ctx context.Context, id uuid.UUID, patch map[string]any) (bool, error) {
	if r.updateErr != nil {
		return false, r.updateErr
	}
	return r.NodeExecutionRepository.(instance.OpenSessionRecordUpdater).PatchSessionFields(ctx, id, patch)
}
func (r resolutionSessionRepo) ListByInstance(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.NodeExecutionRepository.ListByInstance(ctx, id)
}

func TestGroupResolutionBridge_NotificationRequiresExactOpenActiveSession(t *testing.T) {
	for _, name := range []string{"wrong run", "wrong instance", "wrong node", "wrong merge", "wrong attempt", "missing row", "closed", "inactive", "not resolving"} {
		t.Run(name, func(t *testing.T) {
			f := newBridgeFixture(t)
			f.agent.streamCallFn = cannedStreamWithSession([]string{"still conflicted"}, "s")
			ref, err := f.start(1)
			require.NoError(t, err)
			f.setActiveSession()
			switch name {
			case "wrong run":
				ref.GroupRunID = uuid.New()
			case "wrong instance":
				ref.InstanceID = uuid.New()
			case "wrong node":
				ref.NodeID = "elsewhere"
			case "wrong merge":
				ref.MergeIndex++
			case "wrong attempt":
				ref.Attempt++
			case "missing row":
				f.nodeExecs.executions = f.nodeExecs.executions[:1]
			case "closed":
				require.NoError(t, f.bridge.CloseSession(context.Background(), *ref, "completed"))
			case "inactive":
				f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.ActiveSession = nil })
			case "not resolving":
				f.groups.update(f.runID, func(r *instance.GroupRun) { r.State = instance.GroupRunGathering })
			}
			require.Error(t, f.bridge.NotifyResolutionRequired(context.Background(), *ref))
			assert.Empty(t, f.notifier.all())
		})
	}
}

func TestGroupResolutionBridge_NotificationStorageErrorsRemainRetryable(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"still conflicted"}, "s")
	ref, err := f.start(1)
	require.NoError(t, err)
	f.setActiveSession()
	ctx := context.Background()
	f.groups.failOnce("Get", assert.AnError)
	require.ErrorIs(t, f.bridge.NotifyResolutionRequired(ctx, *ref), assert.AnError)
	assert.Empty(t, f.notifier.all())

	f.bridge = engine.NewGroupResolutionBridge(f.groups, f.instances, resolutionSessionRepo{NodeExecutionRepository: f.nodeExecs, listErr: assert.AnError}, groupResolverResolver(), f.selector, f.files, f.notifier, f.bus, f.waker, f.queue, groupResolverTimeout)
	require.ErrorIs(t, f.bridge.NotifyResolutionRequired(ctx, *ref), assert.AnError)
	assert.Empty(t, f.notifier.all())

	f.bridge = engine.NewGroupResolutionBridge(f.groups, f.instances, resolutionSessionRepo{NodeExecutionRepository: f.nodeExecs, updateErr: assert.AnError}, groupResolverResolver(), f.selector, f.files, f.notifier, f.bus, f.waker, f.queue, groupResolverTimeout)
	require.ErrorIs(t, f.bridge.NotifyResolutionRequired(ctx, *ref), assert.AnError)
	assert.Len(t, f.notifier.all(), 1)
	assert.NotEqual(t, true, f.sessionRows()[0].LLMConversation["notificationSent"], "a failed write must not set a fake durable marker")
	f.bridge = engine.NewGroupResolutionBridge(f.groups, f.instances, f.nodeExecs, groupResolverResolver(), f.selector, f.files, f.notifier, f.bus, f.waker, f.queue, groupResolverTimeout)
	require.NoError(t, f.bridge.NotifyResolutionRequired(ctx, *ref))
	require.NoError(t, f.bridge.NotifyResolutionRequired(ctx, *ref))
	assert.Len(t, f.notifier.all(), 1, "a failed marker write reuses the durable notification ID")
}

func TestGroupResolutionBridge_RequiresEveryCollaborator(t *testing.T) {
	f := newBridgeFixture(t)
	for i := 0; i < 10; i++ {
		groups, instances, nodes := instance.GroupRunRepository(f.groups), instance.InstanceRepository(f.instances), instance.NodeExecutionRepository(f.nodeExecs)
		resolver, selector, files := groupResolverResolver(), engine.InteractiveStreamSelector(f.selector), engine.FileReader(f.files)
		notifier, bus, waker, queue := engine.ResolutionNotifier(f.notifier), engine.EventBus(f.bus), instance.NodeAddressedWaker(f.waker), engine.QueueManager(f.queue)
		switch i {
		case 0:
			groups = nil
		case 1:
			instances = nil
		case 2:
			nodes = nil
		case 3:
			resolver = nil
		case 4:
			selector = nil
		case 5:
			files = nil
		case 6:
			notifier = nil
		case 7:
			bus = nil
		case 8:
			waker = nil
		case 9:
			queue = nil
		}
		// Convert a nil concrete resolver to a nil interface, like the real root.
		assert.Panics(t, func() {
			if i == 3 {
				engine.NewGroupResolutionBridge(groups, instances, nodes, nil, selector, files, notifier, bus, waker, queue, groupResolverTimeout)
				return
			}
			engine.NewGroupResolutionBridge(groups, instances, nodes, resolver, selector, files, notifier, bus, waker, queue, groupResolverTimeout)
		}, "collaborator %d", i)
	}
}

func TestGroupResolutionBridge_StartResolution_ReplayUsesPersistedSession(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"still conflicted"}, "s")
	first, err := f.start(1)
	require.NoError(t, err)
	second, err := f.start(1)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Len(t, f.agent.getCapturedReqs(), 1, "recovery must not launch a second agent for the same identity")
	assert.Len(t, f.sessionRows(), 1)
}

// turnAfterRead saves a conversation turn (new messages and provider session
// ID) right after the bridge reads the session record, i.e. inside the
// bridge's read-modify-write window, as the conversation handler can.
type turnAfterRead struct {
	*mockNodeExecutionRepo
	armed bool
}

func (r *turnAfterRead) ListByInstance(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	execs, err := r.mockNodeExecutionRepo.ListByInstance(ctx, id)
	if err != nil || !r.armed {
		return execs, err
	}
	r.armed = false
	for _, e := range execs {
		if e.LLMConversation["kind"] != engine.ConversationKindGroupResolution {
			continue
		}
		turn := e
		turn.LLMConversation = map[string]any{}
		for k, v := range e.LLMConversation {
			turn.LLMConversation[k] = v
		}
		turn.LLMConversation[engine.ConversationKeySessionID] = "saved-turn-session"
		turn.LLMConversation[engine.ConversationKeyMessages] = []any{map[string]any{"role": "user", "content": "saved turn"}}
		if _, err := r.UpdateIfSessionOpen(ctx, &turn); err != nil {
			return nil, err
		}
	}
	return execs, nil
}

func TestGroupResolutionBridge_SessionMarkerAndCloseKeepATurnSavedAfterTheirRead(t *testing.T) {
	for _, path := range []string{"notify", "close"} {
		t.Run(path, func(t *testing.T) {
			f := newBridgeFixture(t)
			f.agent.streamCallFn = cannedStreamWithSession([]string{"still conflicted"}, "s")
			ref, err := f.start(1)
			require.NoError(t, err)
			f.setActiveSession()
			repo := &turnAfterRead{mockNodeExecutionRepo: f.nodeExecs, armed: true}
			bridge := engine.NewGroupResolutionBridge(f.groups, f.instances, repo, groupResolverResolver(), f.selector, f.files, f.notifier, f.bus, f.waker, f.queue, groupResolverTimeout)
			ctx := context.Background()
			if path == "notify" {
				require.NoError(t, bridge.NotifyResolutionRequired(ctx, *ref))
			} else {
				require.NoError(t, bridge.CloseSession(ctx, *ref, "cancelled"))
			}
			require.False(t, repo.armed, "the turn was saved inside the bridge's window")
			rows := f.sessionRows()
			require.Len(t, rows, 1)
			conv := rows[0].LLMConversation
			assert.Equal(t, "saved-turn-session", conv[engine.ConversationKeySessionID], "provider session ID survives")
			assert.Equal(t, []any{map[string]any{"role": "user", "content": "saved turn"}}, conv[engine.ConversationKeyMessages], "saved messages survive")
			if path == "notify" {
				assert.Equal(t, true, conv["notificationSent"])
			} else {
				assert.Equal(t, "cancelled", conv[engine.ConversationKeyEndReason])
				assert.NotEmpty(t, conv[engine.ConversationKeyEndedAt])
			}
		})
	}
}
