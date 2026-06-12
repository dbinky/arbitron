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

// ---------------------------------------------------------------------------
// fakeConnectionStore — in-memory provider.ConnectionStore for resolver tests.
// It records Get/Reveal call counts so laziness is observable, and lets a test
// force Reveal to fail. Shared by the dynamic-resolver and backend-slice tests.
// ---------------------------------------------------------------------------

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

func (f *fakeConnectionStore) Get(ctx context.Context, name string) (provider.Connection, error) {
	f.getCalls++
	// Honour cancellation so the resolver's ctx-threading is observable: a
	// cancelled request-scoped ctx aborts the connection lookup.
	if err := ctx.Err(); err != nil {
		return provider.Connection{}, err
	}
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

// ---------------------------------------------------------------------------
// fakeProfileStore — in-memory provider.ProfileStore backing the ProfileCache.
// ---------------------------------------------------------------------------

type fakeProfileStore struct {
	byAlias map[string]provider.Profile
}

func newFakeProfileStore(profiles ...provider.Profile) *fakeProfileStore {
	s := &fakeProfileStore{byAlias: map[string]provider.Profile{}}
	for _, p := range profiles {
		s.byAlias[p.Alias] = p
	}
	return s
}

// put inserts/overwrites a profile (test-only mutation; the cache only refreshes
// on Reload, so a put is invisible to a resolver until the next Reload).
func (s *fakeProfileStore) put(p provider.Profile) { s.byAlias[p.Alias] = p }

func (s *fakeProfileStore) Create(_ context.Context, p provider.Profile) error {
	if _, ok := s.byAlias[p.Alias]; ok {
		return provider.ErrProfileExists
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *fakeProfileStore) Update(_ context.Context, p provider.Profile) error {
	if _, ok := s.byAlias[p.Alias]; !ok {
		return provider.ErrProfileNotFound
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *fakeProfileStore) Delete(_ context.Context, alias string) error {
	if _, ok := s.byAlias[alias]; !ok {
		return provider.ErrProfileNotFound
	}
	delete(s.byAlias, alias)
	return nil
}
func (s *fakeProfileStore) Get(_ context.Context, alias string) (provider.Profile, error) {
	p, ok := s.byAlias[alias]
	if !ok {
		return provider.Profile{}, provider.ErrProfileNotFound
	}
	return p, nil
}
func (s *fakeProfileStore) List(_ context.Context) ([]provider.Profile, error) {
	out := make([]provider.Profile, 0, len(s.byAlias))
	for _, p := range s.byAlias {
		out = append(out, p)
	}
	return out, nil
}
func (s *fakeProfileStore) Count(_ context.Context) (int, error) { return len(s.byAlias), nil }

var _ provider.ProfileStore = (*fakeProfileStore)(nil)

// newDynamicResolver builds a DynamicProfileResolver over a cache loaded from the
// given profiles, wired to the given connection store. It is the test analogue
// of the old NewStaticProfileResolver(profiles, store) and is used by the
// ported resolver tests + the backend-slice integration tests.
func newDynamicResolver(t *testing.T, profiles []provider.Profile, store provider.ConnectionStore) *adapterprovider.DynamicProfileResolver {
	t.Helper()
	ps := newFakeProfileStore(profiles...)
	cache := adapterprovider.NewProfileCache(ps)
	require.NoError(t, cache.Load(context.Background()))
	return adapterprovider.NewDynamicProfileResolver(cache, store)
}

// ---------------------------------------------------------------------------
// New axes — the headline Phase-2 behaviour.
// ---------------------------------------------------------------------------

// Happy: Resolve copies the profile's ContextWindow/Effort onto the binding on
// the no-key subscription path.
func TestDynamicResolver_ResolveCarriesAxes_NoConnection(t *testing.T) {
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code",
			Model: "opus-4.8", ContextWindow: provider.Context1M, Effort: provider.EffortHigh},
	}, nil)

	b, err := r.Resolve(context.Background(), "coder")
	require.NoError(t, err)
	assert.Equal(t, provider.Context1M, b.ContextWindow)
	assert.Equal(t, provider.EffortHigh, b.Effort)
}

