package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLRCaps map[string]LunaRouteModel

func (f fakeLRCaps) LunaRouteModel(id string) (LunaRouteModel, bool) {
	m, ok := f[id]
	return m, ok
}

func fullGLM() LunaRouteModel {
	return LunaRouteModel{
		ID:            "glm-5.3",
		ContextTokens: LimitFact{Tokens: 200000, Evidence: "e#context"},
		OutputTokens:  LimitFact{Tokens: 32768, Evidence: "e#output"},
		Reasoning:     SupportFact{Support: SupportSupported, Evidence: "e#reasoning"},
		Efforts: map[string]EffortFact{
			LunaRouteChatAxis: {Known: true, Levels: []Effort{EffortLow, EffortHigh, EffortXHigh}, Evidence: "e#chat"},
			"claude-code":     {Known: true, Levels: []Effort{EffortNone, EffortHigh}, Evidence: "e#claude"},
			"codex":           {Known: true, Levels: []Effort{EffortNone, EffortLow}, Evidence: "e#codex"},
		},
	}
}

func agentRoute(model, backend string) LunaRouteRoute {
	return LunaRouteRoute{Kind: ProfileKindAgent, Model: model, Backend: backend}
}

func requireRefusal(t *testing.T, err error, reason error) *LunaRouteRefusal {
	t.Helper()
	require.ErrorIs(t, err, reason)
	require.ErrorIs(t, err, ErrInvalidSelection)
	var r *LunaRouteRefusal
	require.ErrorAs(t, err, &r)
	return r
}

func TestCheckLunaRouteRoute_HappyPaths(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "opencode", "claude-code", "codex", "pi")
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	for _, backend := range LunaRouteBackends {
		require.True(t, LunaRouteBackendEnabled(backend))
		require.NoError(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", backend)))
	}
	require.NoError(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3"}))
	require.NoError(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3", Effort: EffortHigh}))
	require.NoError(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindAgent, Model: "glm-5.3", Backend: "claude-code", Effort: EffortNone}))
	require.NoError(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3", ContextWindow: ContextDefault, Effort: EffortDefault, MaxOutputTokens: 32768}))
	// Use a reviewed id with no capability entry, not an unconfirmed release id.
	require.NoError(t, CheckLunaRouteRoute(fakeLRCaps{}, agentRoute("glm-5.3", "codex")))
	require.NoError(t, CheckLunaRouteRoute(fakeLRCaps{"glm-5.3-flash": {ContextTokens: LimitFact{Tokens: 1, Evidence: "e"}, OutputTokens: LimitFact{Tokens: 1, Evidence: "e"}}}, agentRoute("glm-5.3-flash", "claude-code")))
}

func TestCheckLunaRouteRoute_BackendGate(t *testing.T) {
	DisableLunaRouteBackendsForTest(t, "codex", "opencode")
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	r := requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", "")), ErrLunaRouteBackendRequired)
	assert.Contains(t, r.Error(), "claude-code, opencode, codex or pi")
	r = requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", "codex")), ErrLunaRouteBackendUnsupported)
	assert.Contains(t, r.Error(), "LunaRoute on codex not yet supported")
	requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", "opencode")), ErrLunaRouteBackendUnsupported)
	requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute("latest", "codex")), ErrLunaRouteBackendUnsupported)
	requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", "  ")), ErrLunaRouteBackendRequired)
}

func TestCheckLunaRouteRoute_GrammarThenFamily(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "opencode")
	for _, model := range []string{"latest", "", "GLM-5.3", "glm-999.9", "claude-sonnet-5-lr999", "lunaroute/glm-5.3"} {
		err := CheckLunaRouteRoute(fakeLRCaps{}, agentRoute(model, "opencode"))
		require.ErrorIs(t, err, ErrSpecificVersionUnavailable)
		r := requireRefusal(t, err, ErrLunaRouteModelSpelling)
		assert.Equal(t, model, r.Model)
		assert.Equal(t, "opencode", r.Backend)
		assert.Contains(t, r.Error(), "reviewed fixed LunaRoute id")
	}
}

