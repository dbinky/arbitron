package main

import (
	"context"
	"errors"
	"testing"

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
func (s *seedFakeStore) Count(_ context.Context) (int, error) { return len(s.byAlias), nil }

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

// Edge: no candidates on an empty table seeds nothing without error.
func TestSeedProfiles_NoCandidates(t *testing.T) {
	store := newSeedFakeStore()
	n, skipped, err := SeedProfiles(context.Background(), store, testRules(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 0, skipped)
}
