package provider

import (
	"context"
	"errors"
)

// ErrProfileExists is returned by a ProfileStore.Create when a profile with the
// same alias already exists. Callers match it with errors.Is.
var ErrProfileExists = errors.New("provider: profile already exists")

// ErrProfileNotFound is returned by a ProfileStore Get/Update/Delete when no
// profile with the given alias exists. Callers match it with errors.Is.
var ErrProfileNotFound = errors.New("provider: profile not found")

// ProfileStore is the driven port for profile persistence. It is a DUMB
// persistence port: capability validation (against the CapabilityRules) lives in
// the Phase-4 ProfileService and the startup seed path, NOT here. The store
// persists exactly what it is given, mirroring how ConnectionStore keeps
// provider-specific URL/Azure checks in ValidateConnection at the seam above it.
type ProfileStore interface {
	// Create persists a new profile; a duplicate alias returns ErrProfileExists.
	Create(ctx context.Context, p Profile) error
	// Update overwrites an existing profile by alias; a missing alias returns
	// ErrProfileNotFound.
	Update(ctx context.Context, p Profile) error
	// Delete removes a profile by alias; a missing alias returns
	// ErrProfileNotFound.
	Delete(ctx context.Context, alias string) error
	// Get returns the profile for an alias; a missing alias returns
	// ErrProfileNotFound.
	Get(ctx context.Context, alias string) (Profile, error)
	// List returns all profiles ordered by alias (a non-nil empty slice when
	// empty).
	List(ctx context.Context) ([]Profile, error)
	// Count returns the number of stored profiles. It feeds the one-time seed
	// gate (seed only when the table is empty; the DB wins thereafter).
	Count(ctx context.Context) (int, error)
}
