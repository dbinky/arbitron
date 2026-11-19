package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Engine is the core workflow execution engine. It orchestrates the loading,
// validation, and step-by-step execution of workflow graphs.
type Engine struct {
	workflows        workflow.Repository
	instances        instance.InstanceRepository
	nodeExecutions   instance.NodeExecutionRepository
	snapshots        instance.VariableSnapshotRepository
	users            instance.UserRepository
	executors        map[workflow.NodeType]NodeExecutor
	eventBus         EventBus
	queueManager     QueueManager
	queueRepo        instance.QueueRepository
	scheduler        TimerScheduler
	webhookRegistry  WebhookRegistrar
	notifications    notification.Repository
	dismissals       instance.DismissalRepository
	externalNotifier notification.ExternalNotifier
	interventions    instance.InterventionRepository
	auditRecorder    AuditRecorder // optional; emits lead_intervention/admin.override audit events
	workspaceManager WorkspaceManager
	// groupFinalizer is the optional group-task terminal hook; nil keeps the
	// plain completion path. Configured before starting instances.
	groupFinalizer   GroupTaskFinalizer
	publishers       workflow.PublisherLookup  // optional; gates the trigger read path by visibility
	resolver         IDResolver                // optional; workflow identity (id<->name + archived) seam
	enricher         *InstanceEnricher         // optional; when set, ListEnriched uses it
	modelSnapshotter provider.ModelSnapshotter // optional; when set, a trigger pins every model alias the run uses
	pinRelease       func(uuid.UUID)           // optional; drops a terminal run's cached pins
	pinGate          ModelPinGate              // optional; serializes the pin capture against advancement and admin edits
	// spawnLinks is the link-addressed read surface. It is NEVER nil: NewEngine
	// installs NoopSpawnLinks{} and SetSpawnLinks rejects nil, so the cancel
	// cascade can never be silently downgraded to a no-op by a missed wiring
	// step. Opting out is possible but must be spelled NoopSpawnLinks{}, whose
	// doc comment states that it disables descendant cancellation.
	spawnLinks instance.SpawnLinkRepository
	// groupHandler receives terminal group-task children instead of the legacy
	// wake. Guarded by mu; read through groupTerminalHandler.
	groupHandler GroupTerminalHandler
	// profiles resolves a node's model_profile so node.started can describe the
	// binding the node runs on. NEVER nil: NoopProfileResolver{} until wired.
	profiles provider.ProfileResolver
	// defaultAgentBackend is the deployment-wide fallback agent backend stamped
	// onto node events when a node has BackendUnset. It is deployment config,
	// injected at construction — not read through the presentation enricher.
	defaultAgentBackend string
	// groupSupport is the parallel_group execution level (see group_support.go).
	// Zero value GroupSupportNone refuses every group-bearing trigger; guarded
	// by mu and raised only by the composition root via SetGroupSupport.
	groupSupport GroupSupport
	// executorsFrozen seals the executor map. Registration and lookup both go
	// through mu, and freezing makes a post-startup registration a loud panic
	// rather than a data race — see RegisterExecutor.
	executorsFrozen bool
	mu              sync.Mutex
	contexts        map[uuid.UUID]context.CancelFunc
	wg              sync.WaitGroup
}

// NewEngine constructs a new Engine with all required dependencies.
func NewEngine(
	workflows workflow.Repository,
	instances instance.InstanceRepository,
	nodeExecutions instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	users instance.UserRepository,
	executors map[workflow.NodeType]NodeExecutor,
	eventBus EventBus,
	queueManager QueueManager,
	queueRepo instance.QueueRepository,
	defaultAgentBackend string,
) *Engine {
	return &Engine{
		workflows:           workflows,
		instances:           instances,
		nodeExecutions:      nodeExecutions,
		snapshots:           snapshots,
		users:               users,
		executors:           executors,
		eventBus:            eventBus,
		queueManager:        queueManager,
		queueRepo:           queueRepo,
		defaultAgentBackend: defaultAgentBackend,
		contexts:            make(map[uuid.UUID]context.CancelFunc),
		// Non-nil from birth so cascadeCancel never has to nil-check a
		// containment control. The composition root overwrites this with the real
		// repository via SetSpawnLinks.
		spawnLinks: NoopSpawnLinks{},
		profiles:   NoopProfileResolver{},
	}
}

// RegisterExecutor registers a node executor AFTER construction.
//
// Several executors cannot be built before the engine exists — timer_delay and
// webhook_wait need the scheduler and webhook registry, spawn_workflow needs
// the engine itself as its ChildSpawner — so the composition root has always
// mutated the executor map post-construction. That worked only because the map
// header is shared, and it was a plain unsynchronized write to a map the run
// loop reads from other goroutines: a data race the moment any recovery or
// queue goroutine starts before wiring finishes.
//
// Registration now takes the engine's mutex, and FreezeExecutors seals the set
// before those goroutines start, so a late registration is a loud panic instead
// of silent memory corruption. A nil executor is rejected for the same reason:
// it would surface much later as "no executor registered for node type".
func (e *Engine) RegisterExecutor(t workflow.NodeType, ex NodeExecutor) {
	if ex == nil {
		panic("engine: RegisterExecutor requires a non-nil NodeExecutor for node type " + string(t))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.executorsFrozen {
		panic("engine: RegisterExecutor called after FreezeExecutors for node type " + string(t) +
			"; every executor must be registered before recovery and queue goroutines start")
	}
	e.executors[t] = ex
}

// FreezeExecutors seals the executor set. The composition root MUST call it
// after the last RegisterExecutor and BEFORE starting any goroutine that can
// execute a node (crash recovery, the queue evaluator, the schedule evaluator).
// It is idempotent.
func (e *Engine) FreezeExecutors() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executorsFrozen = true
}

// executorFor looks up a node type's executor under the same lock registration
// takes, so a read can never observe a half-written map.
func (e *Engine) executorFor(t workflow.NodeType) (NodeExecutor, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ex, ok := e.executors[t]
	return ex, ok
}

// SetModelSnapshotter enables trigger-time model pinning: every instance the
// engine creates afterwards carries the models its workflow's profile aliases
// resolved to at trigger. Triggers fail if it or the shared pin gate is missing,
// including workflows with no model aliases. Configure both before serving.
// Nil and typed-nil snapshotters are rejected before replacing a working one.
func (e *Engine) SetModelSnapshotter(s provider.ModelSnapshotter) {
	if nilPinDependency(s) {
		panic("engine: SetModelSnapshotter requires a non-nil snapshotter")
	}
	e.modelSnapshotter = s
}

