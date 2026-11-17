package opencode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrKey = "lr_live_opencode_Secret987"

func lrOCBinding(base, model, key string, calls *int) provider.ResolvedBinding {
	return provider.ResolvedBinding{BackendExplicit: true, Kind: provider.ProfileKindAgent, Provider: "lunaroute", Backend: "opencode", BaseURL: base, Model: model,
		RevealKey: func(context.Context) (string, error) { *calls++; return key, nil }}
}

func lrOCRules() provider.CapabilityRules {
	lim := func(c, o int) provider.LunaRouteModel {
		return provider.LunaRouteModel{ContextTokens: provider.LimitFact{Tokens: c, Evidence: "ev#ctx"}, OutputTokens: provider.LimitFact{Tokens: o, Evidence: "ev#out"}}
	}
	return provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": lim(202752, 32768), "glm-5.3-background": lim(202752, 32768)})
}

func TestLunaRouteDescriptor(t *testing.T) {
	assert.Equal(t, providerDescriptor{opencodeID: "lunaroute", credEnvVar: "LUNAROUTE_API_KEY", npmPackage: "@ai-sdk/openai-compatible", alwaysConfig: true}, mustDesc(t, " LunaRoute "))
	require.NoError(t, validateDescriptors())
}

func TestBuildConfig_LunaRouteDerivesV1AndRegistersModel(t *testing.T) {
	for _, base := range []string{"", "https://gw.lunaroute.com", "https://gw.lunaroute.com/", "https://gw.lunaroute.com/v1"} {
		calls := 0
		out, _, err := buildConfig(mustDesc(t, "lunaroute"), lrOCBinding(base, "glm-5.3", lrKey, &calls), Posture{}, lrOCRules())
		require.NoError(t, err)
		assert.Equal(t, `{"$schema":"https://opencode.ai/config.json","provider":{"lunaroute":{"npm":"@ai-sdk/openai-compatible","options":{"apiKey":"{env:LUNAROUTE_API_KEY}","baseURL":"https://gw.lunaroute.com/v1"},"models":{"glm-5.3":{"limit":{"context":202752,"output":32768},"name":"glm-5.3","tool_call":true}}}},"permission":{"*":"deny"},"model":"lunaroute/glm-5.3","small_model":"lunaroute/glm-5.3","enabled_providers":["lunaroute"]}`, out)
		assert.Zero(t, calls)
	}
}

func TestBuildConfig_LunaRouteRefusalsAndPrefix(t *testing.T) {
	calls := 0
	d := mustDesc(t, "lunaroute")
	for _, rules := range []provider.CapabilityRules{{}, provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": {ContextTokens: provider.LimitFact{Tokens: 10, Evidence: "ev"}}})} {
		_, _, err := buildConfig(d, lrOCBinding("", "glm-5.3", lrKey, &calls), Posture{}, rules)
		require.ErrorIs(t, err, provider.ErrLunaRouteLimitsUnknown)
	}
	_, _, err := buildConfig(d, lrOCBinding("https://evil.example/v1", "glm-5.3", lrKey, &calls), Posture{}, lrOCRules())
	require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
	_, _, err = buildConfig(d, lrOCBinding("", " ", lrKey, &calls), Posture{}, lrOCRules())
	require.ErrorIs(t, err, ErrModelRequired)
	out, _, err := buildConfig(d, lrOCBinding("", "lunaroute/glm-5.3-background", lrKey, &calls), Posture{AllowAll: true}, lrOCRules())
	require.NoError(t, err)
	m := decodeConfig(t, out)
	assert.Equal(t, "lunaroute/glm-5.3-background", m["small_model"])
	assert.Contains(t, m["provider"].(map[string]any)["lunaroute"].(map[string]any)["models"], "glm-5.3-background")
	assert.Zero(t, calls)
}

func TestBuildConfig_NonLunaRouteGolden(t *testing.T) {
	for _, tc := range []struct {
		b    provider.ResolvedBinding
		p    Posture
		want string
	}{
		{provider.ResolvedBinding{Provider: "litellm", Model: "gpt-4o", BaseURL: "https://gw.example/v1"}, Posture{AllowAll: true}, `{"$schema":"https://opencode.ai/config.json","provider":{"litellm":{"npm":"@ai-sdk/openai-compatible","options":{"apiKey":"{env:OPENAI_API_KEY}","baseURL":"https://gw.example/v1"}}},"permission":{"*":"allow"}}`},
		{provider.ResolvedBinding{Provider: "ollama-cloud", Model: "deepseek-v4-flash", BaseURL: "https://ollama.com/v1"}, Posture{}, `{"$schema":"https://opencode.ai/config.json","permission":{"*":"deny"}}`},
	} {
		out, _, err := buildConfig(mustDesc(t, tc.b.Provider), tc.b, tc.p, provider.CapabilityRules{})
		require.NoError(t, err)
		assert.Equal(t, tc.want, out)
	}
}

