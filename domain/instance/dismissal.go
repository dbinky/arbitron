package instance

import (
	"context"

	"github.com/google/uuid"
)

// DismissalRepository is the port for per-user instance dismissals. A dismissal
// is keyed by (userID, instanceID) and is independent of the instance's own
// lifecycle, except that engine.Retry clears all dismissals for an instance via
// ClearForInstance. Implementations must wrap any domain sentinel errors with
// fmt.Errorf %w so the HTTP adapter's errors.Is mapping works.
type DismissalRepository interface {
	// Dismiss records that userID has dismissed instanceID. It is idempotent:
	// dismissing an already-dismissed instance is a no-op success.
	Dismiss(ctx context.Context, userID, instanceID uuid.UUID) error
	// DismissMany records, atomically, that userID has dismissed every instance
	// in instanceIDs. Either all rows are written or none are (single
	// transaction); there is no partial-commit state. It is idempotent per row.
	// An empty instanceIDs slice is a no-op success.
	//
	// SECURITY: callers MUST derive instanceIDs server-side (e.g. from
	// InstanceRepository.List); the set must never be taken from client input,
	// or DismissMany becomes a write-IDOR.
	DismissMany(ctx context.Context, userID uuid.UUID, instanceIDs []uuid.UUID) error
	// Restore removes userID's dismissal of instanceID. Restoring a
	// non-dismissed instance is a no-op success.
	Restore(ctx context.Context, userID, instanceID uuid.UUID) error
	// DismissedIDs returns the set of instance IDs userID has dismissed.
	DismissedIDs(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]bool, error)
	// ClearForInstance removes the dismissal of instanceID for every user.
	// Called on retry so a re-running instance resurfaces for everyone who had
	// dismissed it.
	ClearForInstance(ctx context.Context, instanceID uuid.UUID) error
}
