// domain/engine/conversation_recovery_test.go
package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rrLeaseSpy counts Acquire calls on the real in-memory lease store.
type rrLeaseSpy struct {
	engine.TurnLeaseStore
	mu       sync.Mutex
	acquires int
}

func (s *rrLeaseSpy) Acquire(ctx context.Context, key string, ttl time.Duration) (engine.TurnLease, error) {
	s.mu.Lock()
	s.acquires++
	s.mu.Unlock()
	return s.TurnLeaseStore.Acquire(ctx, key, ttl)
}

// rrIntents is an in-memory TurnIntentStore.
type rrIntents struct {
	mu   sync.Mutex
	byID map[uuid.UUID]engine.TurnIntent
}

func newRRIntents() *rrIntents { return &rrIntents{byID: map[uuid.UUID]engine.TurnIntent{}} }

func (s *rrIntents) Persist(_ context.Context, in engine.TurnIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[in.ID] = in
	return nil
}

func (s *rrIntents) Complete(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.byID[id]
	now := time.Now()
	in.CompletedAt = &now
	s.byID[id] = in
	return nil
}

func (s *rrIntents) Pending(context.Context) ([]engine.TurnIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []engine.TurnIntent
	for _, in := range s.byID {
		if in.CompletedAt == nil {
			out = append(out, in)
		}
	}
	return out, nil
}

func (s *rrIntents) PendingFor(ctx context.Context, key string) (*engine.TurnIntent, error) {
	all, _ := s.Pending(ctx)
	for i := range all {
		if all[i].LeaseKey == key {
			return &all[i], nil
		}
	}
	return nil, nil
}

// rrMover reserves the next generation with the HELD token (like the real
// mover) and records every call.
type rrMover struct {
	mu      sync.Mutex
	store   engine.TurnLeaseStore
	leases  []engine.TurnLease
	reqs    []engine.SessionMoveRequest
	results []engine.SessionMoveResult
	errs    []error
}

func (m *rrMover) MoveSession(ctx context.Context, lease engine.TurnLease, req engine.SessionMoveRequest) (engine.SessionMoveResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := len(m.reqs)
	m.leases = append(m.leases, lease)
	m.reqs = append(m.reqs, req)
	if i < len(m.errs) && m.errs[i] != nil {
		return engine.SessionMoveResult{}, m.errs[i]
	}
	bumped, err := m.store.BumpGeneration(ctx, lease) // fails unless lease is the held one
	if err != nil {
		return engine.SessionMoveResult{}, err
	}
	res := engine.SessionMoveResult{To: "mac-b", SessionID: "sid-b"}
	if i < len(m.results) {
		res = m.results[i]
	}
	res.Generation = bumped.Generation
	return res, nil
}

// interruptedThen: call 1 streams a partial token, the session-move error,
// then (optionally) late output from the superseded attempt; later calls
// stream tail + Done.
func interruptedThen(reason string, tail []string, late string) func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	var mu sync.Mutex
	calls := 0
	return func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			ch := make(chan engine.ChatChunk, 3)
			ch <- engine.ChatChunk{Token: "partial-A"}
			ch <- engine.ChatChunk{Error: &engine.SessionMoveRequiredError{Reason: reason, From: "mac-a", Err: errors.New("interrupted")}}
			if late != "" {
				ch <- engine.ChatChunk{Token: late}
			}
			close(ch)
			return ch, nil
		}
		ch := make(chan engine.ChatChunk, len(tail)+1)
		for _, tok := range tail {
			ch <- engine.ChatChunk{Token: tok}
		}
		ch <- engine.ChatChunk{Done: true, SessionID: "sid-b"}
		close(ch)
		return ch, nil
	}
}

type rrHarness struct {
	h       engine.ConversationHandler
	inst    *mockInstanceRepo
	execs   *mockNodeExecutionRepo
	llm     *mockStreamingChatProvider
	bus     *mockEventBus
	id      uuid.UUID
	leases  *rrLeaseSpy
	intents *rrIntents
	mover   *rrMover
}

