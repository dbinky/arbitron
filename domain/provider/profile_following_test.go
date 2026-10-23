package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFollowingProfilePatchParsingAndOwnership(t *testing.T) {
	base := Profile{Alias: "worker", Model: "Vendor/Version-X", SelectionMode: SelectionFollowing, FollowTarget: &FollowTarget{Provider: "vendor", Tier: "premium"}, AllowedTools: []string{"Read"}}
	got, err := (ProfilePatch{}).apply(base)
	require.NoError(t, err)
	require.NotNil(t, got.FollowTarget)
	assert.Equal(t, *base.FollowTarget, *got.FollowTarget)
	got.FollowTarget.Tier = "changed"
	got.AllowedTools[0] = "Edit"
	assert.Equal(t, "premium", base.FollowTarget.Tier)
	assert.Equal(t, "Read", base.AllowedTools[0])
	var cleared *FollowTarget
	mode := "pinned"
	got, err = (ProfilePatch{SelectionMode: &mode, FollowTarget: &cleared}).apply(base)
	require.NoError(t, err)
	assert.Nil(t, got.FollowTarget)
	assert.Equal(t, SelectionPinned, got.SelectionMode)
	_, err = (ProfilePatch{FollowTarget: &cleared}).apply(base)
	assert.Error(t, err)
	mode = "unknown"
	_, err = (ProfilePatch{SelectionMode: &mode}).apply(base)
	assert.Error(t, err)
	target := &FollowTarget{Provider: " Vendor ", Tier: " PREMIUM "}
	got, err = (ProfilePatch{FollowTarget: &target}).apply(base)
	require.NoError(t, err)
	assert.Equal(t, FollowTarget{Provider: "vendor", Tier: "premium"}, *got.FollowTarget)
	assert.Equal(t, "Vendor/Version-X", got.Model)
	got.FollowTarget.Provider = "other"
	assert.Equal(t, " Vendor ", target.Provider)
	target = &FollowTarget{Provider: "vendor", Tier: "bad tier"}
	_, err = (ProfilePatch{FollowTarget: &target}).apply(base)
	assert.Error(t, err)
}

func TestProfileSelectionDefaultsPinned(t *testing.T) {
	p, err := NormalizeSelection(Profile{Alias: "old", Model: "X"})
	require.NoError(t, err)
	assert.Equal(t, SelectionPinned, p.SelectionMode)
	assert.Nil(t, p.FollowTarget)
}
