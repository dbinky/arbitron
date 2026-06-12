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

// fixedProfileStore is a read-only, in-memory ProfileStore used to back a cache
// over a fixed profile set (no DB). Writes are unsupported — it exists so a
// caller with an immutable profile slice can build a DynamicProfileResolver
// without a database, and so a Reload re-reads the same fixed set.
type fixedProfileStore struct{ profiles []provider.Profile }

func (s fixedProfileStore) Create(context.Context, provider.Profile) error {
	return provider.ErrReadOnlyStore
}
func (s fixedProfileStore) Update(context.Context, provider.Profile) error {
	return provider.ErrReadOnlyStore
}
func (s fixedProfileStore) Delete(context.Context, string) error { return provider.ErrReadOnlyStore }
func (s fixedProfileStore) Get(_ context.Context, alias string) (provider.Profile, error) {
	for _, p := range s.profiles {
		if p.Alias == alias {
			return p, nil
		}
	}
	return provider.Profile{}, provider.ErrProfileNotFound
}
func (s fixedProfileStore) List(context.Context) ([]provider.Profile, error) {
	out := make([]provider.Profile, len(s.profiles))
	copy(out, s.profiles)
	return out, nil
}
func (s fixedProfileStore) Count(context.Context) (int, error) { return len(s.profiles), nil }

// NewProfileCacheFromProfiles builds a cache over a fixed, in-memory profile set
// and loads it eagerly. It is the non-DB analogue of NewProfileCache+Load for a
// caller (or a test) that holds an immutable profile slice rather than a store.
// The last entry wins on a duplicate alias.
func NewProfileCacheFromProfiles(profiles ...provider.Profile) *ProfileCache {
	c := NewProfileCache(fixedProfileStore{profiles: profiles})
	_ = c.Load(context.Background()) // fixedProfileStore.List never errors
	return c
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
