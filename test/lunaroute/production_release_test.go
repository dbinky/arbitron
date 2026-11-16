package lunaroute_test

import (
	"encoding/json"
	"strings"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const manualTaskEvidence = evidencePath + "/observations/operator-manual-agent-tasks-2026-11-12.md"

// R7 accepts the operator's tool tasks, not controls queries alone. Consume
// both genuine producers and compare their selected model and facts with the
// deployed table; offline protocol fixtures never authorize support.
func TestProductionR7CompletionApplication(t *testing.T) {
	rules, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	raw, err := readRepoFile(repoRoot(t), manualTaskEvidence)
	require.NoError(t, err)
	require.False(t, unsafeText(string(raw), ""))
	assert.Contains(t, string(raw), "# Operator manual agent tool tasks — 2026-11-12")
	for backend, proofs := range map[string][]string{
		"codex": {"codex-cli 0.167.2", "`glm-5.3`", "Private `CODEX_HOME`", "LR-07", "**Pass.** Exit 0", "`hello.txt` (`DONE`)", "through `apply_patch`", "`turn.completed`"},
		"pi":    {"pi 1.5.2", "`lunaroute/glm-5.3`", "Private `PI_CODING_AGENT_DIR`", "LR-08", "**Pass.**", "`hello.txt` (`DONE`)", "through the `write` tool call", "`agent_settled` with `aborted:false`"},
	} {
		var rows []string
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "| "+backend+" |") {
				rows = append(rows, line)
			}
		}
		require.Len(t, rows, 1, "one operator task row per released backend")
		for _, proof := range proofs {
			assert.Contains(t, rows[0], proof, backend)
		}
	}

	readProof := func(name string, dst any) {
		t.Helper()
		b, err := readRepoFile(repoRoot(t), evidencePath+"/observations/"+name)
		require.NoError(t, err)
		require.NoError(t, validateObservationJSON(b, ""))
		require.NoError(t, json.Unmarshal(b, dst))
	}
	var codex codexConfigControlProof
	readProof("lr07-codex-config-controls.json", &codex)
	assert.Equal(t, "app_server_resolved_config_only", codex.Scope)
	assert.Equal(t, "codex-cli 0.167.2", codex.Version)
	assert.Equal(t, "glm-5.3", codex.Model)
	assert.Equal(t, codex.Model, codex.ReviewModel)
	assert.Equal(t, "lunaroute", codex.Provider)
	assert.Equal(t, hostedRoot+"/v1", codex.BaseURL)
	assert.Equal(t, "responses", codex.WireAPI)
	assert.Equal(t, "LUNAROUTE_API_KEY", codex.EnvKey)
	assert.ElementsMatch(t, []string{"conflicting", "absent"}, codex.InputCases)
	assert.True(t, codex.ProtectedInputsUnchanged)
	assert.True(t, codex.PrivateCleaned)
	_, err = readRepoFile(repoRoot(t), codex.Source)
	require.NoError(t, err)

	var pi piModelControlProof
	readProof("lr08-pi-model-controls.json", &pi)
	assert.Equal(t, "rpc_resolved_model_only", pi.Scope)
	assert.Equal(t, "1.5.2", pi.Version)
	assert.Equal(t, codex.Model, pi.Model)
	assert.Equal(t, "lunaroute", pi.Provider)
	assert.Equal(t, "openai-completions", pi.API)
	assert.Equal(t, hostedRoot+"/v1", pi.BaseURL)
	assert.True(t, pi.ProtectedInputsUnchanged)
	assert.True(t, pi.PrivateCleaned)
	require.NotEmpty(t, pi.SessionID)
	assert.Equal(t, pi.SessionID, pi.AuthCaseSessions["conflicting"])
	require.NotEmpty(t, pi.AuthCaseSessions["absent"])
	assert.NotEqual(t, pi.SessionID, pi.AuthCaseSessions["absent"])
	_, err = readRepoFile(repoRoot(t), pi.Source)
	require.NoError(t, err)
	assert.Equal(t, evidencePath+"/sources/lr02-live-models-2026-11-11.json", pi.MetadataSource)
	model, ok := rules.LunaRouteModel(pi.Model)
	require.True(t, ok)
	require.Positive(t, pi.ContextTokens)
	require.Positive(t, pi.OutputTokens)
	assert.Equal(t, pi.ContextTokens, model.ContextTokens.Tokens)
	assert.Equal(t, pi.OutputTokens, model.OutputTokens.Tokens)
	assert.True(t, pi.Reasoning)
	assert.Equal(t, provider.SupportSupported, model.Reasoning.Support)

	for _, id := range releaseCandidates {
		m, ok := rules.LunaRouteModel(id)
		require.True(t, ok)
		want := provider.SupportFact{Support: provider.SupportUnknown}
		if id == pi.Model {
			want = provider.SupportFact{Support: provider.SupportSupported, Evidence: manualTaskEvidence}
		}
		assert.Equal(t, want, m.Task(provider.ProfileKindAgent))
		for _, backend := range []string{"codex", "pi"} {
			assert.True(t, provider.LunaRouteBackendEnabled(backend))
			assert.Equal(t, want, m.Backend(backend), "a different model's tool task cannot authorize support")
		}
		for _, backend := range []string{"claude-code", "opencode"} {
			assert.False(t, provider.LunaRouteBackendEnabled(backend))
			assert.Equal(t, provider.SupportUnknown, m.Backend(backend).Support)
		}
	}
}