// Interface collaborators can carry typed nils that evade a plain nil check.
func nilPinDependency(dependency any) bool {
	if dependency == nil {
		return true
	}
	v := reflect.ValueOf(dependency)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// ModelPinGate is the shared execution-gate lease a trigger holds while it
// snapshots model aliases. provider.ExecutionGate satisfies it.
type ModelPinGate interface {
	WithRead(ctx context.Context, fn func(context.Context) error) error
}

// SetModelPinGate makes every trigger capture its model pins under the shared
// execution-gate read lease. Nil and typed-nil gates are rejected. Omitting the
// dependency also fails every new trigger before instance creation.
func (e *Engine) SetModelPinGate(g ModelPinGate) {
	if nilPinDependency(g) {
		panic("engine: SetModelPinGate requires a non-nil gate")
	}
	e.pinGate = g
}

// SetPinCacheRelease registers the callback that drops a run's cached model
// pins once it reaches a terminal state. It runs from OnChildTerminal, which
// every terminal transition (and the queue manager's child-terminal hook)
// already calls, so it composes with the spawn wake instead of replacing it.
// Eviction is safe for paused or retryable runs: pins are immutable in the
// repository and reload on the next read. A nil callback is rejected.
func (e *Engine) SetPinCacheRelease(release func(uuid.UUID)) {
	if release == nil {
		panic("engine: SetPinCacheRelease requires a non-nil release func")
	}
	e.pinRelease = release
}

// workflowModelAliases returns the distinct non-empty aliases of the serialized
// workflow's model consumers, sorted. workflow.ModelConsumers is the single
// enumeration path_inventory and validateModelProfiles also range over, so a
// stale model_profile on any other node type neither pins nor refuses, and the
// parallel_group conflict resolver's alias is pinned like a node's.
func workflowModelAliases(wf *workflow.Workflow) []string {
	seen := map[string]struct{}{}
	for _, c := range workflow.ModelConsumers(wf) {
		if c.Alias != "" {
			seen[c.Alias] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// captureModelPins snapshots the workflow's aliases through the configured
// snapshotter. A snapshotter that omits a requested alias is a fault: a partial
// map would leave the new run without an immutable model for that alias.
func (e *Engine) captureModelPins(ctx context.Context, wf *workflow.Workflow) (provider.ModelPinSnapshot, error) {
	var empty provider.ModelPinSnapshot
	if e.modelSnapshotter == nil {
		return empty, fmt.Errorf("pin models for %q: model snapshotter is not configured", wf.Name)
	}
	if e.pinGate == nil {
		return empty, fmt.Errorf("pin models for %q: shared execution gate is not configured", wf.Name)
	}
	aliases := workflowModelAliases(wf)
	var pins provider.ModelPinSnapshot
	snapshot := func(ctx context.Context) (err error) {
		pins, err = e.modelSnapshotter.SnapshotModels(ctx, aliases, wf)
		return err
	}
	if err := e.pinGate.WithRead(ctx, snapshot); err != nil {
		return empty, fmt.Errorf("pin models for %q: %w", wf.Name, err)
	}
	out := provider.ModelPinSnapshot{
		Models:            make(map[string]string, len(aliases)),
		OpaqueRoutes:      make(map[string]string, len(pins.OpaqueRoutes)),
		OpaqueConnections: make(map[string]string, len(pins.OpaqueRoutes)),
	}
	for _, a := range aliases {
		m, ok := pins.Models[a]
		if !ok || m == "" {
			return empty, fmt.Errorf("pin models for %q: snapshot returned no model for profile %q", wf.Name, a)
		}
		out.Models[a] = m
	}
	if len(pins.Models) != len(out.Models) {
		return empty, fmt.Errorf("pin models for %q: snapshot returned unrequested profiles", wf.Name)
	}
	for alias, route := range pins.OpaqueRoutes {
		conn := pins.OpaqueConnections[alias]
		if _, ok := out.Models[alias]; !ok || conn == "" || route != provider.CanonicalAzure && route != provider.CanonicalBedrock {
			return empty, fmt.Errorf("pin models for %q: snapshot returned invalid model pin routes for profile %q", wf.Name, alias)
		}
		out.OpaqueRoutes[alias] = route
		out.OpaqueConnections[alias] = conn
	}
	if len(pins.OpaqueConnections) != len(out.OpaqueConnections) {
		return empty, fmt.Errorf("pin models for %q: snapshot returned connections without model pin routes", wf.Name)
	}
	return out, nil
}

// SetScheduler sets the timer scheduler on the engine.
func (e *Engine) SetScheduler(s TimerScheduler) {
	e.scheduler = s
}

// SetWebhookRegistry sets the webhook registry on the engine.
func (e *Engine) SetWebhookRegistry(reg WebhookRegistrar) {
	e.webhookRegistry = reg
}

// SetNotifications sets the notification repository on the engine.
func (e *Engine) SetNotifications(repo notification.Repository) {
	e.notifications = repo
}

// SetDismissals wires the per-user dismissal repository. When set, Retry clears
// an instance's dismissals (for all users) so a retried item resurfaces. When
// unset, retry behaves exactly as before.
func (e *Engine) SetDismissals(repo instance.DismissalRepository) {
	e.dismissals = repo
}

// SetExternalNotifier sets the external notifier on the engine. When set,
// every notification emitted by the engine will also be forwarded to this
// notifier (e.g., for email, Slack, or logging).
func (e *Engine) SetExternalNotifier(n notification.ExternalNotifier) {
	e.externalNotifier = n
}

// SetWorkspaceManager wires the per-instance workspace manager. When unset,
// workspace directories are not created and {{workspace}} is not seeded.
func (e *Engine) SetWorkspaceManager(wm WorkspaceManager) {
	e.workspaceManager = wm
}

// SetInterventions wires the intervention audit repository. When unset,
// attribution rows are not written (nil-safe), but the instance.intervened
// event is still published so live viewers see admin actions.
func (e *Engine) SetInterventions(repo instance.InterventionRepository) {
	e.interventions = repo
}

// SetAuditRecorder wires the Phase-2 audit service for non-owner interventions
// (lead_intervention / admin.override). When unset, no domain audit events are
// written (the legacy intervention attribution row + event are unaffected).
func (e *Engine) SetAuditRecorder(r AuditRecorder) {
	e.auditRecorder = r
}

// SetPublisherLookup wires the publication lookup used to gate the trigger read
// path by workflow visibility. When unset (nil), all shared workflows are
// triggerable (local/read-only mode), preserving today's behavior.
func (e *Engine) SetPublisherLookup(p workflow.PublisherLookup) {
	e.publishers = p
}

// IDResolver is the narrow workflow-identity seam the engine uses to stamp a
// stable workflow id on a triggered instance, to re-resolve the current name of
// a workflow on rerun (so a rename between the original run and the rerun still
// targets the right definition), and to refuse triggering an archived workflow.
// workflow.Registry satisfies it.
//
// IsArchived is part of the contract — NOT an optional layered capability —
// precisely so a resolver that cannot report archived state is a COMPILE error
// rather than a runtime type-assertion that silently disables archive-blocking
// under any wrapping decorator. When the resolver is unset (nil, local/
// unconfigured mode), the engine behaves as before: instances carry a Nil
// WorkflowID, rerun resolves by the retained WorkflowName label, and no archive
// check runs.
type IDResolver interface {
	ResolveID(ctx context.Context, name string) (uuid.UUID, error)
	ResolveName(ctx context.Context, id uuid.UUID) (string, error)
	// IsArchived reports whether the workflow registered under name is archived.
	// A lookup error must NOT be swallowed by the caller: the trigger path fails
	// CLOSED (refuses the trigger) on error, because the archived flag is a
	// deliberate "do not run this" invariant.
	IsArchived(ctx context.Context, name string) (bool, error)
}

// SetWorkflowResolver wires the workflow-identity resolver used to stamp
// instances with a stable WorkflowID at trigger time, to re-resolve the current
// name on rerun, and to refuse archived triggers. When unset (nil), instances
// carry a Nil WorkflowID, rerun falls back to the retained WorkflowName label,
// and no archive check runs (local/unconfigured mode).
func (e *Engine) SetWorkflowResolver(r IDResolver) {
	e.resolver = r
}

// SetProfileResolver wires the resolver node.started uses to describe a node's
// model/effort/backend/provider. Pass the same resolver the executors receive
// so the live queue patch matches what the node runs on. nil panics — pass
// NoopProfileResolver{} to opt out deliberately.
func (e *Engine) SetProfileResolver(r provider.ProfileResolver) {
	if r == nil {
		panic("engine: SetProfileResolver requires a non-nil ProfileResolver (use NoopProfileResolver{})")
	}
	e.profiles = r
}

// SetInstanceEnricher wires the list-enrichment step. When set, ListEnriched
// runs the authorized ListInstances then the enricher to compose the additive
// fields. When unset, ListEnriched returns the un-enriched summaries wrapped in
// EnrichedInstanceSummary (run name only).
func (e *Engine) SetInstanceEnricher(en *InstanceEnricher) {
	e.enricher = en
}

// recordIntervention writes an audit row and publishes instance.intervened when
// the acting identity is NOT the instance owner. Owner self-actions (and
// identity-less system actions) are skipped. Attribution is best-effort: a
// repo write failure is logged, never returned, so the operation that already
// succeeded is not undone.
func (e *Engine) recordIntervention(ctx context.Context, inst *instance.Instance, action string, detail *string) {
	// Attribution row + domain audit event are owned by the shared
	// interventionRecorder so the engine and interactive paths cannot diverge
	// (PR #21). It resolves the actor, writes the best-effort row, and emits the
	// lead_intervention/admin.override audit event — all nil-safe.
	rec := interventionRecorder{interventions: e.interventions, audit: e.auditRecorder}
	actor, isNonOwner := rec.record(ctx, inst, action, detail)
	if !isNonOwner {
		return // owner self-action (or no identity): no event
	}
	e.eventBus.Publish(inst.ID, Event{
		Type:      "instance.intervened",
		Timestamp: time.Now(),
		Data:      map[string]any{"actorUserId": actor.String(), "action": action},
	})
}

// edgeTarget describes a destination from a graph edge.
type edgeTarget struct {
	nodeID     string
	outputPort string
}

// triggerOwner returns the triggering user's id: the context identity when
// present (REST/MCP triggers), otherwise the default user (scheduler/webhook
// system triggers that carry no identity). The bool is false only when no
// identity is present and the default user cannot be resolved.
func triggerOwner(ctx context.Context, users instance.UserRepository) (uuid.UUID, bool) {
	if id, ok := identity.FromContext(ctx); ok {
		return id.UserID, true
	}
	def, err := users.GetDefaultUser(ctx)
	if err != nil {
		return uuid.Nil, false
	}
	return def, true
}

// Trigger loads a workflow by name, validates inputs, creates a new instance,
// and delegates to the queue manager for capacity-based scheduling.
// The second return value indicates whether the instance started immediately (true)
// or stayed in queued state (false). It delegates to TriggerWithDepartment with
// no per-trigger department override, preserving the existing entrypoint.
func (e *Engine) Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
	return e.TriggerWithDepartment(ctx, workflowName, variables, priority, "")
}

// resolvePublication returns the resolved workflow's audience (visibility +
// department) for instance stamping. A workflow resolved from the shared scope
// MAY carry a publication record (department/public); a shared workflow with no
// record, or any private-scope resolution, is private (owner-only). It mirrors
// workflow.Visibility into the instance.Visibility self-owned type so the engine
// does not leak the workflow type onto the instance.
func (e *Engine) resolvePublication(ctx context.Context, scope workflow.Scope, workflowName string) (instance.Visibility, string) {
	if scope.Kind != workflow.ScopeShared || e.publishers == nil {
		return instance.VisibilityPrivate, ""
	}
	pub, found, err := e.publishers.PublicationOf(ctx, workflowName)
	if err != nil || !found {
		return instance.VisibilityPrivate, ""
	}
	// One named, tested conversion (instance.VisibilityFromWorkflow) replaces the
	// inline default-to-private switch (PR #21). Department travels with the
	// department level only; public/private carry no attribution department here.
	vis := instance.VisibilityFromWorkflow(string(pub.Visibility))
	if vis == instance.VisibilityDepartment {
		return vis, pub.Department
	}
	return vis, ""
}

// triggerAdminOverrode reports whether admin status was the SOLE reason callerID
// could access workflowName at trigger time, via the canonical
// workflow.AdminOverrode predicate. It returns false for non-shared scopes, an
// unconfigured lookup, an unpublished workflow, or a lookup error (best-effort —
// a transient lookup failure must not block the trigger). Routing through the
// shared predicate keeps the publisher branch the inline engine check dropped.
func (e *Engine) triggerAdminOverrode(ctx context.Context, scope workflow.Scope, workflowName string, callerID identity.Identity) bool {
	if scope.Kind != workflow.ScopeShared || e.publishers == nil {
		return false
	}
	pub, found, err := e.publishers.PublicationOf(ctx, workflowName)
	if err != nil || !found {
		return false
	}
	return workflow.AdminOverrode(callerID, pub)
}

// sharedVisibilityFilter builds the shared-scope gate for a workflow read.
//
// It is shared by TriggerWithDepartment and ChildInputs so a spawn node's
// child-workflow lookup can NEVER resolve a workflow the same caller could not
// trigger directly. Gating keys on the CONTEXT identity (a REST/MCP caller, or
// the reconstructed owner identity a spawn injects). A system trigger (no
// context identity) skips gating — a scheduler/webhook trigger already passed
// authorization at registration time. A nil filter disables shared-scope gating
// for callers with no publication lookup.
func (e *Engine) sharedVisibilityFilter(ctx context.Context) workflow.SharedVisibilityFilter {
	id, ok := identity.FromContext(ctx)
	if !ok || e.publishers == nil {
		return nil
	}
	pubs := e.publishers
	return func(ctx context.Context, name string) (bool, error) {
		pub, found, err := pubs.PublicationOf(ctx, name)
		if err != nil {
			return false, err
		}
		if !found {
			return true, nil // unpublished shared folder ⇒ open
		}
		return workflow.CanSeePublication(id, pub), nil
	}
}

// TriggerWithDepartment loads a workflow, validates inputs, stamps the run's
// visibility+department (applying the optional validated per-trigger department
// override), creates the instance, and enqueues it. departmentOverride is "" for
// the default Trigger entrypoint; when non-empty it must be one of the caller's
// effective departments (else ErrInvalidDepartmentOverride and no instance).
func (e *Engine) TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority string, departmentOverride string) (uuid.UUID, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// An omitted priority (MCP instance_trigger without the argument) means
	// normal; the store CHECKs priority and refuses an empty value. A
	// whitespace-only value is as absent as an empty one.
	if strings.TrimSpace(priority) == "" {
		priority = string(instance.PriorityNormal)
	}
	spec := triggerSpec{
		workflowName:       workflowName,
		variables:          variables,
		priority:           priority,
		departmentOverride: departmentOverride,
	}
	// The parent/child spawn link rides the context (Engine.Spawn sets it via
	// instance.WithSpawnLink). An INCOMPLETE link is ignored by trigger, exactly
	// as before.
	if l, ok := instance.SpawnLinkFromContext(ctx); ok {
		// Only StartGroupTask may supply the internal mode through triggerSpec:
		// a public trigger has no reserved roster row to receive its terminal.
		// Reject even partial group links instead of silently creating a root.
		if l.Mode == instance.SpawnModeGroupTask {
			return uuid.Nil, false, fmt.Errorf("trigger workflow %q: group_task mode requires StartGroupTask", workflowName)
		}
		spec.link = &l
	}
	return e.trigger(ctx, spec)
}

// triggerSpec is everything one run creation needs. TriggerWithDepartment
// builds it from its arguments and the context-carried spawn link;
// StartGroupTask builds it with a RESERVED instance id and a group-task link,
// so a workflow-kind group task is created through the identical
// owner / visibility / archive / input / stamping pipeline as any trigger.
type triggerSpec struct {
	workflowName       string
	variables          map[string]any
	priority           string
	departmentOverride string
	// instanceID is the id the run is created under; uuid.Nil mints a fresh one.
	instanceID uuid.UUID
	// link is the parent link to stamp; nil or incomplete creates a root run.
	link *instance.SpawnLink
}

// trigger is the body TriggerWithDepartment always had, parameterized by spec.
func (e *Engine) trigger(ctx context.Context, spec triggerSpec) (uuid.UUID, bool, error) {
	workflowName := spec.workflowName
	variables := spec.variables

	// Resolve the triggering owner: the context identity for REST/MCP triggers,
	// or the default user for system triggers (scheduler/webhook) that carry no
	// identity. The workflow is resolved across the owner's visible scopes
	// (private first, then shared) so a shared workflow runs directly with no
	// private copy.
	owner, ok := triggerOwner(ctx, e.users)
	if !ok {
		return uuid.Nil, false, fmt.Errorf("failed to resolve triggering user")
	}

	// Gate the shared read path by visibility. The owner above may be the default
	// system user; gating keys on the CONTEXT identity (a REST/MCP caller). A
	// system trigger (no context identity) skips gating — a scheduler/webhook
	// trigger already passed authorization at registration time.
	filter := e.sharedVisibilityFilter(ctx)
	wf, scope, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, filter)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to load workflow: %w", err)
	}

	// Refuse a parallel_group this server cannot execute (P1-C) BEFORE anything
	// is built, stamped, created or enqueued. It runs AFTER the visibility-gated
	// load so an invisible workflow still reports not-found. Every
	// instance-creating path — REST/MCP trigger, Rerun, Spawn, schedules —
	// funnels through this function, so this one check covers them all.
	if err := checkGroupSupport(wf, e.GroupSupportLevel()); err != nil {
		return uuid.Nil, false, err
	}

	// Refuse to trigger an archived workflow. IsArchived is part of the resolver
	// contract (no runtime type assertion), so the check engages whenever a
	// resolver is wired (the SQLite registry; local mode has no resolver and so
	// cannot — and need not — block). It runs AFTER the visibility-gated load so a
	// caller who cannot see the workflow still gets not-found (existence is not
	// leaked via the archived path), and BEFORE any instance is built so an
	// archived trigger creates nothing.
	//
	// FAIL CLOSED: the archived flag is a deliberate "do not run this" invariant,
	// so an IsArchived lookup error refuses the trigger rather than running an
	// possibly-archived workflow. A transient registry outage blocks the trigger
	// (surfaced loudly) instead of silently defeating the invariant.
	if e.resolver != nil {
		archived, aerr := e.resolver.IsArchived(ctx, workflowName)
		if aerr != nil {
			return uuid.Nil, false, fmt.Errorf("checking archived state of %q: %w", workflowName, aerr)
		}
		if archived {
			return uuid.Nil, false, fmt.Errorf("%w: %s", workflow.ErrArchived, workflowName)
		}
	}

	// Refuse a parent whose spawn nodes map inputs the published child does not
	// declare (an upgraded install can hold a new parent beside an old child)
	// BEFORE anything is stamped, created or enqueued: otherwise the run fails
	// at the spawn node, after every upstream stage has already been paid for.
	if err := e.preflightSpawnChildren(ctx, owner, wf, filter); err != nil {
		return uuid.Nil, false, err
	}

	// Stamp the run's audience from the resolved publication (private when the
	// workflow has no publication record), applying the validated per-trigger
	// department override. A system trigger (no context identity) yields a
	// zero-value identity whose Departments is nil, so a public/department run is
	// stamped with an empty attribution department. An invalid override fails the
	// trigger before any instance is created.
	vis, pubDept := e.resolvePublication(ctx, scope, workflowName)
	callerID, _ := identity.FromContext(ctx)
	st, stErr := resolveStamp(callerID, vis, pubDept, spec.departmentOverride)
	if stErr != nil {
		return uuid.Nil, false, stErr
	}

	// Emit admin.override when admin status was the SOLE reason the caller could
	// access the workflow. This consults the ONE canonical sole-reason predicate
	// (workflow.AdminOverrode) the read paths also use, so the override condition —
	// which keeps the publisher branch (an admin who PUBLISHED the workflow sees it
	// for a non-admin reason) — cannot drift from the read path (PR #21).
	// Best-effort and nil-safe: identical contract to all other audit paths.
	if e.auditRecorder != nil && e.triggerAdminOverrode(ctx, scope, workflowName, callerID) {
		e.auditRecorder.Record(ctx, AuditActionAdminOverride, workflowName, `{"action":"trigger"}`)
	}

	// Validate inputs.
	vars := variable.NewStore()
	for _, inp := range wf.Inputs {
		val, provided := variables[inp.Name]
		if !provided || val == nil {
			if inp.Required {
				return uuid.Nil, false, fmt.Errorf("missing required input: %s", inp.Name)
			}
			if inp.Default != nil {
				if err := vars.SetInput(inp.Name, inp.Default); err != nil {
					return uuid.Nil, false, fmt.Errorf("seed input default %q: %w", inp.Name, err)
				}
			}
		} else {
			if err := vars.SetInput(inp.Name, val); err != nil {
				return uuid.Nil, false, fmt.Errorf("seed input %q: %w", inp.Name, err)
			}
		}
	}

	// Also set any extra variables that don't match input definitions.
	// SECURITY: this path accepts arbitrary caller-supplied keys; SetInput's
	// reserved-name guard prevents writing to engine-seeded variables like
	// "workspace".
	for k, v := range variables {
		if _, exists := vars.Get(k); !exists {
			if err := vars.SetInput(k, v); err != nil {
				return uuid.Nil, false, fmt.Errorf("seed extra variable %q: %w", k, err)
			}
		}
	}

	// Snapshot the workflow definition as a JSON-serializable map.
	wfBytes, err := json.Marshal(wf)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to snapshot workflow: %w", err)
	}
	var wfSnapshot map[string]any
	if err := json.Unmarshal(wfBytes, &wfSnapshot); err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to parse workflow snapshot: %w", err)
	}

	// OwnerID is the triggering user resolved above (the context identity, or the
	// default user for system triggers). A shared workflow therefore runs in the
	// triggering user's queue, independent of the publisher.
	ownerID := owner

	now := time.Now()
	snap := vars.Snapshot()
	origVars := make(map[string]any, len(snap))
	for k, v := range snap {
		origVars[k] = v
	}

	// Stamp run provenance from the trigger context. Manual REST/MCP triggers
	// carry no origin and default to OriginManual; the two schedule trigger sites
	// (cron fireOne, run-now Runner.Fire) set it via instance.WithOrigin. The
	// engine reads it WITHOUT importing the schedule package, the same
	// one-directional edge identity uses.
	triggerSource, scheduleID := triggerOrigin(ctx)

	// Stamp the parent/child link from the spec. It is stamped into the SAME
	// Instance literal below — never as a follow-up write. This trigger path
	// creates AND enqueues, so a link written after enqueue could lose a race
	// with a fast child reaching a terminal state, leaving its waiting parent
	// with nothing to match. An INCOMPLETE link is ignored entirely rather than
	// partially persisted. A group-task link additionally carries the group run
	// and task key, stamped in the same literal for the same reason.
	var parentInstanceID *uuid.UUID
	var parentNodeID *string
	var spawnMode instance.SpawnMode
	var groupRunID *uuid.UUID
	var groupTaskKey *string
	var groupTaskKind instance.GroupTaskKind
	// spawnDepth stays 0 for a root run, which is exactly what a root's depth is.
	spawnDepth := 0
	if l := spec.link; l != nil && l.Valid() {
		p := l.ParentInstanceID
		n := l.ParentNodeID
		parentInstanceID = &p
		parentNodeID = &n
		spawnMode = l.Mode
		spawnDepth = l.SpawnDepth
		if l.Mode == instance.SpawnModeGroupTask {
			g := l.GroupRunID
			k := l.GroupTaskKey
			groupRunID = &g
			groupTaskKey = &k
			groupTaskKind = l.GroupTaskKind
		}
	}

	// Resolve the stable workflow id (name->id) so the run carries an identity
	// reference that survives a later rename/edit. The registry is authoritative;
	// when it is unwired or has no row for this name, fall back to the id carried
	// in the loaded definition (the lazy backfill stamps it onto the folder), or
	// uuid.Nil for a fully legacy workflow. Resolution NEVER fails the trigger —
	// WorkflowName + WorkflowSnapshot remain the display + execution authority.
	workflowID := wf.ID
	if e.resolver != nil {
		if rid, rerr := e.resolver.ResolveID(ctx, workflowName); rerr == nil {
			workflowID = rid
		}
	}

	// Pin the models BEFORE the instance exists: a version, snapshot or alias
	// failure must leave no queued row and make no provider call. The pins are
	// captured from the same workflow value that was just serialized into
	// wfSnapshot, so the pinned aliases are the aliases that will execute.
	modelPins, pinErr := e.captureModelPins(ctx, wf)
	if pinErr != nil {
		return uuid.Nil, false, pinErr
	}

	// A group task is created under the id its roster RESERVED before enqueue;
	// every other trigger mints a fresh one.
	id := spec.instanceID
	if id == uuid.Nil {
		id = uuid.New()
	}

	inst := &instance.Instance{
		ID:                       id,
		OwnerID:                  ownerID,
		WorkflowID:               workflowID,
		WorkflowName:             workflowName,
		WorkflowSnapshot:         wfSnapshot,
		Department:               st.Department,
		Visibility:               st.Visibility,
		TriggerSource:            triggerSource,
		ScheduleID:               scheduleID,
		ParentInstanceID:         parentInstanceID,
		ParentNodeID:             parentNodeID,
		SpawnMode:                spawnMode,
		SpawnDepth:               spawnDepth,
		GroupRunID:               groupRunID,
		GroupTaskKey:             groupTaskKey,
		GroupTaskKind:            groupTaskKind,
		ModelPins:                modelPins.Models,
		ModelPinRoutes:           modelPins.OpaqueRoutes,
		ModelPinRouteConnections: modelPins.OpaqueConnections,
		ModelPinsVersion:         instance.ModelPinsVersionSnapshot,
		State:                    instance.StateQueued,
		Priority:                 instance.Priority(spec.priority),
		Variables:                snap,
		OriginalVariables:        origVars,
		QueuedAt:                 &now,
		CreatedAt:                now,
		UpdatedAt:                now,
	}

	started, err := e.createAndEnqueue(ctx, inst)
	if err != nil {
		return uuid.Nil, false, err
	}
	return inst.ID, started, nil
}

