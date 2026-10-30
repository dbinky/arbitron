package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIncludeLib_AgentPromptContainsExpandedLib(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("Start. {{include:@lib/rules.md}}")
	fr.files["test-wf/@lib/rules.md"] = []byte("RULES[{{include:more.md}}]")
	fr.files["test-wf/@lib/more.md"] = []byte("LIBMORE")

	var captured engine.AgentMission
	ap := &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			captured = m
			return &engine.AgentResult{Summary: "ok", Model: m.Model}, nil
		},
	}
	node := workflow.Node{
		ID:   "agent1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: "agent-default",
		},
	}
	exec := engine.NewAgentCallExecutor(fr, agentSelector(ap), agentResolver("claude-opus-4-8"), "claude-code")
	_, err := exec.Execute(context.Background(), node, storeWithWorkspace("/workspace/inst-1"), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, "Start. RULES[LIBMORE]", captured.Prompt)
}

func TestInteractive_OrchestrationFileFromLib(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/@lib/orch.md"] = []byte("LIB ORCH [{{include:part.md}}]")
	fileReader.files["test-wf/@lib/part.md"] = []byte("ORCHPART")
	fileReader.files["test-wf/template.md"] = []byte("Template content")
	llmProvider.streamCallFn = cannedStream([]string{"Hello"}, 10, 20)

	node := interactiveNode()
	node.Config.OrchestrationFile = "@lib/orch.md"

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")
	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0].Prompt, "LIB ORCH [ORCHPART]")
}

func TestGroupResolverPrompt_FromLib(t *testing.T) {
	fr := newOwnerScopedFileReader()
	owner := uuid.New()
	fr.put(owner, "wf", "@lib/resolve.md", "{{include:rules.md}}\nResolve {{taskKey}}.")
	fr.put(owner, "wf", "@lib/rules.md", "LIB RULES")

	out, err := engine.LoadGroupResolverPrompt(fr, owner, "wf", "@lib/resolve.md", samplePromptData())
	require.NoError(t, err)
	assert.Equal(t, "LIB RULES\nResolve b.", out)
	assert.Equal(t, []string{
		ownerFileKey(owner, "wf", "@lib/resolve.md"),
		ownerFileKey(owner, "wf", "@lib/rules.md"),
	}, fr.readKeys())
}
