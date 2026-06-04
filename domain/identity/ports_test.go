package identity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compile-time guards: ensure all repository and provider interfaces are
// satisfied by a minimal fake — catches signature drift immediately.
type fakeTokenRepoP struct{}

func (fakeTokenRepoP) Create(context.Context, *identity.Token) error                      { return nil }
func (fakeTokenRepoP) ListByUser(context.Context, uuid.UUID) ([]identity.Token, error)    { return nil, nil }
func (fakeTokenRepoP) GetByHash(context.Context, string) (*identity.Token, error)         { return nil, nil }
func (fakeTokenRepoP) Delete(context.Context, uuid.UUID, uuid.UUID) error                 { return nil }
func (fakeTokenRepoP) TouchLastUsed(context.Context, uuid.UUID) error                     { return nil }

type fakeDeviceCodeRepoP struct{}

func (fakeDeviceCodeRepoP) Create(context.Context, *identity.DeviceCode) error                          { return nil }
func (fakeDeviceCodeRepoP) GetByHash(context.Context, string) (*identity.DeviceCode, error)             { return nil, nil }
func (fakeDeviceCodeRepoP) GetByUserCode(context.Context, string) (*identity.DeviceCode, error)         { return nil, nil }
func (fakeDeviceCodeRepoP) Approve(context.Context, string, uuid.UUID) error                            { return nil }
func (fakeDeviceCodeRepoP) Deny(context.Context, string) error                                          { return nil }
func (fakeDeviceCodeRepoP) Consume(context.Context, string) error                                       { return nil }
func (fakeDeviceCodeRepoP) DeleteExpired(context.Context, time.Time) (int, error)                       { return 0, nil }

var (
	_ identity.TokenRepository      = (*fakeTokenRepoP)(nil)
	_ identity.DeviceCodeRepository = (*fakeDeviceCodeRepoP)(nil)
)

func TestSession_IsExpired(t *testing.T) {
	now := time.Now()
	assert.True(t, identity.Session{ExpiresAt: now}.IsExpired(now),
		"a session whose ExpiresAt == now is expired")
	assert.False(t, identity.Session{ExpiresAt: now.Add(time.Nanosecond)}.IsExpired(now))
	assert.False(t, identity.Session{ExpiresAt: now.Add(time.Hour)}.IsExpired(now))
}

func TestErrNotFound_IsSentinel(t *testing.T) {
	wrapped := fmt.Errorf("wrap: %w", identity.ErrNotFound)
	assert.True(t, errors.Is(wrapped, identity.ErrNotFound))
}

// TestDeviceCodeErrors_AreSentinels verifies that all RFC-8628 sentinel errors
// wrap correctly via %w so errors.Is works at HTTP-layer call sites.
func TestDeviceCodeErrors_AreSentinels(t *testing.T) {
	sentinels := []error{
		identity.ErrAuthorizationPending,
		identity.ErrAccessDenied,
		identity.ErrExpired,
		identity.ErrInvalidGrant,
		identity.ErrUnauthenticated,
		identity.ErrInvalidLabel,
	}
	for _, s := range sentinels {
		wrapped := fmt.Errorf("adapter: %w", s)
		assert.True(t, errors.Is(wrapped, s), "errors.Is must match %v through %%w wrapping", s)
	}
}

// Compile-time guards that fakes can satisfy the new ports.
type fakeSessionRepo struct{}

func (fakeSessionRepo) Create(context.Context, *identity.Session) error        { return nil }
func (fakeSessionRepo) Get(context.Context, string) (*identity.Session, error) { return nil, nil }
func (fakeSessionRepo) Delete(context.Context, string) error                   { return nil }
func (fakeSessionRepo) DeleteExpired(context.Context, time.Time) (int, error)  { return 0, nil }

type fakeOIDC struct{}

func (fakeOIDC) AuthCodeURL(state, nonce string) string { return "" }
func (fakeOIDC) Exchange(context.Context, string) (identity.Claims, error) {
	return identity.Claims{}, nil
}

var (
	_ identity.SessionRepository = (*fakeSessionRepo)(nil)
	_ identity.OIDCProvider      = (*fakeOIDC)(nil)
)

func TestPorts_FakesSatisfyInterfaces(t *testing.T) {
	ctx := context.Background()

	var sr identity.SessionRepository = fakeSessionRepo{}
	var op identity.OIDCProvider = fakeOIDC{}
	var tr identity.TokenRepository = fakeTokenRepoP{}
	var dc identity.DeviceCodeRepository = fakeDeviceCodeRepoP{}
	require.NotNil(t, sr)
	require.NotNil(t, op)
	require.NotNil(t, tr)
	require.NotNil(t, dc)

	s := &identity.Session{ID: "tok", UserID: uuid.New(), CreatedAt: time.Now(), ExpiresAt: time.Now()}
	require.NoError(t, sr.Create(ctx, s))

	tok := &identity.Token{ID: uuid.New(), UserID: uuid.New(), Label: "x", Prefix: "pre", TokenHash: "h"}
	require.NoError(t, tr.Create(ctx, tok))
	toks, err := tr.ListByUser(ctx, tok.UserID)
	require.NoError(t, err)
	_ = toks // nil is acceptable from a stub

	code := &identity.DeviceCode{HashedCode: "h", UserCode: "AB12-CD34", Status: identity.DeviceCodePending, ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, dc.Create(ctx, code))
	n, err := dc.DeleteExpired(ctx, time.Now())
	require.NoError(t, err)
	_ = n
}

