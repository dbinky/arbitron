package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCaps(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "capabilities.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

const lrOverride = `lunaroute_models:
  glm-5.3:
    tasks:
      chat: {support: supported, evidence: "ev#chat"}
      agent: {support: unknown}
    backends:
      opencode: {support: supported, evidence: "ev#oc"}
      codex: {support: unsupported, evidence: "ev#codex"}
    context_tokens: {tokens: 200000, evidence: "ev#ctx"}
    output_tokens: {tokens: 32768, evidence: "ev#out"}
    efforts:
      chat: {levels: [low, high], evidence: "ev#eff"}
      opencode: {levels: [], evidence: "ev#oc-eff"}
    reasoning: {support: unsupported, evidence: "ev#reasoning"}
    request_settings:
      temperature: {support: supported, evidence: "ev#temp"}
      output_schema: {support: unsupported, evidence: "ev#schema"}
`

func TestLoadCapabilityRules_LunaRouteSection(t *testing.T) {
	r, err := adapterconfig.LoadCapabilityRules(writeCaps(t, lrOverride))
	require.NoError(t, err)
	m, ok := r.LunaRouteModel("glm-5.3")
	require.True(t, ok)
	assert.Equal(t, "glm-5.3", m.ID)
	assert.Equal(t, provider.SupportSupported, m.Task(provider.ProfileKindChat).Support)
	assert.Equal(t, provider.SupportUnknown, m.Task(provider.ProfileKindAgent).Support)
	assert.Equal(t, provider.SupportSupported, m.Backend("opencode").Support)
	assert.Equal(t, provider.SupportUnsupported, m.Backend("codex").Support)
	assert.Equal(t, provider.SupportUnknown, m.Backend("pi").Support)
	assert.Equal(t, provider.LimitFact{Tokens: 200000, Evidence: "ev#ctx"}, m.ContextTokens)
	assert.Equal(t, provider.LimitFact{Tokens: 32768, Evidence: "ev#out"}, m.OutputTokens)
	assert.Equal(t, provider.EffortFact{Known: true, Levels: []provider.Effort{provider.EffortLow, provider.EffortHigh}, Evidence: "ev#eff"}, m.Effort("chat"))
	oc := m.Effort("opencode")
	assert.True(t, oc.Known)
	assert.Empty(t, oc.Levels)
	assert.False(t, m.Effort("claude-code").Known)
	assert.Equal(t, provider.SupportFact{Support: provider.SupportUnsupported, Evidence: "ev#reasoning"}, m.Reasoning)
	assert.Equal(t, provider.SupportFact{Support: provider.SupportSupported, Evidence: "ev#temp"}, m.RequestSettings["temperature"])
	assert.Equal(t, provider.SupportFact{Support: provider.SupportUnsupported, Evidence: "ev#schema"}, m.RequestSettings["output_schema"])
	assert.True(t, r.Has("claude-code"), "embedded backend kept")
}

func TestLoadCapabilityRules_EmbeddedLunaRouteReleaseSet(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "docs/evidence/lunaroute-provider/model-capabilities.md"))
	require.NoError(t, err, "production facts require the P1-T11 source note")
	for _, id := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash-1017", "claude-sonnet-5-lr1"} {
		assert.Contains(t, string(source), "\n## "+id+"\n", "even unknown facts need a reviewed per-model source anchor")
	}
	r, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	var ids []string
	for _, m := range r.LunaRouteModels() {
		ids = append(ids, m.ID)
		static := []string{m.ContextTokens.Evidence, m.OutputTokens.Evidence, m.Reasoning.Evidence}
		for _, f := range m.Efforts {
			static = append(static, f.Evidence)
		}
		for _, f := range m.RequestSettings {
			static = append(static, f.Evidence)
		}
		for _, ev := range static {
			if ev != "" {
				require.True(t, strings.HasPrefix(ev, "docs/evidence/lunaroute-provider/model-capabilities.md#"), "%s: static fact must cite the source note: %q", m.ID, ev)
				parts := strings.SplitN(ev, "#", 2)
				raw, err := os.ReadFile(filepath.Join("..", "..", parts[0]))
				require.NoError(t, err)
				assert.Contains(t, string(raw), "\n## "+parts[1]+"\n", "evidence anchor exists")
			}
		}
		for _, facts := range []map[string]provider.SupportFact{m.Backends, {"chat": m.Task(provider.ProfileKindChat), "agent": m.Task(provider.ProfileKindAgent)}} {
			for axis, f := range facts {
				if f.Support == provider.SupportSupported {
					assert.True(t, strings.HasPrefix(f.Evidence, "docs/evidence/lunaroute-provider/observations/"), "%s/%s: support requires a live observation", m.ID, axis)
				}
			}
		}
	}
	assert.ElementsMatch(t, []string{"claude-sonnet-5-lr1", "deepseek-4.1-flash-1017", "glm-5.3", "glm-5.3-flash"}, ids)
}

