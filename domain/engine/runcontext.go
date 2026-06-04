package engine

import (
	"context"

	"github.com/google/uuid"
)

// ownerCtxKey is the unexported context key under which the engine carries the
// running instance's OwnerID into node executors, so file-reading executors
// can resolve workflow files from the owner's private workspace (with a shared
// fallback) via the scope-aware FileReader.
type ownerCtxKey struct{}

// withOwner returns a child context carrying the given instance owner.
func withOwner(ctx context.Context, owner uuid.UUID) context.Context {
	return context.WithValue(ctx, ownerCtxKey{}, owner)
}

// ownerFromContext returns the instance owner carried in ctx, or uuid.Nil when
// none is present (in which case file reads search the shared space only).
func ownerFromContext(ctx context.Context) uuid.UUID {
	if v, ok := ctx.Value(ownerCtxKey{}).(uuid.UUID); ok {
		return v
	}
	return uuid.Nil
}

// OwnerFromContext exposes the run-context owner to adapter-layer executors
// (e.g. the process script executor) so they can pass it to the FileReader.
func OwnerFromContext(ctx context.Context) uuid.UUID {
	return ownerFromContext(ctx)
}
