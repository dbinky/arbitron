package claudecli

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrTestKey = "lr_live_claude_Secret123"

func lrModel(id string) provider.LunaRouteModel {
	return provider.LunaRouteModel{ID: id,
		ContextTokens: provider.LimitFact{Tokens: 202752, Evidence: "ev#ctx"},
		OutputTokens:  provider.LimitFact{Tokens: 32768, Evidence: "ev#out"},
		Efforts:       map[string]provider.EffortFact{"claude-code": {Known: true, Levels: []provider.Effort{provider.EffortNone, provider.EffortHigh, provider.EffortXHigh}, Evidence: "ev#eff"}}}
}
func lrRules(models ...provider.LunaRouteModel) provider.CapabilityRules {
	table := map[string]provider.LunaRouteModel{}
	for _, m := range models {
		table[m.ID] = m
	}
	return testCapRules().WithLunaRouteModels(table)
}
func lrBinding(model, key string, revealErr error, calls *int) provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "lunaroute", Backend: "claude-code", BackendExplicit: true, BaseURL: "https://gw.lunaroute.com/", Model: model,
		RevealKey: func(context.Context) (string, error) { *calls++; return key, revealErr }}
}
func countVar(env []string, name string) int {
	n := 0
	for _, s := range env {
		if strings.HasPrefix(s, name+"=") {
			n++
		}
	}
	return n
}
func assertOnce(t *testing.T, env []string, name, want string) {
	t.Helper()
	require.Equal(t, 1, countVar(env, name), name)
	assertEnv(t, env, name, want)
}

