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

// NewRouter creates the chi router with all API routes wired up.
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
	webhookHandler ...*WebhookHandler,
) chi.Router {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)

	wfHandler := NewWorkflowHandler(workflows, svc, fileReader)
	instHandler := NewInstanceHandler(instances, nodeExecs, snapshots, svc)
	streamHandler := NewStreamHandler(instances, eventBus)
	sysStreamHandler := NewSystemStreamHandler(eventBus)
	sysHandler := &SystemHandler{config: cfg, queueManager: queueManager, queueRepo: queueRepo}

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
		})

		// Webhook endpoints
		if len(webhookHandler) > 0 && webhookHandler[0] != nil {
			r.Post("/webhooks/wait/{token}", webhookHandler[0].ReceiveWebhook)
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
		})
	})

	return r
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
	Connected bool `json:"connected"`
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
			Connected: h.config.Database.URL != "",
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
