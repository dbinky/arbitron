package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// consumeFinishedWait completes a timer_delay or webhook_wait node whose wait
// is over, for a run that resumes AT that node.
//
// Every wake path (the scheduler firing a timer, the webhook handler, Resume
// from paused, startup recovery) only re-queues the instance; the run then
// restarts at current_node_id, which is still the wait node. Executing the node
// again would arm a brand-new wait, so the run would never advance. Instead,
// when the instance shows the wait finished, this returns the node's result and
// done=true, and the caller follows the node's edge.
//
// The markers that prove the wait finished are cleared here, so a later visit
// to the same node (a loop back-edge) arms a fresh wait:
//   - timer_delay:  waiting_until is set and has passed.
//   - webhook_wait: webhook_received_at is set. The payload the webhook handler
//     stored on the node's execution record becomes the node's output.
//
// Any other node type, or a wait that has not finished, returns done=false and
// the node executes normally.
func (e *Engine) consumeFinishedWait(ctx context.Context, inst *instance.Instance, node workflow.Node) (*NodeResult, bool, error) {
	switch node.Type {
	case workflow.NodeTypeTimerDelay:
		if inst.WaitingUntil == nil || time.Now().Before(*inst.WaitingUntil) {
			return nil, false, nil
		}
		output := map[string]any{"waitingUntil": inst.WaitingUntil.UTC().Format(time.RFC3339)}
		if err := e.instances.SetWaiting(ctx, inst.ID, nil); err != nil {
			return nil, false, fmt.Errorf("clearing fired timer: %w", err)
		}
		return &NodeResult{Output: output}, true, nil

	case workflow.NodeTypeWebhookWait:
		if inst.WebhookReceivedAt == nil {
			return nil, false, nil
		}
		output := map[string]any{}
		if rec, err := e.nodeExecutions.GetByInstanceAndNode(ctx, inst.ID, node.ID); err == nil && rec != nil && rec.Output != nil {
			output = rec.Output
		}
		// Clearing the token also clears webhook_received_at (see
		// InstanceRepository.SetWebhookFields).
		if err := e.instances.SetWebhookFields(ctx, inst.ID, nil, nil); err != nil {
			return nil, false, fmt.Errorf("clearing delivered webhook: %w", err)
		}
		if inst.WaitingUntil != nil {
			if err := e.instances.SetWaiting(ctx, inst.ID, nil); err != nil {
				return nil, false, fmt.Errorf("clearing webhook deadline: %w", err)
			}
		}
		return &NodeResult{Output: output}, true, nil
	}
	return nil, false, nil
}
