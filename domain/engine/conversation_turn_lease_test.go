package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type turnLeaseSetter interface {
	SetTurnLeases(engine.TurnLeaseStore, time.Duration)
}

// twoProcessLeases opens the same SQLite file twice: two server processes.
func twoProcessLeases(t *testing.T) (*sqlite.TurnLeaseRepo, *sqlite.TurnLeaseRepo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "arbitron.db")
	open := func() *sqlite.TurnLeaseRepo {
		db, err := sqlite.Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		require.NoError(t, sqlite.RunMigrations(db))
		return sqlite.NewTurnLeaseRepo(db, t.Name())
	}
	return open(), open()
}

// blockingTurn makes every StreamChat block until release closes.
func blockingTurn(p *mockStreamingChatProvider, calls *int32, entered chan<- struct{}, release <-chan struct{}) {
	p.streamCallFn = func(ctx context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		atomic.AddInt32(calls, 1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		ch := make(chan engine.ChatChunk, 2)
		ch <- engine.ChatChunk{Token: "ok"}
		ch <- engine.ChatChunk{Done: true}
		close(ch)
		return ch, nil
	}
}

func TestConversationHandler_TurnLease_TwoProcessesExactlyOneRejection(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, instanceID := conversationTestSetup(t)
	leaseA, leaseB := twoProcessLeases(t)
	procA := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	procB := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	procA.(turnLeaseSetter).SetTurnLeases(leaseA, time.Minute)
	procB.(turnLeaseSetter).SetTurnLeases(leaseB, time.Minute)

	var calls int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	blockingTurn(llm, &calls, entered, release)

	errA := make(chan error, 1)
	go func() { errA <- procA.HandleMessage(convoCtx(), instanceID, "from A") }()
	<-entered
	errB := procB.HandleMessage(convoCtx(), instanceID, "from B")
	assert.ErrorIs(t, errB, instance.ErrInteractiveTurnInProgress, "the second PROCESS is rejected")
	close(release)
	require.NoError(t, <-errA)
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls), "exactly one turn reached the provider")

	// EndSession in the other process is also rejected while a turn holds the lease.
	release2 := make(chan struct{})
	blockingTurn(llm, &calls, entered, release2)
	go func() { errA <- procA.HandleMessage(convoCtx(), instanceID, "again") }()
	<-entered
	_, endErr := procB.EndSession(convoCtx(), instanceID)
	assert.ErrorIs(t, endErr, instance.ErrInteractiveTurnInProgress)
	close(release2)
	require.NoError(t, <-errA)
}

func TestConversationHandler_TurnLease_RenewalPreventsTakeover(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, instanceID := conversationTestSetup(t)
	leaseA, leaseB := twoProcessLeases(t)
	procA := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	procB := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	procA.(turnLeaseSetter).SetTurnLeases(leaseA, 300*time.Millisecond)
	procB.(turnLeaseSetter).SetTurnLeases(leaseB, 300*time.Millisecond)

	var calls int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	blockingTurn(llm, &calls, entered, release)
	errA := make(chan error, 1)
	go func() { errA <- procA.HandleMessage(convoCtx(), instanceID, "long turn") }()
	<-entered
	time.Sleep(700 * time.Millisecond) // > 2x TTL: only renewal keeps A's lease alive
	assert.ErrorIs(t, procB.HandleMessage(convoCtx(), instanceID, "overlap?"), instance.ErrInteractiveTurnInProgress)
	close(release)
	require.NoError(t, <-errA)
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

func TestConversationHandler_TurnLease_ExpiredButLiveOwnerRejectsSecondDispatch(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, instanceID := conversationTestSetup(t)
	crashed, survivor := twoProcessLeases(t)
	_, err := crashed.Acquire(context.Background(), engine.RunLeaseKey(instanceID), 50*time.Millisecond) // never renewed nor released
	require.NoError(t, err)

	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	h.(turnLeaseSetter).SetTurnLeases(survivor, time.Minute)
	var calls int32
	llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		atomic.AddInt32(&calls, 1)
		return cannedStream([]string{"back"}, 1, 1)(ctx, req)
	}
	assert.ErrorIs(t, h.HandleMessage(convoCtx(), instanceID, "too soon"), instance.ErrInteractiveTurnInProgress)
	time.Sleep(80 * time.Millisecond) // past expiry
	assert.ErrorIs(t, h.HandleMessage(convoCtx(), instanceID, "after expiry"), instance.ErrInteractiveTurnInProgress,
		"expiry alone never reclaims an occupied lease; P4 T10 recovers it with a durable proof (P4 T2 Cycle 2b)")
	assert.EqualValues(t, 0, atomic.LoadInt32(&calls), "no overlapping turn reached the provider")
}

// losingStore loses the lease on the first renewal.
type losingStore struct{ engine.TurnLeaseStore }

func (losingStore) Renew(context.Context, engine.TurnLease, time.Duration) (engine.TurnLease, error) {
	return engine.TurnLease{}, engine.ErrTurnLeaseLost
}

func TestConversationHandler_TurnLease_LostLeaseAbortsTurnWithoutPersisting(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, instanceID := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	h.(turnLeaseSetter).SetTurnLeases(losingStore{engine.NewMemoryTurnLeaseStore()}, 30*time.Millisecond)
	llm.streamCallFn = func(ctx context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk)
		go func() { <-ctx.Done(); close(ch) }()
		return ch, nil
	}
	err := h.HandleMessage(convoCtx(), instanceID, "doomed")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrTurnLeaseLost), "got %v", err)
	exec, gerr := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, gerr)
	assert.Len(t, extractMessages(t, exec.LLMConversation), 2, "nothing from the fenced turn is persisted")
}

func TestConversationHandler_SetTurnLeasesRejectsNil(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, _ := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	assert.Panics(t, func() { h.(turnLeaseSetter).SetTurnLeases(nil, time.Minute) })
	assert.Panics(t, func() { h.(turnLeaseSetter).SetTurnLeases(engine.NewMemoryTurnLeaseStore(), 0) })
}
