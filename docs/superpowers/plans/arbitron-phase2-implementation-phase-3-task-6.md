# Phase 3, Task 6: Crash Recovery + API Changes + Integration Tests

## Objective
Implement webhook crash recovery (re-register tokens on startup), add webhook-related fields to the API response, and write end-to-end integration tests for the full webhook wait lifecycle.

## Dependencies
- Phase 3, Tasks 1-5 (all webhook infrastructure must be complete)

## Files to Create/Modify
- `domain/queue/recovery.go` — add webhook recovery logic
- `adapter/http/instance_handler.go` — add `webhookUrl` and `webhookReceived` to response
- `adapter/postgres/instance_repo.go` — add `ListWaitingWithWebhook` query
- `tests/integration/webhook_wait_test.go` — end-to-end tests

## Implementation Steps

1. **Add ListWaitingWithWebhook to QueueRepository**:
   ```go
   ListWaitingWithWebhook(ctx context.Context) ([]Instance, error)
   ```
   SQL query:
   ```sql
   -- name: ListWaitingWithWebhook :many
   SELECT * FROM instances WHERE state = 'waiting' AND webhook_token IS NOT NULL;
   ```

2. **Implement RecoverWebhooks in Manager** (`domain/queue/recovery.go`):
   ```go
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

           // Re-register token in webhook registry
           m.webhookRegistry.Register(*inst.WebhookToken, inst.ID, m.getWaitingNodeID(ctx, inst), inst.WebhookSecretHash)

           // Check timeout if configured
           // Need to look at the workflow snapshot to find the webhook_wait node config
           nodeConfig, err := m.getWebhookNodeConfig(ctx, inst)
           if err != nil || nodeConfig == nil {
               continue
           }

           if nodeConfig.WaitTimeoutSeconds != "" {
               // Find the node execution to get started_at
               // Calculate original deadline = started_at + timeout
               nodeExec := m.getNodeExecution(ctx, inst)
               if nodeExec != nil {
                   timeoutSec, err := strconv.ParseFloat(nodeConfig.WaitTimeoutSeconds, 64)
                   if err == nil {
                       deadline := nodeExec.StartedAt.Add(time.Duration(timeoutSec * float64(time.Second)))
                       if deadline.Before(now) {
                           // Timeout expired during downtime
                           m.webhookRegistry.Deactivate(*inst.WebhookToken)
                           _ = m.instances.SetFailed(ctx, inst.ID, nodeExec.NodeID)
                       } else {
                           // Timeout still pending — register with scheduler
                           m.scheduler.Schedule(inst.ID, deadline, TimerTypeWebhook)
                       }
                   }
               }
           }
       }

       return m.Evaluate(ctx)
   }
   ```

3. **Helper: getWebhookNodeConfig** — extracts the webhook_wait node config from the workflow snapshot:
   ```go
   func (m *Manager) getWebhookNodeConfig(ctx context.Context, inst instance.Instance) (*workflow.NodeConfig, error) {
       // Parse inst.WorkflowSnapshot to find the current node (webhook_wait type)
       // Return its config
   }
   ```

4. **Wire RecoverWebhooks into startup** — call after `RecoverTimers`:
   ```go
   if err := queueManager.RecoverTimers(ctx); err != nil {
       log.Error("timer recovery failed", "error", err)
   }
   if err := queueManager.RecoverWebhooks(ctx); err != nil {
       log.Error("webhook recovery failed", "error", err)
   }
   ```

5. **Add API response fields** in `adapter/http/instance_handler.go`:
   ```go
   type instanceResponse struct {
       // ... existing fields ...
       QueuedAt        *string `json:"queuedAt,omitempty"`
       WaitingUntil    *string `json:"waitingUntil,omitempty"`
       WebhookUrl      *string `json:"webhookUrl,omitempty"`
       WebhookReceived bool    `json:"webhookReceived,omitempty"`
   }
   ```

6. **Update toInstanceResponse**:
   ```go
   if inst.WebhookToken != nil {
       url := fmt.Sprintf("/api/v1/webhooks/wait/%s", *inst.WebhookToken)
       resp.WebhookUrl = &url
   }
   if inst.WebhookReceivedAt != nil {
       resp.WebhookReceived = true
   }
   ```

