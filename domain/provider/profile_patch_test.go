package provider_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

// strptr / toolsptr / cwptr / effptr are tiny helpers for building a sparse
// ProfilePatch in tests: a nil field means "absent (leave unchanged)", a non-nil
// field means "set to this value (including an explicit clear)".
func strptr(s string) *string { return &s }

// UpdatePatch is the merge-on-update path: it loads the existing profile and
// overlays ONLY the fields present in the patch, so omitting an optional field
// leaves it unchanged rather than blanking it. The merge lives in the domain
// service (not the MCP handler) so a future REST surface inherits it.
func TestProfileService_UpdatePatch_MergesOmittedFields(t *testing.T) {
	existing := provider.Profile{
		Alias:         "prod-agent",
		Kind:          provider.ProfileKindAgent,
		ConnectionRef: "openrouter-prod",
		Model:         "opus-4.8",
		Backend:       "claude-code",
		AllowedTools:  []string{"Read", "Edit"},
		ContextWindow: provider.Context1M,
		Effort:        provider.EffortHigh,
	}
	store := newMemProfileStore(existing)
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	// Admin tweaks ONLY kind; everything else is absent and must be preserved.
	stored, _, err := svc.UpdatePatch(admins(), "prod-agent", provider.ProfilePatch{
		Kind: strptr("agent"),
	})
	require.NoError(t, err)
	assert.Equal(t, "openrouter-prod", stored.ConnectionRef, "omitted connection_ref must be preserved, not blanked")
	assert.Equal(t, "opus-4.8", stored.Model, "omitted model must be preserved")
	assert.Equal(t, "claude-code", stored.Backend, "omitted backend must be preserved")
	assert.Equal(t, []string{"Read", "Edit"}, stored.AllowedTools, "omitted allowed_tools must be preserved")
	assert.Equal(t, provider.Context1M, stored.ContextWindow, "omitted context_window must be preserved")
	assert.Equal(t, provider.EffortHigh, stored.Effort, "omitted effort must be preserved")
	assert.Equal(t, 1, rl.calls, "a successful merge reloads exactly once")
}

// An explicitly cleared field (present, empty) IS applied — clearing
// connection_ref is distinguishable from omitting it.
func TestProfileService_UpdatePatch_ExplicitClearIsApplied(t *testing.T) {
	existing := provider.Profile{
		Alias: "p", Kind: provider.ProfileKindChat, ConnectionRef: "c", Model: "m",
	}
	store := newMemProfileStore(existing)
	svc := provider.NewProfileService(store, provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-cli": {Backend: "claude-cli"},
	}), &fakeReloader{})

	stored, _, err := svc.UpdatePatch(admins(), "p", provider.ProfilePatch{
		ConnectionRef: strptr(""), // explicit clear
	})
	require.NoError(t, err)
	assert.Equal(t, "", stored.ConnectionRef, "an explicit empty connection_ref clears the field")
	assert.Equal(t, "m", stored.Model, "an unrelated omitted field stays put")
}

// Authz still gates the patch path: a non-admin is forbidden and nothing is
// persisted or reloaded.
func TestProfileService_UpdatePatch_RequiresAdmin(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "p", Kind: provider.ProfileKindChat})
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	_, _, err := svc.UpdatePatch(users(), "p", provider.ProfilePatch{Kind: strptr("chat")})
	assert.ErrorIs(t, err, provider.ErrForbidden)
	assert.Equal(t, 0, store.writeCount())
	assert.Equal(t, 0, rl.calls)
}

// A patch against a missing alias surfaces ErrProfileNotFound (the load fails)
// and does not persist or reload.
func TestProfileService_UpdatePatch_MissingAlias(t *testing.T) {
	store := newMemProfileStore()
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	_, _, err := svc.UpdatePatch(admins(), "ghost", provider.ProfilePatch{Kind: strptr("chat")})
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	assert.Equal(t, 0, store.writeCount())
	assert.Equal(t, 0, rl.calls)
}

// The merged result is still capability-validated: patching effort to an
// inapplicable value is rejected and nothing is persisted.
func TestProfileService_UpdatePatch_ValidatesMergedResult(t *testing.T) {
	existing := provider.Profile{Alias: "c", Kind: provider.ProfileKindAgent, Backend: "codex"}
	store := newMemProfileStore(existing)
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	// codex exposes no context knob: patching a non-default tier must be rejected.
	cw := string(provider.Context1M)
	_, _, err := svc.UpdatePatch(admins(), "c", provider.ProfilePatch{ContextWindow: &cw})
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable)
	assert.Equal(t, 0, store.writeCount(), "a rejected merge persists nothing")
	assert.Equal(t, 0, rl.calls)
}
