package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RecoverTimers scans for waiting instances with timer delays and either
// re-registers them with the scheduler or transitions past-due ones to queued.
// It should be called during startup after the scheduler has been started and
// SetScheduler has been called on the manager.
func (m *Manager) RecoverTimers(ctx context.Context) error {
	waiting, err := m.queue.ListWaitingWithTimer(ctx)
	if err != nil {
		return fmt.Errorf("listing waiting instances: %w", err)
	}

	now := time.Now()
	for _, inst := range waiting {
		if inst.WaitingUntil == nil {
			continue
		}

		if !inst.WaitingUntil.After(now) {
			// Past-due: transition to queued.
			if err := m.queue.SetQueued(ctx, inst.ID, now); err != nil {
				slog.Error("queue recovery: failed to re-queue past-due timer instance", "instanceID", inst.ID, "error", err)
				continue
			}
		} else {
			// Still pending: register with scheduler.
			m.scheduler.Schedule(inst.ID, *inst.WaitingUntil, TimerTypeDelay)
		}
	}

	// Run queue evaluation (past-due timers may have added to the queue).
	return m.Evaluate(ctx)
}

// RecoverWebhooks scans for waiting instances with webhook tokens and either
// re-registers them in the webhook registry, or fails them if their timeout
// has expired during downtime.
// It should be called during startup after the scheduler has been started and
// SetScheduler has been called on the manager.
func (m *Manager) RecoverWebhooks(ctx context.Context) error {
	waiting, err := m.queue.ListWaitingWithWebhook(ctx)
	if err != nil {
		return fmt.Errorf("listing waiting webhook instances: %w", err)
	}

	now := time.Now()
	for _, inst := range waiting {
		if inst.WebhookToken == nil {
			continue
		}

		// Determine the webhook node ID from current_node_id.
		nodeID := ""
		if inst.CurrentNodeID != nil {
			nodeID = *inst.CurrentNodeID
		}

		// If the timeout expired during downtime, fail the instance without
		// re-registering the token.
		if inst.WaitingUntil != nil && !inst.WaitingUntil.After(now) {
			if m.webhookRegistry != nil {
				m.webhookRegistry.Deactivate(*inst.WebhookToken)
			}
			if err := m.instances.SetWebhookFields(ctx, inst.ID, nil, nil); err != nil {
				slog.Error("queue recovery: failed to clear webhook fields for timed-out instance", "instanceID", inst.ID, "error", err)
			}
			if err := m.instances.SetFailed(ctx, inst.ID, nodeID); err != nil {
				slog.Error("queue recovery: failed to fail timed-out webhook instance", "instanceID", inst.ID, "error", err)
				continue
			}
			// This instance may be somebody's awaited child; mirror the live
			// OnTimerFired(TimerTypeWebhook) path so a wait_for_child parent
			// resumes in this same startup pass instead of hanging until the
			// next restart.
			m.notifyChildTerminal(ctx, inst.ID)
			continue
		}

		// Re-register token in webhook registry.
		if m.webhookRegistry != nil {
			m.webhookRegistry.Register(*inst.WebhookToken, inst.ID, nodeID, inst.WebhookSecretHash)
		}

		// If timeout is still pending, re-register with the scheduler.
		if inst.WaitingUntil != nil {
			m.scheduler.Schedule(inst.ID, *inst.WaitingUntil, TimerTypeWebhook)
		}
	}

	return m.Evaluate(ctx)
}

