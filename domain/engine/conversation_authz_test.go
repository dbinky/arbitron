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

// --- Edge: no identity in context → fail-closed (identical response to non-owner) ---

func TestConversationAuthz_NoIdentity_FailClosed(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	// context.Background() carries no identity. canView() returns false → loadAndValidate
	// returns ErrNotFound before any state is revealed. This pins the fail-closed invariant:
	// an unauthenticated request (e.g. auth middleware misconfiguration) behaves identically
	// to a non-owner request — existence and state are both withheld.
	err := handler.HandleMessage(context.Background(), instanceID, "intrusion")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound, "no-identity → ErrNotFound (fail-closed)")
	assert.NotErrorIs(t, err, instance.ErrNotInteractiveWait, "state must not be revealed to unauthenticated caller")

	_, err = handler.EndSession(context.Background(), instanceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound, "EndSession is also fail-closed for no-identity context")
	assert.NotErrorIs(t, err, instance.ErrNotInteractiveWait)

	// No mutation: LLM was never invoked; no interactive events were published.
	assert.Empty(t, llmProvider.getCapturedReqs(), "LLM must not run for unauthenticated caller")
	assert.Empty(t, eventBus.eventsOfType(instanceID, "interactive.message"), "no interactive event published")
	assert.Empty(t, eventBus.eventsOfType(instanceID, "interactive.ended"), "no interactive event published")
	inst, _ := instRepo.Get(context.Background(), instanceID)
	assert.Equal(t, instance.StateWaiting, inst.State, "instance state is untouched")
}

// --- RBAC-INTERACT-001: department lead may participate; peer (member, not lead) may NOT ---

// TestConversationAuthz_DepartmentLead_CanParticipate pins the explicit design
// decision: participation is routed through canIntervene, which includes leads.
// A department lead who does not own the run may send messages and end sessions.
func TestConversationAuthz_DepartmentLead_CanParticipate(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Stamp the instance with a department so the lead gate is meaningful.
	instRepo.mu.Lock()
	instRepo.instances[instanceID].Department = "engineering"
	instRepo.instances[instanceID].Visibility = instance.VisibilityDepartment
	instRepo.mu.Unlock()

	llmProvider.streamCallFn = cannedStream([]string{"Sure"}, 10, 20)
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	// A lead of "engineering" who is NOT the owner may participate.
	leadCtxLocal := identity.WithIdentity(context.Background(), identity.Identity{
		UserID:         uuid.New(), // distinct from convoTestOwner
		Username:       "eng-lead",
		Role:           identity.RoleUser,
		Departments:    []string{"engineering"},
		LedDepartments: []string{"engineering"},
	})

	err := handler.HandleMessage(leadCtxLocal, instanceID, "lead message")
	require.NoError(t, err, "department lead must be able to participate in an interactive session (canIntervene=true)")
}

// TestConversationAuthz_DepartmentPeer_Forbidden pins the read-vs-act split for
// interactive sessions: a department member (viewer but not owner/lead/admin)
// gets ErrForbidden — existence is not withheld, but participation is denied.
func TestConversationAuthz_DepartmentPeer_Forbidden(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	instRepo.mu.Lock()
	instRepo.instances[instanceID].Department = "engineering"
	instRepo.instances[instanceID].Visibility = instance.VisibilityDepartment
	instRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	// A member of "engineering" who does NOT lead it gets ErrForbidden (not ErrNotFound).
	peerCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID:         uuid.New(),
		Username:       "eng-peer",
		Role:           identity.RoleUser,
		Departments:    []string{"engineering"},
		LedDepartments: nil,
	})

	err := handler.HandleMessage(peerCtx, instanceID, "peer message")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrForbidden, "department peer (viewer) must get ErrForbidden, not ErrNotFound")
	assert.NotErrorIs(t, err, instance.ErrNotFound, "existence must not be withheld from a department viewer")

	_, err = handler.EndSession(peerCtx, instanceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrForbidden)
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
