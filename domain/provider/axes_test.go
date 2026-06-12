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

// Valid: every defined Effort constant passes Valid; an unknown string does not.
func TestEffort_Valid(t *testing.T) {
	for _, e := range []provider.Effort{
		provider.EffortDefault,
		provider.EffortNone,
		provider.EffortLow,
		provider.EffortMedium,
		provider.EffortHigh,
		provider.EffortXHigh,
		provider.EffortMax,
	} {
		assert.Truef(t, e.Valid(), "expected %q valid", e)
	}
	assert.False(t, provider.Effort("ultra").Valid(), "expected ultra invalid")
	assert.False(t, provider.Effort("").Valid(), "empty string is not a valid Effort constant (parse normalises separately)")
}

// GreaterThan: the full graded order is none < low < medium < high < xhigh < max;
// EffortDefault sits outside the order (never greater, nothing is greater than it).
func TestEffort_GreaterThan(t *testing.T) {
	tests := []struct {
		e, o     provider.Effort
		expected bool
		name     string
	}{
		// Graded order — adjacent and span
		{provider.EffortMax, provider.EffortXHigh, true, "max > xhigh"},
		{provider.EffortXHigh, provider.EffortHigh, true, "xhigh > high"},
		{provider.EffortHigh, provider.EffortMedium, true, "high > medium"},
		{provider.EffortMedium, provider.EffortLow, true, "medium > low"},
		{provider.EffortLow, provider.EffortNone, true, "low > none"},
		{provider.EffortMax, provider.EffortNone, true, "max > none (span)"},
		// Reverses
		{provider.EffortNone, provider.EffortLow, false, "none not > low"},
		{provider.EffortHigh, provider.EffortXHigh, false, "high not > xhigh"},
		{provider.EffortXHigh, provider.EffortMax, false, "xhigh not > max"},
		// Equal levels are never greater
		{provider.EffortNone, provider.EffortNone, false, "none not > none (equal)"},
		{provider.EffortLow, provider.EffortLow, false, "low not > low (equal)"},
		{provider.EffortHigh, provider.EffortHigh, false, "high not > high (equal)"},
		{provider.EffortMax, provider.EffortMax, false, "max not > max (equal)"},
		// Default is outside the order — never greater, nothing is greater than it
		{provider.EffortDefault, provider.EffortDefault, false, "default not > default"},
		{provider.EffortDefault, provider.EffortLow, false, "default not > low (unordered)"},
		{provider.EffortLow, provider.EffortDefault, false, "low not > default (unordered)"},
		{provider.EffortNone, provider.EffortDefault, false, "none not > default (unordered)"},
		{provider.EffortMax, provider.EffortDefault, false, "max not > default (unordered)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.e.GreaterThan(tc.o))
		})
	}
}

// Parse is case-sensitive: capitalised or mixed-case variants of known values
// are not accepted. This matters because LLMs generating MCP tool calls may
// produce "High", "200K", or "Default" — they must be rejected so validation
// stays fail-closed rather than silently accepting a mismatched string.
func TestParse_CaseSensitive(t *testing.T) {
	effortCases := []string{"High", "NONE", "Low", "Medium", "XHigh", "Max", "DEFAULT"}
	for _, s := range effortCases {
		t.Run("effort/"+s, func(t *testing.T) {
			_, err := provider.ParseEffort(s)
			assert.ErrorIs(t, err, provider.ErrInvalidEffort, "case variant %q must be rejected", s)
		})
	}

	cwCases := []string{"200K", "1M", "Default", "DEFAULT"}
	for _, s := range cwCases {
		t.Run("contextwindow/"+s, func(t *testing.T) {
			_, err := provider.ParseContextWindow(s)
			assert.ErrorIs(t, err, provider.ErrInvalidContextWindow, "case variant %q must be rejected", s)
		})
	}
}

// Sentinel errors are non-nil and distinct so callers can match with errors.Is.
func TestAxisSentinels_Distinct(t *testing.T) {
	require.Error(t, provider.ErrInvalidContextWindow)
	require.Error(t, provider.ErrInvalidEffort)
	assert.False(t, errors.Is(provider.ErrInvalidContextWindow, provider.ErrInvalidEffort))
}