func newRRHarness(t *testing.T, resolver provider.ProfileResolver, withMover bool) *rrHarness {
	t.Helper()
	instRepo, nodeExecRepo, llm, bus, queueMgr, id := conversationTestSetup(t)
	if resolver == nil {
		resolver = interactiveResolver()
	}
	h := engine.NewConversationHandler(instRepo, nodeExecRepo, streamSelector(llm), resolver, bus, queueMgr)
	r := &rrHarness{h: h, inst: instRepo, execs: nodeExecRepo, llm: llm, bus: bus, id: id,
		leases: &rrLeaseSpy{TurnLeaseStore: engine.NewMemoryTurnLeaseStore()}, intents: newRRIntents()}
	h.(interface {
		SetTurnLeases(engine.TurnLeaseStore, time.Duration)
	}).SetTurnLeases(r.leases, time.Minute)
	h.(interface{ SetTurnIntents(engine.TurnIntentStore) }).SetTurnIntents(r.intents)
	r.mover = &rrMover{store: r.leases}
	if withMover {
		h.(interface {
			SetSessionMover(engine.SessionMover, int)
		}).SetSessionMover(r.mover, 2)
	}
	return r
}

func (r *rrHarness) tokens() ([]string, []int64) {
	var toks []string
	var gens []int64
	for _, e := range r.bus.eventsOfType(r.id, engine.EventTypeInteractiveToken) {
		d := e.Data.(map[string]any)
		toks = append(toks, d["token"].(string))
		gens = append(gens, d["generation"].(int64))
	}
	return toks, gens
}

func TestConversationHandler_MidTurnExhaustionResendsSameMessageAndRestarts(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"B-reply"}, "")

	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "please refactor the parser"))

	reqs := r.llm.getCapturedReqs()
	require.Len(t, reqs, 2)
	assert.Equal(t, "please refactor the parser", reqs[0].Prompt)
	assert.Equal(t, "please refactor the parser", reqs[1].Prompt, "the SAME message is resent automatically")
	assert.Equal(t, "sid-b", reqs[1].SessionID, "resumes the imported session on the new runner")

	restarted := r.bus.eventsOfType(r.id, engine.EventTypeInteractiveTurnRestarted)
	require.Len(t, restarted, 1)
	data := restarted[0].Data.(map[string]any)
	assert.Equal(t, 2, data["turnIndex"], "second user turn of the conversation")
	assert.Equal(t, int64(2), data["generation"], "the generation the resend runs under")

	require.Len(t, r.mover.reqs, 1)
	assert.Equal(t, "exhausted", r.mover.reqs[0].Reason)
	assert.Equal(t, "mac-a", r.mover.reqs[0].From)
	assert.Equal(t, 2, r.mover.reqs[0].TurnIndex)
	assert.NotEqual(t, uuid.Nil, r.mover.reqs[0].ExecutionID)
	assert.Equal(t, 1, r.leases.acquires, "the mover reused the handler's held lease: no second Acquire, no self-deadlock")

	exec, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	msgs := extractMessages(t, exec.LLMConversation)
	require.Len(t, msgs, 4)
	assert.Equal(t, "please refactor the parser", msgs[2]["content"])
	assert.Equal(t, "B-reply", msgs[3]["content"], "the partial turn is discarded, never persisted")
	assert.Equal(t, "sid-b", exec.LLMConversation["sessionId"])

	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending, "a recovered turn completes its intent")
	_, err = r.leases.Acquire(context.Background(), engine.RunLeaseKey(r.id), time.Minute)
	assert.NoError(t, err, "lease released after the turn")
}

func TestConversationHandler_StaleGenerationLateOutputDropped(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"B-reply"}, "late-A")

	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "go"))
	toks, gens := r.tokens()
	assert.Equal(t, []string{"partial-A", "B-reply"}, toks, "superseded output never reaches clients")
	assert.Equal(t, []int64{1, 2}, gens, "tokens carry their attempt's generation so clients drop superseded ones")
	exec, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	msgs := extractMessages(t, exec.LLMConversation)
	assert.Equal(t, "B-reply", msgs[len(msgs)-1]["content"])
}

