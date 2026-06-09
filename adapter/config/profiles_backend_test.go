package config_test

import (
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ProfilesFromConfig maps ProfileConfig.Backend -> provider.Profile.Backend.
func TestProfilesFromConfig_MapsBackend(t *testing.T) {
	cfgs := []domainconfig.ProfileConfig{
		{Alias: "a", Kind: "agent", Model: "m", Backend: "opencode"},
	}
	got := adapterconfig.ProfilesFromConfig(cfgs)
	require.Len(t, got, 1)
	assert.Equal(t, "opencode", got[0].Backend)
}
