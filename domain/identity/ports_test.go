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