func TestBuildArgs_LunaRoutePureAndPrefix(t *testing.T) {
	calls := 0
	for _, model := range []string{"glm-5.3", "lunaroute/glm-5.3"} {
		args, err := buildArgs(engine.AgentMission{Prompt: "fix it"}, lrOCBinding("", model, lrKey, &calls), mustDesc(t, "lunaroute"))
		require.NoError(t, err)
		assert.Equal(t, []string{"run", "fix it", "-m", "lunaroute/glm-5.3", "--dangerously-skip-permissions", "--format", "json", "--pure"}, args)
	}
	args, err := buildArgs(engine.AgentMission{Prompt: "p"}, provider.ResolvedBinding{Provider: "openrouter", Model: "meta-llama/llama-4"}, mustDesc(t, "openrouter"))
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "p", "-m", "openrouter/meta-llama/llama-4", "--dangerously-skip-permissions", "--format", "json"}, args)
}

func TestOpencodeEnv_LunaRouteGuardedKeyOnlyInEnv(t *testing.T) {
	calls := 0
	d := mustDesc(t, "lunaroute")
	env, err := opencodeEnv(context.Background(), d, lrOCBinding("", "glm-5.3", lrKey, &calls))
	require.NoError(t, err)
	assert.Contains(t, env, "LUNAROUTE_API_KEY="+lrKey)
	n := 0
	for _, v := range env {
		if strings.Contains(v, lrKey) {
			n++
		}
	}
	assert.Equal(t, 1, n)
	_, err = opencodeEnv(context.Background(), d, lrOCBinding("", "glm-5.3", "sk-or-v1-foreign", &calls))
	require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
	assert.NotContains(t, err.Error(), "sk-or-v1-foreign")
	_, err = opencodeEnv(context.Background(), d, lrOCBinding("https://evil.example", "glm-5.3", lrKey, &calls))
	require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
}

func lrRunScripts() []runScript {
	return []runScript{{lines: []string{"opencode 1.19.5"}}, {lines: []string{exactRunEvent}}, {lines: []string{modelExport("ses_run", "msg_answer", "assistant", "lunaroute", "glm-5.3")}}}
}

func TestRunAgent_LunaRouteHappyPathSpec(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		calls := 0
		b := lrOCBinding("https://gw.lunaroute.com", "glm-5.3", lrKey, &calls)
		b.Pinned = pinned
		scripts := lrRunScripts()
		scripts[1].lines = append(scripts[1].lines, `{"type":"tool_use","sessionID":"ses_run","part":{"sessionID":"ses_run","messageID":"msg_answer","type":"tool","tool":"bash","state":{"status":"completed"}}}`)
		r := &multiRunner{scripts: scripts}
		res, err := New(Config{Binding: b, CapabilityRules: lrOCRules(), BinaryPath: "opencode", Runner: r}).RunAgent(context.Background(), engine.AgentMission{Prompt: "p", WorkDir: t.TempDir()})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, "glm-5.3", res.Model)
		assert.Equal(t, "verified output", res.Summary)
		assert.Equal(t, "ses_run", res.SessionID, "retain the current verified export session for helper correlation")
		assert.Contains(t, res.Steps, engine.AgentStep{Kind: "tool_call", Summary: "bash"})
		assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Provider: "lunaroute", Source: "opencode_export", Kind: engine.EvidenceRoutingIntent}}, res.ModelEvidence)
		require.Len(t, r.calls, 3)
		run := r.calls[1]
		assert.Contains(t, run.Args, "lunaroute/glm-5.3")
		cfg, ok := envValue(run.Env, "OPENCODE_CONFIG_CONTENT")
		require.True(t, ok)
		assert.Contains(t, cfg, `"context":202752`)
		assert.NotContains(t, cfg, lrKey)
		assert.NotContains(t, strings.Join(run.Args, " "), lrKey)
		assert.Equal(t, run.Env, r.calls[2].Env)
		assert.Equal(t, run.Dir, r.calls[2].Dir)
		assert.Equal(t, []string{"export", "ses_run", "--pure"}, r.calls[2].Args)
		assert.Equal(t, 1, calls)
	}
}

