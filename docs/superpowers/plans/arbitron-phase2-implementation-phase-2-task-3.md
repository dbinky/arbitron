# Phase 2, Task 3: Timer Delay Executor + WaitResult + Engine run() Loop Change

> **Historical note (added 2026-06-02):** This document predates Arbitron's migration from Postgres to SQLite. All Postgres references below are obsolete — the live persistence layer is the SQLite adapter (`adapter/sqlite/`, configured via `database.path`, default `./arbitron.db`). Retained as a historical record.

## Objective
Implement the timer delay executor that transitions instances to `waiting` state, introduce the `WaitResult` type, and modify the engine's `run()` loop to handle wait results by exiting cleanly.

## Dependencies
- Phase 2, Task 1 (NodeTypeTimerDelay, DurationSeconds in NodeConfig)
- Phase 2, Task 2 (Scheduler with Schedule method)
- Phase 1, Task 4 (Engine with StartInstance and NotifyCapacityFreed)

## Files to Create/Modify
- `domain/engine/result.go` — add `WaitResult` type
- `domain/engine/executor_timer.go` — timer delay executor implementation
- `domain/engine/executor.go` — update `NodeExecutor` interface or add `WaitExecutor` interface
- `domain/engine/engine.go` — modify `run()` loop to handle `WaitResult`
- `domain/instance/instance.go` — add `WaitingUntil` field
- `domain/instance/ports.go` — add `SetWaiting` method to InstanceRepository
- `adapter/postgres/instance_repo.go` — implement `SetWaiting`
- `adapter/postgres/migrations/006_add_timer_delay.up.sql` — add `waiting_until` column
- `adapter/postgres/migrations/006_add_timer_delay.down.sql` — remove column

## Implementation Steps

1. **Add WaitingUntil field** to `Instance` in `domain/instance/instance.go`:
   ```go
   type Instance struct {
       // ... existing fields ...
       WaitingUntil *time.Time
   }
   ```

2. **Add SetWaiting to InstanceRepository** in `domain/instance/ports.go`:
   ```go
   SetWaiting(ctx context.Context, id uuid.UUID, waitingUntil *time.Time) error
   ```

3. **Create database migration** `006_add_timer_delay.up.sql`:
   ```sql
   ALTER TABLE instances ADD COLUMN waiting_until TIMESTAMPTZ;
   ```
   And `006_add_timer_delay.down.sql`:
   ```sql
   ALTER TABLE instances DROP COLUMN IF EXISTS waiting_until;
   ```

4. **Define WaitResult** in `domain/engine/result.go`:
   ```go
   // WaitResult signals that the engine should stop walking the graph because
   // the instance has transitioned to a waiting state. The executor has already
   // handled the state transition.
   type WaitResult struct {
       Reason string // "timer_delay" or "webhook_wait"
   }
   ```

5. **Update NodeExecutor interface** — the executor needs to return either a `*NodeResult` or a `*WaitResult`. Use a combined return approach:
   ```go
   // ExecuteResult wraps either a NodeResult (continue execution) or a WaitResult (stop execution).
   type ExecuteResult struct {
       NodeResult *NodeResult
       WaitResult *WaitResult
   }
   ```
   However, to avoid breaking existing executors, create a separate interface:
   ```go
   // WaitExecutor is implemented by executors that may put an instance into a waiting state.
   type WaitExecutor interface {
       ExecuteWait(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*WaitResult, error)
   }
   ```
   Alternatively (simpler): modify the existing `Execute` return to `(*NodeResult, error)` and add a sentinel to NodeResult:
   ```go
   // In NodeResult, add a field:
   Wait *WaitResult // non-nil means the engine should stop
   ```
   **Recommended approach**: Add `IsWait bool` to `NodeResult` to minimize interface changes:
   ```go
   type NodeResult struct {
       // ... existing fields ...
       IsWait bool   // if true, engine exits the run loop
   }
   ```

