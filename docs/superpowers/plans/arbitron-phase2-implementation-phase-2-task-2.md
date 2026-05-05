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
       OnTimerFired(ctx context.Context, instanceID uuid.UUID) error
   }
   ```

2. **Define the Scheduler struct**:
   ```go
   type Scheduler struct {
       mu       sync.Mutex
       timers   map[uuid.UUID]time.Time  // instanceID -> waitingUntil
       timer    *time.Timer
       handler  TimerFiredHandler
       repo     instance.QueueRepository
       stopCh   chan struct{}
       doneCh   chan struct{}
   }

   func NewScheduler(repo instance.QueueRepository, handler TimerFiredHandler) *Scheduler {
       return &Scheduler{
           timers:  make(map[uuid.UUID]time.Time),
           repo:    repo,
           handler: handler,
           stopCh:  make(chan struct{}),
           doneCh:  make(chan struct{}),
       }
   }
   ```

3. **Implement Schedule method**:
   ```go
   func (s *Scheduler) Schedule(instanceID uuid.UUID, waitingUntil time.Time) {
       s.mu.Lock()
       defer s.mu.Unlock()

       s.timers[instanceID] = waitingUntil
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
       for _, t := range s.timers {
           if !found || t.Before(earliest) {
               earliest = t
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
   func (s *Scheduler) processExpired(ctx context.Context) {
       s.mu.Lock()
       now := time.Now()
       var expired []uuid.UUID
       for id, t := range s.timers {
           if !t.After(now) {
               expired = append(expired, id)
           }
       }
       for _, id := range expired {
           delete(s.timers, id)
       }
       s.resetTimerLocked()
       s.mu.Unlock()

       // Process expired timers outside the lock
       for _, id := range expired {
           _ = s.handler.OnTimerFired(ctx, id)
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
    func (m *Manager) OnTimerFired(ctx context.Context, instanceID uuid.UUID) error {
        now := time.Now()
        if err := m.queue.SetQueued(ctx, instanceID, now); err != nil {
            return err
        }
        return m.Evaluate(ctx)
    }
    ```

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

## Acceptance Criteria
- `Scheduler` struct exists in `domain/queue/scheduler.go`
- Single background goroutine manages all timers via one `time.Timer`
- `Schedule` adds/updates a timer; if earlier than current, resets the active timer
- `Cancel` removes a timer; if it was earliest, recalculates
- When timer fires: instance transitions from `waiting` to `queued`, queue evaluation runs
- Multiple simultaneous expirations are all processed
- `Start`/`Stop` lifecycle is clean with no goroutine leaks
- All tests pass with `-race` flag
