package http

import (
	"context"
	"net/http"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// nullDismissalRepository is a Null Object implementation of
// instance.DismissalRepository used when the dismissal feature is disabled (no
// repo/user wiring). Every operation is a successful no-op: reads return an
// empty set, writes do nothing. It lets the handler body stay free of nil-guards
// — h.dismissals is always non-nil — while route registration still decides
// whether the dismiss/restore endpoints are exposed at all.
type nullDismissalRepository struct{}

func (nullDismissalRepository) Dismiss(_ context.Context, _, _ uuid.UUID) error { return nil }
func (nullDismissalRepository) DismissMany(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}
func (nullDismissalRepository) Restore(_ context.Context, _, _ uuid.UUID) error { return nil }
func (nullDismissalRepository) DismissedIDs(_ context.Context, _ uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}
func (nullDismissalRepository) ClearForInstance(_ context.Context, _ uuid.UUID) error { return nil }

var _ instance.DismissalRepository = nullDismissalRepository{}

// nullUserResolver is a Null Object CurrentUserResolver used when the dismissal
// feature is disabled. It returns the zero user without error so the
// always-non-nil resolver invariant holds; it is only ever reached on the
// per-user List path, which subtracts an empty dismissed set from the null repo.
type nullUserResolver struct{}

func (nullUserResolver) CurrentUser(_ *http.Request) (uuid.UUID, error) { return uuid.UUID{}, nil }

var _ CurrentUserResolver = nullUserResolver{}
