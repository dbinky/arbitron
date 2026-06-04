// Package identity defines the request-scoped caller identity, the role/mode
// vocabulary, and the auth driven ports (sessions, OIDC). It is the dependency
// floor for all authentication code and imports nothing from domain/instance —
// the auth middleware maps a persisted instance.User to an identity.Identity.
package identity

import (
	"context"

	"github.com/google/uuid"
)

// Role is a user's authorization role.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Mode is the server operating mode (mirrors config.AuthMode* strings).
type Mode string

const (
	ModeLocal Mode = "local"
	ModeOrg   Mode = "org"
)

// Identity is the authenticated caller for one request. It is carried in
// context.Context (the single pragmatic-hex allowance) and read by domain
// services via FromContext so REST/WS/MCP all inherit the same authz.
type Identity struct {
	UserID   uuid.UUID
	Username string
	Role     Role
	// Departments is the caller's effective membership: persisted (claim-derived)
	// departments unioned with config-derived led departments, all normalized.
	// In local mode this is the single default group. Later phases authorize
	// department-scoped reads/writes against this set.
	Departments []string
	// LedDepartments is the subset of departments the caller leads (config-derived,
	// not persisted). Empty in local mode. A lead may intervene on any instance in
	// these departments and publish their workflows to public.
	LedDepartments []string
}

// IsAdmin reports whether the identity has the admin role.
func (i Identity) IsAdmin() bool { return i.Role == RoleAdmin }

// InDepartment reports whether the caller is a member of dept. dept must be a
// normalized (trimmed, lower-cased) department name; the membership set is
// already normalized.
func (i Identity) InDepartment(dept string) bool {
	for _, d := range i.Departments {
		if d == dept {
			return true
		}
	}
	return false
}

// LeadsDepartment reports whether the caller is a configured lead of dept. dept
// must be a normalized department name.
func (i Identity) LeadsDepartment(dept string) bool {
	for _, d := range i.LedDepartments {
		if d == dept {
			return true
		}
	}
	return false
}

// identityKey is an unexported context key type so no other package can collide
// with or read the identity value by guessing the key.
type identityKey struct{}

// WithIdentity returns a child context carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext returns the identity carried in ctx, if any.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// MustFromContext returns the identity carried in ctx or panics. Absence is a
// PROGRAMMING error: the auth middleware always sets an identity before any
// handler runs, so a missing identity means a route bypassed the middleware.
func MustFromContext(ctx context.Context) Identity {
	id, ok := FromContext(ctx)
	if !ok {
		panic("identity: no identity in context (route not behind auth middleware)")
	}
	return id
}

// ModeFromString maps a config mode string (config.AuthModeLocal/Org) to the
// typed Mode. It takes a plain string (not a config type) so domain/identity
// stays import-clean. An unknown/empty value maps to ModeLocal; config
// validation already rejects genuinely bad values before this is reached.
func ModeFromString(s string) Mode {
	switch s {
	case "org":
		return ModeOrg
	default:
		return ModeLocal
	}
}
