# Phase 3, Task 4: Webhook HTTP Handler (The Receiver Endpoint)

## Objective
Implement the `POST /api/v1/webhooks/wait/{token}` HTTP handler that receives webhook payloads, validates secrets, stores payloads, and transitions instances back to the queue.

## Dependencies
- Phase 3, Task 2 (WebhookRegistry with Lookup/Deactivate)
- Phase 3, Task 3 (Instance has WebhookToken/SecretHash fields, webhook_received_at column)
- Phase 2, Task 2 (Scheduler for cancelling timeout timers)
- Phase 1, Task 3 (Queue Manager for triggering evaluation)

## Files to Create/Modify
- `adapter/http/webhook_handler.go` — the webhook receiver handler
- `adapter/http/webhook_handler_test.go` — unit tests
- `adapter/http/router.go` — register the webhook route
- `domain/instance/ports.go` — add `SetWebhookReceived` to InstanceRepository

## Implementation Steps

1. **Define WebhookHandler struct**:
   ```go
   package http

   import (
       "crypto/hmac"
       "crypto/sha256"
       "encoding/base64"
       "encoding/json"
       "io"
       "net/http"
       "time"

       "github.com/go-chi/chi/v5"
       "github.com/google/uuid"

       "github.com/arbitron/arbitron/domain/instance"
   )

   // WebhookHandler handles incoming webhook POST requests.
   type WebhookHandler struct {
       registry   *WebhookRegistry
       instances  instance.InstanceRepository
       nodeExecs  instance.NodeExecutionRepository
       scheduler  WebhookTimerCanceller
       queueRepo  instance.QueueRepository
       queueMgr   WebhookQueueNotifier
   }

   // WebhookTimerCanceller cancels pending timeout timers.
   type WebhookTimerCanceller interface {
       Cancel(instanceID uuid.UUID)
   }

   // WebhookQueueNotifier triggers queue evaluation.
   type WebhookQueueNotifier interface {
       NotifyCapacityFreed(ctx context.Context)
   }
   ```

2. **Implement the handler**:
   ```go
   func (h *WebhookHandler) ReceiveWebhook(w http.ResponseWriter, r *http.Request) {
       // Only accept POST
       if r.Method != http.MethodPost {
           http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
           return
       }

       // 1. Extract token from URL
       token := chi.URLParam(r, "token")

       // 2. Lookup in registry
       entry, ok := h.registry.Lookup(token)
       if !ok {
           http.Error(w, "Not Found", http.StatusNotFound)
           return
       }

       // 3. Verify instance state (database check)
       inst, err := h.instances.Get(r.Context(), entry.InstanceID)
       if err != nil {
           http.Error(w, "Not Found", http.StatusNotFound)
           return
       }
       if inst.State != instance.StateWaiting && inst.State != instance.StatePaused {
           // Instance is no longer waiting (cancelled, completed, failed)
           h.registry.Deactivate(token)
           http.Error(w, "Gone", http.StatusGone)
           return
       }

       // 4. Validate secret if configured
       if entry.SecretHash != nil {
           secret := r.Header.Get("X-Webhook-Secret")
           if secret == "" {
               http.Error(w, "Unauthorized", http.StatusUnauthorized)
               return
           }
           // Recompute HMAC: HMAC-SHA256(key=token, message=secret)
           mac := hmac.New(sha256.New, []byte(token))
           mac.Write([]byte(secret))
           computedHash := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
           if !hmac.Equal([]byte(computedHash), []byte(*entry.SecretHash)) {
               http.Error(w, "Unauthorized", http.StatusUnauthorized)
               return
           }
       }

       // 5. Read and parse request body as JSON
       var payload map[string]any
       body, err := io.ReadAll(r.Body)
       if err != nil || len(body) == 0 {
           payload = map[string]any{}
       } else {
           if err := json.Unmarshal(body, &payload); err != nil {
               payload = map[string]any{} // Non-JSON body → empty object
           }
       }

       // 6. Store payload as node output (update existing node execution record)
       nodeExec, err := h.nodeExecs.GetByInstanceAndNode(r.Context(), entry.InstanceID, entry.NodeID)
       if err == nil && nodeExec != nil {
           nodeExec.Output = payload
           now := time.Now()
           nodeExec.CompletedAt = &now
           _ = h.nodeExecs.Update(r.Context(), nodeExec)
       }

       // 7. Deactivate token in registry
       h.registry.Deactivate(token)

       // 8. Cancel pending timeout timer if configured
       h.scheduler.Cancel(entry.InstanceID)

       // 9. Set webhook_received_at on instance
       now := time.Now()
       _ = h.instances.SetWebhookReceived(r.Context(), entry.InstanceID, now)

       // 10. Handle state transition
       if inst.State == instance.StateWaiting {
           // Transition to queued
           _ = h.queueRepo.SetQueued(r.Context(), entry.InstanceID, now)
           h.queueMgr.NotifyCapacityFreed(r.Context())
       }
       // If paused: payload stored, endpoint deactivated. Resume logic handles transition.

       // 11. Return 200 OK
       w.Header().Set("Content-Type", "application/json")
       w.WriteHeader(http.StatusOK)
       json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
   }
   ```

