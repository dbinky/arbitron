package engine_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rrFencer is a generation-fenced conversation writer over the mock repo.
type rrFencer struct {
	mu    sync.Mutex
	execs *mockNodeExecutionRepo
	runID uuid.UUID
	stale bool // simulate a concurrent move that advanced the generation
	calls []rrPatch
}

type rrPatch struct {
	gen   int64
	patch map[string]any
}

func (f *rrFencer) PatchSessionFieldsAtGeneration(ctx context.Context, id uuid.UUID, gen int64, patch map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rrPatch{gen: gen, patch: patch})
	if f.stale {
		return instance.ErrStaleSessionGeneration
	}
	exec, err := f.execs.GetByInstanceAndNode(ctx, f.runID, "interact")
	if err != nil {
		return err
	}
	if exec.ID != id {
		return instance.ErrNotFound
	}
	if exec.SessionGeneration != gen {
		return instance.ErrStaleSessionGeneration
	}
	conv := map[string]any{}
	for k, v := range exec.LLMConversation {
		conv[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(conv, k)
		} else {
			conv[k] = v
		}
	}
	exec.LLMConversation = conv
	return f.execs.Update(ctx, exec)
}

type rrRestorer struct {
	mu        sync.Mutex
	calls     []uuid.UUID
	runID     uuid.UUID
	dir, base string
}

func (r *rrRestorer) RestoreBuiltIn(_ context.Context, runID uuid.UUID, dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.runID || dir != r.dir {
		return fmt.Errorf("wrong restore target: %s %q", runID, dir)
	}
	r.calls = append(r.calls, runID)
	data, err := os.ReadFile(filepath.Join(r.base, "state.txt"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "state.txt"), data, 0o600)
}

// withProvisional seeds a session the mover committed as a provisional native
// import at the lease's current generation (a fresh memory store starts at 1),
// and installs the fencer and restorer.
func (r *rrHarness) withProvisional(t *testing.T, provisional bool) (*rrFencer, *rrRestorer) {
	t.Helper()
	ctx := context.Background()
	exec, err := r.execs.GetByInstanceAndNode(ctx, r.id, "interact")
	require.NoError(t, err)
	conv := map[string]any{"sessionId": "sid-b", "nonce": "n-1", "messages": []any{
		map[string]any{"role": "user", "content": "FIRST PROMPT n-1"}, map[string]any{"role": "assistant", "content": "hi"}}}
	if provisional {
		conv[engine.ConversationKeyProvisionalResume] = true
	}
	exec.LLMConversation, exec.SessionGeneration = conv, 1
	require.NoError(t, r.execs.Update(ctx, exec))
	dir, base := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.txt"), []byte("partial edits"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "state.txt"), []byte("committed bytes"), 0o600))
	setWorkspaceVar(r.inst, r.id, dir)
	fencer, restorer := &rrFencer{execs: r.execs, runID: r.id}, &rrRestorer{runID: r.id, dir: dir, base: base}
	r.h.(interface {
		SetConversationFencer(instance.SessionConversationFencer)
	}).SetConversationFencer(fencer)
	r.h.(interface {
		SetWorkspaceRestorer(engine.CommittedWorkspaceRestorer)
	}).SetWorkspaceRestorer(restorer)
	return fencer, restorer
}

// refusedThen: call 1 is the harness refusing the provisional native resume;
// call 2 streams the rebuilt reply, or fails with second.
func refusedThen(reply string, second error) func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	var mu sync.Mutex
	calls := 0
	return func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		ch := make(chan engine.ChatChunk, 2)
		switch {
		case n == 1:
			ch <- engine.ChatChunk{Error: errors.New("No conversation found with session ID: sid-b")}
		case second != nil:
			ch <- engine.ChatChunk{Error: second}
		default:
			ch <- engine.ChatChunk{Token: reply}
			ch <- engine.ChatChunk{Done: true, SessionID: "sid-c"}
		}
		close(ch)
		return ch, nil
	}
}

