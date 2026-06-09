package instance

import (
	"context"

	"github.com/google/uuid"
)

// TriggerSource records how a run was started. It is a self-owned type in
// domain/instance (the package that owns the field) so the engine can read run
// origin from context WITHOUT importing domain/schedule — the same
// one-directional domain edge instance.Visibility / instance.Claims preserve.
type TriggerSource string

const (
	// OriginManual is the default: a REST/MCP trigger that carries no origin.
	OriginManual TriggerSource = "manual"
	// OriginSchedule is a cron-tick fire from the schedule evaluator.
	OriginSchedule TriggerSource = "schedule"
	// OriginRunNow is an on-demand "run now" fire of a schedule (Phase 1).
	OriginRunNow TriggerSource = "run_now"
)

// Origin is the run's provenance, threaded into the trigger context by the two
// schedule trigger sites (cron + run-now). ScheduleID is nil for a manual run.
type Origin struct {
	Source     TriggerSource
	ScheduleID *uuid.UUID
}

// originKey is the unexported context key; using a private type prevents
// collisions with any other package's context values.
type originKey struct{}

// WithOrigin returns a copy of ctx carrying o so the engine can stamp the run's
// provenance without a Trigger signature change.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFromContext returns the Origin set by WithOrigin, or ok=false when none
// is present (a plain manual trigger). Callers default to OriginManual on !ok.
func OriginFromContext(ctx context.Context) (Origin, bool) {
	o, ok := ctx.Value(originKey{}).(Origin)
	return o, ok
}
