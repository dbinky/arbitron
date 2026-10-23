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

// ErrReadOnlyStore is returned by the mutating methods (Create/Update/Delete) of
// a read-only ProfileStore (e.g. the fixed in-memory fixture backing a cache over
// an immutable profile set). It honestly describes "this store does not mutate"
// rather than borrowing ErrProfileExists/ErrProfileNotFound, which describe a
// missing/duplicate alias — a caller branching on those (e.g. retry-on-conflict)
// against a read-only store would otherwise loop or misbehave. Callers match it
// with errors.Is.
var ErrReadOnlyStore = errors.New("provider: profile store is read-only")

// ErrProfileMutationUnavailable marks a store without conditional admin writes.
var ErrProfileMutationUnavailable = errors.New("provider: conditional profile mutations not configured")

// ProfileMutationStore protects admin edits against changes since their read.
// Both operations compare the complete expected row, including its revision;
// a changed or deleted row returns ErrAdvanceConflict without any mutation.
type ProfileMutationStore interface {
	UpdateExpected(ctx context.Context, expected, replacement Profile) (Profile, error)
	DeleteExpected(ctx context.Context, expected Profile) error
}

// SelectionMutationStore persists a prepared selection after the caller has
// revalidated its exact prospective proof under the execution mutation lease.
// Following selections require a matching candidate and execution token; policy
// and connection revisions are checked in the same transaction as the write.
// Pinned selections require prior fixed-version validation, not follow evidence.
// UpdateValidated compares the complete expected row and revision, returning
// ErrAdvanceConflict on a concurrent edit or deletion. Both methods return the
// authoritative stored profile and revision, never a caller-supplied revision.
// Service/seed callers must not fall back to unconditional CRUD if unavailable.
// External fingerprints and candidate usability remain the caller's checks.
type SelectionMutationStore interface {
	CreateValidated(context.Context, PreparedSelection) (Profile, error)
	UpdateValidated(context.Context, Profile, PreparedSelection) (Profile, error)
}

// ProfileSeedStore retains the original one-time config seed across interrupted
// boots. PendingProfileSeed atomically snapshots candidates only for an empty,
// never-seeded store; subsequent calls return only unfinished original entries.
// CompleteProfileSeed acknowledges an entry without changing its profile row.
// ReplaceProfileSeed rewrites an unfinished entry's original with a corrected
// candidate for the same alias; it never adds an entry. SeedBatch uses it only
// for an original that can never be prepared.
type ProfileSeedStore interface {
	PendingProfileSeed(context.Context, []Profile) ([]Profile, error)
	CompleteProfileSeed(context.Context, string) error
	ReplaceProfileSeed(context.Context, Profile) error
}

// ProfileStore is the driven port for profile persistence. It is a DUMB
// persistence port: capability validation (against the CapabilityRules) lives in
// the Phase-4 ProfileService and the startup seed path, NOT here. The store
// defaults selection mode to pinned and enforces mode/target shape; it owns
// Revision (callers cannot choose it). Identity/usability validation stays above
// this port, mirroring how ConnectionStore keeps
// provider-specific URL/Azure checks in ValidateConnection at the seam above it.
type ProfileStore interface {
	// Create persists a new profile at revision 1, ignoring p.Revision;
	// a duplicate alias returns ErrProfileExists.
	Create(ctx context.Context, p Profile) error
	// Update overwrites an existing profile by alias and atomically increments
	// the stored revision, ignoring p.Revision; a missing alias returns
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
	// Count returns the number of stored profiles.
	Count(ctx context.Context) (int, error)
}
