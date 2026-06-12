package main

import (
	"context"
	"errors"
	"testing"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertAnError is a sentinel for the Create-error propagation path.
var assertAnError = errors.New("seed: forced create failure")

// seedFakeStore is an in-memory provider.ProfileStore for the seed-helper tests.
type seedFakeStore struct {
	byAlias     map[string]provider.Profile
	createErr   error // forced Create error (non-ErrProfileExists), for the error path
	countErr    error // forced Count error, to exercise the early-abort gate
	createCalls int
}

func newSeedFakeStore(profiles ...provider.Profile) *seedFakeStore {
	s := &seedFakeStore{byAlias: map[string]provider.Profile{}}
	for _, p := range profiles {
		s.byAlias[p.Alias] = p
	}
	return s
}

func (s *seedFakeStore) Create(_ context.Context, p provider.Profile) error {
	s.createCalls++
	if s.createErr != nil {
		return s.createErr
	}
	if _, ok := s.byAlias[p.Alias]; ok {
		return provider.ErrProfileExists
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *seedFakeStore) Update(_ context.Context, p provider.Profile) error {
	if _, ok := s.byAlias[p.Alias]; !ok {
		return provider.ErrProfileNotFound
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *seedFakeStore) Delete(_ context.Context, alias string) error {
	if _, ok := s.byAlias[alias]; !ok {
		return provider.ErrProfileNotFound
	}
	delete(s.byAlias, alias)
	return nil
}
func (s *seedFakeStore) Get(_ context.Context, alias string) (provider.Profile, error) {
	p, ok := s.byAlias[alias]
	if !ok {
		return provider.Profile{}, provider.ErrProfileNotFound
	}
	return p, nil
}
func (s *seedFakeStore) List(_ context.Context) ([]provider.Profile, error) {
	out := make([]provider.Profile, 0, len(s.byAlias))
	for _, p := range s.byAlias {
		out = append(out, p)
	}
	return out, nil
}
func (s *seedFakeStore) Count(_ context.Context) (int, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return len(s.byAlias), nil
}

var _ provider.ProfileStore = (*seedFakeStore)(nil)

// testRules builds a CapabilityRules where claude-code is a known backend (with
// the metered 1m tier + a high effort ceiling) and every other backend is
// unknown (so a bogus backend fails Validate → is skipped by the seeder).
func testRules() provider.CapabilityRules {
	return provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-code": {
			Backend:      "claude-code",
			ContextTiers: []provider.ContextWindow{provider.Context1M},
			EffortLevels: []provider.Effort{provider.EffortLow, provider.EffortMedium, provider.EffortHigh},
		},
		// claude-cli is the chat-subscription default the seeder substitutes for an
		// empty backend + empty connection; make it a known no-axis backend.
		"claude-cli": {Backend: "claude-cli"},
	})
}

// Seed: an empty table seeds the valid candidates and skips invalid ones
// (operator config never blocks boot).
func TestSeedProfiles_EmptyTableSeedsValidSkipsInvalid(t *testing.T) {
	store := newSeedFakeStore()
	in := []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "opus-4.8"},
		{Alias: "weird", Kind: provider.ProfileKindAgent, Backend: "nonexistent", ContextWindow: provider.Context1M},
	}
	n, skipped, err := SeedProfiles(context.Background(), store, testRules(), in)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly the valid candidate is seeded")
	assert.Equal(t, 1, skipped, "the unknown-backend candidate is skipped")

	_, err = store.Get(context.Background(), "coder")
	assert.NoError(t, err, "valid candidate persisted")
	_, err = store.Get(context.Background(), "weird")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound, "invalid candidate not persisted")
}

// Seed: a non-empty table is a no-op (the DB wins on every subsequent boot).
func TestSeedProfiles_NonEmptyTableIsNoOp(t *testing.T) {
	store := newSeedFakeStore(provider.Profile{Alias: "existing", Kind: provider.ProfileKindChat})
	n, skipped, err := SeedProfiles(context.Background(), store, testRules(),
		[]provider.Profile{{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code"}})
	require.NoError(t, err)
	assert.Equal(t, 0, n, "no seeding when the table is non-empty")
	assert.Equal(t, 0, skipped)
	assert.Equal(t, 0, store.createCalls, "Create is never called on a non-empty table")
}

// Seed: a chat/agent profile with empty axes and no connection defaults its
// backend to claude-cli and validates (default/default always pass), so a bare
// config profile seeds cleanly.
func TestSeedProfiles_DefaultBackendSubstitutedForEmpty(t *testing.T) {
	store := newSeedFakeStore()
	in := []provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat}, // empty backend + empty conn
	}
	n, skipped, err := SeedProfiles(context.Background(), store, testRules(), in)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, skipped)
}

