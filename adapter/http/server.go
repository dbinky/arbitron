package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// authRateLimitPerMinute caps requests per client IP to the unauthenticated
// /auth/* endpoints (login, callback, logout, device code/token). It throttles
// credential brute-force and auth-availability DoS while leaving normal
// interactive login well within budget.
const authRateLimitPerMinute = 20

// EngineService combines both Service and InstanceService interfaces.
// The concrete engine.Engine satisfies both.
type EngineService interface {
	Service
	InstanceService
}

// FileReader provides access to workflow-associated files on disk. The owner
// selects which user's private workspace to search first, with a shared
// fallback (uuid.Nil searches shared only).
type FileReader interface {
	ReadFile(owner uuid.UUID, workflowName string, relativePath string) ([]byte, error)
	ResolvePath(owner uuid.UUID, workflowName string, relativePath string) (string, error)
	FileExists(owner uuid.UUID, workflowName string, relativePath string) bool
}

// QueueManagerPort defines the queue manager operations used by the API layer.
type QueueManagerPort interface {
	UpdateConfig(globalCap, perUserCap int)
	Evaluate(ctx context.Context) error
}

// RouterDeps bundles all dependencies required by NewRouter, replacing
// the previous 14-parameter function signature.
type RouterDeps struct {
	Service   EngineService
	Workflows workflow.Repository
	// WorkflowSvc resolves storage scope/authorization from the request identity
	// for the workflow CRUD/list/load handlers. Optional: when nil, a default
	// service over Workflows (no publisher lookup) is constructed by the handler.
	WorkflowSvc *workflow.Service
	Instances   instance.InstanceRepository
	NodeExecs   instance.NodeExecutionRepository
	Snapshots   instance.VariableSnapshotRepository
	// Dismissals is the per-user instance dismissal repository. When non-nil
	// (together with DismissalUsers), the dismiss/restore/dismiss-all routes are
	// registered and GET /instances becomes per-user-aware.
	Dismissals instance.DismissalRepository
	// DismissalUsers sources the acting user for the dismissal seam
	// (GetDefaultUser today). Distinct from Users, which is the admin
	// role-management UserService.
	DismissalUsers instance.UserRepository
	FileReader     FileReader
	EventBus       engine.EventBus
	Config         *config.Config
	// Settings is the synchronized owner of runtime-mutable Teams settings. When
	// nil, NewRouterFromDeps seeds one from Config so the HTTP handler stays the
	// sole writer; production wiring shares one store across HTTP, MCP, and the
	// Teams notifier so all three observe the same locked state.
	Settings            *config.SettingsStore
	QueueManager        QueueManagerPort
	QueueRepo           instance.QueueRepository
	DashboardHandler    http.Handler
	NotifHandler        *NotificationHandler
	ConversationHandler engine.ConversationHandler
	WebhookHandler      *WebhookHandler
	// MCPHandler, when non-nil, is mounted at /mcp for the MCP Streamable
	// HTTP transport so LLM clients can call tools over HTTP.
	MCPHandler http.Handler
	// ProfileChecker resolves a node's model_profile alias to its capability
	// kind during workflow validation. May be nil to skip the model_profile
	// validation pass (the workflow validator treats a nil checker as a no-op).
	ProfileChecker workflow.ProfileChecker
	// ConnChecker validates that a profile's non-empty ConnectionRef names an
	// existing connection during workflow validation. May be nil to skip the
	// connection-reference validation pass.
	ConnChecker workflow.ConnectionChecker
	// Auth, when non-nil, is mounted at the top of /api/v1 and wraps the /mcp
	// handler so REST, WS, and MCP all inherit one authz boundary. Nil disables
	// auth (legacy/tests) -- production wiring always sets it (Task 8).
	Auth *AuthMiddleware
	// AuthHandler serves the /auth/* login flow OUTSIDE the auth middleware
	// (these routes establish the session). Nil in local mode / when auth is
	// disabled.
	AuthHandler *AuthHandler
	// Tokens powers the personal API token endpoints under /api/v1/tokens. The
	// service scopes every operation to the request identity, so the routes sit
	// inside the authenticated group. Nil disables the token routes.
	Tokens *identity.TokenService
	// DeviceCodes powers the OAuth 2.0 device-grant endpoints. /auth/device/code
	// and /auth/device/token are unauthenticated (mounted outside /api/v1); the
	// approve/deny actions run behind the auth middleware. Nil disables them.
	DeviceCodes *identity.DeviceCodeService
	// PublicBaseURL is the externally reachable base URL used to build the device
	// verification_uri shown to the human approver.
	PublicBaseURL string
	// Users powers the admin-only role-management endpoints under /api/v1/users.
	// The admin gate and demotion floors live in the service. Nil disables them.
	Users *instance.UserService
	// DashboardStats powers GET /api/v1/dashboard/stats. The service clamps
	// requested departments to the caller's effective set (admins exempt). Nil
	// disables the route (legacy/tests).
	DashboardStats dashboardStatsService
	// ScheduleHandler powers the recurring-schedule management endpoints under
	// /api/v1/schedules (org-readiness Phase 3). Authz lives in the shared
	// schedule.Service. Nil disables the routes (legacy/tests).
	ScheduleHandler *ScheduleHandler
	// BackendHealth is the shared, TTL-cached backend-health component (codex
	// install/login readiness) constructed once in the composition root and
	// injected into BOTH this HTTP SystemHandler and the MCP server, so the cache
	// outlives any single request and the probe forks at most once per TTL. Nil
	// omits the backends list (legacy/tests).
	BackendHealth BackendHealthChecker
	// Layouts supplies authorized revision-scoped status/retry and the instance layout block.
	// Routes remain registered when nil, reporting explicit unavailability.
	Layouts LayoutQueryService
	// GraphState enables GET /instances/{id}/graph-state when non-nil.
	GraphState GraphStateService
	// SubtreeEvents enables ?subtree=1 only when GraphState is also set.
	// Otherwise subtree requests return 501; per-instance mode is unchanged.
	SubtreeEvents SubtreeSubscriber
}