// createAndEnqueue is the tail every run-creation path shares: persist the
// row, announce it, and hand it to the queue. The error strings are the ones
// TriggerWithDepartment has always returned.
func (e *Engine) createAndEnqueue(ctx context.Context, inst *instance.Instance) (bool, error) {
	if err := e.instances.Create(ctx, inst); err != nil {
		return false, fmt.Errorf("failed to create instance: %w", err)
	}

	e.eventBus.Publish(inst.ID, Event{
		Type:      EventTypeInstanceCreated,
		Timestamp: inst.CreatedAt,
		Data:      map[string]any{"workflowName": inst.WorkflowName},
	})

	started, err := e.queueManager.Enqueue(ctx, inst.ID, inst.OwnerID)
	if err != nil {
		return false, fmt.Errorf("failed to enqueue instance: %w", err)
	}
	return started, nil
}

// triggerOrigin reads the run provenance carried on ctx (instance.WithOrigin),
// defaulting to a manual trigger with no schedule.
func triggerOrigin(ctx context.Context) (instance.TriggerSource, *uuid.UUID) {
	if o, ok := instance.OriginFromContext(ctx); ok {
		return o.Source, o.ScheduleID
	}
	return instance.OriginManual, nil
}

// ListInstances returns the instances visible to the caller. The owner filter
// is derived from the request identity: a non-admin sees only their own
// instances; an admin sees all. Any OwnerID the caller set on filters is
// overwritten here so scope cannot be widened from request input. A request
// with no identity returns ErrNotFound (reveal nothing) and never queries the
// repository.
func (e *Engine) ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	lc, ok := listConstraint(ctx)
	if !ok {
		return nil, instance.ErrNotFound // no identity: reveal nothing
	}
	return e.instances.List(ctx, applyListConstraint(filters, lc))
}

