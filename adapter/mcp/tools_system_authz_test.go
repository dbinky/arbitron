package mcp

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PR #21 (fail-open mcpNotifOwnerID): a MISSING identity must fail CLOSED — it
// must NOT collapse to the unrestricted admin scope (nil owner = see ALL
// notifications). It restricts to a non-matching owner (uuid.Nil) instead, the
// same fail-closed convention every other authz predicate this PR adds uses.
func TestMcpNotifOwnerID_NoIdentity_FailsClosed(t *testing.T) {
	got := mcpNotifOwnerID(context.Background())
	require.NotNil(t, got, "a missing identity must NOT yield the unrestricted admin scope (nil)")
	assert.Equal(t, uuid.Nil, *got, "missing identity restricts to a non-matching owner (fail closed)")
}

// An admin identity yields the unrestricted scope (nil owner = see all).
func TestMcpNotifOwnerID_Admin_Unrestricted(t *testing.T) {
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleAdmin,
	})
	assert.Nil(t, mcpNotifOwnerID(ctx), "an admin sees all notifications (no owner restriction)")
}

// A non-admin identity is restricted to their OWN notifications.
func TestMcpNotifOwnerID_NonAdmin_RestrictedToSelf(t *testing.T) {
	uid := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uid, Role: identity.RoleUser,
	})
	got := mcpNotifOwnerID(ctx)
	require.NotNil(t, got)
	assert.Equal(t, uid, *got, "a non-admin sees only their own notifications")
}
