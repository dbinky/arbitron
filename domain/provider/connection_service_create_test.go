package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

// createRecorder is a store that applies none of the create rules itself and
// records exactly what the service hands it. It exposes what the service
// contract guarantees independently of any persistence adapter.
type createRecorder struct {
	*memConnStore
	created []provider.Connection
	keys    []string
}

func (r *createRecorder) Create(_ context.Context, c provider.Connection, apiKey string) error {
	r.created = append(r.created, c)
	r.keys = append(r.keys, apiKey)
	return nil
}

// The service, not the store, normalizes a LunaRoute create: canonical provider,
// hosted root, and the connection otherwise untouched.
func TestConnectionService_CreateNormalizesLunaRouteBeforeStore(t *testing.T) {
	for _, base := range []string{"", "https://gw.lunaroute.com", "https://gw.lunaroute.com/v1/"} {
		t.Run("base="+base, func(t *testing.T) {
			rec := &createRecorder{memConnStore: newMemConnStore()}
			svc := provider.NewConnectionService(rec, fakeProfileLister{})
			in := provider.Connection{Name: "lr", Provider: "LunaRoute", BaseURL: base, Extras: map[string]string{"note": "x"}}
			require.NoError(t, svc.Create(admins(), in, "lr_live_abc"))
			require.Len(t, rec.created, 1)
			got := rec.created[0]
			assert.Equal(t, provider.CanonicalLunaRoute, got.Provider)
			assert.Equal(t, provider.LunaRouteHostedRoot, got.BaseURL)
			assert.Equal(t, "lr", got.Name)
			assert.Equal(t, map[string]string{"note": "x"}, got.Extras)
			assert.Equal(t, []string{"lr_live_abc"}, rec.keys)
			assert.Equal(t, base, in.BaseURL, "the caller's value is not mutated")
		})
	}
}

// Refusals happen before the store is reached: nothing is persisted or sealed.
func TestConnectionService_CreateRefusesBeforeStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    provider.Connection
		key  string
		want error
	}{
		{"foreign key for lunaroute", provider.Connection{Name: "lr", Provider: "lunaroute"}, "sk-or-v1-foreign", provider.ErrLunaRouteKeyPrefix},
		{"empty key for lunaroute", provider.Connection{Name: "lr", Provider: "lunaroute"}, "", provider.ErrLunaRouteKeyRequired},
		{"unredactable key shape", provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_live_ab!cd", provider.ErrLunaRouteKeyPrefix},
		{"non-hosted endpoint", provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: "https://evil.example/v1"}, "lr_live_abc", provider.ErrLunaRouteEndpoint},
		{"azure without api-version", provider.Connection{Name: "az", Provider: "azure-openai", BaseURL: "https://az"}, "k", provider.ErrAzureAPIVersionRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &createRecorder{memConnStore: newMemConnStore()}
			err := provider.NewConnectionService(rec, fakeProfileLister{}).Create(admins(), tc.c, tc.key)
			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, rec.created)
			assert.Empty(t, rec.keys)
		})
	}
}

// Non-LunaRoute connections reach the store unchanged, and the admin gate still
// precedes everything.
func TestConnectionService_CreatePassesThroughForeignProvidersAndKeepsAdminGate(t *testing.T) {
	rec := &createRecorder{memConnStore: newMemConnStore()}
	svc := provider.NewConnectionService(rec, fakeProfileLister{})
	in := provider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.example/v1/"}
	require.NoError(t, svc.Create(admins(), in, "sk-or"))
	assert.Equal(t, []provider.Connection{in}, rec.created)

	rec.created, rec.keys = nil, nil
	err := svc.Create(context.Background(), provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_live_abc")
	require.Error(t, err)
	assert.Empty(t, rec.created, "an unauthorized caller reaches neither validation nor the store")
}
