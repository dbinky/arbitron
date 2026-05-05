package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// TimerSchedulerForRecovery defines the subset of Scheduler the recovery logic needs.
type TimerSchedulerForRecovery interface {
	Schedule(instanceID interface{ String() string }, waitingUntil time.Time, timerType string)
}

// RecoverTimers scans for waiting instances with timer delays and either
// re-registers them with the scheduler or transitions past-due ones to queued.
// It should be called during startup after the scheduler has been started.
func (m *Manager) RecoverTimers(ctx context.Context, scheduler *Scheduler) error {
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
				// Log error but continue recovery.
				continue
			}
		} else {
			// Still pending: register with scheduler.
			scheduler.Schedule(inst.ID, *inst.WaitingUntil, TimerTypeDelay)
		}
	}

	// Run queue evaluation (past-due timers may have added to the queue).
	return m.Evaluate(ctx)
}

// RecoverWebhooks scans for waiting instances with webhook tokens and either
// re-registers them in the webhook registry, or fails them if their timeout
// has expired during downtime.
func (m *Manager) RecoverWebhooks(ctx context.Context, scheduler *Scheduler, nodeExecs instance.NodeExecutionRepository) error {
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

		// Re-register token in webhook registry.
		if m.webhookRegistry != nil {
			m.webhookRegistry.Register(*inst.WebhookToken, inst.ID, nodeID, inst.WebhookSecretHash)
		}

		// Check if there's a timeout configured by looking at the workflow snapshot.
		nodeConfig := getWebhookNodeConfig(inst)
		if nodeConfig != nil && nodeConfig.WaitTimeoutSeconds != "" {
			timeoutSec, err := strconv.ParseFloat(nodeConfig.WaitTimeoutSeconds, 64)
			if err == nil && timeoutSec > 0 {
				// Find the node execution to determine when it started.
				nodeExec, execErr := nodeExecs.GetByInstanceAndNode(ctx, inst.ID, nodeID)
				if execErr == nil && nodeExec != nil {
					deadline := nodeExec.StartedAt.Add(time.Duration(timeoutSec * float64(time.Second)))
					if deadline.Before(now) {
						// Timeout expired during downtime.
						if m.webhookRegistry != nil {
							m.webhookRegistry.Deactivate(*inst.WebhookToken)
						}
						_ = m.instances.SetWebhookFields(ctx, inst.ID, nil, nil)
						_ = m.instances.SetFailed(ctx, inst.ID, nodeID)
					} else {
						// Timeout still pending — register with scheduler.
						scheduler.Schedule(inst.ID, deadline, TimerTypeWebhook)
					}
				}
			}
		}
	}

	return m.Evaluate(ctx)
}

// getWebhookNodeConfig extracts the webhook_wait node config from the workflow
// snapshot for the given instance's current node.
func getWebhookNodeConfig(inst instance.Instance) *workflow.NodeConfig {
	if inst.WorkflowSnapshot == nil || inst.CurrentNodeID == nil {
		return nil
	}

	wfBytes, err := json.Marshal(inst.WorkflowSnapshot)
	if err != nil {
		return nil
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return nil
	}

	for _, n := range wf.Nodes {
		if n.ID == *inst.CurrentNodeID && n.Type == workflow.NodeTypeWebhookWait {
			return &n.Config
		}
	}
	return nil
}
