# Phase 3, Task 3: Webhook Wait Executor + Instance Model Fields + Database Migration

## Objective
Implement the webhook wait executor that generates tokens, hashes secrets, transitions instances to waiting, and registers with the webhook registry and scheduler. Add webhook-related fields to the Instance model and create the database migration.

## Dependencies
- Phase 3, Task 1 (NodeTypeWebhookWait, NodeConfig fields)
- Phase 3, Task 2 (WebhookRegistry with Register method)
- Phase 2, Task 2 (Scheduler for timeout registration)
- Phase 2, Task 3 (WaitResult/IsWait pattern, executor registration)

## Files to Create/Modify
- `domain/instance/instance.go` — add `WebhookToken`, `WebhookSecretHash`, `WebhookReceivedAt` fields
- `domain/engine/executor_webhook.go` — webhook wait executor implementation
- `adapter/postgres/migrations/007_add_webhook_wait.up.sql` — migration
- `adapter/postgres/migrations/007_add_webhook_wait.down.sql` — rollback
- `adapter/postgres/instance_repo.go` — update queries to handle new columns
- `adapter/postgres/sqlc/queries/instances.sql` — update sqlc queries

## Implementation Steps

1. **Add webhook fields to Instance** in `domain/instance/instance.go`:
   ```go
   type Instance struct {
       // ... existing fields ...
       WaitingUntil      *time.Time
       WebhookToken      *string
       WebhookSecretHash *string
       WebhookReceivedAt *time.Time
   }
   ```

2. **Create database migration** `007_add_webhook_wait.up.sql`:
   ```sql
   ALTER TABLE instances ADD COLUMN webhook_token TEXT;
   ALTER TABLE instances ADD COLUMN webhook_secret_hash TEXT;
   ALTER TABLE instances ADD COLUMN webhook_received_at TIMESTAMPTZ;

   -- Unique index on webhook_token for fast lookup and uniqueness enforcement
   CREATE UNIQUE INDEX idx_instances_webhook_token ON instances (webhook_token)
       WHERE webhook_token IS NOT NULL;
   ```

3. **Create rollback** `007_add_webhook_wait.down.sql`:
   ```sql
   DROP INDEX IF EXISTS idx_instances_webhook_token;
   ALTER TABLE instances DROP COLUMN IF EXISTS webhook_received_at;
   ALTER TABLE instances DROP COLUMN IF EXISTS webhook_secret_hash;
   ALTER TABLE instances DROP COLUMN IF EXISTS webhook_token;
   ```

4. **Update sqlc queries** — add new columns to `CreateInstance`, `GetInstance`, and `ListInstances` queries. Regenerate.

5. **Define WebhookRegistry port** in `domain/engine/ports.go` (if not done in Task 2):
   ```go
   // WebhookRegistrar defines the port for registering webhook endpoints.
   type WebhookRegistrar interface {
       Register(token string, instanceID uuid.UUID, nodeID string, secretHash *string)
   }
   ```

6. **Implement WebhookWaitExecutor** in `domain/engine/executor_webhook.go`:
   ```go
   package engine

   import (
       "context"
       "crypto/hmac"
       "crypto/rand"
       "crypto/sha256"
       "encoding/base64"
       "fmt"
       "strconv"
       "time"

       "github.com/arbitron/arbitron/domain/instance"
       "github.com/arbitron/arbitron/domain/variable"
       "github.com/arbitron/arbitron/domain/workflow"
       "github.com/google/uuid"
   )

   type WebhookWaitExecutor struct {
       instances  instance.InstanceRepository
       nodeExecs  instance.NodeExecutionRepository
       registry   WebhookRegistrar
       scheduler  TimerScheduler
       queueMgr   QueueManager
   }

   func NewWebhookWaitExecutor(
       instances instance.InstanceRepository,
       nodeExecs instance.NodeExecutionRepository,
       registry WebhookRegistrar,
       scheduler TimerScheduler,
       queueMgr QueueManager,
   ) *WebhookWaitExecutor {
       return &WebhookWaitExecutor{
           instances: instances,
           nodeExecs: nodeExecs,
           registry:  registry,
           scheduler: scheduler,
           queueMgr:  queueMgr,
       }
   }
   ```

