package mcp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
)

// --- System Mocks ---

type stubQueueRepo struct {
	countRunningFn func(ctx context.Context) (int, error)
	countQueuedFn  func(ctx context.Context) (int, error)
	countWaitingFn func(ctx context.Context) (int, error)
}

func (s *stubQueueRepo) CountRunning(ctx context.Context) (int, error) {
	if s.countRunningFn != nil {
		return s.countRunningFn(ctx)
	}
	return 0, nil
}

func (s *stubQueueRepo) CountRunningByOwner(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}

func (s *stubQueueRepo) CountQueued(ctx context.Context) (int, error) {
	if s.countQueuedFn != nil {
		return s.countQueuedFn(ctx)
	}
	return 0, nil
}

func (s *stubQueueRepo) CountWaiting(ctx context.Context) (int, error) {
	if s.countWaitingFn != nil {
		return s.countWaitingFn(ctx)
	}
	return 0, nil
}

func (s *stubQueueRepo) NextEligible(_ context.Context, _, _ int) (*instance.Instance, error) {
	return nil, nil
}

func (s *stubQueueRepo) SetQueued(_ context.Context, _ uuid.UUID, _ time.Time) error { return nil }

func (s *stubQueueRepo) ListWaitingWithTimer(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}

func (s *stubQueueRepo) ListWaitingWithWebhook(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}

type stubQueueManager struct {
	updateConfigFn func(globalCap, perUserCap int)
	evaluateFn     func(ctx context.Context) error
}

func (s *stubQueueManager) UpdateConfig(globalCap, perUserCap int) {
	if s.updateConfigFn != nil {
		s.updateConfigFn(globalCap, perUserCap)
	}
}

func (s *stubQueueManager) Evaluate(ctx context.Context) error {
	if s.evaluateFn != nil {
		return s.evaluateFn(ctx)
	}
	return nil
}

type stubNotificationRepo struct {
	listFn        func(ctx context.Context, filters notification.ListFilters) ([]notification.Notification, error)
	countUnreadFn func(ctx context.Context, ownerID *uuid.UUID) (int, error)
	markAllReadFn func(ctx context.Context, ownerID *uuid.UUID) error
	dismissFn     func(ctx context.Context, id uuid.UUID, ownerID *uuid.UUID) error
}

func (s *stubNotificationRepo) Create(_ context.Context, _ *notification.Notification) error {
	return nil
}

func (s *stubNotificationRepo) List(ctx context.Context, filters notification.ListFilters) ([]notification.Notification, error) {
	if s.listFn != nil {
		return s.listFn(ctx, filters)
	}
	return nil, nil
}

func (s *stubNotificationRepo) CountUnread(ctx context.Context, ownerID *uuid.UUID) (int, error) {
	if s.countUnreadFn != nil {
		return s.countUnreadFn(ctx, ownerID)
	}
	return 0, nil
}

func (s *stubNotificationRepo) MarkAllRead(ctx context.Context, ownerID *uuid.UUID) error {
	if s.markAllReadFn != nil {
		return s.markAllReadFn(ctx, ownerID)
	}
	return nil
}

func (s *stubNotificationRepo) Dismiss(ctx context.Context, id uuid.UUID, ownerID *uuid.UUID) error {
	if s.dismissFn != nil {
		return s.dismissFn(ctx, id, ownerID)
	}
	return nil
}

// We need a stubInstanceRepo with Stats for workflow_stats tool.
type stubInstanceRepoWithStats struct {
	stubInstanceRepo
	statsFn func(ctx context.Context, name string, from, to time.Time) (*instance.WorkflowStats, error)
}

func (s *stubInstanceRepoWithStats) Stats(ctx context.Context, name string, from, to time.Time) (*instance.WorkflowStats, error) {
	if s.statsFn != nil {
		return s.statsFn(ctx, name, from, to)
	}
	return nil, nil
}

// --- Helpers ---

