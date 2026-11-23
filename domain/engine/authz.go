package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// canView reports whether the context identity may READ an instance: admins and
// the owner always; a public run is visible to everyone; a department run is
// visible to members of that department. Missing identity => false (fail closed).
func canView(ctx context.Context, inst *instance.Instance) bool {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false
	}
	if id.IsAdmin() || id.UserID == inst.OwnerID {
		return true
	}
	if inst.Visibility == instance.VisibilityPublic {
		return true
	}
	return inst.Visibility == instance.VisibilityDepartment && id.InDepartment(inst.Department)
}

// CanViewInstance exposes the engine read predicate to other domain services.
// Missing instances and missing identities fail closed.
func CanViewInstance(ctx context.Context, inst *instance.Instance) bool {
	return inst != nil && canView(ctx, inst)
}

// canIntervene reports whether the context identity may MUTATE an instance
// (cancel/pause/resume/retry/rerun/priority): the owner, a lead of the run's
// department, or an admin. A department peer (member, not owner, not lead) is
// denied even though canView is true. Missing identity => false.
func canIntervene(ctx context.Context, inst *instance.Instance) bool {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false
	}
	return id.IsAdmin() || id.UserID == inst.OwnerID || id.LeadsDepartment(inst.Department)
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