// applyListConstraint overwrites every visibility field on filters from the
// service-derived scope, discarding whatever the caller supplied.
//
// The admin arm sets Unconstrained EXPLICITLY rather than clearing the
// narrowing and letting the repository infer "no filters means admin". That
// inference was the divergence: the repository's List read a scope-less filter
// set as an admin list and returned the full table, while the identical input
// to childVisibilityClause was read as "no scope" and returned nothing. One of
// those two is a data leak, and it is not the fail-closed one.
func applyListConstraint(filters instance.ListFilters, lc listFilter) instance.ListFilters {
	if lc.Unconstrained {
		filters.Unconstrained = true
		filters.OwnerID = nil
		filters.Departments = nil
		filters.IncludePublic = false
		return filters
	}
	filters.Unconstrained = false
	filters.OwnerID = lc.OwnerID
	filters.Departments = lc.Departments
	filters.IncludePublic = lc.IncludePublic
	return filters
}

// ListEnriched returns the instances visible to the caller with the additive
// enriched fields composed (run name, submitter, friendly step, model,
// backend, retry count, queue position). Authorization is identical to
// ListInstances (the same service-set filters); enrichment runs only on the
// authorized page. A request with no identity returns ErrNotFound, exactly
// like ListInstances. When no enricher is wired, rows carry the run name only
// (derived from the lean summary) so the endpoint still works.
func (e *Engine) ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
	lc, ok := listConstraint(ctx)
	if !ok {
		return nil, instance.ErrNotFound
	}
	summaries, err := e.instances.List(ctx, applyListConstraint(filters, lc))
	if err != nil {
		return nil, err
	}
	if e.enricher == nil {
		out := make([]instance.EnrichedInstanceSummary, len(summaries))
		for i, s := range summaries {
			out[i] = instance.EnrichedInstanceSummary{
				InstanceSummary: s,
				RunName:         instance.DeriveRunName(s.WorkflowName, nil, nil, s.ID),
			}
		}
		return out, nil
	}
	return e.enricher.Enrich(ctx, summaries)
}

// GetInstance loads an instance the caller may view, else ErrNotFound. Both an
// absent instance and a not-owned instance collapse to ErrNotFound so existence
// is never leaked to a non-owner non-admin.
func (e *Engine) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return nil, err // already ErrNotFound-wrapped on absence; infra errors pass through
	}
	if inst == nil {
		// Defensive: a repo that returns (nil, nil) is treated as absence so the
		// caller never dereferences a nil instance and existence is not leaked.
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	if !canView(ctx, inst) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return inst, nil
}

// authorizeView is the shared guard for the sub-resource reads (trace,
// variables, history): it loads the owning instance and returns ErrNotFound if
// the caller may not view it. It returns the loaded instance for callers that
// need its fields (e.g. GetVariables). Sub-resource repos are only touched after
// this guard passes, so a non-viewer never causes a node-execution or snapshot
// read.
func (e *Engine) authorizeView(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	return e.GetInstance(ctx, id)
}

// authorizeIntervention loads the instance and confirms the caller may MUTATE
// it (owner, a lead of the run's department, or an admin). It returns the loaded
// instance so the caller reuses it (avoiding a second Get).
//
// A caller with no view access gets ErrNotFound so a write probe cannot
// distinguish "exists but forbidden" from "absent" (existence not leaked). A
// caller who CAN view but cannot intervene — a department peer — gets
// ErrForbidden (403): they already see the run in their list, so existence is
// not a secret, but a control action is denied.
func (e *Engine) authorizeIntervention(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance: %w", err)
	}
	if inst == nil {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	// No view access => hide existence (404), preserving the non-leak posture.
	if !canView(ctx, inst) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	// Viewable but not owner/lead/admin (a department peer) => 403, not 404.
	if !canIntervene(ctx, inst) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
	}
	return inst, nil
}

// GetTrace returns the ordered node executions for an instance the caller may
// view, else ErrNotFound.
func (e *Engine) GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.nodeExecutions.ListByInstance(ctx, id)
}

// GetNodeTrace returns a single node execution for an instance the caller may
// view, else ErrNotFound.
func (e *Engine) GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.nodeExecutions.GetByInstanceAndNode(ctx, id, nodeID)
}

// GetVariables returns the current variables for an instance the caller may
// view, else ErrNotFound.
func (e *Engine) GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error) {
	inst, err := e.authorizeView(ctx, id)
	if err != nil {
		return nil, err
	}
	return inst.Variables, nil
}

// GetVariableHistory returns the variable snapshot history for an instance the
// caller may view, else ErrNotFound.
func (e *Engine) GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.snapshots.ListByInstance(ctx, id)
}

// GetInterventions returns the intervention audit trail for an instance the
// caller may view (owner or admin), else ErrNotFound. When no intervention
// repository is wired it returns an empty list.
func (e *Engine) GetInterventions(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	if e.interventions == nil {
		return []instance.Intervention{}, nil
	}
	return e.interventions.ListByInstance(ctx, id)
}

