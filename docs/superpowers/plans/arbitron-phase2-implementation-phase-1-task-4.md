# Phase 1, Task 4: Engine Changes (Trigger Refactor, StartInstance Extraction, State Transition Notifications)

## Objective
Refactor `Engine.Trigger()` to create instances as `queued` and delegate to the queue manager, extract `StartInstance()` as a public method, and wire state transition notifications to trigger queue evaluation.

## Dependencies
- Phase 1, Task 1 (domain model: StateWaiting, QueuedAt)
- Phase 1, Task 3 (Queue Manager with CanStart/Enqueue/NotifyCapacityFreed)

## Files to Create/Modify
- `domain/engine/engine.go` — refactor Trigger, add StartInstance, add queue manager dependency, wire notifications
- `domain/engine/engine_test.go` — update existing tests, add new tests for queue integration

## Implementation Steps

1. **Add Queue Manager and QueueRepo dependencies to Engine**:
   ```go
   type Engine struct {
       workflows      workflow.WorkflowRepository
       instances      instance.InstanceRepository
       nodeExecutions instance.NodeExecutionRepository
       snapshots      instance.VariableSnapshotRepository
       users          instance.UserRepository
       executors      map[workflow.NodeType]NodeExecutor
       eventBus       EventBus
       queueManager   QueueManager  // new field
       queueRepo      instance.QueueRepository  // new field — used by Resume in Phase 2 Task 4
       mu             sync.Mutex
       contexts       map[uuid.UUID]context.CancelFunc
       wg             sync.WaitGroup
   }
   ```

2. **Define QueueManager interface in engine package** (to avoid importing queue package directly):
   ```go
   // QueueManager defines the queue operations the engine needs.
   type QueueManager interface {
       Enqueue(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (started bool, err error)
       NotifyCapacityFreed(ctx context.Context)
   }
   ```
   Add to `domain/engine/ports.go`.

3. **Update NewEngine** to accept `QueueManager` and `QueueRepository`:
   ```go
   func NewEngine(
       workflows workflow.WorkflowRepository,
       instances instance.InstanceRepository,
       nodeExecutions instance.NodeExecutionRepository,
       snapshots instance.VariableSnapshotRepository,
       users instance.UserRepository,
       executors map[workflow.NodeType]NodeExecutor,
       eventBus EventBus,
       queueManager QueueManager,
       queueRepo instance.QueueRepository,
   ) *Engine
   ```

4. **Refactor Trigger** — change the instance creation to use `StateQueued` and set `QueuedAt`:
   ```go
   now := time.Now()
   inst := &instance.Instance{
       ID:                uuid.New(),
       OwnerID:           ownerID,
       WorkflowName:      workflowName,
       WorkflowSnapshot:  wfSnapshot,
       State:             instance.StateQueued,  // was StateRunning
       Priority:          instance.Priority(priority),
       Variables:         snap,
       OriginalVariables: origVars,
       QueuedAt:          &now,  // new field
       CreatedAt:         now,
       UpdatedAt:         now,
   }
   ```
   After creating the instance, call `e.queueManager.Enqueue(ctx, inst.ID, inst.OwnerID)` instead of directly launching the goroutine. Return the `started` boolean alongside the instance ID so the HTTP handler can determine 201 vs 202.

5. **Change Trigger return signature**:
   ```go
   func (e *Engine) Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
   ```
   The second return value `started` is true when the instance began running immediately, false when it stayed queued.

6. **Extract StartInstance method**:
   ```go
   func (e *Engine) StartInstance(ctx context.Context, id uuid.UUID) error {
       // Transition instance to running
       if err := e.instances.UpdateState(ctx, id, instance.StateRunning); err != nil {
           return fmt.Errorf("failed to transition to running: %w", err)
       }

       // Create cancellable context and register
       runCtx, cancel := context.WithCancel(context.Background())
       e.mu.Lock()
       e.contexts[id] = cancel
       e.mu.Unlock()

       e.eventBus.Publish(id, Event{
           Type:      "instance.started",
           Timestamp: time.Now(),
           Data:      map[string]any{},
       })

       e.wg.Add(1)
       go e.run(runCtx, id)

       return nil
   }
   ```

7. **Wire NotifyCapacityFreed** — at every point where an instance leaves `running` state, call `e.queueManager.NotifyCapacityFreed(ctx)`. Locations:
   - After `e.instances.SetCompleted` succeeds in the `run()` loop
   - After `e.instances.SetFailed` succeeds in the `run()` loop and in the panic recovery
   - In the `Cancel` method after setting cancelled state
   - In the `Pause` method after setting paused state

   In each case:
   ```go
   e.queueManager.NotifyCapacityFreed(context.Background())
   ```

8. **Update existing tests** — all tests that call `NewEngine` must pass the new `QueueManager` parameter (use a no-op mock for tests that don't exercise queue behavior). All tests that check `Trigger` return value must account for the new `bool` return.

## Tests
- `domain/engine/engine_test.go`:
  - `TestTrigger_CreatesInstanceAsQueued` — instance starts in queued state with QueuedAt set
  - `TestTrigger_ReturnsStartedTrue_WhenCapacityAvailable` — mock Enqueue returns started=true
  - `TestTrigger_ReturnsStartedFalse_WhenAtCapacity` — mock Enqueue returns started=false
  - `TestStartInstance_TransitionsToRunning` — verifies state change and goroutine launch
  - `TestStartInstance_PublishesStartedEvent` — event bus receives instance.started
  - `TestCompleted_NotifiesQueueManager` — verify NotifyCapacityFreed called
  - `TestFailed_NotifiesQueueManager` — verify NotifyCapacityFreed called
  - `TestCancel_NotifiesQueueManager` — verify NotifyCapacityFreed called
  - `TestPause_NotifiesQueueManager` — verify NotifyCapacityFreed called

## Acceptance Criteria
- `Trigger()` creates instances in `queued` state with `QueuedAt` set
- `Trigger()` returns `(uuid.UUID, bool, error)` where bool indicates immediate start
- `StartInstance()` is a public method that transitions to running and launches the goroutine
- Every state transition out of `running` calls `NotifyCapacityFreed`
- No circular dependency between engine and queue packages (both depend on interfaces)
- All existing engine tests pass (updated for new signature)
