package instance

import (
	"context"
	"errors"
	"fmt"
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
	// EmailVerified is the IdP's email_verified claim. It gates whether Email may
	// drive bootstrap-admin matching: an unverified email is attacker-controllable
	// (the user can self-assert any address at the IdP), so only a verified email
	// is trusted for privilege bootstrap. Subject matching is unaffected — the
	// subject is the verifier-checked, IdP-stable identifier.
	EmailVerified bool
	// Groups is the raw (un-normalized) group-claim values from the verified ID
	// token; the provisioner normalizes them via identity.ResolveDepartments and
	// snapshots the result. Nil => the user falls back to the default group.
	Groups []string
}

// Provisioner resolves verified Claims to a persisted User, creating one on
// first sight. A subject/email on the bootstrap list is created as admin. It is
// a pure domain service over the UserRepository port (no infra import).
type Provisioner struct {
	users     UserRepository
	bootstrap map[string]struct{} // lower-cased subjects AND emails
	groups    identity.GroupConfig
	now       func() time.Time
	newID     func() uuid.UUID
}

// NewProvisioner builds a Provisioner, normalizing the bootstrap list to a
// lower-cased set for O(1), case-insensitive membership. groups supplies the
// default-group fallback used when snapshotting departments at login.
func NewProvisioner(users UserRepository, bootstrapAdmins []string, groups identity.GroupConfig) *Provisioner {
	set := make(map[string]struct{}, len(bootstrapAdmins))
	for _, v := range bootstrapAdmins {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			set[v] = struct{}{}
		}
	}
	return &Provisioner{users: users, bootstrap: set, groups: groups, now: time.Now, newID: uuid.New}
}

// Resolve returns the existing user for claims.Subject, or creates a new one.
// On EVERY login it (1) re-evaluates the admin role from the live bootstrap-admin
// config (promote/demote so config is authoritative — spec §Roles), and
// (2) snapshots the user's departments from the verified group claim. Username is
// refreshed if the IdP changed it.
func (p *Provisioner) Resolve(ctx context.Context, c Claims) (*User, error) {
	if c.Subject == "" {
		return nil, ErrInvalidClaims
	}

	// SECURITY: only a VERIFIED email may drive bootstrap-admin matching (an
	// unverified email is attacker-controllable at the IdP). Subject is always
	// trusted (signature-verified, IdP-stable).
	matchEmail := ""
	if c.EmailVerified {
		matchEmail = c.Email
	}
	wantAdmin := p.IsBootstrapAdmin(c.Subject, matchEmail)
	wantRole := identity.RoleUser
	if wantAdmin {
		wantRole = identity.RoleAdmin
	}

	u, err := p.users.GetBySubject(ctx, c.Subject)
	switch {
	case err == nil:
		if c.Username != "" && c.Username != u.Username {
			u.Username = c.Username
			// Best-effort: a username collision must not fail login.
			_ = p.users.UpdateUsername(ctx, u.ID, c.Username)
		}
		// Live admin re-evaluation: config is authoritative at login. This
		// promotes a newly-listed user and DEMOTES one removed from the list,
		// bypassing the role-API demotion floors (which guard the runtime API,
		// not config-driven login).
		if u.Role != wantRole {
			if err := p.users.UpdateRole(ctx, u.ID, string(wantRole)); err != nil {
				return nil, fmt.Errorf("re-evaluating role at login: %w", err)
			}
			u.Role = wantRole
		}
	case errors.Is(err, ErrNotFound):
		subj := c.Subject
		// SECURITY: persist the email ONLY when it is VERIFIED. The stored email
		// later drives privilege-bearing matching — lead resolution in the auth
		// middleware (enrich → identity.LedDepartments) and the bootstrap-admin
		// demotion floor (IsBootstrapAdmin over the persisted User). An unverified
		// email is attacker-controllable at the IdP, so it must never reach storage,
		// mirroring the matchEmail gate above that already protects admin promotion.
		var emailPtr *string
		if c.EmailVerified && c.Email != "" {
			e := c.Email
			emailPtr = &e
		}
		nu := &User{
			ID:       p.newID(),
			Subject:  &subj,
			Email:    emailPtr,
			Username: pickUsername(c),
			Role:     wantRole,
		}
		if err := p.users.Create(ctx, nu); err != nil {
			return nil, err
		}
		u = nu
	default:
		return nil, err
	}

	// Department snapshot (RBAC Phase 1): replace the user's stored departments
	// with the normalized, default-group-fallback set derived from this login's
	// group claim. Non-interactive triggers later read this stored set.
	depts := identity.ResolveDepartments(c.Groups, p.groups)
	if err := p.users.ReplaceDepartments(ctx, u.ID, depts); err != nil {
		return nil, fmt.Errorf("snapshotting departments at login: %w", err)
	}
	return u, nil
}

