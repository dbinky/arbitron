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

	"github.com/arbitron/arbitron/adapter/claudecli"
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
			inner := &diagnosisInner{err: original, result: result}
			got, err := diagnosisAgent(t, inner, p).RunAgent(context.Background(), engine.AgentMission{})
			require.ErrorIs(t, err, original)
			require.ErrorIs(t, err, tc.want)
			assert.Same(t, result, got)
			var d *domprovider.LunaRouteDiagnosis
			require.ErrorAs(t, err, &d)
			assert.Equal(t, tc.checked, d.Checked)
			assert.Equal(t, tc.listed, d.ModelListed)
			assert.Same(t, original, d.Original)
			assert.Equal(t, 1, inner.calls, "diagnosis must never retry the mission")
			switch {
			case tc.want == domprovider.ErrLunaRouteModelUnavailable:
				assert.Same(t, tc.want, d.Observation)
			case tc.checked && p.err != nil:
				assert.Same(t, p.err, d.Observation)
				assert.ErrorIs(t, d, p.err)
			default:
				assert.Nil(t, d.Observation, "local probe faults are not gateway observations")
			}
			if !tc.checked {
				assert.Contains(t, err.Error(), "could not be checked afterwards, so the cause is unknown")
				assert.NotContains(t, err.Error(), "a current observation")
				if p.err != nil {
					assert.NotErrorIs(t, err, p.err)
				}
			} else {
				assert.Contains(t, err.Error(), "a current observation, not proof of the original cause")
			}
			if tc.listed {
				assert.Contains(t, err.Error(), "the credential was accepted and the model is listed now, so the cause is unknown")
				assert.NotErrorIs(t, err, domprovider.ErrLunaRouteCredentialsRejected)
			}
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
	var d *domprovider.LunaRouteDiagnosis
	require.ErrorAs(t, err, &d)
	assert.False(t, d.Checked)
	assert.Nil(t, d.Observation)
	assert.Contains(t, err.Error(), "could not be checked afterwards")
}

// This inner sink reveals only if launch policy lets execution reach it.
type revealingDiagnosisInner struct {
	binding domprovider.ResolvedBinding
	calls   int
}

func (a *revealingDiagnosisInner) RunAgent(ctx context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
	a.calls++
	_, err := a.binding.RevealKey(ctx)
	return &engine.AgentResult{Summary: "launched"}, err
}

func TestLunaRouteAgent_EffectiveRouteRefusalsBeforeRevealAndRun(t *testing.T) {
	model := domprovider.LunaRouteModel{ID: "glm-5.3",
		ContextTokens: domprovider.LimitFact{Tokens: 200000, Evidence: "fixture#context"},
		OutputTokens:  domprovider.LimitFact{Tokens: 32768, Evidence: "fixture#output"},
		Reasoning:     domprovider.SupportFact{Support: domprovider.SupportSupported, Evidence: "fixture#reasoning"},
		Efforts:       map[string]domprovider.EffortFact{"pi": {Known: true, Levels: []domprovider.Effort{domprovider.EffortHigh}}},
	}
	for _, tc := range []struct {
		name   string
		mutate func(*domprovider.ResolvedBinding, *domprovider.LunaRouteModel)
		want   error
	}{
		{"saved pi route", func(*domprovider.ResolvedBinding, *domprovider.LunaRouteModel) {}, nil},
		{"node claude override", func(b *domprovider.ResolvedBinding, _ *domprovider.LunaRouteModel) { b.Backend = "claude-code" }, domprovider.ErrLunaRouteBackendUnsupported},
		{"node whitespace override", func(b *domprovider.ResolvedBinding, _ *domprovider.LunaRouteModel) { b.Backend = " \t" }, domprovider.ErrLunaRouteBackendRequired},
		{"legacy codex default", func(b *domprovider.ResolvedBinding, _ *domprovider.LunaRouteModel) {
			b.Backend, b.BackendExplicit = "codex", false
		}, domprovider.ErrLunaRouteBackendRequired},
		{"pi limits removed since save", func(_ *domprovider.ResolvedBinding, m *domprovider.LunaRouteModel) {
			m.OutputTokens = domprovider.LimitFact{}
		}, domprovider.ErrLunaRouteLimitsUnknown},
		{"pi efforts removed since save", func(_ *domprovider.ResolvedBinding, m *domprovider.LunaRouteModel) { m.Efforts = nil }, domprovider.ErrLunaRouteAxisUnsupported},
		{"tampered endpoint", func(b *domprovider.ResolvedBinding, _ *domprovider.LunaRouteModel) {
			b.BaseURL = "https://evil.example"
		}, domprovider.ErrLunaRouteEndpoint},
		{"alias pin", func(b *domprovider.ResolvedBinding, _ *domprovider.LunaRouteModel) { b.Model = "claude-sonnet-5-lr1" }, domprovider.ErrSpecificVersionUnavailable},
		{"unreviewed pin", func(b *domprovider.ResolvedBinding, _ *domprovider.LunaRouteModel) { b.Model = "glm-5.3-background" }, domprovider.ErrSpecificVersionUnavailable},
		{"empty pin", func(b *domprovider.ResolvedBinding, _ *domprovider.LunaRouteModel) { b.Model = "" }, domprovider.ErrSpecificVersionUnavailable},
		{"nil capability port", func(*domprovider.ResolvedBinding, *domprovider.LunaRouteModel) {}, domprovider.ErrSelectionPortsMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, m := diagnosisBinding(), model
			b.Backend, b.Pinned = "pi", true
			tc.mutate(&b, &m)
			reveals := 0
			b.RevealKey = func(context.Context) (string, error) { reveals++; return "lr_k", nil }
			var caps domprovider.LunaRouteCapabilities = domprovider.CapabilityRules{}.WithLunaRouteModels(map[string]domprovider.LunaRouteModel{model.ID: m})
			if tc.name == "nil capability port" {
				caps = nil
			}
			inner := &revealingDiagnosisInner{binding: b}
			probes := &diagnosisProbes{t: t, binding: b}
			_, err := wrapLunaRouteAgent(inner, b, probes, caps).RunAgent(context.Background(), engine.AgentMission{Model: b.Model})
			if tc.want == nil {
				require.NoError(t, err)
				assert.Equal(t, 1, inner.calls)
				assert.Equal(t, 1, reveals)
			} else {
				require.ErrorIs(t, err, tc.want)
				assert.Zero(t, inner.calls)
				assert.Zero(t, reveals)
			}
			assert.Zero(t, probes.calls)
		})
	}
}

