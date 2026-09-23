package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/identity"
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

// Malformed context tier: an unknown context_tiers value fails fast with a
// message naming the offending backend (the parseTiers error path).
func TestLoadCapabilityRules_MalformedContextTier(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(p, []byte("backends:\n  mybackend:\n    context_tiers: [bogus]\n    effort_levels: [default]\n"), 0o600))

	_, err := adapterconfig.LoadCapabilityRules(p)
	require.Error(t, err, "malformed context_tier must fail fast")
	assert.ErrorContains(t, err, "mybackend", "error must name the offending backend")
}

// Malformed effort_floor key: an unknown effort string as a floor map key fails
// fast (the ParseEffort error path inside the floor-key loop).
func TestLoadCapabilityRules_MalformedEffortFloorKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	yaml := "backends:\n  mybackend:\n    context_tiers: [default]\n    effort_levels: [default, high]\n    model_overrides:\n      mymodel:\n        effort_floor: {boguskey: high}\n"
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))

	_, err := adapterconfig.LoadCapabilityRules(p)
	require.Error(t, err, "malformed effort_floor key must fail fast")
	assert.ErrorContains(t, err, "mybackend", "error must name the offending backend")
	assert.ErrorContains(t, err, "mymodel", "error must name the offending model")
}

// Malformed effort_floor value: an unknown effort string as a floor map value
// fails fast (the ParseEffort error path inside the floor-value loop).
func TestLoadCapabilityRules_MalformedEffortFloorValue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	yaml := "backends:\n  mybackend:\n    context_tiers: [default]\n    effort_levels: [default, high]\n    model_overrides:\n      mymodel:\n        effort_floor: {xhigh: bogusval}\n"
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))

	_, err := adapterconfig.LoadCapabilityRules(p)
	require.Error(t, err, "malformed effort_floor value must fail fast")
	assert.ErrorContains(t, err, "mybackend", "error must name the offending backend")
}

// Structurally invalid YAML: if the override file contains invalid YAML syntax
// (not just a bad enum, but a parse error), LoadCapabilityRules returns a
// wrapped error that identifies the file. The embedded default is NOT used.
func TestLoadCapabilityRules_InvalidYAMLSyntax(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(p, []byte("backends: {\nnot: valid: yaml: at: all\n"), 0o600))

	_, err := adapterconfig.LoadCapabilityRules(p)
	require.Error(t, err, "structurally invalid YAML must fail")
	// The error must wrap and name the offending file path.
	assert.ErrorContains(t, err, p, "error must reference the override file path")
}

// Override adds new backend: an override file can introduce backends that are
// not present in the embedded defaults. The result contains both the new backend
// and all unaffected embedded entries.
func TestLoadCapabilityRules_OverrideAddsNewBackend(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "capabilities.yaml")
	yaml := "backends:\n  custom-backend:\n    context_tiers: [default]\n    effort_levels: [default, medium]\n"
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))

	r, err := adapterconfig.LoadCapabilityRules(p)
	require.NoError(t, err)

	// The new backend is accessible.
	_, eff, err := r.Validate("custom-backend", "any", provider.ContextDefault, provider.EffortMedium)
	require.NoError(t, err)
	assert.Equal(t, provider.EffortMedium, eff, "new backend must be usable after additive override")

	// Embedded backends are still present.
	_, _, err = r.Validate("claude-code", "opus-4.8", provider.Context200K, provider.EffortHigh)
	require.NoError(t, err, "embedded claude-code backend must survive after additive override")
}

// Paired integration (#22): the embedded/override ruleset actually drives
// ProfileService write rejection + flooring. LoadCapabilityRules feeds directly
// into a ProfileService; the service's Validate call uses the same rules object
// that LoadCapabilityRules returned so the two components are genuinely linked.
//
// This also verifies that a narrowed override (here: the embedded sonnet-4.6
// override) changes what writes are accepted — xhigh is floored to high,
// context tiers are inherited from the parent backend.
func TestPaired_LoadedRulesDriveProfileServiceValidation(t *testing.T) {
	// Load the embedded default ruleset (no override file).
	rules, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)

	// Wire a ProfileService with the LOADED rules.
	store := &inlineProfileStore{byAlias: map[string]provider.Profile{}}
	rl := &noopReload{}
	svc := provider.NewProfileService(store, rules, rl)

	// Codex: context_tiers=[default] — requesting 200k must be rejected.
	_, _, err = svc.Create(adminIdentity(), provider.Profile{
		Alias: "bad-ctx", Kind: provider.ProfileKindAgent, Backend: "codex",
		ContextWindow: provider.Context200K,
	})
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable,
		"codex context_tiers=[default] must reject 200k (loaded ruleset drives rejection)")

	// claude-code + sonnet-4.6: xhigh is floored to high by the embedded override.
	stored, eff, err := svc.Create(adminIdentity(), provider.Profile{
		Alias: "capped", Kind: provider.ProfileKindAgent, Backend: "claude-code",
		Model: "sonnet-4.6", Effort: provider.EffortXHigh,
	})
	require.NoError(t, err, "sonnet-4.6 + xhigh must succeed (floored, not rejected)")
	assert.Equal(t, provider.EffortXHigh, stored.Effort, "stored effort must be the requested value")
	assert.Equal(t, provider.EffortHigh, eff.Effort,
		"effective effort must be floored by the loaded sonnet-4.6 override")
}

