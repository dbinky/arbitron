package instance

import "context"

// spawnLinkKey is the unexported context key; using a private type prevents
// collisions with any other package's context values.
type spawnLinkKey struct{}

// WithSpawnLink returns a copy of ctx carrying l, so the engine can stamp a
// child's parent link at instance CONSTRUCTION without a Trigger signature
// change.
//
// This is a SIBLING of WithOrigin, not an extension of it. Origin answers "how
// did this run start" and is stamped onto trigger_source/schedule_id; the spawn
// link answers "which node of which run created me". Folding the link into
// Origin would force both schedule trigger sites — and every other Origin
// consumer — to reason about spawning.
//
// The link MUST be carried this way rather than written after the trigger
// returns: Engine.TriggerWithDepartment creates AND enqueues, so control
// returns to the caller only after the child may already be running. A child
// that reached a terminal state before a follow-up link write landed would
// leave its waiting parent with nothing to match, and the parent would hang.
func WithSpawnLink(ctx context.Context, l SpawnLink) context.Context {
	return context.WithValue(ctx, spawnLinkKey{}, l)
}

// SpawnLinkFromContext returns the link set by WithSpawnLink, or ok=false when
// none is present (a root run: manual, scheduled, or webhook-triggered).
func SpawnLinkFromContext(ctx context.Context) (SpawnLink, bool) {
	l, ok := ctx.Value(spawnLinkKey{}).(SpawnLink)
	return l, ok
}
