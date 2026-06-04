package instance

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// ErrInvalidClaims is returned by Provisioner.Resolve when the verified claims
// carry no subject (the stable IdP identifier required to resolve a user).
var ErrInvalidClaims = errors.New("instance: invalid claims (missing subject)")

// Claims is the minimal verified-identity projection the Provisioner needs. It
// mirrors identity.Claims but is redeclared here so domain/instance does not
// take a structural dependency on the identity claims type; the OIDC adapter
// maps its claims to this shape at the call site (identity already imports
// nothing from instance — keep the edge one-directional).
type Claims struct {
	Subject  string
	Username string
	Email    string
}

// Provisioner resolves verified Claims to a persisted User, creating one on
// first sight. A subject/email on the bootstrap list is created as admin. It is
// a pure domain service over the UserRepository port (no infra import).
type Provisioner struct {
	users     UserRepository
	bootstrap map[string]struct{} // lower-cased subjects AND emails
	now       func() time.Time
	newID     func() uuid.UUID
}

// NewProvisioner builds a Provisioner, normalizing the bootstrap list to a
// lower-cased set for O(1), case-insensitive membership.
func NewProvisioner(users UserRepository, bootstrapAdmins []string) *Provisioner {
	set := make(map[string]struct{}, len(bootstrapAdmins))
	for _, v := range bootstrapAdmins {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			set[v] = struct{}{}
		}
	}
	return &Provisioner{users: users, bootstrap: set, now: time.Now, newID: uuid.New}
}

// Resolve returns the existing user for claims.Subject, or creates a new one.
// On an existing user it refreshes the username if the IdP changed it; the role
// is never modified here (promotion/demotion of an existing user is the role
// service's job). New bootstrap identities are created as admin.
func (p *Provisioner) Resolve(ctx context.Context, c Claims) (*User, error) {
	if c.Subject == "" {
		return nil, ErrInvalidClaims
	}
	u, err := p.users.GetBySubject(ctx, c.Subject)
	switch {
	case err == nil:
		if c.Username != "" && c.Username != u.Username {
			u.Username = c.Username
			// Best-effort refresh: a username collision (another user owns it)
			// must not fail login — the subject is the identity. Surface only by
			// keeping the in-memory value; the persisted error is intentionally
			// tolerated.
			_ = p.users.UpdateUsername(ctx, u.ID, c.Username)
		}
		return u, nil
	case errors.Is(err, ErrNotFound):
		role := identity.RoleUser
		if p.IsBootstrapAdmin(c.Subject, c.Email) {
			role = identity.RoleAdmin
		}
		subj := c.Subject
		var emailPtr *string
		if c.Email != "" {
			e := c.Email
			emailPtr = &e
		}
		nu := &User{
			ID:       p.newID(),
			Subject:  &subj,
			Email:    emailPtr,
			Username: pickUsername(c),
			Role:     role,
		}
		if err := p.users.Create(ctx, nu); err != nil {
			return nil, err
		}
		return nu, nil
	default:
		return nil, err
	}
}

// IsBootstrapAdmin is the SINGLE canonical bootstrap-match predicate, exported
// so the role service's demotion floor (Task 6) consults the SAME normalized set
// with the SAME rule. Rule: match email case-insensitively first; if email is
// empty or unmatched, fall back to the (lower-cased) subject. Task 6 calls this
// with the PERSISTED user's subject+email (dereferenced, "" when nil), so a
// bootstrap-by-email admin is correctly floored using only the stored User.
func (p *Provisioner) IsBootstrapAdmin(subject, email string) bool {
	if email != "" {
		if _, ok := p.bootstrap[strings.ToLower(strings.TrimSpace(email))]; ok {
			return true
		}
	}
	if subject != "" {
		if _, ok := p.bootstrap[strings.ToLower(strings.TrimSpace(subject))]; ok {
			return true
		}
	}
	return false
}

// pickUsername prefers the IdP username, falling back to email then subject so
// the NOT NULL username column is never empty.
func pickUsername(c Claims) string {
	if c.Username != "" {
		return c.Username
	}
	if c.Email != "" {
		return c.Email
	}
	return c.Subject
}
