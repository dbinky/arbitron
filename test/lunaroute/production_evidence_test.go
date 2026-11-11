package lunaroute_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var releaseCandidates = []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash-1017", "claude-sonnet-5-lr1"}

// Inspect the deployed source, not an injected nonempty unit fixture. Unknown
// entries are justified by an explicit per-model source review, never a default.
func TestProductionCapabilityEvidence(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(evidenceDir(t), "model-capabilities.md"))
	require.NoError(t, err)
	rules, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	require.Len(t, rules.LunaRouteModels(), len(releaseCandidates))
	for _, id := range releaseCandidates {
		t.Run(id, func(t *testing.T) {
			section := modelSourceSection(t, string(source), id)
			model, ok := rules.LunaRouteModel(id)
			require.True(t, ok, "reviewed capability record must reach the production loader")
			fact := func(field, value, ev string) {
				t.Helper()
				assert.Contains(t, section, "| "+field+" | "+value+" |", "runtime value must match the cited model source")
				if ev != "" {
					assert.Equal(t, evidencePath+"/model-capabilities.md#"+id, ev)
				}
			}
			limit := func(field string, f provider.LimitFact) {
				t.Helper()
				value := "unknown"
				if f.Known() {
					value = strconv.Itoa(f.Tokens)
					require.NotEmpty(t, f.Evidence)
				}
				fact(field, value, f.Evidence)
			}
			limit("context_tokens", model.ContextTokens)
			limit("output_tokens", model.OutputTokens)
			reasoning := string(model.Reasoning.Support)
			if reasoning == "" {
				reasoning = "unknown"
			}
			fact("reasoning", reasoning, model.Reasoning.Evidence)
			for _, setting := range []string{"temperature", "output_schema"} {
				f := model.RequestSettings[setting]
				value := string(f.Support)
				if value == "" {
					value = "unknown"
				}
				fact("request_settings."+setting, value, f.Evidence)
			}
			for _, axis := range append([]string{"chat"}, provider.LunaRouteBackends...) {
				f := model.Effort(axis)
				if f.Known {
					require.NotEmpty(t, f.Evidence)
					assert.Equal(t, evidencePath+"/model-capabilities.md#"+id, f.Evidence)
					var levels []string
					for _, level := range f.Levels {
						levels = append(levels, string(level))
					}
					fact("efforts."+axis, "["+strings.Join(levels, ", ")+"]", f.Evidence)
				} else {
					assert.Contains(t, section, "| efforts (chat, claude-code, opencode, codex, pi) | unknown |")
				}
			}
		})
	}
}

func modelSourceSection(t *testing.T, source, id string) string {
	t.Helper()
	_, rest, ok := strings.Cut(source, "\n## "+id+"\n")
	require.True(t, ok, "source anchor required")
	section, _, _ := strings.Cut(rest, "\n## ")
	return section
}

func TestProductionIdentityAndGatewayEvidence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(evidenceDir(t), "model-identities.md"))
	require.NoError(t, err)
	for _, id := range releaseCandidates {
		section := modelSourceSection(t, string(raw), id)
		selected, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{RouteProvider: "lunaroute", Model: id})
		if strings.Contains(section, "Identity spelling: **unknown**") {
			require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, id, selected)
		vendor := map[string]modelidentity.Family{"glm-5.3": "zhipuai", "claude-sonnet-5-lr1": "anthropic"}[id]
		require.NotEmpty(t, vendor, "confirmed identities require an explicit source-reviewed vendor")
		assert.Contains(t, section, string(vendor))
		assert.Equal(t, vendor, modelidentity.ClassifyFamily("lunaroute", id))
	}
	docs, err := os.ReadFile(filepath.Join(evidenceDir(t), "gateway-contract.md"))
	require.NoError(t, err)
	outcomes := map[string]error{
		"INVALID_API_KEY":                   provider.ErrLunaRouteCredentialsRejected,
		"CONCURRENT_REQUEST_LIMIT_EXCEEDED": provider.ErrLunaRouteBusy,
		"INFERENCE_UNAVAILABLE":             provider.ErrLunaRouteServiceFailure,
		"NO_HEALTHY_PROVIDER":               provider.ErrLunaRouteServiceFailure,
		"PROVIDER_KEY_UNAVAILABLE":          provider.ErrLunaRouteServiceFailure,
		"BYOK_SPEND_GATE_UNAVAILABLE":       provider.ErrLunaRouteServiceFailure,
		"BYOK_SPEND_CAP_REACHED":            provider.ErrLunaRouteRequestRefused,
		"MODEL_UNPRICED":                    provider.ErrLunaRouteRequestRefused,
	}
	for code, outcome := range outcomes {
		assert.Contains(t, string(docs), "| "+code+" |")
		for _, op := range []provider.LunaRouteOperation{provider.LunaRouteListModels, provider.LunaRouteChat, provider.LunaRouteAgent} {
			assert.ErrorIs(t, provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: op, Status: 418, Code: code}), outcome)
		}
	}
	gateway, err := os.ReadFile(filepath.Join(evidenceDir(t), "sources/lr01-gateway-docs.md"))
	require.NoError(t, err)
	assert.Contains(t, string(gateway), provider.LunaRouteHostedRoot)
}
