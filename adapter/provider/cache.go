package provider

import (
	"context"
	"fmt"
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
	loadMu  sync.Mutex // serializes whole Load calls (read, build, swap) so an older read cannot publish last
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
			return p.Clone(), nil
		}
	}
	return provider.Profile{}, provider.ErrProfileNotFound
}
func (s fixedProfileStore) List(context.Context) ([]provider.Profile, error) {
	out := make([]provider.Profile, len(s.profiles))
	for i, p := range s.profiles {
		out[i] = p.Clone()
	}
	return out, nil
}
func (s fixedProfileStore) Count(context.Context) (int, error) { return len(s.profiles), nil }

// NewProfileCacheFromProfiles builds a cache over a fixed, in-memory profile set
// and loads it eagerly. It is the non-DB analogue of NewProfileCache+Load for a
// caller (or a test) that holds an immutable profile slice rather than a store.
// The last entry wins on a duplicate alias.
func NewProfileCacheFromProfiles(profiles ...provider.Profile) *ProfileCache {
	owned := make([]provider.Profile, len(profiles))
	for i, p := range profiles {
		owned[i] = p.Clone()
	}
	c := NewProfileCache(fixedProfileStore{profiles: owned})
	_ = c.Load(context.Background()) // fixedProfileStore.List never errors
	return c
}

// Load (re)populates the cache from the bound store, replacing the snapshot
// atomically under the write-lock. An empty store yields an empty (non-nil)
// registry, not an error. Whole loads are serialized (loadMu) so two reloads
// that overlap cannot publish in reverse order and regress the cache to an older
// store read; readers only contend on mu for the final swap.
func (c *ProfileCache) Load(ctx context.Context) error {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	list, err := c.store.List(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]provider.Profile, len(list))
	for _, p := range list {
		m[p.Alias] = p.Clone()
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

// PublishProfile installs one committed row without a full reload, so a write
// takes effect even when Load fails on an unrelated row. It is serialized with
// Load and never replaces a newer cached revision with an older row.
func (c *ProfileCache) PublishProfile(p provider.Profile) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.byAlias[p.Alias]; ok && cur.Revision > p.Revision {
		return
	}
	next := make(map[string]provider.Profile, len(c.byAlias)+1)
	for a, q := range c.byAlias {
		next[a] = q
	}
	next[p.Alias] = p.Clone()
	c.byAlias = next
}

// ForgetProfile removes a deleted alias without a full reload.
func (c *ProfileCache) ForgetProfile(alias string) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byAlias[alias]; !ok {
		return
	}
	next := make(map[string]provider.Profile, len(c.byAlias))
	for a, q := range c.byAlias {
		if a != alias {
			next[a] = q
		}
	}
	c.byAlias = next
}

// get returns the cached profile for an alias under the read-lock. ok is false
// when the alias is unknown.
func (c *ProfileCache) get(alias string) (provider.Profile, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.byAlias[alias]
	return p.Clone(), ok
}

// SnapshotProfiles returns clones of the named aliases taken under ONE read-lock,
// so every profile comes from the same registry generation: a concurrent
// Load/Reload cannot land between two aliases and hand a run a mixed view. The
// result is independent of the cache (callers may mutate it). An unknown alias
// returns ErrUnknownProfile wrapped with the alias and no map.
func (c *ProfileCache) SnapshotProfiles(aliases []string) (map[string]provider.Profile, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]provider.Profile, len(aliases))
	for _, a := range aliases {
		p, ok := c.byAlias[a]
		if !ok {
			return nil, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, a)
		}
		out[a] = p.Clone()
	}
	return out, nil
}

// PublishCommitted reloads the registry and confirms every committed
// advancement is reconciled with the authoritative registry. A present alias
// must have a revision no older than the commit's, and at exactly that revision
// must carry the committed model. Later admin edits and deletions supersede the
// commit; repair never restores them. An unconfirmed live row returns an error
// so the caller keeps the publication pending.
func (c *ProfileCache) PublishCommitted(ctx context.Context, advs []provider.Advancement) error {
	if err := c.Load(ctx); err != nil {
		return fmt.Errorf("reloading profiles for publication: %w", err)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, a := range advs {
		p, ok := c.byAlias[a.Alias]
		switch {
		case !ok:
			// Load confirmed the authoritative deletion and removed the alias.
			continue
		case p.Revision < a.Revision:
			return fmt.Errorf("publishing %q: cache revision %d older than committed %d", a.Alias, p.Revision, a.Revision)
		case p.Revision == a.Revision && p.Model != a.ToModel:
			return fmt.Errorf("publishing %q: cache model %q, committed %q", a.Alias, p.Model, a.ToModel)
		}
	}
	return nil
}

var (
	_ provider.AdvancementPublisher = (*ProfileCache)(nil)
	_ provider.ProfilePublisher     = (*ProfileCache)(nil)
)
