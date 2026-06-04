package http

import (
	"net/http"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// CurrentUserResolver resolves the acting user for a request. It is a
// consumer-owned, single-method interface so the dismissal handlers depend on
// the seam, not on a concrete resolver — tests inject a fake acting user (or a
// fail-closed resolver), and the Phase-6 swap replaces only the implementation.
type CurrentUserResolver interface {
	// CurrentUser returns the acting user's ID for this request. It MUST fail
	// closed: when no principal can be resolved it returns an error rather than
	// falling back to a default user. The per-user authorization model rests on
	// this contract.
	CurrentUser(r *http.Request) (uuid.UUID, error)
}

// DefaultUserResolver is the current implementation of CurrentUserResolver. It
// always returns the seeded "local" user via UserRepository.GetDefaultUser,
// ignoring the request entirely.
//
// TODO(phase-6): replace this with a resolver that reads the authenticated user
// from the request context (set by the auth middleware noted in server.go) and
// returns an error when no principal is present — never a silent default-user
// fallback. All per-user dismissal endpoints depend only on the
// CurrentUserResolver seam, so swapping the implementation makes the per-user
// semantics correct for many users without touching handlers.
type DefaultUserResolver struct {
	users instance.UserRepository
}

// NewCurrentUserResolver builds the default resolver over the user repository.
func NewCurrentUserResolver(users instance.UserRepository) *DefaultUserResolver {
	return &DefaultUserResolver{users: users}
}

// CurrentUser returns the acting user's ID for this request.
func (c *DefaultUserResolver) CurrentUser(r *http.Request) (uuid.UUID, error) {
	return c.users.GetDefaultUser(r.Context())
}

// Compile-time assertion that the default resolver satisfies the seam.
var _ CurrentUserResolver = (*DefaultUserResolver)(nil)
