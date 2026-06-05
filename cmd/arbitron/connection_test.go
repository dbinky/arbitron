package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	"github.com/arbitron/arbitron/domain/provider"
)

// --- Fakes ---------------------------------------------------------------

// fakeConnectionStore implements provider.ConnectionStore. Only Get and Reveal
// are exercised by the validate path; the CRUD methods panic if reached.
type fakeConnectionStore struct {
	conn        provider.Connection
	getErr      error
	secret      string
	revealErr   error
	revealNames []string
}

func (f *fakeConnectionStore) Get(_ context.Context, _ string) (provider.Connection, error) {
	if f.getErr != nil {
		return provider.Connection{}, f.getErr
	}
	return f.conn, nil
}

func (f *fakeConnectionStore) Reveal(_ context.Context, name string) (string, error) {
	f.revealNames = append(f.revealNames, name)
	if f.revealErr != nil {
		return "", f.revealErr
	}
	return f.secret, nil
}

func (f *fakeConnectionStore) Create(context.Context, provider.Connection, string) error {
	panic("Create not used by validate")
}
func (f *fakeConnectionStore) Update(context.Context, provider.Connection, *string) error {
	panic("Update not used by validate")
}
func (f *fakeConnectionStore) Delete(context.Context, string) error {
	panic("Delete not used by validate")
}
func (f *fakeConnectionStore) List(context.Context) ([]provider.Connection, error) {
	panic("List not used by validate")
}

// fakeProbe implements provider.ConnectionProbe with a canned result/error. It
// optionally invokes the binding's RevealKey to mimic the real auth path.
type fakeProbe struct {
	result     provider.ProbeResult
	err        error
	callReveal bool
	calls      int
}

func (p *fakeProbe) Probe(ctx context.Context, b provider.ResolvedBinding) (provider.ProbeResult, error) {
	p.calls++
	if p.callReveal && b.RevealKey != nil {
		_, _ = b.RevealKey(ctx)
	}
	return p.result, p.err
}

// fakeProbeSelector implements provider.ProbeSelector, returning a canned probe
// or a selection error.
type fakeProbeSelector struct {
	probe    *fakeProbe
	selErr   error
	forCalls int
}

func (s *fakeProbeSelector) ProbeFor(provider.ResolvedBinding) (provider.ConnectionProbe, error) {
	s.forCalls++
	if s.selErr != nil {
		return nil, s.selErr
	}
	return s.probe, nil
}

func newDeps(store provider.ConnectionStore, sel provider.ProbeSelector) validateDeps {
	return validateDeps{store: store, selector: sel}
}

// --- Happy-path ----------------------------------------------------------

func TestRunConnectionValidate_ListsModels(t *testing.T) {
	store := &fakeConnectionStore{conn: provider.Connection{Name: "oai", Provider: "openai", BaseURL: "https://api.openai.com/v1"}}
	sel := &fakeProbeSelector{probe: &fakeProbe{result: provider.ProbeResult{Reachable: true, Models: []string{"gpt-4o", "gpt-4o-mini"}}}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "oai", &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "reachable")
	assert.Contains(t, out, "2 model(s)")
	assert.Contains(t, out, "gpt-4o")
	assert.Contains(t, out, "gpt-4o-mini")
}

func TestRunConnectionValidate_ReachabilityOnly(t *testing.T) {
	store := &fakeConnectionStore{conn: provider.Connection{Name: "claude", Provider: "claude-cli"}}
	sel := &fakeProbeSelector{probe: &fakeProbe{result: provider.ProbeResult{Reachable: true, ModelsUnsupported: true}}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "claude", &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "reachable")
	assert.Contains(t, out, "model listing not supported")
	assert.NotContains(t, out, "model(s):")
}

