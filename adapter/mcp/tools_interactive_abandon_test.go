package mcp_test

import (
	"fmt"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestInteractiveAbandonSession(t *testing.T) {
	t.Run("happy path abandons the session of the given instance", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		id := uuid.New()
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": id.String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, `"status": "abandoned"`, "MarshalResult indents with two spaces")
		assert.Contains(t, text, id.String())
		assert.Equal(t, []uuid.UUID{id}, conv.abandonCalls)
	})

	t.Run("non-owner gets the not-found tool error", func(t *testing.T) {
		conv := &mockConversationHandler{abandonErr: fmt.Errorf("conversation: %w", instance.ErrNotFound)}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": uuid.New().String()})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})

	t.Run("ordinary interactive session is refused with an actionable error", func(t *testing.T) {
		conv := &mockConversationHandler{abandonErr: fmt.Errorf("conversation: %w", engine.ErrNotResolutionSession)}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": uuid.New().String()})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not a group conflict-resolution session")
		assert.Contains(t, text, "interactive_end_session")
	})

	t.Run("missing instanceId returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "instanceId is required")
		assert.Empty(t, conv.abandonCalls)
	})

	t.Run("invalid UUID returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": "not-a-uuid"})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "invalid instanceId")
	})

	t.Run("nil ConversationHandler returns not available", func(t *testing.T) {
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{Instances: &stubInstanceRepo{}, NodeExecs: &stubNodeExecRepo{}})
		adaptmcp.RegisterInteractiveTools(srv)
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": uuid.New().String()})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not available")
	})
}
