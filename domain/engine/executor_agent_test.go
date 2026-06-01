package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentResolver builds a resolver with a single agent profile aliased
// "agent-default" mapping to the given model.
func agentResolver(model string) *fakeResolver {
	return newFakeResolver(provider.Profile{
		Alias: "agent-default",
		Kind:  provider.ProfileKindAgent,
		Model: model,
	})
}

// storeWithWorkspace returns a store seeded with the reserved workspace var.
func storeWithWorkspace(path string) *variable.VariableStore {
	vars := variable.NewStore()
	vars.SetReserved(engine.ReservedVarWorkspace, path)
	return vars
}

// Happy path: template read + {{variable}} substitution feeds mission.Prompt;
// profile resolves to mission.Model; {{workspace}} seeds mission.WorkDir; a
// schema-less result yields the summary; tokens + model propagate; steps land on
// the trace payload.
func TestAgentCallExecutor_HappyPath(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("Fix the bug in {{component}}")

	var captured engine.AgentMission
	ap := &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			captured = m
			return &engine.AgentResult{
				Summary: "fixed the parser bug",
				Steps: []engine.AgentStep{
					{Kind: "tool_call", Summary: "Read parser.go"},
					{Kind: "edit", Summary: "rewrote tokenizer"},
				},
				Model:     m.Model,
				TokensIn:  100,
				TokensOut: 400,
			}, nil
		},
	}

	vars := storeWithWorkspace("/workspace/inst-1")
	vars.SetInput("component", "parser")

	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
		},
	}

	exec := engine.NewAgentCallExecutor(fr, ap, agentResolver("claude-opus-4-8"))
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	// Mission seeded correctly.
	assert.Equal(t, "Fix the bug in parser", captured.Prompt)
	assert.Equal(t, "claude-opus-4-8", captured.Model)
	assert.Equal(t, "/workspace/inst-1", captured.WorkDir)
	// Output is the summary (no schema).
	assert.Equal(t, map[string]any{"response": "fixed the parser bug"}, result.Output)
	assert.Equal(t, "claude-opus-4-8", result.LLMModel)
	assert.Equal(t, 100, result.LLMTokensIn)
	assert.Equal(t, 400, result.LLMTokensOut)
	// Steps recorded on the trace payload.
	conv, ok := result.LLMConversation.(map[string]any)
	require.True(t, ok, "LLMConversation should carry the agent trace map")
	assert.Equal(t, "fixed the parser bug", conv["summary"])
	steps, ok := conv["steps"].([]engine.AgentStep)
	require.True(t, ok)
	require.Len(t, steps, 2)
	assert.Equal(t, "rewrote tokenizer", steps[1].Summary)
}

// Success: with OutputSchema set, a JSON summary parses into structured output;
// ToolScope and timeoutSeconds thread into the mission.
func TestAgentCallExecutor_SuccessWithSchemaAndConfig(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")

	var captured engine.AgentMission
	ap := &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			captured = m
			return &engine.AgentResult{
				Summary: `{"status": "done", "files": 3}`,
				Model:   m.Model,
			}, nil
		},
	}

	vars := storeWithWorkspace("/workspace/inst-2")
	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile:   "mission.txt",
			ModelProfile:   "agent-default",
			ToolScope:      []string{"Read", "Edit", "Bash(go test:*)"},
			TimeoutSeconds: 600,
			OutputSchema:   map[string]any{"type": "object"},
		},
	}

	exec := engine.NewAgentCallExecutor(fr, ap, agentResolver("claude-opus-4-8"))
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, []string{"Read", "Edit", "Bash(go test:*)"}, captured.ToolScope)
	assert.Equal(t, 600*time.Second, captured.Timeout)
	// JSON summary parsed under schema.
	assert.Equal(t, map[string]any{"status": "done", "files": float64(3)}, result.Output)
}

// Failure: fileReader.ReadFile error -> wrapped read template error, no provider call.
func TestAgentCallExecutor_TemplateReadError(t *testing.T) {
	fr := newMockFileReader()
	// No template registered.

	ap := &fakeAgent{
		runFn: func(_ context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			t.Fatal("RunAgent should not be called on a read error")
			return nil, nil
		},
	}

	vars := storeWithWorkspace("/workspace/inst-3")
	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "missing.txt",
			ModelProfile: "agent-default",
		},
	}

	exec := engine.NewAgentCallExecutor(fr, ap, agentResolver("claude-opus-4-8"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent_call")
	assert.Contains(t, err.Error(), "read template")
}

// Error: RunAgent error -> wrapped run agent error.
func TestAgentCallExecutor_RunAgentError(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")

	ap := &fakeAgent{
		runFn: func(_ context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			return nil, errors.New("agent reported failure")
		},
	}

	vars := storeWithWorkspace("/workspace/inst-4")
	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
		},
	}

	exec := engine.NewAgentCallExecutor(fr, ap, agentResolver("claude-opus-4-8"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "run agent")
	assert.Contains(t, err.Error(), "agent reported failure")
}

// Error: resolver miss -> wrapped resolve profile error, no provider call.
func TestAgentCallExecutor_ResolverMiss(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")

	ap := &fakeAgent{
		runFn: func(_ context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			t.Fatal("RunAgent should not be called on a resolver miss")
			return nil, nil
		},
	}

	vars := storeWithWorkspace("/workspace/inst-5")
	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "no-such-profile",
		},
	}

	exec := engine.NewAgentCallExecutor(fr, ap, agentResolver("claude-opus-4-8"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve profile")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// Edge case: {{workspace}} not seeded -> empty WorkDir propagated; the provider
// rejects it and the error surfaces (no silent run).
func TestAgentCallExecutor_MissingWorkspace(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")

	errWorkDirRequired := errors.New("work dir required")
	var sawWorkDir string
	ap := &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			sawWorkDir = m.WorkDir
			if m.WorkDir == "" {
				return nil, errWorkDirRequired
			}
			return &engine.AgentResult{Summary: "ok"}, nil
		},
	}

	// No workspace seeded.
	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
		},
	}

	exec := engine.NewAgentCallExecutor(fr, ap, agentResolver("claude-opus-4-8"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Empty(t, sawWorkDir)
	assert.ErrorIs(t, err, errWorkDirRequired)
	assert.Contains(t, err.Error(), "run agent")
}

// Edge case: empty Steps + non-JSON summary with no schema -> valid NodeResult,
// summary wrapped via parseOrWrapText.
func TestAgentCallExecutor_EmptyStepsNonJSONSummary(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")

	ap := &fakeAgent{
		runFn: func(_ context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			return &engine.AgentResult{
				Summary: "plain text summary",
				Steps:   nil,
				Model:   "claude-opus-4-8",
			}, nil
		},
	}

	vars := storeWithWorkspace("/workspace/inst-6")
	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
		},
	}

	exec := engine.NewAgentCallExecutor(fr, ap, agentResolver("claude-opus-4-8"))
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"response": "plain text summary"}, result.Output)
	conv, ok := result.LLMConversation.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "plain text summary", conv["summary"])
	steps, ok := conv["steps"].([]engine.AgentStep)
	require.True(t, ok)
	assert.Empty(t, steps)
}
