package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// --- Instance Mocks ---

type stubInstanceRepo struct {
	getFn  func(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
	listFn func(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error)
}

func (s *stubInstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	if s.getFn != nil {
		return s.getFn(ctx, id)
	}
	return nil, nil
}

func (s *stubInstanceRepo) List(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	if s.listFn != nil {
		return s.listFn(ctx, filters)
	}
	return nil, nil
}

// Implement remaining interface methods as no-ops.
func (s *stubInstanceRepo) Create(_ context.Context, _ *instance.Instance) error { return nil }
func (s *stubInstanceRepo) UpdateState(_ context.Context, _ uuid.UUID, _ instance.State) error {
	return nil
}
func (s *stubInstanceRepo) UpdateVariables(_ context.Context, _ uuid.UUID, _ map[string]any) error {
	return nil
}
func (s *stubInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (s *stubInstanceRepo) SetFailed(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (s *stubInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error        { return nil }
func (s *stubInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (s *stubInstanceRepo) UpdatePriority(_ context.Context, _ uuid.UUID, _ instance.Priority) error {
	return nil
}

func (s *stubInstanceRepo) SetWaiting(_ context.Context, _ uuid.UUID, _ *time.Time) error {
	return nil
}

func (s *stubInstanceRepo) SetWebhookFields(_ context.Context, _ uuid.UUID, _ *string, _ *string) error {
	return nil
}

func (s *stubInstanceRepo) SetWebhookReceived(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

func (s *stubInstanceRepo) Stats(_ context.Context, _ string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) {
	return nil, nil
}

type stubEngine struct {
	triggerFn          func(wfName string, vars map[string]any, priority string) (uuid.UUID, bool, error)
	capturedTriggerCtx context.Context
	capturedDepartment string
	listFn             func(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error)
	capturedListCtx    context.Context
	listEnrichedFn     func(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
	getFn              func(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
	traceFn            func(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)
	nodeTraceFn        func(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)
	variablesFn        func(ctx context.Context, id uuid.UUID) (map[string]any, error)
	varHistFn          func(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)
	interventionsFn    func(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error)
	pauseFn            func(id uuid.UUID) error
	resumeFn           func(id uuid.UUID) error
	cancelFn           func(id uuid.UUID) error
	retryFn            func(id uuid.UUID, vars map[string]any) error
	rerunFn            func(id uuid.UUID) (uuid.UUID, error)
	setPriorityFn      func(id uuid.UUID, priority string) error
}

func (s *stubEngine) TriggerWithDepartment(ctx context.Context, wfName string, vars map[string]any, priority string, department string) (uuid.UUID, bool, error) {
	s.capturedTriggerCtx = ctx
	s.capturedDepartment = department
	if s.triggerFn != nil {
		return s.triggerFn(wfName, vars, priority)
	}
	return uuid.New(), false, nil
}

func (s *stubEngine) ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	s.capturedListCtx = ctx
	if s.listFn != nil {
		return s.listFn(ctx, filters)
	}
	return nil, nil
}

func (s *stubEngine) ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
	if s.listEnrichedFn != nil {
		return s.listEnrichedFn(ctx, filters)
	}
	summaries, err := s.ListInstances(ctx, filters)
	if err != nil {
		return nil, err
	}
	out := make([]instance.EnrichedInstanceSummary, len(summaries))
	for i, sm := range summaries {
		out[i] = instance.EnrichedInstanceSummary{InstanceSummary: sm}
	}
	return out, nil
}

func (s *stubEngine) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	if s.getFn != nil {
		return s.getFn(ctx, id)
	}
	return nil, instance.ErrNotFound
}

func (s *stubEngine) GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	if s.traceFn != nil {
		return s.traceFn(ctx, id)
	}
	return nil, nil
}

func (s *stubEngine) GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	if s.nodeTraceFn != nil {
		return s.nodeTraceFn(ctx, id, nodeID)
	}
	return nil, instance.ErrNotFound
}

func (s *stubEngine) GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error) {
	if s.variablesFn != nil {
		return s.variablesFn(ctx, id)
	}
	return nil, instance.ErrNotFound
}

func (s *stubEngine) GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
	if s.varHistFn != nil {
		return s.varHistFn(ctx, id)
	}
	return nil, nil
}

func (s *stubEngine) GetInterventions(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error) {
	if s.interventionsFn != nil {
		return s.interventionsFn(ctx, id)
	}
	return nil, nil
}

func (s *stubEngine) Pause(_ context.Context, id uuid.UUID) error {
	if s.pauseFn != nil {
		return s.pauseFn(id)
	}
	return nil
}

func (s *stubEngine) Resume(_ context.Context, id uuid.UUID) error {
	if s.resumeFn != nil {
		return s.resumeFn(id)
	}
	return nil
}

func (s *stubEngine) Cancel(_ context.Context, id uuid.UUID) error {
	if s.cancelFn != nil {
		return s.cancelFn(id)
	}
	return nil
}

func (s *stubEngine) Retry(_ context.Context, id uuid.UUID, vars map[string]any) error {
	if s.retryFn != nil {
		return s.retryFn(id, vars)
	}
	return nil
}

func (s *stubEngine) Rerun(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	if s.rerunFn != nil {
		return s.rerunFn(id)
	}
	return uuid.New(), nil
}

func (s *stubEngine) SetPriority(_ context.Context, id uuid.UUID, priority string) error {
	if s.setPriorityFn != nil {
		return s.setPriorityFn(id, priority)
	}
	return nil
}

// --- Helpers ---

func newInstanceServer(instRepo *stubInstanceRepo, eng *stubEngine) *adaptmcp.Server {
	// instance_list now flows through the scoped Engine.ListInstances service.
	// Default the engine's list to the repo's listFn so the existing list-filter
	// tests (which seed instRepo.listFn) keep exercising the same path; scoping is
	// unit-tested in the engine.
	if eng.listFn == nil && instRepo != nil {
		eng.listFn = func(ctx context.Context, f instance.ListFilters) ([]instance.InstanceSummary, error) {
			return instRepo.List(ctx, f)
		}
	}
	if eng.getFn == nil && instRepo != nil {
		eng.getFn = func(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
			return instRepo.Get(ctx, id)
		}
	}
	deps := &adaptmcp.Deps{
		Instances: instRepo,
		Engine:    eng,
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterInstanceTools(srv)
	return srv
}

// --- Tests ---

func TestInstanceList(t *testing.T) {
	t.Run("happy path returns summaries", func(t *testing.T) {
		repo := &stubInstanceRepo{
			listFn: func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
				return []instance.InstanceSummary{
					{ID: uuid.New(), WorkflowName: "wf1", State: instance.StateRunning},
				}, nil
			},
		}
		srv := newInstanceServer(repo, &stubEngine{})
		result := callTool(t, srv, "instance_list", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "wf1")
	})

	t.Run("passes state filter", func(t *testing.T) {
		var capturedFilters instance.ListFilters
		repo := &stubInstanceRepo{
			listFn: func(_ context.Context, f instance.ListFilters) ([]instance.InstanceSummary, error) {
				capturedFilters = f
				return nil, nil
			},
		}
		srv := newInstanceServer(repo, &stubEngine{})
		callTool(t, srv, "instance_list", map[string]any{"state": "running"})

		require.NotNil(t, capturedFilters.State)
		assert.Equal(t, instance.StateRunning, *capturedFilters.State)
	})

	t.Run("passes workflowName filter", func(t *testing.T) {
		var capturedFilters instance.ListFilters
		repo := &stubInstanceRepo{
			listFn: func(_ context.Context, f instance.ListFilters) ([]instance.InstanceSummary, error) {
				capturedFilters = f
				return nil, nil
			},
		}
		srv := newInstanceServer(repo, &stubEngine{})
		callTool(t, srv, "instance_list", map[string]any{"workflowName": "my-wf"})

		require.NotNil(t, capturedFilters.WorkflowName)
		assert.Equal(t, "my-wf", *capturedFilters.WorkflowName)
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubInstanceRepo{
			listFn: func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
				return nil, errors.New("db error")
			},
		}
		srv := newInstanceServer(repo, &stubEngine{})
		result := callTool(t, srv, "instance_list", nil)

		assert.True(t, result.IsError)
	})

	t.Run("delegates to the scoped Engine.ListInstances, not the repo", func(t *testing.T) {
		// The repo's List must NOT be consulted; scoping lives in the engine
		// service. Make the repo error so a regression to the repo path is loud.
		repo := &stubInstanceRepo{
			listFn: func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
				return nil, errors.New("repo path must not be used")
			},
		}
		eng := &stubEngine{
			listFn: func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
				return []instance.InstanceSummary{{ID: uuid.New(), WorkflowName: "scoped-wf"}}, nil
			},
		}
		srv := newInstanceServer(repo, eng)
		result := callTool(t, srv, "instance_list", nil)

		assert.False(t, result.IsError)
		assert.Contains(t, extractText(t, result), "scoped-wf")
	})

	t.Run("nil repo result produces JSON array not null", func(t *testing.T) {
		// Edge case: repo returns nil slice (zero instances); handler must normalise
		// to [] so LLM clients can iterate without a nil-dereference on JSON null.
		repo := &stubInstanceRepo{
			listFn: func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
				return nil, nil
			},
		}
		srv := newInstanceServer(repo, &stubEngine{})
		result := callTool(t, srv, "instance_list", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Equal(t, "[]", text)
	})

	t.Run("caller identity context is forwarded to engine", func(t *testing.T) {
		// Verifies the MCP tool passes its call context (which carries the auth
		// identity injected by callTool) through to Engine.ListInstances, so the
		// engine's ownerConstraint resolves the authenticated caller rather than
		// falling back to the default user.
		eng := &stubEngine{}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)

		result := callTool(t, srv, "instance_list", nil)
		assert.False(t, result.IsError, extractText(t, result))

		require.NotNil(t, eng.capturedListCtx, "engine ListInstances must receive the MCP call context")
		id, ok := identity.FromContext(eng.capturedListCtx)
		require.True(t, ok, "engine context must carry the caller identity")
		assert.Equal(t, mcpTestUserID, id.UserID, "forwarded identity must be the authenticated caller")
	})
}

