package mcp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/instance"
)

// --- Trace Mocks ---

type stubNodeExecRepo struct {
	listByInstanceFn     func(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)
	getByInstanceAndNode func(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)
}

func (s *stubNodeExecRepo) Create(_ context.Context, _ *instance.NodeExecution) error { return nil }
func (s *stubNodeExecRepo) Update(_ context.Context, _ *instance.NodeExecution) error { return nil }

func (s *stubNodeExecRepo) ListByInstance(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	if s.listByInstanceFn != nil {
		return s.listByInstanceFn(ctx, id)
	}
	return nil, nil
}

func (s *stubNodeExecRepo) GetByInstanceAndNode(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	if s.getByInstanceAndNode != nil {
		return s.getByInstanceAndNode(ctx, id, nodeID)
	}
	return nil, nil
}

type stubSnapshotRepo struct {
	listByInstanceFn func(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)
}

func (s *stubSnapshotRepo) Create(_ context.Context, _ *instance.VariableSnapshot) error { return nil }

func (s *stubSnapshotRepo) ListByInstance(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
	if s.listByInstanceFn != nil {
		return s.listByInstanceFn(ctx, id)
	}
	return nil, nil
}

// --- Helpers ---

func newTraceServer(nodeExecs *stubNodeExecRepo, snapshots *stubSnapshotRepo, instRepo *stubInstanceRepo) *adaptmcp.Server {
	deps := &adaptmcp.Deps{
		NodeExecs: nodeExecs,
		Snapshots: snapshots,
		Instances: instRepo,
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterTraceTools(srv)
	return srv
}

// --- Tests ---

func TestTraceGet(t *testing.T) {
	t.Run("happy path returns node executions", func(t *testing.T) {
		instID := uuid.New()
		repo := &stubNodeExecRepo{
			listByInstanceFn: func(_ context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
				return []instance.NodeExecution{
					{ID: uuid.New(), InstanceID: id, NodeID: "node1", Status: instance.NodeStatusSucceeded},
				}, nil
			},
		}
		srv := newTraceServer(repo, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get", map[string]any{"id": instID.String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "node1")
	})

	t.Run("missing id returns error", func(t *testing.T) {
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubNodeExecRepo{
			listByInstanceFn: func(_ context.Context, _ uuid.UUID) ([]instance.NodeExecution, error) {
				return nil, errors.New("db error")
			},
		}
		srv := newTraceServer(repo, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
	})

	t.Run("nil repo result produces JSON array not null", func(t *testing.T) {
		// Edge case: no executions yet; handler must normalise nil to [] so
		// LLM clients can iterate without hitting a null value.
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get", map[string]any{"id": uuid.New().String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Equal(t, "[]", text)
	})
}

func TestTraceGetNode(t *testing.T) {
	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubNodeExecRepo{
			getByInstanceAndNode: func(_ context.Context, _ uuid.UUID, _ string) (*instance.NodeExecution, error) {
				return nil, errors.New("exec not found")
			},
		}
		srv := newTraceServer(repo, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get_node", map[string]any{
			"instanceId": uuid.New().String(),
			"nodeId":     "step1",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "exec not found")
	})

	t.Run("happy path returns node execution", func(t *testing.T) {
		instID := uuid.New()
		repo := &stubNodeExecRepo{
			getByInstanceAndNode: func(_ context.Context, _ uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
				return &instance.NodeExecution{NodeID: nodeID, Status: instance.NodeStatusSucceeded}, nil
			},
		}
		srv := newTraceServer(repo, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get_node", map[string]any{
			"instanceId": instID.String(),
			"nodeId":     "step1",
		})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "step1")
	})

	t.Run("missing nodeId returns error", func(t *testing.T) {
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get_node", map[string]any{"instanceId": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "nodeId is required")
	})

	t.Run("missing instanceId returns error", func(t *testing.T) {
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get_node", map[string]any{"nodeId": "x"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "instanceId is required")
	})

	t.Run("invalid instanceId returns error", func(t *testing.T) {
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get_node", map[string]any{
			"instanceId": "bad",
			"nodeId":     "x",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid instanceId")
	})
}

func TestVariablesGet(t *testing.T) {
	t.Run("missing id returns error", func(t *testing.T) {
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "variables_get", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "variables_get", map[string]any{"id": "not-a-uuid"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("happy path returns instance variables", func(t *testing.T) {
		instID := uuid.New()
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return &instance.Instance{
					ID:        instID,
					Variables: map[string]any{"key": "value"},
				}, nil
			},
		}
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, instRepo)
		result := callTool(t, srv, "variables_get", map[string]any{"id": instID.String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "key")
		assert.Contains(t, text, "value")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		instRepo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return nil, errors.New("not found")
			},
		}
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, instRepo)
		result := callTool(t, srv, "variables_get", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
	})
}

func TestVariablesHistory(t *testing.T) {
	t.Run("missing id returns error", func(t *testing.T) {
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "variables_history", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("nil repo result produces JSON array not null", func(t *testing.T) {
		// Edge case: no snapshots yet; handler must normalise nil to [].
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "variables_history", map[string]any{"id": uuid.New().String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Equal(t, "[]", text)
	})

	t.Run("happy path returns snapshots", func(t *testing.T) {
		instID := uuid.New()
		snapRepo := &stubSnapshotRepo{
			listByInstanceFn: func(_ context.Context, _ uuid.UUID) ([]instance.VariableSnapshot, error) {
				return []instance.VariableSnapshot{
					{
						ID:         uuid.New(),
						InstanceID: instID,
						NodeID:     "node1",
						Variables:  map[string]any{"x": 1},
						CapturedAt: time.Now(),
					},
				}, nil
			},
		}
		srv := newTraceServer(&stubNodeExecRepo{}, snapRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "variables_history", map[string]any{"id": instID.String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "node1")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		snapRepo := &stubSnapshotRepo{
			listByInstanceFn: func(_ context.Context, _ uuid.UUID) ([]instance.VariableSnapshot, error) {
				return nil, errors.New("storage error")
			},
		}
		srv := newTraceServer(&stubNodeExecRepo{}, snapRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "variables_history", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
	})
}