type refusedRouteRunner struct{ calls int }

func (r *refusedRouteRunner) Run(context.Context, claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls++
	return nil, nil, errors.New("unexpected launch")
}

func TestLunaRouteAgent_SwitchedProviderPinRefusedBeforeRevealAndRunner(t *testing.T) {
	for _, backend := range []string{"pi", "codex"} {
		t.Run(backend, func(t *testing.T) {
			store := &lrConnStore{conn: domprovider.Connection{Name: "lr", Provider: "openai", BaseURL: "https://api.openai.com/v1"}, key: "foreign-key"}
			cache := NewProfileCacheFromProfiles(domprovider.Profile{Alias: "agent", Kind: domprovider.ProfileKindAgent, Backend: backend, Model: "glm-5.3", ConnectionRef: "lr"})
			b, err := NewDynamicProfileResolver(cache, store).Resolve(context.Background(), "agent")
			require.NoError(t, err)
			b.Pinned = true // the retained run's LunaRoute model pin
			runner := &refusedRouteRunner{}
			_, err = NewFactory(Deps{Runner: runner}).AgentProviderFor(b)
			require.ErrorIs(t, err, domprovider.ErrSpecificVersionUnavailable)
			assert.Zero(t, store.calls)
			assert.Zero(t, runner.calls)
		})
	}
}

func TestLunaRouteAgent_StalledDiagnosisHonorsDeadline(t *testing.T) {
	for _, parentBound := range []bool{false, true} {
		t.Run(fmt.Sprintf("parentBound=%v", parentBound), func(t *testing.T) {
			ctx := context.Background()
			limit := 20 * time.Second
			if parentBound {
				var cancel context.CancelFunc
				limit = 50 * time.Millisecond
				ctx, cancel = context.WithTimeout(ctx, limit)
				defer cancel()
			}
			calls := 0
			transport := diagnosisTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "https://gw.lunaroute.com/v1/models", r.URL.String())
				require.Equal(t, "Bearer lr_k", r.Header.Get("Authorization"))
				deadline, ok := r.Context().Deadline()
				require.True(t, ok)
				require.LessOrEqual(t, time.Until(deadline), limit)
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			probes := probe.NewProbeFactory(probe.Deps{HTTPClient: &http.Client{Transport: transport}})
			original := errors.New("opaque harness exit")
			inner := &diagnosisInner{err: original, result: &engine.AgentResult{Summary: "partial"}}
			start := time.Now()
			got, err := diagnosisAgent(t, inner, probes).RunAgent(ctx, engine.AgentMission{})
			require.ErrorIs(t, err, original)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Same(t, inner.result, got)
			assert.Less(t, time.Since(start), limit+2*time.Second)
			assert.Equal(t, 1, calls)
			assert.Equal(t, 1, inner.calls)
		})
	}
}
