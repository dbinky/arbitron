package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
)

// EngineService combines both WorkflowService and InstanceService interfaces.
// The concrete engine.Engine satisfies both.
type EngineService interface {
	WorkflowService
	InstanceService
}

// FileReader provides access to workflow-associated files on disk.
type FileReader interface {
	ReadFile(workflowName string, relativePath string) ([]byte, error)
	ResolvePath(workflowName string, relativePath string) (string, error)
	FileExists(workflowName string, relativePath string) bool
}

// The following type aliases are used as parameter types for NewRouter. They
// allow callers within this package to reference the domain ports by short
// names without each handler file importing the domain packages directly.

// WorkflowRepo is a type alias for the domain workflow repository port.
type WorkflowRepo = workflow.WorkflowRepository

// InstanceRepo is a type alias for the domain instance repository port.
type InstanceRepo = instance.InstanceRepository

// NodeExecRepo is a type alias for the domain node execution repository port.
type NodeExecRepo = instance.NodeExecutionRepository

// SnapshotRepo is a type alias for the domain variable snapshot repository port.
type SnapshotRepo = instance.VariableSnapshotRepository

// EventBusPort is a type alias for the domain event bus port.
type EventBusPort = engine.EventBus

// QueueManagerPort defines the queue manager operations used by the API layer.
type QueueManagerPort interface {
	UpdateConfig(globalCap, perUserCap int)
	Evaluate(ctx context.Context) error
}

// QueueRepoPort is a type alias for the domain queue repository port.
type QueueRepoPort = instance.QueueRepository

// NotificationRepoPort is a type alias for the domain notification repository port.
type NotificationRepoPort = notification.Repository

// ConversationHandlerPort is a type alias for the domain ConversationHandler port.
type ConversationHandlerPort = engine.ConversationHandler

// RouterDeps bundles all dependencies required by NewRouter, replacing
// the previous 14-parameter function signature.
type RouterDeps struct {
	Service             EngineService
	Workflows           WorkflowRepo
	Instances           InstanceRepo
	NodeExecs           NodeExecRepo
	Snapshots           SnapshotRepo
	FileReader          FileReader
	EventBus            EventBusPort
	Config              *config.Config
	QueueManager        QueueManagerPort
	QueueRepo           QueueRepoPort
	DashboardHandler    http.Handler
	NotifHandler        *NotificationHandler
	ConversationHandler ConversationHandlerPort
	WebhookHandler      *WebhookHandler
}

// NewRouter creates the chi router with all API routes wired up.
// If DashboardHandler is non-nil, unmatched routes fall through to the
// embedded SPA instead of returning chi's default 404.
// ConversationHandler may be nil if interactive messaging is not needed.
func NewRouter(
	svc EngineService,
	workflows WorkflowRepo,
	instances InstanceRepo,
	nodeExecs NodeExecRepo,
	snapshots SnapshotRepo,
	fileReader FileReader,
	eventBus EventBusPort,
	cfg *config.Config,
	queueManager QueueManagerPort,
	queueRepo QueueRepoPort,
	dashboardHandler http.Handler,
	notifHandler *NotificationHandler,
	conversationHandler ConversationHandlerPort,
	webhookHandler ...*WebhookHandler,
) chi.Router {
	var wh *WebhookHandler
	if len(webhookHandler) > 0 {
		wh = webhookHandler[0]
	}
	return NewRouterFromDeps(RouterDeps{
		Service:             svc,
		Workflows:           workflows,
		Instances:           instances,
		NodeExecs:           nodeExecs,
		Snapshots:           snapshots,
		FileReader:          fileReader,
		EventBus:            eventBus,
		Config:              cfg,
		QueueManager:        queueManager,
		QueueRepo:           queueRepo,
		DashboardHandler:    dashboardHandler,
		NotifHandler:        notifHandler,
		ConversationHandler: conversationHandler,
		WebhookHandler:      wh,
	})
}

