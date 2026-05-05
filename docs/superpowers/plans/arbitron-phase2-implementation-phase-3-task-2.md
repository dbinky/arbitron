# Phase 3, Task 2: Webhook Registry (adapter/http/webhook_registry.go)

## Objective
Implement the in-memory webhook registry that maps tokens to instance metadata for fast lookup by the HTTP handler, with thread-safe operations.

## Dependencies
- None (standalone data structure, though used by Phase 3 Tasks 3-6)

## Files to Create/Modify
- `adapter/http/webhook_registry.go` — the WebhookRegistry implementation
- `adapter/http/webhook_registry_test.go` — unit tests

## Implementation Steps

1. **Define the webhookEntry struct**:
   ```go
   package http

   import (
       "sync"
       "github.com/google/uuid"
   )

   // webhookEntry holds metadata for an active webhook wait endpoint.
   type webhookEntry struct {
       InstanceID uuid.UUID
       NodeID     string
       SecretHash *string // SHA-256 HMAC hash of the secret (nil if no secret configured)
   }
   ```

2. **Define the WebhookRegistry struct**:
   ```go
   // WebhookRegistry maps webhook tokens to instance metadata for fast lookup.
   type WebhookRegistry struct {
       mu      sync.RWMutex
       entries map[string]webhookEntry
   }

   func NewWebhookRegistry() *WebhookRegistry {
       return &WebhookRegistry{
           entries: make(map[string]webhookEntry),
       }
   }
   ```

3. **Implement Register**:
   ```go
   // Register adds an active webhook endpoint token to the registry.
   func (r *WebhookRegistry) Register(token string, entry webhookEntry) {
       r.mu.Lock()
       defer r.mu.Unlock()
       r.entries[token] = entry
   }
   ```

4. **Implement Lookup**:
   ```go
   // Lookup retrieves the entry for a token. Returns false if not found.
   func (r *WebhookRegistry) Lookup(token string) (webhookEntry, bool) {
       r.mu.RLock()
       defer r.mu.RUnlock()
       entry, ok := r.entries[token]
       return entry, ok
   }
   ```

5. **Implement Deactivate**:
   ```go
   // Deactivate removes an active webhook endpoint. No-op if token doesn't exist.
   func (r *WebhookRegistry) Deactivate(token string) {
       r.mu.Lock()
       defer r.mu.Unlock()
       delete(r.entries, token)
   }
   ```

6. **Implement ActiveCount**:
   ```go
   // ActiveCount returns the number of currently active webhook endpoints.
   func (r *WebhookRegistry) ActiveCount() int {
       r.mu.RLock()
       defer r.mu.RUnlock()
       return len(r.entries)
   }
   ```

7. **Define the port interface** — for use by domain code that needs to register/deactivate webhooks without depending on the adapter package. Create in `domain/engine/ports.go` or a new file:
   ```go
   // WebhookRegistry defines the port for managing active webhook endpoints.
   type WebhookRegistry interface {
       Register(token string, instanceID uuid.UUID, nodeID string, secretHash *string)
       Deactivate(token string)
   }
   ```
   The adapter's `WebhookRegistry` implements this interface (with the entry struct as an internal detail).

## Tests
- `adapter/http/webhook_registry_test.go`:
  - `TestRegistry_Register_Lookup` — register a token, lookup succeeds with correct entry
  - `TestRegistry_Lookup_NotFound` — lookup unknown token returns false
  - `TestRegistry_Deactivate` — deactivate token, subsequent lookup returns false
  - `TestRegistry_Deactivate_NonExistent_NoOp` — no panic or error
  - `TestRegistry_ActiveCount_Empty` — returns 0
  - `TestRegistry_ActiveCount_AfterRegistrations` — returns correct count
  - `TestRegistry_ActiveCount_AfterDeactivation` — count decreases
  - `TestRegistry_ConcurrentAccess` — run with `-race`, multiple goroutines doing Register/Lookup/Deactivate concurrently

## Acceptance Criteria
- `WebhookRegistry` struct exists in `adapter/http/webhook_registry.go`
- Thread-safe via `sync.RWMutex` (reads use RLock, writes use Lock)
- `Register` adds entries, `Lookup` retrieves them, `Deactivate` removes them
- `ActiveCount` returns the current number of active entries
- Deactivating a non-existent token is a no-op (no panic)
- All tests pass with `-race` flag
- A domain-side interface exists for the executor to use without importing the adapter
