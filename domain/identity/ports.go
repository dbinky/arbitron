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

// ErrUnauthenticated is returned by domain services when the request carries no
// identity in context. The auth middleware should already reject such requests
// in org mode; the service check is defense-in-depth.
var ErrUnauthenticated = errors.New("identity: unauthenticated")

// ErrInvalidLabel is returned by TokenService.Mint when the token label is empty
// or whitespace-only.
var ErrInvalidLabel = errors.New("identity: token label is required")

// Device-authorization-grant sentinels (RFC 8628). They map to the standard
// token-endpoint error codes at the HTTP layer.
var (
	// ErrAuthorizationPending: the row is approved-pending; the client should
	// keep polling.
	ErrAuthorizationPending = errors.New("identity: authorization_pending")
	// ErrAccessDenied: the approver denied the request (terminal).
	ErrAccessDenied = errors.New("identity: access_denied")
	// ErrExpired: the device code expired before approval/consumption (terminal).
	ErrExpired = errors.New("identity: expired_token")
	// ErrInvalidGrant: the device code is unknown, already consumed, or raced
	// (terminal).
	ErrInvalidGrant = errors.New("identity: invalid_grant")
)

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

// Token is the stored, non-secret view of a personal API token. The plaintext
// arb_<prefix>_<secret> is NEVER held here; only the prefix (for display) and
// metadata are. TokenHash is the sha256(full token) used for lookup and is
// never echoed on any read surface.
type Token struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Label      string
	Prefix     string     // clear, non-secret, for display
	TokenHash  string     `json:"-"` // sha256 hex; internal only, never serialized to clients
	CreatedAt  time.Time
	LastUsedAt *time.Time // nil until first use
}

// TokenRepository persists personal API tokens. Implementations wrap domain
// sentinels (ErrNotFound) via %w so errors.Is mapping works at call sites.
//
// SECURITY: the plaintext token is NEVER passed to or held by this port; only
// the sha256 hash (for lookup) and the clear prefix (for display) are stored.
type TokenRepository interface {
	Create(ctx context.Context, t *Token) error
	ListByUser(ctx context.Context, userID uuid.UUID) ([]Token, error)
	GetByHash(ctx context.Context, tokenHash string) (*Token, error) // ErrNotFound if absent
	Delete(ctx context.Context, id, userID uuid.UUID) error          // ErrNotFound if no owned row
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}

// DeviceCodeStatus is the lifecycle state of an OAuth 2.0 device-grant row.
type DeviceCodeStatus string

const (
	DeviceCodePending  DeviceCodeStatus = "pending"
	DeviceCodeApproved DeviceCodeStatus = "approved"
	DeviceCodeDenied   DeviceCodeStatus = "denied"
	DeviceCodeConsumed DeviceCodeStatus = "consumed"
)

// DeviceCode is a stored device-authorization row. SECURITY: the polling secret
// (the raw device_code) is NEVER held here — only its sha256 (HashedCode). The
// short UserCode is low-entropy and shown to the human approver, so it is clear.
type DeviceCode struct {
	HashedCode string `json:"-"` // sha256 of the polling secret; never the raw code
	UserCode   string // short, human, shown to the approver
	Status     DeviceCodeStatus
	UserID     *uuid.UUID // nil until approved
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// DeviceCodeRepository persists device-authorization grant rows. Adapters wrap
// ErrNotFound via %w. Approve/Consume return ErrNotFound when their status guard
// matched zero rows (nothing pending to approve / nothing approved to consume).
type DeviceCodeRepository interface {
	Create(ctx context.Context, dc *DeviceCode) error
	GetByHash(ctx context.Context, hashedCode string) (*DeviceCode, error)   // ErrNotFound
	GetByUserCode(ctx context.Context, userCode string) (*DeviceCode, error) // ErrNotFound
	Approve(ctx context.Context, userCode string, userID uuid.UUID) error    // ErrNotFound if not pending
	Deny(ctx context.Context, userCode string) error
	Consume(ctx context.Context, hashedCode string) error // ErrNotFound if not 'approved'
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
