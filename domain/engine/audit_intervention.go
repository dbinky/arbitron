package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// Audit action strings emitted by the engine for non-owner interventions. They
// match the Phase-2 audit vocabulary (admin.override) and extend it
// (instance.lead_intervention).
const (
	AuditActionLeadIntervention = "instance.lead_intervention"
	AuditActionAdminOverride    = "admin.override"
)

// AuditRecorder is the minimal write surface the engine needs from the Phase-2
// audit service. Record reads the actor from ctx and persists an event. It is
// best-effort and VOID: a store failure is logged and swallowed inside the audit
// service, never propagated, so auditing can never fail the underlying
// intervention. This matches *audit.Service.Record exactly (Phase 2), so the
// service satisfies this port with no adapter.
type AuditRecorder interface {
	Record(ctx context.Context, action, target, detail string)
}

// auditActionFor classifies a non-owner intervention into an audit action, or
// "" when no audit event should be written (owner self-action). Admin precedence
// over lead: an admin acting on a non-owned run is admin.override even if they
// also lead the run's department.
func auditActionFor(id identity.Identity, inst *instance.Instance) string {
	if id.UserID == inst.OwnerID {
		return "" // owner self-action: not audited
	}
	if id.IsAdmin() {
		return AuditActionAdminOverride
	}
	if id.LeadsDepartment(inst.Department) {
		return AuditActionLeadIntervention
	}
	return "" // a peer cannot reach a mutate path (canIntervene already denied)
}
