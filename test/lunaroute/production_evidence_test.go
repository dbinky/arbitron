package lunaroute_test

import (
	"context"
	"encoding/json"
	"net/http"
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

var releaseCandidates = []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"}

// R2 authorizes the recorded metadata, not runtime catalog inference. Check the
// loaded production values against that independent producer, including omissions.
func TestProductionCapabilitySnapshot(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(evidenceDir(t), "sources/lr02-live-models-2026-11-11.json"))
	require.NoError(t, err)
	var snapshot struct {
		Source string `json:"source"`
		Models []struct {
			ID           string          `json:"id"`
			Context      int             `json:"context_window"`
			Output       int             `json:"max_output_tokens"`
			Capabilities map[string]bool `json:"capabilities"`
			ClientCompat struct {
				Pi struct {
					SupportsReasoningEffort bool               `json:"supportsReasoningEffort"`
					ThinkingLevelMap        map[string]*string `json:"thinkingLevelMap"`
				} `json:"pi"`
			} `json:"client_compat"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Contains(t, snapshot.Source, provider.LunaRouteHostedRoot+"/v1/models")
	rules, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	var ids []string
	for _, source := range snapshot.Models {
		ids = append(ids, source.ID)
		m, ok := rules.LunaRouteModel(source.ID)
		require.True(t, ok, source.ID)
		ev := evidencePath + "/model-capabilities.md#" + source.ID
		require.Positive(t, source.Context)
		require.Positive(t, source.Output)
		assert.Equal(t, provider.LimitFact{Tokens: source.Context, Evidence: ev}, m.ContextTokens)
		assert.Equal(t, provider.LimitFact{Tokens: source.Output, Evidence: ev}, m.OutputTokens)
		require.True(t, source.Capabilities["reasoning"])
		require.True(t, source.Capabilities["json_schema"])
		assert.Equal(t, provider.SupportFact{Support: provider.SupportSupported, Evidence: ev}, m.Reasoning)
		assert.Equal(t, provider.SupportFact{Support: provider.SupportSupported, Evidence: ev}, m.RequestSettings["output_schema"])
		assert.Equal(t, provider.SupportUnknown, m.RequestSettings["temperature"].Support)
		require.True(t, source.ClientCompat.Pi.SupportsReasoningEffort)
		var exact []provider.Effort
		for level, wire := range source.ClientCompat.Pi.ThinkingLevelMap {
			if wire != nil && *wire == level {
				exact = append(exact, provider.Effort(level))
			} else if level == "off" && wire != nil && *wire == "none" {
				// Arbitron's none is pi's off, not a graded-effort remap.
				exact = append(exact, provider.EffortNone)
			}
		}
		require.NotEmpty(t, exact)
		assert.True(t, m.Effort("pi").Known)
		assert.ElementsMatch(t, exact, m.Effort("pi").Levels, "do not authorize remapped or null effort levels")
		assert.Equal(t, ev, m.Effort("pi").Evidence)
		for _, axis := range []string{"chat", "claude-code", "opencode", "codex"} {
			assert.False(t, m.Effort(axis).Known, "pi compatibility does not establish %s effort support", axis)
		}
		for _, kind := range []provider.ProfileKind{provider.ProfileKindChat, provider.ProfileKindAgent} {
			assert.Equal(t, provider.SupportUnknown, m.Task(kind).Support, "metadata alone is not a completed task")
		}
		for _, backend := range provider.LunaRouteBackends {
			assert.Equal(t, provider.SupportUnknown, m.Backend(backend).Support)
		}
	}
	assert.ElementsMatch(t, releaseCandidates, ids)
	assert.Len(t, rules.LunaRouteModels(), len(ids), "no revoked alias or upstream weights id")
}

// Inspect the deployed source, not an injected nonempty unit fixture.
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
					assert.Contains(t, section, "| efforts."+axis+" | unknown |")
				}
			}
		})
	}
}

func TestProductionSnapshotRegisterApplication(t *testing.T) {
	r := loadRegister(t)
	require.Empty(t, validateRegister(r))
	it := r.Items["LR-02"]
	assert.Equal(t, "decided", it.Status, "static application must not claim completed backend verification")
	assert.Empty(t, it.Escalation, "R2 lifts the missing-metadata escalation")
	assert.Contains(t, it.Evidence, evidencePath+"/sources/lr02-live-models-2026-11-11.json")
	for _, ref := range []string{
		"adapter/config/capabilities.default.yaml:lunaroute_models",
		"adapter/config/capabilities.go:LoadCapabilityRules",
		"domain/provider/model_discovery.go:Discover",
	} {
		assert.Contains(t, it.AppliedTo, ref)
	}
	assert.NotEmpty(t, releaseProblems(repoRoot(t), r), "unfinished backend/matrix evidence still closes the release gate")
}

func TestProductionGatewayObservation(t *testing.T) {
	const name = "lr01-gateway-20261111T155705.865199000Z.json"
	raw, err := readRepoFile(repoRoot(t), evidencePath+"/observations/"+name)
	require.NoError(t, err)
	require.NoError(t, validateObservationJSON(raw, ""), "operator evidence must satisfy the same private observation schema")
	var observed []gatewayObservation
	require.NoError(t, json.Unmarshal(raw, &observed))
	require.Len(t, observed, 5)
	for i, dialect := range []string{"models", "chat", "messages", "responses", "invalid-key"} {
		o := observed[i]
		assert.Equal(t, dialect, o.Dialect)
		assert.Equal(t, "Authorization", o.AuthHeader)
		switch dialect {
		case "models":
			assert.Equal(t, "/v1/models", o.Path)
			assert.Equal(t, http.StatusOK, o.Status)
			assert.Positive(t, o.ModelCount)
			assert.Equal(t, "array", o.Schema["data"])
		case "invalid-key":
			assert.Equal(t, "/v1/models", o.Path)
			assert.Equal(t, http.StatusUnauthorized, o.Status)
			assert.Equal(t, "INVALID_API_KEY", o.ErrorCode)
			assert.ErrorIs(t, provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Status: o.Status, Code: o.ErrorCode}), provider.ErrLunaRouteCredentialsRejected)
		default:
			path := map[string]string{"chat": "/v1/chat/completions", "messages": "/v1/messages", "responses": "/v1/responses"}[dialect]
			assert.Equal(t, path, o.Path)
			assert.Equal(t, http.StatusOK, o.Status)
			assert.Equal(t, "glm-5.3", o.Model)
			assert.True(t, modelidentity.LunaRouteExecutedModelAccepted(o.Model, o.ReportedModel))
			assert.True(t, o.Terminal)
			assert.True(t, o.HasContent)
			assert.NotEmpty(t, o.Schema)
		}
	}
	assert.Contains(t, loadRegister(t).Items["LR-01"].Evidence, evidencePath+"/observations/"+name)
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
		vendor := map[string]modelidentity.Family{"glm-5.3": "zhipuai", "glm-5.3-flash": "zhipuai", "deepseek-4.1-flash": "deepseek"}[id]
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
