package mcp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
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
func (s *stubInstanceRepo) Create(_ context.Context, _ *instance.Instance) error   { return nil }
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
	triggerFn     func(wfName string, vars map[string]any, priority string) (uuid.UUID, bool, error)
	pauseFn       func(id uuid.UUID) error
	resumeFn      func(id uuid.UUID) error
	cancelFn      func(id uuid.UUID) error
	retryFn       func(id uuid.UUID, vars map[string]any) error
	rerunFn       func(id uuid.UUID) (uuid.UUID, error)
	setPriorityFn func(id uuid.UUID, priority string) error
}

func (s *stubEngine) Trigger(wfName string, vars map[string]any, priority string) (uuid.UUID, bool, error) {
	if s.triggerFn != nil {
		return s.triggerFn(wfName, vars, priority)
	}
	return uuid.New(), false, nil
}

func (s *stubEngine) Pause(id uuid.UUID) error {
	if s.pauseFn != nil {
		return s.pauseFn(id)
	}
	return nil
}

func (s *stubEngine) Resume(id uuid.UUID) error {
	if s.resumeFn != nil {
		return s.resumeFn(id)
	}
	return nil
}

func (s *stubEngine) Cancel(id uuid.UUID) error {
	if s.cancelFn != nil {
		return s.cancelFn(id)
	}
	return nil
}

func (s *stubEngine) Retry(id uuid.UUID, vars map[string]any) error {
	if s.retryFn != nil {
		return s.retryFn(id, vars)
	}
	return nil
}

func (s *stubEngine) Rerun(id uuid.UUID) (uuid.UUID, error) {
	if s.rerunFn != nil {
		return s.rerunFn(id)
	}
	return uuid.New(), nil
}

func (s *stubEngine) SetPriority(id uuid.UUID, priority string) error {
	if s.setPriorityFn != nil {
		return s.setPriorityFn(id, priority)
	}
	return nil
}

// --- Helpers ---

func newInstanceServer(instRepo *stubInstanceRepo, eng *stubEngine) *adaptmcp.Server {
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

	t.Run("missing priority returns error", func(t *testing.T) {
		srv := newInstanceServer(&stubInstanceRepo{}, &stubEngine{})
		result := callTool(t, srv, "instance_set_priority", map[string]any{
			"id": uuid.New().String(),
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "priority is required")
	})
}
