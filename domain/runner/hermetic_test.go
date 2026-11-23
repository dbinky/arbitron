package runner

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

func hermeticStream() <-chan engine.ChatChunk {
	ch := make(chan engine.ChatChunk)
	close(ch)
	return ch
}

func newHermeticDispatcher() *recordingDispatcher {
	return &recordingDispatcher{out: map[OpType]StepOutput{
		"chat":   {Chat: &engine.ChatResponse{Output: "routed"}},
		"stream": {Stream: hermeticStream()},
	}}
}

// routeOnce sends one call through a chat selector built with affinity and
// returns what the dispatcher was asked to do.
func routeOnce(t *testing.T, ctx context.Context, affinity func(provider.ResolvedBinding) Affinity, b provider.ResolvedBinding, req engine.ChatRequest, stream bool) (ExecRequirements, StepJob) {
	t.Helper()
	d := newHermeticDispatcher()
	p, err := newRoutingChatSelector(&innerChats{}, d, affinity).ChatProviderFor(b)
	require.NoError(t, err)
	if stream {
		_, err = p.StreamChat(ctx, req)
	} else {
		_, err = p.Chat(ctx, req)
	}
	require.NoError(t, err)
	require.Len(t, d.reqs, 1)
	return d.reqs[0], d.jobs[0]
}

func kindCtx(kind engine.OperationKind, opID string) context.Context {
	return engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{
		RunID: uuid.New(), NodeID: "n1", InvocationID: uuid.New(), OperationID: opID, Kind: kind, Attempt: 1,
	})
}

func hermeticReq() engine.ChatRequest {
	return engine.ChatRequest{Model: "claude-opus-5-5", Prompt: "summarise", WorkDir: "/srv/arbitron/workspaces/run-1"}
}

func TestHermeticChatAffinity_ByBindingKind(t *testing.T) {
	assert.Equal(t, AffinityNone, HermeticChatAffinity(provider.ResolvedBinding{Kind: provider.ProfileKindChat}))
	assert.Equal(t, AffinityBuiltIn, HermeticChatAffinity(provider.ResolvedBinding{Kind: provider.ProfileKindAgent}))
	assert.Equal(t, AffinityBuiltIn, HermeticChatAffinity(provider.ResolvedBinding{}))
}

func TestRoutedChat_LLMCallAndChildInputAreHermetic(t *testing.T) {
	runID := uuid.New()
	node := stepCtx(runID)
	child := engine.WithSubOperation(stepCtx(runID), "child-input", engine.OperationChildInput)

	for name, ctx := range map[string]context.Context{"llm_call node": node, "child input": child} {
		req, job := routeOnce(t, ctx, HermeticChatAffinity, subscriptionBinding("claude-opus-5-5"), hermeticReq(), false)
		assert.Equal(t, AffinityNone, req.Affinity, name)
		assert.Equal(t, CredAnthropicSubscription, req.Group.Kind, name)
		require.NotNil(t, job.Chat)
		assert.Equal(t, "/srv/arbitron/workspaces/run-1", job.Chat.WorkDir,
			"%s: the dispatched job keeps the work dir for a built-in placement; remote paths strip it", name)
	}

	req, _ := routeOnce(t, node, HermeticChatAffinity, subscriptionBinding("claude-opus-5-5"), hermeticReq(), true)
	assert.Equal(t, AffinityNone, req.Affinity, "a streamed llm_call through the chat selector is hermetic too")
	assert.Equal(t, OpType("stream"), req.Op)

	luna := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: provider.CanonicalLunaRoute, Model: "glm-5.3", ConnectionRef: "lunaroute"}
	req, _ = routeOnce(t, node, HermeticChatAffinity, luna, engine.ChatRequest{Model: "glm-5.3", Prompt: "p"}, false)
	assert.Equal(t, AffinityNone, req.Affinity)
	assert.Equal(t, CredLunaRoute, req.Group.Kind)
	assert.Equal(t, "lunaroute", req.Group.ConnectionRef)

	suppressed := hermeticReq()
	suppressed.ToolScope = []string{"Read"}
	suppressed.SuppressTools = true
	req, _ = routeOnce(t, node, HermeticChatAffinity, subscriptionBinding("claude-opus-5-5"), suppressed, false)
	assert.Equal(t, AffinityNone, req.Affinity, "a suppressed tool scope carries no tools")
}

func TestRoutedChat_EverythingElseStaysBuiltIn(t *testing.T) {
	runID := uuid.New()
	withTools := subscriptionBinding("claude-opus-5-5")
	withTools.AllowedTools = []string{"Read"}
	scoped := hermeticReq()
	scoped.ToolScope = []string{"Grep"}
	resume := hermeticReq()
	resume.SessionID = "sess-1"

	cases := []struct {
		name string
		ctx  context.Context
		b    provider.ResolvedBinding
		req  engine.ChatRequest
	}{
		{"interactive turn", kindCtx(engine.OperationTurn, "turn/1"), subscriptionBinding("claude-opus-5-5"), hermeticReq()},
		{"conflict resolution", kindCtx(engine.OperationResolution, "resolution/g:0:1"), subscriptionBinding("claude-opus-5-5"), hermeticReq()},
		{"probe", kindCtx(engine.OperationProbe, "probe"), subscriptionBinding("claude-opus-5-5"), hermeticReq()},
		{"no step identity", context.Background(), subscriptionBinding("claude-opus-5-5"), hermeticReq()},
		{"binding tools", stepCtx(runID), withTools, hermeticReq()},
		{"request tool scope", stepCtx(runID), subscriptionBinding("claude-opus-5-5"), scoped},
		{"session resume", stepCtx(runID), subscriptionBinding("claude-opus-5-5"), resume},
		{"agent-kind binding", stepCtx(runID), provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-opus-5-5"}, hermeticReq()},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			req, _ := routeOnce(t, tc.ctx, HermeticChatAffinity, tc.b, tc.req, stream)
			assert.Equal(t, AffinityBuiltIn, req.Affinity, "%s (stream=%v)", tc.name, stream)
		}
	}

	req, _ := routeOnce(t, stepCtx(runID), BuiltInAffinity, subscriptionBinding("claude-opus-5-5"), hermeticReq(), false)
	assert.Equal(t, AffinityBuiltIn, req.Affinity, "the Phase 1 affinity function is unchanged")

	d := newHermeticDispatcher()
	p, err := newRoutingStreamSelector(&innerChats{}, d).StreamProviderFor(subscriptionBinding("claude-opus-5-5"))
	require.NoError(t, err)
	_, err = p.StreamChat(stepCtx(runID), hermeticReq())
	require.NoError(t, err)
	require.Len(t, d.reqs, 1)
	assert.Equal(t, AffinityBuiltIn, d.reqs[0].Affinity, "the interactive stream selector never leaves the server")
}