6. **Implement TimerDelayExecutor** in `domain/engine/executor_timer.go`:
   ```go
   type TimerDelayExecutor struct {
       instances instance.InstanceRepository
       scheduler TimerScheduler
       queueMgr  QueueManager
   }

   // TimerScheduler is the subset of Scheduler the executor needs.
   type TimerScheduler interface {
       Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string)
   }

   func NewTimerDelayExecutor(instances instance.InstanceRepository, scheduler TimerScheduler, queueMgr QueueManager) *TimerDelayExecutor {
       return &TimerDelayExecutor{instances: instances, scheduler: scheduler, queueMgr: queueMgr}
   }

   func (e *TimerDelayExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
       instanceID := vars.GetInstanceID() // need a way to pass instanceID — see step 7

       // 1. Resolve {{substitution}} on DurationSeconds
       durationStr := resolveTemplate(node.Config.DurationSeconds, vars)

       // 2. Parse to float64
       duration, err := strconv.ParseFloat(durationStr, 64)
       if err != nil {
           return nil, fmt.Errorf("durationSeconds is not numeric: %q", durationStr)
       }

       // 3. Calculate waitingUntil
       var waitingUntil time.Time
       if duration <= 0 {
           waitingUntil = time.Now() // immediately eligible
       } else {
           waitingUntil = time.Now().Add(time.Duration(duration * float64(time.Second)))
       }

       // 4. Transition instance to waiting
       if err := e.instances.SetWaiting(ctx, instanceID, &waitingUntil); err != nil {
           return nil, fmt.Errorf("failed to set waiting state: %w", err)
       }

       // 5. Register with scheduler (TimerTypeDelay from domain/queue package)
       e.scheduler.Schedule(instanceID, waitingUntil, "timer_delay")

       // 6. Notify queue manager (capacity freed)
       e.queueMgr.NotifyCapacityFreed(ctx)

       // 7. Return result with IsWait=true
       return &NodeResult{IsWait: true}, nil
   }
   ```

7. **Pass instanceID to executors** — the current `Execute` signature does not include instanceID. Options:
   - Add instanceID to the `VariableStore` (set it before execution begins)
   - Add instanceID as a parameter to `Execute`

   **Recommended**: Store instanceID in the VariableStore as a system variable before execution. In the `run()` loop, call `vars.SetSystem("_instanceID", instanceID.String())` and read it in the executor. Or better: add instanceID to the Execute context via `context.WithValue`. Or simplest: change Execute signature to include instanceID:
   ```go
   Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error)
   ```
   This requires updating all existing executors. Choose the approach that minimizes churn — if there are only 4-5 executors, updating the signature is cleanest.

8. **Modify the run() loop** in `domain/engine/engine.go` to check `IsWait`:
   ```go
   // After executing a node:
   result, err := executor.Execute(ctx, node, vars, inst.WorkflowName, inst.ID)
   if err != nil {
       // handle error (existing logic)
   }
   if result.IsWait {
       // Instance has transitioned to waiting. Exit the run loop cleanly.
       // Do NOT mark as completed or failed.
       return
   }
   // ... continue with existing logic (store output, follow edges, etc.)
   ```

9. **Register TimerDelayExecutor** in the executor map during engine construction:
   ```go
   executors[workflow.NodeTypeTimerDelay] = timerDelayExecutor
   ```

10. **Write node execution record** — before returning the WaitResult, the executor should write a node execution record with status `succeeded`:
    ```go
    exec := &instance.NodeExecution{
        ID:         uuid.New(),
        InstanceID: instanceID,
        NodeID:     node.ID,
        Attempt:    1,
        Status:     instance.NodeStatusSucceeded,
        Output:     map[string]any{"waitingUntil": waitingUntil.Format(time.RFC3339)},
        StartedAt:  startTime,
        CompletedAt: &now,
    }
    e.nodeExecs.Create(ctx, exec)
    ```
    The executor needs access to `NodeExecutionRepository` — add it as a dependency.

## Tests
- `domain/engine/executor_timer_test.go`:
  - `TestTimerDelayExecutor_ResolvesSubstitution` — `"{{delay}}"` with vars `{"delay": "30"}` → 30 second duration
  - `TestTimerDelayExecutor_NonNumericDuration_Fails` — returns error for `"abc"`
  - `TestTimerDelayExecutor_ZeroDuration_ImmediateEligibility` — waitingUntil is now or past
  - `TestTimerDelayExecutor_NegativeDuration_ImmediateEligibility` — same as zero
  - `TestTimerDelayExecutor_SetsWaitingState` — verifies SetWaiting called with correct time
  - `TestTimerDelayExecutor_RegistersWithScheduler` — verifies Schedule called
  - `TestTimerDelayExecutor_NotifiesQueueManager` — verifies NotifyCapacityFreed called
  - `TestTimerDelayExecutor_WritesNodeExecution` — verifies node execution record created
  - `TestTimerDelayExecutor_ReturnsIsWait` — result.IsWait is true
- `domain/engine/engine_test.go`:
  - `TestRun_WaitResult_ExitsCleanly` — run loop stops without marking completed/failed
  - `TestRun_WaitResult_DoesNotFollowEdges` — no subsequent nodes execute

## Acceptance Criteria
- `WaitResult` (or `IsWait` flag) exists and is recognized by the run loop
- `TimerDelayExecutor` resolves substitutions, parses duration, transitions to waiting, schedules timer
- Non-numeric duration returns an error (node fails per error behavior)
- Zero/negative duration sets waitingUntil to now (immediately eligible)
- Engine `run()` loop exits cleanly on wait result without completing/failing the instance
- Node execution record is written with `succeeded` status
- All tests pass with `-race`