// inlineProfileStore is a minimal provider.ProfileStore for the paired test
// above. It avoids importing a test fixture from another package.
type inlineProfileStore struct{ byAlias map[string]provider.Profile }

func (s *inlineProfileStore) Create(_ context.Context, p provider.Profile) error {
	if _, ok := s.byAlias[p.Alias]; ok {
		return provider.ErrProfileExists
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *inlineProfileStore) Update(_ context.Context, p provider.Profile) error {
	if _, ok := s.byAlias[p.Alias]; !ok {
		return provider.ErrProfileNotFound
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *inlineProfileStore) Delete(_ context.Context, alias string) error {
	if _, ok := s.byAlias[alias]; !ok {
		return provider.ErrProfileNotFound
	}
	delete(s.byAlias, alias)
	return nil
}
func (s *inlineProfileStore) Get(_ context.Context, alias string) (provider.Profile, error) {
	p, ok := s.byAlias[alias]
	if !ok {
		return provider.Profile{}, provider.ErrProfileNotFound
	}
	return p, nil
}
func (s *inlineProfileStore) List(_ context.Context) ([]provider.Profile, error) {
	out := make([]provider.Profile, 0, len(s.byAlias))
	for _, p := range s.byAlias {
		out = append(out, p)
	}
	return out, nil
}
func (s *inlineProfileStore) Count(_ context.Context) (int, error) {
	return len(s.byAlias), nil
}

var _ provider.ProfileStore = (*inlineProfileStore)(nil)

type noopReload struct{}

func (*noopReload) Reload(context.Context) error { return nil }

func adminIdentity() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
}

// NilInherit: a model_override that specifies only effort_floor (omitting
// context_tiers and effort_levels) inherits the backend's lists for those axes.
// The embedded default exercises this (sonnet-4.6 has no context_tiers; it
// inherits claude-code's [default, 200k, 1m]).
func TestLoadCapabilityRules_NilInherit(t *testing.T) {
	r, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)

	// sonnet-4.6 inherits claude-code's context tiers (including 1m) even though
	// the model_override in the embedded default omits context_tiers.
	_, _, err = r.Validate("claude-code", "sonnet-4.6", provider.Context1M, provider.EffortHigh)
	assert.NoError(t, err, "sonnet-4.6 must inherit claude-code's context tiers (nil → inherit)")

	// sonnet-4.6 inherits none of the xhigh tier (excluded in effort_levels override).
	_, eff, err := r.Validate("claude-code", "sonnet-4.6", provider.ContextDefault, provider.EffortXHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, eff, "sonnet-4.6 effort_floor xhigh -> high must still apply")
}

// The pi backend must be in the embedded default ruleset, or a profile naming
// it is rejected at create time with "unknown backend in capability rules".
// pi exposes off/minimal/low/medium/high/xhigh/max thinking levels, so every
// graded effort maps straight through; it has no context-window knob, so it is
// default-tier only (like codex and opencode).
func TestLoadCapabilityRules_PiBackend(t *testing.T) {
	r, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)

	for _, e := range []provider.Effort{
		provider.EffortLow, provider.EffortMedium, provider.EffortHigh,
		provider.EffortXHigh, provider.EffortMax,
	} {
		_, gotEffort, verr := r.Validate("pi", "openai-codex/gpt-5.6-sol", provider.ContextDefault, e)
		require.NoError(t, verr, "effort %q must be valid on pi", e)
		assert.Equal(t, e, gotEffort, "effort %q must not be floored on pi", e)
	}

	// No context knob: a non-default tier is rejected rather than silently kept.
	_, _, verr := r.Validate("pi", "openai-codex/gpt-5.6-sol", provider.Context1M, provider.EffortHigh)
	assert.Error(t, verr, "pi has no context-window axis; a 1m tier must be rejected")
}