func TestConversationHandler_MidTurnRunnerLostOnAgentRestoresWorkspace(t *testing.T) {
	agent := newFakeResolver(provider.Profile{Alias: interactiveChatProfile, Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: interactiveChatModel})
	r := newRRHarness(t, agent, true)
	r.inst.mu.Lock()
	r.inst.instances[r.id].Variables[engine.ReservedVarWorkspace] = "/ws/run"
	r.inst.mu.Unlock()
	r.llm.streamCallFn = interruptedThen("runner_lost", []string{"done"}, "")

	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "continue"))
	require.Len(t, r.mover.reqs, 1)
	assert.Equal(t, "runner_lost", r.mover.reqs[0].Reason)
	assert.Equal(t, "/ws/run", r.mover.reqs[0].WorkDir)
	assert.True(t, r.mover.reqs[0].RestoreWorkspace, "an agent turn's partial edits are discarded by restoring the committed workspace")
	assert.False(t, r.mover.reqs[0].FirstTurn)
	assert.Equal(t, provider.ProfileKindAgent, r.mover.reqs[0].Binding.Kind)
	reqs := r.llm.getCapturedReqs()
	require.Len(t, reqs, 2)
	assert.Equal(t, "continue", reqs[1].Prompt)
	assert.Equal(t, "/ws/run", reqs[1].WorkDir)
}

func TestConversationHandler_ChatTurnMoveDoesNotRestoreWorkspace(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.llm.streamCallFn = interruptedThen("logged_out", []string{"ok"}, "")
	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "hi"))
	require.Len(t, r.mover.reqs, 1)
	assert.Equal(t, "logged_out", r.mover.reqs[0].Reason)
	assert.False(t, r.mover.reqs[0].RestoreWorkspace)
}

func TestConversationHandler_MidTurnRebuildUsesTranscriptPrimer(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.mover.results = []engine.SessionMoveResult{{To: "mac-b", PendingRebuild: true, Lossy: true, Notice: engine.SessionLossNotice}}
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"rebuilt-reply"}, "")

	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "the exact message"))
	reqs := r.llm.getCapturedReqs()
	require.Len(t, reqs, 2)
	assert.Equal(t, "", reqs[1].SessionID, "fresh provider session")
	assert.True(t, strings.HasPrefix(reqs[1].Prompt, "Initial orchestration prompt"), "the original first prompt leads")
	assert.Contains(t, reqs[1].Prompt, "Hello! How can I help you?", "the recorded transcript is included")
	assert.True(t, strings.HasSuffix(reqs[1].Prompt, "the exact message"))

	exec, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	_, pending := exec.LLMConversation[engine.ConversationKeyPendingRebuild]
	assert.False(t, pending, "a successful turn clears the pending rebuild")
	msgs := extractMessages(t, exec.LLMConversation)
	assert.Equal(t, "the exact message", msgs[2]["content"], "the transcript records the user's message, not the primer")
	assert.Equal(t, "sid-b", exec.LLMConversation["sessionId"], "the fresh session id is persisted")
}

func TestConversationHandler_PendingRebuildBetweenTurnsPrimesNextMessage(t *testing.T) {
	r := newRRHarness(t, nil, true)
	exec, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	exec.LLMConversation[engine.ConversationKeyPendingRebuild] = true
	exec.LLMConversation["sessionId"] = ""
	require.NoError(t, r.execs.Update(context.Background(), exec))
	r.llm.streamCallFn = cannedStreamWithSession([]string{"ok"}, "sid-new")

	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "next"))
	reqs := r.llm.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(reqs[0].Prompt, "Initial orchestration prompt"))
	assert.True(t, strings.HasSuffix(reqs[0].Prompt, "next"))
	assert.Equal(t, "", reqs[0].SessionID)
	exec, err = r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	assert.Equal(t, "sid-new", exec.LLMConversation["sessionId"])
	_, pending := exec.LLMConversation[engine.ConversationKeyPendingRebuild]
	assert.False(t, pending)
}