// Success: the axes are copied on the connection-backed path too (the two
// Resolve branches are structurally separate, so each is verified independently).
func TestDynamicResolver_ResolveCarriesAxes_WithConnection(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "lite", Provider: "litellm", BaseURL: "https://lite:4000"}, "sk")
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, ConnectionRef: "lite",
			Model: "m", ContextWindow: provider.Context200K, Effort: provider.EffortLow},
	}, store)

	b, err := r.Resolve(context.Background(), "coder")
	require.NoError(t, err)
	assert.Equal(t, "litellm", b.Provider)
	assert.Equal(t, provider.Context200K, b.ContextWindow)
	assert.Equal(t, provider.EffortLow, b.Effort)
}

// Edge: an unset (empty) axis stays empty on the binding — the default sentinel
// that the Phase-3 emitter treats as a no-op.
func TestDynamicResolver_DefaultAxesStayEmpty(t *testing.T) {
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "plain", Kind: provider.ProfileKindChat},
	}, nil)
	b, err := r.Resolve(context.Background(), "plain")
	require.NoError(t, err)
	assert.Equal(t, provider.ContextWindow(""), b.ContextWindow)
	assert.Equal(t, provider.Effort(""), b.Effort)
}

// ---------------------------------------------------------------------------
// Ported StaticProfileResolver coverage (now cache-backed).
// ---------------------------------------------------------------------------

// Happy: alias -> profile (with a ConnectionRef) -> connection -> binding carries
// the connection's Provider/BaseURL/Extras and the profile's Model/Kind.
func TestDynamicResolver_ResolveWithConnection(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{
		Name:     "azure-prod",
		Provider: "azure-openai",
		BaseURL:  "https://prod.azure.com",
		Extras:   map[string]string{"api-version": "2024-02-01"},
	}, "sk-secret")

	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "gpt-4o"},
	}, store)

	b, err := r.Resolve(context.Background(), "fast-chat")
	require.NoError(t, err)
	assert.Equal(t, provider.ProfileKindChat, b.Kind)
	assert.Equal(t, "azure-openai", b.Provider)
	assert.Equal(t, "https://prod.azure.com", b.BaseURL)
	assert.Equal(t, map[string]string{"api-version": "2024-02-01"}, b.Extras)
	assert.Equal(t, "gpt-4o", b.Model)

	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", key)
}

// Cancellation: a cancelled request-scoped ctx aborts the connection-backed
// lookup.
func TestDynamicResolver_Resolve_CancelledCtxAbortsLookup(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "https://prod.azure.com"}, "sk")
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "gpt-4o"},
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Resolve(ctx, "fast-chat")
	require.ErrorIs(t, err, context.Canceled, "a cancelled ctx must abort the connection lookup")
}

// Zero-I/O Kind: Kind returns a profile's capability kind from the cache without
// touching the ConnectionStore, and reports ok=false for an unknown alias.
func TestDynamicResolver_Kind_ZeroIO(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "https://prod.azure.com"}, "k")
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "gpt-4o"},
	}, store)

	kind, ok := r.Kind("fast-chat")
	require.True(t, ok)
	assert.Equal(t, provider.ProfileKindChat, kind)
	assert.Equal(t, 0, store.getCalls, "Kind must not touch the ConnectionStore")

	_, ok = r.Kind("ghost")
	assert.False(t, ok, "an unknown alias reports ok=false")
}

// Success: an empty ConnectionRef resolves to the claude-cli no-key binding
// without touching the store.
func TestDynamicResolver_EmptyConnectionRefNoKey(t *testing.T) {
	store := newFakeConnectionStore()
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "claude-opus"},
	}, store)

	b, err := r.Resolve(context.Background(), "default-agent")
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

// Success: the no-key path resolves with NO store at all (a nil ConnectionStore).
func TestDynamicResolver_EmptyConnectionRefNilStore(t *testing.T) {
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "claude-sonnet"},
	}, nil)

	b, err := r.Resolve(context.Background(), "default-chat")
	require.NoError(t, err)
	assert.Equal(t, "claude-cli", b.Provider)
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "", key)
}

// Failure: a profile whose ConnectionRef names a connection absent from the
// store fails Resolve with an error wrapping ErrConnectionNotFound.
func TestDynamicResolver_UnknownConnection(t *testing.T) {
	store := newFakeConnectionStore()
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "ghost", Model: "m"},
	}, store)

	_, err := r.Resolve(context.Background(), "fast-chat")
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
	assert.Contains(t, err.Error(), "fast-chat")
	assert.Contains(t, err.Error(), "ghost")
}