// NewRouter creates the chi router with all API routes wired up.
// If DashboardHandler is non-nil, unmatched routes fall through to the
// embedded SPA instead of returning chi's default 404.
// ConversationHandler may be nil if interactive messaging is not needed.
//
// Deprecated: Use NewRouterFromDeps instead.
func NewRouter(
	svc EngineService,
	workflows workflow.Repository,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	fileReader FileReader,
	eventBus engine.EventBus,
	cfg *config.Config,
	queueManager QueueManagerPort,
	queueRepo instance.QueueRepository,
	dashboardHandler http.Handler,
	notifHandler *NotificationHandler,
	conversationHandler engine.ConversationHandler,
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
	// Authentication (Phase 6): d.Auth is mounted at the top of the /api/v1 group
	// (covering REST + the WS upgrade GETs) and wraps the /mcp handler below, so
	// all three surfaces share one authz boundary. In local mode it injects the
	// default admin identity (frictionless); in org mode it requires a valid
	// session. The /auth/* routes are registered OUTSIDE this guard. A nil Auth
	// disables authentication (legacy/tests only).
	//
	// NOTE: this is AUTHENTICATION (who is calling), not per-user VISIBILITY
	// scoping (which rows a caller may see) -- that is a later Phase 6 milestone.

	wfHandler := NewWorkflowHandler(d.Workflows, d.Service, d.FileReader, d.Instances, d.ProfileChecker)
	wfHandler.SetWorkflowService(d.WorkflowSvc)
	if d.ConnChecker != nil {
		wfHandler.SetConnectionChecker(d.ConnChecker)
	}
	instHandler := NewInstanceHandler(d.Instances, d.NodeExecs, d.Snapshots, d.Service)
	instHandler.SetLayouts(d.Layouts)
	layoutHandler := NewLayoutHandler(d.Layouts)
	if d.GraphState != nil {
		instHandler.SetGraphState(d.GraphState)
	}
	// One on/off decision for the whole feature. When enabled we wire the real
	// per-user repo + acting-user resolver; when disabled we wire a named Null
	// Object repo + resolver so the handler body never nil-checks the seam. The
	// dismiss/restore/dismiss-all routes are still only registered when enabled
	// (so the endpoints 404 rather than no-op-200 when the feature is off).
	dismissalsEnabled := d.Dismissals != nil && d.DismissalUsers != nil
	if dismissalsEnabled {
		instHandler.SetDismissals(d.Dismissals, NewCurrentUserResolver(d.DismissalUsers))
	} else {
		var (
			noopDismissals instance.DismissalRepository = nullDismissalRepository{}
			noopResolver   CurrentUserResolver          = nullUserResolver{}
		)
		instHandler.SetDismissals(noopDismissals, noopResolver)
	}
	streamHandler := NewStreamHandler(d.Instances, d.NodeExecs, d.EventBus, d.ConversationHandler, d.Service)
	if d.SubtreeEvents != nil && d.GraphState != nil {
		streamHandler.SetSubtreeStreaming(d.SubtreeEvents, d.GraphState, 0)
	}
	sysStreamHandler := NewSystemStreamHandler(d.EventBus, d.Instances)
	settings := d.Settings
	if settings == nil {
		settings = config.NewSettingsStore(&d.Config.Notifications.Teams)
	}
	sysHandler := &SystemHandler{config: d.Config, settings: settings, queueManager: d.QueueManager, queueRepo: d.QueueRepo, backendHealth: d.BackendHealth}

	var interactiveHandler *InteractiveHandler
	if d.ConversationHandler != nil {
		interactiveHandler = NewInteractiveHandler(d.ConversationHandler, d.Instances, d.NodeExecs)
	}

	var deviceHandler *DeviceHandler
	if d.DeviceCodes != nil {
		deviceHandler = NewDeviceHandler(d.DeviceCodes, strings.TrimRight(d.PublicBaseURL, "/")+"/auth/device")
	}

	r.Route("/api/v1", func(r chi.Router) {
		if d.Auth != nil {
			// Covers every REST route AND the WS upgrade GETs in this group, since
			// the session cookie is sent on the upgrade request.
			r.Use(d.Auth.Handler)
		}

		// System-wide WebSocket stream
		r.Get("/ws", sysStreamHandler.HandleWebSocket)

		// System endpoints
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})

		// Whoami: returns the caller's identity projection (RBAC Phase 1). Behind
		// the auth middleware (when enabled); in local mode it returns the
		// default-group admin. No secrets (subject/email) are serialized.
		meHandler := NewMeHandler(d.Config.Auth.Mode)
		r.Get("/me", meHandler.Me)

		r.Get("/system/config", sysHandler.GetConfig)
		r.Put("/system/config/queue", sysHandler.UpdateQueueConfig)
		r.Put("/system/config/teams", sysHandler.UpdateTeamsConfig)

		// Department dashboard stats (Phase 4). Inside the auth group so the
		// service reads the caller identity from context for the department clamp.
		if d.DashboardStats != nil {
			dashStatsHandler := NewDashboardHandler(d.DashboardStats)
			r.Get("/dashboard/stats", dashStatsHandler.Stats)
		}

		// Workflow endpoints
		r.Route("/workflows", func(r chi.Router) {
			r.Get("/", wfHandler.List)
			r.Post("/", wfHandler.Create)
			r.Get("/{name}", wfHandler.Get)
			r.Get("/{name}/group-layout", wfHandler.GroupLayout)
			r.Put("/{name}", wfHandler.Update)
			r.Delete("/{name}", wfHandler.Delete)
			r.Post("/{name}/validate", wfHandler.Validate)
			r.Post("/{name}/trigger", wfHandler.Trigger)
			r.Post("/{name}/publish", wfHandler.Publish)
			r.Put("/{name}/visibility", wfHandler.SetVisibility)
			r.Post("/{name}/copy", wfHandler.Copy)
			r.Get("/{name}/layout", layoutHandler.GetWorkflowLayout)
			r.Post("/{name}/layout/retry", layoutHandler.RetryWorkflowLayout)
			r.Get("/{name}/stats", wfHandler.Stats)
			r.Get("/{name}/files", wfHandler.ListFiles)
			r.Get("/{name}/files/*", wfHandler.ReadFile)
			r.Put("/{name}/files/*", wfHandler.WriteFile)
			r.Delete("/{name}/files/*", wfHandler.DeleteFile)
		})

		// Schedule endpoints (org-readiness Phase 3). Static /enable & /disable
		// are registered alongside the {id} wildcard; chi matches the longer
		// /{id}/enable pattern over GET /{id}, so ordering is unambiguous here.
		if d.ScheduleHandler != nil {
			r.Route("/schedules", func(r chi.Router) {
				r.Get("/", d.ScheduleHandler.List)
				r.Post("/", d.ScheduleHandler.Create)
				r.Get("/{id}", d.ScheduleHandler.Get)
				r.Put("/{id}", d.ScheduleHandler.Update)
				r.Delete("/{id}", d.ScheduleHandler.Delete)
				r.Post("/{id}/enable", d.ScheduleHandler.Enable)
				r.Post("/{id}/disable", d.ScheduleHandler.Disable)
				r.Post("/{id}/run", d.ScheduleHandler.RunNow)
			})
		}

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
			// Register dismiss-all first so the static segment is not captured by
			// the {id} wildcard routes below.
			if dismissalsEnabled {
				r.Post("/dismiss-all", instHandler.DismissAll)
			}
			r.Get("/", instHandler.List)
			r.Get("/{id}", instHandler.Get)
			if dismissalsEnabled {
				r.Post("/{id}/dismiss", instHandler.Dismiss)
				r.Post("/{id}/restore", instHandler.Restore)
			}
			r.Post("/{id}/pause", instHandler.Pause)
			r.Post("/{id}/resume", instHandler.Resume)
			r.Post("/{id}/cancel", instHandler.Cancel)
			r.Post("/{id}/retry", instHandler.Retry)
			r.Post("/{id}/rerun", instHandler.Rerun)
			r.Post("/{id}/priority", instHandler.SetPriority)
			r.Get("/{id}/trace", instHandler.GetTrace)
			r.Get("/{id}/trace/{nodeId}", instHandler.GetNodeTrace)
			if d.GraphState != nil {
				r.Get("/{id}/graph-state", instHandler.GraphState)
			}
			r.Get("/{id}/variables", instHandler.GetVariables)
			r.Get("/{id}/variables/history", instHandler.GetVariableHistory)
			r.Get("/{id}/interventions", instHandler.GetInterventions)
			r.Get("/{id}/layout", layoutHandler.GetInstanceLayout)
			r.Post("/{id}/layout/retry", layoutHandler.RetryInstanceLayout)
			r.Get("/{id}/ws", streamHandler.HandleWebSocket)
			if interactiveHandler != nil {
				r.Post("/{id}/interact/end", interactiveHandler.EndSession)
				r.Post("/{id}/interactive/abandon", interactiveHandler.AbandonSession)
			}
		})

		// Personal API token endpoints. The service scopes every call to the
		// request identity injected by the auth middleware above, so a caller can
		// only manage their own tokens.
		if d.Tokens != nil {
			th := NewTokenHandler(d.Tokens)
			r.Route("/tokens", func(r chi.Router) {
				r.Post("/", th.Create)
				r.Get("/", th.List)
				r.Delete("/{id}", th.Revoke)
			})
		}

		// Device-grant approval actions run behind the auth middleware: the
		// approver must be a logged-in browser (the service reads their identity
		// from context to own the eventual token).
		if deviceHandler != nil {
			r.Post("/auth/device/approve", deviceHandler.Approve)
			r.Post("/auth/device/deny", deviceHandler.Deny)
		}

		// Admin-only user/role management. The service enforces admin + the
		// demotion floors; the routes sit inside the authenticated group.
		if d.Users != nil {
			uh := NewUserHandler(d.Users)
			r.Route("/users", func(r chi.Router) {
				r.Get("/", uh.List)
				r.Put("/{id}/role", uh.SetRole)
			})
		}
	})

	// Mount the MCP Streamable HTTP endpoint so LLM clients can access tools
	// over HTTP instead of (or in addition to) the stdio transport.
	//
	// AUTH (Phase 6): /mcp is a sibling of the /api/v1 group, so it is wrapped
	// explicitly with d.Auth. It exposes destructive tools (workflow_delete,
	// instance_cancel, system_update_queue_config); in org mode an anonymous
	// request is rejected at the middleware before reaching any tool. In local
	// mode the default admin identity is injected. With auth disabled (nil) the
	// endpoint is unauthenticated and MUST NOT be exposed on untrusted networks.
	if d.MCPHandler != nil {
		mcp := d.MCPHandler
		if d.Auth != nil {
			mcp = d.Auth.Handler(mcp)
		}
		r.Mount("/mcp", mcp)
	}

	// Unauthenticated /auth/* surface (OIDC login flow + device-grant
	// code/token). These are registered OUTSIDE the /api/v1 group and NOT wrapped
	// by d.Auth — they establish the session the middleware later validates, so
	// they cannot require one. Because they are unauthenticated and credential-
	// adjacent, they are the natural DoS/brute-force target, so the whole group is
	// rate-limited per client IP (RealIP middleware above sets the key).
	r.Group(func(r chi.Router) {
		r.Use(httprate.LimitByIP(authRateLimitPerMinute, time.Minute))

		// OIDC browser login flow (Phase 6).
		if d.AuthHandler != nil {
			r.Get("/auth/login", d.AuthHandler.Login)
			r.Get("/auth/callback", d.AuthHandler.Callback)
			r.Post("/auth/logout", d.AuthHandler.Logout)
		}

		// Device authorization grant: /code (start) and /token (poll) are
		// unauthenticated — the browserless client has no session yet. The
		// approve/deny actions are registered INSIDE /api/v1 above (session-gated).
		if deviceHandler != nil {
			r.Post("/auth/device/code", deviceHandler.Start)
			r.Post("/auth/device/token", deviceHandler.Token)
		}
	})

	// Serve the embedded dashboard SPA for any route not matched by the API.
	//
	// AUTH (Phase 6): the SPA is the browser-navigation surface, so it is wrapped
	// with d.Auth too. In org mode an anonymous navigation (GET, Accept text/html)
	// is redirected to /auth/login by the middleware's reject(); after login the
	// session cookie is present and assets load. In local mode the default admin
	// identity is injected. /auth/* is registered above, OUTSIDE this guard.
	if d.DashboardHandler != nil {
		dash := d.DashboardHandler
		if d.Auth != nil {
			dash = d.Auth.Handler(dash)
		}
		r.NotFound(dash.ServeHTTP)
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

// BackendHealthChecker probes backend-scoped health (codex install/login
// readiness) and projects it into the transport-neutral RedactedBackend list.
// It is the injected, TTL-cached component (adapter/backendhealth.Checker) shared
// with the MCP surface; the handler depends on this seam, not the concrete type,
// so a frequently-polled GET /system/config forks at most one probe pair per TTL.
type BackendHealthChecker interface {
	Backends(ctx context.Context) []config.RedactedBackend
}

// SystemHandler handles system configuration endpoints.
type SystemHandler struct {
	mu           sync.Mutex // guards the runtime-mutable Queue caps on config
	config       *config.Config
	settings     *config.SettingsStore // synchronized owner of Teams settings
	queueManager QueueManagerPort
	queueRepo    instance.QueueRepository
	// backendHealth is the shared, TTL-cached backend-health component injected
	// from the composition root. When nil, the handler omits the backends list
	// (legacy/tests) rather than forking an uncached probe per request.
	backendHealth BackendHealthChecker
}

// GetConfig handles GET /system/config. It marshals the shared
// config.RedactedConfig allow-list projection (the same one the MCP
// system_config tool uses) augmented with live queue counts.
func (h *SystemHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	var running, queued, waiting int
	if h.queueRepo != nil {
		running, _ = h.queueRepo.CountRunning(r.Context())
		queued, _ = h.queueRepo.CountQueued(r.Context())
		waiting, _ = h.queueRepo.CountWaiting(r.Context())
	}

	teams := h.settings.GetTeams()
	h.mu.Lock()
	resp := config.Redact(h.config, teams)
	h.mu.Unlock()

	resp.Queue.RunningCount = running
	resp.Queue.QueuedCount = queued
	resp.Queue.WaitingCount = waiting
	// Attach backend-scoped health (codex install/login readiness) so the UI can
	// pre-flight a run. Served from the shared, TTL-cached component injected at
	// the composition root: a cached read forks nothing, so a frequently-polled
	// (and, in local mode, unauthenticated) GET /system/config cannot fork a codex
	// probe pair per request. Nil seam ⇒ omit the list (legacy/tests).
	if h.backendHealth != nil {
		resp.Backends = h.backendHealth.Backends(r.Context())
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

// updateTeamsConfigRequest is the JSON body for updating Teams notification
// config. Pointer fields distinguish "absent" (leave unchanged) from "set".
type updateTeamsConfigRequest struct {
	WebhookURL *string `json:"webhookUrl"`
	Enabled    *bool   `json:"enabled"`
}

// UpdateTeamsConfig handles PUT /system/config/teams. Partial updates are
// supported via pointer fields. A non-empty webhook URL must pass
// config.ValidateTeamsWebhookURL (absolute https + non-private host);
// otherwise the request is rejected and config is left unchanged.
func (h *SystemHandler) UpdateTeamsConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req updateTeamsConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	if req.WebhookURL != nil {
		if err := config.ValidateTeamsWebhookURL(*req.WebhookURL); err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
	}

	// Route the partial update through the synchronized store so the HTTP
	// writer, the MCP writer, and the notifier's live read all share one mutex.
	result := h.settings.SetTeams(req.WebhookURL, req.Enabled)

	WriteJSON(w, http.StatusOK, map[string]any{
		"enabled":    result.Enabled,
		"webhookUrl": config.RedactWebhookURL(result.WebhookURL),
	})
}
