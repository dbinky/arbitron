package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// interventionRecorder is the single collaborator that owns non-owner
// intervention accountability for BOTH the engine (Pause/Resume/Cancel/Retry/
// Rerun/SetPriority) and the interactive conversation handler (HandleMessage/
// EndSession). It resolves the acting (non-owner) actor, writes the best-effort
// intervention attribution row, and emits the domain audit event
// (instance.lead_intervention / admin.override).
//
// It exists because the engine and conversation paths previously implemented the
// same three steps independently and DRIFTED: the interactive path wrote the
// attribution row but never emitted the audit event, so a lead/admin steering a
// session they did not own left ZERO audit trail (PR #21 Must Fix). Funneling
// both paths through one recorder makes that divergence structurally impossible —
// the audit emission cannot be forgotten on one path again.
//
// Both fields are nil-safe: a nil interventions repo skips the row, a nil audit
// recorder skips the event. Both writes are best-effort and never fail the
// underlying operation that already succeeded.
type interventionRecorder struct {
	interventions instance.InterventionRepository // optional; nil-safe attribution row
	audit         AuditRecorder                   // optional; nil-safe domain audit event
}

// record attributes a non-owner intervention on inst. It returns the acting
// user's id and true when the caller is a non-owner (so the caller can tag its
// own events); for an owner self-action or an identity-less call it returns
// (uuid.Nil, false) and writes nothing.
//
// detail is the optional intervention-row detail (e.g. a rerun's new instance
// id). The audit event carries its own JSON detail describing the action.
func (r *interventionRecorder) record(ctx context.Context, inst *instance.Instance, action string, detail *string) (uuid.UUID, bool) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.UserID == inst.OwnerID {
		return uuid.Nil, false // owner self-action (or no identity): not attributed
	}

	// 1. Best-effort attribution row.
	if r.interventions != nil {
		iv := &instance.Intervention{
			ID:          uuid.New(),
			InstanceID:  inst.ID,
			ActorUserID: id.UserID,
			Action:      action,
			Detail:      detail,
			CreatedAt:   time.Now(),
		}
		if err := r.interventions.Create(ctx, iv); err != nil {
			slog.Error("failed to record intervention", "instanceID", inst.ID, "error", err)
		}
	}

	// 2. Domain audit event for the non-owner intervention (lead_intervention or
	// admin.override). Void/nil-safe: a write failure is swallowed inside the
	// audit service, never returned.
	if r.audit != nil {
		if auditAction := auditActionFor(id, inst); auditAction != "" {
			r.audit.Record(ctx, auditAction, inst.ID.String(), fmt.Sprintf(`{"action":%q}`, action))
		}
	}

	return id.UserID, true
}