// NewRouterFromDeps creates the chi router from a RouterDeps struct.
func NewRouterFromDeps(d RouterDeps) chi.Router {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(securityHeaders)
	// TODO(phase-6): Add authentication middleware here when multi-user support
	// lands. Currently all endpoints (REST and WebSocket) are unauthenticated.
	// The interactive session endpoints (WebSocket messaging and POST .../interact/end)
	// are especially sensitive — without auth, any client can send messages to or
	// terminate any instance's interactive session.

	wfHandler := NewWorkflowHandler(d.Workflows, d.Service, d.FileReader, d.Instances)
	instHandler := NewInstanceHandler(d.Instances, d.NodeExecs, d.Snapshots, d.Service)
	streamHandler := NewStreamHandler(d.Instances, d.NodeExecs, d.EventBus, d.ConversationHandler)
	sysStreamHandler := NewSystemStreamHandler(d.EventBus)
	sysHandler := &SystemHandler{config: d.Config, queueManager: d.QueueManager, queueRepo: d.QueueRepo}

	var interactiveHandler *InteractiveHandler
	if d.ConversationHandler != nil {
		interactiveHandler = NewInteractiveHandler(d.ConversationHandler, d.Instances, d.NodeExecs)
	}

	r.Route("/api/v1", func(r chi.Router) {
		// System-wide WebSocket stream
		r.Get("/ws", sysStreamHandler.HandleWebSocket)

		// System endpoints
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})

		r.Get("/system/config", sysHandler.GetConfig)
		r.Put("/system/config/queue", sysHandler.UpdateQueueConfig)

		// Workflow endpoints
		r.Route("/workflows", func(r chi.Router) {
			r.Get("/", wfHandler.List)
			r.Get("/{name}", wfHandler.Get)
			r.Post("/{name}/validate", wfHandler.Validate)
			r.Post("/{name}/trigger", wfHandler.Trigger)
			r.Put("/{name}/layout", wfHandler.SaveLayout)
			r.Get("/{name}/stats", wfHandler.Stats)
		})

		// Notification endpoints
		if d.NotifHandler != nil {
			r.Route("/notifications", func(r chi.Router) {
				r.Get("/", d.NotifHandler.List)
				r.Get("/unread-count", d.NotifHandler.UnreadCount)
				r.Post("/mark-read", d.NotifHandler.MarkRead)
				r.Post("/{id}/dismiss", d.NotifHandler.Dismiss)
			})
		}

		// Webhook endpoints
		if d.WebhookHandler != nil {
			r.Post("/webhooks/wait/{token}", d.WebhookHandler.ReceiveWebhook)
		}

		// Instance endpoints
		r.Route("/instances", func(r chi.Router) {
			r.Get("/", instHandler.List)
			r.Get("/{id}", instHandler.Get)
			r.Post("/{id}/pause", instHandler.Pause)
			r.Post("/{id}/resume", instHandler.Resume)
			r.Post("/{id}/cancel", instHandler.Cancel)
			r.Post("/{id}/retry", instHandler.Retry)
			r.Post("/{id}/rerun", instHandler.Rerun)
			r.Post("/{id}/priority", instHandler.SetPriority)
			r.Get("/{id}/trace", instHandler.GetTrace)
			r.Get("/{id}/trace/{nodeId}", instHandler.GetNodeTrace)
			r.Get("/{id}/variables", instHandler.GetVariables)
			r.Get("/{id}/variables/history", instHandler.GetVariableHistory)
			r.Get("/{id}/ws", streamHandler.HandleWebSocket)
			if interactiveHandler != nil {
				r.Post("/{id}/interact/end", interactiveHandler.EndSession)
			}
		})
	})

	// Serve the embedded dashboard SPA for any route not matched by the API.
	if d.DashboardHandler != nil {
		r.NotFound(d.DashboardHandler.ServeHTTP)
	}

	return r
}

// securityHeaders adds standard security headers to every HTTP response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// SystemHandler handles system configuration endpoints.
type SystemHandler struct {
	mu           sync.Mutex
	config       *config.Config
	queueManager QueueManagerPort
	queueRepo    QueueRepoPort
}

// systemConfigResponse is the JSON representation of system configuration
// with sensitive fields redacted.
type systemConfigResponse struct {
	Server    serverConfigResponse    `json:"server"`
	Database  databaseConfigResponse  `json:"database"`
	Workspace workspaceConfigResponse `json:"workspace"`
	LLM       llmConfigResponse       `json:"llm"`
	Logging   loggingConfigResponse   `json:"logging"`
	Tracing   tracingConfigResponse   `json:"tracing"`
	Queue     queueConfigResponse     `json:"queue"`
}

