package config_test

import (
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	domainconfig "github.com/arbitron/arbitron/domain/config"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy path: a YAML file with a profiles list unmarshals into []ProfileConfig
// with the correct alias/kind/model for each entry.
func TestLoad_ProfilesFromYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
profiles:
  - alias: "fast-chat"
    kind: "chat"
    model: "claude-haiku"
  - alias: "deep-agent"
    kind: "agent"
    model: "claude-opus"
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.Len(t, cfg.Profiles, 2)
	assert.Equal(t, domainconfig.ProfileConfig{Alias: "fast-chat", Kind: "chat", Model: "claude-haiku"}, cfg.Profiles[0])
	assert.Equal(t, domainconfig.ProfileConfig{Alias: "deep-agent", Kind: "agent", Model: "claude-opus"}, cfg.Profiles[1])
}

// Success: ProfilesFromConfig maps []ProfileConfig -> []provider.Profile,
// converting the Kind string into a ProfileKind.
func TestProfilesFromConfig_Mapping(t *testing.T) {
	cfgs := []domainconfig.ProfileConfig{
		{Alias: "a", Kind: "chat", ConnectionRef: "azure-prod", Model: "m1", Params: map[string]any{"temperature": 0.2}},
		{Alias: "b", Kind: "agent", Model: "m2"},
	}

	got := adapterconfig.ProfilesFromConfig(cfgs)
	require.Len(t, got, 2)
	assert.Equal(t, provider.Profile{
		Alias:         "a",
		Kind:          provider.ProfileKindChat,
		ConnectionRef: "azure-prod",
		Model:         "m1",
		Params:        map[string]any{"temperature": 0.2},
	}, got[0])
	assert.Equal(t, provider.Profile{Alias: "b", Kind: provider.ProfileKindAgent, Model: "m2"}, got[1])
}

// Failure/Error: a malformed kind value flows through the loader and mapping as
// a literal ProfileKind (no panic) and is rejected downstream by the resolver.
func TestProfilesFromConfig_MalformedKindRejectedDownstream(t *testing.T) {
	cfgs := []domainconfig.ProfileConfig{
		{Alias: "weird", Kind: "tool", Model: "m"},
	}

	got := adapterconfig.ProfilesFromConfig(cfgs)
	require.Len(t, got, 1)
	assert.Equal(t, provider.ProfileKind("tool"), got[0].Kind)

	_, err := adapterprovider.NewStaticProfileResolver(got, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool")
}

// Edge: with no profiles block in YAML, defaults supply default-chat and
// default-agent, and the empty model is preserved.
func TestLoad_ProfilesDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.Len(t, cfg.Profiles, 2)

	byAlias := map[string]domainconfig.ProfileConfig{}
	for _, p := range cfg.Profiles {
		byAlias[p.Alias] = p
	}
	assert.Equal(t, domainconfig.ProfileConfig{Alias: "default-chat", Kind: "chat", Model: ""}, byAlias["default-chat"])
	assert.Equal(t, domainconfig.ProfileConfig{Alias: "default-agent", Kind: "agent", Model: ""}, byAlias["default-agent"])
}
