package engine

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// QueuePositionProvider returns a 1-based priority-aware queue position for
// each currently-queued instance ID. Non-queued IDs are absent (position 0).
// Implemented by queue.Manager.QueuePositions in Phase 2; nil leaves the
// position field at 0 (non-queued) until Phase 2 wires the real provider.
type QueuePositionProvider interface {
	QueuePositions(ctx context.Context) (map[uuid.UUID]int, error)
}

// NoopQueuePositionProvider is the Null Object for QueuePositionProvider: it
// reports no queue positions (every instance is non-queued, position 0). The
// composition root constructs the enricher with it before the real queue
// manager exists, then swaps in the manager via SetQueuePositionProvider. Using
// an explicit Null Object (never nil) keeps the provider non-nil at all times.
type NoopQueuePositionProvider struct{}

// QueuePositions always returns an empty map (no positions) and never errors.
func (NoopQueuePositionProvider) QueuePositions(context.Context) (map[uuid.UUID]int, error) {
	return map[uuid.UUID]int{}, nil
}

// InstanceEnricher is the service-layer enrichment step for the instance list.
// Given an already-authorized page of InstanceSummary, it composes the
// additive enriched fields by calling the real producers and returns
// EnrichedInstanceSummary rows. It owns composition and degradation policy
// only: each producer call is isolated, and a failure for one field degrades
// only that field — the row still renders. It never fails the list.
type InstanceEnricher struct {
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	users     instance.UserRepository
	queuePos  QueuePositionProvider // nil -> no position (Phase 2 wires this)
	// spawnLinks is the link-addressed read surface. It is NEVER nil: the
	// constructor installs NoopSpawnLinks{} and SetSpawnLinks rejects nil, so a
	// missed wiring step cannot silently blank every row's child count.
	spawnLinks instance.SpawnLinkRepository
	// profiles resolves a node's model_profile for the row's binding cell. NEVER
	// nil: NoopProfileResolver{} until the composition root wires the real one.
	profiles            provider.ProfileResolver
	defaultAgentBackend string // deployment-wide fallback for BackendUnset nodes
}

// NewInstanceEnricher constructs the enricher with its collaborators. queuePos
// must be non-nil — pass NoopQueuePositionProvider{} when no real provider is
// available yet (the composition root swaps in the queue manager later via
// SetQueuePositionProvider). A nil queuePos is a programming error and panics.
// defaultAgentBackend is the deployment-wide fallback used when a node's
// Config.Backend is BackendUnset (inheriting).
func NewInstanceEnricher(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	users instance.UserRepository,
	queuePos QueuePositionProvider,
	defaultAgentBackend string,
) *InstanceEnricher {
	if queuePos == nil {
		panic("engine: NewInstanceEnricher requires a non-nil QueuePositionProvider (use NoopQueuePositionProvider{})")
	}
	return &InstanceEnricher{
		instances:           instances,
		nodeExecs:           nodeExecs,
		users:               users,
		queuePos:            queuePos,
		defaultAgentBackend: defaultAgentBackend,
		// Non-nil from birth, like queuePos above; the composition root swaps in
		// the real repository via SetSpawnLinks.
		spawnLinks: NoopSpawnLinks{},
		profiles:   NoopProfileResolver{},
	}
}

// SetProfileResolver wires the profile resolver used to describe the relevant
// node's model/effort/backend/provider. Pass the same resolver the executors
// use so the queue shows what the node actually runs on. nil panics — pass
// NoopProfileResolver{} to opt out deliberately.
func (e *InstanceEnricher) SetProfileResolver(r provider.ProfileResolver) {
	if r == nil {
		panic("engine: SetProfileResolver requires a non-nil ProfileResolver (use NoopProfileResolver{})")
	}
	e.profiles = r
}

