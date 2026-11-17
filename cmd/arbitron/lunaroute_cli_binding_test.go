package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliKeyStore struct {
	provider.ConnectionStore
	name  string
	key   string
	calls int
}

func (s *cliKeyStore) Get(_ context.Context, name string) (provider.Connection, error) {
	if name != s.name {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return provider.Connection{Name: name, Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com"}, nil
}

func (s *cliKeyStore) Reveal(_ context.Context, name string) (string, error) {
	if name != s.name {
		return "", provider.ErrConnectionNotFound
	}
	s.calls++
	return s.key, nil
}

func TestBindingFromConnection_GuardsLunaRoute(t *testing.T) {
	c := provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com"}
	s := &cliKeyStore{name: "lr", key: "sk-foreign"}
	b := bindingFromConnection(s, "lr", c)
	assert.Zero(t, s.calls)
	key, err := b.RevealKey(context.Background())
	require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
	assert.Empty(t, key)

	s.key = "lr_ok"
	b = bindingFromConnection(s, "lr", c)
	key, err = b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lr_ok", key)
	assert.False(t, b.BackendExplicit, "CLI probing does not select an agent backend")

	c.BaseURL = "https://evil.example/v1"
	b = bindingFromConnection(s, "lr", c)
	key, err = b.RevealKey(context.Background())
	require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
	assert.Empty(t, key)
	assert.Equal(t, 2, s.calls, "invalid endpoint refuses before store reveal")
}
