package config_test

import (
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Embedded default: with no override path, the compiled-in ruleset loads and the
// sonnet-4.6 model override floors xhigh -> high.
func TestLoadCapabilityRules_EmbeddedDefault(t *testing.T) {
	r, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)

	_, eff, err := r.Validate("claude-code", "sonnet-4.6", provider.Context200K, provider.EffortXHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, eff)
}

// Override merge: an override file replaces a backend entry per-backend. Narrowing
// codex to {default, high} means a requested medium has no graded level at or
// below it (only high remains) and is therefore rejected.
func TestLoadCapabilityRules_OverrideMerges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "capabilities.yaml")
	require.NoError(t, os.WriteFile(p, []byte("backends:\n  codex:\n    context_tiers: [default]\n    effort_levels: [default, high]\n"), 0o600))

	r, err := adapterconfig.LoadCapabilityRules(p)
	require.NoError(t, err)

	_, _, err = r.Validate("codex", "gpt-5.5", provider.ContextDefault, provider.EffortMedium)
	assert.Error(t, err, "expected medium rejected under narrowed codex rules")

	// Backends absent from the override keep their embedded defaults.
	_, eff, err := r.Validate("claude-code", "sonnet-4.6", provider.Context200K, provider.EffortXHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, eff)
}

// Malformed: an unknown enum value in the override file fails fast.
func TestLoadCapabilityRules_MalformedFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(p, []byte("backends:\n  codex:\n    effort_levels: [bogus]\n"), 0o600))

	_, err := adapterconfig.LoadCapabilityRules(p)
	assert.Error(t, err, "expected malformed enum to fail fast")
}

// Missing override path is not an error: the embedded default is used.
func TestLoadCapabilityRules_MissingOverrideIgnored(t *testing.T) {
	r, err := adapterconfig.LoadCapabilityRules(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	require.NoError(t, err)

	tier, eff, err := r.Validate("claude-code", "opus-4.8", provider.Context1M, provider.EffortHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.Context1M, tier)
	assert.Equal(t, provider.EffortHigh, eff)
}