// StartInstance transitions a queued instance to running and launches the
// graph walker goroutine. It implements queue.InstanceStarter.
func (e *Engine) StartInstance(ctx context.Context, id uuid.UUID) error {
	if err := e.instances.UpdateState(ctx, id, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to transition to running: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[id] = cancel
	e.mu.Unlock()

	e.eventBus.Publish(id, Event{
		Type:      "instance.started",
		Timestamp: time.Now(),
		Data:      map[string]any{},
	})

	e.wg.Add(1)
	go e.run(runCtx, id)

	return nil
}

// run is the graph walker loop. It executes nodes sequentially following
// edges until there are no more nodes to execute.
func (e *Engine) run(ctx context.Context, instanceID uuid.UUID) {
	defer e.wg.Done()

	// capacityFreed tracks whether capacity notification was already handled
	// by an executor (e.g., timer/webhook wait) or an external caller (pause/cancel).
	// The deferred cleanup only fires NotifyCapacityFreed if this remains false.
	capacityFreed := false

	// workflowName is set after the instance is loaded; the panic deferred function
	// reads it via closure so notifications carry the correct workflow name.
	var workflowName string

	defer func() {
		if r := recover(); r != nil {
			// A panic in the run loop should not crash the process.
			// Mark the instance as failed and log via event bus.
			if e.failInstance(ctx, instanceID, "") {
				e.emitNotification(context.Background(), instanceID, workflowName, notification.TypeFailed)
			}
			e.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInstanceFailed,
				Timestamp: time.Now(),
				Data:      map[string]any{"error": fmt.Sprintf("panic: %v", r)},
			})
			capacityFreed = false // ensure we notify below
		}
		if !capacityFreed {
			e.queueManager.NotifyCapacityFreed(context.Background())
		}
	}()
	defer func() {
		e.mu.Lock()
		delete(e.contexts, instanceID)
		e.mu.Unlock()
	}()

	inst, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		// A corrupt pin record (or another load fault) prevents execution, but
		// returning alone strands the row in running and holds queue capacity.
		// SetFailed does not need to decode the row. Keep the corrupt pins intact
		// for diagnosis, release cached pins through the normal terminal hook,
		// and surface the load error. Cancellation belongs to its caller.
		slog.Error("failed to load running instance", "instanceID", instanceID, "error", err)
		if ctx.Err() == nil && e.failInstance(ctx, instanceID, "") {
			e.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInstanceFailed,
				Timestamp: time.Now(),
				Data:      map[string]any{"error": fmt.Sprintf("failed to load instance: %v", err)},
			})
		}
		return
	}
	workflowName = inst.WorkflowName

	// Carry the instance owner into the run context so file-reading executors
	// resolve workflow files from the owner's private workspace (with a shared
	// fallback) via the scope-aware FileReader.
	ctx = withOwner(ctx, inst.OwnerID)

	// Parse the workflow from the snapshot.
	wf, nodeMap, adj, parseErr := parseWorkflowGraph(inst.WorkflowSnapshot)
	if parseErr != nil {
		e.failInstance(ctx, instanceID, "")
		return
	}

	// Find start node.
	var currentNodeID string
	resuming := false
	if inst.CurrentNodeID != nil && *inst.CurrentNodeID != "" {
		// Resuming from a specific node.
		currentNodeID = *inst.CurrentNodeID
		resuming = true
	} else {
		// Find the entrypoint.
		for _, n := range wf.Nodes {
			if n.Entrypoint {
				currentNodeID = n.ID
				break
			}
		}
	}

	if currentNodeID == "" {
		e.failInstance(ctx, instanceID, "")
		return
	}

	// Reconstruct variable store using node IDs so object-valued inputs are not
	// misclassified as node outputs.
	nodeIDs := make(map[string]bool, len(nodeMap))
	for id := range nodeMap {
		nodeIDs[id] = true
	}
	vars := variable.FromSnapshotWithNodeIDs(inst.Variables, nodeIDs)

	if e.workspaceManager != nil {
		if !e.prepareWorkspace(ctx, instanceID, currentNodeID, vars) {
			return
		}
	}

	// A finalization failure is recorded against the last node executed.
	var lastNodeID string

	// A run woken from a finished wait restarts at the wait node itself. Complete
	// that node and move on; executing it again would arm a fresh wait forever.
	if resuming {
		if node, ok := nodeMap[currentNodeID]; ok {
			result, done, werr := e.consumeFinishedWait(ctx, inst, node)
			if werr != nil {
				slog.Error("failed to complete finished wait", "instanceID", instanceID, "nodeID", currentNodeID, "error", werr)
				e.failInstance(ctx, instanceID, currentNodeID)
				return
			}
			if done {
				lastNodeID = currentNodeID
				nextNodeID, halt := e.handleNodeSuccess(ctx, instanceID, workflowName, currentNodeID, node, result, vars, adj, &capacityFreed)
				if halt {
					return
				}
				currentNodeID = nextNodeID
			}
		}
	}

	for currentNodeID != "" {
		// TODO(race): There is a TOCTOU race between Pause() writing StatePaused
		// and this loop reading the fresh state. If the pause arrives between Get()
		// and the next node execution, one extra node may execute. Fix: use
		// "UPDATE ... SET state = 'paused' WHERE state = 'running' RETURNING ..."
		// and check the affected row count to detect concurrent state changes.

		// Check for pause/cancel by re-reading instance state.
		freshInst, err := e.instances.Get(ctx, instanceID)
		if err != nil {
			e.failInstance(ctx, instanceID, currentNodeID)
			return
		}

		if freshInst.State == instance.StatePaused || freshInst.State == instance.StateCancelled {
			// The instance was paused/cancelled externally. Stop execution.
			// The caller already called NotifyCapacityFreed.
			capacityFreed = true
			// Pause lets the in-flight node finish, so currentNodeID is now the
			// NEXT node. Record it: Resume restarts at current_node_id, and a
			// stale value would run the finished node a second time.
			if freshInst.State == instance.StatePaused &&
				(freshInst.CurrentNodeID == nil || *freshInst.CurrentNodeID != currentNodeID) {
				if err := e.instances.UpdateCurrentNode(ctx, instanceID, currentNodeID); err != nil {
					slog.Error("failed to record next node for paused instance", "instanceID", instanceID, "nodeID", currentNodeID, "error", err)
				}
			}
			return
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			capacityFreed = true
			return
		default:
		}

		node, exists := nodeMap[currentNodeID]
		if !exists {
			e.failInstance(ctx, instanceID, currentNodeID)
			return
		}

		// Snapshot variables before execution (diagnostic; log-only on error).
		if err := e.snapshots.Create(ctx, &instance.VariableSnapshot{
			ID:         uuid.New(),
			InstanceID: instanceID,
			NodeID:     currentNodeID,
			Variables:  vars.Snapshot(),
			CapturedAt: time.Now(),
		}); err != nil {
			slog.Warn("failed to create variable snapshot", "instanceID", instanceID, "nodeID", currentNodeID, "error", err)
		}

		// Update current node.
		if err := e.instances.UpdateCurrentNode(ctx, instanceID, currentNodeID); err != nil {
			e.failInstance(ctx, instanceID, currentNodeID)
			return
		}

		// The queue live-patches its row from this event, so it carries the same
		// binding and git ref a list refetch would compute (resolveNodeBinding /
		// deriveGitRef are shared with the enricher).
		// Resolved inside the run context so a pinning resolver reports the model
		// the node will execute on, not the profile's current one.
		execCtx := ContextWithInstanceID(ctx, instanceID)
		nb, nbErr := resolveNodeBinding(execCtx, e.profiles, node, e.defaultAgentBackend)
		if nbErr != nil {
			slog.Warn("node.started: profile resolve failed; event shows node config", "instanceID", instanceID, "nodeID", currentNodeID, "error", nbErr)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeNodeStarted,
			Timestamp: time.Now(),
			Data: map[string]any{
				"nodeId": currentNodeID, "nodeType": nb.NodeType, "nodeLabel": nodeLabel(node),
				"model": nb.Model, "effort": nb.Effort, "backend": nb.Backend, "provider": nb.Provider,
				"gitRef": deriveGitRef(wf.GitVariable, vars.Snapshot()),
			},
		})

		// Execute the node with retry logic. Each attempt is individually recorded.
		lastNodeID = currentNodeID
		result, nodeExec, execErr := e.executeWithRetry(execCtx, instanceID, node, vars, *wf)

		if execErr != nil {
			nextNodeID, halt := e.handleNodeError(ctx, instanceID, workflowName, currentNodeID, node, *wf, nodeExec, execErr, vars, adj)
			if halt {
				return
			}
			currentNodeID = nextNodeID
		} else {
			nextNodeID, halt := e.handleNodeSuccess(ctx, instanceID, workflowName, currentNodeID, node, result, vars, adj, &capacityFreed)
			if halt {
				return
			}
			currentNodeID = nextNodeID
		}
	}

	// Finalize group files before publishing success or removing the workspace.
	if !e.finalizeGroupTask(ctx, inst, workflowName, lastNodeID) {
		return
	}
	if err := e.instances.SetCompleted(ctx, instanceID); err != nil {
		slog.Error("failed to set instance completed", "instanceID", instanceID, "error", err)
		// Persistence failed: keep the workspace for debugging/retry. Log
		// the retention with size/file-count so the operator can see how
		// much sensitive data is sitting on disk.
		e.warnWorkspaceRetained(instanceID, "set_completed_failed")
	} else {
		e.emitNotification(ctx, instanceID, workflowName, notification.TypeCompleted)
		// Keep workspaces still needed by gathering or active descendants.
		if e.retainGroupTaskWorkspace(ctx, inst) {
			e.warnWorkspaceRetained(instanceID, "group_task_retained")
		} else {
			e.cleanupWorkspace(instanceID)
		}
		// A completed run may be somebody's awaited child.
		e.OnChildTerminal(context.Background(), instanceID)
	}
	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInstanceCompleted,
		Timestamp: time.Now(),
	})
}

// failInstance marks an instance as failed and logs errors. It consolidates
// the repeated SetFailed + slog.Error pattern used throughout the run loop.
// It returns true if the state change succeeded, false otherwise.
//
// SF9: on failure the workspace is intentionally NOT removed — it is retained
// for debugging (the success path in run() removes it after SetCompleted). A
// slog.Warn with size/file-count is emitted so the sensitive-data-retention
// surface is visible to operators.
func (e *Engine) failInstance(ctx context.Context, instanceID uuid.UUID, nodeID string) bool {
	if err := e.instances.SetFailed(ctx, instanceID, nodeID); err != nil {
		slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
		return false
	}
	e.warnWorkspaceRetained(instanceID, "instance_failed")
	// A failed run may be somebody's awaited child. Use a background context: the
	// run context is often already cancelled at this point.
	e.OnChildTerminal(context.Background(), instanceID)
	return true
}

// FailCorruptRun drives a run whose stored model pin record cannot be decoded
// (reported by a queue-admission or recovery read as an
// instance.CorruptPinsError) through the same complete terminal path run takes
// after a failed load: SetFailed (which does not decode pins), the
// child-terminal hook (pin-cache release, parent wake), an instance.failed
// event and a queue-capacity notification. The corrupt record is kept for
// diagnosis.
func (e *Engine) FailCorruptRun(ctx context.Context, id uuid.UUID) error {
	return e.failCorruptRun(ctx, id, true)
}

func (e *Engine) failCorruptRun(ctx context.Context, id uuid.UUID, notifyCapacity bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !e.failInstance(ctx, id, "") {
		return fmt.Errorf("engine: could not fail run %s with corrupt model pins", id)
	}
	slog.Error("instance failed: corrupt model pins", "instanceID", id)
	e.eventBus.Publish(id, Event{
		Type:      EventTypeInstanceFailed,
		Timestamp: time.Now(),
		Data:      map[string]any{"error": "corrupt model pins: the stored pin record cannot be decoded"},
	})
	if notifyCapacity {
		e.queueManager.NotifyCapacityFreed(context.Background())
	}
	return nil
}

