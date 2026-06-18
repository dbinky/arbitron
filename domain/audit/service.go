package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// systemActor is the actor id used when no identity is present (scheduler /
// webhook / startup actions).
const systemActor = "system"

// Service records consequential mutations best-effort. A store failure is
// logged and dropped — auditing must never turn a successful operation into a
// failed one (design doc §Audit Log).
type Service struct {
	store  EventStore
	logger *slog.Logger
	now    func() time.Time // injectable for tests; defaults to time.Now
}

// NewService builds an audit Service over store. A nil store yields a nil-safe
// no-op Service (local/unconfigured deployments still call Record harmlessly).
func NewService(store EventStore) *Service {
	return &Service{store: store, logger: slog.Default(), now: time.Now}
}

// SetLogger overrides the failure logger (optional).
func (s *Service) SetLogger(l *slog.Logger) {
	if s != nil && l != nil {
		s.logger = l
	}
}

// Record appends an audit event, reading the actor from ctx. It never returns
// an error: a nil Service, a nil store, or an Append failure are all swallowed
// (the latter logged at WARN). detail must be valid JSON or ""; "" becomes "{}".
func (s *Service) Record(ctx context.Context, action, target, detail string) {
	if s == nil || s.store == nil {
		return
	}
	actor := systemActor
	if id, ok := identity.FromContext(ctx); ok {
		actor = id.UserID.String()
	}
	ev := Event{
		ID:         uuid.New(),
		OccurredAt: s.now().UTC(),
		ActorID:    actor,
		Action:     action,
		Target:     target,
		Detail:     detail,
	}
	if err := s.store.Append(ctx, ev); err != nil {
		s.logger.Warn("audit append failed (operation already succeeded)",
			"action", action, "target", target, "error", err)
	}
}

// ListByTarget returns every audit event recorded against target (e.g. a
// workflow name), newest first. A nil Service or nil store yields an empty
// slice and no error, so the query surface is harmless in local/unconfigured
// deployments.
func (s *Service) ListByTarget(ctx context.Context, target string) ([]Event, error) {
	if s == nil || s.store == nil {
		return []Event{}, nil
	}
	return s.store.ListByTarget(ctx, target)
}
