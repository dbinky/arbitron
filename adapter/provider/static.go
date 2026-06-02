// Package provider implements adapters for the domain provider ports. It hosts
// the StaticProfileResolver: an immutable in-memory profile registry built once
// at startup from config.
package provider

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
)

// claudeCLIProvider is the Provider value the no-connection subscription path
// resolves to. A profile with an empty ConnectionRef runs against the local
// Claude subscription (claude-cli) and needs no endpoint or key.
const claudeCLIProvider = "claude-cli"

// StaticProfileResolver resolves aliases against an immutable in-memory registry
// loaded once at startup from config, then runs the full
// alias -> profile -> connection -> binding chain. A profile with an empty
// ConnectionRef resolves to the claude-cli no-key binding and never touches the
// ConnectionStore; a profile with a ConnectionRef looks up the connection
// metadata for Provider/BaseURL/Extras and produces a lazy RevealKey closure.
type StaticProfileResolver struct {
	byAlias map[string]provider.Profile
	store   provider.ConnectionStore
}

var _ provider.ProfileResolver = (*StaticProfileResolver)(nil)

// NewStaticProfileResolver builds the alias->Profile map and captures the
// ConnectionStore used to resolve connection-backed profiles. It fails fast at
// construction (startup) rather than at resolve time on a duplicate alias or an
// unknown kind value. The store may be nil when every profile uses the
// claude-cli no-key subscription path (which stores zero connections).
func NewStaticProfileResolver(profiles []provider.Profile, store provider.ConnectionStore) (*StaticProfileResolver, error) {
	byAlias := make(map[string]provider.Profile, len(profiles))
	for _, p := range profiles {
		if p.Alias == "" {
			return nil, fmt.Errorf("provider: profile has empty alias (set a unique non-empty alias for each profile)")
		}
		if _, dup := byAlias[p.Alias]; dup {
			return nil, fmt.Errorf("provider: duplicate profile alias %q", p.Alias)
		}
		switch p.Kind {
		case provider.ProfileKindChat, provider.ProfileKindAgent:
			// ok
		default:
			return nil, fmt.Errorf("provider: profile %q has unknown kind %q (want chat or agent)", p.Alias, p.Kind)
		}
		byAlias[p.Alias] = p
	}
	return &StaticProfileResolver{byAlias: byAlias, store: store}, nil
}

// Kind returns a profile's capability kind by alias from the in-memory registry
// with ZERO I/O: it never touches the ConnectionStore. ok=false means the alias
// is unknown. This is the cheap lookup workflow validation uses for capability
// checks — distinct from Resolve, which runs the full connection-backed chain.
// SECURITY/CORRECTNESS: using Kind (not Resolve) for ProfileKind means a valid
// profile whose connection is absent is NOT mislabeled UNKNOWN_PROFILE; the
// missing connection falls through to the connection-existence check instead.
func (r *StaticProfileResolver) Kind(alias string) (provider.ProfileKind, bool) {
	p, ok := r.byAlias[alias]
	if !ok {
		return "", false
	}
	return p.Kind, true
}

// Resolve runs the alias -> profile -> connection -> binding chain. An unknown
// alias returns ErrUnknownProfile (wrapped with the alias). An empty
// ConnectionRef yields the claude-cli no-key binding with no ConnectionStore
// call. Otherwise the connection metadata is looked up (a miss wraps
// ErrConnectionNotFound, naming the profile and connection), and a lazy
// RevealKey closure is attached that decrypts only when invoked.
func (r *StaticProfileResolver) Resolve(ctx context.Context, alias string) (provider.ResolvedBinding, error) {
	p, ok := r.byAlias[alias]
	if !ok {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, alias)
	}

	// No-key subscription path: empty ConnectionRef -> claude-cli, no store call.
	if p.ConnectionRef == "" {
		return provider.ResolvedBinding{
			Kind:     p.Kind,
			Provider: claudeCLIProvider,
			BaseURL:  "",
			Extras:   nil,
			Model:    p.Model,
			Params:   p.Params,
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
		Kind:     p.Kind,
		Provider: conn.Provider,
		BaseURL:  conn.BaseURL,
		Extras:   conn.Extras,
		Model:    p.Model,
		Params:   p.Params,
		// RevealKey is lazy: it decrypts the connection's key only when invoked,
		// at call/launch time. SECURITY: the revealed key is returned to the
		// caller and never logged here.
		RevealKey: func(ctx context.Context) (string, error) {
			return store.Reveal(ctx, connRef)
		},
	}, nil
}
