package provider

import (
	"context"
	"errors"
)

// PinRefusal names a persisted fixed-mode profile whose current routes cannot
// pin a fixed model version, so every trigger of a workflow using it refuses.
type PinRefusal struct {
	Alias string
	Model string
	Err   error
}

// PinPreflight checks every persisted pinned (fixed-mode) profile against the
// same route grammar triggers apply. Following selections are skipped: their
// eligibility is governed by their selection mode. It reads only local state
// (profiles, connections, workflows, active runs): no catalog and no probes.
// Refusals are returned; a profile whose routes could not be read is returned
// in unchecked so the caller can report it without guessing.
func (s *ProfileService) PinPreflight(ctx context.Context) (refusals, unchecked []PinRefusal, err error) {
	profiles, err := s.store.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range profiles {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if p.SelectionMode == SelectionFollowing {
			continue
		}
		cerr := checkRouteVersions(ctx, p, s.versions, s.source, s.rules)
		switch {
		case cerr == nil:
		case errors.Is(cerr, ErrSpecificVersionUnavailable), errors.Is(cerr, ErrInvalidSelection):
			refusals = append(refusals, PinRefusal{Alias: p.Alias, Model: p.Model, Err: cerr})
		default:
			unchecked = append(unchecked, PinRefusal{Alias: p.Alias, Model: p.Model, Err: cerr})
		}
	}
	return refusals, unchecked, nil
}