// SetQueuePositionProvider wires the queue-position provider (Phase 2). Kept
// as a setter so the composition root can construct the enricher before the
// queue manager exists, then wire the provider once it does.
func (e *InstanceEnricher) SetQueuePositionProvider(p QueuePositionProvider) {
	if p == nil {
		panic("engine: SetQueuePositionProvider requires a non-nil QueuePositionProvider (use NoopQueuePositionProvider{})")
	}
	e.queuePos = p
}

// SetSpawnLinks wires the link-addressed read surface used for batched child
// counts. Kept as a setter (like SetQueuePositionProvider) so the composition
// root can construct the enricher before deciding whether the capability is
// available.
//
// repo MUST be non-nil — pass NoopSpawnLinks{} to opt out deliberately. The nil
// case is rejected for the same reason it is on Engine.SetSpawnLinks: a missed
// wiring step must fail loudly at startup, not degrade a feature in silence.
func (e *InstanceEnricher) SetSpawnLinks(repo instance.SpawnLinkRepository) {
	if repo == nil {
		panic("engine: SetSpawnLinks requires a non-nil SpawnLinkRepository (use NoopSpawnLinks{})")
	}
	e.spawnLinks = repo
}

// batchChildCounts resolves the whole page's child counts in ONE round trip,
// restricted to what the CALLER may see.
//
// The visibility is taken from the SAME listConstraint that authorized the page,
// so the count is post-authorization by construction. A count that included
// children the caller cannot see would render an expander whose expansion yields
// an empty list — leaking the existence of runs the caller may not view.
//
// A failure degrades only this field (an empty map => no expanders); the page
// still renders. With no identity on the context there is no authorized scope,
// so counting is skipped entirely rather than defaulting to unconstrained.
func (e *InstanceEnricher) batchChildCounts(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]int {
	if len(ids) == 0 {
		return map[uuid.UUID]int{}
	}
	lc, ok := listConstraint(ctx)
	if !ok {
		return map[uuid.UUID]int{}
	}
	counts, err := e.spawnLinks.ChildCountsByParents(ctx, ids, instance.ChildVisibility{
		Unconstrained: lc.Unconstrained,
		OwnerID:       lc.OwnerID,
		Departments:   lc.Departments,
		IncludePublic: lc.IncludePublic,
	})
	if err != nil {
		slog.Warn("enricher: child-count lookup failed; hierarchy expanders degraded", "error", err)
		return map[uuid.UUID]int{}
	}
	return counts
}