func TestInstanceGet(t *testing.T) {
	t.Run("happy path returns instance", func(t *testing.T) {
		expectedID := uuid.New()
		repo := &stubInstanceRepo{
			getFn: func(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
				return &instance.Instance{ID: id, WorkflowName: "wf1"}, nil
			},
		}
		srv := newInstanceServer(repo, &stubEngine{})
		result := callTool(t, srv, "instance_get", map[string]any{"id": expectedID.String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "wf1")
	})

	t.Run("invalid UUID returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_get", map[string]any{"id": "not-uuid"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("missing id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_get", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubInstanceRepo{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return nil, errors.New("not found")
			},
		}
		srv := newInstanceServer(repo, &stubEngine{})
		result := callTool(t, srv, "instance_get", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})

	t.Run("nil instance without error returns not found", func(t *testing.T) {
		// stubInstanceRepo.Get returns (nil, nil) by default — handler must guard against
		// nil inst before marshaling, otherwise it returns confusing JSON null.
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_get", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})
}

func TestInstanceTrigger(t *testing.T) {
	t.Run("happy path triggers workflow", func(t *testing.T) {
		expectedID := uuid.New()
		eng := &stubEngine{
			triggerFn: func(wfName string, _ map[string]any, _ string) (uuid.UUID, bool, error) {
				assert.Equal(t, "my-wf", wfName)
				return expectedID, false, nil
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_trigger", map[string]any{"workflowName": "my-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, expectedID.String())
	})

	t.Run("parses variables and priority", func(t *testing.T) {
		var capturedVars map[string]any
		var capturedPriority string
		eng := &stubEngine{
			triggerFn: func(_ string, vars map[string]any, priority string) (uuid.UUID, bool, error) {
				capturedVars = vars
				capturedPriority = priority
				return uuid.New(), true, nil
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_trigger", map[string]any{
			"workflowName": "wf",
			"variables":    `{"key":"value"}`,
			"priority":     "high",
		})

		assert.False(t, result.IsError)
		assert.Equal(t, "value", capturedVars["key"])
		assert.Equal(t, "high", capturedPriority)
	})

	t.Run("missing workflowName returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_trigger", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "workflowName is required")
	})

	t.Run("invalid variables JSON returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_trigger", map[string]any{
			"workflowName": "wf",
			"variables":    "not-json",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid variables JSON")
	})

	t.Run("engine error returned as tool error", func(t *testing.T) {
		eng := &stubEngine{
			triggerFn: func(_ string, _ map[string]any, _ string) (uuid.UUID, bool, error) {
				return uuid.Nil, false, errors.New("workflow not found")
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_trigger", map[string]any{"workflowName": "missing"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "workflow not found")
	})

	t.Run("invalid priority value returns error", func(t *testing.T) {
		// Edge case: priority string not in {high, normal, low} must be rejected
		// before reaching the engine; otherwise the invalid value is persisted to DB.
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_trigger", map[string]any{
			"workflowName": "wf",
			"priority":     "urgent",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid priority")
	})

	t.Run("caller identity context is forwarded to engine", func(t *testing.T) {
		// Verifies the MCP tool passes its call context (which carries the auth
		// identity injected by callTool) through to the engine, so triggerOwner
		// resolves the authenticated user rather than falling back to the default user.
		eng := &stubEngine{}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)

		result := callTool(t, srv, "instance_trigger", map[string]any{"workflowName": "wf"})
		assert.False(t, result.IsError, extractText(t, result))

		require.NotNil(t, eng.capturedTriggerCtx, "engine Trigger must receive the MCP call context")
		id, ok := identity.FromContext(eng.capturedTriggerCtx)
		require.True(t, ok, "engine context must carry the caller identity")
		assert.Equal(t, mcpTestUserID, id.UserID, "forwarded identity must be the authenticated caller")
	})
}

func TestInstancePause(t *testing.T) {
	t.Run("happy path pauses instance", func(t *testing.T) {
		expectedID := uuid.New()
		var capturedID uuid.UUID
		eng := &stubEngine{
			pauseFn: func(id uuid.UUID) error {
				capturedID = id
				return nil
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_pause", map[string]any{"id": expectedID.String()})

		assert.False(t, result.IsError)
		assert.Equal(t, expectedID, capturedID)
	})

	t.Run("engine error returned as tool error", func(t *testing.T) {
		eng := &stubEngine{
			pauseFn: func(_ uuid.UUID) error {
				return errors.New("not running")
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_pause", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not running")
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_pause", map[string]any{"id": "not-uuid"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("missing id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_pause", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("non-owner gets the not-found tool error (gate enforced in the service)", func(t *testing.T) {
		// The engine's authorizeIntervention returns ErrNotFound for a non-owner;
		// the MCP tool surfaces it identically to a missing instance (existence not leaked).
		eng := &stubEngine{
			pauseFn: func(id uuid.UUID) error {
				return fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_pause", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})
}

func TestInstanceResume(t *testing.T) {
	t.Run("happy path resumes instance", func(t *testing.T) {
		eng := &stubEngine{
			resumeFn: func(_ uuid.UUID) error { return nil },
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_resume", map[string]any{"id": uuid.New().String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "resumed")
	})

	t.Run("engine error returned as tool error", func(t *testing.T) {
		eng := &stubEngine{
			resumeFn: func(_ uuid.UUID) error {
				return errors.New("not paused")
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_resume", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not paused")
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_resume", map[string]any{"id": "not-uuid"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("missing id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_resume", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("non-owner gets the not-found tool error (gate enforced in the service)", func(t *testing.T) {
		// The engine's authorizeIntervention returns ErrNotFound for a non-owner;
		// the MCP tool surfaces it identically to a missing instance (existence not leaked).
		eng := &stubEngine{
			resumeFn: func(id uuid.UUID) error {
				return fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_resume", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})
}

func TestInstanceCancel(t *testing.T) {
	t.Run("happy path cancels instance", func(t *testing.T) {
		eng := &stubEngine{
			cancelFn: func(_ uuid.UUID) error { return nil },
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_cancel", map[string]any{"id": uuid.New().String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "cancelled")
	})

	t.Run("engine error returned as tool error", func(t *testing.T) {
		eng := &stubEngine{
			cancelFn: func(_ uuid.UUID) error {
				return errors.New("already terminal")
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_cancel", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "already terminal")
	})

	t.Run("non-owner gets the not-found tool error (gate enforced in the service)", func(t *testing.T) {
		// The engine's authorizeIntervention returns ErrNotFound for a non-owner;
		// the MCP tool surfaces it identically to a missing instance.
		eng := &stubEngine{
			cancelFn: func(id uuid.UUID) error {
				return fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_cancel", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_cancel", map[string]any{"id": "not-uuid"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("missing id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_cancel", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})
}

func TestInstanceRetry(t *testing.T) {
	t.Run("happy path retries with variable edits", func(t *testing.T) {
		var capturedVars map[string]any
		eng := &stubEngine{
			retryFn: func(_ uuid.UUID, vars map[string]any) error {
				capturedVars = vars
				return nil
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_retry", map[string]any{
			"id":        uuid.New().String(),
			"variables": `{"fix":"value"}`,
		})

		assert.False(t, result.IsError)
		assert.Equal(t, "value", capturedVars["fix"])
	})

	t.Run("retries without variables", func(t *testing.T) {
		eng := &stubEngine{
			retryFn: func(_ uuid.UUID, vars map[string]any) error {
				assert.Nil(t, vars)
				return nil
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_retry", map[string]any{"id": uuid.New().String()})

		assert.False(t, result.IsError)
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_retry", map[string]any{"id": "not-uuid"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("invalid variables JSON returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_retry", map[string]any{
			"id":        uuid.New().String(),
			"variables": "not-json",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid variables JSON")
	})

	t.Run("engine error returned as tool error", func(t *testing.T) {
		eng := &stubEngine{
			retryFn: func(_ uuid.UUID, _ map[string]any) error {
				return errors.New("not failed")
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_retry", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not failed")
	})
}

func TestInstanceRerun(t *testing.T) {
	t.Run("happy path reruns and returns new ID", func(t *testing.T) {
		newID := uuid.New()
		eng := &stubEngine{
			rerunFn: func(_ uuid.UUID) (uuid.UUID, error) {
				return newID, nil
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_rerun", map[string]any{"id": uuid.New().String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, newID.String())
	})

	t.Run("engine error returned as tool error", func(t *testing.T) {
		eng := &stubEngine{
			rerunFn: func(_ uuid.UUID) (uuid.UUID, error) {
				return uuid.Nil, errors.New("cannot rerun")
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_rerun", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "cannot rerun")
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_rerun", map[string]any{"id": "not-uuid"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("missing id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_rerun", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})
}

func TestInstanceSetPriority(t *testing.T) {
	t.Run("happy path sets priority", func(t *testing.T) {
		var capturedPriority string
		eng := &stubEngine{
			setPriorityFn: func(_ uuid.UUID, p string) error {
				capturedPriority = p
				return nil
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_set_priority", map[string]any{
			"id":       uuid.New().String(),
			"priority": "high",
		})

		assert.False(t, result.IsError)
		assert.Equal(t, "high", capturedPriority)
	})

	t.Run("missing id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_set_priority", map[string]any{
			"priority": "high",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("missing priority returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_set_priority", map[string]any{
			"id": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "priority is required")
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_set_priority", map[string]any{
			"id":       "not-uuid",
			"priority": "high",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid id")
	})

	t.Run("engine error returned as tool error", func(t *testing.T) {
		eng := &stubEngine{
			setPriorityFn: func(_ uuid.UUID, _ string) error {
				return errors.New("instance not found")
			},
		}
		srv := newInstanceServer(&stubInstanceRepo{}, eng)
		result := callTool(t, srv, "instance_set_priority", map[string]any{
			"id":       uuid.New().String(),
			"priority": "low",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "instance not found")
	})

	t.Run("invalid priority value returns error", func(t *testing.T) {
		// Edge case: priority not in {high, normal, low} must be rejected at the
		// MCP layer before reaching the engine; otherwise invalid string is persisted.
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_set_priority", map[string]any{
			"id":       uuid.New().String(),
			"priority": "critical",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid priority")
	})
}

// ---------------------------------------------------------------------------
// FA #19: Instance adapter surface — MCP department trigger field
// ---------------------------------------------------------------------------

// Success: the optional department field on instance_trigger is forwarded to
// TriggerWithDepartment — the stubEngine captures it and the test asserts it.
func TestInstanceTrigger_DepartmentForwarded(t *testing.T) {
	eng := &stubEngine{}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)
	result := callTool(t, srv, "instance_trigger", map[string]any{
		"workflowName": "wf",
		"department":   "engineering",
	})

	assert.False(t, result.IsError)
	assert.Equal(t, "engineering", eng.capturedDepartment,
		"department field must be threaded through to TriggerWithDepartment")
}

// Success: instance_get response includes department and visibility when the
// instance carries those fields. This ensures the MCP layer surfaces both
// columns added in migration 012.
func TestInstanceGet_DepartmentAndVisibilityInResponse(t *testing.T) {
	id := uuid.New()
	repo := &stubInstanceRepo{
		getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
			return &instance.Instance{
				ID:           id,
				WorkflowName: "wf",
				Department:   "engineering",
				Visibility:   instance.VisibilityDepartment,
			}, nil
		},
	}
	srv := newInstanceServer(repo, &stubEngine{})
	result := callTool(t, srv, "instance_get", map[string]any{"id": id.String()})

	assert.False(t, result.IsError)
	text := extractText(t, result)
	assert.Contains(t, text, "engineering",
		"department must appear in the instance_get response")
	assert.Contains(t, text, "department",
		"visibility value 'department' must appear in the instance_get response")
}

// ---------------------------------------------------------------------------
// Phase 1 / Task 6: MCP instance_list returns enriched fields
// ---------------------------------------------------------------------------

// TestInstanceList_ReturnsEnrichedFields verifies the MCP instance_list tool
// flows through Engine.ListEnriched so MCP clients receive the same enriched
// fields (run name, submitter, friendly step, model, backend, retry count) the
// HTTP path returns.
func TestInstanceList_ReturnsEnrichedFields(t *testing.T) {
	id := uuid.New()
	eng := &stubEngine{
		listEnrichedFn: func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
			return []instance.EnrichedInstanceSummary{{
				InstanceSummary: instance.InstanceSummary{ID: id, WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: time.Now(), UpdatedAt: time.Now()},
				RunName:         "MyTitle — wf",
				Submitter:       instance.Submitter{Name: "alice"},
				FriendlyStep:    "Generate",
				Model:           "gpt-4o",
				Backend:         "claude-code",
				RetryCount:      2,
				ChildCount:      3,
			}}, nil
		},
	}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)

	res := callTool(t, srv, "instance_list", nil)
	require.False(t, res.IsError, "tool must not error")
	body := extractText(t, res)
	assert.Contains(t, body, "MyTitle — wf", "run name reaches the MCP result")
	assert.Contains(t, body, "Generate", "friendly step reaches the MCP result")
	assert.Contains(t, body, "gpt-4o", "model reaches the MCP result")
	assert.Contains(t, body, "claude-code", "backend reaches the MCP result")
	assert.Contains(t, body, "alice", "submitter name reaches the MCP result")
	assert.Contains(t, body, "2", "retry count reaches the MCP result")

	// EnrichedInstanceSummary.ChildCount carries no JSON tag — the same
	// untagged convention already used by its siblings RetryCount/QueuePosition/
	// FailedNodeID — so MCP marshals it under its bare Go field name
	// "ChildCount" rather than REST's DTO-mapped "childCount". Decode (not
	// substring-match) so a tag added only on one side, or the count silently
	// dropping to 0, fails this test instead of slipping through.
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, float64(3), rows[0]["ChildCount"],
		"ChildCount must reach the MCP result under its Go field name")
}

// ---------------------------------------------------------------------------
// FA #30: ErrForbidden on intervention paths (404-vs-403 MCP adapter mapping)
// ---------------------------------------------------------------------------

// TestInstancePause_ErrForbidden_IsError verifies that when the engine returns
// ErrForbidden (department peer — canView=true, canIntervene=false), the MCP
// tool surfaces it as an error result. The MCP protocol has no HTTP status
// analog, but the tool must not silently succeed and must include the error
// message so the caller can distinguish it from ErrNotFound.
func TestInstancePause_ErrForbidden_IsError(t *testing.T) {
	eng := &stubEngine{
		pauseFn: func(id uuid.UUID) error {
			return fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
		},
	}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)
	result := callTool(t, srv, "instance_pause", map[string]any{"id": uuid.New().String()})

	assert.True(t, result.IsError,
		"ErrForbidden must produce an error result — a dept peer cannot pause the run")
	// The error message must contain distinguishing content from ErrForbidden,
	// not silently succeed or return a generic "not found".
	text := extractText(t, result)
	assert.NotContains(t, text, "not found",
		"ErrForbidden must not be presented as ErrNotFound — existence is NOT hidden from a dept peer")
}

// TestInstanceCancel_ErrForbidden_IsError mirrors the pause case for Cancel.
func TestInstanceCancel_ErrForbidden_IsError(t *testing.T) {
	eng := &stubEngine{
		cancelFn: func(id uuid.UUID) error {
			return fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
		},
	}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)
	result := callTool(t, srv, "instance_cancel", map[string]any{"id": uuid.New().String()})

	assert.True(t, result.IsError,
		"ErrForbidden must produce an error result on Cancel — dept peer cannot intervene")
	text := extractText(t, result)
	assert.NotContains(t, text, "not found",
		"ErrForbidden must not be collapsed to ErrNotFound at the MCP adapter")
}
