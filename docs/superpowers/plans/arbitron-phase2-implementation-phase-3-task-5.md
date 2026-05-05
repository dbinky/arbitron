# Phase 3, Task 5: Timeout Handling (Reuse Scheduler) + Waiting State Operations for Webhook

## Objective
Wire the timer scheduler to handle webhook timeouts, and implement cancel/pause/resume operations for instances waiting on webhooks.

## Dependencies
- Phase 3, Task 3 (WebhookWaitExecutor registers timeout with scheduler)
- Phase 3, Task 4 (WebhookHandler deactivates on receipt)
- Phase 2, Task 2 (Scheduler infrastructure)
- Phase 2, Task 4 (Pattern for waiting state operations)

## Files to Create/Modify
- `domain/queue/scheduler.go` — extend `OnTimerFired` to handle webhook timeouts
- `domain/engine/engine.go` — extend Cancel/Pause/Resume for webhook wait instances
- `adapter/http/webhook_registry.go` — (already has Deactivate, just verify it's used)
- `domain/queue/manager.go` — extend `OnTimerFired` to distinguish timer delay vs webhook timeout

## Implementation Steps

1. **Extend OnTimerFired to handle webhook timeouts** — when a timer fires, the handler needs to know whether it's a timer delay expiration or a webhook timeout. Two approaches:
   
   **Approach A**: Store the reason in the scheduler's in-memory map:
   ```go
   type timerEntry struct {
       WaitingUntil time.Time
       Type         string // "timer_delay" or "webhook_timeout"
   }

   type Scheduler struct {
       mu      sync.Mutex
       timers  map[uuid.UUID]timerEntry
       // ...
   }
   ```

   **Approach B**: Check the instance state/fields when the timer fires to determine the action.
   
   **Recommended**: Approach A is simpler — store the type at schedule time and route accordingly.

2. **Update Schedule method signature**:
   ```go
   func (s *Scheduler) Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string)
   ```
   Constants:
   ```go
   const (
       TimerTypeDelay   = "timer_delay"
       TimerTypeWebhook = "webhook_timeout"
   )
   ```

3. **Update OnTimerFired to route by type**:
   ```go
   func (m *Manager) OnTimerFired(ctx context.Context, instanceID uuid.UUID, timerType string) error {
       inst, err := m.instances.Get(ctx, instanceID)
       if err != nil { return err }

       switch timerType {
       case TimerTypeDelay:
           // Existing logic: waiting → queued (if still waiting)
           if inst.State != instance.StateWaiting { return nil }
           now := time.Now()
           return m.queue.SetQueued(ctx, instanceID, now)

       case TimerTypeWebhook:
           // Webhook timeout: deactivate endpoint, fail the node
           if inst.State != instance.StateWaiting { return nil }
           // Deactivate webhook token
           if inst.WebhookToken != nil {
               m.webhookRegistry.Deactivate(*inst.WebhookToken)
           }
           // Clear webhook fields
           _ = m.instances.SetWebhookFields(ctx, instanceID, nil, nil)
           // Fail the node per error behavior
           // This requires access to the workflow snapshot to determine error behavior
           // Mark instance as failed with the webhook node as failed_node_id
           _ = m.instances.SetFailed(ctx, instanceID, m.getWebhookNodeID(ctx, instanceID))
           return nil
       }
       return nil
   }
   ```

4. **Add WebhookRegistry reference to Manager**:
   ```go
   type Manager struct {
       // ... existing fields ...
       webhookRegistry WebhookDeactivator
       instances       instance.InstanceRepository
   }

   type WebhookDeactivator interface {
       Deactivate(token string)
   }
   ```

5. **Implement Cancel for webhook-waiting instances** in Engine:
   ```go
   case instance.StateWaiting:
       // Check if waiting on webhook
       if inst.WebhookToken != nil {
           // Deactivate webhook endpoint
           e.webhookRegistry.Deactivate(*inst.WebhookToken)
       }
       // Cancel any pending timer (timeout or delay)
       e.scheduler.Cancel(id)
       // Transition to cancelled
       if err := e.instances.UpdateState(ctx, id, instance.StateCancelled); err != nil {
           return err
       }
       e.queueManager.NotifyCapacityFreed(ctx)
   ```

6. **Implement Pause for webhook-waiting instances** — same as timer delay pause (webhook endpoint stays active, timeout keeps counting):
   ```go
   case instance.StateWaiting:
       // Transition to paused — webhook endpoint stays active, timeout keeps counting
       if err := e.instances.UpdateState(ctx, id, instance.StatePaused); err != nil {
           return err
       }
   ```

7. **Extend Resume for webhook-paused instances**:
   ```go
   // In Resume, after checking WaitingUntil for timer delays:
   if inst.WebhookToken != nil {
       if inst.WebhookReceivedAt != nil {
           // Webhook received while paused → go to queued
           now := time.Now()
           if err := e.queueRepo.SetQueued(ctx, id, now); err != nil {
               return err
           }
           e.queueManager.NotifyCapacityFreed(ctx)
           return nil
       }
       // Check if timeout fired while paused (webhook_token cleared by timeout handler)
       // If WebhookToken is still set, no timeout → go back to waiting
       if err := e.instances.UpdateState(ctx, id, instance.StateWaiting); err != nil {
           return err
       }
       return nil
   }
   ```

8. **Add WebhookDeactivator to Engine** (port interface):
   ```go
   // In domain/engine/ports.go:
   type WebhookDeactivator interface {
       Deactivate(token string)
   }
   ```

## Tests
- `domain/queue/scheduler_test.go`:
  - `TestScheduler_WebhookTimeout_FiresAndDeactivates` — timer fires with webhook_timeout type
  - `TestScheduler_TimerType_StoredCorrectly` — schedule with type, verify on fire
- `domain/engine/engine_test.go`:
  - `TestCancel_WebhookWaiting_DeactivatesEndpoint` — webhook token deactivated
  - `TestCancel_WebhookWaiting_CancelsTimeout` — scheduler.Cancel called
  - `TestPause_WebhookWaiting_EndpointStaysActive` — webhook not deactivated
  - `TestResume_WebhookPaused_ReceivedWhilePaused_GoesToQueued` — WebhookReceivedAt set → queued
  - `TestResume_WebhookPaused_NotReceived_GoesToWaiting` — no reception → waiting
  - `TestResume_WebhookPaused_TimedOutWhilePaused_FailsNode` — token cleared → fail
- `domain/queue/manager_test.go`:
  - `TestOnTimerFired_WebhookTimeout_DeactivatesAndFails` — webhook endpoint deactivated, instance fails
  - `TestOnTimerFired_WebhookTimeout_InstanceNotWaiting_NoOp` — already cancelled → no action

## Acceptance Criteria
- Webhook timeout fires: endpoint deactivated, node fails per error behavior
- Webhook received before timeout: timeout timer cancelled (from Task 4)
- Cancel on webhook-waiting instance: deactivates endpoint, cancels timeout, transitions to cancelled
- Pause on webhook-waiting instance: endpoint stays active, timeout keeps counting
- Resume after webhook received while paused: transitions to queued
- Resume with no webhook received and no timeout: back to waiting
- Timer scheduler distinguishes between timer_delay and webhook_timeout types
- All tests pass with `-race`
