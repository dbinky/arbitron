package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// ownerConstraint returns the OwnerID filter the caller is allowed to see:
// nil for an admin (unconstrained), or a pointer to the caller's own UserID
// for a non-admin. The bool reports whether an identity was present.
func ownerConstraint(ctx context.Context) (*uuid.UUID, bool) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, false
	}
	if id.IsAdmin() {
		return nil, true
	}
	uid := id.UserID
	return &uid, true
}

// canView reports whether the context identity may read an instance owned by
// ownerID: admins may read any instance; a non-admin only their own. It is the
// shared gate consumed by the scoped read service methods (get/trace/variables)
// and the intervention authorization gate.
func canView(ctx context.Context, ownerID uuid.UUID) bool {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false
	}
	return id.IsAdmin() || id.UserID == ownerID
}
