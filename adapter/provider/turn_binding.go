package provider

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

type turnBindingRestorer struct{ store provider.ConnectionStore }

// NewTurnBindingRestorer restores the original connection, never a current profile alias.
func NewTurnBindingRestorer(store provider.ConnectionStore) engine.TurnBindingRestorer {
	return &turnBindingRestorer{store: store}
}

func (r *turnBindingRestorer) RestoreTurnBinding(ctx context.Context, a engine.AcceptedTurnBinding) (provider.ResolvedBinding, error) {
	if a.Kind == "" || a.Provider == "" || a.Model == "" || a.RouteConfigHash == "" {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: empty accepted snapshot", engine.ErrTurnBindingChanged)
	}
	b := provider.ResolvedBinding{
		Kind: a.Kind, Provider: a.Provider, Model: a.Model, Backend: a.Backend, BackendExplicit: a.BackendExplicit,
		AllowedTools: append([]string(nil), a.AllowedTools...), ContextWindow: a.ContextWindow, Effort: a.Effort,
		Pinned: a.Pinned, OpaquePinRoute: a.OpaquePinRoute, ConnectionRef: a.ConnectionRef,
	}
	if a.ConnectionRef == "" {
		if engine.RouteConfigHash(a.Provider, "", nil) != a.RouteConfigHash {
			return provider.ResolvedBinding{}, fmt.Errorf("%w: subscription route changed", engine.ErrTurnBindingChanged)
		}
		b.RevealKey = func(context.Context) (string, error) { return "", nil }
		return b, nil
	}
	if r.store == nil {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: connection %q but no connection store", engine.ErrTurnBindingChanged, a.ConnectionRef)
	}
	conn, err := r.store.Get(ctx, a.ConnectionRef)
	if err != nil {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: connection %q: %w", engine.ErrTurnBindingChanged, a.ConnectionRef, err)
	}
	if provider.Canonicalize(conn.Provider) != provider.Canonicalize(a.Provider) || engine.RouteConfigHash(conn.Provider, conn.BaseURL, conn.Extras) != a.RouteConfigHash {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: connection %q route changed since acceptance", engine.ErrTurnBindingChanged, a.ConnectionRef)
	}
	b.Provider, b.BaseURL, b.Extras = conn.Provider, conn.BaseURL, conn.Extras
	b.RevealKey = provider.RevealBoundToRevision(r.store, a.ConnectionRef, conn)
	return provider.GuardLunaRouteReveal(b), nil
}
