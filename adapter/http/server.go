package http

import (
	"net/http"

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

// NewRouter creates the chi router with all API routes wired up.
func NewRouter(
	svc EngineService,
	workflows workflow.WorkflowRepository,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	fileReader engine.FileReader,
	eventBus engine.EventBus,
	cfg *config.Config,
) chi.Router {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)

	wfHandler := NewWorkflowHandler(workflows, svc, fileReader)
	instHandler := NewInstanceHandler(instances, nodeExecs, snapshots, svc)
	streamHandler := NewStreamHandler(instances, eventBus)

	r.Route("/api/v1", func(r chi.Router) {
		// System endpoints
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})

		r.Get("/system/config", systemConfigHandler(cfg))

		// Workflow endpoints
		r.Route("/workflows", func(r chi.Router) {
			r.Get("/", wfHandler.List)
			r.Get("/{name}", wfHandler.Get)
			r.Post("/{name}/validate", wfHandler.Validate)
			r.Post("/{name}/trigger", wfHandler.Trigger)
		})

		// Instance endpoints
		r.Route("/instances", func(r chi.Router) {
			r.Get("/", instHandler.List)
			r.Get("/{id}", instHandler.Get)
			r.Post("/{id}/pause", instHandler.Pause)
			r.Post("/{id}/resume", instHandler.Resume)
			r.Post("/{id}/cancel", instHandler.Cancel)
			r.Post("/{id}/retry", instHandler.Retry)
			r.Post("/{id}/rerun", instHandler.Rerun)
			r.Get("/{id}/trace", instHandler.GetTrace)
			r.Get("/{id}/trace/{nodeId}", instHandler.GetNodeTrace)
			r.Get("/{id}/variables", instHandler.GetVariables)
			r.Get("/{id}/variables/history", instHandler.GetVariableHistory)
			r.Get("/{id}/ws", streamHandler.HandleWebSocket)
		})
	})

	return r
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
}

type serverConfigResponse struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type databaseConfigResponse struct {
	URL string `json:"url"`
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

func systemConfigHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp := systemConfigResponse{
			Server: serverConfigResponse{
				Host: cfg.Server.Host,
				Port: cfg.Server.Port,
			},
			Database: databaseConfigResponse{
				URL: cfg.Database.URL,
			},
			Workspace: workspaceConfigResponse{
				Path: cfg.Workspace.Path,
			},
			LLM: llmConfigResponse{
				Provider: cfg.LLM.Provider,
				APIKey:   "**REDACTED**",
			},
			Logging: loggingConfigResponse{
				Level: cfg.Logging.Level,
				File:  cfg.Logging.File,
			},
			Tracing: tracingConfigResponse{
				Enabled:   cfg.Tracing.Enabled,
				ExportDir: cfg.Tracing.ExportDir,
			},
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}
