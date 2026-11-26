package engine_test

import (
	"context"
	"path/filepath"
	"testing"

	provideradapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInteractiveExecutor_Intent_PersistsRenderedPromptBeforeDispatch(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, instanceID := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("ORCH")
	files.files["test-wf/template.md"] = []byte("TEMPLATE")
	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "x.db"))
	exec := buildInteractiveExecutor(instRepo, nodeExecRepo, llm, files, bus, queue)
	exec.SetTurnIntents(repo)
	var atDispatch *engine.TurnIntent
	llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		in, err := repo.PendingFor(context.Background(), engine.RunLeaseKey(instanceID))
		require.NoError(t, err)
		atDispatch = in
		return cannedStream([]string{"hello"}, 1, 1)(ctx, req)
	}
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := exec.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	require.NotNil(t, atDispatch)
	assert.NotEqual(t, uuid.Nil, atDispatch.RequestID)
	assert.Equal(t, engine.TurnIntentFirst, atDispatch.Kind)
	assert.Equal(t, 1, atDispatch.TurnIndex)
	assert.Contains(t, atDispatch.Prompt, "ORCH")
	assert.Contains(t, atDispatch.Prompt, "TEMPLATE")
	assert.NotEmpty(t, atDispatch.Nonce)
	assert.Contains(t, atDispatch.Prompt, engine.SentinelOpenTag(atDispatch.Nonce))
	assert.Equal(t, "You are a helpful assistant.", atDispatch.SystemPrompt)
	assert.Equal(t, interactiveChatModel, atDispatch.Model)
	assert.Equal(t, "interact", atDispatch.NodeID)
	rec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	assert.Equal(t, atDispatch.Nonce, rec.LLMConversation[engine.ConversationKeyNonce])
	pending, err := repo.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestInteractiveExecutor_Intent_RecoveryReusesPromptAndNonce(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, instanceID := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("EDITED ORCH")
	files.files["test-wf/template.md"] = []byte("EDITED TEMPLATE")
	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "y.db"))
	// This is the same node visit; a new visit must not reuse its intent.
	invocationID := uuid.New()
	original := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), InvocationID: invocationID, LeaseKey: engine.RunLeaseKey(instanceID), RunID: instanceID, NodeID: "interact",
		TurnIndex: 1, Kind: engine.TurnIntentFirst, Prompt: "ORIGINAL RENDERED PROMPT " + engine.SentinelOpenTag("nonce-orig"),
		SystemPrompt: "original system", Nonce: "nonce-orig", Model: interactiveChatModel, Tools: []string{"Read"}, SessionGeneration: 1, OperationID: "node"}
	accepted, err := engine.CaptureAcceptedTurnBinding(provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: interactiveChatModel, AllowedTools: []string{"Read"}})
	require.NoError(t, err)
	original.Binding = accepted
	require.NoError(t, repo.Persist(context.Background(), original))
	changed := newFakeResolver(provider.Profile{Alias: interactiveChatProfile, Kind: provider.ProfileKindChat, Model: "changed-after-crash"})
	exec := engine.NewInteractiveExecutor(instRepo, nodeExecRepo, streamSelector(llm), changed, files, bus, queue)
	exec.SetTurnIntents(repo)
	exec.SetTurnBindingRestorer(provideradapter.NewTurnBindingRestorer(nil))
	llm.streamCallFn = cannedStream([]string{"resumed"}, 1, 1)
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	ctx = engine.ContextWithStepIdentity(ctx, engine.StepIdentity{RunID: instanceID, NodeID: "interact", InvocationID: invocationID, OperationID: "node"})
	_, err = exec.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	reqs := llm.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, original.Prompt, reqs[0].Prompt)
	assert.Equal(t, "original system", reqs[0].SystemPrompt)
	assert.Equal(t, []string{"Read"}, reqs[0].ToolScope)
	assert.Equal(t, interactiveChatModel, reqs[0].Model)
	rec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	assert.Equal(t, "nonce-orig", rec.LLMConversation[engine.ConversationKeyNonce])
	msgs := extractMessages(t, rec.LLMConversation)
	assert.Equal(t, original.Prompt, msgs[0]["content"])
	pending, err := repo.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestInteractiveExecutor_SetTurnIntentsRejectsNil(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, _ := interactiveTestSetup()
	exec := buildInteractiveExecutor(instRepo, nodeExecRepo, llm, files, bus, queue)
	assert.Panics(t, func() { exec.SetTurnIntents(nil) })
}