// Enrich composes the additive fields for the given authorized page. The
// enricher resolves the submitter's display name; the HTTP layer decides the
// "you" vs display-name label from the auth mode. Returns the enriched rows in
// the same order as the input.
func (e *InstanceEnricher) Enrich(ctx context.Context, summaries []instance.InstanceSummary) ([]instance.EnrichedInstanceSummary, error) {
	if len(summaries) == 0 {
		return []instance.EnrichedInstanceSummary{}, nil
	}

	ids := make([]uuid.UUID, len(summaries))
	ownerSet := make(map[uuid.UUID]struct{}, len(summaries))
	for i, s := range summaries {
		ids[i] = s.ID
		ownerSet[s.OwnerID] = struct{}{}
	}

	// One batched node-exec aggregate call for the whole page.
	aggMap, aggErr := e.nodeExecs.AggregateByInstances(ctx, ids)
	if aggErr != nil {
		slog.Warn("enricher: node-exec aggregate failed; retry count degraded", "error", aggErr)
		aggMap = map[uuid.UUID]instance.NodeExecutionAggregate{}
	}

	// One batched user lookup for the page's distinct owners.
	userMap := e.batchUsers(ctx, ownerSet)

	// Queue positions (one call) when a provider is wired.
	var posMap map[uuid.UUID]int
	if e.queuePos != nil {
		pm, perr := e.queuePos.QueuePositions(ctx)
		if perr != nil {
			slog.Warn("enricher: queue positions failed; position degraded", "error", perr)
		}
		posMap = pm
	}
	if posMap == nil {
		posMap = map[uuid.UUID]int{}
	}

	// One batched child-count call for the whole page (never N+1).
	childCounts := e.batchChildCounts(ctx, ids)

	// One profile lookup per alias for the whole page (never N+1), and one log
	// line per failing alias.
	profiles := newPageProfileResolver(e.profiles)

	out := make([]instance.EnrichedInstanceSummary, len(summaries))
	for i, s := range summaries {
		row := instance.EnrichedInstanceSummary{InstanceSummary: s}

		// Run name + friendly step come from the summary's variables and workflow
		// snapshot, which the ListInstances read-model now carries — no per-row
		// instance load (avoids the N+1). When the snapshot is absent the ladder
		// still yields the short id from s.ID.
		inputs := readWorkflowInputs(s.WorkflowSnapshot)
		row.RunName = instance.DeriveRunName(s.WorkflowName, s.Variables, inputs, s.ID)

		// Friendly step from the relevant node; binding (model/effort/backend/
		// provider/type) from the node the run is on NOW; git ref from variables.
		row.FriendlyStep = e.resolveStepLabel(s)
		b := e.resolveBinding(ctx, profiles, s)
		row.NodeType, row.Model, row.Effort, row.Backend, row.Provider = b.NodeType, b.Model, b.Effort, b.Backend, b.Provider
		row.GitRef = deriveGitRef(snapshotGitVariable(s.WorkflowSnapshot), s.Variables)

		// FailedNodeID carried for the dashboard detail panel's lazy error load.
		row.FailedNodeID = s.FailedNodeID

		// Submitter from the batched user map.
		if u, ok := userMap[s.OwnerID]; ok {
			row.Submitter = instance.Submitter{Name: u.Username, AccountIdentity: accountIdentity(u)}
		}

		// Retry count from the batched aggregate.
		if agg, ok := aggMap[s.ID]; ok {
			row.RetryCount = agg.MaxAttempt
		}

		// Queue position (queued rows only; 0 for non-queued).
		if pos, ok := posMap[s.ID]; ok {
			row.QueuePosition = pos
		}

		// Spawned-children count (0 => no expander).
		if n, ok := childCounts[s.ID]; ok {
			row.ChildCount = n
		}

		out[i] = row
	}
	return out, nil
}

// batchUsers resolves the distinct owner IDs to a user map in a single batched
// query. The enricher owns deduplication (ownerSet is already distinct). A
// repository error degrades the submitter field for the whole page (rows still
// render) rather than failing the list; a missing user is simply absent.
func (e *InstanceEnricher) batchUsers(ctx context.Context, ownerSet map[uuid.UUID]struct{}) map[uuid.UUID]instance.User {
	out := make(map[uuid.UUID]instance.User, len(ownerSet))
	if e.users == nil || len(ownerSet) == 0 {
		return out
	}
	ids := make([]uuid.UUID, 0, len(ownerSet))
	for id := range ownerSet {
		ids = append(ids, id)
	}
	users, err := e.users.UsersByID(ctx, ids)
	if err != nil {
		slog.Warn("enricher: batched user lookup failed; submitter degraded", "error", err)
		return out
	}
	return users
}

// nodeLabel derives the friendly step label for a node: its Label, falling
// back to the node id. Shared by the enricher and the engine's node-event
// emitters so a live-patched queue row matches what a refetch would produce.
func nodeLabel(node workflow.Node) string {
	if node.Label != "" {
		return node.Label
	}
	return node.ID
}

// resolveStepLabel returns the friendly step label for the summary's relevant
// node (see relevantNodeID). Best-effort: any lookup failure returns "".
func (e *InstanceEnricher) resolveStepLabel(s instance.InstanceSummary) string {
	nodeID := relevantNodeID(s)
	if nodeID == "" {
		return ""
	}
	node, err := FindNodeByID(s.WorkflowSnapshot, nodeID)
	if err != nil {
		return ""
	}
	return nodeLabel(*node)
}

