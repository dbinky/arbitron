package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func immediateCompletionStream(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if _, deferred := engine.StepCompletionSinkFromContext(ctx); deferred {
		return nil, errors.New("legacy caller has no turn committer to consume a deferred receipt")
	}
	return cannedStreamWithSession([]string{"committed directly"}, "legacy-session")(ctx, req)
}
func TestConversationHandler_CheckpointUnwiredDoesNotDeferTheStreamJob(t *testing.T) {
	instRepo, execs, llm, bus, queue, id := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, execs, llm, bus, queue)
	llm.streamCallFn = immediateCompletionStream
	require.NoError(t, h.HandleMessage(convoCtx(), id, "next"))
	stored, err := execs.GetByInstanceAndNode(context.Background(), id, "interact")
	require.NoError(t, err)
	assert.Equal(t, "legacy-session", engine.SessionID(stored.LLMConversation))
	assert.Len(t, extractMessages(t, stored.LLMConversation), 4)
}
func TestInteractiveExecutor_CheckpointUnwiredDoesNotInventADeferredCollector(t *testing.T) {
	instRepo, execs, llm, files, bus, queue, id := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("o")
	files.files["test-wf/template.md"] = []byte("t")
	exec := buildInteractiveExecutor(instRepo, execs, llm, files, bus, queue)
	llm.streamCallFn = immediateCompletionStream
	_, err := exec.Execute(engine.ContextWithInstanceID(context.Background(), id), interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	stored, err := execs.GetByInstanceAndNode(context.Background(), id, "interact")
	require.NoError(t, err)
	assert.Equal(t, "legacy-session", engine.SessionID(stored.LLMConversation))
}
