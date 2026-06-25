package mcp

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/arbitron/arbitron/domain/workflow"
)

// EngineService defines the subset of engine.Engine that the MCP adapter
// needs for workflow lifecycle operations. This interface keeps the adapter
// decoupled from the concrete Engine implementation.
type EngineService interface {
	TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority string, department string) (uuid.UUID, bool, error)
	ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error)
	// ListEnriched returns the authorized page with additive enriched fields
	// (run name, submitter, friendly step, model, backend, retry count,
	// queue position). Same authz as ListInstances.
	ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
	GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
	GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)
	GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)
	GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error)
	GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)
	GetInterventions(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error)
	Pause(ctx context.Context, instanceID uuid.UUID) error
	Resume(ctx context.Context, instanceID uuid.UUID) error
	Cancel(ctx context.Context, instanceID uuid.UUID) error
	Retry(ctx context.Context, instanceID uuid.UUID, variableEdits map[string]any) error
	Rerun(ctx context.Context, instanceID uuid.UUID) (uuid.UUID, error)
	SetPriority(ctx context.Context, id uuid.UUID, priority string) error
}

// QueueManagerPort defines the subset of queue.Manager that the MCP adapter
// needs for queue configuration and evaluation.
type QueueManagerPort interface {
	UpdateConfig(globalCap, perUserCap int)
	Evaluate(ctx context.Context) error
}

// BackendHealthChecker probes backend-scoped health (codex install/login
// readiness) and projects it into the transport-neutral RedactedBackend list.
// It is the injected, TTL-cached component (adapter/backendhealth.Checker) shared
// with the HTTP surface; the system_config tool depends on this seam so a
// frequently-called probe forks at most once per TTL across both transports.
type BackendHealthChecker interface {
	Backends(ctx context.Context) []config.RedactedBackend
}

// Deps holds the domain dependencies injected into MCP tool handlers.
// Each field corresponds to a port from the hexagonal architecture.
//
// Required fields (must be non-nil for the server to function):
//   - Engine, Workflows, Instances, NodeExecs, Snapshots, FileReader, Config
//
// Optional fields (nil-safe; handlers guard against nil before use):
//   - EventBus, QueueManager, QueueRepo, Notifications, ConversationHandler
type Deps struct {
	Engine    EngineService
	Workflows workflow.Repository
	// WorkflowSvc resolves storage scope/authorization from the calling user's
	// identity for the workflow_* tools. Optional: when nil, a default service
	// over Workflows (no publisher lookup) is constructed lazily.
	WorkflowSvc *workflow.Service
	Instances   instance.InstanceRepository
	NodeExecs   instance.NodeExecutionRepository
	Snapshots   instance.VariableSnapshotRepository
	FileReader  engine.FileReader
	EventBus    engine.EventBus
	Config      *config.Config
	// Settings is the synchronized owner of runtime-mutable Teams settings,
	// shared with the HTTP adapter and the Teams notifier so all three route
	// reads/writes through one mutex. Required for the system_config and
	// system_update_teams_config tools.
	Settings            *config.SettingsStore
	QueueManager        QueueManagerPort
	QueueRepo           instance.QueueRepository
	Notifications       notification.Repository
	ConversationHandler engine.ConversationHandler
	// ProfileChecker resolves a node's model_profile alias to its capability
	// kind during workflow validation. Optional: a nil checker skips the
	// model_profile validation pass.
	ProfileChecker workflow.ProfileChecker
	// ConnChecker validates that a profile's non-empty ConnectionRef names an
	// existing connection during workflow validation. Optional: a nil checker
	// skips the connection-reference validation pass.
	ConnChecker workflow.ConnectionChecker
	// Connections is the connection-management store powering the runtime
	// resolver path. The MCP connection_* CRUD tools now route through
	// ConnectionAdmin (admin-gated); this raw store is retained for any non-admin
	// engine path. The plaintext API key enters only through Create/Update and is
	// sealed immediately; it is never returned by Get/List.
	Connections provider.ConnectionStore
	// ProfileAdmin powers the admin-only profile_* mutation tools and the
	// read-open profile_list / profile_get + provider_capabilities tools. Authz +
	// capability validation + recache live in the service. Optional: when nil, the
	// profile tools report the service as unavailable.
	ProfileAdmin *provider.ProfileService
	// ConnectionAdmin powers the now admin-only connection_* tools. It is a thin
	// admin gate over the ConnectionStore (same authz rule as ProfileAdmin).
	// Optional: when nil, the connection tools report the service as unavailable.
	ConnectionAdmin *provider.ConnectionService
	// Capabilities is the effective capability ruleset (allowed tiers + effort per
	// backend/model). Injected for any surface that needs the rules directly;
	// provider_capabilities also reaches them through ProfileAdmin.Capabilities.
	Capabilities provider.CapabilityRules
	// Tokens powers the token_* tools. Every operation is scoped to the calling
	// user's identity (carried on the tool ctx by the auth middleware). Optional:
	// when nil, the token tools report the service as unavailable.
	Tokens *identity.TokenService
	// Users powers the admin-only user_* tools. The admin gate + demotion floors
	// live in the service. Optional: when nil, the user tools report unavailable.
	Users *instance.UserService
	// ScheduleService powers the schedule_* tools (org-readiness Phase 3). Authz
	// + validation live in the service, shared with the REST surface, so the two
	// never drift. Optional: when nil, the schedule tools are not registered (the
	// handlers therefore always see a non-nil service).
	ScheduleService *schedule.Service
	// Logger is the structured logger used by tool handlers. SECURITY: the
	// connection tools log the connection NAME only, never the api_key. Optional:
	// a nil logger falls back to slog.Default().
	Logger *slog.Logger
	// AuditSvc powers the workflow_audit tool's target-scoped audit-trail query.
	// It is the same best-effort audit service the Service records through,
	// so the query reads exactly what the lifecycle ops wrote. Optional: when nil,
	// the workflow_audit tool reports the audit trail as unavailable.
	AuditSvc *audit.Service
	// BackendHealth is the shared, TTL-cached backend-health component (codex
	// install/login readiness) constructed once in the composition root and
	// injected into BOTH the MCP server and the HTTP SystemHandler, so the cache
	// outlives any single call and the probe forks at most once per TTL. Nil omits
	// the backends list (legacy/tests).
	BackendHealth BackendHealthChecker
}
