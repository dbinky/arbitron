package workflow_test

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Success: a Workflow's stable ID round-trips through JSON under the "id" key,
// so the durable identity persists in workflow.json across reloads.
func TestWorkflow_ID_JSONRoundTrip(t *testing.T) {
	id := uuid.New()
	wf := workflow.Workflow{ID: id, Name: "triage"}

	data, err := json.Marshal(wf)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, id.String(), raw["id"])

	var back workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, id, back.ID)
	assert.Equal(t, "triage", back.Name)
}

// Happy path: the new agent_call node kind constant carries its wire value.
func TestNodeTypeAgentCall_Value(t *testing.T) {
	assert.Equal(t, workflow.NodeType("agent_call"), workflow.NodeTypeAgentCall)
}

// Success: the additive NodeConfig fields (model_profile, toolScope) round-trip
// through JSON under their documented keys.
func TestNodeConfig_AgentCallFields_JSONRoundTrip(t *testing.T) {
	cfg := workflow.NodeConfig{
		TemplateFile:   "mission.txt",
		ModelProfile:   "agent-default",
		ToolScope:      []string{"Read", "Edit", "Bash(go test:*)"},
		TimeoutSeconds: 600,
		OutputSchema:   map[string]any{"type": "object"},
	}

	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "agent-default", raw["model_profile"])
	assert.Equal(t, []any{"Read", "Edit", "Bash(go test:*)"}, raw["toolScope"])

	var back workflow.NodeConfig
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, cfg.ModelProfile, back.ModelProfile)
	assert.Equal(t, cfg.ToolScope, back.ToolScope)
}

// Happy path: the two interactive node type constants carry their wire values.
func TestInteractiveNodeTypeConstants(t *testing.T) {
	if workflow.NodeTypeInteractiveLLM != "interactive_llm" {
		t.Errorf("NodeTypeInteractiveLLM = %q, want interactive_llm", workflow.NodeTypeInteractiveLLM)
	}
	if workflow.NodeTypeInteractiveAgent != "interactive_agent" {
		t.Errorf("NodeTypeInteractiveAgent = %q, want interactive_agent", workflow.NodeTypeInteractiveAgent)
	}
}

// Edge case: omitempty keeps the new fields out of the wire form when unset.
func TestNodeConfig_AgentCallFields_OmitEmpty(t *testing.T) {
	data, err := json.Marshal(workflow.NodeConfig{})
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasProfile := raw["model_profile"]
	_, hasScope := raw["toolScope"]
	assert.False(t, hasProfile)
	assert.False(t, hasScope)
}