func TestFormatProbeResult_ThreeShapes(t *testing.T) {
	listed := formatProbeResult("c", "openai", provider.ProbeResult{Reachable: true, Models: []string{"a", "b"}})
	assert.Contains(t, listed, "2 model(s)")
	assert.Contains(t, listed, "a")
	assert.Contains(t, listed, "b")

	unsupported := formatProbeResult("c", "claude-cli", provider.ProbeResult{Reachable: true, ModelsUnsupported: true})
	assert.Contains(t, unsupported, "not supported")

	empty := formatProbeResult("c", "openai", provider.ProbeResult{Reachable: true})
	assert.Contains(t, empty, "reachable, no models")
}

func TestNewConnectionCmd_HostsValidateOnly(t *testing.T) {
	cmd := newConnectionCmd()
	assert.Equal(t, "connection", cmd.Use)
	subs := cmd.Commands()
	require.Len(t, subs, 1, "connection hosts exactly one subcommand (CRUD stays MCP-only)")
	assert.True(t, strings.HasPrefix(subs[0].Use, "validate"))
	for _, s := range subs {
		assert.NotContains(t, s.Use, "create")
		assert.NotContains(t, s.Use, "update")
		assert.NotContains(t, s.Use, "delete")
	}
}

// --- Input-validation ----------------------------------------------------

func TestRunConnectionValidate_UnknownConnection(t *testing.T) {
	store := &fakeConnectionStore{getErr: provider.ErrConnectionNotFound}
	sel := &fakeProbeSelector{probe: &fakeProbe{}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "missing", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such connection")
	assert.Contains(t, err.Error(), "missing")
	assert.Equal(t, 0, buf.Len(), "no success output on unknown connection")
	assert.Equal(t, 0, sel.forCalls, "selector never consulted for an unknown connection")
}

func TestConnectionValidate_RequiresExactlyOneArg(t *testing.T) {
	cmd := newConnectionValidateCmd()
	// Stub RunE so only arg parsing is exercised.
	cmd.RunE = func(*cobra.Command, []string) error { return nil }

	cmd.SetArgs([]string{})
	assert.Error(t, cmd.Execute(), "zero args must fail ExactArgs(1)")

	cmd2 := newConnectionValidateCmd()
	cmd2.RunE = func(*cobra.Command, []string) error { return nil }
	cmd2.SetArgs([]string{"a", "b"})
	assert.Error(t, cmd2.Execute(), "two args must fail ExactArgs(1)")
}

// --- Error-handling ------------------------------------------------------

func TestRunConnectionValidate_CredentialsRejected(t *testing.T) {
	store := &fakeConnectionStore{conn: provider.Connection{Name: "oai", Provider: "openai"}}
	sel := &fakeProbeSelector{probe: &fakeProbe{err: adaptprobe.ErrCredentialsRejected}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "oai", &buf)
	require.Error(t, err)
	assert.ErrorIs(t, err, adaptprobe.ErrCredentialsRejected)
	assert.Contains(t, err.Error(), "oai")
	assert.Equal(t, 0, buf.Len())
}

func TestRunConnectionValidate_Unreachable(t *testing.T) {
	store := &fakeConnectionStore{conn: provider.Connection{Name: "oai", Provider: "openai"}}
	sel := &fakeProbeSelector{probe: &fakeProbe{err: adaptprobe.ErrUnreachable}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "oai", &buf)
	require.Error(t, err)
	assert.ErrorIs(t, err, adaptprobe.ErrUnreachable)
	assert.Equal(t, 0, buf.Len())
}

func TestRunConnectionValidate_RegionConfigError(t *testing.T) {
	store := &fakeConnectionStore{conn: provider.Connection{Name: "bed", Provider: "bedrock"}}
	sel := &fakeProbeSelector{probe: &fakeProbe{err: adaptprobe.ErrRegionConfig}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "bed", &buf)
	require.Error(t, err)
	assert.ErrorIs(t, err, adaptprobe.ErrRegionConfig)
}

func TestRunConnectionValidate_NoProbeForProvider(t *testing.T) {
	store := &fakeConnectionStore{conn: provider.Connection{Name: "x", Provider: "mystery"}}
	fp := &fakeProbe{}
	sel := &fakeProbeSelector{probe: fp, selErr: provider.ErrNoProbeForProvider}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "x", &buf)
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrNoProbeForProvider)
	assert.Equal(t, 0, fp.calls, "probe is never called when selection fails")
}

