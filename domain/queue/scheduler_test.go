package queue_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock handler for scheduler tests
// ---------------------------------------------------------------------------

type firedEvent struct {
	ID        uuid.UUID
	TimerType string
	FiredAt   time.Time
}

type mockTimerHandler struct {
	mu     sync.Mutex
	events []firedEvent
}

func (h *mockTimerHandler) OnTimerFired(_ context.Context, instanceID uuid.UUID, timerType string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, firedEvent{ID: instanceID, TimerType: timerType, FiredAt: time.Now()})
	return nil
}

func (h *mockTimerHandler) getEvents() []firedEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]firedEvent, len(h.events))
	copy(cp, h.events)
	return cp
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestScheduler_SingleTimer_FiresAtCorrectTime(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	id := uuid.New()
	target := time.Now().Add(50 * time.Millisecond)
	s.Schedule(id, target, queue.TimerTypeDelay)

	time.Sleep(150 * time.Millisecond)

	events := handler.getEvents()
	require.Len(t, events, 1)
	assert.Equal(t, id, events[0].ID)
	assert.Equal(t, queue.TimerTypeDelay, events[0].TimerType)
	// Should have fired within reasonable tolerance of target.
	assert.WithinDuration(t, target, events[0].FiredAt, 50*time.Millisecond)
}

func TestScheduler_MultipleTimers_EarliestFiresFirst(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()

	now := time.Now()
	s.Schedule(id3, now.Add(150*time.Millisecond), queue.TimerTypeDelay)
	s.Schedule(id1, now.Add(50*time.Millisecond), queue.TimerTypeDelay)
	s.Schedule(id2, now.Add(100*time.Millisecond), queue.TimerTypeDelay)

	time.Sleep(250 * time.Millisecond)

	events := handler.getEvents()
	require.Len(t, events, 3)
	// Verify firing order.
	assert.Equal(t, id1, events[0].ID)
	assert.Equal(t, id2, events[1].ID)
	assert.Equal(t, id3, events[2].ID)
}

func TestScheduler_Cancel_RemovesTimer(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	id := uuid.New()
	s.Schedule(id, time.Now().Add(100*time.Millisecond), queue.TimerTypeDelay)
	s.Cancel(id)

	time.Sleep(200 * time.Millisecond)

	events := handler.getEvents()
	assert.Empty(t, events)
}

func TestScheduler_Cancel_RecalculatesNext(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	id1 := uuid.New()
	id2 := uuid.New()

	s.Schedule(id1, time.Now().Add(50*time.Millisecond), queue.TimerTypeDelay)
	s.Schedule(id2, time.Now().Add(150*time.Millisecond), queue.TimerTypeDelay)

	// Cancel the earlier one.
	s.Cancel(id1)

	time.Sleep(250 * time.Millisecond)

	events := handler.getEvents()
	require.Len(t, events, 1)
	assert.Equal(t, id2, events[0].ID)
}

func TestScheduler_ScheduleEarlierThanCurrent(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	id1 := uuid.New()
	id2 := uuid.New()

	s.Schedule(id1, time.Now().Add(200*time.Millisecond), queue.TimerTypeDelay)
	// Schedule an earlier timer — should fire first.
	s.Schedule(id2, time.Now().Add(50*time.Millisecond), queue.TimerTypeDelay)

	time.Sleep(300 * time.Millisecond)

	events := handler.getEvents()
	require.Len(t, events, 2)
	assert.Equal(t, id2, events[0].ID)
	assert.Equal(t, id1, events[1].ID)
}

func TestScheduler_MultipleExpireSimultaneously(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	target := time.Now().Add(50 * time.Millisecond)
	id1 := uuid.New()
	id2 := uuid.New()
	s.Schedule(id1, target, queue.TimerTypeDelay)
	s.Schedule(id2, target, queue.TimerTypeDelay)

	time.Sleep(150 * time.Millisecond)

	events := handler.getEvents()
	assert.Len(t, events, 2)
	firedIDs := []uuid.UUID{events[0].ID, events[1].ID}
	assert.Contains(t, firedIDs, id1)
	assert.Contains(t, firedIDs, id2)
}

func TestScheduler_CancelNonExistent_NoOp(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	// Should not panic.
	s.Cancel(uuid.New())
	assert.Equal(t, 0, s.Pending())
}

func TestScheduler_Stop_GracefulShutdown(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// Schedule a timer that won't fire.
	s.Schedule(uuid.New(), time.Now().Add(10*time.Second), queue.TimerTypeDelay)

	// Stop should return promptly.
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within timeout")
	}
}

func TestScheduler_ConcurrentScheduleCancel(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		id := uuid.New()
		go func() {
			defer wg.Done()
			s.Schedule(id, time.Now().Add(time.Second), queue.TimerTypeDelay)
		}()
		go func() {
			defer wg.Done()
			s.Cancel(id)
		}()
	}
	wg.Wait()
}

func TestScheduler_TimerType_PassedToHandler(t *testing.T) {
	handler := &mockTimerHandler{}
	s := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	id := uuid.New()
	s.Schedule(id, time.Now().Add(10*time.Millisecond), queue.TimerTypeWebhook)

	time.Sleep(100 * time.Millisecond)

	events := handler.getEvents()
	require.Len(t, events, 1)
	assert.Equal(t, queue.TimerTypeWebhook, events[0].TimerType)
}

// ---------------------------------------------------------------------------
// Tests: OnTimerFired on the Manager
// ---------------------------------------------------------------------------

func TestOnTimerFired_InstanceStillWaiting_TransitionsToQueued(t *testing.T) {
	qr := newMockQueueRepo()
	instRepo := newMockInstanceRepo()
	starter := newMockStarter()

	id := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:    id,
		State: instance.StateWaiting,
	}
	instRepo.mu.Unlock()

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, instRepo, cfg, starter)

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeDelay)
	require.NoError(t, err)

	qr.mu.Lock()
	assert.Contains(t, qr.setQueuedCalls, id)
	qr.mu.Unlock()
}

func TestOnTimerFired_InstanceNoLongerWaiting_NoOp(t *testing.T) {
	qr := newMockQueueRepo()
	instRepo := newMockInstanceRepo()
	starter := newMockStarter()

	id := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:    id,
		State: instance.StatePaused, // not waiting
	}
	instRepo.mu.Unlock()

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, instRepo, cfg, starter)

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeDelay)
	require.NoError(t, err)

	qr.mu.Lock()
	assert.Empty(t, qr.setQueuedCalls)
	qr.mu.Unlock()
}
