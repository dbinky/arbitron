package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/opencode"
	"github.com/arbitron/arbitron/adapter/pi"
	"github.com/arbitron/arbitron/adapter/probe"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type diagnosisInner struct {
	result  *engine.AgentResult
	err     error
	calls   int
	mission engine.AgentMission
	cancel  context.CancelFunc
}

func (a *diagnosisInner) RunAgent(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
	a.calls++
	a.mission = m
	if a.cancel != nil {
		a.cancel()
	}
	return a.result, a.err
}

type diagnosisProbes struct {
	t                                  *testing.T
	binding                            domprovider.ResolvedBinding
	calls, requests                    int
	models                             []string
	err, selectErr                     error
	unreachable, unsupported, nilProbe bool
}

func (p *diagnosisProbes) ProbeFor(b domprovider.ResolvedBinding) (domprovider.ConnectionProbe, error) {
	p.calls++
	assertDiagnosisBinding(p.t, p.binding, b)
	if p.nilProbe {
		return nil, nil
	}
	return p, p.selectErr
}
func (p *diagnosisProbes) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	p.requests++
	assertDiagnosisBinding(p.t, p.binding, b)
	key, err := b.RevealKey(ctx)
	require.NoError(p.t, err)
	require.Equal(p.t, "lr_k", key)
	deadline, bounded := ctx.Deadline()
	require.True(p.t, bounded)
	require.Positive(p.t, time.Until(deadline))
	require.LessOrEqual(p.t, time.Until(deadline), 20*time.Second)
	return domprovider.ProbeResult{Reachable: !p.unreachable, ModelsUnsupported: p.unsupported, Models: p.models}, p.err
}
func assertDiagnosisBinding(t *testing.T, want, got domprovider.ResolvedBinding) {
	t.Helper()
	// Compare every non-function field, not just the model/connection label.
	want.RevealKey, got.RevealKey = nil, nil
	require.Equal(t, want, got)
}
func diagnosisBinding() domprovider.ResolvedBinding {
	return domprovider.ResolvedBinding{Kind: domprovider.ProfileKindAgent, Backend: "opencode", BackendExplicit: true, Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com", Model: "glm-5.3", ConnectionRef: "lr", Effort: domprovider.EffortHigh,
		RevealKey: func(context.Context) (string, error) { return "lr_k", nil }}
}
func diagnosisAgent(t *testing.T, inner engine.AgentProvider, probes domprovider.ProbeSelector) engine.AgentProvider {
	t.Helper()
	b := diagnosisBinding()
	a := wrapLunaRouteAgent(inner, b, probes, domprovider.CapabilityRules{}).(*lunaRouteAgent)
	calls := 0
	t.Cleanup(func() { assert.Equal(t, 1, calls, "preflight must run once") })
	a.preflight = func(got domprovider.ResolvedBinding) error {
		calls++
		assertDiagnosisBinding(t, b, got)
		require.True(t, got.BackendExplicit)
		return nil
	}
	return a
}
func TestLunaRouteAgent_SuccessIsPassThrough(t *testing.T) {
	want := &engine.AgentResult{Summary: "done", ModelEvidence: []engine.ModelEvidence{{Model: "glm-5.3", Provider: "lunaroute", Source: "export:main"}}}
	inner := &diagnosisInner{result: want}
	probes := &diagnosisProbes{t: t, binding: diagnosisBinding()}
	mission := engine.AgentMission{Prompt: "nonce work", Model: "glm-5.3", WorkDir: t.TempDir(), ToolScope: []string{"Read"}, Timeout: time.Minute}
	got, err := diagnosisAgent(t, inner, probes).RunAgent(context.Background(), mission)
	require.NoError(t, err)
	assert.Same(t, want, got)
	assert.Equal(t, mission, inner.mission)
	assert.Equal(t, 1, inner.calls)
	assert.Zero(t, probes.calls)
}
func TestLunaRouteAgent_LaunchRefusalBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domprovider.ResolvedBinding)
		want   error
	}{
		{"backend", func(b *domprovider.ResolvedBinding) { b.Backend = "unsupported-fixture" }, domprovider.ErrLunaRouteBackendUnsupported},
		{"endpoint", func(b *domprovider.ResolvedBinding) { b.BaseURL = "https://evil.example" }, domprovider.ErrLunaRouteEndpoint},
		{"kind", func(b *domprovider.ResolvedBinding) { b.Kind = domprovider.ProfileKindChat }, engine.ErrBindingKindMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := diagnosisBinding()
			tc.mutate(&b)
			inner := &diagnosisInner{}
			probes := &diagnosisProbes{t: t, binding: b}
			_, err := wrapLunaRouteAgent(inner, b, probes, domprovider.CapabilityRules{}).RunAgent(context.Background(), engine.AgentMission{})
			require.ErrorIs(t, err, tc.want)
			assert.Zero(t, inner.calls)
			assert.Zero(t, probes.calls)
		})
	}
}
func TestLunaRouteAgent_DiagnosisObservations(t *testing.T) {
	for _, tc := range []struct {
		name            string
		configure       func(*diagnosisProbes)
		want            error
		checked, listed bool
		requests        int
	}{
		{"credentials now", func(p *diagnosisProbes) {
			p.err = domprovider.ClassifyLunaRoute(domprovider.LunaRouteOutcome{Operation: domprovider.LunaRouteListModels, Status: 401})
		}, domprovider.ErrLunaRouteCredentialsRejected, true, false, 1},
		{"service now", func(p *diagnosisProbes) {
			p.err = domprovider.ClassifyLunaRoute(domprovider.LunaRouteOutcome{Operation: domprovider.LunaRouteListModels, Status: 503})
		}, domprovider.ErrLunaRouteServiceFailure, true, false, 1},
		{"missing now", func(p *diagnosisProbes) { p.models = []string{"other"} }, domprovider.ErrLunaRouteModelUnavailable, true, false, 1},
		{"empty now", func(p *diagnosisProbes) {}, domprovider.ErrLunaRouteModelUnavailable, true, false, 1},
		{"listed now", func(p *diagnosisProbes) { p.models = []string{"glm-5.3"} }, domprovider.ErrLunaRouteCauseUnknown, true, true, 1},
		{"guard fault", func(p *diagnosisProbes) { p.err = domprovider.ErrLunaRouteStoredCredential }, domprovider.ErrLunaRouteCauseUnknown, false, false, 1},
		{"deadline", func(p *diagnosisProbes) { p.err = context.DeadlineExceeded }, domprovider.ErrLunaRouteCauseUnknown, false, false, 1},
		{"no provider", func(p *diagnosisProbes) { p.selectErr = domprovider.ErrNoProbeForProvider }, domprovider.ErrLunaRouteCauseUnknown, false, false, 0},
		{"nil probe", func(p *diagnosisProbes) { p.nilProbe = true }, domprovider.ErrLunaRouteCauseUnknown, false, false, 0},
		{"unreachable", func(p *diagnosisProbes) { p.unreachable = true }, domprovider.ErrLunaRouteCauseUnknown, false, false, 1},
		{"unsupported", func(p *diagnosisProbes) { p.unsupported = true }, domprovider.ErrLunaRouteCauseUnknown, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := errors.New("exit status 1 lr_secret")
			result := &engine.AgentResult{Summary: "partial"}
			p := &diagnosisProbes{t: t, binding: diagnosisBinding()}
			tc.configure(p)
			got, err := diagnosisAgent(t, &diagnosisInner{err: original, result: result}, p).RunAgent(context.Background(), engine.AgentMission{})
			require.ErrorIs(t, err, original)
			require.ErrorIs(t, err, tc.want)
			assert.Same(t, result, got)
			var d *domprovider.LunaRouteDiagnosis
			require.ErrorAs(t, err, &d)
			assert.Equal(t, tc.checked, d.Checked)
			assert.Equal(t, tc.listed, d.ModelListed)
			assert.Equal(t, 1, p.calls)
			assert.Equal(t, tc.requests, p.requests)
			assert.NotContains(t, err.Error(), "lr_secret")
		})
	}
}
func TestLunaRouteAgent_NoDiagnosisForLocalOrCancelled(t *testing.T) {
	for _, local := range []error{context.Canceled, context.DeadlineExceeded, domprovider.ErrLunaRouteStoredCredential, domprovider.ErrLunaRouteEndpoint, domprovider.ErrLunaRouteLocalFailure, domprovider.ErrInvalidSelection, domprovider.ErrSpecificVersionUnavailable, domprovider.ErrSelectionPortsMissing, ErrUnknownProvider, ErrUnknownBackend, opencode.ErrUnknownProvider, pi.ErrUnknownProvider, domprovider.ClassifyLunaRoute(domprovider.LunaRouteOutcome{Operation: domprovider.LunaRouteAgent, Status: 429})} {
		t.Run(local.Error(), func(t *testing.T) {
			p := &diagnosisProbes{t: t, binding: diagnosisBinding()}
			original := fmt.Errorf("wrapped: %w", local)
			_, err := diagnosisAgent(t, &diagnosisInner{err: original}, p).RunAgent(context.Background(), engine.AgentMission{})
			require.Same(t, original, err)
			assert.Zero(t, p.calls)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &diagnosisProbes{t: t, binding: diagnosisBinding()}
	original := errors.New("killed")
	_, err := diagnosisAgent(t, &diagnosisInner{err: original, cancel: cancel}, p).RunAgent(ctx, engine.AgentMission{})
	require.Same(t, original, err)
	assert.Zero(t, p.calls)
}

type diagnosisTransport func(*http.Request) (*http.Response, error)

func (f diagnosisTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This exercises the real probe selector and HTTP implementation. Only the
// phased launch-policy decision uses the strict unit spy; the HTTP sink still
// authenticates the selected binding and refuses any other destination.
func TestLunaRouteAgent_PostFailureHTTPSink(t *testing.T) {
	calls := 0
	transport := diagnosisTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "https://gw.lunaroute.com/v1/models", r.URL.String())
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer lr_k", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("x-api-key"))
		assert.Empty(t, r.Header.Get("LUNAROUTE-API-KEY"))
		_, bounded := r.Context().Deadline()
		require.True(t, bounded)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"glm-5.3"}]}`)), Request: r}, nil
	})
	probes := probe.NewProbeFactory(probe.Deps{HTTPClient: &http.Client{Transport: transport}, MaxResponseBytes: 1024})
	original := errors.New("opaque subprocess failure")
	_, err := diagnosisAgent(t, &diagnosisInner{err: original}, probes).RunAgent(context.Background(), engine.AgentMission{})
	require.ErrorIs(t, err, original)
	var diagnosis *domprovider.LunaRouteDiagnosis
	require.ErrorAs(t, err, &diagnosis)
	assert.True(t, diagnosis.Checked)
	assert.True(t, diagnosis.ModelListed)
	assert.Contains(t, err.Error(), "not proof of the original cause")
	assert.Equal(t, 1, calls)
}

func TestLunaRouteAgent_NilProbesIsUncheckedNotPanic(t *testing.T) {
	original := errors.New("exit status 2")
	_, err := diagnosisAgent(t, &diagnosisInner{err: original}, nil).RunAgent(context.Background(), engine.AgentMission{})
	require.ErrorIs(t, err, original)
	require.ErrorIs(t, err, domprovider.ErrLunaRouteCauseUnknown)
}
