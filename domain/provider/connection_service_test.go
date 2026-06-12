package provider_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

// memConnStore is an in-memory provider.ConnectionStore for service tests. It
// records the last plaintext key handed to Create/Update so a test can confirm
// the service forwards it (and never returns it).
type memConnStore struct {
	mu    sync.Mutex
	conns map[string]provider.Connection
	keys  map[string]string
}

func newMemConnStore(seed ...provider.Connection) *memConnStore {
	s := &memConnStore{conns: map[string]provider.Connection{}, keys: map[string]string{}}
	for _, c := range seed {
		s.conns[c.Name] = c
	}
	return s
}

func (s *memConnStore) Create(_ context.Context, c provider.Connection, apiKey string) error {
	if err := provider.ValidateConnection(c); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[c.Name]; ok {
		return provider.ErrConnectionExists
	}
	s.conns[c.Name] = c
	s.keys[c.Name] = apiKey
	return nil
}
func (s *memConnStore) Update(_ context.Context, c provider.Connection, apiKey *string) error {
	if err := provider.ValidateConnection(c); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[c.Name]; !ok {
		return provider.ErrConnectionNotFound
	}
	s.conns[c.Name] = c
	if apiKey != nil {
		s.keys[c.Name] = *apiKey
	}
	return nil
}
func (s *memConnStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[name]; !ok {
		return provider.ErrConnectionNotFound
	}
	delete(s.conns, name)
	delete(s.keys, name)
	return nil
}
func (s *memConnStore) Get(_ context.Context, name string) (provider.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[name]
	if !ok {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return c, nil
}
func (s *memConnStore) List(_ context.Context) ([]provider.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Connection, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, c)
	}
	return out, nil
}
func (s *memConnStore) Reveal(_ context.Context, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[name]
	if !ok {
		return "", provider.ErrConnectionNotFound
	}
	return k, nil
}
func (s *memConnStore) Count(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns), nil
}

var _ provider.ConnectionStore = (*memConnStore)(nil)

// profilesRef builds profiles that reference the given connection names (one
// agent profile per name) for ConnectionService's in-use check tests.
func profilesRef(refs ...string) []provider.Profile {
	out := make([]provider.Profile, 0, len(refs))
	for i, ref := range refs {
		out = append(out, provider.Profile{
			Alias:         "p" + string(rune('a'+i)),
			Kind:          provider.ProfileKindAgent,
			ConnectionRef: ref,
		})
	}
	return out
}

// fakeProfileLister is a provider.ProfileLister returning a fixed slice.
type fakeProfileLister struct {
	profiles []provider.Profile
	err      error
}

func (f fakeProfileLister) List(context.Context) ([]provider.Profile, error) {
	return f.profiles, f.err
}

// In-use check: deleting a connection that a profile still references is rejected
// fail-closed with ErrConnectionInUse, and the connection is NOT removed.
func TestConnectionService_DeleteRejectsInUse(t *testing.T) {
	store := newMemConnStore(provider.Connection{Name: "openrouter-prod", Provider: "openrouter", BaseURL: "https://x"})
	lister := fakeProfileLister{profiles: profilesRef("openrouter-prod", "openrouter-prod", "other")}
	svc := provider.NewConnectionService(store, lister)

	err := svc.Delete(admins(), "openrouter-prod")
	assert.ErrorIs(t, err, provider.ErrConnectionInUse, "deleting an in-use connection must fail closed")
	// The error names the referencing profiles so the operator learns at delete time.
	assert.Contains(t, err.Error(), "openrouter-prod")

	// The connection must still exist (the delete was rejected before the store).
	_, getErr := store.Get(context.Background(), "openrouter-prod")
	assert.NoError(t, getErr, "a rejected delete must not remove the connection")
}