// ResolveByEmail resolves a VERIFIED edge token to a persisted User, creating
// one on first sight. subject is the edge token's stable IdP-verified subject
// claim ("sub"/"oid"); it is "" when the token carries none. email is the
// verified email claim.
//
// SECURITY — DEPLOYMENT TRUST CONTRACT: edge mode trusts the email claim because
// the edge verifier checked the token signature/issuer/audience/expiry, which
// proves the token was MINTED by your Cloudflare Access tenant — but NOT that
// Access validated the underlying email ADDRESS. The bootstrap-admin allow-list
// is identity-keyed, so EDGE MODE REQUIRES the Cloudflare Access application to
// be backed by an IdP that verifies email ownership; otherwise an attacker who
// can self-assert an allow-listed email at a non-verifying IdP would be promoted
// to admin (vertical privilege escalation). To narrow that exposure, this method
// PREFERS the stable verified subject for bootstrap-admin matching when the token
// carries one (IsBootstrapAdmin checks subject AND email): operators SHOULD
// allow-list the subject/oid claim rather than the free-text email where their
// IdP exposes one. When no subject is present it falls back to email matching
// (the documented contract above then applies in full).
//
// Admin status is re-evaluated on every call from the live bootstrap list (config
// authoritative). The verified subject is persisted (so it becomes the stable
// identity key); the user is looked up by email for idempotency with the prior
// edge contract. Departments are snapshotted from the edge-forwarded group claim
// via the same ResolveDepartments chokepoint the OIDC path uses. A repository
// error surfaces as a non-ErrInvalidClaims error so the middleware can
// distinguish a 500 (we could not check) from a 401 (no credential).
func (p *Provisioner) ResolveByEmail(ctx context.Context, subject, email string, groups []string) (*User, error) {
	subject = strings.TrimSpace(subject)
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, ErrInvalidClaims
	}
	// Prefer the verified subject for admin matching; fall back to email.
	wantRole := identity.RoleUser
	if p.IsBootstrapAdmin(subject, email) {
		wantRole = identity.RoleAdmin
	}
	u, err := p.users.GetByEmail(ctx, email)
	switch {
	case err == nil:
		if u.Role != wantRole {
			if err := p.users.UpdateRole(ctx, u.ID, string(wantRole)); err != nil {
				return nil, fmt.Errorf("re-evaluating role at edge login: %w", err)
			}
			u.Role = wantRole
		}
	case errors.Is(err, ErrNotFound):
		e := email
		// Persist the verified subject when present so the user is keyed on the
		// stable IdP identifier rather than the free-text email alone.
		var subjPtr *string
		if subject != "" {
			s := subject
			subjPtr = &s
		}
		nu := &User{
			ID:       p.newID(),
			Subject:  subjPtr,
			Email:    &e,
			Username: email, // edge tokens carry no username; email is the display name
			Role:     wantRole,
		}
		if err := p.users.Create(ctx, nu); err != nil {
			return nil, err
		}
		u = nu
	default:
		return nil, err
	}
	depts := identity.ResolveDepartments(groups, p.groups)
	if err := p.users.ReplaceDepartments(ctx, u.ID, depts); err != nil {
		return nil, fmt.Errorf("snapshotting departments at edge login: %w", err)
	}
	return u, nil
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
