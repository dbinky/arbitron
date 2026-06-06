package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProbe is a minimal ConnectionProbe used to assert the port shape compiles
// and a selector can return it. Concrete adapter behaviour is tested in
// adapter/probe; this file only pins the domain contract.
type fakeProbe struct {
	res provider.ProbeResult
	err error
}

func (f fakeProbe) Probe(_ context.Context, _ provider.ResolvedBinding) (provider.ProbeResult, error) {
	return f.res, f.err
}

// fakeSelector implements provider.ProbeSelector.
type fakeSelector struct {
	probe provider.ConnectionProbe
	err   error
}

func (s fakeSelector) ProbeFor(_ provider.ResolvedBinding) (provider.ConnectionProbe, error) {
	return s.probe, s.err
}

// Failure: a ConnectionProbe returning an error propagates it unchanged so
// callers can errors.Is against classification sentinels.
func TestConnectionProbe_ProbeError(t *testing.T) {
	wantErr := provider.ErrNoProbeForProvider
	var p provider.ConnectionProbe = fakeProbe{err: wantErr}
	res, err := p.Probe(context.Background(), provider.ResolvedBinding{})
	assert.ErrorIs(t, err, wantErr)
	assert.False(t, res.Reachable, "failed probe must return zero ProbeResult")
}

// Happy path: a ConnectionProbe can be invoked through the interface and its
// ProbeResult round-trips the supported-listing shape.
func TestConnectionProbe_PortShape(t *testing.T) {
	var p provider.ConnectionProbe = fakeProbe{
		res: provider.ProbeResult{Reachable: true, Models: []string{"gpt-4o"}},
	}
	res, err := p.Probe(context.Background(), provider.ResolvedBinding{Provider: "openai"})
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.False(t, res.ModelsUnsupported)
	assert.Equal(t, []string{"gpt-4o"}, res.Models)
}

// Edge case: the zero ProbeResult is the documented "not reachable, supported,
// no models" value so callers can rely on field defaults.
func TestProbeResult_ZeroValue(t *testing.T) {
	var res provider.ProbeResult
	assert.False(t, res.Reachable)
	assert.False(t, res.ModelsUnsupported)
	assert.Nil(t, res.Models)
}

// Happy path: a ProbeSelector returns the chosen probe for a binding.
func TestProbeSelector_PortShape(t *testing.T) {
	want := fakeProbe{res: provider.ProbeResult{Reachable: true, ModelsUnsupported: true}}
	var sel provider.ProbeSelector = fakeSelector{probe: want}
	got, err := sel.ProbeFor(provider.ResolvedBinding{Provider: "claude-cli"})
	require.NoError(t, err)
	require.NotNil(t, got)
	res, err := got.Probe(context.Background(), provider.ResolvedBinding{})
	require.NoError(t, err)
	assert.True(t, res.ModelsUnsupported)
}

// Error handling: ErrNoProbeForProvider is a distinct sentinel a selector can
// surface (and callers match with errors.Is).
func TestErrNoProbeForProvider_IsSentinel(t *testing.T) {
	var sel provider.ProbeSelector = fakeSelector{err: provider.ErrNoProbeForProvider}
	_, err := sel.ProbeFor(provider.ResolvedBinding{Provider: "made-up"})
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrNoProbeForProvider)
}
