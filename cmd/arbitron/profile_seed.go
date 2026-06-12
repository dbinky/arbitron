package main

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/provider"
)

// SeedProfiles seeds the store ONCE, only when it is empty (the seed gate). Each
// candidate is validated against the capability rules; an invalid candidate is
// SKIPPED (not fatal) so operator-authored config never blocks boot — the caller
// logs the seeded/skipped counts. Returns (seeded, skipped, err).
//
// The DB is the source of truth after the first seed: a non-empty store is left
// untouched, so config.yaml profile entries are ignored on every subsequent
// boot. A non-ErrProfileExists Create error aborts and propagates (a duplicate
// alias mid-seed is treated as already-present and skipped silently).
func SeedProfiles(ctx context.Context, store provider.ProfileStore, rules provider.CapabilityRules, candidates []provider.Profile) (seeded, skipped int, err error) {
	n, err := store.Count(ctx)
	if err != nil {
		return 0, 0, err
	}
	if n > 0 {
		return 0, 0, nil // already seeded; the DB wins.
	}
	for _, p := range candidates {
		// Choose the backend the capability ruleset is keyed on via the shared
		// EffectiveBackend rule (the SAME one ProfileService.write uses): an empty
		// backend with no connection is the claude-cli chat-subscription default, so
		// a bare config chat profile (default/default axes) validates against that.
		backend := provider.EffectiveBackend(p)
		// Normalize the unset ("") axes to their explicit default sentinel before
		// validating: an empty string is not a Valid() enum value, but a config
		// profile that omits the new axes must seed as default/default (always
		// valid). A genuinely malformed axis string fails to parse and is skipped.
		tier, terr := provider.ParseContextWindow(string(p.ContextWindow))
		if terr != nil {
			skipped++
			continue
		}
		effort, eerr := provider.ParseEffort(string(p.Effort))
		if eerr != nil {
			skipped++
			continue
		}
		if _, _, verr := rules.Validate(backend, p.Model, tier, effort); verr != nil {
			skipped++
			continue
		}
		if cerr := store.Create(ctx, p); cerr != nil {
			if errors.Is(cerr, provider.ErrProfileExists) {
				continue // a concurrent/duplicate alias is already present; skip.
			}
			return seeded, skipped, cerr
		}
		seeded++
	}
	return seeded, skipped, nil
}
