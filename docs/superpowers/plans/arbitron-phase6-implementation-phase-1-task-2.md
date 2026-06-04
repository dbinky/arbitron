# Phase 1, Task 2: `domain/identity` Package — Identity Value, Role/Mode, Context Helpers, and the SessionRepository & OIDCProvider Ports

## Goal

Create the new `domain/identity` package: the request-scoped `Identity` value, the `Role` and `Mode` typed strings, the `WithIdentity`/`FromContext`/`MustFromContext` context helpers, and the two new driven ports `SessionRepository` and `OIDCProvider` (with their `Session` and `Claims` data types). This package is the dependency floor for all auth code; it imports nothing from `domain/instance` (and `domain/instance` must never import it — the auth middleware does the `User`→`Identity` mapping).

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §2 and §3.

## Dependencies

- Task 1 (config) for `AuthModeLocal`/`AuthModeOrg` string constants that `Mode.IsValid` / a `ModeFromString` helper can validate against — soft dependency; the typed `Mode` constants are defined here independently, so Task 2 can proceed in parallel, but the plan assumes Task 1's mode strings exist for the conversion helper.

## Background (current state)

- There is no `domain/identity` package today. No request carries a caller identity; `adapter/http/stream_handler.go` documents this explicitly ("There is no per-request user identity, session, or token anywhere").
- `domain/instance/ports.go` holds the `UserRepository` port (expanded in Task 3) and will hold the domain `User{ID, Subject, Username, Role}`. Per the pinned cross-cutting decision, `domain/instance` may import `domain/identity` (for the `Role` type) but `domain/identity` must NOT import `domain/instance` — there is no cycle.
- `github.com/google/uuid` is already a direct dependency (used throughout `domain/instance`).
- Sentinel-error precedent: `domain/instance/instance.go` defines `ErrNotFound = errors.New("not found")`. The new ports reuse a package-local `ErrNotFound` in `domain/identity` (ports return it; sqlite adapters wrap it with `%w`).

## Files to Create/Modify

### Create: `domain/identity/identity.go`

```go
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
}

// IsAdmin reports whether the identity has the admin role.
func (i Identity) IsAdmin() bool { return i.Role == RoleAdmin }

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
```

### Create: `domain/identity/ports.go`

```go
package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by SessionRepository.Get when the session is absent or
// expired. Adapters wrap it with fmt.Errorf %w so errors.Is works at call sites.
var ErrNotFound = errors.New("identity: not found")

// Session is a server-side login session. ID is the RAW high-entropy token that
// lives in the browser cookie; the store persists only sha256(ID) (see the
// sqlite adapter), so a DB read leak cannot mint a session.
type Session struct {
	ID        string // raw cookie token (never persisted as-is)
	UserID    uuid.UUID
	CreatedAt time.Time
	ExpiresAt time.Time
}

// IsExpired reports whether the session is past its expiry relative to now.
func (s Session) IsExpired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

// SessionRepository persists login sessions. Arbitron is the session authority;
// OIDC is only the login mechanism, so request-time auth consults ONLY this port.
type SessionRepository interface {
	Create(ctx context.Context, s *Session) error
	// Get returns the session for the raw token, or ErrNotFound when absent OR
	// expired (the adapter hashes the token and filters expires_at server-side).
	Get(ctx context.Context, id string) (*Session, error)
	Delete(ctx context.Context, id string) error // logout
	// DeleteExpired removes every row whose expires_at <= now; returns the count.
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// Claims are the verified identity claims returned by the IdP after a successful
// authorization-code exchange. Subject is the IdP-stable identifier used to
// resolve/provision the local user.
type Claims struct {
	Subject  string
	Username string
	Email    string
}

// OIDCProvider abstracts the IdP so the OIDC library stays in adapter/oidc.
// AuthCodeURL builds the redirect to the IdP; Exchange swaps the returned code
// for verified Claims (ID-token signature + nonce verified inside the adapter).
type OIDCProvider interface {
	AuthCodeURL(state, nonce string) string
	Exchange(ctx context.Context, code string) (Claims, error)
}
```

### Optional helper (recommended): `domain/identity/mode.go`

A `ModeFromString` that maps the config string (`config.AuthModeLocal`/`Org`) to the typed `Mode`, keeping the conversion in one place (the middleware in Task 6 needs it). Keep it dependency-free (take a plain `string`, not a config type), so `domain/identity` stays import-clean:

```go
package identity

func ModeFromString(s string) Mode {
	switch s {
	case "org":
		return ModeOrg
	default:
		return ModeLocal // unknown/empty -> local; config.validateAuth already rejects bad values
	}
}
```

## Testing (strict TDD — five scenario categories)

All tests in `domain/identity/identity_test.go` and `domain/identity/ports_test.go` (package `identity`). Write first.

- **Happy path** — `TestWithIdentity_FromContext_RoundTrip`: `WithIdentity` then `FromContext` returns the same `Identity` (UserID, Username, Role) with `ok == true`; `MustFromContext` returns it without panicking. `Identity{Role: RoleAdmin}.IsAdmin()` is true; `RoleUser` is false.
- **Input validation** — `TestFromContext_Absent`: a bare `context.Background()` yields `ok == false` and the zero `Identity`. `TestModeFromString`: `"org"`→`ModeOrg`, `"local"`/`""`/`"garbage"`→`ModeLocal`.
- **Error/failure** — `TestMustFromContext_PanicsWhenAbsent`: `MustFromContext(context.Background())` panics (assert via `require.Panics`); confirms the "route bypassed middleware" invariant is enforced, not silently defaulted.
- **Boundary** — `TestSession_IsExpired`: a session with `ExpiresAt == now` is expired (`!now.Before(now)` is true); `ExpiresAt == now.Add(time.Nanosecond)` is not; a far-future expiry is not. `ErrNotFound` is a distinct sentinel (`errors.Is(fmt.Errorf("wrap: %w", ErrNotFound), ErrNotFound)` is true).
- **Isolation/security** — `TestIdentityKey_NotForgeable`: a value stored under a *different* key type in another package (simulate with a local `type otherKey struct{}`) is NOT returned by `FromContext` (proves the unexported key prevents cross-package collision/forgery); and a compile-time guard `var _ SessionRepository = (*fakeSessionRepo)(nil)` plus `var _ OIDCProvider = (*fakeOIDC)(nil)` in the test package confirm fakes can satisfy the new ports. Add a package-boundary note/test asserting `domain/identity` has no import of `domain/instance` (a simple test reading `go list -deps` output, or just an architectural comment; the real guard is the build — an instance import would create a cycle once Task 3 makes instance import identity).

## Acceptance Criteria

- `domain/identity` exists with `Identity`, `Role`(`RoleUser`/`RoleAdmin`), `Mode`(`ModeLocal`/`ModeOrg`), `IsAdmin`, and the three context helpers.
- `SessionRepository`, `OIDCProvider`, `Session`, `Claims`, and `ErrNotFound` are defined.
- `domain/identity` imports only stdlib + `github.com/google/uuid`; it does NOT import `domain/instance`.
- `go build ./...` and `go test ./domain/identity/...` pass.
