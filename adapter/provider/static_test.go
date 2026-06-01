package provider_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConnectionStore is an in-memory provider.ConnectionStore for resolver
// tests. It records Get/Reveal call counts so laziness is observable, and lets
// a test force Reveal to fail with ErrDecrypt.
type fakeConnectionStore struct {
	conns       map[string]provider.Connection
	keys        map[string]string
	getCalls    int
	revealCalls int
	revealErr   error
}

func newFakeConnectionStore() *fakeConnectionStore {
	return &fakeConnectionStore{
		conns: make(map[string]provider.Connection),
		keys:  make(map[string]string),
	}
}

func (f *fakeConnectionStore) put(c provider.Connection, key string) {
	f.conns[c.Name] = c
	f.keys[c.Name] = key
}

func (f *fakeConnectionStore) Create(context.Context, provider.Connection, string) error {
	return errors.New("not implemented")
}
func (f *fakeConnectionStore) Update(context.Context, provider.Connection, *string) error {
	return errors.New("not implemented")
}
func (f *fakeConnectionStore) Delete(context.Context, string) error {
	return errors.New("not implemented")
}
func (f *fakeConnectionStore) List(context.Context) ([]provider.Connection, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeConnectionStore) Get(_ context.Context, name string) (provider.Connection, error) {
	f.getCalls++
	c, ok := f.conns[name]
	if !ok {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return c, nil
}

func (f *fakeConnectionStore) Reveal(_ context.Context, name string) (string, error) {
	f.revealCalls++
	if f.revealErr != nil {
		return "", f.revealErr
	}
	k, ok := f.keys[name]
	if !ok {
		return "", provider.ErrConnectionNotFound
	}
	return k, nil
}

// Happy path: alias -> profile (with a ConnectionRef) -> connection -> binding
// carries the connection's Provider/BaseURL/Extras and the profile's
// Model/Params/Kind.
func TestStaticProfileResolver_ResolveWithConnection(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{
		Name:     "azure-prod",
		Provider: "azure-openai",
		BaseURL:  "https://prod.azure.com",
		Extras:   map[string]string{"api-version": "2024-02-01"},
	}, "sk-secret")

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{
			Alias:         "fast-chat",
			Kind:          provider.ProfileKindChat,
			ConnectionRef: "azure-prod",
			Model:         "gpt-4o",
			Params:        map[string]any{"temperature": 0.3},
		},
	}, store)
	require.NoError(t, err)

	b, err := r.Resolve("fast-chat")
	require.NoError(t, err)

	assert.Equal(t, provider.ProfileKindChat, b.Kind)
	assert.Equal(t, "azure-openai", b.Provider)
	assert.Equal(t, "https://prod.azure.com", b.BaseURL)
	assert.Equal(t, map[string]string{"api-version": "2024-02-01"}, b.Extras)
	assert.Equal(t, "gpt-4o", b.Model)
	assert.Equal(t, map[string]any{"temperature": 0.3}, b.Params)

	// RevealKey decrypts when invoked.
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", key)
}

// Success: an empty ConnectionRef resolves to the claude-cli no-key binding —
// Provider == "claude-cli", empty BaseURL, RevealKey returns ("", nil) WITHOUT
// touching the store (no Get/Reveal calls).
func TestStaticProfileResolver_EmptyConnectionRefNoKey(t *testing.T) {
	store := newFakeConnectionStore()

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "claude-opus"},
	}, store)
	require.NoError(t, err)

	b, err := r.Resolve("default-agent")
	require.NoError(t, err)

	assert.Equal(t, provider.ProfileKindAgent, b.Kind)
	assert.Equal(t, "claude-cli", b.Provider)
	assert.Equal(t, "", b.BaseURL)
	assert.Nil(t, b.Extras)
	assert.Equal(t, "claude-opus", b.Model)

	require.NotNil(t, b.RevealKey)
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "", key)

	assert.Equal(t, 0, store.getCalls, "no-key path must not call Get")
	assert.Equal(t, 0, store.revealCalls, "no-key path must not call Reveal")
}

// Success: the no-key path resolves with NO store at all (the Claude
// subscription path stores zero connections / a nil store).
func TestStaticProfileResolver_EmptyConnectionRefNilStore(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "claude-sonnet"},
	}, nil)
	require.NoError(t, err)

	b, err := r.Resolve("default-chat")
	require.NoError(t, err)
	assert.Equal(t, "claude-cli", b.Provider)
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "", key)
}

// Failure: a profile whose ConnectionRef names a connection absent from the
// store fails Resolve with an error wrapping ErrConnectionNotFound, naming both
// the profile alias and the missing connection.
func TestStaticProfileResolver_UnknownConnection(t *testing.T) {
	store := newFakeConnectionStore()

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "ghost", Model: "m"},
	}, store)
	require.NoError(t, err)

	_, err = r.Resolve("fast-chat")
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
	assert.Contains(t, err.Error(), "fast-chat")
	assert.Contains(t, err.Error(), "ghost")
}

// Error: an unknown alias returns an error matchable as ErrUnknownProfile,
// and the wrapped message carries the offending alias for context.
func TestStaticProfileResolver_ResolveUnknown(t *testing.T) {
	store := newFakeConnectionStore()
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"},
	}, store)
	require.NoError(t, err)

	_, err = r.Resolve("nope")
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
	assert.Contains(t, err.Error(), "nope")
}

