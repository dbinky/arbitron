package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildAuditedConversationHandler builds a conversation handler wired with both
// an intervention repo and an audit recorder (the two attribution sinks), so the
// interactive-path audit emission can be asserted.
func buildAuditedConversationHandler(
	instRepo *mockInstanceRepo,
	nodeExecRepo *mockNodeExecutionRepo,
	llmProvider *mockStreamingChatProvider,
	eventBus *mockEventBus,
	queueMgr *mockQueueManager,
	ivRepo instance.InterventionRepository,
	recorder engine.AuditRecorder,
) engine.ConversationHandler {
	h := engine.NewConversationHandler(
		instRepo, nodeExecRepo, streamSelector(llmProvider), interactiveResolver(), eventBus, queueMgr,
	)
	if s, ok := h.(interface {
		SetInterventions(instance.InterventionRepository)
	}); ok {
		s.SetInterventions(ivRepo)
	}
	if s, ok := h.(interface {
		SetAuditRecorder(engine.AuditRecorder)
	}); ok {
		s.SetAuditRecorder(recorder)
	}
	return h
}

// PR #21 Must Fix (missing interactive-session audit event): a lead/admin who
// steers an interactive session they do not own must leave an audit trail. This
// was the accountability hole — recordInteractiveIntervention wrote only the
// intervention row and never called the auditRecorder, so a forensic query
// returned nothing for a lead reading/steering a subordinate's live session.
func TestConversationAudit_Lead_HandleMessage_EmitsAuditEvent(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Stamp the instance's department so the lead gate (and classification) apply.
	instRepo.mu.Lock()
	instRepo.instances[instanceID].Department = "engineering"
	instRepo.instances[instanceID].Visibility = instance.VisibilityDepartment
	instRepo.mu.Unlock()

	llmProvider.streamCallFn = cannedStream([]string{"Sure"}, 10, 20)
	ivRepo := &fakeInterventionRepo{}
	audit := &fakeAuditRecorder{}
	handler := buildAuditedConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, ivRepo, audit)

	leadCtx := identity.WithIdentity(t.Context(), identity.Identity{
		UserID:         uuid.New(), // distinct from convoTestOwner
		Username:       "eng-lead",
		Role:           identity.RoleUser,
		Departments:    []string{"engineering"},
		LedDepartments: []string{"engineering"},
	})

	require.NoError(t, handler.HandleMessage(leadCtx, instanceID, "lead steering message"))

	// Accountability: the lead's participation must be on the audit trail.
	events := audit.all()
	require.Len(t, events, 1, "a non-owner lead steering an interactive session must emit one audit event")
	assert.Equal(t, engine.AuditActionLeadIntervention, events[0].action)
	assert.Equal(t, instanceID.String(), events[0].target, "target is the instance id")
	// The intervention attribution row is still written alongside the audit event.
	require.Len(t, ivRepo.rows(), 1, "the intervention attribution row is still written")
}

// The owner steering their own interactive session emits NO audit event (a
// self-action is not a privileged override).
func TestConversationAudit_Owner_HandleMessage_NoAuditEvent(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	llmProvider.streamCallFn = cannedStream([]string{"Sure"}, 10, 20)
	ivRepo := &fakeInterventionRepo{}
	audit := &fakeAuditRecorder{}
	handler := buildAuditedConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, ivRepo, audit)

	require.NoError(t, handler.HandleMessage(convoCtx(), instanceID, "owner message"))
	assert.Empty(t, audit.all(), "owner self-participation is not audited")
}
