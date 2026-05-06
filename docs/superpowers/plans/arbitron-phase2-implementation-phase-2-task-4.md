# Phase 2, Task 4: Waiting State Operations (Cancel/Pause/Resume on Waiting Instances)

## Objective
Implement cancel, pause, and resume operations for instances in the `waiting` state, including timer cancellation and the conditional resume logic (timer expired while paused vs. still pending).

## Dependencies
- Phase 2, Task 2 (Scheduler with Cancel method)
- Phase 2, Task 3 (Instance has WaitingUntil field, SetWaiting in repository)
- Phase 1, Task 4 (Engine Cancel/Pause/Resume methods)

## Files to Create/Modify
- `domain/engine/engine.go` — modify `Cancel`, `Pause`, and `Resume` methods to handle `waiting` state
- `domain/instance/instance.go` — ensure state transition rules allow waiting→cancelled, waiting→paused, paused→waiting, paused→queued
- `domain/engine/engine_test.go` — tests for new waiting state operations

## Implementation Steps

1. **Modify Engine.Cancel** to handle waiting instances:
   ```go
   func (e *Engine) Cancel(id uuid.UUID) error {
       ctx := context.Background()
       inst, err := e.instances.Get(ctx, id)
       if err != nil { return err }

       switch inst.State {
       case instance.StateRunning:
           // existing logic: cancel context, set cancelled
           e.mu.Lock()
           if cancel, ok := e.contexts[id]; ok {
               cancel()
               delete(e.contexts, id)
           }
           e.mu.Unlock()
           if err := e.instances.UpdateState(ctx, id, instance.StateCancelled); err != nil {
               return err
           }
           e.queueManager.NotifyCapacityFreed(ctx)

       case instance.StateWaiting:
           // Cancel the pending timer
           e.scheduler.Cancel(id)
           // Transition to cancelled
           if err := e.instances.UpdateState(ctx, id, instance.StateCancelled); err != nil {
               return err
           }
           // Notify queue manager (consistent, though no capacity change)
           e.queueManager.NotifyCapacityFreed(ctx)

       case instance.StateQueued:
           // Cancel a queued instance
           if err := e.instances.UpdateState(ctx, id, instance.StateCancelled); err != nil {
               return err
           }

       default:
           return instance.ErrCannotCancel
       }
       return nil
   }
   ```

2. **Add Scheduler dependency to Engine** — the Engine needs access to the scheduler for cancel operations:
   ```go
   type Engine struct {
       // ... existing fields ...
       scheduler TimerScheduler
   }
   ```
   Define `TimerScheduler` interface in `domain/engine/ports.go`:
   ```go
   type TimerScheduler interface {
       Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string)
       Cancel(instanceID uuid.UUID)
   }
   ```

3. **Modify Engine.Pause** to handle waiting instances:
   ```go
   func (e *Engine) Pause(id uuid.UUID) error {
       ctx := context.Background()
       inst, err := e.instances.Get(ctx, id)
       if err != nil { return err }

       switch inst.State {
       case instance.StateRunning:
           // existing pause logic (cancel context, set paused)
           // ...
           e.queueManager.NotifyCapacityFreed(ctx)

       case instance.StateWaiting:
           // Transition to paused — timer keeps counting (do NOT cancel scheduler entry)
           if err := e.instances.UpdateState(ctx, id, instance.StatePaused); err != nil {
               return err
           }
           // Note: no capacity change (waiting doesn't count against cap)

       default:
           return instance.ErrNotRunning // or a more specific error
       }
       return nil
   }
   ```

4. **Modify Engine.Resume** to handle paused-from-waiting instances:
   ```go
   func (e *Engine) Resume(id uuid.UUID) error {
       ctx := context.Background()
       inst, err := e.instances.Get(ctx, id)
       if err != nil { return err }

       if inst.State != instance.StatePaused {
           return instance.ErrNotPaused
       }

       // Check if the instance was waiting on a timer delay
       if inst.WaitingUntil != nil {
           if time.Now().After(*inst.WaitingUntil) {
               // Timer expired while paused → go to queued
               now := time.Now()
               if err := e.queueRepo.SetQueued(ctx, id, now); err != nil {
                   return err
               }
               // Clear waitingUntil
               if err := e.instances.SetWaiting(ctx, id, nil); err != nil {
                   return err
               }
               e.queueManager.NotifyCapacityFreed(ctx) // triggers evaluation
               return nil
           }
           // Timer has not expired → go back to waiting
           if err := e.instances.UpdateState(ctx, id, instance.StateWaiting); err != nil {
               return err
           }
           return nil
       }

       // Check if the instance was waiting on a webhook (handled in Phase 3, Task 5)
       // For Phase 2, fall through to normal resume.

       // Normal resume (was running when paused) — go back to running
       return e.StartInstance(ctx, id)
   }
   ```
   Note: Distinguishing "paused from running" vs "paused from waiting" uses the `WaitingUntil` field (non-nil = was waiting on timer) and `WebhookToken` field (non-nil = was waiting on webhook, handled in Phase 3 Task 5).

5. **Add QueueRepository dependency to Engine** — the Engine needs access to `QueueRepository.SetQueued` for the resume-from-waiting flow. Add a `queueRepo instance.QueueRepository` field to the Engine struct and accept it in `NewEngine`:
   ```go
   type Engine struct {
       // ... existing fields ...
       queueRepo instance.QueueRepository
   }
   ```
   The `Resume` method calls `e.queueRepo.SetQueued(ctx, id, now)` when resuming a paused-from-waiting instance whose timer has expired.

6. **Update state transition validation** — ensure the following transitions are allowed:
   - `waiting → cancelled`
   - `waiting → paused`
   - `paused → waiting` (resume, timer not expired)
   - `paused → queued` (resume, timer expired while paused)

## Tests
- `domain/engine/engine_test.go`:
  - `TestCancel_WaitingInstance_CancelsTimer` — scheduler.Cancel called, state → cancelled
  - `TestCancel_WaitingInstance_NotifiesQueueManager` — NotifyCapacityFreed called
  - `TestCancel_QueuedInstance_Success` — queued → cancelled
  - `TestPause_WaitingInstance_TransitionsToPaused` — state → paused
  - `TestPause_WaitingInstance_DoesNotCancelTimer` — scheduler.Cancel NOT called
  - `TestResume_PausedFromWaiting_TimerExpired_GoesToQueued` — WaitingUntil in past → queued
  - `TestResume_PausedFromWaiting_TimerNotExpired_GoesToWaiting` — WaitingUntil in future → waiting
  - `TestResume_PausedFromRunning_StartsInstance` — normal resume behavior (WaitingUntil nil)

## Acceptance Criteria
- Cancelling a waiting instance: cancels timer, transitions to cancelled
- Pausing a waiting instance: transitions to paused, timer keeps counting
- Resuming a paused-from-waiting instance where timer expired: transitions to queued, triggers evaluation
- Resuming a paused-from-waiting instance where timer not expired: transitions back to waiting
- Normal resume (paused from running) still works as before
- All existing cancel/pause/resume tests continue to pass
- No goroutine leaks when cancelling waiting instances