func newSystemServer(
	cfg *config.Config,
	queueRepo *stubQueueRepo,
	queueMgr *stubQueueManager,
	notifRepo *stubNotificationRepo,
	instRepo instance.InstanceRepository,
) *adaptmcp.Server {
	deps := &adaptmcp.Deps{
		Config:        cfg,
		Settings:      config.NewSettingsStore(&cfg.Notifications.Teams),
		QueueRepo:     queueRepo,
		QueueManager:  queueMgr,
		Notifications: notifRepo,
		Instances:     instRepo,
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterSystemTools(srv)
	return srv
}

// --- Tests ---

func TestSystemConfig(t *testing.T) {
	t.Run("happy path returns config and queue counts", func(t *testing.T) {
		cfg := &config.Config{
			Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 2},
		}
		queueRepo := &stubQueueRepo{
			countRunningFn: func(_ context.Context) (int, error) { return 5, nil },
			countQueuedFn:  func(_ context.Context) (int, error) { return 3, nil },
			countWaitingFn: func(_ context.Context) (int, error) { return 1, nil },
		}
		srv := newSystemServer(cfg, queueRepo, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_config", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "running")
		assert.Contains(t, text, "5")
	})

	t.Run("CountRunning error returned as tool error", func(t *testing.T) {
		queueRepo := &stubQueueRepo{
			countRunningFn: func(_ context.Context) (int, error) { return 0, errors.New("db down") },
		}
		srv := newSystemServer(&config.Config{}, queueRepo, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_config", nil)

		assert.True(t, result.IsError)
	})

	t.Run("CountQueued error returned as tool error", func(t *testing.T) {
		queueRepo := &stubQueueRepo{
			countQueuedFn: func(_ context.Context) (int, error) { return 0, errors.New("db down") },
		}
		srv := newSystemServer(&config.Config{}, queueRepo, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_config", nil)

		assert.True(t, result.IsError)
	})

	t.Run("CountWaiting error returned as tool error", func(t *testing.T) {
		queueRepo := &stubQueueRepo{
			countWaitingFn: func(_ context.Context) (int, error) { return 0, errors.New("db down") },
		}
		srv := newSystemServer(&config.Config{}, queueRepo, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_config", nil)

		assert.True(t, result.IsError)
	})
}

func TestSystemUpdateQueueConfig(t *testing.T) {
	t.Run("happy path updates config and evaluates", func(t *testing.T) {
		var capturedGlobal, capturedPerUser int
		var evaluated bool
		mgr := &stubQueueManager{
			updateConfigFn: func(g, p int) {
				capturedGlobal = g
				capturedPerUser = p
			},
			evaluateFn: func(_ context.Context) error {
				evaluated = true
				return nil
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, mgr, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_update_queue_config", map[string]any{
			"globalCap":  "20",
			"perUserCap": "5",
		})

		assert.False(t, result.IsError)
		assert.Equal(t, 20, capturedGlobal)
		assert.Equal(t, 5, capturedPerUser)
		assert.True(t, evaluated)
	})

	t.Run("invalid globalCap returns error", func(t *testing.T) {
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_update_queue_config", map[string]any{
			"globalCap":  "abc",
			"perUserCap": "5",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid globalCap")
	})

	t.Run("invalid perUserCap returns error", func(t *testing.T) {
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_update_queue_config", map[string]any{
			"globalCap":  "10",
			"perUserCap": "xyz",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid perUserCap")
	})

	t.Run("evaluate error returned as tool error", func(t *testing.T) {
		mgr := &stubQueueManager{
			evaluateFn: func(_ context.Context) error {
				return errors.New("evaluation failed")
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, mgr, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "system_update_queue_config", map[string]any{
			"globalCap":  "10",
			"perUserCap": "2",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "evaluation failed")
	})
}

func TestNotificationList(t *testing.T) {
	t.Run("happy path returns notifications", func(t *testing.T) {
		notifRepo := &stubNotificationRepo{
			listFn: func(_ context.Context, _ notification.ListFilters) ([]notification.Notification, error) {
				return []notification.Notification{
					{ID: uuid.New(), WorkflowName: "wf1", Type: notification.TypeFailed},
				}, nil
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_list", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "wf1")
	})

	t.Run("passes dismissed filter", func(t *testing.T) {
		var capturedFilters notification.ListFilters
		notifRepo := &stubNotificationRepo{
			listFn: func(_ context.Context, f notification.ListFilters) ([]notification.Notification, error) {
				capturedFilters = f
				return nil, nil
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		callTool(t, srv, "notification_list", map[string]any{"dismissed": true})

		assert.NotNil(t, capturedFilters.Dismissed)
		assert.True(t, *capturedFilters.Dismissed)
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		notifRepo := &stubNotificationRepo{
			listFn: func(_ context.Context, _ notification.ListFilters) ([]notification.Notification, error) {
				return nil, errors.New("db error")
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_list", nil)

		assert.True(t, result.IsError)
	})

	t.Run("nil repo result produces JSON array not null", func(t *testing.T) {
		// Edge case: zero notifications; handler must normalise nil to [] so
		// LLM clients can iterate without hitting a null value.
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_list", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Equal(t, "[]", text)
	})
}

func TestNotificationUnreadCount(t *testing.T) {
	t.Run("happy path returns count", func(t *testing.T) {
		notifRepo := &stubNotificationRepo{
			countUnreadFn: func(_ context.Context, _ *uuid.UUID) (int, error) { return 7, nil },
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_unread_count", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "7")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		notifRepo := &stubNotificationRepo{
			countUnreadFn: func(_ context.Context, _ *uuid.UUID) (int, error) {
				return 0, errors.New("db error")
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_unread_count", nil)

		assert.True(t, result.IsError)
	})
}

func TestNotificationMarkRead(t *testing.T) {
	t.Run("happy path marks all read", func(t *testing.T) {
		var called bool
		notifRepo := &stubNotificationRepo{
			markAllReadFn: func(_ context.Context, _ *uuid.UUID) error {
				called = true
				return nil
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_mark_read", nil)

		assert.False(t, result.IsError)
		assert.True(t, called)
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		notifRepo := &stubNotificationRepo{
			markAllReadFn: func(_ context.Context, _ *uuid.UUID) error {
				return errors.New("db error")
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_mark_read", nil)

		assert.True(t, result.IsError)
	})
}

func TestNotificationDismiss(t *testing.T) {
	t.Run("missing id returns error", func(t *testing.T) {
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_dismiss", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "id is required")
	})

	t.Run("happy path dismisses notification", func(t *testing.T) {
		expectedID := uuid.New()
		var capturedID uuid.UUID
		notifRepo := &stubNotificationRepo{
			dismissFn: func(_ context.Context, id uuid.UUID, _ *uuid.UUID) error {
				capturedID = id
				return nil
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_dismiss", map[string]any{"id": expectedID.String()})

		assert.False(t, result.IsError)
		assert.Equal(t, expectedID, capturedID)
	})

	t.Run("invalid UUID returns error", func(t *testing.T) {
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_dismiss", map[string]any{"id": "bad"})

		assert.True(t, result.IsError)
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		notifRepo := &stubNotificationRepo{
			dismissFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) error {
				return errors.New("not found")
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, notifRepo, &stubInstanceRepo{})
		result := callTool(t, srv, "notification_dismiss", map[string]any{"id": uuid.New().String()})

		assert.True(t, result.IsError)
	})
}

func TestWorkflowStats(t *testing.T) {
	t.Run("happy path returns stats", func(t *testing.T) {
		instRepo := &stubInstanceRepoWithStats{
			statsFn: func(_ context.Context, name string, _, _ time.Time) (*instance.WorkflowStats, error) {
				return &instance.WorkflowStats{
					TotalRuns:       10,
					SuccessCount:    8,
					FailureCount:    2,
					AvgDurationSecs: 45.5,
				}, nil
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, instRepo)
		result := callTool(t, srv, "workflow_stats", map[string]any{"name": "my-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "10")
		assert.Contains(t, text, "45.5")
	})

	t.Run("with from and to time", func(t *testing.T) {
		var capturedFrom, capturedTo time.Time
		instRepo := &stubInstanceRepoWithStats{
			statsFn: func(_ context.Context, _ string, from, to time.Time) (*instance.WorkflowStats, error) {
				capturedFrom = from
				capturedTo = to
				return &instance.WorkflowStats{}, nil
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, instRepo)

		fromStr := "2025-01-01T00:00:00Z"
		toStr := "2025-06-01T00:00:00Z"
		result := callTool(t, srv, "workflow_stats", map[string]any{
			"name": "wf",
			"from": fromStr,
			"to":   toStr,
		})

		assert.False(t, result.IsError)
		expectedFrom, _ := time.Parse(time.RFC3339, fromStr)
		expectedTo, _ := time.Parse(time.RFC3339, toStr)
		assert.Equal(t, expectedFrom, capturedFrom)
		assert.Equal(t, expectedTo, capturedTo)
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "workflow_stats", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "name is required")
	})

	t.Run("invalid from time returns error", func(t *testing.T) {
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "workflow_stats", map[string]any{
			"name": "wf",
			"from": "not-a-time",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid from time")
	})

	t.Run("invalid to time returns error", func(t *testing.T) {
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
		result := callTool(t, srv, "workflow_stats", map[string]any{
			"name": "wf",
			"to":   "not-a-time",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid to time")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		instRepo := &stubInstanceRepoWithStats{
			statsFn: func(_ context.Context, _ string, _, _ time.Time) (*instance.WorkflowStats, error) {
				return nil, errors.New("stats error")
			},
		}
		srv := newSystemServer(&config.Config{}, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, instRepo)
		result := callTool(t, srv, "workflow_stats", map[string]any{"name": "wf"})

		assert.True(t, result.IsError)
	})
}

// Edge-case: system_config never echoes the raw Teams webhook URL.
func TestSystemConfig_RedactsTeamsWebhook(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://secret.example.com/hook", Enabled: true},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
	result := callTool(t, srv, "system_config", nil)

	assert.False(t, result.IsError)
	text := extractText(t, result)
	assert.Contains(t, text, "**REDACTED**")
	assert.NotContains(t, text, "secret.example.com")
}

// Security: system_config must never echo the raw LLM API key. The positive
// allow-list projection masks it; the previous raw-marshal + deny-list path
// leaked any secret it did not explicitly know to redact. This case fails
// against that old handler.
func TestSystemConfig_RedactsLLMAPIKey(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{Provider: "anthropic", APIKey: "sk-secret"},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
	result := callTool(t, srv, "system_config", nil)

	assert.False(t, result.IsError)
	text := extractText(t, result)
	assert.NotContains(t, text, "sk-secret")
	assert.Contains(t, text, "**REDACTED**")
}

// Success: system_update_teams_config writes URL + toggle to in-memory config.
func TestSystemUpdateTeamsConfig_Success(t *testing.T) {
	cfg := &config.Config{}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{
		"webhookUrl": "https://example.com/hook",
		"enabled":    true,
	})

	assert.False(t, result.IsError)
	assert.Equal(t, "https://example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.True(t, cfg.Notifications.Teams.Enabled)
	text := extractText(t, result)
	assert.Contains(t, text, "**REDACTED**")
	assert.NotContains(t, text, "example.com/hook")
}

// Edge-case: omitting webhookUrl preserves the stored URL (toggle-only update).
func TestSystemUpdateTeamsConfig_PartialPreservesURL(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://kept.example.com/hook", Enabled: true},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{"enabled": false})

	assert.False(t, result.IsError)
	assert.Equal(t, "https://kept.example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.False(t, cfg.Notifications.Teams.Enabled)
}

// Failure: a non-http(s) webhook URL is rejected, config unchanged.
func TestSystemUpdateTeamsConfig_InvalidURL(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://old.example.com/hook"},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{"webhookUrl": "ftp://nope"})

	assert.True(t, result.IsError)
	assert.Equal(t, "https://old.example.com/hook", cfg.Notifications.Teams.WebhookURL)
}

// Security: an SSRF target is rejected at the MCP boundary, config unchanged.
func TestSystemUpdateTeamsConfig_RejectsSSRFTargets(t *testing.T) {
	for _, raw := range []string{
		"https://169.254.169.254/latest/meta-data",
		"https://127.0.0.1/hook",
		"https://localhost/hook",
		"https://192.168.0.1/hook",
		"http://example.com/hook", // non-https also rejected now
	} {
		cfg := &config.Config{
			Notifications: config.NotificationsConfig{
				Teams: config.TeamsConfig{WebhookURL: "https://old.example.com/hook"},
			},
		}
		srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

		result := callTool(t, srv, "system_update_teams_config", map[string]any{"webhookUrl": raw})

		assert.True(t, result.IsError, "expected %q to be rejected", raw)
		assert.Equal(t, "https://old.example.com/hook", cfg.Notifications.Teams.WebhookURL, "config must be unchanged for %q", raw)
	}
}

// Error: a non-string webhookUrl is a type error; config is left unchanged.
func TestSystemUpdateTeamsConfig_WebhookURLNotString(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://old.example.com/hook"},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{"webhookUrl": 123})

	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "webhookUrl must be a string")
	assert.Equal(t, "https://old.example.com/hook", cfg.Notifications.Teams.WebhookURL)
}

// SECURITY: system_config must never echo any raw OIDC field value (clientSecret,
// clientId, issuerUrl, redirectUrl) even when auth is org mode and those fields
// are fully populated. Mirrors TestRouter_GetConfig_AuthSection_NeverLeaksOIDCSecret
// for the MCP surface. Both surfaces call config.Redact, but this test guards the
// MCP code path against a regression independently.
func TestSystemConfig_NeverLeaksOIDCSecret(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode: "org",
			OIDC: config.OIDCConfig{
				IssuerURL:    "https://idp.example.com/SHOULD-NOT-APPEAR",
				ClientID:     "client-SHOULD-NOT-APPEAR",
				ClientSecret: "secret-SHOULD-NOT-APPEAR",
				RedirectURL:  "https://app.example.com/SHOULD-NOT-APPEAR/auth/callback",
			},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
	result := callTool(t, srv, "system_config", nil)

	assert.False(t, result.IsError)
	text := extractText(t, result)
	assert.NotContains(t, text, "SHOULD-NOT-APPEAR",
		"system_config must never echo any raw OIDC field value on the MCP surface")
	// The auth section must be present with the correct fields.
	assert.Contains(t, text, `"mode"`)
	assert.Contains(t, text, `"oidcConfigured"`)
}

// Error: a non-bool enabled is a type error; config is left unchanged.
func TestSystemUpdateTeamsConfig_EnabledNotBool(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://kept.example.com/hook", Enabled: true},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{"enabled": "yes"})

	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "enabled must be a boolean")
	// Config unchanged: enabled stays true, URL preserved.
	assert.True(t, cfg.Notifications.Teams.Enabled)
	assert.Equal(t, "https://kept.example.com/hook", cfg.Notifications.Teams.WebhookURL)
}