func TestRunAgent_LunaRouteRefusesBeforeIO(t *testing.T) {
	for _, name := range []string{"mission mismatch", "unknown limits", "half limits", "endpoint", "context", "unreviewed", "prefixed", "background", "unsupported task", "unsupported backend", "effort none", "effort low", "effort max"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			b := lrOCBinding("", "glm-5.3", lrKey, &calls)
			rules := lrOCRules()
			model := b.Model
			want := provider.ErrLunaRouteAxisUnsupported
			switch name {
			case "mission mismatch":
				model = "deepseek-4.1-flash"
				want = ErrModelExecutionEvidence
			case "unknown limits":
				rules = provider.CapabilityRules{}
				want = provider.ErrLunaRouteLimitsUnknown
			case "half limits":
				rules = provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": {OutputTokens: provider.LimitFact{Tokens: 100, Evidence: "ev"}}})
				want = provider.ErrLunaRouteLimitsUnknown
			case "endpoint":
				b.BaseURL = "https://evil.example"
				want = provider.ErrLunaRouteEndpoint
			case "context":
				b.ContextWindow = provider.ContextWindow("1m")
			case "unreviewed":
				b.Model = "glm-999.9"
				model = b.Model
				want = provider.ErrLunaRouteModelSpelling
			case "prefixed":
				b.Model = "lunaroute/glm-5.3"
				model = b.Model
				want = provider.ErrLunaRouteModelSpelling
			case "background":
				b.Model = "glm-5.3-background"
				model = b.Model
				want = provider.ErrLunaRouteModelSpelling
			case "unsupported task", "unsupported backend":
				m, _ := rules.LunaRouteModel(b.Model)
				if name == "unsupported task" {
					m.Tasks = map[provider.ProfileKind]provider.SupportFact{provider.ProfileKindAgent: {Support: provider.SupportUnsupported, Evidence: "ev"}}
				} else {
					m.Backends = map[string]provider.SupportFact{"opencode": {Support: provider.SupportUnsupported, Evidence: "ev"}}
				}
				rules = rules.WithLunaRouteModels(map[string]provider.LunaRouteModel{b.Model: m})
				want = provider.ErrLunaRouteIncompatible
			default:
				b.Effort = provider.Effort(strings.TrimPrefix(name, "effort "))
			}
			r := &multiRunner{}
			_, err := New(Config{Binding: b, CapabilityRules: rules, Runner: r}).RunAgent(context.Background(), engine.AgentMission{Prompt: "p", WorkDir: t.TempDir(), Model: model})
			require.ErrorIs(t, err, want)
			require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
			assert.Zero(t, calls)
			assert.Empty(t, r.calls)
		})
	}
}

func TestRunAgent_LunaRouteEvidenceRequiredAndSafe(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, reported := range []string{"", "glm-5.3-flash", "gpt-6.1-sol", "glm-5.3 ", "glm-5.3\n", strings.Repeat("g", 257), "glm-" + lrKey, "claude-" + lrKey} {
			calls := 0
			b := lrOCBinding("", "glm-5.3", lrKey, &calls)
			b.Pinned = pinned
			scripts := lrRunScripts()
			scripts[2].lines = []string{modelExport("ses_run", "msg_answer", "assistant", "lunaroute", reported)}
			r := &multiRunner{scripts: scripts}
			res, err := New(Config{Binding: b, CapabilityRules: lrOCRules(), Runner: r}).RunAgent(context.Background(), engine.AgentMission{WorkDir: t.TempDir(), Model: b.Model})
			require.ErrorIs(t, err, ErrModelExecutionEvidence)
			assert.Nil(t, res)
			assert.NotContains(t, err.Error(), lrKey)
		}
	}
	calls := 0
	b := lrOCBinding("", "glm-5.3", lrKey, &calls)
	for _, providerID := range []string{"", "openai", "lunaroute-" + lrKey} {
		scripts := lrRunScripts()
		scripts[2].lines = []string{modelExport("ses_run", "msg_answer", "assistant", providerID, b.Model)}
		_, err := New(Config{Binding: b, CapabilityRules: lrOCRules(), Runner: &multiRunner{scripts: scripts}}).RunAgent(context.Background(), engine.AgentMission{WorkDir: t.TempDir()})
		require.ErrorIs(t, err, ErrModelExecutionEvidence)
		assert.NotContains(t, err.Error(), lrKey)
	}
}