// Success (alignment): a refused first resume of a provisional native import
// is handled by ONE fenced fallback: marker and session id cleared at the
// held generation, original prompt + transcript rebuilt, the SAME message sent
// once more, and the turn commits. The mover is never called again.
func TestTurnRecovery_ProvisionalNativeResumeRefusalRebuildsAndCommits(t *testing.T) {
	r := newRRHarness(t, nil, true)
	fencer, _ := r.withProvisional(t, true)
	r.llm.streamCallFn = refusedThen("C-reply", nil)

	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "continue the parser"))

	reqs := r.llm.getCapturedReqs()
	require.Len(t, reqs, 2, "exactly one resend")
	assert.Equal(t, "sid-b", reqs[0].SessionID, "the first attempt resumed the provisional import")
	assert.Equal(t, "continue the parser", reqs[0].Prompt)
	assert.Empty(t, reqs[1].SessionID, "the rebuild starts a fresh provider session")
	assert.Contains(t, reqs[1].Prompt, "FIRST PROMPT n-1", "original prompt (same nonce)")
	assert.Contains(t, reqs[1].Prompt, "hi", "the recorded transcript")
	assert.Contains(t, reqs[1].Prompt, "continue the parser", "the exact intent message")
	assert.Empty(t, r.mover.reqs, "the mover stays move-only")

	require.Len(t, fencer.calls, 1, "one fenced fallback")
	assert.Equal(t, int64(1), fencer.calls[0].gen, "fenced by the held session generation")
	assert.Equal(t, map[string]any{engine.ConversationKeyProvisionalResume: nil, "sessionId": nil,
		engine.ConversationKeyPendingRebuild: true}, fencer.calls[0].patch)

	exec, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	_, prov := exec.LLMConversation[engine.ConversationKeyProvisionalResume]
	_, rebuild := exec.LLMConversation[engine.ConversationKeyPendingRebuild]
	assert.False(t, prov, "the commit never resurrects the refused provisional session")
	assert.False(t, rebuild, "a successful turn ends the pending rebuild")
	assert.Equal(t, "sid-c", exec.LLMConversation["sessionId"])
	msgs := extractMessages(t, exec.LLMConversation)
	require.Len(t, msgs, 4)
	assert.Equal(t, "continue the parser", msgs[2]["content"])
	assert.Equal(t, "C-reply", msgs[3]["content"])
	assert.Len(t, r.bus.eventsOfType(r.id, engine.EventTypeInteractiveContextNotice), 1, "the rebuild is visibly lossy")
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestTurnRecovery_RebuildWithoutSessionIDNeverResurrectsRefusedID(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.withProvisional(t, true)
	calls := 0
	r.llm.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("native resume refused")
		}
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Token: "rebuilt", Done: true}
		close(ch)
		return ch, nil
	}
	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "continue"))
	got, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	assert.Empty(t, engine.SessionID(got.LLMConversation), "the pre-fallback native id was explicitly refused")
}

// Failure: a second deterministic failure after the fallback is a visible
// restoration error. There is no third attempt and no loop.
func TestTurnRecovery_SecondFailureAfterProvisionalFallbackIsRestorationError(t *testing.T) {
	r := newRRHarness(t, nil, true)
	fencer, _ := r.withProvisional(t, true)
	r.llm.streamCallFn = refusedThen("", errors.New("adapter refused again"))

	err := r.h.HandleMessage(convoCtx(), r.id, "continue the parser")
	require.ErrorIs(t, err, engine.ErrSessionRestoreFailed)
	assert.Len(t, r.llm.getCapturedReqs(), 2, "never a third attempt")
	assert.Len(t, fencer.calls, 1, "never a second fallback")
	assert.Empty(t, r.mover.reqs)
}

// Error: the fallback patch is fenced. If another writer moved the session,
// nothing is written, nothing is resent, and the turn reports the fence.
func TestTurnRecovery_ProvisionalFallbackIsFencedByGeneration(t *testing.T) {
	r := newRRHarness(t, nil, true)
	fencer, restorer := r.withProvisional(t, true)
	fencer.stale = true
	r.llm.streamCallFn = refusedThen("C-reply", nil)

	err := r.h.HandleMessage(convoCtx(), r.id, "continue the parser")
	require.ErrorIs(t, err, engine.ErrTurnFenced)
	assert.Len(t, r.llm.getCapturedReqs(), 1, "no resend from a fenced fallback")
	assert.Empty(t, restorer.calls)
	exec, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	assert.Equal(t, true, exec.LLMConversation[engine.ConversationKeyProvisionalResume], "nothing was written")
}

// Edge: without the provisional marker, a provider failure is an ordinary
// turn error. No fallback, no patch, no resend.
func TestTurnRecovery_NonProvisionalFailureTakesNoFallback(t *testing.T) {
	r := newRRHarness(t, nil, true)
	fencer, _ := r.withProvisional(t, false)
	r.llm.streamCallFn = refusedThen("C-reply", nil)

	err := r.h.HandleMessage(convoCtx(), r.id, "continue the parser")
	require.Error(t, err)
	assert.NotErrorIs(t, err, engine.ErrSessionRestoreFailed)
	assert.Len(t, r.llm.getCapturedReqs(), 1)
	assert.Empty(t, fencer.calls)
}

