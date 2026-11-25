package provider

import (
	"errors"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAgentPolicy_UnenforceableScopeFailsClosed(t *testing.T) {
	f := policyFactory()
	cx := domprovider.ResolvedBinding{Kind: domprovider.ProfileKindAgent, Provider: "claude-cli", Backend: workflow.BackendCodex}
	got, err := f.EffectiveAgentTools(cx, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "codex with no scope is unrestricted")
	got, err = f.EffectiveAgentTools(cx, []string{"Bash", "Read"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash", "Read"}, got)
	for _, scope := range [][]string{{"Read"}, {"Bash", "Bash(gh:*)"}, {"Bash", "mcp__x__y"}} {
		_, err := f.EffectiveAgentTools(cx, scope)
		require.ErrorIs(t, err, codex.ErrToolScopeUnsupported, "%v", scope)
	}
	profiled := cx
	profiled.AllowedTools = []string{"Bash(gh:*)"}
	_, err = f.EffectiveAgentTools(profiled, []string{"Bash"})
	require.ErrorIs(t, err, codex.ErrToolScopeUnsupported)
	_, err = f.EffectiveAgentTools(domprovider.ResolvedBinding{Backend: "nope"}, []string{"Bash"})
	require.True(t, errors.Is(err, ErrUnknownBackend))
	for _, be := range []string{workflow.BackendOpencode, workflow.BackendPi} {
		got, err := f.EffectiveAgentTools(domprovider.ResolvedBinding{Backend: be}, []string{"Bash(gh:*)"})
		require.NoError(t, err, be)
		assert.Equal(t, []string{"Bash(gh:*)"}, got)
	}
}

func policyFactory() *Factory {
	return NewFactory(Deps{ClaudeAgentConfig: claudecli.Config{BinaryPath: "/bin/claude", AllowedTools: []string{"Read", "Grep"}}})
}

func TestAgentPolicy_ProfileRequestAndGlobalPrecedence(t *testing.T) {
	f := policyFactory()
	claude := domprovider.ResolvedBinding{Kind: domprovider.ProfileKindAgent, Provider: "claude-cli", Backend: workflow.BackendClaudeCode}
	withProfile := claude
	withProfile.AllowedTools = []string{"Bash(gh:*)", "Read"}
	got, err := f.EffectiveAgentTools(withProfile, []string{"Edit"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash(gh:*)", "Read"}, got, "the profile scope wins")
	got, err = f.EffectiveAgentTools(claude, []string{"Edit"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Edit"}, got, "the request scope is the fallback")
	got, err = f.EffectiveAgentTools(claude, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Read", "Grep"}, got, "then configured global scope")
	got[0] = "mutated"
	again, _ := f.EffectiveAgentTools(claude, nil)
	assert.Equal(t, "Read", again[0])
	bare := NewFactory(Deps{})
	got, err = bare.EffectiveAgentTools(claude, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "no scope is the unrestricted default")
}