func TestLunaRouteAgentEnv_HappyPath(t *testing.T) {
	calls := 0
	env, err := agentEnvWithRules(context.Background(), lrBinding("glm-5.3", lrTestKey, nil, &calls), lrRules(lrModel("glm-5.3")))
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assertOnce(t, env, "ANTHROPIC_BASE_URL", "https://gw.lunaroute.com")
	assertOnce(t, env, "ANTHROPIC_AUTH_TOKEN", lrTestKey)
	assert.Zero(t, countVar(env, "ANTHROPIC_API_KEY"))
	for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		assertOnce(t, env, name, "glm-5.3")
	}
	assertOnce(t, env, "CLAUDE_CODE_GATEWAY_HINT_HEADERS", "1")
	assertOnce(t, env, "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "202752")
	assertOnce(t, env, "CLAUDE_CODE_MAX_OUTPUT_TOKENS", "32768")
	for _, name := range []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_DISABLE_1M_CONTEXT", "CLAUDE_CODE_EFFORT_LEVEL", "MAX_THINKING_TOKENS"} {
		assert.Zero(t, countVar(env, name))
	}
	for _, s := range env {
		if strings.Contains(s, lrTestKey) {
			assert.True(t, strings.HasPrefix(s, "ANTHROPIC_AUTH_TOKEN="))
		}
	}
}
func TestLunaRouteAgentEnv_EffortExact(t *testing.T) {
	for _, tc := range []struct {
		effort      provider.Effort
		name, value string
	}{{provider.EffortHigh, "CLAUDE_CODE_EFFORT_LEVEL", "high"}, {provider.EffortXHigh, "CLAUDE_CODE_EFFORT_LEVEL", "xhigh"}, {provider.EffortNone, "MAX_THINKING_TOKENS", "0"}} {
		calls := 0
		b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
		b.Effort = tc.effort
		env, err := agentEnvWithRules(context.Background(), b, lrRules(lrModel(b.Model)))
		require.NoError(t, err)
		assertOnce(t, env, tc.name, tc.value)
	}
	for _, effort := range []provider.Effort{provider.EffortMedium, "turbo"} {
		calls := 0
		b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
		b.Effort = effort
		_, err := agentEnvWithRules(context.Background(), b, lrRules(lrModel(b.Model)))
		require.Error(t, err)
		assert.Zero(t, calls)
		if effort == "turbo" {
			assert.ErrorIs(t, err, provider.ErrInvalidEffort)
		} else {
			assert.ErrorIs(t, err, provider.ErrLunaRouteAxisUnsupported)
		}
	}
}
func TestLunaRouteAgentEnv_FailsClosedBeforeReveal(t *testing.T) {
	noOut := lrModel("glm-5.3")
	noOut.OutputTokens = provider.LimitFact{}
	noCtx := lrModel("glm-5.3")
	noCtx.ContextTokens = provider.LimitFact{}
	for _, tc := range []struct {
		name  string
		rules provider.CapabilityRules
		mut   func(*provider.ResolvedBinding)
		want  error
	}{
		{"absent", lrRules(), nil, provider.ErrLunaRouteLimitsUnknown},
		{"output", lrRules(noOut), nil, provider.ErrLunaRouteLimitsUnknown},
		{"context", lrRules(noCtx), nil, provider.ErrLunaRouteLimitsUnknown},
		{"empty", provider.CapabilityRules{}, nil, provider.ErrLunaRouteLimitsUnknown},
		{"tier", lrRules(), func(b *provider.ResolvedBinding) { b.ContextWindow = provider.Context1M }, provider.ErrLunaRouteAxisUnsupported},
		{"endpoint first", lrRules(), func(b *provider.ResolvedBinding) {
			b.BaseURL = "https://evil.example"
			b.ContextWindow = provider.Context200K
		}, provider.ErrLunaRouteEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
			if tc.mut != nil {
				tc.mut(&b)
			}
			env, err := agentEnvWithRules(context.Background(), b, tc.rules)
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, env)
			assert.Zero(t, calls)
		})
	}
}
func TestLunaRouteAgentEnv_CredentialErrors(t *testing.T) {
	boom := errors.New("decrypt failed")
	for _, tc := range []struct {
		key          string
		reveal, want error
	}{{"sk-ant-foreign", nil, provider.ErrLunaRouteStoredCredential}, {"", nil, provider.ErrLunaRouteStoredCredential}, {"", boom, boom}} {
		calls := 0
		env, err := agentEnvWithRules(context.Background(), lrBinding("glm-5.3", tc.key, tc.reveal, &calls), lrRules(lrModel("glm-5.3")))
		require.ErrorIs(t, err, tc.want)
		assert.Nil(t, env)
		assert.NotContains(t, err.Error(), "sk-ant-foreign")
	}
	calls := 0
	b := lrBinding("glm-5.3", "", nil, &calls)
	b.RevealKey = nil
	_, err := agentEnvWithRules(context.Background(), b, lrRules(lrModel(b.Model)))
	require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
}
func TestLunaRouteAgentEnv_OverridesAmbientVars(t *testing.T) {
	for _, name := range append(append([]string{}, lunaRouteModelSelectorVars...), "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_DISABLE_1M_CONTEXT", "CLAUDE_CODE_EFFORT_LEVEL", "MAX_THINKING_TOKENS", "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "CLAUDE_CODE_MAX_OUTPUT_TOKENS") {
		t.Setenv(name, "ambient-conflict")
	}
	calls := 0
	env, err := agentEnvWithRules(context.Background(), lrBinding("glm-5.3-background", lrTestKey, nil, &calls), lrRules(lrModel("glm-5.3-background")))
	require.NoError(t, err)
	for _, name := range lunaRouteModelSelectorVars {
		assertOnce(t, env, name, "glm-5.3-background")
	}
	assertOnce(t, env, "ANTHROPIC_BASE_URL", "https://gw.lunaroute.com")
	assert.NotContains(t, strings.Join(env, "\n"), "ambient-conflict")
}
func TestAgentEnv_NonLunaRouteGolden(t *testing.T) {
	rules := testCapRules()
	b := gatewayBinding("https://litellm.example/v1", "sk-litellm", nil)
	b.ContextWindow, b.Effort = provider.Context200K, provider.EffortHigh
	got, err := agentEnvWithRules(context.Background(), b, rules)
	require.NoError(t, err)
	want := procenv.RemoveVar(procenv.BaseEnv(), "ANTHROPIC_BASE_URL")
	want = append(want, "ANTHROPIC_BASE_URL=https://litellm.example/v1", "ANTHROPIC_API_KEY=sk-litellm", "CLAUDE_CODE_DISABLE_1M_CONTEXT=1", "CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000", "CLAUDE_CODE_EFFORT_LEVEL=high")
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)
}
func TestAgentAdapter_LunaRouteEnvReachesRunSpec(t *testing.T) {
	fr := &fakeRunner{lines: []string{`{"type":"result","result":"done","session_id":"s1","modelUsage":{"glm-5.3":{}}}`}}
	calls := 0
	a := NewAgentAdapterForBinding(Config{BinaryPath: "claude", Runner: fr, CapabilityRules: lrRules(lrModel("glm-5.3"))}, lrBinding("glm-5.3", lrTestKey, nil, &calls))
	res, err := a.RunAgent(context.Background(), engine.AgentMission{Prompt: "nonce:mission", WorkDir: "/tmp/ws", ToolScope: []string{"Read", "Write"}})
	require.NoError(t, err)
	assert.Equal(t, "glm-5.3", res.Model)
	assert.Equal(t, "done", res.Summary)
	assertOnce(t, fr.gotSpec.Env, "CLAUDE_CODE_MAX_OUTPUT_TOKENS", "32768")
	assert.Equal(t, "nonce:mission", fr.gotSpec.Stdin)
	assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
	model, ok := argValue(fr.gotSpec.Args, "--model")
	require.True(t, ok)
	assert.Equal(t, "glm-5.3", model)
	tools, ok := argValue(fr.gotSpec.Args, "--allowedTools")
	require.True(t, ok)
	assert.Equal(t, "Read,Write", tools)
	assert.Contains(t, fr.gotSpec.Args, "--dangerously-skip-permissions")
	assert.NotContains(t, strings.Join(fr.gotSpec.Args, " "), lrTestKey)
	require.Len(t, res.ModelEvidence, 1)
	assert.Equal(t, "glm-5.3", res.ModelEvidence[0].Model)
	assert.Equal(t, "claude_model_usage", res.ModelEvidence[0].Source)
	assert.Empty(t, res.ModelEvidence[0].Provider)
}