func TestConversationHandler_MidTurnNoTargetLeavesIntentPendingAndPersistsNothing(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.mover.errs = []error{&engine.SessionWaitError{Kind: "capacity", Group: "anthropic_subscription:anthropic", Model: "m"}}
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"never"}, "")
	before, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	beforeLen := len(extractMessages(t, before.LLMConversation))

	err = r.h.HandleMessage(convoCtx(), r.id, "hello")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrNoSessionTarget))
	assert.Contains(t, err.Error(), "anthropic_subscription:anthropic", "the wait names the missing group")

	after, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	assert.Len(t, extractMessages(t, after.LLMConversation), beforeLen)
	assert.Len(t, r.llm.getCapturedReqs(), 1, "no resend without a target")
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1, "the accepted message stays durable for the recovery controller (P4 T10)")
	assert.Equal(t, "hello", pending[0].Message)
}

func TestConversationHandler_RecoveryBoundedByMaxMoves(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.llm.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Error: &engine.SessionMoveRequiredError{Reason: "exhausted", From: "mac-x", Err: errors.New("limit")}}
		close(ch)
		return ch, nil
	}
	err := r.h.HandleMessage(convoCtx(), r.id, "x")
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrRecoveryBudgetExhausted)
	assert.NotErrorIs(t, err, engine.ErrSessionRestoreFailed, "a pass budget is not permanent restoration failure")
	assert.Len(t, r.mover.reqs, 2, "maxMoves=2 bounds automatic recovery per pass")
	assert.Len(t, r.llm.getCapturedReqs(), 3)
}

func TestConversationHandler_WithoutMoverInterruptionIsTurnError(t *testing.T) {
	r := newRRHarness(t, nil, false)
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"B"}, "")
	err := r.h.HandleMessage(convoCtx(), r.id, "x")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrSessionMoveRequired))
	assert.Empty(t, r.bus.eventsOfType(r.id, engine.EventTypeInteractiveTurnRestarted))
	assert.Len(t, r.llm.getCapturedReqs(), 1)
}

func TestConversationHandler_SetSessionMoverRejectsNil(t *testing.T) {
	r := newRRHarness(t, nil, false)
	assert.Panics(t, func() {
		r.h.(interface {
			SetSessionMover(engine.SessionMover, int)
		}).SetSessionMover(nil, 2)
	})
}

// Edge (alignment): a replay never republishes the user's message.
func TestTurnRecovery_DuplicateUserMessageIsNotPublishedOnReplay(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"B"}, "")
	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "only once"))
	n := 0
	for _, e := range r.bus.eventsOfType(r.id, engine.EventTypeInteractiveMessage) {
		if d := e.Data.(map[string]any); d["role"] == "user" && d["content"] == "only once" {
			n++
		}
	}
	assert.Equal(t, 1, n, "one user message per request, even across a move and resend")
}

// Edge (alignment): an exhausted move budget yields to P4 T10's retry worker.
// The accepted message stays PENDING, never completed or dropped.
func TestTurnRecovery_MoveBudgetYieldsWithoutDroppingIntent(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.llm.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Error: &engine.SessionMoveRequiredError{Reason: "exhausted", From: "mac-x", Err: errors.New("limit")}}
		close(ch)
		return ch, nil
	}
	err := r.h.HandleMessage(convoCtx(), r.id, "keep me")
	require.ErrorIs(t, err, engine.ErrRecoveryBudgetExhausted)
	pending, perr := r.intents.Pending(context.Background())
	require.NoError(t, perr)
	require.Len(t, pending, 1, "the accepted message stays durable for the retry worker")
	assert.Equal(t, "keep me", pending[0].Message)
}