func TestLoadCapabilityRules_LunaRouteRejectsBadFacts(t *testing.T) {
	cases := map[string]string{
		"unknown task":                 "tasks:\n      embed: {support: supported, evidence: e}",
		"unknown support":              "tasks:\n      chat: {support: maybe, evidence: e}",
		"known without evidence":       "tasks:\n      chat: {support: supported}",
		"unsupported without evidence": "backends:\n      pi: {support: unsupported}",
		"unknown backend":              "backends:\n      cursor: {support: supported, evidence: e}",
		"zero limit":                   "context_tokens: {tokens: 0, evidence: e}",
		"negative limit":               "output_tokens: {tokens: -5, evidence: e}",
		"limit without evidence":       "output_tokens: {tokens: 100}",
		"bad effort":                   "efforts:\n      chat: {levels: [turbo], evidence: e}",
		"default effort":               "efforts:\n      chat: {levels: [default], evidence: e}",
		"unknown effort axis":          "efforts:\n      cursor: {levels: [low], evidence: e}",
		"effort without evidence":      "efforts:\n      chat: {levels: [low]}",
		"reasoning without evidence":   "reasoning: {support: supported}",
		"bad reasoning":                "reasoning: {support: maybe, evidence: e}",
		"unknown setting":              "request_settings:\n      top_p: {support: supported, evidence: e}",
		"temperature without evidence": "request_settings:\n      temperature: {support: unsupported}",
		"schema without evidence":      "request_settings:\n      output_schema: {support: supported}",
		"whitespace evidence":          "reasoning: {support: supported, evidence: '   '}",
		"schema type":                  "context_tokens: {tokens: many, evidence: e}",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := adapterconfig.LoadCapabilityRules(writeCaps(t, "lunaroute_models:\n  distinctive-model:\n    "+body+"\n"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "lunaroute_models")
			assert.Contains(t, err.Error(), "distinctive-model")
		})
	}
}

func TestLoadCapabilityRules_LunaRouteOverlayIsPerModel(t *testing.T) {
	body := lrOverride + `  deepseek-4.1-flash-1017:
    tasks:
      chat: {support: supported, evidence: e}
`
	embedded, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	r, err := adapterconfig.LoadCapabilityRules(writeCaps(t, body))
	require.NoError(t, err)
	for _, original := range embedded.LunaRouteModels() {
		if original.ID == "glm-5.3" || original.ID == "deepseek-4.1-flash-1017" {
			continue
		}
		got, ok := r.LunaRouteModel(original.ID)
		require.True(t, ok)
		assert.Equal(t, original, got)
	}
	m, ok := r.LunaRouteModel("deepseek-4.1-flash-1017")
	require.True(t, ok)
	assert.False(t, m.ContextTokens.Known())
	assert.Equal(t, provider.SupportUnknown, m.Reasoning.Support)
	assert.Empty(t, m.RequestSettings)
}

func TestLoadCapabilityRules_ExplicitUnknownNeedsNoEvidence(t *testing.T) {
	r, err := adapterconfig.LoadCapabilityRules(writeCaps(t, "lunaroute_models:\n  m:\n    backends:\n      pi: {support: unknown}\n    reasoning: {support: unknown}\n    request_settings:\n      output_schema: {support: unknown}\n"))
	require.NoError(t, err)
	m, ok := r.LunaRouteModel("m")
	require.True(t, ok)
	assert.Equal(t, provider.SupportUnknown, m.Backend("pi").Support)
	assert.Equal(t, provider.SupportUnknown, m.Reasoning.Support)
	assert.Equal(t, provider.SupportUnknown, m.RequestSettings["output_schema"].Support)
}
