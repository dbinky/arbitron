package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tbConnStore is a minimal ConnectionStore: metadata by name, one key each.
type tbConnStore struct {
	conns   map[string]provider.Connection
	keys    map[string]string
	reveals int
	getErr  error
}

func (s *tbConnStore) Create(context.Context, provider.Connection, string) error  { return nil }
func (s *tbConnStore) Update(context.Context, provider.Connection, *string) error { return nil }
func (s *tbConnStore) Delete(context.Context, string) error                       { return nil }
func (s *tbConnStore) List(context.Context) ([]provider.Connection, error)        { return nil, nil }
func (s *tbConnStore) Get(_ context.Context, name string) (provider.Connection, error) {
	if s.getErr != nil {
		return provider.Connection{}, s.getErr
	}
	c, ok := s.conns[name]
	if !ok {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return c, nil
}
func (s *tbConnStore) Reveal(_ context.Context, name string) (string, error) {
	s.reveals++
	k, ok := s.keys[name]
	if !ok {
		return "", provider.ErrConnectionNotFound
	}
	return k, nil
}

func tbStore() *tbConnStore {
	return &tbConnStore{
		conns: map[string]provider.Connection{"azure-east": {ConfigRevision: 4, Name: "azure-east", Provider: "azure-openai",
			BaseURL: "https://east.example.com/openai", Extras: map[string]string{"api-version": "2025-01-01"}}},
		keys: map[string]string{"azure-east": "sk-original"},
	}
}

func tbAccepted(t *testing.T, s *tbConnStore) engine.AcceptedTurnBinding {
	t.Helper()
	c := s.conns["azure-east"]
	a, err := engine.CaptureAcceptedTurnBinding(provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: c.Provider,
		BaseURL: c.BaseURL, Extras: c.Extras, Model: "gpt-5", Backend: "codex", BackendExplicit: true,
		AllowedTools: []string{"Read"}, ContextWindow: provider.Context200K, Effort: provider.EffortHigh, ConnectionRef: "azure-east"})
	require.NoError(t, err)
	return a
}

// Happy: the accepted model/backend/tools/axes come back with the original route and a lazy key.
func TestTurnBinding_RestoreReturnsAcceptedRouteWithLazyKey(t *testing.T) {
	s := tbStore()
	b, err := NewTurnBindingRestorer(s).RestoreTurnBinding(context.Background(), tbAccepted(t, s))
	require.NoError(t, err)
	assert.Equal(t, "gpt-5", b.Model)
	assert.Equal(t, "codex", b.Backend)
	assert.True(t, b.BackendExplicit)
	assert.Equal(t, []string{"Read"}, b.AllowedTools)
	assert.Equal(t, provider.Context200K, b.ContextWindow)
	assert.Equal(t, provider.EffortHigh, b.Effort)
	assert.Equal(t, "https://east.example.com/openai", b.BaseURL)
	assert.Equal(t, 0, s.reveals, "the key is revealed lazily, not at restore time")
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-original", key)
}

// Success: a key rotation that keeps the route is a fresh key for the same accepted request.
func TestTurnBinding_ReplayIgnoresChangedProfileButRefreshesOriginalKey(t *testing.T) {
	s := tbStore()
	accepted := tbAccepted(t, s)
	s.keys["azure-east"] = "sk-rotated"
	c := s.conns["azure-east"]
	c.ConfigRevision = 5 // key rotated after acceptance
	s.conns["azure-east"] = c
	b, err := NewTurnBindingRestorer(s).RestoreTurnBinding(context.Background(), accepted)
	require.NoError(t, err)
	assert.Equal(t, "gpt-5", b.Model, "the accepted model, never the profile's current model")
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-rotated", key)
}

// Failure: a changed route or removed connection never dispatches.
func TestTurnBinding_ChangedRouteOrRevokedConnectionNeverDispatches(t *testing.T) {
	s := tbStore()
	accepted := tbAccepted(t, s)
	moved := s.conns["azure-east"]
	moved.BaseURL = "https://west.example.com/openai"
	s.conns["azure-east"] = moved
	_, err := NewTurnBindingRestorer(s).RestoreTurnBinding(context.Background(), accepted)
	assert.ErrorIs(t, err, engine.ErrTurnBindingChanged)

	delete(s.conns, "azure-east")
	_, err = NewTurnBindingRestorer(s).RestoreTurnBinding(context.Background(), accepted)
	assert.ErrorIs(t, err, engine.ErrTurnBindingChanged)
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
	assert.Equal(t, 0, s.reveals)
}

// Error: store failures propagate; an empty snapshot is refused.
func TestTurnBinding_RestoreErrors(t *testing.T) {
	s := tbStore()
	accepted := tbAccepted(t, s)
	s.getErr = errors.New("db down")
	_, err := NewTurnBindingRestorer(s).RestoreTurnBinding(context.Background(), accepted)
	assert.ErrorContains(t, err, "db down")
	_, err = NewTurnBindingRestorer(tbStore()).RestoreTurnBinding(context.Background(), engine.AcceptedTurnBinding{})
	assert.ErrorIs(t, err, engine.ErrTurnBindingChanged)
}

// Edge: the no-connection subscription path restores without a store call.
func TestTurnBinding_SubscriptionPathNeedsNoStore(t *testing.T) {
	accepted, err := engine.CaptureAcceptedTurnBinding(provider.ResolvedBinding{Kind: provider.ProfileKindAgent,
		Provider: "claude-cli", Model: "claude-opus-4-5", Backend: "claude-code"})
	require.NoError(t, err)
	b, err := NewTurnBindingRestorer(nil).RestoreTurnBinding(context.Background(), accepted)
	require.NoError(t, err)
	assert.Equal(t, "claude-cli", b.Provider)
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Empty(t, key)

	tampered := accepted
	tampered.RouteConfigHash = "0000"
	_, err = NewTurnBindingRestorer(nil).RestoreTurnBinding(context.Background(), tampered)
	assert.ErrorIs(t, err, engine.ErrTurnBindingChanged)
}

func TestTurnBinding_ConnectionWithoutStoreFailsClosed(t *testing.T) {
	_, err := NewTurnBindingRestorer(nil).RestoreTurnBinding(context.Background(), tbAccepted(t, tbStore()))
	assert.ErrorIs(t, err, engine.ErrTurnBindingChanged)
}
