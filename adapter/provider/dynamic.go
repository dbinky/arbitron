package provider

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
)

// DynamicProfileResolver resolves aliases against a live ProfileCache (DB-backed,
// refreshed by Reload) and runs the full alias -> profile -> connection ->
// binding chain. It replaces the immutable StaticProfileResolver: the registry
// is no longer fixed at startup but reflects the cache, so profile edits become
// visible to the engine and workflow validation after a Reload. The connection
// lookup + lazy RevealKey logic is identical to the static resolver; the new
// ContextWindow/Effort axes are copied verbatim onto the binding on both the
// no-key and connection-backed branches.
type DynamicProfileResolver struct {
	cache *ProfileCache
	store provider.ConnectionStore
}

var _ provider.ProfileResolver = (*DynamicProfileResolver)(nil)

// NewDynamicProfileResolver wraps a populated ProfileCache and captures the
// ConnectionStore used to resolve connection-backed profiles. The store may be
// nil when every profile uses the claude-cli no-key subscription path.
func NewDynamicProfileResolver(cache *ProfileCache, store provider.ConnectionStore) *DynamicProfileResolver {
	return &DynamicProfileResolver{cache: cache, store: store}
}

// Reload refreshes the underlying cache from its bound store. It is exposed so a
// caller holding only the resolver can trigger a recache (delegating to
// ProfileCache.Reload).
func (r *DynamicProfileResolver) Reload(ctx context.Context) error {
	return r.cache.Reload(ctx)
}

// Kind returns a profile's capability kind by alias from the cache with ZERO
// I/O: it never touches the ConnectionStore. ok=false means the alias is
// unknown. This is the cheap lookup workflow validation uses for capability
// checks — distinct from Resolve, which runs the full connection-backed chain.
// CORRECTNESS: using Kind (not Resolve) for ProfileKind means a valid profile
// whose connection is absent is NOT mislabeled UNKNOWN_PROFILE; the missing
// connection falls through to the connection-existence check instead.
func (r *DynamicProfileResolver) Kind(alias string) (provider.ProfileKind, bool) {
	p, ok := r.cache.get(alias)
	if !ok {
		return "", false
	}
	return p.Kind, true
}

// Resolve runs the alias -> profile -> connection -> binding chain against the
// cache. An unknown alias returns ErrUnknownProfile (wrapped with the alias). An
// empty ConnectionRef yields the claude-cli no-key binding with no
// ConnectionStore call. Otherwise the connection metadata is looked up (a miss
// wraps ErrConnectionNotFound, naming the profile and connection), and a lazy
// RevealKey closure is attached that decrypts only when invoked. The profile's
// ContextWindow/Effort axes are copied verbatim onto the binding on both
// branches.
func (r *DynamicProfileResolver) Resolve(ctx context.Context, alias string) (provider.ResolvedBinding, error) {
	p, ok := r.cache.get(alias)
	if !ok {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, alias)
	}

	// No-key subscription path: empty ConnectionRef -> claude-cli, no store call.
	if p.ConnectionRef == "" {
		return provider.ResolvedBinding{
			Kind:          p.Kind,
			Provider:      claudeCLIProvider,
			BaseURL:       "",
			Extras:        nil,
			Model:         p.Model,
			Backend:       p.Backend,
			AllowedTools:  p.AllowedTools,
			ContextWindow: p.ContextWindow,
			Effort:        p.Effort,
			RevealKey: func(context.Context) (string, error) {
				return "", nil
			},
		}, nil
	}

	// Connection-backed path: look up metadata (no key material here). The
	// request-scoped ctx is threaded into the store Get so a cancelled run aborts
	// the lookup (matching the lazy RevealKey(ctx) closure below).
	if r.store == nil {
		return provider.ResolvedBinding{}, fmt.Errorf(
			"provider: profile %q references connection %q but no connection store is configured",
			alias, p.ConnectionRef)
	}

	conn, err := r.store.Get(ctx, p.ConnectionRef)
	if err != nil {
		return provider.ResolvedBinding{}, fmt.Errorf(
			"provider: profile %q references connection %q: %w", alias, p.ConnectionRef, err)
	}

	connRef := p.ConnectionRef
	store := r.store
	return provider.ResolvedBinding{
		Kind:          p.Kind,
		Provider:      conn.Provider,
		BaseURL:       conn.BaseURL,
		Extras:        conn.Extras,
		Model:         p.Model,
		Backend:       p.Backend,
		AllowedTools:  p.AllowedTools,
		ContextWindow: p.ContextWindow,
		Effort:        p.Effort,
		// RevealKey is lazy: it decrypts the connection's key only when invoked,
		// at call/launch time. SECURITY: the revealed key is returned to the
		// caller and never logged here.
		RevealKey: func(ctx context.Context) (string, error) {
			return store.Reveal(ctx, connRef)
		},
	}, nil
}
