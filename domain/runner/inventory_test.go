package runner

import (
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func invByID(srcs []Source) map[SourceID]Source {
	m := map[SourceID]Source{}
	for _, s := range srcs {
		m[s.ID] = s
	}
	return m
}

func TestBuiltInSources_FullHost(t *testing.T) {
	in := InventoryInput{
		ClaudeCLI:     &HarnessPresence{Path: "/usr/local/bin/claude", Version: "2.1.328", Supported: true},
		Codex:         &HarnessPresence{Path: "/usr/local/bin/codex", Version: "0.170.0", Supported: true},
		Connections:   []provider.Connection{{Name: "lr", Provider: provider.CanonicalLunaRoute}, {Name: "azure-east", Provider: "azure-openai"}},
		LocalBackends: []string{"pi", "opencode"},
	}
	got := BuiltInSources(in)
	m := invByID(got)
	require.Len(t, got, 7)

	anth := m[MakeSourceID(BuiltInRunnerID, CredAnthropicSubscription, "")]
	assert.Equal(t, []modelidentity.Family{"anthropic"}, anth.Families)
	assert.Equal(t, []string{"", "claude-code"}, anth.Backends)
	assert.Equal(t, BuiltInRunnerID, anth.RunnerID)
	assert.Equal(t, AuthState("unknown"), anth.Health.Auth)
	assert.Equal(t, CapacityState("available"), anth.Health.Capacity)
	assert.Empty(t, anth.UnavailableReason)

	oa := m[MakeSourceID(BuiltInRunnerID, CredOpenAISubscription, "")]
	assert.Equal(t, []modelidentity.Family{"openai"}, oa.Families)
	assert.Equal(t, []string{"codex"}, oa.Backends)

	lr := m[MakeSourceID(BuiltInRunnerID, CredLunaRoute, "lr")]
	assert.Equal(t, "lr", lr.CredentialRef)
	assert.Equal(t, modelidentity.KnownFamilies(), lr.Families)
	assert.Equal(t, []string{"", "codex", "pi"}, lr.Backends)

	az := m[MakeSourceID(BuiltInRunnerID, CredServerConnection, "azure-east")]
	assert.Contains(t, az.Families, modelidentity.FamilyUnresolved)
	assert.Equal(t, []string{"", "claude-code", "codex", "opencode", "pi"}, az.Backends)

	pi := m[MakeSourceID(BuiltInRunnerID, CredLocalHarness, "pi")]
	assert.Equal(t, []string{"pi"}, pi.Backends)
	assert.Contains(t, pi.Families, modelidentity.FamilyUnresolved)

	none := m[MakeSourceID(BuiltInRunnerID, CredNone, "")]
	assert.Empty(t, none.Families)
	assert.Empty(t, none.Backends)

	for i := 1; i < len(got); i++ {
		assert.Less(t, string(got[i-1].ID), string(got[i].ID), "deterministic order")
	}
}

// Missing/unsupported harnesses keep their source (today's fail-fast behaviour
// stays with the adapter) but are visible with a class-only unavailability reason and excluded from placement.
func TestBuiltInSources_AnnotatesMissingAndUnsupported(t *testing.T) {
	got := invByID(BuiltInSources(InventoryInput{
		ClaudeCLI: &HarnessPresence{Path: "/c", Version: "2.1.100", Supported: false, Reason: "claude 2.1.100 is below 2.1.284"},
	}))
	require.Len(t, got, 5, "anthropic, openai, pi, opencode, none")
	assert.Equal(t, "harness_unsupported", got[MakeSourceID(BuiltInRunnerID, CredAnthropicSubscription, "")].UnavailableReason)
	assert.Equal(t, "harness_not_found", got[MakeSourceID(BuiltInRunnerID, CredOpenAISubscription, "")].UnavailableReason)
	assert.Equal(t, "harness_not_found", got[MakeSourceID(BuiltInRunnerID, CredLocalHarness, "pi")].UnavailableReason)
}

// Edge: two connections never share a source (health is per connection).
func TestBuiltInSources_ConnectionsDistinct(t *testing.T) {
	got := BuiltInSources(InventoryInput{Connections: []provider.Connection{{Name: "a", Provider: "openrouter"}, {Name: "b", Provider: "openrouter"}}})
	m := invByID(got)
	assert.Contains(t, m, MakeSourceID(BuiltInRunnerID, CredServerConnection, "a"))
	assert.Contains(t, m, MakeSourceID(BuiltInRunnerID, CredServerConnection, "b"))
}
