package schedule

import (
	"context"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
)

// ScheduleAlerter raises schedule_skipped / schedule_missed notifications,
// mirroring engine.emitNotification: it writes a notification.Notification via
// the repository and forwards to the ExternalNotifier (Teams) when one is wired.
//
// Schedule alerts have NO instance (a scheduled run did not happen), so
// InstanceID is uuid.Nil; the SQLite notification repo persists it as a NULL
// instance_id and carries the workflow name in the denormalized column. The
// operator sees from Type + WorkflowName that a run did not happen.
type ScheduleAlerter struct {
	notifs   notification.Repository
	external notification.ExternalNotifier
	logger   *slog.Logger
}

// Compile-time assertion that ScheduleAlerter satisfies the evaluator's port.
var _ Alerter = (*ScheduleAlerter)(nil)

// NewScheduleAlerter constructs a ScheduleAlerter. external may be nil (no
// external forwarding); logger defaults to slog.Default().
func NewScheduleAlerter(notifs notification.Repository, external notification.ExternalNotifier, logger *slog.Logger) *ScheduleAlerter {
	if logger == nil {
		logger = slog.Default()
	}
	return &ScheduleAlerter{notifs: notifs, external: external, logger: logger}
}

// Alert creates a notification of type t for schedule s and forwards it to the
// external notifier. A nil repository is a no-op (defensive, mirrors the
// engine's nil-notifications guard).
func (a *ScheduleAlerter) Alert(ctx context.Context, s Schedule, t notification.Type) error {
	if a.notifs == nil {
		return nil
	}
	n := &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   uuid.Nil, // schedule alerts are instance-less
		WorkflowName: s.WorkflowName,
		Type:         t,
		CreatedAt:    time.Now(),
	}
	if err := a.notifs.Create(ctx, n); err != nil {
		return err
	}
	if a.external != nil {
		if err := a.external.Notify(ctx, n); err != nil {
			// External delivery is best-effort: log, don't fail the alert.
			a.logger.Error("schedule: external notifier failed", "scheduleID", s.ID, "type", t, "error", err)
		}
	}
	return nil
}
