package config_test

import (
	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestFollowingProfilesYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`profiles:
  - alias: worker
    kind: agent
    model: Vendor/Version-X
    selection_mode: following
    follow_target:
      provider: Vendor
      tier: Premium
  - alias: explicit
    kind: chat
    model: Specific-Y
    selection_mode: pinned
  - alias: legacy
    kind: chat
    model: Specific-Z
`), 0600))
	cfg, err := configadapter.Load(path)
	require.NoError(t, err)
	profiles := configadapter.ProfilesFromConfig(cfg.Profiles)
	require.Len(t, profiles, 3)
	assert.Equal(t, provider.SelectionFollowing, profiles[0].SelectionMode)
	require.NotNil(t, profiles[0].FollowTarget)
	assert.Equal(t, provider.FollowTarget{Provider: "Vendor", Tier: "Premium"}, *profiles[0].FollowTarget)
	assert.Equal(t, "Vendor/Version-X", profiles[0].Model)
	assert.Equal(t, provider.SelectionPinned, profiles[1].SelectionMode)
	assert.Equal(t, provider.SelectionPinned, profiles[2].SelectionMode)
	profiles[0].FollowTarget.Tier = "mutated"
	assert.Equal(t, "Premium", cfg.Profiles[0].FollowTarget.Tier)
}
