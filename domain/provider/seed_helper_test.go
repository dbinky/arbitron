package provider_test

import (
	"context"

	"github.com/arbitron/arbitron/domain/provider"
)

// memSeedStore is the in-memory ProfileSeedStore the boot path journals against:
// the first PendingProfileSeed snapshots the candidates, later calls return only
// the entries not yet acknowledged.
type memSeedStore struct {
	started bool
	pending []provider.Profile
	done    map[string]bool
}

func (m *memSeedStore) PendingProfileSeed(_ context.Context, candidates []provider.Profile) ([]provider.Profile, error) {
	if !m.started {
		m.started = true
		for _, p := range candidates {
			m.pending = append(m.pending, p.Clone())
		}
	}
	var out []provider.Profile
	for _, p := range m.pending {
		if !m.done[p.Alias] {
			out = append(out, p.Clone())
		}
	}
	return out, nil
}

func (m *memSeedStore) CompleteProfileSeed(_ context.Context, alias string) error {
	if m.done == nil {
		m.done = map[string]bool{}
	}
	m.done[alias] = true
	return nil
}

func (m *memSeedStore) ReplaceProfileSeed(_ context.Context, p provider.Profile) error {
	for i := range m.pending {
		if m.pending[i].Alias == p.Alias && !m.done[p.Alias] {
			m.pending[i] = p.Clone()
		}
	}
	return nil
}

// seedViaBatch drives one profile through the production boot path,
// ProfileService.SeedBatch, and reports the outcome the way the removed
// per-profile entry did: a refused seed returns the refusal (satisfying
// IsInvalidProfile), a store fault aborts with its own error, and an accepted
// seed returns the stored profile. The service must already carry a mutation
// gate, as the composition root installs one.
func seedViaBatch(svc *provider.ProfileService, store provider.ProfileStore, p provider.Profile) (provider.Profile, error) {
	ctx := context.Background()
	var refusal error
	seeded, skipped, err := svc.SeedBatch(ctx, &memSeedStore{}, []provider.Profile{p}, func(_ string, cause error) { refusal = cause })
	if err != nil {
		return provider.Profile{}, err
	}
	if skipped > 0 || seeded == 0 {
		return provider.Profile{}, refusal
	}
	return store.Get(ctx, p.Alias)
}