// handleNodeError processes a node execution failure according to the resolved
// error behavior. It returns the next node ID to execute and whether the run
// loop should halt.
func (e *Engine) handleNodeError(
	ctx context.Context,
	instanceID uuid.UUID,
	workflowName string,
	currentNodeID string,
	node workflow.Node,
	wf workflow.Workflow,
	nodeExec *instance.NodeExecution,
	execErr error,
	vars *variable.VariableStore,
	adj map[string][]edgeTarget,
) (nextNodeID string, halt bool) {
	// Cancellation race: when the run context is cancelled (e.g. via Cancel()),
	// the executing node returns context.Canceled. That is a cancellation, NOT a
	// node failure — marking the instance Failed here would clobber the Cancelled
	// state Cancel() set. Halt without changing state; the cancel path owns it.
	if ctx.Err() != nil || errors.Is(execErr, context.Canceled) {
		return "", true
	}

	flabel, fbackend := nodeLabel(node), firstNonEmpty(node.Config.Backend, e.defaultAgentBackend)
	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeNodeFailed,
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type), "nodeLabel": flabel, "backend": fbackend, "error": execErr.Error()},
	})

	var pendingGather *pendingFileGatherError
	if errors.As(execErr, &pendingGather) {
		// Gathering may have changed files before a storage/verification error.
		// Keep the cursor here and let the gather reconciler wake the parent;
		// skip/route/retry must never continue with an unfrozen gather output.
		// The suspension is a running->waiting CAS: Cancel commits `cancelled`
		// before it cancels ctx, so the check above can miss it, and an
		// unconditional write would revive the parent for the reconciler to wake.
		// Detached: a cancellation landing now must lose the CAS, not fail it.
		slog.Warn("file gather interrupted; suspending for reconciliation", "instanceID", instanceID, "nodeID", currentNodeID, "error", execErr)
		suspended, err := pendingGather.suspend(context.WithoutCancel(ctx))
		switch {
		case err != nil:
			slog.Error("failed to suspend interrupted file gather", "instanceID", instanceID, "error", err)
			e.failInstance(ctx, instanceID, currentNodeID)
		case !suspended:
			slog.Info("interrupted file gather: the parent left running before it could be suspended; its new state stands",
				"instanceID", instanceID, "nodeID", currentNodeID)
		}
		return "", true
	}

	var pendingWait *pendingGroupWaitError
	if errors.As(execErr, &pendingWait) {
		// The fixed roster is already running. Preserve the cursor and release
		// capacity; terminal callbacks or the group sweep will wake this node.
		// If storage still refuses waiting, halt as failed rather than routing
		// past the group. The diagnostic execution retains the original error.
		slog.Warn("parallel group wait interrupted; suspending for reconciliation", "instanceID", instanceID, "nodeID", currentNodeID, "error", execErr)
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
			slog.Error("failed to suspend interrupted parallel group", "instanceID", instanceID, "error", err)
			e.failInstance(ctx, instanceID, currentNodeID)
		}
		return "", true
	}

	eb := resolveErrorBehavior(node, wf)

	switch eb.Type {
	case "skip":
		if nodeExec != nil {
			nodeExec.Status = instance.NodeStatusSkipped
			_ = e.nodeExecutions.Update(ctx, nodeExec)
		}
		if eb.DefaultOutput != nil {
			vars.SetNodeOutput(currentNodeID, eb.DefaultOutput)
		}
		if err := e.instances.UpdateVariables(ctx, instanceID, vars.Snapshot()); err != nil {
			e.failInstance(ctx, instanceID, currentNodeID)
			return "", true
		}
		// Conditional nodes route via their declared default output port so
		// the skip path is deterministic rather than arbitrary-first-edge.
		skipPort := ""
		if node.Type == workflow.NodeTypeConditional {
			skipPort = node.Config.DefaultOutput
		}
		return e.findNextNode(currentNodeID, skipPort, adj), false

	case "route":
		if eb.ErrorNodeID != "" {
			return eb.ErrorNodeID, false
		}
		e.failInstance(ctx, instanceID, currentNodeID)
		return "", true

	default: // "halt" or unrecognized
		if e.failInstance(ctx, instanceID, currentNodeID) {
			e.emitNotification(ctx, instanceID, workflowName, notification.TypeFailed)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeInstanceFailed,
			Timestamp: time.Now(),
			Data:      map[string]any{"failedNodeId": currentNodeID, "error": execErr.Error()},
		})
		return "", true
	}
}

// handleNodeSuccess processes a successful node execution, updating variables
// and determining the next node. It returns the next node ID and whether the
// run loop should halt (e.g., for wait results).
func (e *Engine) handleNodeSuccess(
	ctx context.Context,
	instanceID uuid.UUID,
	workflowName string,
	currentNodeID string,
	node workflow.Node,
	result *NodeResult,
	vars *variable.VariableStore,
	adj map[string][]edgeTarget,
	capacityFreed *bool,
) (nextNodeID string, halt bool) {
	// Check if this is a wait result (timer delay, webhook wait, etc.)
	if result != nil && result.IsWait {
		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeNodeCompleted,
			Timestamp: time.Now(),
			Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
		})
		if workflow.IsInteractive(node.Type) {
			e.emitNotification(ctx, instanceID, workflowName, notification.TypeNeedsInteraction)
		}
		*capacityFreed = true
		return "", true
	}

	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeNodeCompleted,
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
	})

	// Update variables with node output.
	if result != nil && result.Output != nil {
		vars.SetNodeOutput(currentNodeID, result.Output)
	}
	if err := e.instances.UpdateVariables(ctx, instanceID, vars.Snapshot()); err != nil {
		e.failInstance(ctx, instanceID, currentNodeID)
		return "", true
	}

	// Find next node.
	outputPort := ""
	if result != nil {
		outputPort = result.OutputPort
	}
	return e.findNextNode(currentNodeID, outputPort, adj), false
}

// executeWithRetry executes a node, applying retry logic from the error behavior.
// A NodeExecution record is created for each attempt. The final record is returned
// so the caller can update it (e.g., marking it skipped).
func (e *Engine) executeWithRetry(ctx context.Context, instanceID uuid.UUID, node workflow.Node, vars *variable.VariableStore, wf workflow.Workflow) (*NodeResult, *instance.NodeExecution, error) {
	executor, ok := e.executorFor(node.Type)
	if !ok {
		return nil, nil, fmt.Errorf("no executor registered for node type: %s", node.Type)
	}

	eb := resolveErrorBehavior(node, wf)
	maxAttempts := 1
	if eb.Type == "retry" && eb.Retries > 0 {
		maxAttempts = eb.Retries + 1
	}

	var lastErr error
	var lastExec *instance.NodeExecution

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 && eb.RetryDelaySeconds > 0 {
			select {
			case <-ctx.Done():
				return nil, lastExec, ctx.Err()
			case <-time.After(time.Duration(eb.RetryDelaySeconds) * time.Second):
			}
		}

		resolvedInputs := vars.Snapshot()
		startedAt := time.Now()
		result, err := executor.Execute(ctx, node, vars, wf.Name)
		completedAt := time.Now()

		execStatus := instance.NodeStatusSucceeded
		var execError map[string]any
		if err != nil {
			execStatus = instance.NodeStatusFailed
			execError = map[string]any{"error": err.Error()}
		}

		var execOutput map[string]any
		if result != nil && result.Output != nil {
			if m, ok := result.Output.(map[string]any); ok {
				execOutput = m
			} else {
				execOutput = map[string]any{"value": result.Output}
			}
		}

		nodeExec := &instance.NodeExecution{
			ID:             uuid.New(),
			InstanceID:     instanceID,
			NodeID:         node.ID,
			Attempt:        attempt,
			Status:         execStatus,
			ResolvedInputs: resolvedInputs,
			Output:         execOutput,
			Error:          execError,
			StartedAt:      startedAt,
			CompletedAt:    &completedAt,
		}
		nodeResultToExecution(result, nodeExec)
		// Skip writing when the executor itself already wrote the record (IsWait path).
		if result == nil || !result.IsWait {
			_ = e.nodeExecutions.Create(ctx, nodeExec)
		}
		lastExec = nodeExec

		if err == nil {
			return result, nodeExec, nil
		}
		var pendingGather *pendingFileGatherError
		var pendingWait *pendingGroupWaitError
		if errors.As(err, &pendingGather) || errors.As(err, &pendingWait) {
			return nil, nodeExec, err
		}
		lastErr = err
	}

	return nil, lastExec, lastErr
}

// nodeResultToExecution maps optional NodeResult fields onto a NodeExecution
// record. This consolidates the repeated field-copying logic from executeWithRetry.
func nodeResultToExecution(result *NodeResult, exec *instance.NodeExecution) {
	if result == nil {
		return
	}
	// LLMConversation is typed as any in NodeResult because different LLM
	// providers return different shapes. Only map[string]any values are stored;
	// non-map values (e.g., from a provider returning a typed struct that was
	// not JSON-round-tripped) are silently dropped since the DB column is JSONB.
	if result.LLMConversation != nil {
		if m, ok := result.LLMConversation.(map[string]any); ok {
			exec.LLMConversation = m
		}
	}
	if result.LLMModel != "" {
		exec.LLMModel = &result.LLMModel
	}
	if result.LLMTokensIn > 0 {
		exec.LLMTokensIn = &result.LLMTokensIn
	}
	if result.LLMTokensOut > 0 {
		exec.LLMTokensOut = &result.LLMTokensOut
	}
	if result.ScriptStdout != "" {
		exec.ScriptStdout = &result.ScriptStdout
	}
	if result.ScriptStderr != "" {
		exec.ScriptStderr = &result.ScriptStderr
	}
	if result.ScriptExitCode != 0 {
		exec.ScriptExitCode = &result.ScriptExitCode
	}
}

// emitNotification creates a notification and publishes a notification.created event.
// It is a no-op if the notification repository has not been wired.
//
// TODO: if a 4th notification type is added, or routing logic becomes necessary,
// extract a dedicated NotificationListener that subscribes to the event bus
// instead of embedding this logic in Engine.
func (e *Engine) emitNotification(ctx context.Context, instanceID uuid.UUID, workflowName string, notifType notification.Type) {
	if e.notifications == nil {
		return
	}
	n := &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   instanceID,
		WorkflowName: workflowName,
		Type:         notifType,
		CreatedAt:    time.Now(),
	}
	if err := e.notifications.Create(ctx, n); err != nil {
		slog.Error("failed to create notification", "instanceID", instanceID, "error", err)
		return
	}
	e.publishNotification(ctx, n)
}

func (e *Engine) publishNotification(ctx context.Context, n *notification.Notification) {
	e.eventBus.Publish(n.InstanceID, Event{
		Type:      "notification.created",
		Timestamp: time.Now(),
		Data:      notificationEventData(n),
	})
	if e.externalNotifier != nil {
		if err := e.externalNotifier.Notify(ctx, n); err != nil {
			slog.Error("external notifier failed", "instanceID", n.InstanceID, "error", err)
		}
	}
}

