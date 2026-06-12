package provider_test

import (
	"context"
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

// Authz: every connection operation (Create/Update/Delete/Get/List) is
// admin-only. A non-admin is forbidden; a missing identity is unauthenticated.
func TestConnectionService_AdminGate(t *testing.T) {
	conn := provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}

	t.Run("non-admin is forbidden on every op", func(t *testing.T) {
		svc := provider.NewConnectionService(newMemConnStore(conn))
		_, err := svc.List(users())
		assert.ErrorIs(t, err, provider.ErrForbidden)
		_, err = svc.Get(users(), "c")
		assert.ErrorIs(t, err, provider.ErrForbidden)
		assert.ErrorIs(t, svc.Create(users(), conn, "k"), provider.ErrForbidden)
		assert.ErrorIs(t, svc.Update(users(), conn, nil), provider.ErrForbidden)
		assert.ErrorIs(t, svc.Delete(users(), "c"), provider.ErrForbidden)
	})

	t.Run("unauthenticated is rejected on every op", func(t *testing.T) {
		svc := provider.NewConnectionService(newMemConnStore(conn))
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
	svc := provider.NewConnectionService(store)
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

// Domain validation still applies under the admin gate: an Azure connection
// missing api-version is rejected by the store's ValidateConnection.
func TestConnectionService_DelegatesValidation(t *testing.T) {
	svc := provider.NewConnectionService(newMemConnStore())
	err := svc.Create(admins(), provider.Connection{Name: "az", Provider: "azure-openai", BaseURL: "https://az"}, "k")
	assert.ErrorIs(t, err, provider.ErrAzureAPIVersionRequired)
}
