package mcp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/instance"
)

// --- Interactive Mocks ---

type mockConversationHandler struct {
	handleErr error
	endOutput map[string]any
	endErr    error
}

func (m *mockConversationHandler) HandleMessage(_ context.Context, _ uuid.UUID, _ string) error {
	return m.handleErr
}

func (m *mockConversationHandler) EndSession(_ context.Context, _ uuid.UUID) (map[string]any, error) {
	return m.endOutput, m.endErr
}

// --- Helpers ---

func newInteractiveServer(conv *mockConversationHandler, instRepo *stubInstanceRepo, nodeExecs *stubNodeExecRepo) *adaptmcp.Server {
	// interactive_get_history now reads through the scoped Engine.GetInstance.
	// Default it to the repo (unconstrained) so the existing history tests keep
	// passing; scoping is unit-tested in the engine package.
	eng := &stubEngine{
		getFn: func(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
			return instRepo.Get(ctx, id)
		},
	}
	deps := &adaptmcp.Deps{
		ConversationHandler: conv,
		Instances:           instRepo,
		NodeExecs:           nodeExecs,
		Engine:              eng,
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterInteractiveTools(srv)
	return srv
}

// --- Tests ---

func TestInteractiveSendMessage(t *testing.T) {
	t.Run("happy path returns success", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_send_message", map[string]any{
			"instanceId": uuid.New().String(),
			"content":    "Hello, world!",
		})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "sent")
	})

	t.Run("non-owner gets the not-found tool error (gate enforced in the service)", func(t *testing.T) {
		// The ConversationHandler's ownership gate returns ErrNotFound for a
		// non-owner; the MCP tool surfaces it as a not-found tool error, identical
		// to a missing instance (no existence leak).
		conv := &mockConversationHandler{handleErr: instance.ErrNotFound}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_send_message", map[string]any{
			"instanceId": uuid.New().String(),
			"content":    "intrusion",
		})

		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})

	t.Run("missing instanceId returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_send_message", map[string]any{
			"content": "Hello",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "instanceId is required")
	})

	t.Run("invalid UUID returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_send_message", map[string]any{
			"instanceId": "not-a-uuid",
			"content":    "Hello",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid instanceId")
	})

	t.Run("missing content returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_send_message", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "content is required")
	})

	t.Run("handler error returned as tool error", func(t *testing.T) {
		conv := &mockConversationHandler{
			handleErr: errors.New("instance is not in interactive wait state"),
		}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_send_message", map[string]any{
			"instanceId": uuid.New().String(),
			"content":    "Hello",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not in interactive wait state")
	})

	t.Run("nil ConversationHandler returns not available", func(t *testing.T) {
		deps := &adaptmcp.Deps{
			Instances: &stubInstanceRepo{},
			NodeExecs: &stubNodeExecRepo{},
		}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterInteractiveTools(srv)
		result := callTool(t, srv, "interactive_send_message", map[string]any{
			"instanceId": uuid.New().String(),
			"content":    "Hello",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not available")
	})
}

func TestInteractiveEndSession(t *testing.T) {
	t.Run("happy path returns output", func(t *testing.T) {
		conv := &mockConversationHandler{
			endOutput: map[string]any{"summary": "session ended", "score": 42},
		}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_end_session", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "summary")
		assert.Contains(t, text, "session ended")
		assert.Contains(t, text, "42")
	})

	t.Run("non-owner gets the not-found tool error (gate enforced in the service)", func(t *testing.T) {
		// The ConversationHandler's ownership gate returns ErrNotFound for a
		// non-owner; the MCP tool surfaces it as a not-found tool error, identical
		// to a missing instance (no existence leak).
		conv := &mockConversationHandler{endErr: instance.ErrNotFound}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_end_session", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})

	t.Run("missing instanceId returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_end_session", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "instanceId is required")
	})

	t.Run("handler error returned as tool error", func(t *testing.T) {
		conv := &mockConversationHandler{
			endErr: errors.New("instance is not in interactive wait state"),
		}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_end_session", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not in interactive wait state")
	})

	t.Run("invalid UUID returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_end_session", map[string]any{
			"instanceId": "not-a-uuid",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid instanceId")
	})

	t.Run("nil ConversationHandler returns not available", func(t *testing.T) {
		deps := &adaptmcp.Deps{
			Instances: &stubInstanceRepo{},
			NodeExecs: &stubNodeExecRepo{},
		}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterInteractiveTools(srv)
		result := callTool(t, srv, "interactive_end_session", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not available")
	})
}

func TestInteractiveGetHistory(t *testing.T) {
	t.Run("happy path returns conversation", func(t *testing.T) {
		instID := uuid.New()
		nodeID := "interactive-1"
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return &instance.Instance{
					ID:            instID,
					CurrentNodeID: &nodeID,
				}, nil
			},
		}
		nodeExecRepo := &stubNodeExecRepo{
			getByInstanceAndNode: func(_ context.Context, _ uuid.UUID, nid string) (*instance.NodeExecution, error) {
				require.Equal(t, nodeID, nid)
				return &instance.NodeExecution{
					NodeID: nid,
					LLMConversation: map[string]any{
						"messages": []any{
							map[string]any{"role": "user", "content": "Hello"},
							map[string]any{"role": "assistant", "content": "Hi there!"},
						},
					},
				}, nil
			},
		}
		srv := newInteractiveServer(nil, instRepo, nodeExecRepo)
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": instID.String(),
		})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "Hello")
		assert.Contains(t, text, "Hi there!")
	})

	t.Run("missing instanceId returns error", func(t *testing.T) {
		srv := newInteractiveServer(nil, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_get_history", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "instanceId is required")
	})

	t.Run("invalid UUID returns error", func(t *testing.T) {
		srv := newInteractiveServer(nil, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": "not-a-uuid",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid instanceId")
	})

	t.Run("instance repo error returned as tool error", func(t *testing.T) {
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return nil, errors.New("not found")
			},
		}
		srv := newInteractiveServer(nil, instRepo, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "failed to get instance")
	})

	t.Run("nil instance without error returns not found", func(t *testing.T) {
		// stubInstanceRepo.Get returns (nil, nil) by default — handler must guard
		// against nil inst before accessing inst.CurrentNodeID, otherwise panics.
		srv := newInteractiveServer(nil, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})

	t.Run("no current node returns error", func(t *testing.T) {
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return &instance.Instance{
					ID:            uuid.New(),
					CurrentNodeID: nil,
				}, nil
			},
		}
		srv := newInteractiveServer(nil, instRepo, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "no current node")
	})

	t.Run("empty current node returns error", func(t *testing.T) {
		empty := ""
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return &instance.Instance{
					ID:            uuid.New(),
					CurrentNodeID: &empty,
				}, nil
			},
		}
		srv := newInteractiveServer(nil, instRepo, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "no current node")
	})

	t.Run("node execution not found returns error", func(t *testing.T) {
		nodeID := "interactive-1"
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return &instance.Instance{
					ID:            uuid.New(),
					CurrentNodeID: &nodeID,
				}, nil
			},
		}
		nodeExecRepo := &stubNodeExecRepo{
			getByInstanceAndNode: func(_ context.Context, _ uuid.UUID, _ string) (*instance.NodeExecution, error) {
				return nil, errors.New("not found")
			},
		}
		srv := newInteractiveServer(nil, instRepo, nodeExecRepo)
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "failed to get node execution")
	})

	t.Run("nil exec without error returns not found", func(t *testing.T) {
		nodeID := "interactive-1"
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return &instance.Instance{
					ID:            uuid.New(),
					CurrentNodeID: &nodeID,
				}, nil
			},
		}
		// stubNodeExecRepo default: returns (nil, nil) when getByInstanceAndNode is unset
		srv := newInteractiveServer(nil, instRepo, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_get_history", map[string]any{
			"instanceId": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})
}
