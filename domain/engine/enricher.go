package engine

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/arbitron/arbitron/domain/identity"
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
}

// NewInstanceEnricher constructs the enricher with its collaborators. A nil
// queuePos leaves the queue-position field at 0 (non-queued) until Phase 2
// wires the real provider.
func NewInstanceEnricher(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	users instance.UserRepository,
	queuePos QueuePositionProvider,
) *InstanceEnricher {
	return &InstanceEnricher{
		instances: instances,
		nodeExecs: nodeExecs,
		users:     users,
		queuePos:  queuePos,
	}
}

// SetQueuePositionProvider wires the queue-position provider (Phase 2). Kept
// as a setter so the composition root can construct the enricher before the
// queue manager exists, then wire the provider once it does.
func (e *InstanceEnricher) SetQueuePositionProvider(p QueuePositionProvider) {
	e.queuePos = p
}

// Enrich composes the additive fields for the given authorized page. The
// callerID drives "you" vs display-name rendering in the HTTP layer (the
// enricher resolves the display name; the HTTP layer decides the label from
// mode). Returns the enriched rows in the same order as the input.
func (e *InstanceEnricher) Enrich(ctx context.Context, summaries []instance.InstanceSummary, callerID identity.Identity) ([]instance.EnrichedInstanceSummary, error) {
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

		// Run name: needs the full instance for variables + snapshot + inputs.
		// Best-effort: a Get failure degrades only the run name (the ladder
		// still yields the short id from s.ID).
		inst, instErr := e.instances.Get(ctx, s.ID)
		if instErr != nil || inst == nil {
			slog.Warn("enricher: instance load failed; run name degraded to short id", "instanceID", s.ID, "error", instErr)
			row.RunName = instance.DeriveRunName(s.WorkflowName, nil, nil, s.ID)
		} else {
			inputs := readWorkflowInputs(inst.WorkflowSnapshot)
			row.RunName = instance.DeriveRunName(inst.WorkflowName, inst.Variables, inputs, inst.ID)

			// Friendly step + backend from the relevant node in the snapshot.
			label, backend := e.resolveNodeFields(inst)
			row.FriendlyStep = label
			row.Backend = backend

			// FailedNodeID carried for the dashboard detail panel's lazy error load.
			row.FailedNodeID = inst.FailedNodeID
		}

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

// batchUsers resolves the distinct owner IDs to a user map. A missing or
// errored user is absent from the map (the caller degrades the submitter
// field). Distinct IDs are looked up once.
func (e *InstanceEnricher) batchUsers(ctx context.Context, ownerSet map[uuid.UUID]struct{}) map[uuid.UUID]instance.User {
	out := make(map[uuid.UUID]instance.User, len(ownerSet))
	if e.users == nil {
		return out
	}
	for id := range ownerSet {
		u, err := e.users.GetByID(ctx, id)
		if err != nil {
			slog.Warn("enricher: user lookup failed; submitter degraded", "ownerID", id, "error", err)
			continue
		}
		out[id] = *u
	}
	return out
}

// resolveNodeFields returns (label, backend) for the relevant node in the
// instance's workflow snapshot. The relevant node is:
//   - CurrentNodeID when the instance is active (running/waiting/paused)
//   - FailedNodeID when failed
//   - none otherwise (completed/cancelled have no current step) -> ("", "")
//
// Best-effort: any parse/lookup failure also returns ("", "").
func (e *InstanceEnricher) resolveNodeFields(inst *instance.Instance) (label, backend string) {
	nodeID := relevantNodeID(inst)
	if nodeID == "" {
		return "", ""
	}
	node, err := FindNodeByID(inst.WorkflowSnapshot, nodeID)
	if err != nil {
		return "", ""
	}
	if node.Label != "" {
		label = node.Label
	} else {
		label = node.ID
	}
	if node.Config.Backend != workflow.BackendUnset {
		backend = node.Config.Backend
	}
	return label, backend
}

// relevantNodeID picks the node whose label/backend the row should show.
func relevantNodeID(inst *instance.Instance) string {
	if inst.State == instance.StateFailed && inst.FailedNodeID != nil && *inst.FailedNodeID != "" {
		return *inst.FailedNodeID
	}
	if inst.CurrentNodeID != nil && *inst.CurrentNodeID != "" {
		return *inst.CurrentNodeID
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

// accountIdentity returns the email if present, else the subject, for the
// shared-mode row detail. Empty when neither is set (local default user).
func accountIdentity(u instance.User) string {
	if u.Email != nil && *u.Email != "" {
		return *u.Email
	}
	if u.Subject != nil && *u.Subject != "" {
		return *u.Subject
	}
	return ""
}