func TestProductionR7RegisterApplication(t *testing.T) {
	r := loadRegister(t)
	require.Empty(t, validateRegister(r))
	for id, refs := range map[string][]string{
		"LR-01": {"domain/provider/lunaroute_endpoint.go:LunaRouteHostedRoot", "adapter/probe/lunaroute.go:Probe", "adapter/provider/factory.go:ChatProviderFor", "adapter/codex/lunaroute.go:prepareLunaRouteLaunch", "adapter/pi/lunaroute.go:prepareLunaRoutePiLaunch"},
		"LR-02": {"adapter/config/capabilities.default.yaml:lunaroute_models", "adapter/config/capabilities.go:LoadCapabilityRules", "domain/provider/model_discovery.go:Discover", "domain/provider/lunaroute_validate.go:CheckLunaRouteRequest", "adapter/pi/lunaroute.go:lunaRouteModelsJSON"},
		"LR-03": {"domain/modelidentity/version.go:lunaRouteReviewedIDs", "domain/modelidentity/family.go:ClassifyFamily"},
		"LR-07": {"adapter/codex/lunaroute.go:lunaRouteExecExtra", "adapter/codex/lunaroute.go:prepareLunaRouteLaunch", "adapter/provider/factory.go:AgentProviderFor"},
		"LR-08": {"adapter/pi/lunaroute.go:lunaRouteModelsJSON", "adapter/pi/lunaroute.go:prepareLunaRoutePiLaunch", "adapter/provider/factory.go:AgentProviderFor"},
	} {
		it := r.Items[id]
		assert.Equal(t, "verified", it.Status, id)
		assert.Empty(t, it.Escalation)
		for _, ref := range refs {
			assert.Contains(t, it.AppliedTo, ref, id)
		}
	}
	assert.Contains(t, r.Items["LR-02"].Evidence, manualTaskEvidence)
	for _, id := range []string{"LR-05", "LR-06"} {
		assert.Equal(t, "deferred", r.Items[id].Status)
	}
	assert.Empty(t, releaseProblems(repoRoot(t), r), "all released contracts have evidence AND runtime application; matrix enforcement is separate")
}

// The gate's comment points at a tracked checklist; neither may rot. Both
// unreleased backends stay refused until their sections are complete.
func TestProductionEnablementChecklistLinkedToGate(t *testing.T) {
	const checklist = evidencePath + "/enablement-checklist.md"
	gate, err := readRepoFile(repoRoot(t), "domain/provider/lunaroute_validate.go")
	require.NoError(t, err)
	assert.Contains(t, string(gate), "docs/evidence/lunaroute-provider/enablement-checklist.md")
	raw, err := readRepoFile(repoRoot(t), checklist)
	require.NoError(t, err)
	require.False(t, unsafeText(string(raw), ""))
	for _, want := range []string{
		"lunaRouteEnabledBackends", "Deployed versions", "Live routing and identity provenance",
		"Credential and project isolation", "Existing-provider regressions", "SDK provenance",
		"## claude-code (disabled)", "## opencode (disabled)",
	} {
		assert.Contains(t, string(raw), want)
	}
	for _, backend := range []string{"claude-code", "opencode"} {
		assert.False(t, provider.LunaRouteBackendEnabled(backend), backend)
	}
}