// Error: a non-ErrProfileExists Create error aborts seeding and propagates.
func TestSeedProfiles_CreateErrorPropagates(t *testing.T) {
	store := newSeedFakeStore()
	store.createErr = assertAnError
	_, _, err := SeedProfiles(context.Background(), store, testRules(),
		[]provider.Profile{{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, assertAnError)
}

// Failure: a candidate whose ContextWindow or Effort string is malformed (not a
// valid enum value) is skipped — the axis parse happens BEFORE rules.Validate so
// the skip is independent of the capability rules.
func TestSeedProfiles_MalformedAxisSkipped(t *testing.T) {
	store := newSeedFakeStore()
	in := []provider.Profile{
		// Bad context_window — will fail ParseContextWindow.
		{Alias: "bad-cw", Kind: provider.ProfileKindAgent, Backend: "claude-code", ContextWindow: "not-a-tier"},
		// Bad effort — will fail ParseEffort.
		{Alias: "bad-eff", Kind: provider.ProfileKindAgent, Backend: "claude-code", Effort: "not-an-effort"},
		// Valid — must still be seeded (malformed entries must not abort the loop).
		{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code"},
	}
	n, skipped, err := SeedProfiles(context.Background(), store, testRules(), in)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the valid candidate is seeded")
	assert.Equal(t, 2, skipped, "both malformed-axis candidates are skipped")
}

// Error: an ErrProfileExists returned by Create is treated as a silent skip
// (the profile was created concurrently or is a duplicate alias in the input),
// not as an abort. seeded count is not incremented.
func TestSeedProfiles_CreateExistsIsSkipped(t *testing.T) {
	store := newSeedFakeStore()
	// Pre-populate the store so Create will return ErrProfileExists for the alias.
	_ = store.Create(context.Background(), provider.Profile{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	store.createCalls = 0 // reset the counter

	n, skipped, err := SeedProfiles(context.Background(), store, testRules(),
		[]provider.Profile{{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code"}})
	require.NoError(t, err, "ErrProfileExists must not abort seeding")
	assert.Equal(t, 0, n, "a duplicate is not counted as seeded")
	assert.Equal(t, 0, skipped, "a duplicate is not counted as skipped (it is silently skipped)")
}

// Error: a Count failure aborts SeedProfiles before any Create is attempted.
func TestSeedProfiles_CountErrorPropagates(t *testing.T) {
	store := newSeedFakeStore()
	store.countErr = errors.New("count: db unavailable")

	_, _, err := SeedProfiles(context.Background(), store, testRules(),
		[]provider.Profile{{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.countErr)
	assert.Equal(t, 0, store.createCalls, "Count failure must abort before any Create is attempted")
}

// Failure (Pass 2): a profile with an empty backend but a non-empty ConnectionRef
// does NOT get the claude-cli backend substitution (that only applies to the
// no-connection path). An empty backend passed to rules.Validate is unknown and
// the profile is skipped. This exercises the boundary of the substitution guard.
func TestSeedProfiles_EmptyBackendWithConnectionSkipped(t *testing.T) {
	store := newSeedFakeStore()
	in := []provider.Profile{
		{Alias: "cloud", Kind: provider.ProfileKindAgent, ConnectionRef: "my-conn"}, // backend="" conn!=""
		{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code"},       // valid
	}
	n, skipped, err := SeedProfiles(context.Background(), store, testRules(), in)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the explicit-backend candidate is seeded")
	assert.Equal(t, 1, skipped, "empty backend + non-empty connection is skipped (no substitution)")
}

// Edge: no candidates on an empty table seeds nothing without error.
func TestSeedProfiles_NoCandidates(t *testing.T) {
	store := newSeedFakeStore()
	n, skipped, err := SeedProfiles(context.Background(), store, testRules(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 0, skipped)
}

// Paired integration (#20): ProfilesFromConfig → SeedProfiles → store reflects the
// valid entries; invalid entries are skipped. On a second call (non-empty store)
// the seeder is a complete no-op (DB-wins). This exercises the full
// config → adapter/config → domain chain in one test.
func TestPaired_ConfigToSeedToStore(t *testing.T) {
	ctx := context.Background()
	// Construct config-file-style profile entries (as the YAML/koanf parser
	// would produce them) and map them through ProfilesFromConfig to domain profiles.
	cfgProfiles := []struct {
		alias, kind, backend string
		valid                bool
	}{
		{"chat-one", "chat", "", true},            // empty backend → substituted to claude-cli
		{"agent-one", "agent", "claude-code", true},
		{"invalid-backend", "agent", "notabackend", false}, // unknown backend → skipped
	}

	var domainCfg []domainconfig.ProfileConfig
	for _, c := range cfgProfiles {
		domainCfg = append(domainCfg, domainconfig.ProfileConfig{
			Alias:   c.alias,
			Kind:    c.kind,
			Backend: c.backend,
		})
	}

	profiles := configadapter.ProfilesFromConfig(domainCfg)
	assert.Len(t, profiles, 3, "ProfilesFromConfig must map all 3 entries (invalid filter happens in SeedProfiles)")

	store := newSeedFakeStore()
	n, skipped, err := SeedProfiles(ctx, store, testRules(), profiles)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "exactly 2 valid profiles must be seeded")
	assert.Equal(t, 1, skipped, "the invalid-backend profile must be skipped, not an error")

	// Valid profiles are reachable by alias.
	_, err = store.Get(ctx, "chat-one")
	assert.NoError(t, err, "chat-one must be in the store")
	_, err = store.Get(ctx, "agent-one")
	assert.NoError(t, err, "agent-one must be in the store")

	// Invalid entry must not have been stored.
	_, err = store.Get(ctx, "invalid-backend")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound, "invalid-backend must not be in the store")

	// Second boot: the store is non-empty — seed must be a complete no-op.
	n2, skipped2, err := SeedProfiles(ctx, store, testRules(), profiles)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second seed must produce zero creates (DB-wins)")
	assert.Equal(t, 0, skipped2, "second seed must not skip any entries (no validation run)")
	assert.Equal(t, 2, len(store.byAlias), "store must still contain exactly 2 profiles after second seed")
}
