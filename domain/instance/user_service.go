package instance

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// Role-management sentinels. ErrUnauthenticated/ErrNotFound are reused from the
// package; these add the authz-specific failures the surfaces map to HTTP/tool
// errors.
var (
	// ErrUnauthenticated: the request carries no identity in context.
	ErrUnauthenticated = errors.New("instance: unauthenticated")
	// ErrForbidden: the caller is authenticated but not an admin.
	ErrForbidden = errors.New("instance: forbidden (admin only)")
	// ErrInvalidRole: the requested role is neither 'user' nor 'admin'.
	ErrInvalidRole = errors.New("instance: invalid role")
	// ErrBootstrapAdminProtected: a config-listed bootstrap admin cannot be
	// demoted (prevents accidental lockout).
	ErrBootstrapAdminProtected = errors.New("instance: bootstrap admin cannot be demoted")
	// ErrLastAdmin: the last remaining admin cannot be demoted (no zero-admin
	// system).
	ErrLastAdmin = errors.New("instance: cannot demote the last admin")
)

// bootstrapChecker is the narrow seam UserService needs from the Provisioner so
// the demotion floor consults the SAME normalized bootstrap set and rule the
// provisioner used at creation time.
type bootstrapChecker interface {
	IsBootstrapAdmin(subject, email string) bool
}

// UserService implements admin-only role management. The admin gate and demotion
// floors live HERE (in the domain) so REST and MCP share one rule.
type UserService struct {
	users     UserRepository
	bootstrap bootstrapChecker
}

// NewUserService builds the service. bootstrap is typically the *Provisioner so
// the demotion floor matches the provisioning rule.
func NewUserService(users UserRepository, bootstrap bootstrapChecker) *UserService {
	return &UserService{users: users, bootstrap: bootstrap}
}

// List returns all users. Admin-only (full visibility per the spec).
func (s *UserService) List(ctx context.Context) ([]User, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	if !id.IsAdmin() {
		return nil, ErrForbidden
	}
	return s.users.List(ctx)
}

// SetRole sets a target user's role. Admin-only, with two demotion floors: a
// bootstrap admin can never be demoted, and the last remaining admin can never
// be demoted. Self-demotion is allowed only while another admin remains.
func (s *UserService) SetRole(ctx context.Context, targetID uuid.UUID, role string) error {
	caller, ok := identity.FromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if !caller.IsAdmin() {
		return ErrForbidden
	}
	if role != string(identity.RoleUser) && role != string(identity.RoleAdmin) {
		return ErrInvalidRole
	}

	target, err := s.users.GetByID(ctx, targetID)
	if err != nil {
		return err // ErrNotFound propagates
	}
	if string(target.Role) == role {
		return nil // idempotent no-op
	}

	// Guards apply only to DEMOTIONS (admin -> user).
	if target.Role == identity.RoleAdmin && role == string(identity.RoleUser) {
		// Floor 1: a bootstrap admin can never be demoted. Match on the PERSISTED
		// user's email (case-insensitive) falling back to subject — the same
		// normalized set/rule the provisioner used.
		if s.bootstrap.IsBootstrapAdmin(derefStr(target.Subject), derefStr(target.Email)) {
			return ErrBootstrapAdminProtected
		}
		// Floor 2: never demote the last remaining admin.
		n, err := s.users.CountAdmins(ctx)
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
	}
	return s.users.UpdateRole(ctx, targetID, role)
}

// derefStr returns the pointed-to string, or "" when nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
