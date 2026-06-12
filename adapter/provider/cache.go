package provider

import (
	"context"
	"sync"

	"github.com/arbitron/arbitron/domain/provider"
)

// ProfileCache is an in-memory snapshot of the profile registry behind a
// sync.RWMutex. It binds the ProfileStore at construction so it satisfies the
// Phase-4 recache hook shape (Reload(ctx) error) directly: the ProfileService
// calls Reload after every successful write so the cache (and therefore the
// resolver and the workflow-validation checkers that read it) reflects live
// edits. Readers take the read-lock; a Load/Reload takes the write-lock.
type ProfileCache struct {
	store   provider.ProfileStore
	mu      sync.RWMutex
	byAlias map[string]provider.Profile
}

// NewProfileCache binds the store and starts with an empty (non-nil) registry.
// Call Load once before serving so the cache is populated.
func NewProfileCache(store provider.ProfileStore) *ProfileCache {
	return &ProfileCache{store: store, byAlias: map[string]provider.Profile{}}
}

// Load (re)populates the cache from the bound store, replacing the snapshot
// atomically under the write-lock. An empty store yields an empty (non-nil)
// registry, not an error.
func (c *ProfileCache) Load(ctx context.Context) error {
	list, err := c.store.List(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]provider.Profile, len(list))
	for _, p := range list {
		m[p.Alias] = p
	}
	c.mu.Lock()
	c.byAlias = m
	c.mu.Unlock()
	return nil
}

// Reload is the recache hook; it is an exact alias of Load so a caller holding
// only the cache (the Phase-4 ProfileService) can refresh the snapshot after a
// write. Keeping the signature Reload(ctx) error lets ProfileCache satisfy the
// Phase-4 Reloader interface structurally without this phase importing it.
func (c *ProfileCache) Reload(ctx context.Context) error { return c.Load(ctx) }

// get returns the cached profile for an alias under the read-lock. ok is false
// when the alias is unknown.
func (c *ProfileCache) get(alias string) (provider.Profile, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.byAlias[alias]
	return p, ok
}
