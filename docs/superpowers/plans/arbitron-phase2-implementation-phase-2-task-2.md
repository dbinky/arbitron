# Phase 2, Task 2: Timer Scheduler (domain/queue/scheduler.go)

## Objective
Implement the hybrid single-timer scheduler that manages in-memory timers for waiting instances, firing callbacks when timers expire to transition instances back to the queue.

## Dependencies
- Phase 1, Task 1 (QueueRepository.SetQueued for transitioning instances)
- Phase 1, Task 3 (Queue Manager's Evaluate method for post-transition evaluation)

## Files to Create/Modify
- `domain/queue/scheduler.go` — the Timer Scheduler implementation
- `domain/queue/scheduler_test.go` — unit tests

## Implementation Steps

1. **Define the TimerCallback interface** (or use the Queue Manager directly):
   ```go
   // TimerFiredHandler is called when a timer expires for an instance.
   type TimerFiredHandler interface {
       OnTimerFired(ctx context.Context, instanceID uuid.UUID, timerType string) error
   }
   ```
   Timer type constants (used now for timer_delay, extended in Phase 3 for webhook_timeout):
   ```go
   const (
       TimerTypeDelay   = "timer_delay"
       TimerTypeWebhook = "webhook_timeout"
   )
   ```

2. **Define the Scheduler struct**:
   ```go
   type timerEntry struct {
       WaitingUntil time.Time
       Type         string // TimerTypeDelay or TimerTypeWebhook
   }

   type Scheduler struct {
       mu       sync.Mutex
       timers   map[uuid.UUID]timerEntry  // instanceID -> timer entry
       timer    *time.Timer
       handler  TimerFiredHandler
       repo     instance.QueueRepository
       stopCh   chan struct{}
       doneCh   chan struct{}
   }

   func NewScheduler(repo instance.QueueRepository, handler TimerFiredHandler) *Scheduler {
       return &Scheduler{
           timers:  make(map[uuid.UUID]timerEntry),
           repo:    repo,
           handler: handler,
           stopCh:  make(chan struct{}),
           doneCh:  make(chan struct{}),
       }
   }
   ```

3. **Implement Schedule method**:
   ```go
   func (s *Scheduler) Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string) {
       s.mu.Lock()
       defer s.mu.Unlock()

       s.timers[instanceID] = timerEntry{WaitingUntil: waitingUntil, Type: timerType}
       s.resetTimerLocked()
   }
   ```

4. **Implement Cancel method**:
   ```go
   func (s *Scheduler) Cancel(instanceID uuid.UUID) {
       s.mu.Lock()
       defer s.mu.Unlock()

       delete(s.timers, instanceID)
       s.resetTimerLocked()
   }
   ```

5. **Implement resetTimerLocked** (must hold mutex):
   ```go
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
   ```

6. **Implement findEarliestLocked**:
   ```go
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
   ```

7. **Implement Start method** (the scheduler goroutine):
   ```go
   func (s *Scheduler) Start(ctx context.Context) {
       go s.loop(ctx)
   }

   func (s *Scheduler) loop(ctx context.Context) {
       defer close(s.doneCh)

       for {
           s.mu.Lock()
           timer := s.timer
           s.mu.Unlock()

           if timer == nil {
               // No pending timers — wait for a signal or context cancellation
               select {
               case <-ctx.Done():
                   return
               case <-s.stopCh:
                   return
               }
           }

           select {
           case <-ctx.Done():
               return
           case <-s.stopCh:
               return
           case <-timer.C:
               s.processExpired(ctx)
           }
       }
   }
   ```

8. **Implement processExpired** — handle all timers that have expired:
   ```go
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

       // Process expired timers outside the lock
       for _, e := range expired {
           _ = s.handler.OnTimerFired(ctx, e.ID, e.Type)
       }
   }
   ```

9. **Implement Stop method**:
   ```go
   func (s *Scheduler) Stop() {
       close(s.stopCh)
       <-s.doneCh
   }
   ```

10. **Implement OnTimerFired in Queue Manager** (or a dedicated handler):
    ```go
    func (m *Manager) OnTimerFired(ctx context.Context, instanceID uuid.UUID, timerType string) error {
        inst, err := m.instances.Get(ctx, instanceID)
        if err != nil { return err }

        // Only transition if still in waiting state (instance may have been paused or cancelled)
        if inst.State != instance.StateWaiting { return nil }

        switch timerType {
        case TimerTypeDelay:
            now := time.Now()
            if err := m.queue.SetQueued(ctx, instanceID, now); err != nil {
                return err
            }
            return m.Evaluate(ctx)

        case TimerTypeWebhook:
            // Handled in Phase 3 Task 5 — webhook timeout logic
            // For now, treat as no-op (Phase 2 only uses TimerTypeDelay)
            return nil
        }
        return nil
    }
    ```
    Note: The Manager already has access to `instance.InstanceRepository` (added in Phase 1 Task 3).

## Tests
- `domain/queue/scheduler_test.go`:
  - `TestScheduler_SingleTimer_FiresAtCorrectTime` — schedule one timer, verify callback fires within tolerance
  - `TestScheduler_MultipleTimers_EarliestFiresFirst` — schedule 3 timers, verify firing order
  - `TestScheduler_Cancel_RemovesTimer` — cancel a scheduled timer, verify it does not fire
  - `TestScheduler_Cancel_RecalculatesNext` — cancel the earliest, verify next earliest becomes active
  - `TestScheduler_ScheduleEarlierThanCurrent` — adding an earlier timer resets the active timer
  - `TestScheduler_MultipleExpireSimultaneously` — timers at same time both fire
  - `TestScheduler_CancelNonExistent_NoOp` — cancelling unknown ID does not panic
  - `TestScheduler_Stop_GracefulShutdown` — Stop returns, goroutine exits
  - `TestScheduler_ConcurrentScheduleCancel` — run with `-race`, concurrent Schedule/Cancel calls
  - `TestScheduler_TimerType_PassedToHandler` — verify timerType string is forwarded to OnTimerFired callback

## Acceptance Criteria
- `Scheduler` struct exists in `domain/queue/scheduler.go`
- Single background goroutine manages all timers via one `time.Timer`
- `Schedule(instanceID, waitingUntil, timerType)` adds/updates a timer; if earlier than current, resets the active timer
- `Cancel` removes a timer; if it was earliest, recalculates
- When timer fires: instance transitions from `waiting` to `queued`, queue evaluation runs
- Multiple simultaneous expirations are all processed
- `Start`/`Stop` lifecycle is clean with no goroutine leaks
- All tests pass with `-race` flag