3. **Add SetWebhookReceived to InstanceRepository** in `domain/instance/ports.go`:
   ```go
   SetWebhookReceived(ctx context.Context, id uuid.UUID, receivedAt time.Time) error
   ```

4. **Implement SetWebhookReceived in Postgres adapter**:
   ```sql
   -- name: SetWebhookReceived :exec
   UPDATE instances SET webhook_received_at = $2, updated_at = now() WHERE id = $1;
   ```

5. **Register route** in `adapter/http/router.go`:
   ```go
   r.Post("/api/v1/webhooks/wait/{token}", webhookHandler.ReceiveWebhook)
   ```

6. **Constructor**:
   ```go
   func NewWebhookHandler(
       registry *WebhookRegistry,
       instances instance.InstanceRepository,
       nodeExecs instance.NodeExecutionRepository,
       scheduler WebhookTimerCanceller,
       queueRepo instance.QueueRepository,
       queueMgr WebhookQueueNotifier,
   ) *WebhookHandler {
       return &WebhookHandler{
           registry:  registry,
           instances: instances,
           nodeExecs: nodeExecs,
           scheduler: scheduler,
           queueRepo: queueRepo,
           queueMgr:  queueMgr,
       }
   }
   ```

## Tests
- `adapter/http/webhook_handler_test.go`:
  - `TestWebhookHandler_ValidPOST_JSONBody_200` — valid token, JSON body → 200, payload stored, instance transitions to queued
  - `TestWebhookHandler_ValidPOST_EmptyBody_200` — empty body → 200, output is `{}`
  - `TestWebhookHandler_ValidPOST_NonJSONBody_200` — text body → 200, output is `{}`
  - `TestWebhookHandler_UnknownToken_404` — token not in registry → 404
  - `TestWebhookHandler_InvalidSecret_401` — wrong secret → 401, instance stays waiting
  - `TestWebhookHandler_MissingSecretHeader_401` — no X-Webhook-Secret header when required → 401
  - `TestWebhookHandler_NoSecretConfigured_SkipsValidation` — no secret → 200 regardless of header
  - `TestWebhookHandler_CancelledInstance_410` — instance in cancelled state → 410
  - `TestWebhookHandler_CompletedInstance_410` — instance in completed state → 410
  - `TestWebhookHandler_PausedInstance_StoresPayload` — paused → 200, payload stored but no state transition
  - `TestWebhookHandler_DeactivatesToken` — subsequent request to same token → 404
  - `TestWebhookHandler_CancelsTimeoutTimer` — scheduler.Cancel called
  - `TestWebhookHandler_TriggersQueueEvaluation` — queueMgr.NotifyCapacityFreed called

## Acceptance Criteria
- `POST /api/v1/webhooks/wait/{token}` is registered and functional
- Valid webhook: 200, payload stored, token deactivated, instance transitions queued→running
- Unknown token: 404
- Invalid/missing secret: 401
- Already-processed instance: 410
- Non-JSON body: treated as `{}`, webhook still accepted
- Timeout timer cancelled on webhook receipt
- Queue evaluation triggered after state transition
- Paused instances: payload stored but no state transition (handled on resume)
- All tests pass
