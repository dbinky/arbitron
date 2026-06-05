package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

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

// listFilter is the service-derived visibility scope a caller may list.
type listFilter struct {
	OwnerID       *uuid.UUID // nil => unconstrained (admin)
	Departments   []string
	IncludePublic bool
	Unconstrained bool // admin: no visibility narrowing at all
}

// listConstraint computes the visibility scope for ListInstances from the
// context identity. The bool reports whether an identity was present (false =>
// reveal nothing). An admin is unconstrained. A non-admin is scoped to the
// disjunction owner=self OR public OR department-member.
func listConstraint(ctx context.Context) (listFilter, bool) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return listFilter{}, false
	}
	if id.IsAdmin() {
		return listFilter{Unconstrained: true}, true
	}
	uid := id.UserID
	return listFilter{
		OwnerID:       &uid,
		Departments:   id.Departments,
		IncludePublic: true,
	}, true
}
