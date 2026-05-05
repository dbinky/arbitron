# Phase 2, Task 5: Crash Recovery (Timer Scan on Startup)

## Objective
Implement startup recovery logic that scans for instances in `waiting` state with timer delays, re-registers active timers with the scheduler, and transitions past-due timers to queued.

## Dependencies
- Phase 2, Task 2 (Scheduler with Schedule method)
- Phase 2, Task 3 (Instance WaitingUntil field persisted in database)
- Phase 1, Task 3 (Queue Manager Evaluate for post-recovery queue drain)

## Files to Create/Modify
- `domain/queue/recovery.go` — crash recovery logic
- `domain/queue/recovery_test.go` — unit tests
- `domain/instance/ports.go` — add `ListWaiting` method to InstanceRepository (or QueueRepository)
- `adapter/postgres/instance_repo.go` — implement `ListWaiting`
- `cmd/` (main startup file) — wire recovery into startup sequence

## Implementation Steps

1. **Add ListWaiting to repository** — in `domain/instance/ports.go` add to `QueueRepository`:
   ```go
   ListWaitingWithTimer(ctx context.Context) ([]Instance, error)
   ```
   This returns all instances where `state = 'waiting' AND waiting_until IS NOT NULL`.

2. **Implement ListWaitingWithTimer in Postgres adapter**:
   ```sql
   -- name: ListWaitingWithTimer :many
   SELECT * FROM instances WHERE state = 'waiting' AND waiting_until IS NOT NULL;
   ```

3. **Create recovery function** in `domain/queue/recovery.go`:
   ```go
   package queue

   import (
       "context"
       "time"

       "github.com/arbitron/arbitron/domain/instance"
   )

   // RecoverTimers scans for waiting instances with timer delays and either
   // re-registers them with the scheduler or transitions past-due ones to queued.
   func (m *Manager) RecoverTimers(ctx context.Context) error {
       waiting, err := m.queue.ListWaitingWithTimer(ctx)
       if err != nil {
           return fmt.Errorf("listing waiting instances: %w", err)
       }

       now := time.Now()
       for _, inst := range waiting {
           if inst.WaitingUntil == nil {
               continue
           }

           if inst.WaitingUntil.Before(now) || inst.WaitingUntil.Equal(now) {
               // Past-due: transition to queued
               if err := m.queue.SetQueued(ctx, inst.ID, now); err != nil {
                   // Log error but continue recovery
                   continue
               }
           } else {
               // Still pending: register with scheduler
               m.scheduler.Schedule(inst.ID, *inst.WaitingUntil)
           }
       }

       // Run queue evaluation (past-due timers may have added to the queue)
       return m.Evaluate(ctx)
   }
   ```

4. **Add scheduler reference to Manager** — the Manager needs access to the scheduler:
   ```go
   type Manager struct {
       queue     instance.QueueRepository
       config   *config.Config
       starter  InstanceStarter
       scheduler *Scheduler
       mu       sync.Mutex
   }
   ```
   Update `NewManager` to accept the scheduler.

5. **Wire into startup sequence** — in the main startup code (likely `cmd/root.go` or `cmd/serve.go`):
   ```go
   // After creating the scheduler and queue manager:
   scheduler.Start(ctx)

   // Run crash recovery
   if err := queueManager.RecoverTimers(ctx); err != nil {
       log.Error("timer recovery failed", "error", err)
   }
   ```

6. **Handle the edge case of paused instances** — if an instance was paused while waiting (state=paused, waiting_until is set), it should NOT be recovered by the timer scan (the timer fires for it are handled by the scheduler which checks state). The query `state = 'waiting'` naturally excludes paused instances. However, the scheduler's `OnTimerFired` should check if the instance is still in `waiting` state before transitioning:
   ```go
   func (m *Manager) OnTimerFired(ctx context.Context, instanceID uuid.UUID) error {
       inst, err := m.instances.Get(ctx, instanceID)
       if err != nil { return err }

       // Only transition if still in waiting state
       if inst.State != instance.StateWaiting {
           return nil // instance was paused or cancelled while timer was pending
       }

       now := time.Now()
       if err := m.queue.SetQueued(ctx, instanceID, now); err != nil {
           return err
       }
       return m.Evaluate(ctx)
   }
   ```
   Note: This requires the Manager to also have access to the main InstanceRepository. Add it as a dependency.

## Tests
- `domain/queue/recovery_test.go`:
  - `TestRecoverTimers_PastDue_TransitionsToQueued` — waiting instance with expired timer → queued
  - `TestRecoverTimers_FuturePending_RegistersWithScheduler` — waiting instance with future timer → scheduled
  - `TestRecoverTimers_MultipleMixed` — mix of past-due and pending instances handled correctly
  - `TestRecoverTimers_NoWaitingInstances` — no-op, no errors
  - `TestRecoverTimers_TriggersEvaluation` — Evaluate called after recovery
  - `TestOnTimerFired_InstanceNoLongerWaiting_NoOp` — instance in paused state → no transition
  - `TestOnTimerFired_InstanceStillWaiting_TransitionsToQueued` — normal case

## Acceptance Criteria
- On startup, all waiting instances with `waiting_until` are scanned
- Past-due timers: instance transitions to `queued` with `queued_at` set to now
- Pending timers: registered with the scheduler for future firing
- Queue evaluation runs after recovery (queued instances may be eligible to start)
- Timer fired for paused instance: no state change (already paused)
- Recovery is resilient (individual failures don't stop processing remaining instances)
- Recovery logic is called during application startup before accepting traffic