// resolveBinding describes what the run is running on NOW: the failed node for
// a failed run, the current node for an active one. Completed and cancelled
// runs are running on nothing, so they get an empty binding even though they
// still carry a CurrentNodeID (the Step column keeps showing that label).
// A pinned run's model comes from its own pins, as it does at execution.
func (e *InstanceEnricher) resolveBinding(ctx context.Context, page provider.ProfileResolver, s instance.InstanceSummary) NodeBinding {
	switch s.State {
	case instance.StateCompleted, instance.StateCancelled:
		return NodeBinding{}
	}
	nodeID := relevantNodeID(s)
	if nodeID == "" {
		return NodeBinding{}
	}
	node, err := FindNodeByID(s.WorkflowSnapshot, nodeID)
	if err != nil {
		return NodeBinding{}
	}
	// The page resolver already logged any live failure, and List any unreadable
	// pins; the degraded binding stands.
	b, _ := resolveNodeBinding(ctx, rowProfileResolver(page, s), *node, e.defaultAgentBackend)
	return b
}

// rowProfileResolver overlays the row's own pin record on the page's memoized
// live resolver through the same PinningResolver the executors use: a pinned
// run lists its trigger-time model while credentials, backend and provider
// stay live, a legacy run lists the live model, and a missing or unreadable pin
// degrades to node config rather than showing the live model.
func rowProfileResolver(page provider.ProfileResolver, s instance.InstanceSummary) provider.ProfileResolver {
	pins := summaryPins{Version: s.ModelPinsVersion, Models: s.ModelPins, OpaqueRoutes: s.ModelPinRoutes, OpaqueConnections: s.ModelPinRouteConnections}
	return provider.NewPinningResolver(page, pins, func(context.Context) (uuid.UUID, bool) { return s.ID, true })
}

// summaryPins is the PinLookup for one list row: the pins came with the page,
// so there is nothing to load or release.
type summaryPins provider.PinSet

func (p summaryPins) Lookup(context.Context, uuid.UUID) (provider.PinSet, error) {
	return provider.PinSet(p), nil
}

func (summaryPins) Release(uuid.UUID) {}

// relevantNodeID picks the node whose label the row's Step column shows: the
// failed node for a failed run, otherwise CurrentNodeID. Note a completed or
// cancelled run still carries the CurrentNodeID it ended on, so its Step
// column shows that last node's label; resolveBinding, not this function,
// blanks the binding for those states.
func relevantNodeID(s instance.InstanceSummary) string {
	if s.State == instance.StateFailed && s.FailedNodeID != nil && *s.FailedNodeID != "" {
		return *s.FailedNodeID
	}
	if s.CurrentNodeID != nil && *s.CurrentNodeID != "" {
		return *s.CurrentNodeID
	}
	return ""
}

// snapshotGitVariable reads the workflow's optional gitVariable from the
// instance's workflow snapshot ("" when absent or not a string).
func snapshotGitVariable(snapshot map[string]any) string {
	v, _ := snapshot["gitVariable"].(string)
	return v
}

// readWorkflowInputs extracts the workflow's Inputs from the snapshot so
// DeriveRunName can apply the ladder. Best-effort: a parse failure yields nil.
func readWorkflowInputs(snapshot map[string]any) []instance.RunNameInput {
	if snapshot == nil {
		return nil
	}
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return nil
	}
	inputs := make([]instance.RunNameInput, len(wf.Inputs))
	for i, f := range wf.Inputs {
		inputs[i] = instance.RunNameInput{Name: f.Name, Required: f.Required}
	}
	return inputs
}

// accountIdentity returns the user's email for the shared-mode row detail, or
// empty when no email is set (local default user). The OIDC subject is an opaque
// IdP identifier and is deliberately NOT used as a fallback: disclosing it would
// leak the IdP subject to other viewers of public/department-peer runs.
func accountIdentity(u instance.User) string {
	if u.Email != nil && *u.Email != "" {
		return *u.Email
	}
	return ""
}
