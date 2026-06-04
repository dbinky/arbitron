package http

import (
	"net/http"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// CurrentUserResolver resolves the acting user for a request. Today it always
// returns the seeded "local" user via UserRepository.GetDefaultUser, ignoring
// the request entirely.
//
// TODO(phase-6): replace the body of CurrentUser to read the authenticated user
// from the request context (set by the auth middleware noted in server.go). All
// per-user dismissal endpoints depend only on this seam, so swapping it makes
// the per-user semantics correct for many users without touching handlers.
type CurrentUserResolver struct {
	users instance.UserRepository
}

// NewCurrentUserResolver builds a resolver over the user repository.
func NewCurrentUserResolver(users instance.UserRepository) *CurrentUserResolver {
	return &CurrentUserResolver{users: users}
}

// CurrentUser returns the acting user's ID for this request.
func (c *CurrentUserResolver) CurrentUser(r *http.Request) (uuid.UUID, error) {
	return c.users.GetDefaultUser(r.Context())
}
