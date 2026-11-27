// Package audit defines the consequential-mutation audit trail: a small,
// append-only record of who changed what. Reads are never audited (spec
// §Audit). Introduced in RBAC Phase 2; reused by Phase 3 lead interventions.
package audit

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Action constants name the audited mutations. Kept as exported strings so the
// emitting call sites and the store agree exactly.
const (
	ActionWorkflowPublish          = "workflow.publish"
	ActionWorkflowVisibilityChange = "workflow.visibility_change"
	ActionWorkflowDelete           = "workflow.delete"
	ActionWorkflowUnpublish        = "workflow.unpublish"
	ActionWorkflowCreate           = "workflow.create"
	ActionWorkflowArchive          = "workflow.archive"
	ActionWorkflowUnarchive        = "workflow.unarchive"
	ActionWorkflowTransfer         = "workflow.transfer_ownership"
	ActionWorkflowRename           = "workflow.rename"
	ActionWorkflowDuplicate        = "workflow.duplicate"
	ActionWorkflowEditShared       = "workflow.edit_shared"
	// ActionLibEditShared is emitted when an admin writes or deletes a file in
	// the shared @lib library. Detail: {"path":..., "op":"write"|"delete"}.
	ActionLibEditShared = "lib.edit_shared"
	ActionAdminOverride = "admin.override"
	// ActionLeadIntervention is emitted when a department lead (not an admin)
	// intervenes on a run they do not own. Declared here, the single home for the
	// audit vocabulary, so the engine that emits it and any forensic query that
	// filters on it reference one value.
	ActionLeadIntervention = "instance.lead_intervention"

	ActionRunnerUpdate             = "runner.update"
	ActionRunnerCordon             = "runner.cordon"
	ActionRunnerUncordon           = "runner.uncordon"
	ActionRunnerSourceProbe        = "runner.source_probe"
	ActionRunnerKeyCreate          = "runner_key.create"
	ActionRunnerKeyRevoke          = "runner_key.revoke"
	ActionRunnerDrain              = "runner.drain"
	ActionRunnerJobReconnectAbsent = "runner.job_reconnect_absent"
)

// Event is one audit record. Detail is opaque JSON describing the change
// (e.g. {"from":"department","to":"public","department":"engineering"}).
type Event struct {
	ID         uuid.UUID
	OccurredAt time.Time
	ActorID    string // identity.UserID string, or "system" when no identity
	Action     string
	Target     string // workflow name / instance id
	Detail     string // JSON; "" is normalized to "{}" by the store
}

// EventStore is the append-only persistence port for audit events.
type EventStore interface {
	Append(ctx context.Context, ev Event) error
	// ListByActor returns the most recent events for an actor (newest first),
	// capped at limit. Used by tests and any future audit-view surface.
	ListByActor(ctx context.Context, actorID string, limit int) ([]Event, error)
	// ListByTarget returns every event recorded against a target (e.g. a workflow
	// name), newest first. Used by the workflow audit-trail query surface.
	ListByTarget(ctx context.Context, target string) ([]Event, error)
}
