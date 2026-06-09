package schedule

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ErrCreatorUnresolved is returned when a run cannot be attributed because the
// schedule's creator identity cannot be resolved (e.g. the user was deleted). It
// maps to HTTP 409 / MCP conflict at the adapter seam: the run is not startable
// in its current attributed form, which is a conflict, not a server fault.
var ErrCreatorUnresolved = errors.New("schedule: creator identity could not be resolved")

// Runner is the shared fire core: resolve the schedule's creator identity,
// inject it into the context, and trigger the workflow as that creator. It is
// reused by the cron Evaluator (via resolveCreatorIdentity) and by
// Service.RunNow. It holds NO repository and therefore NEVER advances
// LastFiredAt — advancing the cron position is the Evaluator's responsibility
// alone. Run-now is out of cadence and must not perturb the cron position.
type Runner struct {
	trigger Trigger
	idents  IdentityResolver
	active  ActiveRunChecker
}

// NewRunner builds a Runner from the same three collaborators the Evaluator
// already holds (Trigger, IdentityResolver, ActiveRunChecker).
func NewRunner(t Trigger, i IdentityResolver, a ActiveRunChecker) *Runner {
	return &Runner{trigger: t, idents: i, active: a}
}

// Fire resolves the schedule's creator, optionally enforces skip-when-busy,
// injects the creator identity, and triggers the workflow.
//
//   - skipWhenBusy && an active run exists => returns (uuid.Nil, false, nil):
//     skipped, NOT an error.
//   - default (skipWhenBusy == false) fires regardless of any active run; the
//     active checker is not consulted.
//   - creator identity unresolvable => (uuid.Nil, false, wrapped
//     ErrCreatorUnresolved).
//   - trigger failure => (uuid.Nil, false, wrapped trigger error).
//
// Fire NEVER advances LastFiredAt (run-now is out of cadence).
func (r *Runner) Fire(ctx context.Context, s Schedule, skipWhenBusy bool) (uuid.UUID, bool, error) {
	if skipWhenBusy {
		busy, err := r.active.HasActiveRun(ctx, s.WorkflowName, s.CreatorID)
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("schedule: checking active run: %w", err)
		}
		if busy {
			return uuid.Nil, false, nil // skipped, not an error
		}
	}
	ident, err := resolveCreatorIdentity(ctx, r.idents, s)
	if err != nil {
		return uuid.Nil, false, err
	}
	scheduleID := s.ID
	fireCtx := instance.WithOrigin(identity.WithIdentity(ctx, ident), instance.Origin{
		Source:     instance.OriginRunNow,
		ScheduleID: &scheduleID,
	})
	instID, _, err := r.trigger.TriggerWithDepartment(fireCtx, s.WorkflowName, s.Variables, "normal", s.Department)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("schedule: triggering run-now: %w", err)
	}
	return instID, true, nil
}

// resolveCreatorIdentity is the SINGLE identity-resolution implementation shared
// by Runner.Fire and Evaluator.fireOne. A resolver failure is mapped to
// ErrCreatorUnresolved.
func resolveCreatorIdentity(ctx context.Context, idents IdentityResolver, s Schedule) (identity.Identity, error) {
	ident, err := idents.IdentityForUser(ctx, s.CreatorID)
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: creator %s: %v", ErrCreatorUnresolved, s.CreatorID, err)
	}
	return ident, nil
}
