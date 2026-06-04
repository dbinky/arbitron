package identity_test

import (
	"context"
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
	var sr identity.SessionRepository = fakeSessionRepo{}
	var op identity.OIDCProvider = fakeOIDC{}
	require.NotNil(t, sr)
	require.NotNil(t, op)

	s := &identity.Session{ID: "tok", UserID: uuid.New(), CreatedAt: time.Now(), ExpiresAt: time.Now()}
	require.NoError(t, sr.Create(context.Background(), s))
}