// Error: a connected profile's RevealKey propagates a ConnectionStore.Reveal
// decrypt error when invoked.
func TestStaticProfileResolver_RevealKeyPropagatesDecryptError(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "u"}, "sk")
	store.revealErr = provider.ErrDecrypt

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}, store)
	require.NoError(t, err)

	b, err := r.Resolve("fast-chat")
	require.NoError(t, err)

	_, err = b.RevealKey(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrDecrypt)
}

// Edge: lazy RevealKey — building the binding performs zero Reveal calls;
// calling RevealKey N times performs exactly N Reveal calls (no caching).
func TestStaticProfileResolver_RevealKeyIsLazy(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "u"}, "sk-secret")

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}, store)
	require.NoError(t, err)

	b, err := r.Resolve("fast-chat")
	require.NoError(t, err)
	assert.Equal(t, 0, store.revealCalls, "building the binding must not decrypt")

	for i := 1; i <= 3; i++ {
		_, err := b.RevealKey(context.Background())
		require.NoError(t, err)
		assert.Equal(t, i, store.revealCalls)
	}
}

// Edge: many-to-one topology — two profiles sharing one ConnectionRef both
// resolve to the same BaseURL/key.
func TestStaticProfileResolver_ManyToOneSharedConnection(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "shared", Provider: "openrouter", BaseURL: "https://shared"}, "sk-shared")

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "p1", Kind: provider.ProfileKindChat, ConnectionRef: "shared", Model: "m1"},
		{Alias: "p2", Kind: provider.ProfileKindAgent, ConnectionRef: "shared", Model: "m2"},
	}, store)
	require.NoError(t, err)

	b1, err := r.Resolve("p1")
	require.NoError(t, err)
	b2, err := r.Resolve("p2")
	require.NoError(t, err)

	assert.Equal(t, "https://shared", b1.BaseURL)
	assert.Equal(t, b1.BaseURL, b2.BaseURL)

	k1, err := b1.RevealKey(context.Background())
	require.NoError(t, err)
	k2, err := b2.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-shared", k1)
	assert.Equal(t, k1, k2)
}

// Edge: one-line backend swap — repointing a profile's ConnectionRef to a
// different connection changes the resolved BaseURL with no graph edit.
func TestStaticProfileResolver_OneLineBackendSwap(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "old", Provider: "openrouter", BaseURL: "https://old"}, "k-old")
	store.put(provider.Connection{Name: "new", Provider: "openrouter", BaseURL: "https://new"}, "k-new")

	before, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "p", Kind: provider.ProfileKindChat, ConnectionRef: "old", Model: "m"},
	}, store)
	require.NoError(t, err)
	b, err := before.Resolve("p")
	require.NoError(t, err)
	assert.Equal(t, "https://old", b.BaseURL)

	after, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "p", Kind: provider.ProfileKindChat, ConnectionRef: "new", Model: "m"},
	}, store)
	require.NoError(t, err)
	b2, err := after.Resolve("p")
	require.NoError(t, err)
	assert.Equal(t, "https://new", b2.BaseURL)
}

// Secrecy: calling RevealKey under a buffer-backed slog.Logger captures no
// substring of the revealed key — the resolver references the connection name
// only and never logs key material.
func TestStaticProfileResolver_RevealKeyNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	const secret = "sk-super-secret-value"
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "u"}, secret)

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}, store)
	require.NoError(t, err)

	b, err := r.Resolve("fast-chat")
	require.NoError(t, err)
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, secret, key)

	assert.NotContains(t, buf.String(), secret)
}

// Error: a duplicate alias is a construction-time failure naming the alias.
func TestNewStaticProfileResolver_DuplicateAlias(t *testing.T) {
	_, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "dup", Kind: provider.ProfileKindChat, Model: "a"},
		{Alias: "dup", Kind: provider.ProfileKindAgent, Model: "b"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dup")
}

// Error: an unknown Kind value is rejected at construction.
func TestNewStaticProfileResolver_UnknownKind(t *testing.T) {
	_, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "weird", Kind: provider.ProfileKind("tool"), Model: "a"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool")
}

// Edge: an empty/nil registry constructs successfully and every Resolve misses
// with ErrUnknownProfile.
func TestNewStaticProfileResolver_EmptyRegistry(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver(nil, nil)
	require.NoError(t, err)

	_, err = r.Resolve("anything")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// Edge: alias matching is exact / case-sensitive.
func TestStaticProfileResolver_CaseSensitive(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m"},
	}, nil)
	require.NoError(t, err)

	_, err = r.Resolve("Default-Chat")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// The resolver satisfies the domain ProfileResolver port.
func TestStaticProfileResolver_ImplementsPort(t *testing.T) {
	var _ provider.ProfileResolver = (*adapterprovider.StaticProfileResolver)(nil)
	// Sanity: a constructed resolver is usable through the port.
	r, err := adapterprovider.NewStaticProfileResolver(nil, nil)
	require.NoError(t, err)
	var port provider.ProfileResolver = r
	_, err = port.Resolve("x")
	assert.True(t, errors.Is(err, provider.ErrUnknownProfile))
}
