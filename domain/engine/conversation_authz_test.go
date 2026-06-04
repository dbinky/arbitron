package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminConvoCtx returns a context whose identity is an admin distinct from the
// instance owner.
func adminConvoCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin})
}

// otherConvoCtx returns a context whose identity is a non-admin who does NOT own
// the conversation test instance.
func otherConvoCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "intruder", Role: identity.RoleUser})
}

// --- Happy path: owner may participate (covered broadly elsewhere; asserted here too) ---

func TestConversationAuthz_Owner_CanParticipate(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	llmProvider.streamCallFn = cannedStream([]string{"Sure"}, 10, 20)
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "owner message")
	require.NoError(t, err)
}

// --- Validation: admin (non-owner) may participate ---

func TestConversationAuthz_Admin_CanParticipate(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	llmProvider.streamCallFn = cannedStream([]string{"Sure"}, 10, 20)
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(adminConvoCtx(), instanceID, "admin message")
	require.NoError(t, err)

	_, err = handler.EndSession(adminConvoCtx(), instanceID)
	require.NoError(t, err)
}

// --- Error/failure: infra load error propagates (not masked as ErrNotFound) ---

func TestConversationAuthz_InfraError_Propagates(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	sentinel := errors.New("db connection reset")
	instRepo.getErr = sentinel
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "hi")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.NotErrorIs(t, err, instance.ErrNotFound)
}

// --- Boundary: viewer in wrong state sees the state error, not ErrNotFound ---

func TestConversationAuthz_OwnerWrongState_StateError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	// Flip the instance out of waiting state.
	instRepo.mu.Lock()
	instRepo.instances[instanceID].State = instance.StateRunning
	instRepo.mu.Unlock()
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "hi")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait, "the owner (a viewer) sees the state error")
	assert.NotErrorIs(t, err, instance.ErrNotFound)
}

// --- Isolation/security: non-owner non-admin is refused with ErrNotFound, no mutation ---

func TestConversationAuthz_NonOwner_RefusedNoMutation(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(otherConvoCtx(), instanceID, "intrusion")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound, "non-owner gets not-found (existence withheld)")

	_, err = handler.EndSession(otherConvoCtx(), instanceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)

	// No mutation occurred: the LLM stream was never invoked and no state change
	// or interactive event was published for this instance.
	assert.Empty(t, llmProvider.getCapturedReqs(), "the LLM stream must not run for a non-owner")
	assert.Empty(t, eventBus.eventsOfType(instanceID, "interactive.message"), "no interactive event published")
	assert.Empty(t, eventBus.eventsOfType(instanceID, "interactive.ended"), "no interactive event published")
	inst, _ := instRepo.Get(context.Background(), instanceID)
	assert.Equal(t, instance.StateWaiting, inst.State, "instance state is untouched")
}