func TestCheckLunaRouteRoute_TableIncompatibilities(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "opencode", "pi")
	glm := fullGLM()
	glm.Tasks = map[ProfileKind]SupportFact{ProfileKindChat: {Support: SupportUnsupported, Evidence: "ev#chat"}}
	glm.Backends = map[string]SupportFact{"pi": {Support: SupportUnsupported, Evidence: "ev#pi"}}
	caps := fakeLRCaps{"glm-5.3": glm}
	r := requireRefusal(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3"}), ErrLunaRouteIncompatible)
	assert.Contains(t, r.Error(), "ev#chat")
	r = requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", "pi")), ErrLunaRouteIncompatible)
	assert.Contains(t, r.Error(), "ev#pi")
	require.NoError(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", "opencode")), "unknown backend fact is not a refusal")
	glm.Tasks = map[ProfileKind]SupportFact{ProfileKindAgent: {Support: SupportUnsupported, Evidence: "ev#agent"}}
	caps["glm-5.3"] = glm
	r = requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute("glm-5.3", "opencode")), ErrLunaRouteIncompatible)
	assert.Contains(t, r.Error(), "ev#agent")
}

func TestCheckLunaRouteRoute_Axes(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "opencode", "claude-code", "codex")
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	for _, tier := range []ContextWindow{Context200K, Context1M, "invalid"} {
		r := requireRefusal(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindAgent, Model: "glm-5.3", Backend: "claude-code", ContextWindow: tier}), ErrLunaRouteAxisUnsupported)
		assert.Contains(t, r.Error(), "context")
	}
	for _, tc := range []struct {
		kind    ProfileKind
		backend string
		effort  Effort
	}{
		{ProfileKindChat, "", EffortXHigh},
		{ProfileKindAgent, "codex", EffortNone},
		{ProfileKindAgent, "opencode", EffortLow},
		{ProfileKindAgent, "opencode", EffortNone},
		{ProfileKindChat, "", EffortMedium},
	} {
		requireRefusal(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: tc.kind, Model: "glm-5.3", Backend: tc.backend, Effort: tc.effort}), ErrLunaRouteAxisUnsupported)
	}
	requireRefusal(t, CheckLunaRouteRoute(fakeLRCaps{}, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3", Effort: EffortLow}), ErrLunaRouteAxisUnsupported)
	r := requireRefusal(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3", Effort: "turbo"}), ErrInvalidEffort)
	assert.Contains(t, r.Error(), "turbo")
}

func TestCheckLunaRouteRoute_ExactEffortContract(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, LunaRouteBackends...)
	all := []Effort{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
	exact := map[string][]Effort{
		LunaRouteChatAxis: {EffortNone, EffortLow, EffortMedium, EffortHigh, EffortMax},
		"claude-code":     all,
		"codex":           {EffortLow, EffortMedium, EffortHigh, EffortXHigh},
		"pi":              all,
		"opencode":        {},
	}
	for axis, allowed := range exact {
		t.Run(axis, func(t *testing.T) {
			glm := fullGLM()
			glm.Efforts[axis] = EffortFact{Known: true, Levels: all, Evidence: "ev#all-efforts"}
			caps := fakeLRCaps{"glm-5.3": glm}
			route := agentRoute("glm-5.3", axis)
			if axis == LunaRouteChatAxis {
				route.Kind, route.Backend = ProfileKindChat, ""
			}
			for _, effort := range append([]Effort{"", EffortDefault}, all...) {
				route.Effort = effort
				before := route
				err := CheckLunaRouteRoute(caps, route)
				want := effort == "" || effort == EffortDefault
				for _, e := range allowed {
					want = want || e == effort
				}
				if want {
					require.NoError(t, err, "%s/%s", axis, effort)
				} else {
					requireRefusal(t, err, ErrLunaRouteAxisUnsupported)
				}
				assert.Equal(t, before, route, "no flooring or substitution")
			}
		})
	}
}

func TestCheckLunaRouteRoute_RequiredLimits(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "claude-code", "opencode")
	for _, backend := range []string{"claude-code", "opencode"} {
		t.Run(backend, func(t *testing.T) {
			for _, missing := range []string{"context", "output", "both"} {
				glm := fullGLM()
				if missing != "output" {
					glm.ContextTokens = LimitFact{}
				}
				if missing != "context" {
					glm.OutputTokens = LimitFact{}
				}
				r := requireRefusal(t, CheckLunaRouteRoute(fakeLRCaps{"glm-5.3": glm}, agentRoute("glm-5.3", backend)), ErrLunaRouteLimitsUnknown)
				assert.Contains(t, r.Error(), backend+" on LunaRoute needs known context and output limits")
			}
			requireRefusal(t, CheckLunaRouteRoute(fakeLRCaps{}, agentRoute("glm-5.3", backend)), ErrLunaRouteLimitsUnknown)
		})
	}
}

func TestCheckLunaRouteRoute_OutputTokenConflict(t *testing.T) {
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	r := requireRefusal(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3", MaxOutputTokens: 32769}), ErrLunaRouteSettingConflict)
	assert.Contains(t, r.Error(), "32769")
	assert.Contains(t, r.Error(), "32768")
	for _, max := range []int{0, 1, 32768} {
		require.NoError(t, CheckLunaRouteRoute(caps, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3", MaxOutputTokens: max}))
	}
	require.NoError(t, CheckLunaRouteRoute(fakeLRCaps{}, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3", MaxOutputTokens: 1 << 20}))
}

func TestCheckLunaRouteRoute_RequestSettings(t *testing.T) {
	for _, setting := range []string{"temperature", "output_schema"} {
		t.Run(setting, func(t *testing.T) {
			glm := fullGLM()
			glm.RequestSettings = map[string]SupportFact{setting: {Support: SupportUnsupported, Evidence: "ev#" + setting}}
			caps := fakeLRCaps{"glm-5.3": glm}
			route := LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3"}
			require.NoError(t, CheckLunaRouteRoute(caps, route), "a setting not requested does not conflict")
			route.RequestedSettings = []string{setting}
			r := requireRefusal(t, CheckLunaRouteRoute(caps, route), ErrLunaRouteSettingConflict)
			assert.Contains(t, r.Error(), "ev#"+setting)
			assert.Contains(t, r.Error(), setting)
			for _, support := range []Support{SupportUnknown, SupportSupported} {
				glm.RequestSettings[setting] = SupportFact{Support: support, Evidence: "ev#permitted"}
				require.NoError(t, CheckLunaRouteRoute(caps, route))
			}
			require.NoError(t, CheckLunaRouteRoute(fakeLRCaps{}, route), "absent fact is unknown")
		})
	}
}

func TestCheckLunaRouteRoute_PiReasoningIndependent(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "pi")
	for _, support := range []Support{"", SupportUnknown, SupportUnsupported, SupportSupported} {
		glm := fullGLM()
		glm.Reasoning = SupportFact{Support: support, Evidence: "ev#reasoning"}
		glm.Efforts = nil
		require.NoError(t, CheckLunaRouteRoute(fakeLRCaps{"glm-5.3": glm}, agentRoute("glm-5.3", "pi")), "phase 1 does not require pi metadata or infer effort from reasoning")
	}
}

func TestCheckLunaRouteRoute_NilCapsIsWiringFault(t *testing.T) {
	require.ErrorIs(t, CheckLunaRouteRoute(nil, LunaRouteRoute{Kind: ProfileKindChat, Model: "glm-5.3"}), ErrSelectionPortsMissing)
}

func TestCheckLunaRouteRoute_InvalidKind(t *testing.T) {
	r := requireRefusal(t, CheckLunaRouteRoute(fakeLRCaps{}, LunaRouteRoute{Kind: "embedding", Model: "glm-5.3"}), ErrLunaRouteIncompatible)
	assert.Contains(t, r.Error(), "embedding")
}

func TestCheckLunaRouteLaunch(t *testing.T) {
	DisableLunaRouteBackendsForTest(t, "codex")
	EnableLunaRouteBackendsForTest(t, "opencode")
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	require.NoError(t, CheckLunaRouteLaunch(nil, ResolvedBinding{Provider: "openrouter", Model: "anything", BaseURL: "https://other.example"}, 0))
	b := ResolvedBinding{BackendExplicit: true, Kind: ProfileKindAgent, Provider: " LuNaRoUtE ", BaseURL: "https://gw.lunaroute.com/v1/", Model: "glm-5.3", Backend: " opencode "}
	before := b
	require.NoError(t, CheckLunaRouteLaunch(caps, b, 0))
	assert.Equal(t, before, b)
	b.BaseURL = "https://evil.example"
	require.ErrorIs(t, CheckLunaRouteLaunch(caps, b, 0), ErrLunaRouteEndpoint)
	chat := ResolvedBinding{Kind: ProfileKindChat, Provider: "lunaroute", Model: "glm-5.3", Backend: "ignored-for-chat"}
	require.NoError(t, CheckLunaRouteLaunch(caps, chat, 0))
	requireRefusal(t, CheckLunaRouteLaunch(caps, chat, 999999), ErrLunaRouteSettingConflict)
	b = ResolvedBinding{BackendExplicit: true, Kind: ProfileKindAgent, Provider: "lunaroute", Model: "glm-5.3", Backend: "codex"}
	requireRefusal(t, CheckLunaRouteLaunch(caps, b, 0), ErrLunaRouteBackendUnsupported)
}

func TestCheckLunaRouteAdapterLaunch_SkipsOnlyEnablement(t *testing.T) {
	DisableLunaRouteBackendsForTest(t, LunaRouteBackends...)
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	b := ResolvedBinding{BackendExplicit: true, Kind: ProfileKindAgent, Provider: "lunaroute", Model: "glm-5.3", Backend: "opencode"}
	requireRefusal(t, CheckLunaRouteLaunch(caps, b, 0), ErrLunaRouteBackendUnsupported)
	requireRefusal(t, CheckLunaRouteRoute(caps, agentRoute(b.Model, b.Backend)), ErrLunaRouteBackendUnsupported)
	for _, backend := range LunaRouteBackends {
		b.Backend = backend
		require.NoError(t, CheckLunaRouteAdapterLaunch(caps, b, 0))
	}
	for _, check := range []func(LunaRouteCapabilities, ResolvedBinding, int) error{CheckLunaRouteLaunch, CheckLunaRouteAdapterLaunch} {
		b.Backend = "cursor"
		requireRefusal(t, check(caps, b, 0), ErrLunaRouteBackendUnsupported)
		b.Backend = "opencode"
		b.BackendExplicit = false
		requireRefusal(t, check(caps, b, 0), ErrLunaRouteBackendRequired)
		b.BackendExplicit = true
		b.Backend = ""
		requireRefusal(t, check(caps, b, 0), ErrLunaRouteBackendRequired)
		b.Backend = "opencode"
		b.BaseURL = "https://evil.example"
		require.ErrorIs(t, check(caps, b, 0), ErrLunaRouteEndpoint)
		b.BaseURL = ""
		require.ErrorIs(t, check(nil, b, 0), ErrSelectionPortsMissing)
		require.NoError(t, check(nil, ResolvedBinding{Provider: "openai", Model: "anything"}, 0))
	}
	// A disabled path still has to pass every non-enablement adapter check.
	b.Backend, b.BackendExplicit = "opencode", true
	bad := b
	bad.Model = "glm-999.9"
	requireRefusal(t, CheckLunaRouteAdapterLaunch(caps, bad, 0), ErrLunaRouteModelSpelling)
	requireRefusal(t, CheckLunaRouteLaunch(caps, bad, 0), ErrLunaRouteBackendUnsupported)
	requireRefusal(t, CheckLunaRouteAdapterLaunch(caps, b, 32769), ErrLunaRouteSettingConflict)
	requireRefusal(t, CheckLunaRouteLaunch(caps, b, 32769), ErrLunaRouteBackendUnsupported)
	// Once enabled, both APIs must apply identical identity, capability and axis checks.
	EnableLunaRouteBackendsForTest(t, "opencode")
	b.Backend, b.BackendExplicit = "opencode", true
	for _, check := range []func(LunaRouteCapabilities, ResolvedBinding, int) error{CheckLunaRouteLaunch, CheckLunaRouteAdapterLaunch} {
		bad := b
		bad.Model = "glm-999.9"
		requireRefusal(t, check(caps, bad, 0), ErrLunaRouteModelSpelling)
		bad = b
		bad.ContextWindow = Context1M
		requireRefusal(t, check(caps, bad, 0), ErrLunaRouteAxisUnsupported)
		bad = b
		bad.Effort = EffortNone
		requireRefusal(t, check(caps, bad, 0), ErrLunaRouteAxisUnsupported)
		requireRefusal(t, check(fakeLRCaps{}, b, 0), ErrLunaRouteLimitsUnknown)
		requireRefusal(t, check(caps, b, 32769), ErrLunaRouteSettingConflict)
		glm := fullGLM()
		glm.Backends = map[string]SupportFact{"opencode": {Support: SupportUnsupported, Evidence: "ev#unsupported"}}
		requireRefusal(t, check(fakeLRCaps{"glm-5.3": glm}, b, 0), ErrLunaRouteIncompatible)
	}
}

func TestCheckLunaRouteRequest_FeedsSettingsAndAxes(t *testing.T) {
	glm := fullGLM()
	glm.RequestSettings = map[string]SupportFact{"output_schema": {Support: SupportUnsupported, Evidence: "ev#schema"}}
	caps := fakeLRCaps{"glm-5.3": glm}
	b := ResolvedBinding{Provider: "lunaroute", Kind: ProfileKindChat, Model: "glm-5.3"}
	require.NoError(t, CheckLunaRouteLaunch(caps, b, 0))
	r := requireRefusal(t, CheckLunaRouteRequest(caps, b, 0, []string{"output_schema"}), ErrLunaRouteSettingConflict)
	assert.Contains(t, r.Error(), "ev#schema")
	require.NoError(t, CheckLunaRouteRequest(caps, b, 0, []string{"temperature"}))
	b.Effort = EffortXHigh
	requireRefusal(t, CheckLunaRouteRequest(caps, b, 0, nil), ErrLunaRouteAxisUnsupported)
	b.Effort, b.ContextWindow = EffortDefault, Context200K
	requireRefusal(t, CheckLunaRouteRequest(caps, b, 0, nil), ErrLunaRouteAxisUnsupported)
}

type lrCapsSpy func(string) (LunaRouteModel, bool)

func (f lrCapsSpy) LunaRouteModel(id string) (LunaRouteModel, bool) { return f(id) }

func TestCheckLunaRouteRoute_ValidationOrder(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "opencode")
	noLookup := lrCapsSpy(func(id string) (LunaRouteModel, bool) {
		t.Fatalf("capability lookup for %q before identity proof", id)
		return LunaRouteModel{}, false
	})
	requireRefusal(t, CheckLunaRouteRoute(noLookup, agentRoute("glm-999.9", "opencode")), ErrLunaRouteModelSpelling)
	glm := fullGLM()
	glm.Tasks = map[ProfileKind]SupportFact{ProfileKindAgent: {Support: SupportUnsupported, Evidence: "ev#task"}}
	glm.Backends = map[string]SupportFact{"opencode": {Support: SupportUnsupported, Evidence: "ev#backend"}}
	lookups := 0
	caps := lrCapsSpy(func(id string) (LunaRouteModel, bool) {
		require.Equal(t, "glm-5.3", id)
		lookups++
		return glm, true
	})
	route := agentRoute("glm-5.3", "opencode")
	route.ContextWindow, route.Effort, route.MaxOutputTokens = Context1M, EffortLow, 32769
	r := requireRefusal(t, CheckLunaRouteRoute(caps, route), ErrLunaRouteIncompatible)
	assert.Contains(t, r.Error(), "ev#task")
	delete(glm.Tasks, ProfileKindAgent)
	r = requireRefusal(t, CheckLunaRouteRoute(caps, route), ErrLunaRouteIncompatible)
	assert.Contains(t, r.Error(), "ev#backend")
	delete(glm.Backends, "opencode")
	r = requireRefusal(t, CheckLunaRouteRoute(caps, route), ErrLunaRouteAxisUnsupported)
	assert.Contains(t, r.Error(), "context")
	route.ContextWindow = ContextDefault
	glm.ContextTokens = LimitFact{}
	r = requireRefusal(t, CheckLunaRouteRoute(caps, route), ErrLunaRouteAxisUnsupported)
	assert.Contains(t, r.Error(), "effort")
	route.Effort = EffortDefault
	requireRefusal(t, CheckLunaRouteRoute(caps, route), ErrLunaRouteLimitsUnknown)
	glm.ContextTokens = LimitFact{Tokens: 200000, Evidence: "ev#context"}
	requireRefusal(t, CheckLunaRouteRoute(caps, route), ErrLunaRouteSettingConflict)
	assert.Equal(t, 6, lookups, "each proved route reads its exact model facts")
}

type lrVersionSpy func(context.Context, VersionRequest) (string, error)

func (f lrVersionSpy) SpecificVersion(ctx context.Context, r VersionRequest) (string, error) {
	return f(ctx, r)
}

func TestCheckLunaRouteIdentity_GrammarBeforeFamily(t *testing.T) {
	for _, grammarFails := range []bool{false, true} {
		t.Run(map[bool]string{true: "grammar", false: "family"}[grammarFails], func(t *testing.T) {
			var calls []string
			versions := lrVersionSpy(func(ctx context.Context, r VersionRequest) (string, error) {
				require.NoError(t, ctx.Err())
				assert.Equal(t, VersionRequest{Model: "reviewed-opaque", RouteProvider: CanonicalLunaRoute, ConnectionRef: "lunaroute"}, r)
				calls = append(calls, "version")
				if grammarFails {
					return "", ErrSpecificVersionUnavailable
				}
				return r.Model, nil
			})
			family := func(route, model string) modelidentity.Family {
				assert.Equal(t, CanonicalLunaRoute, route)
				assert.Equal(t, "reviewed-opaque", model)
				calls = append(calls, "family")
				return modelidentity.FamilyUnresolved
			}
			err := checkLunaRouteIdentity("reviewed-opaque", versions, family)
			if grammarFails {
				require.ErrorIs(t, err, ErrLunaRouteModelSpelling)
				assert.Equal(t, []string{"version"}, calls)
			} else {
				require.ErrorIs(t, err, ErrLunaRouteFamilyUnresolved)
				assert.False(t, errors.Is(err, ErrLunaRouteModelSpelling))
				assert.Equal(t, []string{"version", "family"}, calls)
			}
		})
	}
	require.NoError(t, checkLunaRouteIdentity("glm-5.3", modelidentity.NewVersionResolver(), modelidentity.ClassifyFamily))
}
