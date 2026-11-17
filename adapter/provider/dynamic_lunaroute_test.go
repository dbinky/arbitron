package provider

import (
	"context"
	"testing"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lrConnStore struct {
	conn  domprovider.Connection
	key   string
	calls int
}

func (s *lrConnStore) Create(context.Context, domprovider.Connection, string) error  { return nil }
func (s *lrConnStore) Update(context.Context, domprovider.Connection, *string) error { return nil }
func (s *lrConnStore) Delete(context.Context, string) error                          { return nil }
func (s *lrConnStore) Get(_ context.Context, name string) (domprovider.Connection, error) {
	if name != s.conn.Name {
		return domprovider.Connection{}, domprovider.ErrConnectionNotFound
	}
	return s.conn, nil
}
func (s *lrConnStore) List(context.Context) ([]domprovider.Connection, error) {
	return []domprovider.Connection{s.conn}, nil
}
func (s *lrConnStore) Reveal(_ context.Context, name string) (string, error) {
	if name != s.conn.Name {
		return "", domprovider.ErrConnectionNotFound
	}
	s.calls++
	return s.key, nil
}

func TestRouteBinding_GuardsLunaRouteReveal(t *testing.T) {
	store := &lrConnStore{conn: domprovider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com"}, key: "sk-or-v1-foreign"}
	p := domprovider.Profile{Alias: "a", Kind: domprovider.ProfileKindAgent, ConnectionRef: "lr", Model: "glm-5.3"}
	b := routeBinding(p, workflow.BackendOpencode, store.conn, store)
	assert.Zero(t, store.calls, "construction stays lazy")
	key, err := b.RevealKey(context.Background())
	require.ErrorIs(t, err, domprovider.ErrLunaRouteStoredCredential)
	assert.Empty(t, key)

	store.key = "lr_live_ok"
	key, err = b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lr_live_ok", key)

	// Keyed LunaRoute codex reveals lazily through the same credential guard.
	cb := routeBinding(p, workflow.BackendCodex, store.conn, store)
	assert.Equal(t, 2, store.calls)
	key, err = cb.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lr_live_ok", key)
	assert.Equal(t, 3, store.calls)
	store.key = "sk-or-v1-foreign"
	key, err = cb.RevealKey(context.Background())
	require.ErrorIs(t, err, domprovider.ErrLunaRouteStoredCredential)
	assert.Empty(t, key)
	assert.Equal(t, 4, store.calls)

	or := domprovider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}
	orStore := &lrConnStore{conn: or, key: "sk-or-v1-x"}
	ob := routeBinding(domprovider.Profile{Kind: domprovider.ProfileKindChat, ConnectionRef: "or"}, "", or, orStore)
	key, err = ob.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-or-v1-x", key)
}
