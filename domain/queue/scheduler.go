package queue

import (
	"context"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// Timer type constants.
const (
	TimerTypeDelay   = "timer_delay"
	TimerTypeWebhook = "webhook_timeout"
)

// TimerFiredHandler is called when a timer expires for an instance.
type TimerFiredHandler interface {
	OnTimerFired(ctx context.Context, instanceID uuid.UUID, timerType string) error
}

// timerEntry holds the waiting-until time and timer type for a single instance.
type timerEntry struct {
	WaitingUntil time.Time
	Type         string
}

// Scheduler manages in-memory timers for waiting instances using a single
// time.Timer set to the earliest pending expiration (hybrid single-timer design).
type Scheduler struct {
	mu      sync.Mutex
	timers  map[uuid.UUID]timerEntry
	timer   *time.Timer
	handler TimerFiredHandler
	stopCh  chan struct{}
	doneCh  chan struct{}
	// wakeCh is used to notify the loop goroutine when timers change.
	wakeCh chan struct{}
}

// NewScheduler constructs a new Scheduler.
func NewScheduler(handler TimerFiredHandler) *Scheduler {
	return &Scheduler{
		timers: make(map[uuid.UUID]timerEntry),
		handler: handler,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		wakeCh: make(chan struct{}, 1),
	}
}

// Schedule adds or updates a timer for the given instance. If the new timer is
// earlier than the current active timer, it resets.
func (s *Scheduler) Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string) {
	s.mu.Lock()
	s.timers[instanceID] = timerEntry{WaitingUntil: waitingUntil, Type: timerType}
	s.resetTimerLocked()
	s.mu.Unlock()
	s.notifyWake()
}

// Cancel removes a timer for the given instance. No-op if the instance is not scheduled.
func (s *Scheduler) Cancel(instanceID uuid.UUID) {
	s.mu.Lock()
	_, exists := s.timers[instanceID]
	if exists {
		delete(s.timers, instanceID)
		s.resetTimerLocked()
	}
	s.mu.Unlock()
	if exists {
		s.notifyWake()
	}
}

// Start launches the scheduler goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

// Stop shuts down the scheduler goroutine gracefully.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	<-s.doneCh
}

// Pending returns the number of pending timers (useful for testing).
func (s *Scheduler) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}

func (s *Scheduler) notifyWake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.doneCh)

	for {
		s.mu.Lock()
		timer := s.timer
		s.mu.Unlock()

		if timer == nil {
			// No pending timers — wait for a signal or shutdown.
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-s.wakeCh:
				continue
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-s.wakeCh:
			// Timer map changed; re-evaluate.
			continue
		case <-timer.C:
			s.processExpired(ctx)
		}
	}
}

type expiredEntry struct {
	ID   uuid.UUID
	Type string
}

func (s *Scheduler) processExpired(ctx context.Context) {
	s.mu.Lock()
	now := time.Now()
	var expired []expiredEntry
	for id, entry := range s.timers {
		if !entry.WaitingUntil.After(now) {
			expired = append(expired, expiredEntry{ID: id, Type: entry.Type})
		}
	}
	for _, e := range expired {
		delete(s.timers, e.ID)
	}
	s.resetTimerLocked()
	s.mu.Unlock()

	// Process expired timers outside the lock.
	for _, e := range expired {
		_ = s.handler.OnTimerFired(ctx, e.ID, e.Type)
	}
}

// resetTimerLocked resets the internal timer to fire at the earliest pending expiration.
// Must be called while holding s.mu.
func (s *Scheduler) resetTimerLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}

	earliest, found := s.findEarliestLocked()
	if !found {
		return
	}

	duration := time.Until(earliest)
	if duration <= 0 {
		duration = time.Millisecond // fire immediately
	}
	s.timer = time.NewTimer(duration)
}

// findEarliestLocked finds the earliest waiting_until time among pending timers.
func (s *Scheduler) findEarliestLocked() (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, entry := range s.timers {
		if !found || entry.WaitingUntil.Before(earliest) {
			earliest = entry.WaitingUntil
			found = true
		}
	}
	return earliest, found
}

// OnTimerFired implements TimerFiredHandler on the Manager.
// When a timer fires, if the instance is still in waiting state, it transitions
// to queued and triggers queue evaluation.
func (m *Manager) OnTimerFired(ctx context.Context, instanceID uuid.UUID, timerType string) error {
	inst, err := m.instances.Get(ctx, instanceID)
	if err != nil {
		return err
	}

	// Only transition if still in waiting state.
	if inst.State != instance.StateWaiting {
		return nil
	}

	switch timerType {
	case TimerTypeDelay:
		now := time.Now()
		if err := m.queue.SetQueued(ctx, instanceID, now); err != nil {
			return err
		}
		return m.Evaluate(ctx)

	case TimerTypeWebhook:
		// Webhook timeout: deactivate endpoint, fail the instance.
		if m.webhookRegistry != nil && inst.WebhookToken != nil {
			m.webhookRegistry.Deactivate(*inst.WebhookToken)
		}
		// Clear webhook fields.
		_ = m.instances.SetWebhookFields(ctx, instanceID, nil, nil)
		// Determine the failed node ID from the current node.
		failedNodeID := ""
		if inst.CurrentNodeID != nil {
			failedNodeID = *inst.CurrentNodeID
		}
		_ = m.instances.SetFailed(ctx, instanceID, failedNodeID)
		return nil
	}
	return nil
}
