package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Interleave a real committed write with the metadata/decryption reads without
// scheduler timing. The embedded store still performs encryption and SQL reads.
type discoveryChangingStore struct {
	provider.ConnectionStore
	beforeReveal, afterReveal func()
}

func (s discoveryChangingStore) Reveal(ctx context.Context, name string) (string, error) {
	if s.beforeReveal != nil {
		s.beforeReveal()
	}
	key, err := s.ConnectionStore.Reveal(ctx, name)
	if s.afterReveal != nil {
		s.afterReveal()
	}
	return key, err
}

type discoveryCountingTransport struct{ calls int }

func (g *discoveryCountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	g.calls++
	// Any outbound request is a security failure in these interleavings.
	return &http.Response{StatusCode: 200, Header: http.Header{}, Request: r,
		Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"glm-5.3"}]}`))}, nil
}

func TestLunaRouteDiscovery_ConcurrentConnectionChange(t *testing.T) {
	for _, surface := range []string{"discovery", "cli"} {
		for _, change := range []string{"switch to lunaroute", "switch away", "rotate key", "recreate", "change after decrypt", "delete after decrypt"} {
			t.Run(surface+"/"+change, func(t *testing.T) {
				g := &discoveryCountingTransport{}
				ds := bootLunaRouteRoot(t, "", g, func(string) {})
				ctx := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
				conn := provider.Connection{Name: "race", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com"}
				key := "lr_original_discovery"
				if change == "switch to lunaroute" {
					conn.Provider, conn.BaseURL, key = "openrouter", "https://openrouter.ai/api/v1", "sk-or-original"
				}
				require.NoError(t, ds.connStore.Create(ctx, conn, key))
				mutate := func() {
					next := conn
					nextKey := "lr_replacement_discovery"
					switch change {
					case "switch to lunaroute":
						next.Provider, next.BaseURL = "lunaroute", "https://gw.lunaroute.com"
					case "switch away":
						next.Provider, next.BaseURL, nextKey = "openrouter", "https://openrouter.ai/api/v1", "sk-or-replacement"
					case "recreate", "delete after decrypt":
						require.NoError(t, ds.connStore.Delete(ctx, conn.Name))
						if change == "delete after decrypt" {
							return
						}
						require.NoError(t, ds.connStore.Create(ctx, next, nextKey))
						return
					}
					require.NoError(t, ds.connStore.Update(ctx, next, &nextKey))
				}
				store := discoveryChangingStore{ConnectionStore: ds.connStore, beforeReveal: mutate}
				if change == "change after decrypt" || change == "delete after decrypt" {
					store.beforeReveal, store.afterReveal = nil, mutate
				}
				sel := adaptprobe.NewProbeFactory(adaptprobe.Deps{HTTPClient: &http.Client{Transport: g}})
				var err error
				if surface == "discovery" {
					var view provider.DiscoveryView
					view, err = provider.NewModelDiscoveryService(store, sel, ds.capabilityRules).Discover(ctx, conn.Name)
					assert.Equal(t, provider.DiscoveryView{}, view, "no partial catalog from stale routing")
				} else {
					var out bytes.Buffer
					err = runConnectionValidate(ctx, validateDeps{store: store, selector: sel}, conn.Name, &out)
					assert.Empty(t, out.String(), "no success output from stale routing")
				}
				if change == "delete after decrypt" {
					require.ErrorIs(t, err, provider.ErrConnectionNotFound)
				} else {
					require.ErrorIs(t, err, provider.ErrConnectionChangedConcurrently)
				}
				assert.Zero(t, g.calls, "a key must never reach a stale endpoint")
				for _, secret := range []string{"lr_original_discovery", "lr_replacement_discovery", "sk-or-original", "sk-or-replacement"} {
					assert.NotContains(t, err.Error(), secret)
				}
			})
		}
	}
}