// notificationEventData converts a notification into the map shape expected
// by the frontend's NotificationItem interface.
func notificationEventData(n *notification.Notification) map[string]any {
	return map[string]any{
		"id":           n.ID.String(),
		"instanceId":   n.InstanceID.String(),
		"workflowName": n.WorkflowName,
		"type":         string(n.Type),
		"read":         n.Read,
		"dismissed":    n.Dismissed,
		"createdAt":    n.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

// parseWorkflowGraph deserializes a workflow snapshot into its definition,
// a node lookup map, and an adjacency list. This is a pure function with no
// side effects, making it straightforward to test in isolation.
func parseWorkflowGraph(snapshot map[string]any) (*workflow.Workflow, map[string]workflow.Node, map[string][]edgeTarget, error) {
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing workflow snapshot: %w", err)
	}

	nodeMap := make(map[string]workflow.Node, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodeMap[n.ID] = n
	}

	adj := make(map[string][]edgeTarget)
	for _, edge := range wf.Edges {
		adj[edge.Source] = append(adj[edge.Source], edgeTarget{
			nodeID:     edge.Target,
			outputPort: edge.Output,
		})
	}

	return &wf, nodeMap, adj, nil
}

// resolveErrorBehavior returns the effective error behavior for a node,
// falling back to the workflow default, then to "halt".
func resolveErrorBehavior(node workflow.Node, wf workflow.Workflow) workflow.ErrorBehavior {
	if node.ErrorBehavior.Type != "" {
		return node.ErrorBehavior
	}
	if wf.DefaultErrorBehavior.Type != "" {
		return wf.DefaultErrorBehavior
	}
	return workflow.ErrorBehavior{Type: "halt"}
}

// findNextNode finds the next node to execute based on the current node's
// output port and the adjacency map.
func (e *Engine) findNextNode(currentNodeID string, outputPort string, adj map[string][]edgeTarget) string {
	targets := adj[currentNodeID]
	if len(targets) == 0 {
		return ""
	}

	// If there's an output port, find the matching edge.
	if outputPort != "" {
		for _, t := range targets {
			if t.outputPort == outputPort {
				return t.nodeID
			}
		}
		// No matching edge found for the output port.
		return ""
	}

	// No output port specified: take the first edge with no output port,
	// or the first edge if all have output ports.
	for _, t := range targets {
		if t.outputPort == "" {
			return t.nodeID
		}
	}
	// Fallback: take the first edge.
	return targets[0].nodeID
}

// Pause sets a running or waiting instance to paused state, then pauses every
// active descendant it spawned (see pause_cascade.go). The graph walker will
// detect this on its next iteration and stop. For waiting instances, the timer
// keeps counting (do NOT cancel the scheduler entry).
//
// An instance that is already paused is not an error when the call has an
// effect: if a parent's cascade paused it, the operator now owns the pause
// (the marker is cleared so resuming the parent leaves it paused); otherwise
// its still-active descendants are swept. With neither, ErrNotRunning stands.
func (e *Engine) Pause(ctx context.Context, instanceID uuid.UUID) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst.State == instance.StatePaused {
		return e.repause(ctx, inst)
	}
	if err := e.pauseOwn(ctx, inst); err != nil {
		return err
	}
	e.cascadePause(context.WithoutCancel(ctx), instanceID, instanceID, map[uuid.UUID]bool{instanceID: true})
	return nil
}

// repause handles Pause on an already-paused run.
func (e *Engine) repause(ctx context.Context, inst *instance.Instance) error {
	by, err := e.spawnLinks.CascadePausedBy(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("reading cascade pause of %s: %w", inst.ID, err)
	}
	if by != nil {
		if err := e.spawnLinks.ClearCascadePause(ctx, inst.ID); err != nil {
			return fmt.Errorf("claiming pause of %s: %w", inst.ID, err)
		}
		e.recordIntervention(ctx, inst, instance.ActionPause, nil)
		return nil
	}
	if e.cascadePause(context.WithoutCancel(ctx), inst.ID, inst.ID, map[uuid.UUID]bool{inst.ID: true}) == 0 {
		return fmt.Errorf("instance %s is not running (state: %s): %w", inst.ID, inst.State, instance.ErrNotRunning)
	}
	return nil
}

// pauseOwn pauses the named run itself.
func (e *Engine) pauseOwn(ctx context.Context, inst *instance.Instance) error {
	instanceID := inst.ID
	switch inst.State {
	case instance.StateRunning:
		if err := e.instances.UpdateState(ctx, instanceID, instance.StatePaused); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.paused",
			Timestamp: time.Now(),
		})
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StateWaiting:
		// Transition to paused — timer keeps counting (do NOT cancel scheduler entry).
		if err := e.instances.UpdateState(ctx, instanceID, instance.StatePaused); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.paused",
			Timestamp: time.Now(),
		})

	default:
		return fmt.Errorf("instance %s is not running (state: %s): %w", instanceID, inst.State, instance.ErrNotRunning)
	}
	e.recordIntervention(ctx, inst, instance.ActionPause, nil)
	return nil
}

// Resume sets a paused instance back to its appropriate state. If the instance
// was paused from a waiting state (WaitingUntil is set), it checks whether the
// timer has expired:
//   - Timer expired: transition to queued, trigger queue evaluation
//   - Timer not expired: transition back to waiting
//
// If the instance was paused from running (WaitingUntil is nil), resume normally.
func (e *Engine) Resume(ctx context.Context, instanceID uuid.UUID) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst.State != instance.StatePaused {
		return fmt.Errorf("instance %s is not paused (state: %s): %w", instanceID, inst.State, instance.ErrNotPaused)
	}

	// A direct resume ends any cascade claim on this run: a later resume of the
	// ancestor that paused it must not act on it again.
	if err := e.spawnLinks.ClearCascadePause(ctx, instanceID); err != nil {
		return fmt.Errorf("clearing cascade pause of %s: %w", instanceID, err)
	}
	if err := e.resumePaused(ctx, inst); err != nil {
		return err
	}
	e.recordIntervention(ctx, inst, instance.ActionResume, nil)
	e.cascadeResume(context.WithoutCancel(ctx), instanceID, instanceID, map[uuid.UUID]bool{instanceID: true})
	return nil
}

// resumePaused moves a paused run back to the state its wait implies. It does
// no authorization and records no intervention; callers do both.
func (e *Engine) resumePaused(ctx context.Context, inst *instance.Instance) error {
	switch {
	case inst.WebhookToken != nil:
		return e.resumeFromWebhookWait(ctx, inst.ID, inst)
	case inst.WaitingUntil != nil:
		return e.resumeFromTimerWait(ctx, inst.ID, inst)
	default:
		return e.resumeFromRunning(ctx, inst.ID)
	}
}

// resumeFromWebhookWait handles resume for instances paused during a webhook wait.
func (e *Engine) resumeFromWebhookWait(ctx context.Context, instanceID uuid.UUID, inst *instance.Instance) error {
	// Webhook received while paused -> go to queued.
	if inst.WebhookReceivedAt != nil {
		now := time.Now()
		if err := e.queueRepo.SetQueued(ctx, instanceID, now); err != nil {
			return fmt.Errorf("failed to set queued: %w", err)
		}
		e.publishResumedEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())
		return nil
	}

	// Webhook timeout expired while paused -> fail.
	if inst.WaitingUntil != nil && time.Now().After(*inst.WaitingUntil) {
		if e.webhookRegistry != nil {
			e.webhookRegistry.Deactivate(*inst.WebhookToken)
		}
		failedNodeID := ""
		if inst.CurrentNodeID != nil {
			failedNodeID = *inst.CurrentNodeID
		}
		if err := e.instances.SetFailed(ctx, instanceID, failedNodeID); err != nil {
			return fmt.Errorf("failed to fail expired webhook instance on resume: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeInstanceFailed,
			Timestamp: time.Now(),
			Data:      map[string]any{"reason": "webhook timeout expired while paused"},
		})
		// A failed run may be somebody's awaited child.
		e.OnChildTerminal(context.Background(), instanceID)
		return nil
	}

	// No webhook received and timeout not expired -> go back to waiting.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}
	e.publishResumedEvent(instanceID)
	return nil
}

// resumeFromTimerWait handles resume for instances paused during a timer delay.
func (e *Engine) resumeFromTimerWait(ctx context.Context, instanceID uuid.UUID, inst *instance.Instance) error {
	// Timer expired while paused -> go to queued.
	if time.Now().After(*inst.WaitingUntil) {
		now := time.Now()
		// Keep waiting_until: the run reads it to complete the timer node
		// (consumeFinishedWait) instead of arming a new timer.
		if err := e.queueRepo.SetQueued(ctx, instanceID, now); err != nil {
			return fmt.Errorf("failed to set queued: %w", err)
		}
		e.publishResumedEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())
		return nil
	}

	// Timer has not expired -> go back to waiting.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}
	e.publishResumedEvent(instanceID)
	return nil
}

// resumeFromRunning handles resume for instances that were running when paused.
func (e *Engine) resumeFromRunning(ctx context.Context, instanceID uuid.UUID) error {
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	e.publishResumedEvent(instanceID)

	e.wg.Add(1)
	go e.run(runCtx, instanceID)
	return nil
}

// publishResumedEvent emits a standard instance.resumed event.
func (e *Engine) publishResumedEvent(instanceID uuid.UUID) {
	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.resumed",
		Timestamp: time.Now(),
	})
}

// Cancel sets a running, queued, paused, or waiting instance to cancelled and
// cancels its context/timer as appropriate, then cancels every active descendant,
// traversing terminal intermediates without changing their outcomes.
//
// An authorized terminal parent is left untouched, but its subtree cancellation
// intent is durably stamped before the walk. The call succeeds if any descendant
// was cancelled, otherwise returning the original ErrCannotCancel. Stamp failures
// fail closed; authorization or storage failures on the parent cascade nothing.
//
// ORDER IS A CORRECTNESS REQUIREMENT, NOT A STYLE PREFERENCE. The child-terminal
// wake only re-queues a parent that is STILL in the waiting state. Cancelling
// the parent FIRST means it is already cancelled by the time its children reach
// their terminal state, so the wake is a no-op and the parent cannot be
// resurrected through its own failure port by the very children being torn
// down. Reversing the order produces a parent that flips from cancelled back to
// queued.
func (e *Engine) Cancel(ctx context.Context, instanceID uuid.UUID) error {
	ownErr := e.cancelAuthorized(ctx, instanceID)
	if ownErr != nil && !errors.Is(ownErr, instance.ErrCannotCancel) {
		return ownErr
	}
	if ownErr != nil {
		// ErrCannotCancel is returned only after authorization. A terminal
		// parent's outcome cannot carry intent, so persist it separately first.
		if err := e.spawnLinks.MarkSubtreeCancelRequested(context.WithoutCancel(ctx), instanceID); err != nil {
			return fmt.Errorf("recording subtree cancellation of %s: %w", instanceID, err)
		}
	}
	// DETACH the cascade from the caller's request. The parent is already
	// cancelled or terminal, so the walk MUST run to completion: on the
	// request context, a client disconnect or a write timeout part-way through
	// would silently strand the rest of the subtree — active descendants running
	// with the parent's inherited credentials and nobody watching. This is the
	// same posture OnChildTerminal already takes at its call sites.
	//
	// context.WithoutCancel (not context.Background) because cancelAuthorized
	// re-authorizes every descendant from the identity carried on ctx: dropping
	// the values would turn the cascade into an unauthenticated walk. Only
	// cancellation and the deadline are shed.
	stopped := e.cascadeCancel(context.WithoutCancel(ctx), instanceID, map[uuid.UUID]bool{instanceID: true})
	if ownErr != nil && stopped == 0 {
		return ownErr
	}
	return nil
}

