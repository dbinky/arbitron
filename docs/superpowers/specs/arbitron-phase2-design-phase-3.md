# Arbitron Phase 2 — Design Phase 3: Webhook Wait Node

## Overview

Phase 3 adds the Webhook Wait node type — the second wait node. When the engine reaches a webhook wait node, it generates a unique endpoint, transitions the instance to `waiting`, and resumes when an external system POSTs to that endpoint. Optionally validates a shared secret and supports configurable timeouts.

---

## Workflow Model Changes

### New Node Type

Add `NodeTypeWebhookWait NodeType = "webhook_wait"` to `domain/workflow/workflow.go`.

### Webhook Wait Config

```go
type WebhookWaitConfig struct {
    Secret         string  // optional, plain text in workflow definition
    TimeoutSeconds string  // optional, supports {{substitution}}
}
```

### Validation Rules

- Webhook wait nodes must have exactly one outgoing edge
- Webhook wait nodes cannot be entry points
- No required fields (both secret and timeout are optional)

---

## Webhook Wait Executor: `domain/engine/executor_webhook.go`

### Execution Flow

1. Generate a cryptographically random, URL-safe token (32 bytes via `crypto/rand`, base64url encoded)
2. If `secret` is configured: hash it with SHA-256 HMAC (using the token as the key) and store the hash. The plain-text secret is not persisted.
3. If `timeoutSeconds` is configured: resolve `{{substitution}}`, parse to float64 — fail the node if non-numeric
4. Write a node execution record with status `succeeded` and the webhook URL in the output: `{"webhookUrl": "/api/v1/webhooks/wait/{token}"}`
5. Transition instance to `waiting` state, store `webhook_token` and `webhook_secret_hash` on the instance
6. Register the token with the webhook registry
7. If timeout is configured: calculate deadline and register with the timer scheduler (reuse Phase 2's scheduler)
8. Notify queue manager to evaluate (capacity freed)
9. Return `WaitResult` to stop the engine's `run()` loop

---

## Webhook Registry: `adapter/http/webhook_registry.go`

In-memory registry mapping tokens to instance metadata for fast lookup by the HTTP handler.

### Data Structure

```go
type webhookEntry struct {
    InstanceID uuid.UUID
    NodeID     string
    SecretHash *string
}
```

Map: `map[string]webhookEntry` — token to entry. Protected by `sync.RWMutex`.

### Methods

- `Register(token string, entry webhookEntry)` — add an active endpoint
- `Lookup(token string) (webhookEntry, bool)` — check if token is active
- `Deactivate(token string)` — remove an active endpoint
- `ActiveCount() int` — for observability

---

## Webhook HTTP Handler: `adapter/http/webhook_handler.go`

### Route

`POST /api/v1/webhooks/wait/{token}` — registered on the chi router alongside existing routes.

### Handler Flow

1. Extract `{token}` from URL path
2. Look up in registry — return `404 Not Found` if not registered
3. Verify instance is in `waiting` or `paused` state (database check) — return `410 Gone` if in any other state (cancelled, completed, failed)
4. If secret hash exists on the entry: validate `X-Webhook-Secret` header against hash — return `401 Unauthorized` if invalid or missing
5. Read and parse request body as JSON. Non-JSON or empty body → use `{}`
6. Store payload as node output in `node_executions` table (update the existing record)
7. Deactivate token in registry
8. If timeout was configured: cancel the pending timer via scheduler
9. Set `webhook_received_at` on the instance
10. If instance is in `waiting` state: transition to `queued`, set `queued_at`, notify queue manager
11. If instance is in `paused` state: store payload and mark as received — the resume logic will transition to `queued`
12. Return `200 OK` with `{"status": "accepted"}`

### Error Responses

| Status | Condition |
|--------|-----------|
| 404 | Token not found in registry |
| 401 | Secret configured but header missing or invalid |
| 410 | Instance is no longer waiting (already received, cancelled, timed out) |
| 405 | Non-POST method |

---

## Instance Model Changes

### New Fields

- `WebhookToken *string` — the URL token for the active webhook endpoint
- `WebhookSecretHash *string` — hashed secret for validation
- `WebhookReceivedAt *time.Time` — when the webhook was received

---

## Timeout Handling

Reuses the timer scheduler from Phase 2. When a webhook wait has `timeoutSeconds`:

1. At node execution time: register a timer with the scheduler at `now + timeout`
2. If the timer fires before the webhook arrives:
   - Deactivate the token in the webhook registry
   - Fail the node per its error behavior
   - Clear webhook fields on the instance
   - Trigger queue evaluation
3. If the webhook arrives before the timeout: cancel the pending timer

---

## Waiting State Operations for Webhook Wait

### Cancel on Waiting Instance (Webhook)

1. Deactivate token in webhook registry
2. If timeout configured: cancel pending timer
3. Transition to `cancelled`
4. Notify queue manager

### Pause on Waiting Instance (Webhook)

1. Transition to `paused` — webhook endpoint stays active, timeout keeps counting
2. If webhook arrives while paused: store payload, deactivate endpoint, mark as received. On resume → go to `queued`.
3. If timeout fires while paused: deactivate endpoint, mark as timed out. On resume → fail the node per error behavior.
4. On resume with no webhook received and no timeout: go back to `waiting`

---

## Database Migration

Migration `007_add_webhook_wait`:
- Add `webhook_token` column (nullable text, unique index) to `instances`
- Add `webhook_secret_hash` column (nullable text) to `instances`
- Add `webhook_received_at` column (nullable timestamp) to `instances`

---

## API Changes

### Instance Response

- `webhookUrl` field (string, nullable) — the full endpoint URL, populated when instance has an active webhook
- `webhookReceived` field (boolean) — whether the webhook has been received

### New Route

- `POST /api/v1/webhooks/wait/{token}` — the webhook receiver, registered on the chi router

---

## Crash Recovery

Added to the startup sequence after Phase 2's timer recovery:

1. Query all instances in `waiting` state with non-null `webhook_token`
2. For each:
   - Re-register the token in the in-memory webhook registry
   - If `timeoutSeconds` was configured (check node config in workflow snapshot):
     - Calculate original deadline from node execution `started_at` + timeout
     - If deadline has passed → fail the node per error behavior, deactivate
     - If deadline is in the future → register with the scheduler
3. Run a queue evaluation pass

---

## Tests

### Executor Unit Tests

- Token generation: correct length, URL-safe characters
- Secret hashing: hash stored, plain text not persisted
- Timeout parsing: substitution, non-numeric failure
- Node execution record includes webhookUrl in output
- No secret configured: no hash stored

### Handler Unit Tests

- Valid POST with JSON body: 200, payload stored, instance transitions to queued
- Valid POST with empty body: 200, output is `{}`
- Valid POST with non-JSON body: 200, output is `{}`
- Invalid secret: 401, instance stays waiting
- Missing secret header when required: 401
- Unknown token: 404
- Already-received token: 410
- Cancelled instance token: 410
- Non-POST method: 405

### Registry Unit Tests

- Register, lookup, deactivate lifecycle
- Concurrent register/lookup/deactivate with `-race`
- Deactivate non-existent token: no-op

### Integration Tests

- Full flow: trigger → running → webhook wait → waiting → POST to endpoint → queued → running → continues
- With secret: valid secret → accepted, invalid → rejected, workflow still waiting
- With timeout: timeout fires before webhook → node fails
- Webhook arrives before timeout → timer cancelled, instance continues
- Crash recovery: restart with active webhook → endpoint still works
- Pause while waiting for webhook: webhook arrives while paused → queued on resume
