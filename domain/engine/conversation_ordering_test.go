package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pause only the first copied read, before the handler acquires its operation
// guard. Later reads see the repository's current state, like SQLite Get.
type conversationReadBarrier struct {
	instance.InstanceRepository
	paused  atomic.Bool
	read    chan struct{}
	release chan struct{}
}

func (r *conversationReadBarrier) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := r.InstanceRepository.Get(ctx, id)
	if err != nil || !r.paused.CompareAndSwap(false, true) {
		return inst, err
	}
	close(r.read)
	select {
	case <-r.release:
		return inst, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func pendingConversationOperation(t *testing.T, barrier *conversationReadBarrier, handler engine.ConversationHandler, id uuid.UUID, operation string) (<-chan error, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(convoCtx(), 3*time.Second)
	t.Cleanup(cancel)
	var once sync.Once
	release := func() { once.Do(func() { close(barrier.release) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() {
		if operation == "message" {
			done <- handler.HandleMessage(ctx, id, "pending")
		} else {
			_, err := handler.EndSession(ctx, id)
			done <- err
		}
	}()
	select {
	case <-barrier.read:
	case <-ctx.Done():
		t.Fatal("pending operation never read the waiting instance")
	}
	return done, release
}

// Removing the protected reload allows a stale send to resume an ended harness
// session, or a stale EndSession to complete/release/notify the same session twice.
func TestConversationHandler_EndSessionInvalidatesPendingOperation(t *testing.T) {
	for _, operation := range []string{"message", "end"} {
		t.Run(operation, func(t *testing.T) {
			instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
			barrier := &conversationReadBarrier{InstanceRepository: instRepo, read: make(chan struct{}), release: make(chan struct{})}
			llm.streamCallFn = cannedStreamWithSession([]string{"stale response"}, "s")
			lookup := &conversationPinLookup{t: t, id: id}
			resolver := provider.NewPinningResolver(interactiveResolver(), lookup, engine.InstanceIDFromContext).ForRun()
			handler := engine.NewConversationHandler(barrier, nodeExecRepo, streamSelector(llm), resolver, bus, queue)
			var released []uuid.UUID
			handler.(pinReleaseSetter).SetPinCacheRelease(func(id uuid.UUID) { released = append(released, id) })

			done, release := pendingConversationOperation(t, barrier, handler, id, operation)
			_, err := handler.EndSession(convoCtx(), id)
			require.NoError(t, err)
			stored, err := nodeExecRepo.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			release()
			assert.ErrorIs(t, <-done, instance.ErrNotInteractiveWait)

			after, err := json.Marshal(stored)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "stale operation must preserve the completed conversation/output")
			assert.Empty(t, llm.getCapturedReqs(), "ended session must never be resumed")
			assert.Zero(t, lookup.calls, "refuse before resolving model pins")
			assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveMessage))
			assert.Len(t, bus.eventsOfType(id, engine.EventTypeInteractiveEnded), 1)
			assert.Equal(t, []uuid.UUID{id}, released, "only the successful completion releases pins")
			assert.Equal(t, 1, queue.notifyCount())
			inst, err := instRepo.Get(convoCtx(), id)
			require.NoError(t, err)
			assert.Equal(t, instance.StateQueued, inst.State)
		})
	}
}

// Protected validation must repeat authorization too: ownership/visibility can
// change after the preliminary check. Neither operation may use the old grant.
func TestConversationHandler_PendingOperationRechecksAuthorization(t *testing.T) {
	for _, operation := range []string{"message", "end"} {
		for _, visibility := range []instance.Visibility{instance.VisibilityPrivate, instance.VisibilityPublic} {
			t.Run(operation+"/"+string(visibility), func(t *testing.T) {
				instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
				barrier := &conversationReadBarrier{InstanceRepository: instRepo, read: make(chan struct{}), release: make(chan struct{})}
				llm.streamCallFn = cannedStream([]string{"authorized retry"}, 1, 1)
				handler := engine.NewConversationHandler(barrier, nodeExecRepo, streamSelector(llm), interactiveResolver(), bus, queue)
				done, release := pendingConversationOperation(t, barrier, handler, id, operation)
				instRepo.mu.Lock()
				instRepo.instances[id].OwnerID = uuid.New()
				instRepo.instances[id].Visibility = visibility
				instRepo.mu.Unlock()
				release()
				want := instance.ErrNotFound
				if visibility == instance.VisibilityPublic {
					want = instance.ErrForbidden
				}
				assert.ErrorIs(t, <-done, want)
				assert.Empty(t, llm.getCapturedReqs())
				assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveMessage))
				assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveEnded))
				assert.Zero(t, queue.notifyCount())
				stored, err := nodeExecRepo.GetByInstanceAndNode(convoCtx(), id, "interact")
				require.NoError(t, err)
				assert.Len(t, extractMessages(t, stored.LLMConversation), 2)
				assert.Nil(t, stored.Output)

				// Revalidation refusal releases the guard; an authorized retry succeeds.
				if operation == "message" {
					require.NoError(t, handler.HandleMessage(adminConvoCtx(), id, "retry"))
				} else {
					_, err = handler.EndSession(adminConvoCtx(), id)
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestConversationHandler_ProtectedReadFaultRefusesAndAllowsRetry(t *testing.T) {
	for _, operation := range []string{"message", "end"} {
		t.Run(operation, func(t *testing.T) {
			instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
			barrier := &conversationReadBarrier{InstanceRepository: instRepo, read: make(chan struct{}), release: make(chan struct{})}
			llm.streamCallFn = cannedStreamWithSession([]string{"verified retry"}, "s")
			stored, err := nodeExecRepo.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			stored.LLMConversation[engine.ConversationKeySessionID] = "s"
			lookup := &conversationPinLookup{t: t, id: id}
			resolver := provider.NewPinningResolver(interactiveResolver(), lookup, engine.InstanceIDFromContext).ForRun()
			handler := engine.NewConversationHandler(barrier, nodeExecRepo, streamSelector(llm), resolver, bus, queue)
			done, release := pendingConversationOperation(t, barrier, handler, id, operation)
			fault := errors.New("protected instance read failed")
			instRepo.mu.Lock()
			instRepo.getErr = fault
			instRepo.mu.Unlock()
			release()
			assert.ErrorIs(t, <-done, fault)
			assert.Empty(t, llm.getCapturedReqs())
			assert.Zero(t, lookup.calls)
			assert.Len(t, extractMessages(t, stored.LLMConversation), 2)
			assert.Nil(t, stored.Output)
			assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveMessage))
			assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveEnded))
			assert.Zero(t, queue.notifyCount())
			instRepo.mu.Lock()
			instRepo.getErr = nil
			instRepo.mu.Unlock()
			if operation == "message" {
				require.NoError(t, handler.HandleMessage(convoCtx(), id, "retry"))
				reqs := llm.getCapturedReqs()
				require.Len(t, reqs, 1)
				assert.Equal(t, "version-X", reqs[0].Model)
				assert.True(t, reqs[0].RequireExecutedModel)
				assert.Equal(t, "s", reqs[0].SessionID)
			} else {
				_, err = handler.EndSession(convoCtx(), id)
				require.NoError(t, err)
			}
		})
	}
}