// CancelGroupTask cancels an unfinished group task and its descendants on
// behalf of the group's unfinished-task behaviour. Unlike Cancel, a task that
// is already terminal (including one whose completion wins the race with this
// call) returns ErrCannotCancel with no subtree intent stamped and nothing
// below it touched: its own fire-and-forget work is not the group's unfinished
// work. An operator's subtree cancel of the parent reaches it through Cancel.
func (e *Engine) CancelGroupTask(ctx context.Context, taskID uuid.UUID) error {
	if err := e.cancelAuthorized(ctx, taskID); err != nil {
		return err
	}
	e.cascadeCancel(context.WithoutCancel(ctx), taskID, map[uuid.UUID]bool{taskID: true})
	return nil
}

// cancelAuthorized authorizes and performs the cancellation of ONE instance:
// the state transition, wait-resource deactivation, the cancelled event, and
// the intervention audit record. It performs NO cascade — Cancel owns that, so
// the cascade can reuse this authorized path for every descendant and produce
// an audit record for each one.
func (e *Engine) cancelAuthorized(ctx context.Context, instanceID uuid.UUID) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
	}

	switch inst.State {
	case instance.StateRunning:
		if err := e.setCancelledState(ctx, instanceID); err != nil {
			return err
		}
		e.mu.Lock()
		if cancelFn, ok := e.contexts[instanceID]; ok {
			cancelFn()
		}
		e.mu.Unlock()
		e.publishCancelledEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StateWaiting:
		e.deactivateWaitResources(instanceID, inst)
		if err := e.setCancelledState(ctx, instanceID); err != nil {
			return err
		}
		e.publishCancelledEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StatePaused, instance.StateQueued:
		e.deactivateWaitResources(instanceID, inst)
		if err := e.setCancelledState(ctx, instanceID); err != nil {
			return err
		}
		e.publishCancelledEvent(instanceID)

	default:
		return fmt.Errorf("instance %s cannot be cancelled (state: %s): %w", instanceID, inst.State, instance.ErrCannotCancel)
	}
	e.recordIntervention(ctx, inst, instance.ActionCancel, nil)
	// A cancelled run may be somebody's awaited child; a cancelled child routes
	// its parent through the failure port (there is no separate cancellation
	// path).
	e.OnChildTerminal(context.Background(), instanceID)
	return nil
}

// deactivateWaitResources cleans up webhook tokens and timers associated with a
// waiting or paused-from-waiting instance.
func (e *Engine) deactivateWaitResources(instanceID uuid.UUID, inst *instance.Instance) {
	if e.webhookRegistry != nil && inst.WebhookToken != nil {
		e.webhookRegistry.Deactivate(*inst.WebhookToken)
	}
	if e.scheduler != nil && (inst.WaitingUntil != nil || inst.WebhookToken != nil) {
		e.scheduler.Cancel(instanceID)
	}
}

// publishCancelledEvent emits a standard instance.cancelled event.
func (e *Engine) publishCancelledEvent(instanceID uuid.UUID) {
	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInstanceCancelled,
		Timestamp: time.Now(),
	})
}

// Retry resumes a failed instance from the failed node, optionally
// applying variable edits.
func (e *Engine) Retry(ctx context.Context, instanceID uuid.UUID, variableEdits map[string]any) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst.State != instance.StateFailed {
		return fmt.Errorf("instance %s is not failed (state: %s): %w", instanceID, inst.State, instance.ErrNotFailed)
	}
	// Group task terminals are immutable receipts, even before the group decides.
	// Refuse before edits, cursor changes or dismissal clearing: the storage
	// trigger rejects reopening too late to protect those earlier writes.
	if inst.IsGroupTask() {
		return fmt.Errorf("group task %s has an immutable terminal outcome: %w", instanceID, instance.ErrCannotRetry)
	}

	// Apply variable edits.
	if variableEdits != nil {
		for k, v := range variableEdits {
			inst.Variables[k] = v
		}
		if err := e.instances.UpdateVariables(ctx, instanceID, inst.Variables); err != nil {
			return fmt.Errorf("failed to update variables: %w", err)
		}
	}

	// Set the instance to running, keeping the current node as the failed node.
	if inst.FailedNodeID != nil {
		if err := e.instances.UpdateCurrentNode(ctx, instanceID, *inst.FailedNodeID); err != nil {
			return fmt.Errorf("failed to update current node: %w", err)
		}
	}

	// Resurface on a new outcome: clear this instance's dismissals for every
	// user so the now-running item reappears in Active and, when it reaches a
	// fresh terminal state, in Completed/Failed. A new result is new information
	// the user should see. This MUST run before flipping the instance to running:
	// if the clear fails after the state flip, the instance would be stranded in
	// running with no run goroutine (an unrecoverable stuck-running window). By
	// clearing first, a clear failure aborts the retry while the instance is
	// still failed and retryable.
	if e.dismissals != nil {
		if err := e.dismissals.ClearForInstance(ctx, instanceID); err != nil {
			return fmt.Errorf("failed to clear dismissals on retry: %w", err)
		}
	}

	if err := e.instances.UpdateState(ctx, instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.retrying",
		Timestamp: time.Now(),
	})

	e.wg.Add(1)
	go e.run(runCtx, instanceID)
	e.recordIntervention(ctx, inst, instance.ActionRetry, nil)
	return nil
}

// Rerun creates a new instance from a completed instance's original variables
// and triggers it. Only terminal instances (completed, failed, cancelled) can
// be rerun.
func (e *Engine) Rerun(ctx context.Context, instanceID uuid.UUID) (uuid.UUID, error) {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return uuid.Nil, err
	}

	switch inst.State {
	case instance.StateCompleted, instance.StateFailed, instance.StateCancelled:
		// ok
	default:
		return uuid.Nil, fmt.Errorf("instance %s cannot be rerun (state: %s): %w", instanceID, inst.State, instance.ErrCannotRerun)
	}

	if inst.OriginalVariables == nil {
		return uuid.Nil, fmt.Errorf("instance %s has no original variables recorded", instanceID)
	}

	// Resolve the workflow to rerun by its STABLE id first (id->current name), so
	// a rename between the original run and the rerun still targets the right
	// definition. Fall back to the retained WorkflowName label when the source
	// instance has no id (a legacy NULL-id run) or the id no longer resolves (the
	// workflow was deleted/unregistered — the name path then surfaces the
	// definitive not-found from the loader).
	rerunName := inst.WorkflowName
	if e.resolver != nil && inst.WorkflowID != uuid.Nil {
		if name, rerr := e.resolver.ResolveName(ctx, inst.WorkflowID); rerr == nil {
			rerunName = name
		}
	}

	// Forward the request ctx into Trigger so the new instance is owned by the
	// acting caller (Phase 3 Task 7's triggerOwner resolves the identity from ctx,
	// falling back to the default user for system triggers). The new instance is
	// independently scoped and owned by the actor, not the source's owner.
	//
	// Rerun creates a brand-new instance id via Trigger; new instances have no
	// dismissal rows, so the rerun is inherently un-dismissed for every user.
	// The original instance's historical dismissal (if any) is intentionally
	// left intact. No dismissal bookkeeping is needed here.
	id, _, err := e.Trigger(ctx, rerunName, inst.OriginalVariables, string(inst.Priority))
	if err != nil {
		return id, err
	}
	// Attribute on the SOURCE instance so its owner sees an admin reran it; the
	// detail is the new instance's ID. The new instance is owned by the actor and
	// needs no attribution of its own.
	newIDStr := id.String()
	e.recordIntervention(ctx, inst, instance.ActionRerun, &newIDStr)
	return id, nil
}

// SetQueueManager sets the queue manager on the engine. This is used to break
// the circular dependency between engine and queue manager during initialization.
func (e *Engine) SetQueueManager(qm QueueManager) {
	e.queueManager = qm
}

// SetPriority changes the priority of a queued or waiting instance.
func (e *Engine) SetPriority(ctx context.Context, id uuid.UUID, priority string) error {
	inst, err := e.authorizeIntervention(ctx, id)
	if err != nil {
		return err
	}
	if inst.State != instance.StateQueued && inst.State != instance.StateWaiting {
		return fmt.Errorf("instance %s is not queued (state: %s): %w", id, inst.State, instance.ErrNotQueued)
	}
	if err := e.instances.UpdatePriority(ctx, id, instance.Priority(priority)); err != nil {
		return err
	}
	p := priority
	e.recordIntervention(ctx, inst, instance.ActionSetPriority, &p)
	return nil
}

// Shutdown gracefully waits for all in-flight workflow goroutines to finish.
// It should be called before closing database connections or other resources.
func (e *Engine) Shutdown() {
	e.wg.Wait()
}

// RecoverCrashedInstances finds instances that are in running state
// (from a previous crash) and sets them to failed.
//
// It deliberately does NOT call OnChildTerminal. Startup reconcilers run by
// the composition root immediately afterwards read persisted states:
// SpawnWaitReconciler resumes legacy parents whose awaited children crashed;
// GroupCoordinator.Reconcile takes in failed tasks and cancels active tasks
// under failed parents. Dispatching here would duplicate that work and run
// coordinator side effects before queue timers and webhooks are recovered.
func (e *Engine) RecoverCrashedInstances() error {
	return e.RecoverCrashedInstancesContext(context.Background())
}

// RecoverCrashedInstancesContext binds startup recovery to the serving lifecycle.
func (e *Engine) RecoverCrashedInstancesContext(ctx context.Context) error {
	running, err := e.instances.ListRunning(ctx)
	var corrupt *instance.CorruptPinsError
	if errors.As(err, &corrupt) {
		// Startup recovery evaluates the queue afterwards; no capacity notice.
		for _, id := range corrupt.IDs {
			if ferr := e.failCorruptRun(ctx, id, false); ferr != nil {
				return ferr
			}
		}
	} else if err != nil {
		return fmt.Errorf("failed to list running instances: %w", err)
	}

	for _, inst := range running {
		failedNode := ""
		if inst.CurrentNodeID != nil {
			failedNode = *inst.CurrentNodeID
		}
		if err := e.instances.SetFailed(ctx, inst.ID, failedNode); err != nil {
			return fmt.Errorf("failed to set instance %s as failed: %w", inst.ID, err)
		}
		e.eventBus.Publish(inst.ID, Event{
			Type:      "instance.recovered",
			Timestamp: time.Now(),
			Data:      map[string]any{"reason": "crash recovery"},
		})
	}

	return nil
}
