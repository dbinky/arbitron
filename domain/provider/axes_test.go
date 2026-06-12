package provider_test

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy path: every defined ContextWindow value reports Valid, and an unknown
// tier string does not.
func TestContextWindow_Valid(t *testing.T) {
	for _, v := range []provider.ContextWindow{
		provider.ContextDefault,
		provider.Context200K,
		provider.Context1M,
	} {
		assert.Truef(t, v.Valid(), "expected %q valid", v)
	}
	assert.False(t, provider.ContextWindow("4m").Valid(), "expected 4m invalid")
}

// Parse: a known tier round-trips, an empty string normalizes to the unset
// default, and an unknown value returns ErrInvalidContextWindow.
func TestParseContextWindow(t *testing.T) {
	got, err := provider.ParseContextWindow("200k")
	require.NoError(t, err)
	assert.Equal(t, provider.Context200K, got)

	_, err = provider.ParseContextWindow("nope")
	assert.ErrorIs(t, err, provider.ErrInvalidContextWindow)

	// empty string normalizes to default (unset)
	got, err = provider.ParseContextWindow("")
	require.NoError(t, err)
	assert.Equal(t, provider.ContextDefault, got)
}

// Effort ordering: graded levels compare, default is outside the order (never
// greater), and an unknown effort string returns ErrInvalidEffort.
func TestEffort_OrderAndParse(t *testing.T) {
	assert.True(t, provider.EffortHigh.GreaterThan(provider.EffortMedium), "high > medium")
	assert.False(t, provider.EffortDefault.GreaterThan(provider.EffortLow), "default is unordered, never greater")

	_, err := provider.ParseEffort("ultra")
	assert.ErrorIs(t, err, provider.ErrInvalidEffort)
}

// Parse: a known effort round-trips and an empty string normalizes to the unset
// default.
func TestParseEffort(t *testing.T) {
	got, err := provider.ParseEffort("high")
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, got)

	got, err = provider.ParseEffort("")
	require.NoError(t, err)
	assert.Equal(t, provider.EffortDefault, got)
}

// Sentinel errors are non-nil and distinct so callers can match with errors.Is.
func TestAxisSentinels_Distinct(t *testing.T) {
	require.Error(t, provider.ErrInvalidContextWindow)
	require.Error(t, provider.ErrInvalidEffort)
	assert.False(t, errors.Is(provider.ErrInvalidContextWindow, provider.ErrInvalidEffort))
}