7. **Implement Execute method**:
   ```go
   func (e *WebhookWaitExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error) {
       startTime := time.Now()

       // 1. Generate cryptographically random URL-safe token (32 bytes)
       tokenBytes := make([]byte, 32)
       if _, err := rand.Read(tokenBytes); err != nil {
           return nil, fmt.Errorf("generating token: %w", err)
       }
       token := base64.RawURLEncoding.EncodeToString(tokenBytes)

       // 2. Hash secret if configured
       var secretHash *string
       if node.Config.Secret != "" {
           mac := hmac.New(sha256.New, []byte(token))
           mac.Write([]byte(node.Config.Secret))
           hash := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
           secretHash = &hash
       }

       // 3. Parse timeout if configured
       var deadline *time.Time
       if node.Config.WaitTimeoutSeconds != "" {
           timeoutStr := resolveTemplate(node.Config.WaitTimeoutSeconds, vars)
           timeoutSec, err := strconv.ParseFloat(timeoutStr, 64)
           if err != nil {
               return nil, fmt.Errorf("timeoutSeconds is not numeric: %q", timeoutStr)
           }
           d := time.Now().Add(time.Duration(timeoutSec * float64(time.Second)))
           deadline = &d
       }

       // 4. Write node execution record
       webhookURL := fmt.Sprintf("/api/v1/webhooks/wait/%s", token)
       now := time.Now()
       exec := &instance.NodeExecution{
           ID:         uuid.New(),
           InstanceID: instanceID,
           NodeID:     node.ID,
           Attempt:    1,
           Status:     instance.NodeStatusSucceeded,
           Output:     map[string]any{"webhookUrl": webhookURL},
           StartedAt:  startTime,
           CompletedAt: &now,
       }
       if err := e.nodeExecs.Create(ctx, exec); err != nil {
           return nil, fmt.Errorf("creating node execution: %w", err)
       }

       // 5. Transition instance to waiting, store token and secret hash
       if err := e.instances.SetWaiting(ctx, instanceID, deadline); err != nil {
           return nil, fmt.Errorf("setting waiting state: %w", err)
       }
       if err := e.instances.SetWebhookFields(ctx, instanceID, &token, secretHash); err != nil {
           return nil, fmt.Errorf("setting webhook fields: %w", err)
       }

       // 6. Register with webhook registry
       e.registry.Register(token, instanceID, node.ID, secretHash)

       // 7. If timeout configured, register with scheduler (webhook_timeout type)
       if deadline != nil {
           e.scheduler.Schedule(instanceID, *deadline, "webhook_timeout")
       }

       // 8. Notify queue manager (capacity freed)
       e.queueMgr.NotifyCapacityFreed(ctx)

       // 9. Return wait result
       return &NodeResult{IsWait: true}, nil
   }
   ```

8. **Add SetWebhookFields to InstanceRepository**:
   ```go
   SetWebhookFields(ctx context.Context, id uuid.UUID, token *string, secretHash *string) error
   ```

9. **Implement SetWebhookFields in Postgres adapter**:
   ```sql
   -- name: SetWebhookFields :exec
   UPDATE instances SET webhook_token = $2, webhook_secret_hash = $3, updated_at = now() WHERE id = $1;
   ```

10. **Register executor** in engine construction:
    ```go
    executors[workflow.NodeTypeWebhookWait] = webhookWaitExecutor
    ```

## Tests
- `domain/engine/executor_webhook_test.go`:
  - `TestWebhookWaitExecutor_GeneratesURLSafeToken` — token is valid base64url, 32 bytes decoded
  - `TestWebhookWaitExecutor_SecretHashing` — secret configured → secretHash is non-nil HMAC
  - `TestWebhookWaitExecutor_NoSecret_NoHash` — no secret → secretHash is nil
  - `TestWebhookWaitExecutor_TimeoutParsing` — substitution resolves, deadline calculated
  - `TestWebhookWaitExecutor_NonNumericTimeout_Fails` — returns error
  - `TestWebhookWaitExecutor_NoTimeout_NoScheduler` — scheduler.Schedule not called
  - `TestWebhookWaitExecutor_WritesNodeExecution` — node execution record with webhookUrl in output
  - `TestWebhookWaitExecutor_TransitionsToWaiting` — SetWaiting called
  - `TestWebhookWaitExecutor_RegistersWithRegistry` — registry.Register called with correct args
  - `TestWebhookWaitExecutor_NotifiesQueueManager` — NotifyCapacityFreed called
  - `TestWebhookWaitExecutor_ReturnsIsWait` — result.IsWait is true

## Acceptance Criteria
- Token generation uses `crypto/rand` with 32 bytes, encoded as base64url
- Secret is hashed with SHA-256 HMAC using token as key; plain text never stored
- Timeout supports `{{substitution}}` and fails on non-numeric values
- Node execution record includes `webhookUrl` in output
- Instance transitions to `waiting` with webhook_token and secret_hash stored
- Token registered in webhook registry for HTTP handler lookup
- Timeout (if configured) registered with scheduler
- Queue manager notified of freed capacity
- Database migration adds columns with unique index on webhook_token
- All tests pass with `-race`