// Error: an unknown alias returns an error matchable as ErrUnknownProfile.
func TestDynamicResolver_ResolveUnknown(t *testing.T) {
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"},
	}, newFakeConnectionStore())

	_, err := r.Resolve(context.Background(), "nope")
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
	assert.Contains(t, err.Error(), "nope")
}

// Error: a connected profile's RevealKey propagates a Reveal decrypt error.
func TestDynamicResolver_RevealKeyPropagatesDecryptError(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "u"}, "sk")
	store.revealErr = provider.ErrDecrypt
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}, store)

	b, err := r.Resolve(context.Background(), "fast-chat")
	require.NoError(t, err)
	_, err = b.RevealKey(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrDecrypt)
}

// Edge: lazy RevealKey — building the binding performs zero Reveal calls;
// calling RevealKey N times performs exactly N Reveal calls (no caching).
func TestDynamicResolver_RevealKeyIsLazy(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "u"}, "sk-secret")
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}, store)

	b, err := r.Resolve(context.Background(), "fast-chat")
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
func TestDynamicResolver_ManyToOneSharedConnection(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "shared", Provider: "openrouter", BaseURL: "https://shared"}, "sk-shared")
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "p1", Kind: provider.ProfileKindChat, ConnectionRef: "shared", Model: "m1"},
		{Alias: "p2", Kind: provider.ProfileKindAgent, ConnectionRef: "shared", Model: "m2"},
	}, store)

	b1, err := r.Resolve(context.Background(), "p1")
	require.NoError(t, err)
	b2, err := r.Resolve(context.Background(), "p2")
	require.NoError(t, err)
	assert.Equal(t, "https://shared", b1.BaseURL)
	assert.Equal(t, b1.BaseURL, b2.BaseURL)
}

// Backend axis: Resolve copies Profile.Backend onto the binding on the
// connection-less path.
func TestDynamicResolver_BackendPropagated_NoConnection(t *testing.T) {
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, Model: "m", Backend: "opencode"},
	}, nil)
	b, err := r.Resolve(context.Background(), "coder")
	require.NoError(t, err)
	assert.Equal(t, "opencode", b.Backend, "Backend must survive the connection-less resolve path")
}

// Backend axis: Resolve copies Profile.Backend onto the binding on the
// connection-backed path too.
func TestDynamicResolver_BackendPropagated_WithConnection(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "litellm", Provider: "litellm", BaseURL: "https://proxy"}, "key")
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, ConnectionRef: "litellm", Model: "m", Backend: "claude-code"},
	}, store)
	b, err := r.Resolve(context.Background(), "coder")
	require.NoError(t, err)
	assert.Equal(t, "claude-code", b.Backend, "Backend must survive the connection-backed resolve path")
}

// Secrecy: calling RevealKey under a buffer-backed slog.Logger captures no
// substring of the revealed key.
func TestDynamicResolver_RevealKeyNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	const secret = "sk-super-secret-value"
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "azure-prod", Provider: "azure-openai", BaseURL: "u"}, secret)
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "fast-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}, store)

	b, err := r.Resolve(context.Background(), "fast-chat")
	require.NoError(t, err)
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, secret, key)
	assert.NotContains(t, buf.String(), secret)
}

// Edge: an empty registry resolves every alias as ErrUnknownProfile.
func TestDynamicResolver_EmptyRegistry(t *testing.T) {
	r := newDynamicResolver(t, nil, nil)
	_, err := r.Resolve(context.Background(), "anything")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// Edge: alias matching is exact / case-sensitive.
func TestDynamicResolver_CaseSensitive(t *testing.T) {
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m"},
	}, nil)
	_, err := r.Resolve(context.Background(), "Default-Chat")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// Edge: a connection-backed profile cannot resolve when the store is nil; the
// error names both the profile alias and the referenced connection.
func TestDynamicResolver_ConnBackedProfileNilStore_Errors(t *testing.T) {
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "cloud-chat", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}, nil)
	_, err := r.Resolve(context.Background(), "cloud-chat")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloud-chat")
	assert.Contains(t, err.Error(), "azure-prod")
}

// The resolver satisfies the domain ProfileResolver port.
func TestDynamicResolver_ImplementsPort(t *testing.T) {
	var _ provider.ProfileResolver = (*adapterprovider.DynamicProfileResolver)(nil)
	r := newDynamicResolver(t, nil, nil)
	var port provider.ProfileResolver = r
	_, err := port.Resolve(context.Background(), "x")
	assert.True(t, errors.Is(err, provider.ErrUnknownProfile))
}
