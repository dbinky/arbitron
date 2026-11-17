package provider_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lrBinding(base, key string, keyErr error, calls *int) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "lunaroute", BaseURL: base, Model: "glm-5.3",
		RevealKey: func(context.Context) (string, error) {
			*calls++
			return key, keyErr
		},
	}
}

func TestGuardLunaRouteReveal_PassesValidKeyUnchanged(t *testing.T) {
	calls := 0
	b := provider.GuardLunaRouteReveal(lrBinding("https://gw.lunaroute.com", "lr_live_ok", nil, &calls))
	assert.Zero(t, calls, "construction stays lazy")
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lr_live_ok", key)
	assert.Equal(t, 1, calls)
	assert.Equal(t, "glm-5.3", b.Model, "other fields untouched")
}

func TestGuardLunaRouteReveal_RejectsForeignOrEmptyKeys(t *testing.T) {
	for _, k := range []string{"sk-or-v1-foreign", "", "lr_", " lr_x"} {
		calls := 0
		b := provider.GuardLunaRouteReveal(lrBinding("https://gw.lunaroute.com", k, nil, &calls))
		key, err := b.RevealKey(context.Background())
		require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential, k)
		assert.Empty(t, key, "no partial value")
		assert.Equal(t, 1, calls)
		// The public lr_ format hint is not secret material.
		if k != "" && k != "lr_" {
			assert.NotContains(t, err.Error(), k)
		}
	}
}

func TestGuardLunaRouteReveal_EndpointCheckedBeforeReveal(t *testing.T) {
	calls := 0
	b := provider.GuardLunaRouteReveal(lrBinding("https://evil.example/v1", "lr_live_ok", nil, &calls))
	assert.Zero(t, calls)
	key, err := b.RevealKey(context.Background())
	require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
	assert.Empty(t, key)
	assert.Zero(t, calls, "the store is never asked for a key bound for a non-hosted endpoint")
}

func TestGuardLunaRouteReveal_PropagatesRevealErrors(t *testing.T) {
	calls := 0
	b := provider.GuardLunaRouteReveal(lrBinding("", "lr_partial", context.Canceled, &calls))
	key, err := b.RevealKey(context.Background())
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, key, "partial value from a failed reveal is dropped")

	boom := errors.New("decrypt failed: sk-foreign-partial")
	b = provider.GuardLunaRouteReveal(lrBinding("", "sk-foreign-partial", boom, &calls))
	key, err = b.RevealKey(context.Background())
	require.ErrorIs(t, err, boom)
	assert.Empty(t, key)
	assert.NotContains(t, err.Error(), "sk-foreign-partial")
}

func TestGuardLunaRouteReveal_NilRevealFailsClosed(t *testing.T) {
	b := provider.GuardLunaRouteReveal(provider.ResolvedBinding{Provider: " LunaRoute ", BaseURL: ""})
	require.NotNil(t, b.RevealKey)
	key, err := b.RevealKey(context.Background())
	require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
	assert.Empty(t, key)
}

func TestGuardLunaRouteReveal_NonLunaRouteUntouched(t *testing.T) {
	calls := 0
	in := provider.ResolvedBinding{Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
		RevealKey: func(context.Context) (string, error) { calls++; return "sk-or-v1-x", nil }}
	out := provider.GuardLunaRouteReveal(in)
	assert.Equal(t, reflect.ValueOf(in.RevealKey).Pointer(), reflect.ValueOf(out.RevealKey).Pointer(), "same closure")
	key, err := out.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-or-v1-x", key)
	assert.Equal(t, 1, calls)
	none := provider.GuardLunaRouteReveal(provider.ResolvedBinding{Provider: "claude-cli"})
	assert.Nil(t, none.RevealKey, "nil stays nil for non-LunaRoute")
}
