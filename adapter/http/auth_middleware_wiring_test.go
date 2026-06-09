package http

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The per-mode nil-invariant is enforced at construction time: a half-wired mode
// panics at wire time (a startup/programmer error) rather than nil-dereffing on
// the request path. These tests pin that the named sub-structs + assertModeWired
// fallback actually fire.

func wiringGroups() identity.GroupConfig {
	return identity.GroupConfig{DefaultGroup: "default"}
}

func TestAuthMiddleware_EdgeMissingVerifier_PanicsAtWireTime(t *testing.T) {
	assert.Panics(t, func() {
		NewEdgeAuthMiddleware(cfHeader, nil /* verifier */, &fakeEmailProvisioner{}, newFakeUserRepo(), wiringGroups())
	}, "edge mode without a verifier must panic at construction")
}

func TestAuthMiddleware_EdgeMissingProvisioner_PanicsAtWireTime(t *testing.T) {
	assert.Panics(t, func() {
		NewEdgeAuthMiddleware(cfHeader, &fakeEdgeVerifier{}, nil /* provisioner */, newFakeUserRepo(), wiringGroups())
	}, "edge mode without a provisioner must panic at construction")
}

func TestAuthMiddleware_OrgMissingSessions_PanicsAtWireTime(t *testing.T) {
	assert.Panics(t, func() {
		NewAuthMiddleware(identity.ModeOrg, "sid", nil /* sessions */, nil, newFakeUserRepo(), wiringGroups())
	}, "org mode without a session repository must panic at construction")
}

func TestAuthMiddleware_MissingUsers_PanicsAtWireTime(t *testing.T) {
	assert.Panics(t, func() {
		NewAuthMiddleware(identity.ModeLocal, "", nil, nil, nil /* users */, wiringGroups())
	}, "every mode requires a user repository")
}

// Sanity: correctly-wired constructors do NOT panic and select the right mode.
func TestAuthMiddleware_WellWired_NoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		m := NewAuthMiddleware(identity.ModeLocal, "", nil, nil, newFakeUserRepo(), wiringGroups())
		assert.NotNil(t, m.local)
		assert.Nil(t, m.org)
		assert.Nil(t, m.edge)
	})
	require.NotPanics(t, func() {
		m := NewEdgeAuthMiddleware(cfHeader, &fakeEdgeVerifier{}, &fakeEmailProvisioner{}, newFakeUserRepo(), wiringGroups())
		assert.NotNil(t, m.edge)
		assert.Nil(t, m.org)
		assert.Nil(t, m.local)
	})
}
