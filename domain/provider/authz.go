package provider

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
)

// Reuse the canonical authz sentinels so errors.Is matches across surfaces
// (identity → instance → provider all alias the SAME vars). domain/identity
// imports nothing from domain/provider, so there is no import cycle: provider →
// identity is a one-way edge. The adapters map these to 401/403 (HTTP) and to
// stable tool-error strings (MCP) uniformly.
var (
	// ErrUnauthenticated: the request carries no identity in context.
	ErrUnauthenticated = identity.ErrUnauthenticated
	// ErrForbidden: the caller is authenticated but not an admin.
	ErrForbidden = identity.ErrForbidden
)

// requireAuthed returns the request identity or ErrUnauthenticated when none is
// present. The read-open service methods (List/Get/Capabilities) gate on this.
func requireAuthed(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Identity{}, ErrUnauthenticated
	}
	return id, nil
}

// requireAdmin returns nil only for an authenticated admin; an absent identity
// yields ErrUnauthenticated and a non-admin yields ErrForbidden. Every mutating
// service method (profile + connection Create/Update/Delete) gates on this.
func requireAdmin(ctx context.Context) error {
	id, err := requireAuthed(ctx)
	if err != nil {
		return err
	}
	if !id.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

// Reloader is the recache hook a service calls after a successful write so the
// in-memory profile snapshot (and therefore the resolver + workflow-validation
// checkers that read it) reflects live edits without a restart. The adapter
// ProfileCache (Phase 2) satisfies it directly via its Reload(ctx) error method.
type Reloader interface {
	Reload(ctx context.Context) error
}
