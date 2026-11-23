package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePins struct {
	pin   RunnerID
	ok    bool
	err   error
	calls []string
}

func (p *fakePins) PinFor(_ context.Context, runID uuid.UUID, nodeID string) (RunnerID, bool, error) {
	p.calls = append(p.calls, runID.String()+"/"+nodeID)
	return p.pin, p.ok, p.err
}

func streamOK() map[OpType]StepOutput {
	ch := make(chan engine.ChatChunk)
	close(ch)
	return map[OpType]StepOutput{"stream": {Stream: ch}}
}

func turnCtx(runID uuid.UUID, op string) context.Context {
	return engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{
		RunID: runID, NodeID: "n1", InvocationID: uuid.New(), OperationID: op, Kind: engine.OperationTurn, Attempt: 1,
	})
}

func agentBinding() provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"}
}

func TestSessionStream_PinnedTurnCarriesPin(t *testing.T) {
	pins := &fakePins{pin: "mac-a", ok: true}
	d := &recordingDispatcher{out: streamOK()}
	sel := streamSelectorFor(&innerChats{}, d, pins)
	p, err := sel.StreamProviderFor(agentBinding())
	require.NoError(t, err)
	runID := uuid.New()
	_, err = p.StreamChat(turnCtx(runID, "turn/3"), engine.ChatRequest{Prompt: "next", SessionID: "s1"})
	require.NoError(t, err)
	require.Len(t, d.reqs, 1)
	assert.Equal(t, AffinitySession, d.reqs[0].Affinity)
	assert.Equal(t, RunnerID("mac-a"), d.reqs[0].PinnedRunner)
	assert.Equal(t, runID, d.reqs[0].RunID)
	assert.Equal(t, []string{runID.String() + "/n1"}, pins.calls, "looked up by the turn's run and node")
	assert.Equal(t, "s1", d.jobs[0].Chat.SessionID)
}

func TestSessionStream_FirstTurnHasNoPin(t *testing.T) {
	d := &recordingDispatcher{out: streamOK()}
	p, err := streamSelectorFor(&innerChats{}, d, &fakePins{}).StreamProviderFor(agentBinding())
	require.NoError(t, err)
	_, err = p.StreamChat(turnCtx(uuid.New(), "node"), engine.ChatRequest{Prompt: "first"})
	require.NoError(t, err)
	assert.Equal(t, AffinitySession, d.reqs[0].Affinity)
	assert.Empty(t, d.reqs[0].PinnedRunner, "placed like a workspace step by Place")
}

func TestSessionStream_LegacySessionPinsToBuiltIn(t *testing.T) {
	d := &recordingDispatcher{out: streamOK()}
	p, err := streamSelectorFor(&innerChats{}, d, &fakePins{}).StreamProviderFor(subscriptionBinding("claude-sonnet-4-5"))
	require.NoError(t, err)
	_, err = p.StreamChat(turnCtx(uuid.New(), "turn/2"), engine.ChatRequest{Prompt: "x", SessionID: "pre-phase-4"})
	require.NoError(t, err)
	assert.Equal(t, AffinitySession, d.reqs[0].Affinity)
	assert.Equal(t, BuiltInRunnerID, d.reqs[0].PinnedRunner, "its session file lives on the server")
}

func TestSessionStream_StatelessChatAndResolutionTurns(t *testing.T) {
	pins := &fakePins{pin: "mac-a", ok: true}
	d := &recordingDispatcher{out: streamOK()}
	api := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "openrouter", Model: "gpt-6", ConnectionRef: "openrouter-main"}
	p, err := streamSelectorFor(&innerChats{}, d, pins).StreamProviderFor(api)
	require.NoError(t, err)
	_, _ = p.StreamChat(turnCtx(uuid.New(), "turn/2"), engine.ChatRequest{Prompt: "x"})
	if assert.Len(t, d.reqs, 1) {
		assert.Equal(t, AffinityNone, d.reqs[0].Affinity, "API chat has no session file to pin")
		assert.Empty(t, d.reqs[0].PinnedRunner)
	}
	assert.Empty(t, pins.calls)

	p, err = streamSelectorFor(&innerChats{}, d, pins).StreamProviderFor(agentBinding())
	require.NoError(t, err)
	_, err = p.StreamChat(turnCtx(uuid.New(), "resolution/"+uuid.NewString()+":1:1/turn/2"), engine.ChatRequest{Prompt: "x", SessionID: "s"})
	require.NoError(t, err)
	assert.Equal(t, AffinityBuiltIn, d.reqs[1].Affinity, "group staging sessions are pinned by P4 T11")
	assert.Empty(t, pins.calls)
}

func TestSessionStream_PinLookupFailureNeverGuesses(t *testing.T) {
	d := &recordingDispatcher{out: streamOK()}
	p, err := streamSelectorFor(&innerChats{}, d, &fakePins{err: errors.New("db locked")}).StreamProviderFor(agentBinding())
	require.NoError(t, err)
	_, err = p.StreamChat(turnCtx(uuid.New(), "turn/2"), engine.ChatRequest{Prompt: "x", SessionID: "s"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db locked")
	assert.Empty(t, d.reqs, "no dispatch without the pin")
}

func TestStreamSelectorFor_NilPinsKeepsPhaseOneRouting(t *testing.T) {
	d := &recordingDispatcher{out: streamOK()}
	p, err := streamSelectorFor(&innerChats{}, d, nil).StreamProviderFor(agentBinding())
	require.NoError(t, err)
	_, err = p.StreamChat(turnCtx(uuid.New(), "turn/2"), engine.ChatRequest{Prompt: "x", SessionID: "s"})
	require.NoError(t, err)
	assert.Equal(t, AffinityBuiltIn, d.reqs[0].Affinity)

	_, err = streamSelectorFor(&innerChats{}, d, &fakePins{}).StreamProviderFor(subscriptionBinding("refused"))
	assert.Same(t, errInnerRefused, err, "inner validation still runs first")
}