func TestRunAgent_LunaRouteMissingOrUncorrelatedEvidence(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, name := range []string{"missing run", "missing export", "other session", "other turn", "incomplete", "helper drift"} {
			t.Run(name+fmt.Sprint(pinned), func(t *testing.T) {
				calls := 0
				b := lrOCBinding("", "glm-5.3", lrKey, &calls)
				b.Pinned = pinned
				scripts := lrRunScripts()
				switch name {
				case "missing run":
					scripts[1].lines = nil
				case "missing export":
					scripts[2].lines = nil
				case "other session":
					scripts[2].lines = []string{modelExport("ses_other", "msg_answer", "assistant", "lunaroute", b.Model)}
				case "other turn":
					scripts[2].lines = []string{modelExport("ses_run", "msg_other", "assistant", "lunaroute", b.Model)}
				case "incomplete":
					scripts[2].lines[0] = strings.ReplaceAll(scripts[2].lines[0], `"completed":2`, `"completed":0`)
				case "helper drift":
					scripts[2].lines = []string{combineModelExports(t, scripts[2].lines[0], modelExport("ses_run", "msg_helper", "assistant", "lunaroute", "glm-5.3-flash"))}
				}
				res, err := New(Config{Binding: b, CapabilityRules: lrOCRules(), Runner: &multiRunner{scripts: scripts}}).RunAgent(context.Background(), engine.AgentMission{WorkDir: t.TempDir()})
				require.ErrorIs(t, err, ErrModelExecutionEvidence)
				assert.Nil(t, res)
			})
		}
	}
}

func TestRunAgent_LunaRouteErrorsSafeEvenUnpinned(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	bad := &claudecli.StderrError{Err: errors.New("exit status 1"), Summary: "bad key " + lrKey, Tail: []byte("bad key " + lrKey)}
	for _, stage := range []string{"start", "wait", "export start", "export wait", "export parse", "version start", "version wait", "version parse", "reveal", "config"} {
		t.Run(stage, func(t *testing.T) {
			logs.Reset()
			calls := 0
			b := lrOCBinding("", "glm-5.3", lrKey, &calls)
			scripts := lrRunScripts()
			switch stage {
			case "start":
				scripts[1].startErr = bad
			case "wait":
				scripts[1].waitErr = bad
			case "export start":
				scripts[2].startErr = bad
			case "export wait":
				scripts[2].waitErr = bad
			case "export parse":
				scripts[2].lines = []string{lrKey}
			case "version start":
				scripts[0].startErr = bad
			case "version wait":
				scripts[0].waitErr = bad
			case "version parse":
				scripts[0].lines = []string{lrKey}
			case "reveal":
				b.RevealKey = func(context.Context) (string, error) { return "", bad }
			case "config":
				b.AllowedTools = []string{"MultiEdit"}
			}
			_, err := New(Config{Binding: b, CapabilityRules: lrOCRules(), Runner: &multiRunner{scripts: scripts}}).RunAgent(context.Background(), engine.AgentMission{WorkDir: t.TempDir()})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), lrKey)
			assert.NotContains(t, logs.String(), lrKey)
			if strings.HasPrefix(stage, "version") || stage == "reveal" || stage == "config" {
				assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
			}
		})
	}
}

func TestProbeExecutedModel_LunaRouteExactlyOnePureAndFactsBeforeReveal(t *testing.T) {
	calls := 0
	b := lrOCBinding("", "glm-5.3", lrKey, &calls)
	for _, rules := range []provider.CapabilityRules{{}, lrOCRules()} {
		scripts := lrRunScripts()
		scripts = append(scripts[:1], append([]runScript{{lines: []string{`{"mcp":{}}`}}, {lines: []string{`{"mcp":{}}`}}}, scripts[1:]...)...)
		r := &multiRunner{scripts: scripts}
		model, session, err := New(Config{Binding: b, CapabilityRules: rules, Runner: r}).ProbeExecutedModel(context.Background(), "p", t.TempDir())
		if m, _ := rules.LunaRouteModel(b.Model); !m.ContextTokens.Known() {
			require.ErrorIs(t, err, provider.ErrLunaRouteLimitsUnknown)
			assert.Zero(t, calls)
			assert.Empty(t, r.calls)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, b.Model, model)
		assert.Equal(t, "ses_run", session)
		require.Len(t, r.calls, 5)
		for _, spec := range r.calls[1:] {
			n := 0
			for _, a := range spec.Args {
				if a == "--pure" {
					n++
				}
			}
			assert.Equal(t, 1, n)
			assert.True(t, slices.Contains(spec.Env, "LUNAROUTE_API_KEY="+lrKey))
		}
	}
}