// Edge: an agent turn restores the last committed workspace before the resend.
func TestTurnRecovery_ProvisionalFallbackRestoresCommittedWorkspaceForAgents(t *testing.T) {
	agent := newFakeResolver(provider.Profile{Alias: interactiveChatProfile, Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: interactiveChatModel})
	r := newRRHarness(t, agent, true)
	_, restorer := r.withProvisional(t, true)
	r.llm.streamCallFn = refusedThen("C-reply", nil)

	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "continue the parser"))
	assert.Equal(t, []uuid.UUID{r.id}, restorer.calls, "one restore of the run's committed workspace")
	assert.Len(t, r.llm.getCapturedReqs(), 2)
	data, err := os.ReadFile(filepath.Join(restorer.dir, "state.txt"))
	require.NoError(t, err)
	assert.Equal(t, "committed bytes", string(data))
}

// Match the real mover's durable native-import patch; the handler's original
// read must not hide a newly provisional session in this same turn.
type nativeReceiptMove struct{ r *rrHarness }

func (m nativeReceiptMove) MoveSession(ctx context.Context, lease engine.TurnLease, req engine.SessionMoveRequest) (engine.SessionMoveResult, error) {
	res, err := m.r.mover.MoveSession(ctx, lease, req)
	if err != nil {
		return res, err
	}
	stored, err := m.r.execs.GetByInstanceAndNode(ctx, req.RunID, req.NodeID)
	if err != nil {
		return res, err
	}
	if stored.ID != req.ExecutionID {
		return res, instance.ErrNotFound
	}
	exec := *stored
	exec.LLMConversation = maps.Clone(stored.LLMConversation)
	exec.LLMConversation[engine.ConversationKeyProvisionalResume] = true
	exec.LLMConversation["sessionId"] = res.SessionID
	exec.SessionGeneration = res.Generation
	return res, m.r.execs.Update(ctx, &exec)
}

func TestTurnRecovery_ProvisionalNativeRefusalAfterMidTurnMoveCommitsRebuild(t *testing.T) {
	r := newRRHarness(t, nil, true)
	fencer, _ := r.withProvisional(t, false)
	r.h.(interface {
		SetSessionMover(engine.SessionMover, int)
	}).SetSessionMover(nativeReceiptMove{r}, 2)
	f := newCkFixture()
	f.committer.apply = func(exec *instance.NodeExecution) error { return r.execs.Update(context.Background(), exec) }
	f.committer.finish = func(id uuid.UUID) { require.NoError(t, r.intents.Complete(context.Background(), id)) }
	r.h.(sessionCheckpointSetter).SetSessionCheckpoints(f.deps())
	calls := 0
	var operations []string
	r.llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		calls++
		id, ok := engine.StepIdentityFromContext(ctx)
		require.True(t, ok)
		operations = append(operations, id.OperationID)
		for _, prior := range operations[:len(operations)-1] {
			if prior == id.OperationID {
				return nil, fmt.Errorf("attempt reuses terminal ledger identity %q", id.OperationID)
			}
		}
		switch calls {
		case 1:
			return nil, &engine.SessionMoveRequiredError{From: "mac-a", Reason: engine.MoveReasonExhausted}
		case 2:
			assert.Equal(t, "sid-b", req.SessionID)
			return nil, errors.New("native resume refused")
		default:
			assert.Empty(t, req.SessionID)
			assert.Contains(t, req.Prompt, "FIRST PROMPT n-1")
			assert.Contains(t, req.Prompt, "continue exactly")
			return receiptStream("mac-b", false, "sid-c", "rebuilt committed reply")(ctx, req)
		}
	}
	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "continue exactly"))
	assert.Equal(t, 3, calls)
	require.Len(t, fencer.calls, 1)
	assert.Equal(t, int64(2), fencer.calls[0].gen)
	require.Len(t, f.committer.commits, 1)
	assert.Equal(t, int64(2), f.committer.commits[0].SessionGeneration)
	got, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	assert.Equal(t, "sid-c", got.LLMConversation["sessionId"])
	assert.NotContains(t, got.LLMConversation, engine.ConversationKeyProvisionalResume)
	assert.NotContains(t, got.LLMConversation, engine.ConversationKeyPendingRebuild)
	assert.Equal(t, "rebuilt committed reply", extractMessages(t, got.LLMConversation)[3]["content"])
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending)
}
