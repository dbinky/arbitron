package mcp_test

import (
	"context"
	"errors"
	"fmt"
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

func (s *stubNodeExecRepo) AggregateByInstances(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	return map[uuid.UUID]instance.NodeExecutionAggregate{}, nil
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
	// Trace tools now flow through the scoped Engine read services. Build an
	// engine stub that delegates to the repos (unconstrained scope) so the
	// existing trace tests keep exercising the same repo behavior; scoping is
	// unit-tested in the engine package.
	eng := &stubEngine{
		traceFn: func(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
			return nodeExecs.ListByInstance(ctx, id)
		},
		nodeTraceFn: func(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
			return nodeExecs.GetByInstanceAndNode(ctx, id, nodeID)
		},
		varHistFn: func(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
			return snapshots.ListByInstance(ctx, id)
		},
	}
	if instRepo != nil {
		eng.variablesFn = func(ctx context.Context, id uuid.UUID) (map[string]any, error) {
			inst, err := instRepo.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			if inst == nil {
				// Mirror engine.GetInstance: (nil, nil) from a repo is absence.
				return nil, instance.ErrNotFound
			}
			return inst.Variables, nil
		}
	}
	deps := &adaptmcp.Deps{
		NodeExecs: nodeExecs,
		Snapshots: snapshots,
		Instances: instRepo,
		Engine:    eng,
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

	t.Run("non-owner gets the same not-found tool error as a missing instance", func(t *testing.T) {
		// The scoped engine returns ErrNotFound for a non-owner; the node-exec repo
		// must never be queried, and the surfaced tool error must be identical to a
		// genuinely missing instance (no existence leak).
		queried := false
		nodeRepo := &stubNodeExecRepo{
			listByInstanceFn: func(_ context.Context, _ uuid.UUID) ([]instance.NodeExecution, error) {
				queried = true
				return nil, nil
			},
		}
		eng := &stubEngine{
			traceFn: func(_ context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
				return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		deps := &adaptmcp.Deps{NodeExecs: nodeRepo, Snapshots: &stubSnapshotRepo{}, Engine: eng}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterTraceTools(srv)

		result := callTool(t, srv, "trace_get", map[string]any{"id": uuid.New().String()})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
		assert.False(t, queried, "trace must not be read for a non-owner")
	})
}

func TestTraceInterventions(t *testing.T) {
	t.Run("returns the audit trail for a viewer", func(t *testing.T) {
		instID := uuid.New()
		detail := "high"
		eng := &stubEngine{
			interventionsFn: func(_ context.Context, _ uuid.UUID) ([]instance.Intervention, error) {
				return []instance.Intervention{
					{ID: uuid.New(), InstanceID: instID, ActorUserID: uuid.New(), Action: instance.ActionSetPriority, Detail: &detail},
				}, nil
			},
		}
		deps := &adaptmcp.Deps{Engine: eng}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterTraceTools(srv)

		result := callTool(t, srv, "trace_interventions", map[string]any{"id": instID.String()})
		assert.False(t, result.IsError)
		assert.Contains(t, extractText(t, result), "set_priority")
	})

	t.Run("non-viewer gets the not-found tool error", func(t *testing.T) {
		eng := &stubEngine{
			interventionsFn: func(_ context.Context, id uuid.UUID) ([]instance.Intervention, error) {
				return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		deps := &adaptmcp.Deps{Engine: eng}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterTraceTools(srv)

		result := callTool(t, srv, "trace_interventions", map[string]any{"id": uuid.New().String()})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})

	t.Run("empty result is a JSON array not null", func(t *testing.T) {
		eng := &stubEngine{
			interventionsFn: func(_ context.Context, _ uuid.UUID) ([]instance.Intervention, error) {
				return nil, nil
			},
		}
		deps := &adaptmcp.Deps{Engine: eng}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterTraceTools(srv)

		result := callTool(t, srv, "trace_interventions", map[string]any{"id": uuid.New().String()})
		assert.False(t, result.IsError)
		assert.Equal(t, "[]", extractText(t, result))
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

	t.Run("nil exec without error returns not found", func(t *testing.T) {
		// stubNodeExecRepo default returns (nil, nil) — handler must guard against nil exec
		// before accessing exec fields, otherwise it returns confusing JSON null.
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "trace_get_node", map[string]any{
			"instanceId": uuid.New().String(),
			"nodeId":     "step1",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})

	t.Run("non-owner gets the same not-found error as a missing instance (no existence leak)", func(t *testing.T) {
		// The engine's authorizeView gate fires before any node-exec repo access.
		// A non-owner must see an identical error to "instance not found", and the
		// node-exec repo must never be queried (existence of the instance must not
		// be leaked via a timing or error difference).
		queried := false
		nodeRepo := &stubNodeExecRepo{
			getByInstanceAndNode: func(_ context.Context, _ uuid.UUID, _ string) (*instance.NodeExecution, error) {
				queried = true
				return nil, nil
			},
		}
		eng := &stubEngine{
			nodeTraceFn: func(_ context.Context, id uuid.UUID, _ string) (*instance.NodeExecution, error) {
				return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		deps := &adaptmcp.Deps{NodeExecs: nodeRepo, Snapshots: &stubSnapshotRepo{}, Engine: eng}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterTraceTools(srv)

		result := callTool(t, srv, "trace_get_node", map[string]any{
			"instanceId": uuid.New().String(),
			"nodeId":     "step1",
		})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
		assert.False(t, queried, "node-exec repo must not be read for a non-owner")
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

	t.Run("nil instance without error returns not found", func(t *testing.T) {
		// stubInstanceRepo.Get returns (nil, nil) by default — handler must guard against
		// nil inst before accessing inst.Variables, otherwise it panics.
		srv := newTraceServer(&stubNodeExecRepo{}, &stubSnapshotRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "variables_get", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
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

	t.Run("non-owner gets the same not-found error as a missing instance (no existence leak)", func(t *testing.T) {
		// The engine's authorizeView gate fires before the snapshot repo is read.
		// A non-owner must receive a tool error identical to a missing instance,
		// and the snapshot repo must never be queried.
		queried := false
		snapRepo := &stubSnapshotRepo{
			listByInstanceFn: func(_ context.Context, _ uuid.UUID) ([]instance.VariableSnapshot, error) {
				queried = true
				return nil, nil
			},
		}
		eng := &stubEngine{
			varHistFn: func(_ context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
				return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		deps := &adaptmcp.Deps{NodeExecs: &stubNodeExecRepo{}, Snapshots: snapRepo, Engine: eng}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterTraceTools(srv)

		result := callTool(t, srv, "variables_history", map[string]any{"id": uuid.New().String()})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
		assert.False(t, queried, "snapshot repo must not be read for a non-owner")
	})
}
