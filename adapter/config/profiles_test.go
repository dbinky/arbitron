package config_test

import (
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	domainconfig "github.com/arbitron/arbitron/domain/config"
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
		{Alias: "a", Kind: "chat", ConnectionRef: "azure-prod", Model: "m1"},
		{Alias: "b", Kind: "agent", Model: "m2"},
	}

	got := adapterconfig.ProfilesFromConfig(cfgs)
	require.Len(t, got, 2)
	assert.Equal(t, provider.Profile{
		SelectionMode: provider.SelectionPinned,
		Alias:         "a",
		Kind:          provider.ProfileKindChat,
		ConnectionRef: "azure-prod",
		Model:         "m1",
	}, got[0])
	assert.Equal(t, provider.Profile{Alias: "b", SelectionMode: provider.SelectionPinned, Kind: provider.ProfileKindAgent, Model: "m2"}, got[1])
}

// Failure/Error: a malformed kind value flows through the loader and mapping as
// a literal ProfileKind (no panic). The DB-backed DynamicProfileResolver no
// longer validates kinds at construction (the seed validator + workflow
// validation own that), so the literal kind is carried through Kind()/Resolve
// and rejected DOWNSTREAM by workflow.Validate (PROFILE_KIND_MISMATCH), not by
// the resolver constructor.
func TestProfilesFromConfig_MalformedKindCarriedForDownstreamRejection(t *testing.T) {
	cfgs := []domainconfig.ProfileConfig{
		{Alias: "weird", Kind: "tool", Model: "m"},
	}

	got := adapterconfig.ProfilesFromConfig(cfgs)
	require.Len(t, got, 1)
	assert.Equal(t, provider.ProfileKind("tool"), got[0].Kind)

	// Construction does not panic or reject; the literal kind is carried.
	resolver := adapterprovider.NewDynamicProfileResolver(
		adapterprovider.NewProfileCacheFromProfiles(got...), nil)
	kind, ok := resolver.Kind("weird")
	require.True(t, ok, "the profile is present in the registry")
	assert.Equal(t, provider.ProfileKind("tool"), kind,
		"the malformed kind is surfaced literally for downstream (workflow) rejection")
}

// Success: a YAML profile with connectionref round-trips through Load and
// ProfilesFromConfig correctly. This is the primary binding between the
// profile registry and the connection store (the no-key subscription path
// leaves connectionref empty; a non-empty ref names a stored connection).
func TestLoad_ProfileWithConnectionRef(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
profiles:
  - alias: "az-chat"
    kind: "chat"
    connectionref: "azure-prod"
    model: "gpt-4o"
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.Len(t, cfg.Profiles, 1)
	assert.Equal(t, "az-chat", cfg.Profiles[0].Alias)
	assert.Equal(t, "chat", cfg.Profiles[0].Kind)
	assert.Equal(t, "azure-prod", cfg.Profiles[0].ConnectionRef,
		"connectionref YAML key must unmarshall to ProfileConfig.ConnectionRef")
	assert.Equal(t, "gpt-4o", cfg.Profiles[0].Model)

	// ProfilesFromConfig must preserve the ConnectionRef all the way to the
	// domain Profile.
	got := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	require.Len(t, got, 1)
	assert.Equal(t, "azure-prod", got[0].ConnectionRef)
}

// Edge: with no profiles block in YAML, defaults supply the base default-chat
// and default-agent pair, both following the Anthropic sonnet tier. The default registry also
// ships the in-repo workflow aliases (covered by
// TestLoad_DefaultProfileRegistry_IncludesCommittedWorkflowAliases), so this
// test asserts the base pair is present rather than an exact count.
func TestLoad_ProfilesDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)

	byAlias := map[string]domainconfig.ProfileConfig{}
	for _, p := range cfg.Profiles {
		byAlias[p.Alias] = p
	}
	sonnet := &domainconfig.FollowTargetConfig{Provider: "anthropic", Tier: "sonnet"}
	assert.Equal(t, domainconfig.ProfileConfig{Alias: "default-chat", Kind: "chat", SelectionMode: "following", FollowTarget: sonnet}, byAlias["default-chat"])
	assert.Equal(t, domainconfig.ProfileConfig{Alias: "default-agent", Kind: "agent", SelectionMode: "following", FollowTarget: sonnet}, byAlias["default-agent"])
}

// A deployment config.yaml with no profiles block takes the profiles from the
// built-in defaults map, not the generated template: that copy must follow the
// same tiers, or such an install seeds nothing.
func TestLoad_NoProfilesBlockUsesFollowingDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  port: 8080\n"), 0o600))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)

	tiers := map[string]string{}
	for _, p := range cfg.Profiles {
		assert.Equal(t, "following", p.SelectionMode, "default %q must follow a tier", p.Alias)
		assert.Empty(t, p.Model, "default %q must not name a model that goes stale", p.Alias)
		require.NotNil(t, p.FollowTarget, "default %q must name a follow target", p.Alias)
		assert.Equal(t, "anthropic", p.FollowTarget.Provider, p.Alias)
		tiers[p.Alias] = p.FollowTarget.Tier
	}
	assert.Equal(t, map[string]string{
		"default-chat": "sonnet", "default-agent": "sonnet", "assessor": "sonnet",
		"interviewer": "sonnet", "reviewer": "sonnet", "coder": "opus", "planner": "opus",
	}, tiers)
}

// PR #18 item 1b: a profile may carry a per-profile AllowedTools allowlist
// (agent kinds only in practice). It must unmarshal from YAML as a list and
// map through ProfilesFromConfig to provider.Profile.AllowedTools.
func TestLoad_ProfilesAllowedToolsFromYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
profiles:
  - alias: "reviewer"
    kind: "agent"
    model: ""
    allowedtools: ["Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"]
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.Len(t, cfg.Profiles, 1)
	assert.Equal(t, []string{"Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"}, cfg.Profiles[0].AllowedTools)
}

func TestProfilesFromConfig_MapsAllowedTools(t *testing.T) {
	cfgs := []domainconfig.ProfileConfig{
		{Alias: "reviewer", Kind: "agent", AllowedTools: []string{"Read", "Bash(gh:*)"}},
	}
	got := adapterconfig.ProfilesFromConfig(cfgs)
	require.Len(t, got, 1)
	assert.Equal(t, []string{"Read", "Bash(gh:*)"}, got[0].AllowedTools)
}
