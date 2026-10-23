package config_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestFollowingProfileConfigSelectionForms(t *testing.T) {
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal([]byte(`profiles:
  - alias: worker
    selection_mode: following
    follow_target:
      provider: vendor
      tier: premium
  - alias: pinned
    selection_mode: pinned
  - alias: legacy
`), &cfg))
	require.Len(t, cfg.Profiles, 3)
	assert.Equal(t, "following", cfg.Profiles[0].SelectionMode)
	require.NotNil(t, cfg.Profiles[0].FollowTarget)
	assert.Equal(t, config.FollowTargetConfig{Provider: "vendor", Tier: "premium"}, *cfg.Profiles[0].FollowTarget)
	assert.Equal(t, "pinned", cfg.Profiles[1].SelectionMode)
	assert.Nil(t, cfg.Profiles[1].FollowTarget)
	assert.Empty(t, cfg.Profiles[2].SelectionMode, "omission is carried to the pinned-default conversion seam")
	assert.Nil(t, cfg.Profiles[2].FollowTarget)
}
