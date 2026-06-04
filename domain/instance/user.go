package instance

import (
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// User is the persisted account auth resolves identities against. Subject is the
// IdP-stable identifier; it is nil for the pre-existing default 'local' user and
// for any user created outside the OIDC path. Email is the IdP email claim
// (nullable); it is the persisted authority for the bootstrap-admin rule
// (Phase 2: AuthConfig.BootstrapAdmins matches email case-insensitively, falling
// back to subject), so the demotion floor can match a bootstrap-by-email admin
// using only the persisted User.
type User struct {
	ID       uuid.UUID
	Subject  *string // nullable: the default user has none
	Email    *string // nullable: IdP email claim; bootstrap-admin match authority
	Username string
	Role     identity.Role
}
