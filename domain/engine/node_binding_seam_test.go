package engine_test

import (
	"context"
	"testing"

	adaptprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingStreamSelector wraps the real InteractiveStreamSelector and captures
// the binding the InteractiveExecutor hands it, unchanged, before delegating.
type recordingStreamSelector struct {
	inner engine.InteractiveStreamSelector
	got   []provider.ResolvedBinding
}

func (s *recordingStreamSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	s.got = append(s.got, b)
	return s.inner.StreamProviderFor(b)
}

// seamBackends are the agent backends both selection paths can route.
var seamBackends = []string{workflow.BackendClaudeCode, workflow.BackendCodex}

// batchSelectedBackend runs the real AgentCallExecutor and returns the backend
// on the binding its AgentProviderSelector received.
func batchSelectedBackend(t *testing.T, n workflow.Node, r provider.ProfileResolver, def string) string {
	t.Helper()
	fr := newMockFileReader()
	fr.files["test-wf/mission.txt"] = []byte("do work")
	sel := &capturingAgentSelector{provider: &fakeAgent{
		runFn: func(context.Context, engine.AgentMission) (*engine.AgentResult, error) {
			return &engine.AgentResult{}, nil
		},
	}}
	_, err := engine.NewAgentCallExecutor(fr, sel, r, def).
		Execute(context.Background(), n, storeWithWorkspace("/ws/inst"), "test-wf")
	require.NoError(t, err)
	return sel.got.Backend
}

// interactiveSelectedBackend runs the real InteractiveExecutor through the real
// InteractiveStreamSelector and returns the backend whose stream actually ran.
func interactiveSelectedBackend(t *testing.T, n workflow.Node, r provider.ProfileResolver, def string) string {
	t.Helper()
	instRepo, nodeExecRepo, _, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	streams := map[string]*mockStreamingChatProvider{}
	routable := map[string]adaptprovider.AgentStreamFactory{}
	for _, b := range seamBackends {
		s := newMockStreamingChatProvider()
		s.streamCallFn = cannedStream([]string{"OK"}, 1, 1)
		streams[b] = s
		routable[b] = func(provider.ResolvedBinding) engine.ChatProvider { return s }
	}
	sel := &recordingStreamSelector{inner: adaptprovider.NewInteractiveStreamSelector(nil, routable, def)}

	exec := engine.NewInteractiveExecutor(instRepo, nodeExecRepo, sel, r, fileReader, eventBus, queueMgr)
	_, err := exec.Execute(engine.ContextWithInstanceID(context.Background(), instanceID), n, variable.NewStore(), "test-wf")
	require.NoError(t, err)
	require.Len(t, sel.got, 1, "selector must receive exactly one binding")

	var ran []string
	for b, s := range streams {
		if len(s.getCapturedReqs()) > 0 {
			ran = append(ran, b)
		}
	}
	require.Len(t, ran, 1, "exactly one backend stream must run")
	return ran[0]
}

// TestResolveNodeBinding_BackendMatchesRealSelection pins the queue row's
// Backend to what the executor that runs the node actually selects, including
// a node backend that conflicts with the profile (agent_call honors it,
// interactive_agent does not) and the deployment-default fallback.
func TestResolveNodeBinding_BackendMatchesRealSelection(t *testing.T) {
	cases := []struct {
		name                        string
		nodeType                    workflow.NodeType
		nodeBackend, profileBackend string
		def                         string
	}{
		{"agent_call node beats profile", workflow.NodeTypeAgentCall, "codex", "claude-code", "claude-code"},
		{"agent_call profile beats default", workflow.NodeTypeAgentCall, "", "codex", "claude-code"},
		{"agent_call default fallback", workflow.NodeTypeAgentCall, "", "", "codex"},
		{"interactive_agent profile beats conflicting node", workflow.NodeTypeInteractiveAgent, "codex", "claude-code", "claude-code"},
		{"interactive_agent profile beats default", workflow.NodeTypeInteractiveAgent, "", "codex", "claude-code"},
		{"interactive_agent node ignored for default", workflow.NodeTypeInteractiveAgent, "codex", "", "claude-code"},
		{"interactive_agent default fallback", workflow.NodeTypeInteractiveAgent, "", "", "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeResolver(provider.Profile{
				Alias: "agent", Kind: provider.ProfileKindAgent, Model: "m", Backend: tc.profileBackend,
			})
			n := workflow.Node{
				ID:   "n",
				Type: tc.nodeType,
				Config: workflow.NodeConfig{
					ModelProfile:      "agent",
					Backend:           tc.nodeBackend,
					TemplateFile:      "mission.txt",
					OrchestrationFile: "orchestration.md",
				},
			}

			var ran string
			if tc.nodeType == workflow.NodeTypeAgentCall {
				ran = batchSelectedBackend(t, n, r, tc.def)
			} else {
				n.Config.TemplateFile = "template.md"
				ran = interactiveSelectedBackend(t, n, r, tc.def)
			}

			shown, err := engine.ResolveNodeBinding(context.Background(), r, n, tc.def)
			require.NoError(t, err)
			assert.Equal(t, ran, shown.Backend, "queue row backend must match the backend that runs")
		})
	}
}
