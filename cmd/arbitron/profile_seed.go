package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
)

// ProfileSeeder is the trusted service-backed batch seed path. Discovery and
// capability validation precede the shared mutation lease; proofs and original
// pending membership are rechecked under that lease before validated writes.
type ProfileSeeder interface {
	SeedBatch(context.Context, provider.ProfileSeedStore, []provider.Profile, func(string, error)) (int, int, error)
}

type profileSeedRepository interface {
	provider.ProfileStore
	provider.ProfileSeedStore
}

// SeedProfiles seeds only the original, durable empty-store snapshot. Invalid
// or currently unproved config stays pending and is reported through onSkip;
// persistence failures abort without losing the unfinished original entries.
// Existing aliases are never overwritten or revalidated during an outage.
func SeedProfiles(ctx context.Context, store profileSeedRepository, seeder ProfileSeeder, candidates []provider.Profile, onSkip func(alias string, cause error)) (seeded, skipped int, err error) {
	if seeder != nil {
		return seeder.SeedBatch(ctx, store, candidates, onSkip)
	}
	// Recovery may acknowledge a committed row without needing discovery/service
	// dependencies, but it cannot create a profile without the trusted seeder.
	pending, err := store.PendingProfileSeed(ctx, candidates)
	if err != nil {
		return 0, 0, err
	}
	for _, p := range pending {
		if _, err := store.Get(ctx, p.Alias); err != nil {
			if !errors.Is(err, provider.ErrProfileNotFound) {
				return 0, 0, err
			}
			return 0, 0, fmt.Errorf("profile seed: seeder not configured")
		}
		if err := store.CompleteProfileSeed(ctx, p.Alias); err != nil {
			return 0, 0, err
		}
	}
	return 0, 0, nil
}