func TestRunConnectionValidate_StoreGetTransientError(t *testing.T) {
	store := &fakeConnectionStore{getErr: errors.New("db locked")}
	sel := &fakeProbeSelector{probe: &fakeProbe{}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "oai", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looking up connection")
	assert.NotContains(t, err.Error(), "no such connection")
}

// --- Edge-cases ----------------------------------------------------------

func TestRunConnectionValidate_EmptyModelList(t *testing.T) {
	store := &fakeConnectionStore{conn: provider.Connection{Name: "oai", Provider: "openai"}}
	sel := &fakeProbeSelector{probe: &fakeProbe{result: provider.ProbeResult{Reachable: true, Models: nil}}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "oai", &buf)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "reachable, no models")
	assert.NotContains(t, out, "model(s):")
}

func TestRunConnectionValidate_KeylessConnection_LazyReveal(t *testing.T) {
	store := &fakeConnectionStore{
		conn:   provider.Connection{Name: "ollama-local", Provider: "ollama", BaseURL: "http://localhost:11434"},
		secret: "", // keyless local-Ollama
	}
	sel := &fakeProbeSelector{probe: &fakeProbe{callReveal: true, result: provider.ProbeResult{Reachable: true}}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "ollama-local", &buf)
	require.NoError(t, err)
	require.Len(t, store.revealNames, 1, "RevealKey wired lazily; the probe is the single decryption caller")
	assert.Equal(t, "ollama-local", store.revealNames[0])
}

func TestBindingFromConnection_MapsFields(t *testing.T) {
	store := &fakeConnectionStore{secret: "sealed"}
	c := provider.Connection{Provider: "bedrock", BaseURL: "", Extras: map[string]string{"region": "us-east-1", "access-key-id": "AKIA"}}
	b := bindingFromConnection(store, "bed", c)

	assert.Equal(t, "bedrock", b.Provider)
	assert.Equal(t, "us-east-1", b.Extras["region"])
	assert.Equal(t, "", b.Model)
	require.NotNil(t, b.RevealKey)
	_, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	require.Len(t, store.revealNames, 1)
	assert.Equal(t, "bed", store.revealNames[0])
}

// --- Security ------------------------------------------------------------

func TestRunConnectionValidate_NeverPrintsSecret(t *testing.T) {
	const secret = "sk-SUPER-SECRET"
	store := &fakeConnectionStore{conn: provider.Connection{Name: "oai", Provider: "openai"}, secret: secret}
	sel := &fakeProbeSelector{probe: &fakeProbe{callReveal: true, result: provider.ProbeResult{Reachable: true, Models: []string{"gpt-4o"}}}}

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	var buf bytes.Buffer
	err := runConnectionValidate(context.Background(), newDeps(store, sel), "oai", &buf)
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), secret)
	assert.NotContains(t, logBuf.String(), secret)
}

func TestRunConnectionValidate_ProbeErrorSecretFree(t *testing.T) {
	const secret = "sk-SUPER-SECRET"
	store := &fakeConnectionStore{conn: provider.Connection{Name: "oai", Provider: "openai"}, secret: secret}
	// Probe error is secret-free by Task-1 contract; assert wrapping does not
	// re-introduce the secret.
	sel := &fakeProbeSelector{probe: &fakeProbe{err: adaptprobe.ErrCredentialsRejected}}
	var buf bytes.Buffer

	err := runConnectionValidate(context.Background(), newDeps(store, sel), "oai", &buf)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
}
