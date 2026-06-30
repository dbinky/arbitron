package engine

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
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
	instances          instance.InstanceRepository
	nodeExecs          instance.NodeExecutionRepository
	users              instance.UserRepository
	queuePos           QueuePositionProvider // nil -> no position (Phase 2 wires this)
	defaultAgentBackend string               // deployment-wide fallback for BackendUnset nodes
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
		instances:          instances,
		nodeExecs:          nodeExecs,
		users:              users,
		queuePos:           queuePos,
		defaultAgentBackend: defaultAgentBackend,
	}
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
		slog.Warn("enricher: node-exec aggregate failed; model/retry degraded", "error", aggErr)
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

	out := make([]instance.EnrichedInstanceSummary, len(summaries))
	for i, s := range summaries {
		row := instance.EnrichedInstanceSummary{InstanceSummary: s}

		// Run name + friendly step come from the summary's variables and workflow
		// snapshot, which the ListInstances read-model now carries — no per-row
		// instance load (avoids the N+1). When the snapshot is absent the ladder
		// still yields the short id from s.ID.
		inputs := readWorkflowInputs(s.WorkflowSnapshot)
		row.RunName = instance.DeriveRunName(s.WorkflowName, s.Variables, inputs, s.ID)

		// Friendly step + backend from the relevant node in the snapshot.
		label, backend := e.resolveNodeFields(s)
		row.FriendlyStep = label
		row.Backend = backend

		// FailedNodeID carried for the dashboard detail panel's lazy error load.
		row.FailedNodeID = s.FailedNodeID

		// Submitter from the batched user map.
		if u, ok := userMap[s.OwnerID]; ok {
			row.Submitter = instance.Submitter{Name: u.Username, AccountIdentity: accountIdentity(u)}
		}

		// Model + retry from the batched aggregate.
		if agg, ok := aggMap[s.ID]; ok {
			row.Model = agg.LatestModel
			row.RetryCount = agg.MaxAttempt
		}

		// Queue position (queued rows only; 0 for non-queued).
		if pos, ok := posMap[s.ID]; ok {
			row.QueuePosition = pos
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

// nodeLabelAndBackend derives the friendly step label and effective backend for
// a node, applying the same fallbacks the enriched list uses: label falls back
// to the node id; backend falls back to defaultBackend when the node inherits
// (BackendUnset). Shared by the enricher and the engine's node-event emitters so
// a live-patched queue row matches what a refetch would produce.
func nodeLabelAndBackend(node workflow.Node, defaultBackend string) (label, backend string) {
	if node.Label != "" {
		label = node.Label
	} else {
		label = node.ID
	}
	if node.Config.Backend != workflow.BackendUnset {
		backend = node.Config.Backend
	} else if defaultBackend != "" {
		backend = defaultBackend
	}
	return label, backend
}

// resolveNodeFields returns (label, backend) for the relevant node in the
// summary's workflow snapshot. The relevant node is:
//   - CurrentNodeID when the instance is active (running/waiting/paused)
//   - FailedNodeID when failed
//   - none otherwise (completed/cancelled have no current step) -> ("", "")
//
// Best-effort: any parse/lookup failure also returns ("", "").
func (e *InstanceEnricher) resolveNodeFields(s instance.InstanceSummary) (label, backend string) {
	nodeID := relevantNodeID(s)
	if nodeID == "" {
		return "", ""
	}
	node, err := FindNodeByID(s.WorkflowSnapshot, nodeID)
	if err != nil {
		return "", ""
	}
	return nodeLabelAndBackend(*node, e.defaultAgentBackend)
}

// relevantNodeID picks the node whose label/backend the row should show.
func relevantNodeID(s instance.InstanceSummary) string {
	if s.State == instance.StateFailed && s.FailedNodeID != nil && *s.FailedNodeID != "" {
		return *s.FailedNodeID
	}
	if s.CurrentNodeID != nil && *s.CurrentNodeID != "" {
		return *s.CurrentNodeID
	}
	// Terminal (completed/cancelled) rows have no *current* step (per the
	// Phase-1 design provenance and the spec's "what step it is currently on"),
	// so the step cell intentionally degrades to a placeholder. This is not a
	// missing producer — there is no current-step value to show.
	return ""
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
