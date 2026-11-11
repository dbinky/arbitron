package lunaroute_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Consume the operator's committed artifact, not a generated fixture. The
// historical classification is retained; R4 changes its production interpretation.
func TestProductionDiscoveryChatObservation(t *testing.T) {
	const ev = evidencePath + "/observations/lr04-discovery-chat.json"
	raw, err := readRepoFile(repoRoot(t), ev)
	require.NoError(t, err)
	require.NoError(t, validateObservationJSON(raw, ""))
	var obs discoveryChatObservation
	require.NoError(t, json.Unmarshal(raw, &obs))
	_, err = time.Parse(time.RFC3339, obs.RecordedAt)
	require.NoError(t, err)
	assert.Equal(t, hostedRoot, obs.Root)
	assert.Equal(t, "array", obs.ListingShape["data"])
	require.NotEmpty(t, obs.ListingModelIDs)
	assert.Contains(t, obs.ListingModelIDs, obs.Chat.Model)
	assert.Equal(t, "glm-5.3", obs.Chat.Model)
	assert.Equal(t, obs.Chat.Model, obs.Chat.ResponseModel)
	assert.True(t, obs.Chat.Completed)
	assert.Positive(t, obs.Chat.TokensIn)
	assert.Positive(t, obs.Chat.TokensOut)
	require.Len(t, obs.Inference, 1)
	require.NoError(t, validateCapturedInference(obs.Inference, obs.Chat.Model, []string{"main"}))
	assert.Equal(t, "chat", obs.Inference[0].Backend)
	assert.Equal(t, "selected-connection", obs.Inference[0].Connection)
	assert.Equal(t, "/v1/chat/completions", obs.Inference[0].Path)
	assert.Equal(t, []string{obs.Chat.ResponseModel}, obs.Inference[0].ReportedModels)
	assert.GreaterOrEqual(t, discoveryChatTokens, 1024, "reasoning must leave room for terminal content")
	require.Len(t, obs.Errors, 2)
	for i, expected := range []struct {
		name, path, code string
		status           int
		kind             error
	}{
		{"invalid key", "/v1/models", "INVALID_API_KEY", http.StatusUnauthorized, provider.ErrLunaRouteCredentialsRejected},
		{"unavailable model", "/v1/chat/completions", "INFERENCE_MODEL_NOT_FOUND", http.StatusNotFound, provider.ErrLunaRouteModelUnavailable},
	} {
		o := obs.Errors[i]
		assert.Equal(t, expected.name, o.Case)
		assert.Equal(t, expected.path, o.Path)
		assert.Equal(t, expected.status, o.Status)
		assert.Equal(t, expected.code, o.Codes["error.code"])
		assert.Equal(t, "application/json", o.ContentType)
		assert.Equal(t, "object", o.BodyShape["error"])
		assert.Equal(t, []string{"Authorization"}, o.AuthHeaderNames)
		assert.Equal(t, i == 1, o.AuthMatchesSelected)
		op := provider.LunaRouteListModels
		if i == 0 {
			assert.Equal(t, "credentials_rejected", o.Classified)
		}
		if i == 1 {
			op = provider.LunaRouteChat
			assert.Equal(t, "claude-sonnet-5-lr1", o.Model)
			assert.NotContains(t, obs.ListingModelIDs, o.Model)
			assert.Equal(t, "cause_unknown", o.Classified, "do not rewrite historical evidence")
		}
		body, err := json.Marshal(map[string]any{"error": map[string]string{"code": o.Codes["error.code"]}})
		require.NoError(t, err)
		assert.ErrorIs(t, provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: op, Status: o.Status, Code: provider.LunaRouteErrorCode(op, string(body))}), expected.kind)
	}
	rules, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	for _, id := range releaseCandidates {
		m, ok := rules.LunaRouteModel(id)
		require.True(t, ok)
		chat := provider.SupportFact{Support: provider.SupportUnknown}
		if id == obs.Chat.Model {
			chat = provider.SupportFact{Support: provider.SupportSupported, Evidence: ev}
		}
		assert.Equal(t, chat, m.Task(provider.ProfileKindChat), "completion authorizes only this observed model/task")
		assert.Equal(t, provider.SupportUnknown, m.Task(provider.ProfileKindAgent).Support)
		for _, backend := range provider.LunaRouteBackends {
			assert.Equal(t, provider.SupportUnknown, m.Backend(backend).Support)
		}
	}
	r := loadRegister(t)
	require.Empty(t, validateRegister(r))
	it := r.Items["LR-04"]
	assert.Equal(t, "verified", it.Status)
	assert.Empty(t, it.Escalation)
	assert.Contains(t, it.Evidence, ev)
	assert.Contains(t, it.Evidence, evidencePath+"/gateway-contract.md")
	for _, ref := range []string{"domain/provider/lunaroute_classify.go:ClassifyLunaRoute", "adapter/probe/lunaroute.go:Probe", "adapter/provider/factory.go:ChatProviderFor"} {
		assert.Contains(t, it.AppliedTo, ref)
	}
	assert.NotEmpty(t, releaseProblems(repoRoot(t), r), "verified LR-04 is not whole-release readiness")
}