// TestDeviceCodeStatus_ExactStrings verifies the RFC-8628 status constants match
// the wire strings the HTTP layer must produce. Drift here breaks polling clients.
func TestDeviceCodeStatus_ExactStrings(t *testing.T) {
	assert.Equal(t, identity.DeviceCodeStatus("pending"), identity.DeviceCodePending)
	assert.Equal(t, identity.DeviceCodeStatus("approved"), identity.DeviceCodeApproved)
	assert.Equal(t, identity.DeviceCodeStatus("denied"), identity.DeviceCodeDenied)
	assert.Equal(t, identity.DeviceCodeStatus("consumed"), identity.DeviceCodeConsumed)
}

// TestSession_IDIsRawCookieValue documents the SessionRepository contract:
// callers pass the RAW session token to Get and Delete (the cookie value as
// received from the browser). Hashing to id_hash is the adapter's responsibility,
// not the caller's. This test encodes that contract in a fake that stores by raw
// value, confirming the caller-side API is raw-token oriented.
func TestSession_IDIsRawCookieValue(t *testing.T) {
	ctx := context.Background()

	rawRepo := newRawSessionRepo()
	uid := uuid.New()
	now := time.Now()
	raw := "raw-cookie-abc123"
	s := &identity.Session{
		ID:        raw,
		UserID:    uid,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	require.NoError(t, rawRepo.Create(ctx, s))

	// Caller passes the same raw value to Get; the contract does not require the
	// caller to hash first.
	got, err := rawRepo.Get(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, uid, got.UserID)
	assert.Empty(t, got.ID,
		"Get must not re-populate ID — the adapter never stored the raw token")

	// Caller passes the raw value to Delete as well.
	require.NoError(t, rawRepo.Delete(ctx, raw))
	_, err = rawRepo.Get(ctx, raw)
	assert.ErrorIs(t, err, identity.ErrNotFound)
}

// rawSessionEntry holds one stored session keyed by the raw ID.
type rawSessionEntry struct {
	s  identity.Session
	ex time.Time
}

// rawSessionRepo is an in-memory SessionRepository that stores sessions by the
// raw ID — illustrating the caller-side contract (callers always pass raw tokens).
type rawSessionRepo struct {
	store map[string]rawSessionEntry
}

func newRawSessionRepo() *rawSessionRepo {
	return &rawSessionRepo{store: make(map[string]rawSessionEntry)}
}

func (r *rawSessionRepo) Create(_ context.Context, s *identity.Session) error {
	r.store[s.ID] = rawSessionEntry{s: *s, ex: s.ExpiresAt}
	return nil
}

func (r *rawSessionRepo) Get(_ context.Context, id string) (*identity.Session, error) {
	entry, ok := r.store[id]
	if !ok || time.Now().After(entry.ex) {
		return nil, fmt.Errorf("get: %w", identity.ErrNotFound)
	}
	s := entry.s
	s.ID = "" // the real adapter does NOT re-populate ID (it was never stored)
	return &s, nil
}

func (r *rawSessionRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.store[id]; !ok {
		return fmt.Errorf("delete: %w", identity.ErrNotFound)
	}
	delete(r.store, id)
	return nil
}

func (r *rawSessionRepo) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	n := 0
	for k, e := range r.store {
		if !now.Before(e.ex) {
			delete(r.store, k)
			n++
		}
	}
	return n, nil
}

var _ identity.SessionRepository = (*rawSessionRepo)(nil)

// TestToken_JSONMarshal_HashNeverLeaks verifies that json.Marshal of a Token
// struct never includes the TokenHash field, regardless of its value. This is
// enforced by the json:"-" tag and guards against accidental direct serialization
// of Token instead of the DTO.
func TestToken_JSONMarshal_HashNeverLeaks(t *testing.T) {
	tok := identity.Token{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Label:     "ci",
		Prefix:    "arb_abc",
		TokenHash: "SECRET-SHA256-MUST-NOT-APPEAR",
	}
	b, err := json.Marshal(tok)
	require.NoError(t, err)
	body := string(b)
	assert.NotContains(t, body, "SECRET-SHA256-MUST-NOT-APPEAR", "TokenHash value must not appear in JSON output")
	assert.NotContains(t, body, "TokenHash", "TokenHash field name must not appear in JSON output")
	assert.NotContains(t, body, "token_hash", "snake_case variant must not appear in JSON output")
}

// TestDeviceCode_JSONMarshal_HashedCodeNeverLeaks verifies that json.Marshal of a
// DeviceCode struct never includes the HashedCode field, enforced by json:"-".
func TestDeviceCode_JSONMarshal_HashedCodeNeverLeaks(t *testing.T) {
	dc := identity.DeviceCode{
		HashedCode: "SECRET-DEVICE-HASH-MUST-NOT-APPEAR",
		UserCode:   "AB12-CD34",
		Status:     identity.DeviceCodePending,
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	b, err := json.Marshal(dc)
	require.NoError(t, err)
	body := string(b)
	assert.NotContains(t, body, "SECRET-DEVICE-HASH-MUST-NOT-APPEAR", "HashedCode value must not appear in JSON output")
	assert.NotContains(t, body, "HashedCode", "HashedCode field name must not appear in JSON output")
	assert.NotContains(t, body, "hashed_code", "snake_case variant must not appear in JSON output")
}
