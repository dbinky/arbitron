package workflow

import (
	"github.com/arbitron/arbitron/domain/identity"
)

// CanSeePublication is the single visibility predicate consulted by every read
// path that can surface a shared (published) workflow. Order matters only for
// clarity, not correctness:
//   - admins see everything (this is also the "admin override" audit trigger,
//     recorded by the caller in Task 6 when admin is the SOLE reason);
//   - the publisher always sees their own publication;
//   - public is visible to everyone;
//   - department is visible only to members of that department.
//
// private workflows are never publications, so they never reach this predicate.
func CanSeePublication(id identity.Identity, p Publication) bool {
	return id.IsAdmin() ||
		p.Publisher == id.UserID ||
		p.Visibility == VisibilityPublic ||
		(p.Visibility == VisibilityDepartment && id.InDepartment(p.Department))
}

// AdminOverrode reports whether admin status was the SOLE reason id can see p:
// access is granted, but a copy of the identity with admin cleared would be
// denied. This is the precise condition for an admin.override audit event
// (design doc §Audit Log).
//
// It is the ONE canonical sole-reason predicate, consulted by every path that
// can emit admin.override — the workflow read paths AND the engine trigger path —
// so the override condition (which deliberately keeps the publisher branch via
// CanSeePublication) cannot drift between them (PR #21).
func AdminOverrode(id identity.Identity, p Publication) bool {
	if !id.IsAdmin() {
		return false
	}
	demoted := id
	demoted.Role = identity.RoleUser
	return CanSeePublication(id, p) && !CanSeePublication(demoted, p)
}

