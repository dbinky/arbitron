package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingAgentSelector records the binding it is asked to route, so a test can
// assert the EFFECTIVE backend the executor computed before dispatch.
type capturingAgentSelector struct {
	got      provider.ResolvedBinding
	provider engine.AgentProvider
}

func (s *capturingAgentSelector) AgentProviderFor(b provider.ResolvedBinding) (engine.AgentProvider, error) {
	s.got = b
	return s.provider, nil
}

var _ engine.AgentProviderSelector = (*capturingAgentSelector)(nil)

// backendResolver builds a resolver whose binding carries the given backend.
func backendResolver(backend string) *fakeResolver {
	return newFakeResolver(provider.Profile{
		Alias:   "agent-default",
		Kind:    provider.ProfileKindAgent,
		Model:   "m",
		Backend: backend,
	})
}

func runEffectiveBackend(t *testing.T, nodeBackend, profileBackend, defaultBackend string) provider.ResolvedBinding {
	t.Helper()
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")

	ap := &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			return &engine.AgentResult{Summary: "ok", Model: m.Model}, nil
		},
	}
	sel := &capturingAgentSelector{provider: ap}

	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
			Backend:      nodeBackend,
		},
	}

	exec := engine.NewAgentCallExecutor(fr, sel, backendResolver(profileBackend), defaultBackend)
	_, err := exec.Execute(context.Background(), node, storeWithWorkspace("/ws/inst"), "test-wf")
	require.NoError(t, err)
	return sel.got
}

// happy — node overrides profile and default.
func TestEffectiveBackend_NodeOverrides(t *testing.T) {
	b := runEffectiveBackend(t, "opencode", "claude-code", "claude-code")
	assert.Equal(t, "opencode", b.Backend)
}

// happy — profile overrides default when node is empty.
func TestEffectiveBackend_ProfileOverridesDefault(t *testing.T) {
	b := runEffectiveBackend(t, "", "opencode", "claude-code")
	assert.Equal(t, "opencode", b.Backend)
}

// happy — deployment default applies when node and profile are empty.
func TestEffectiveBackend_DefaultApplies(t *testing.T) {
	b := runEffectiveBackend(t, "", "", "claude-code")
	assert.Equal(t, "claude-code", b.Backend)
}

// boundary — all empty + empty default leaves the binding backend empty (the
// executor does NOT invent claude-code itself; the policy lives in the
// composition root / factory).
func TestEffectiveBackend_AllEmptyStaysEmpty(t *testing.T) {
	b := runEffectiveBackend(t, "", "", "")
	assert.Equal(t, "", b.Backend,
		"executor must not invent a default; the factory/composition root owns it")
}

// integration — the precedence computation mutates ONLY Backend; Model,
// AllowedTools, and RevealKey on the binding are untouched.
func TestEffectiveBackend_DoesNotDisturbOtherFields(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")

	ap := &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			return &engine.AgentResult{Summary: "ok", Model: m.Model}, nil
		},
	}
	sel := &capturingAgentSelector{provider: ap}

	resolver := newFakeResolver(provider.Profile{
		Alias:        "agent-default",
		Kind:         provider.ProfileKindAgent,
		Model:        "claude-agent",
		Backend:      "",
		AllowedTools: []string{"Read", "Grep"},
	})

	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
			Backend:      "opencode",
		},
	}

	exec := engine.NewAgentCallExecutor(fr, sel, resolver, "claude-code")
	_, err := exec.Execute(context.Background(), node, storeWithWorkspace("/ws/inst"), "test-wf")
	require.NoError(t, err)

	assert.Equal(t, "opencode", sel.got.Backend, "Backend set to the node override")
	assert.Equal(t, "claude-agent", sel.got.Model, "Model unchanged")
	assert.Equal(t, []string{"Read", "Grep"}, sel.got.AllowedTools, "AllowedTools unchanged")
	assert.NotNil(t, sel.got.RevealKey, "RevealKey unchanged")
	assert.Equal(t, provider.ProfileKindAgent, sel.got.Kind, "Kind unchanged")
}