7. **Write integration tests** in `tests/integration/webhook_wait_test.go`:

   ```go
   func TestWebhookWait_FullFlow(t *testing.T) {
       // Create workflow: entrypoint → webhook_wait → script_node
       // Trigger the workflow
       // Verify: instance → running → webhook_wait → waiting
       // Get instance via API: verify webhookUrl is set
       // POST to the webhook URL with JSON payload
       // Verify: instance transitions waiting → queued → running
       // Verify: script_node can access webhook payload via {{nodeId.field}}
       // Verify: instance completes
   }

   func TestWebhookWait_WithSecret_Valid(t *testing.T) {
       // Workflow with secret="mysecret"
       // Trigger, wait for waiting state
       // POST with X-Webhook-Secret: mysecret → 200
       // Instance continues
   }

   func TestWebhookWait_WithSecret_Invalid(t *testing.T) {
       // Workflow with secret="mysecret"
       // Trigger, wait for waiting state
       // POST with X-Webhook-Secret: wrongsecret → 401
       // Instance stays waiting
       // POST with correct secret → 200, instance continues
   }

   func TestWebhookWait_WithTimeout_TimesOut(t *testing.T) {
       // Workflow with timeoutSeconds="2"
       // Trigger, wait for waiting state
       // Wait 2.5 seconds without posting
       // Verify: instance fails (timeout)
       // Verify: webhook endpoint returns 410
   }

   func TestWebhookWait_WithTimeout_WebhookBeforeTimeout(t *testing.T) {
       // Workflow with timeoutSeconds="60"
       // Trigger, wait for waiting state
       // POST webhook immediately
       // Verify: instance continues (timer cancelled)
   }

   func TestWebhookWait_CancelWhileWaiting(t *testing.T) {
       // Trigger, wait for waiting state
       // Cancel the instance
       // Verify: state is cancelled
       // POST to webhook URL → 410 (endpoint deactivated)
   }

   func TestWebhookWait_PauseWhileWaiting_WebhookArrives(t *testing.T) {
       // Trigger, wait for waiting state
       // Pause the instance
       // POST webhook → 200 (accepted)
       // Resume the instance
       // Verify: instance transitions to queued → running → continues
   }

   func TestWebhookWait_CrashRecovery(t *testing.T) {
       // Create instance in waiting state with webhook_token in database
       // Create new WebhookRegistry (empty — simulating restart)
       // Run RecoverWebhooks
       // Verify: token is re-registered in registry
       // POST to webhook URL → 200
       // Verify: instance transitions to queued
   }

   func TestWebhookWait_CrashRecovery_TimedOut(t *testing.T) {
       // Create instance in waiting state with webhook_token
       // Set node execution started_at to 1 hour ago, timeout = 60s
       // Run RecoverWebhooks
       // Verify: instance is failed (timeout expired during downtime)
       // Verify: endpoint is deactivated
   }

   func TestWebhookWait_SecondHit_Returns410(t *testing.T) {
       // Trigger, wait for waiting state
       // POST webhook → 200
       // POST same URL again → 410 (already received)
   }

   func TestWebhookWait_APIResponse_Fields(t *testing.T) {
       // Trigger webhook_wait workflow
       // GET /instances/{id}
       // Verify: webhookUrl field present, webhookReceived is false
       // POST webhook
       // GET /instances/{id}
       // Verify: webhookReceived is true
   }
   ```

## Tests
All integration tests listed in implementation steps above.

## Acceptance Criteria
- Crash recovery re-registers webhook tokens in the in-memory registry
- Crash recovery detects timed-out webhooks and fails them appropriately
- Crash recovery re-registers pending timeouts with the scheduler
- `GET /instances/{id}` includes `webhookUrl` (when webhook active) and `webhookReceived` (boolean)
- Full webhook wait lifecycle works end-to-end
- Secret validation works correctly (valid accepted, invalid rejected)
- Timeout works (fires when webhook not received in time)
- Cancel/pause/resume operations work correctly for webhook-waiting instances
- Second POST to same endpoint returns 410
- All integration tests pass with `-race` flag
- Tests complete within 30 seconds (use short timeouts)
