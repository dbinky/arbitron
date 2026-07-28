package identity

import "github.com/google/uuid"

// UserIdentityInput carries the primitive, persisted facts about a user needed
// to construct that user's runtime Identity. It is intentionally free of any
// domain/instance type so domain/identity stays the dependency floor (it imports
// nothing from domain/instance); adapters map their persisted *instance.User
// onto this shape, exactly as the auth middleware already does.
type UserIdentityInput struct {
	UserID   uuid.UUID
	Username string
	Role     Role
	// Subject and Email drive lead matching only; they are never placed on the
	// Identity. Either may be empty.
	Subject string
	Email   string
	// StoredDepartments are the user's persisted (claim-derived) departments. May
	// be nil (e.g. a transient repo-read failure the caller chose to tolerate);
	// BuildIdentity still yields at least the default group.
	StoredDepartments []string
}

// BuildIdentity constructs the runtime Identity for a user from their persisted
// facts and the RBAC GroupConfig. It is the SINGLE source of truth for the
// Identity field set so the request path (AuthMiddleware.enrich) and the
// scheduler and spawn paths (userIdentityResolver) can never diverge — in particular
// LedDepartments, which is an authz input (workflow.CanSeePublication /
// Identity.LeadsDepartment), is populated identically on both paths.
//
// Effective departments are the user's stored departments unioned with the
// config-derived led departments (stored first, order-preserving, de-duplicated
// via the shared RBAC primitive). A user with neither still belongs to the
// default group so they are never in zero departments.
//
// The local-mode hardcoded admin/default-group literal is intentionally NOT
// routed through this builder: it has no repo lookup and is a deliberate special
// case (see AuthMiddleware.resolve).
func BuildIdentity(in UserIdentityInput, groups GroupConfig) Identity {
	led := LedDepartments(in.Subject, in.Email, groups)
	// stored ∪ led, order-preserving (stored first). A fresh slice is built so
	// neither input is mutated.
	union := make([]string, 0, len(in.StoredDepartments)+len(led))
	union = append(union, in.StoredDepartments...)
	union = append(union, led...)
	departments := DedupeDepts(union)
	if len(departments) == 0 {
		// Defensive: a user with no stored and no led departments still belongs to
		// the default group (matches the provisioner snapshot).
		departments = []string{groups.DefaultGroupOr()}
	}
	return Identity{
		UserID:         in.UserID,
		Username:       in.Username,
		Role:           in.Role,
		Departments:    departments,
		LedDepartments: led,
	}
}