type serverConfigResponse struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type databaseConfigResponse struct {
	Configured bool `json:"configured"`
}

type workspaceConfigResponse struct {
	Path string `json:"path"`
}

type llmConfigResponse struct {
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
}

type loggingConfigResponse struct {
	Level string `json:"level"`
	File  string `json:"file"`
}

type tracingConfigResponse struct {
	Enabled   bool   `json:"enabled"`
	ExportDir string `json:"exportDir"`
}

type queueConfigResponse struct {
	GlobalCap    int `json:"globalCap"`
	PerUserCap   int `json:"perUserCap"`
	RunningCount int `json:"runningCount"`
	QueuedCount  int `json:"queuedCount"`
	WaitingCount int `json:"waitingCount"`
}

// GetConfig handles GET /system/config.
func (h *SystemHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	var running, queued, waiting int
	if h.queueRepo != nil {
		running, _ = h.queueRepo.CountRunning(r.Context())
		queued, _ = h.queueRepo.CountQueued(r.Context())
		waiting, _ = h.queueRepo.CountWaiting(r.Context())
	}

	h.mu.Lock()
	globalCap := h.config.Queue.GlobalCap
	perUserCap := h.config.Queue.PerUserCap
	h.mu.Unlock()

	resp := systemConfigResponse{
		Server: serverConfigResponse{
			Host: h.config.Server.Host,
			Port: h.config.Server.Port,
		},
		Database: databaseConfigResponse{
			Configured: h.config.Database.Path != "",
		},
		Workspace: workspaceConfigResponse{
			Path: h.config.Workspace.Path,
		},
		LLM: llmConfigResponse{
			Provider: h.config.LLM.Provider,
			APIKey:   "**REDACTED**",
		},
		Logging: loggingConfigResponse{
			Level: h.config.Logging.Level,
			File:  h.config.Logging.File,
		},
		Tracing: tracingConfigResponse{
			Enabled:   h.config.Tracing.Enabled,
			ExportDir: h.config.Tracing.ExportDir,
		},
		Queue: queueConfigResponse{
			GlobalCap:    globalCap,
			PerUserCap:   perUserCap,
			RunningCount: running,
			QueuedCount:  queued,
			WaitingCount: waiting,
		},
	}
	WriteJSON(w, http.StatusOK, resp)
}

// updateQueueConfigRequest is the JSON body for updating queue config.
type updateQueueConfigRequest struct {
	GlobalCap  *int `json:"globalCap"`
	PerUserCap *int `json:"perUserCap"`
}

// UpdateQueueConfig handles PUT /system/config/queue.
func (h *SystemHandler) UpdateQueueConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req updateQueueConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	if req.GlobalCap != nil && *req.GlobalCap < 0 {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "globalCap must be >= 0 (0 = unlimited)")
		return
	}
	if req.PerUserCap != nil && *req.PerUserCap < 0 {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "perUserCap must be >= 0 (0 = unlimited)")
		return
	}

	h.mu.Lock()
	oldGlobalCap := h.config.Queue.GlobalCap
	oldPerUserCap := h.config.Queue.PerUserCap

	newGlobalCap := oldGlobalCap
	newPerUserCap := oldPerUserCap
	if req.GlobalCap != nil {
		newGlobalCap = *req.GlobalCap
	}
	if req.PerUserCap != nil {
		newPerUserCap = *req.PerUserCap
	}
	h.config.Queue.GlobalCap = newGlobalCap
	h.config.Queue.PerUserCap = newPerUserCap
	h.mu.Unlock()

	h.queueManager.UpdateConfig(newGlobalCap, newPerUserCap)

	// If cap was raised, trigger queue evaluation.
	capRaised := (newGlobalCap == 0 && oldGlobalCap > 0) ||
		(newGlobalCap > oldGlobalCap) ||
		(newPerUserCap == 0 && oldPerUserCap > 0) ||
		(newPerUserCap > oldPerUserCap)
	if capRaised {
		go func() {
			if err := h.queueManager.Evaluate(context.Background()); err != nil {
				slog.Error("queue config update: evaluation failed", "error", err)
			}
		}()
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"globalCap":  newGlobalCap,
		"perUserCap": newPerUserCap,
	})
}