// An unreferenced connection deletes cleanly.
func TestConnectionService_DeleteUnreferencedSucceeds(t *testing.T) {
	store := newMemConnStore(provider.Connection{Name: "unused", Provider: "openrouter", BaseURL: "https://x"})
	lister := fakeProfileLister{profiles: profilesRef("some-other-conn")}
	svc := provider.NewConnectionService(store, lister)

	require.NoError(t, svc.Delete(admins(), "unused"))
	_, err := store.Get(context.Background(), "unused")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// UpdatePatch with absent extras (nil) PRESERVES the stored extras rather than
// blanking them. SECURITY: the sealed api_key is kept (nil key) and stays paired
// with its load-bearing extras (e.g. bedrock access-key-id/region), so a partial
// update of base_url cannot desync the credential from its routing/identity.
func TestConnectionService_UpdatePatch_AbsentExtrasPreserved(t *testing.T) {
	store := newMemConnStore(provider.Connection{
		Name:     "bed",
		Provider: "bedrock",
		BaseURL:  "https://bedrock-runtime.us-east-1.amazonaws.com",
		Extras:   map[string]string{"region": "us-east-1", "access-key-id": "AKIA-OLD"},
	})
	store.keys["bed"] = "sealed-secret"
	svc := provider.NewConnectionService(store, fakeProfileLister{})

	// Admin changes only base_url; extras absent (nil) and api_key absent (nil).
	err := svc.UpdatePatch(admins(), provider.Connection{
		Name:     "bed",
		Provider: "bedrock",
		BaseURL:  "https://bedrock-runtime.us-east-1.amazonaws.com/v2",
	}, nil, nil)
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "bed")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"region": "us-east-1", "access-key-id": "AKIA-OLD"}, got.Extras,
		"absent extras must be preserved, not blanked")
	assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com/v2", got.BaseURL)
	assert.Equal(t, "sealed-secret", store.keys["bed"], "absent api_key preserves the sealed key")
}

// UpdatePatch with present extras REPLACES the stored extras (explicit set).
func TestConnectionService_UpdatePatch_PresentExtrasReplace(t *testing.T) {
	store := newMemConnStore(provider.Connection{
		Name:     "az",
		Provider: "azure-openai",
		BaseURL:  "https://az",
		Extras:   map[string]string{"api-version": "2024-01-01"},
	})
	svc := provider.NewConnectionService(store, fakeProfileLister{})

	newExtras := map[string]string{"api-version": "2024-06-01"}
	err := svc.UpdatePatch(admins(), provider.Connection{
		Name: "az", Provider: "azure-openai", BaseURL: "https://az",
	}, &newExtras, nil)
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "az")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"api-version": "2024-06-01"}, got.Extras, "present extras replaces the stored map")
}

// UpdatePatch gates on admin and rejects an unknown connection.
func TestConnectionService_UpdatePatch_AuthzAndMissing(t *testing.T) {
	store := newMemConnStore(provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"})
	svc := provider.NewConnectionService(store, fakeProfileLister{})

	assert.ErrorIs(t, svc.UpdatePatch(users(), provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}, nil, nil), provider.ErrForbidden)
	assert.ErrorIs(t, svc.UpdatePatch(admins(), provider.Connection{Name: "ghost", Provider: "openrouter", BaseURL: "https://x"}, nil, nil), provider.ErrConnectionNotFound)
}

// Authz: every connection operation (Create/Update/Delete/Get/List) is
// admin-only. A non-admin is forbidden; a missing identity is unauthenticated.
func TestConnectionService_AdminGate(t *testing.T) {
	conn := provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}

	t.Run("non-admin is forbidden on every op", func(t *testing.T) {
		svc := provider.NewConnectionService(newMemConnStore(conn), fakeProfileLister{})
		_, err := svc.List(users())
		assert.ErrorIs(t, err, provider.ErrForbidden)
		_, err = svc.Get(users(), "c")
		assert.ErrorIs(t, err, provider.ErrForbidden)
		assert.ErrorIs(t, svc.Create(users(), conn, "k"), provider.ErrForbidden)
		assert.ErrorIs(t, svc.Update(users(), conn, nil), provider.ErrForbidden)
		assert.ErrorIs(t, svc.Delete(users(), "c"), provider.ErrForbidden)
	})

	t.Run("unauthenticated is rejected on every op", func(t *testing.T) {
		svc := provider.NewConnectionService(newMemConnStore(conn), fakeProfileLister{})
		_, err := svc.List(anon())
		assert.ErrorIs(t, err, provider.ErrUnauthenticated)
		_, err = svc.Get(anon(), "c")
		assert.ErrorIs(t, err, provider.ErrUnauthenticated)
		assert.ErrorIs(t, svc.Create(anon(), conn, "k"), provider.ErrUnauthenticated)
		assert.ErrorIs(t, svc.Update(anon(), conn, nil), provider.ErrUnauthenticated)
		assert.ErrorIs(t, svc.Delete(anon(), "c"), provider.ErrUnauthenticated)
	})
}

