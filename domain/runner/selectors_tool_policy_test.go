package runner

import (
	"errors"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type policyDispatcher struct {
	*recordingDispatcher
	policy AgentToolPolicy
}

func (d *policyDispatcher) agentPolicy() AgentToolPolicy { return d.policy }

func TestRoutedAgent_ToolsAreTheEffectiveScopeInBothRequestAndRequirements(t *testing.T) {
	rec := &recordingDispatcher{out: map[OpType]StepOutput{"agent": {Agent: &engine.AgentResult{Summary: "ok"}}}}
	d := &policyDispatcher{recordingDispatcher: rec, policy: func(_ provider.ResolvedBinding, req []string) ([]string, error) {
		if len(req) == 0 {
			return []string{"Read", "Grep"}, nil
		}
		return req, nil
	}}
	b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-opus-5-5", Backend: "claude-code"}
	p, err := newRoutingAgentSelector(&innerChats{}, d).AgentProviderFor(b)
	require.NoError(t, err)
	_, err = p.RunAgent(stepCtx(uuid.New()), engine.AgentMission{Model: "claude-opus-5-5", Prompt: "do"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Read", "Grep"}, rec.reqs[0].Tools)
	assert.Equal(t, []string{"Read", "Grep"}, rec.jobs[0].Agent.ToolScope)
}

func TestRoutedAgent_RefusedScopeDispatchesNothing(t *testing.T) {
	rec := &recordingDispatcher{}
	refused := errors.New("codex: tool scope cannot be enforced by codex-cli")
	d := &policyDispatcher{recordingDispatcher: rec, policy: func(provider.ResolvedBinding, []string) ([]string, error) { return nil, refused }}
	b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "gpt-5.2-codex", Backend: "codex"}
	p, err := newRoutingAgentSelector(&innerChats{}, d).AgentProviderFor(b)
	require.NoError(t, err)
	_, err = p.RunAgent(stepCtx(uuid.New()), engine.AgentMission{Model: "gpt-5.2-codex", ToolScope: []string{"Bash(gh:*)"}})
	require.ErrorIs(t, err, refused)
	assert.Empty(t, rec.reqs, "no intent, no placement")
}

func TestStepDispatcher_ExposesItsAgentPolicy(t *testing.T) {
	d := &StepDispatcher{}
	assert.Nil(t, d.agentPolicy())
	d.SetAgentToolPolicy(func(_ provider.ResolvedBinding, s []string) ([]string, error) { return s, nil })
	assert.NotNil(t, d.agentPolicy())
}
