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
