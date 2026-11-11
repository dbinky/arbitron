package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteModel_AbsentFactsAreUnknown(t *testing.T) {
	var m provider.LunaRouteModel
	assert.Equal(t, provider.SupportUnknown, m.Task(provider.ProfileKindChat).Support)
	assert.Equal(t, provider.SupportUnknown, m.Backend("opencode").Support)
	assert.False(t, m.ContextTokens.Known())
	assert.False(t, m.OutputTokens.Known())
	assert.False(t, m.Effort("chat").Known)
}

func TestCapabilityRules_CarriesLunaRouteTable(t *testing.T) {
	base := provider.NewCapabilityRules(map[string]provider.BackendCapability{"opencode": {Backend: "opencode"}})
	assert.Empty(t, base.LunaRouteModels())
	_, ok := base.LunaRouteModel("glm-5.3")
	assert.False(t, ok)
	glm := provider.LunaRouteModel{
		ID:            "glm-5.3",
		Tasks:         map[provider.ProfileKind]provider.SupportFact{provider.ProfileKindAgent: {Support: provider.SupportSupported, Evidence: "ev#1"}},
		Backends:      map[string]provider.SupportFact{"codex": {Support: provider.SupportUnsupported, Evidence: "ev#2"}},
		ContextTokens: provider.LimitFact{Tokens: 200000, Evidence: "ev#3"},
	}
	r := base.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": glm, "deepseek-4.1-flash-1017": {ID: "deepseek-4.1-flash-1017"}})
	got, ok := r.LunaRouteModel("glm-5.3")
	require.True(t, ok)
	assert.Equal(t, provider.SupportSupported, got.Task(provider.ProfileKindAgent).Support)
	assert.Equal(t, provider.SupportUnsupported, got.Backend("codex").Support)
	assert.Equal(t, provider.SupportUnknown, got.Backend("opencode").Support)
	assert.Equal(t, "ev#3", got.ContextTokens.Evidence)
	assert.True(t, got.ContextTokens.Known())
	assert.False(t, got.OutputTokens.Known())
	assert.True(t, r.Has("opencode"), "backend rules preserved")
	var ids []string
	for _, m := range r.LunaRouteModels() {
		ids = append(ids, m.ID)
	}
	assert.Equal(t, []string{"deepseek-4.1-flash-1017", "glm-5.3"}, ids)
	models := r.LunaRouteModels()
	models[0].ID = "changed"
	assert.Equal(t, "deepseek-4.1-flash-1017", r.LunaRouteModels()[0].ID, "slice is copied")
	_, ok = base.LunaRouteModel("glm-5.3")
	assert.False(t, ok, "receiver unchanged")
	var port provider.LunaRouteCapabilities = r
	_, ok = port.LunaRouteModel("glm-5.3")
	assert.True(t, ok)
	_, ok = port.LunaRouteModel("GLM-5.3")
	assert.False(t, ok, "lookup is exact")
}

func TestLunaRouteModel_ReasoningIsIndependentOfEffort(t *testing.T) {
	m := provider.LunaRouteModel{Efforts: map[string]provider.EffortFact{"chat": {Known: true, Levels: []provider.Effort{provider.EffortNone}, Evidence: "ev#eff"}}}
	assert.Empty(t, m.Reasoning.Support, "known effort does not manufacture reasoning")
	for _, support := range []provider.Support{provider.SupportSupported, provider.SupportUnsupported} {
		m := provider.LunaRouteModel{ID: "fixture", Reasoning: provider.SupportFact{Support: support, Evidence: "ev#reasoning"}}
		r := provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"fixture": m})
		got, ok := r.LunaRouteModel("fixture")
		require.True(t, ok)
		assert.Equal(t, m.Reasoning, got.Reasoning)
		assert.False(t, got.Effort("chat").Known, "reasoning and efforts are independent")
	}
}
