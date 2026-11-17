package main

import (
	"context"
	"net/http"
	"testing"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingTransport struct{ calls int }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls++
	return nil, http.ErrNotSupported
}

// The serve-path resolver captures a connection's route and reveals its key
// lazily. If the connection changes in between, the key stored by the later edit
// must not be sent to the captured route. Driven through the booted root: the
// real resolver, repository, sealed keys and provider factory.
func TestResolverRevealBoundToConnectionRevisionWiring(t *testing.T) {
	const foreignURL = "https://gateway.example.com/v1"
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, ds *domainStack, ctx context.Context)
	}{
		{"foreign switched to lunaroute with a fresh key", func(t *testing.T, ds *domainStack, ctx context.Context) {
			key := "lr_live_after_switch"
			require.NoError(t, ds.connSvc.Update(ctx, provider.Connection{Name: "c1", Provider: "lunaroute"}, &key))
		}},
		{"key rotated on the same route", func(t *testing.T, ds *domainStack, ctx context.Context) {
			key := "sk-rotated"
			require.NoError(t, ds.connSvc.Update(ctx, provider.Connection{Name: "c1", Provider: "openrouter", BaseURL: foreignURL}, &key))
		}},
		{"deleted and recreated as lunaroute", func(t *testing.T, ds *domainStack, ctx context.Context) {
			_, err := ds.db.ExecContext(ctx, `DELETE FROM connections WHERE name = 'c1'`)
			require.NoError(t, err)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "c1", Provider: "lunaroute"}, "lr_live_recreated"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := bootSinkRoot(t, "", func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "c1", Provider: "openrouter", BaseURL: foreignURL}, "sk-original"))
			require.NoError(t, adaptsqlite.NewProfileRepo(ds.db).Create(ctx, provider.Profile{Alias: "p1", Kind: provider.ProfileKindChat, ConnectionRef: "c1", Model: "some-model"}))
			resolver, ok := ds.profileResolver.(*providersadapter.DynamicProfileResolver)
			require.True(t, ok, "root wires the dynamic resolver")
			require.NoError(t, resolver.Reload(ctx))

			binding, err := ds.profileResolver.Resolve(ctx, "p1")
			require.NoError(t, err)
			assert.Equal(t, foreignURL, binding.BaseURL)

			// Unchanged connection: the captured route still gets its own key.
			key, err := binding.RevealKey(ctx)
			require.NoError(t, err)
			assert.Equal(t, "sk-original", key)

			tc.change(t, ds, ctx)

			key, err = binding.RevealKey(ctx)
			require.ErrorIs(t, err, provider.ErrConnectionChangedConcurrently)
			assert.Empty(t, key)

			// The same binding through a real chat provider sends nothing.
			gw := &countingTransport{}
			factory := providersadapter.NewFactory(providersadapter.Deps{HTTPClient: &http.Client{Transport: gw}})
			chat, err := factory.ChatProviderFor(binding)
			require.NoError(t, err)
			_, err = chat.Chat(ctx, engine.ChatRequest{Prompt: "ping"})
			require.ErrorIs(t, err, provider.ErrConnectionChangedConcurrently)
			assert.Zero(t, gw.calls, "no outbound request carries the new key")

			// A fresh resolution follows the new route and key.
			fresh, err := ds.profileResolver.Resolve(ctx, "p1")
			require.NoError(t, err)
			key, err = fresh.RevealKey(ctx)
			if fresh.Provider == "lunaroute" {
				// The profile is a chat profile on a LunaRoute route: a fresh
				// binding reveals its own key.
				require.NoError(t, err)
				assert.Contains(t, key, "lr_live_")
			} else {
				require.NoError(t, err)
				assert.Equal(t, "sk-rotated", key)
			}
		})
	}
}
