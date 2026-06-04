package identity_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithIdentity_FromContext_RoundTrip(t *testing.T) {
	uid := uuid.New()
	id := identity.Identity{UserID: uid, Username: "alice", Role: identity.RoleAdmin}
	ctx := identity.WithIdentity(context.Background(), id)

	got, ok := identity.FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, id, got)

	assert.NotPanics(t, func() {
		mustGot := identity.MustFromContext(ctx)
		assert.Equal(t, id, mustGot)
	})

	assert.True(t, identity.Identity{Role: identity.RoleAdmin}.IsAdmin())
	assert.False(t, identity.Identity{Role: identity.RoleUser}.IsAdmin())
}

func TestFromContext_Absent(t *testing.T) {
	got, ok := identity.FromContext(context.Background())
	assert.False(t, ok)
	assert.Equal(t, identity.Identity{}, got)
}

func TestModeFromString(t *testing.T) {
	assert.Equal(t, identity.ModeOrg, identity.ModeFromString("org"))
	assert.Equal(t, identity.ModeLocal, identity.ModeFromString("local"))
	assert.Equal(t, identity.ModeLocal, identity.ModeFromString(""))
	assert.Equal(t, identity.ModeLocal, identity.ModeFromString("garbage"))
}

func TestMustFromContext_PanicsWhenAbsent(t *testing.T) {
	require.Panics(t, func() {
		identity.MustFromContext(context.Background())
	})
}

// TestIdentityKey_NotForgeable proves the unexported context key prevents a
// different package from injecting or reading the identity value by guessing
// the key type.
func TestIdentityKey_NotForgeable(t *testing.T) {
	type otherKey struct{}
	forged := identity.Identity{Username: "attacker", Role: identity.RoleAdmin}
	ctx := context.WithValue(context.Background(), otherKey{}, forged)

	_, ok := identity.FromContext(ctx)
	assert.False(t, ok, "a value under a foreign key must not be returned by FromContext")
}
