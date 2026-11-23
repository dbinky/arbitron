package notification

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// SourceAlerts implements runner.SourceAlertNotifier: an instance-less
// source_logged_out notification row plus the external (Teams) card, sent in
// the background so the health monitor never blocks on HTTP.
type SourceAlerts struct {
	repo     notification.Repository
	external notification.ExternalNotifier
	logger   *slog.Logger
	now      func() time.Time
	wg       sync.WaitGroup
}

// NewSourceAlerts requires a repository; external may be nil (no Teams).
func NewSourceAlerts(repo notification.Repository, external notification.ExternalNotifier, logger *slog.Logger, now func() time.Time) *SourceAlerts {
	if repo == nil {
		panic("notification: NewSourceAlerts requires a repository")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &SourceAlerts{repo: repo, external: external, logger: logger, now: now}
}

// SourceLoggedOut records the notification and fires the external card.
func (a *SourceAlerts) SourceLoggedOut(ctx context.Context, s runner.Source) error {
	n := &notification.Notification{
		ID:           uuid.New(),
		WorkflowName: fmt.Sprintf("runner %s source %s", s.RunnerID, s.Kind),
		Type:         notification.TypeSourceLoggedOut,
		CreatedAt:    a.now(),
	}
	if err := a.repo.Create(ctx, n); err != nil {
		return fmt.Errorf("source logged-out notification: %w", err)
	}
	if a.external != nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			if err := a.external.Notify(context.WithoutCancel(ctx), n); err != nil {
				a.logger.Warn("source logged-out external notification failed", "source", string(s.ID), "error", err)
			}
		}()
	}
	return nil
}

// SourceRecovered is log-only: recovery needs no operator action.
func (a *SourceAlerts) SourceRecovered(_ context.Context, s runner.Source) error {
	a.logger.Info("runner source recovered", "source", string(s.ID))
	return nil
}

// Wait blocks until background external notifications finish (tests, shutdown).
func (a *SourceAlerts) Wait() { a.wg.Wait() }
