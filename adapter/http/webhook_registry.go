package http

import (
	"sync"

	"github.com/google/uuid"
)

// WebhookEntry holds metadata for an active webhook wait endpoint.
type WebhookEntry struct {
	InstanceID uuid.UUID
	NodeID     string
	SecretHash *string
}

// WebhookRegistry maps webhook tokens to instance metadata for fast lookup.
type WebhookRegistry struct {
	mu      sync.RWMutex
	entries map[string]WebhookEntry
}

// NewWebhookRegistry creates a new empty WebhookRegistry.
func NewWebhookRegistry() *WebhookRegistry {
	return &WebhookRegistry{
		entries: make(map[string]WebhookEntry),
	}
}

// Register adds an active webhook endpoint token to the registry.
func (r *WebhookRegistry) Register(token string, instanceID uuid.UUID, nodeID string, secretHash *string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[token] = WebhookEntry{
		InstanceID: instanceID,
		NodeID:     nodeID,
		SecretHash: secretHash,
	}
}

// Lookup retrieves the entry for a token. Returns false if not found.
func (r *WebhookRegistry) Lookup(token string) (WebhookEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[token]
	return entry, ok
}

// Deactivate removes an active webhook endpoint. No-op if token doesn't exist.
func (r *WebhookRegistry) Deactivate(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, token)
}

// ActiveCount returns the number of currently active webhook endpoints.
func (r *WebhookRegistry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}
