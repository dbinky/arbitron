package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

type seedDiscoveryKey struct{}

// WithoutSeedDiscovery marks a SeedBatch call that must not discover: a
// following seed (whose preparation fetches the catalog and probes models) is
// left pending, neither prepared nor refused, for a later call without the
// mark. Startup uses it so readiness never waits on discovery.
func WithoutSeedDiscovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, seedDiscoveryKey{}, true)
}

func seedDiscoveryDisabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(seedDiscoveryKey{}).(bool)
	return disabled
}

// SeedBatch is the trusted startup path behind SeedProfiles. It prepares the
// original pending snapshot outside the mutation lease, then rechecks membership
// and proofs and inserts accepted entries under one shared batch lease. It never
// overwrites an existing alias. The seed journal may be a repository decorator;
// profile persistence still uses the service's validated store.
func (s *ProfileService) SeedBatch(ctx context.Context, seeds ProfileSeedStore, candidates []Profile, onSkip func(string, error)) (seeded, skipped int, err error) {
	if s.gate == nil {
		return 0, 0, fmt.Errorf("%w: profile seed mutation gate not configured", ErrExecutionRevisionUnavailable)
	}
	var pending []Profile
	if err = s.gate.WithMutation(ctx, func(ctx context.Context) error {
		var err error
		pending, err = seeds.PendingProfileSeed(ctx, candidates)
		return err
	}); err != nil {
		return 0, 0, err
	}
	type entry struct {
		original  Profile
		selection PreparedSelection
		invalid   error
		// replacement is the current config candidate that repairs an original
		// which failed preparation; nil when the original stands.
		replacement *Profile
	}
	current := make(map[string]Profile, len(candidates))
	for _, c := range candidates {
		if _, dup := current[c.Alias]; !dup { // first duplicate alias wins, as in the journal
			current[c.Alias] = c
		}
	}
	prepared := make(map[string]entry, len(pending))
	for _, p := range pending {
		e := entry{original: p.Clone()}
		if _, err := s.store.Get(ctx, p.Alias); err == nil {
			prepared[p.Alias] = e
			continue
		} else if !errors.Is(err, ErrProfileNotFound) {
			return 0, 0, err
		}
		if seedDiscoveryDisabled(ctx) && discovers(p) {
			continue // stays pending: preparing it would discover
		}
		e.selection, e.invalid = s.prepareSeed(ctx, p)
		// An original that can never be prepared (e.g. an empty model frozen
		// before fixed versions were required) is repaired by a different,
		// valid current candidate for the same alias. A valid original stays
		// frozen, so later config edits never re-seed it.
		if e.invalid != nil {
			if c, ok := current[p.Alias]; ok && !reflect.DeepEqual(c, p) && !(seedDiscoveryDisabled(ctx) && discovers(c)) {
				if selection, invalid := s.prepareSeed(ctx, c); invalid == nil {
					c = c.Clone()
					e.selection, e.invalid, e.replacement = selection, nil, &c
				}
			}
		}
		prepared[p.Alias] = e
	}
	type refusal struct {
		alias string
		err   error
	}
	var refused []refusal
	err = s.gate.WithMutation(ctx, func(ctx context.Context) error {
		current, err := seeds.PendingProfileSeed(ctx, nil)
		if err != nil {
			return err
		}
		var accepted []PreparedSelection
		for _, p := range current {
			e, ok := prepared[p.Alias]
			if !ok || !reflect.DeepEqual(e.original, p) {
				continue
			}
			if _, err := s.store.Get(ctx, p.Alias); err == nil {
				if err := seeds.CompleteProfileSeed(ctx, p.Alias); err != nil {
					return err
				}
				continue
			} else if !errors.Is(err, ErrProfileNotFound) {
				return err
			}
			if e.invalid != nil {
				refused = append(refused, refusal{p.Alias, e.invalid})
				skipped++
				continue
			}
			// A row present during preparation but deleted meanwhile needs a
			// fresh preparation, never an unvalidated replacement.
			if e.selection.Profile.Alias == "" {
				continue
			}
			if err := s.revalidatePrepared(ctx, e.selection); err != nil {
				refused = append(refused, refusal{p.Alias, err})
				skipped++
				continue
			}
			if e.replacement != nil {
				if err := seeds.ReplaceProfileSeed(ctx, *e.replacement); err != nil {
					return err
				}
			}
			accepted = append(accepted, e.selection)
		}
		for _, selection := range accepted {
			p := selection.Profile
			_, err = s.storePrepared(ctx, selection, nil, func(ctx context.Context, p Profile) (Profile, error) {
				if err := s.store.Create(ctx, p); err != nil {
					return Profile{}, err
				}
				return s.store.Get(ctx, p.Alias)
			})
			if err != nil {
				if errors.Is(err, ErrProfileExists) {
					if err := seeds.CompleteProfileSeed(ctx, p.Alias); err != nil {
						return err
					}
					continue
				}
				if IsInvalidProfile(err) {
					refused = append(refused, refusal{p.Alias, err})
					skipped++
					continue
				}
				return err
			}
			seeded++
			s.recache(ctx)
			if err := seeds.CompleteProfileSeed(ctx, p.Alias); err != nil {
				return err
			}
		}
		return nil
	})
	// Operator reporting is outside the lease too.
	if onSkip != nil {
		for _, r := range refused {
			onSkip(r.alias, r.err)
		}
	}
	return seeded, skipped, err
}

// prepareSeed validates one seed's selection and capability axes outside the
// mutation lease. A refusal is wrapped as a preparationError.
func (s *ProfileService) prepareSeed(ctx context.Context, p Profile) (PreparedSelection, error) {
	selection, invalid := ValidateSelection(ctx, p, s.versions, s.source)
	if invalid == nil {
		_, invalid = s.validateAxes(selection.Profile)
	}
	if invalid != nil {
		return selection, &preparationError{invalid}
	}
	return selection, nil
}

// discovers reports whether preparing p fetches the catalog and probes models.
func discovers(p Profile) bool {
	return p.SelectionMode == SelectionFollowing || p.FollowTarget != nil
}
