package workflow

import (
	"context"

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

// canSeeShared resolves the publication for a shared name and applies
// CanSeePublication. A nil lookup, or a name with no publication record, is
// treated as visible-public (local/read-only mode, and pre-publication-model
// folders, stay open).
func canSeeShared(ctx context.Context, lookup PublisherLookup, id identity.Identity, sharedName string) (bool, error) {
	if lookup == nil {
		return true, nil
	}
	pub, ok, err := lookup.PublicationOf(ctx, sharedName)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	return CanSeePublication(id, pub), nil
}