// Happy path: an admin passes through to the store on every operation, and the
// plaintext key is forwarded to Create.
func TestConnectionService_AdminPassthrough(t *testing.T) {
	store := newMemConnStore()
	svc := provider.NewConnectionService(store, fakeProfileLister{})
	conn := provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}

	require.NoError(t, svc.Create(admins(), conn, "sk-secret"))
	assert.Equal(t, "sk-secret", store.keys["c"], "service forwards the key to the store")

	list, err := svc.List(admins())
	require.NoError(t, err)
	assert.Len(t, list, 1)

	got, err := svc.Get(admins(), "c")
	require.NoError(t, err)
	assert.Equal(t, "openrouter", got.Provider)

	newKey := "sk-rotated"
	require.NoError(t, svc.Update(admins(), provider.Connection{Name: "c", Provider: "litellm", BaseURL: "https://y"}, &newKey))
	assert.Equal(t, "sk-rotated", store.keys["c"])

	require.NoError(t, svc.Delete(admins(), "c"))
	_, err = store.Get(context.Background(), "c")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// Security: ConnectionService must NOT expose a Reveal method. The key
// decryption path is internal to the runtime resolver only. A future accidental
// addition of Reveal to ConnectionService would be caught here.
func TestConnectionService_NoRevealMethod(t *testing.T) {
	svc := provider.NewConnectionService(newMemConnStore(), fakeProfileLister{})
	rt := reflect.TypeOf(svc)
	_, hasReveal := rt.MethodByName("Reveal")
	assert.False(t, hasReveal, "ConnectionService must NOT expose Reveal — key decryption belongs in the resolver, not the service")
}

// Edge: Update with a nil apiKey preserves the previously stored key — the nil
// sentinel means "keep existing key", not "clear it".
func TestConnectionService_UpdateNilKeyPreservesStoredKey(t *testing.T) {
	store := newMemConnStore()
	svc := provider.NewConnectionService(store, fakeProfileLister{})
	conn := provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}

	require.NoError(t, svc.Create(admins(), conn, "sk-original"))
	assert.Equal(t, "sk-original", store.keys["c"])

	require.NoError(t, svc.Update(admins(), provider.Connection{Name: "c", Provider: "litellm", BaseURL: "https://y"}, nil))
	assert.Equal(t, "sk-original", store.keys["c"], "nil apiKey must preserve the stored key")
}

// Domain validation still applies under the admin gate: an Azure connection
// missing api-version is rejected by the store's ValidateConnection.
func TestConnectionService_DelegatesValidation(t *testing.T) {
	svc := provider.NewConnectionService(newMemConnStore(), fakeProfileLister{})
	err := svc.Create(admins(), provider.Connection{Name: "az", Provider: "azure-openai", BaseURL: "https://az"}, "k")
	assert.ErrorIs(t, err, provider.ErrAzureAPIVersionRequired)
}

// Error (Pass 2): Get on a missing name propagates ErrConnectionNotFound through
// the admin gate. Confirms the service does not swallow or transform the sentinel.
func TestConnectionService_GetMissingPropagatesNotFound(t *testing.T) {
	svc := provider.NewConnectionService(newMemConnStore(), fakeProfileLister{})
	_, err := svc.Get(admins(), "ghost")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// Error (Pass 2): Create with a duplicate name propagates ErrConnectionExists so
// the caller can surface a meaningful conflict error, not a generic failure.
func TestConnectionService_CreateDuplicatePropagatesExists(t *testing.T) {
	conn := provider.Connection{Name: "dup", Provider: "openrouter", BaseURL: "https://x"}
	store := newMemConnStore(conn)
	svc := provider.NewConnectionService(store, fakeProfileLister{})
	err := svc.Create(admins(), conn, "sk")
	assert.ErrorIs(t, err, provider.ErrConnectionExists)
}

// Security (Pass 2): the Connection struct returned by Get carries no key-like
// field. This is a belt-and-suspenders structural check: even if an implementor
// adds an APIKey field to Connection by mistake, this test fails immediately.
func TestConnectionService_GetReturnsConnectionWithoutKeyField(t *testing.T) {
	conn := provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}
	store := newMemConnStore(conn)
	svc := provider.NewConnectionService(store, fakeProfileLister{})
	got, err := svc.Get(admins(), "c")
	require.NoError(t, err)

	rt := reflect.TypeOf(got)
	for i := range rt.NumField() {
		name := rt.Field(i).Name
		assert.False(t,
			name == "APIKey" || name == "ApiKey" || name == "Key" || name == "Secret",
			"Connection.%s looks like a key field — key material must not be carried on Connection", name,
		)
	}
}
