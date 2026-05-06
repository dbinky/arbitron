# Phase 1, Task 3: Queue Manager Domain Service

## Objective
Implement the `domain/queue/manager.go` service that is the single authority on capacity decisions, queue evaluation, and dequeuing.

## Dependencies
- Phase 1, Task 1 (QueueRepository port, QueueConfig)
- Phase 1, Task 2 (QueueRepository Postgres implementation for integration testing)

## Files to Create/Modify
- `domain/queue/manager.go` — the Queue Manager service
- `domain/queue/manager_test.go` — unit tests with mock repositories

## Implementation Steps

1. **Create package** `domain/queue/` with file `manager.go`.

2. **Define the InstanceStarter interface** (port to avoid circular dependency with engine):
   ```go
   // InstanceStarter starts a queued instance. Implemented by the Engine.
   type InstanceStarter interface {
       StartInstance(ctx context.Context, id uuid.UUID) error
   }
   ```

3. **Define the Manager struct**:
   ```go
   type Manager struct {
       queue     instance.QueueRepository
       instances instance.InstanceRepository
       config    *config.Config
       starter   InstanceStarter
       mu        sync.Mutex
   }

   func NewManager(queue instance.QueueRepository, instances instance.InstanceRepository, cfg *config.Config, starter InstanceStarter) *Manager {
       return &Manager{
           queue:     queue,
           instances: instances,
           config:    cfg,
           starter:   starter,
       }
   }
   ```
   Note: The `instances` field is needed by `OnTimerFired` (Phase 2 Task 2) which checks instance state before transitioning. A scheduler field is added later in Phase 2 Task 5 for crash recovery.

4. **Implement `CanStart` method** — checks if a given owner can start an instance:
   ```go
   func (m *Manager) CanStart(ctx context.Context, ownerID uuid.UUID) (bool, error) {
       // Check global cap (skip if 0)
       if m.config.Queue.GlobalCap > 0 {
           running, err := m.queue.CountRunning(ctx)
           if err != nil { return false, err }
           if running >= m.config.Queue.GlobalCap { return false, nil }
       }
       // Check per-user cap (skip if 0)
       if m.config.Queue.PerUserCap > 0 {
           userRunning, err := m.queue.CountRunningByOwner(ctx, ownerID)
           if err != nil { return false, err }
           if userRunning >= m.config.Queue.PerUserCap { return false, nil }
       }
       return true, nil
   }
   ```

5. **Implement `Evaluate` method** — the queue drain loop:
   ```go
   func (m *Manager) Evaluate(ctx context.Context) error {
       m.mu.Lock()
       defer m.mu.Unlock()

       for {
           next, err := m.queue.NextEligible(ctx, m.config.Queue.GlobalCap, m.config.Queue.PerUserCap)
           if err != nil { return err }
           if next == nil { return nil } // no more eligible

           if err := m.starter.StartInstance(ctx, next.ID); err != nil {
               // Log error but continue evaluating
               continue
           }
       }
   }
   ```

6. **Implement `Enqueue` method** — called by Trigger to add an instance to the queue:
   ```go
   func (m *Manager) Enqueue(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (started bool, err error) {
       canStart, err := m.CanStart(ctx, ownerID)
       if err != nil { return false, err }

       if canStart {
           if err := m.starter.StartInstance(ctx, id); err != nil {
               return false, err
           }
           return true, nil
       }

       // Instance stays in queued state (already set by Trigger)
       return false, nil
   }
   ```

7. **Implement `NotifyCapacityFreed` method** — called when an instance leaves running:
   ```go
   func (m *Manager) NotifyCapacityFreed(ctx context.Context) {
       // Fire and forget — evaluate in background to avoid blocking the caller
       go func() {
           _ = m.Evaluate(context.Background())
       }()
   }
   ```

8. **Implement `UpdateConfig` method** — for runtime cap changes:
   ```go
   func (m *Manager) UpdateConfig(globalCap, perUserCap int) {
       m.config.Queue.GlobalCap = globalCap
       m.config.Queue.PerUserCap = perUserCap
   }
   ```

## Tests
- `domain/queue/manager_test.go`:
  - `TestCanStart_UnderBothCaps` — returns true
  - `TestCanStart_AtGlobalCap` — returns false
  - `TestCanStart_AtPerUserCap` — returns false
  - `TestCanStart_GlobalCapUnlimited` — cap 0 always passes global check
  - `TestCanStart_PerUserCapUnlimited` — cap 0 always passes per-user check
  - `TestEvaluate_StartsMultipleEligible` — mock returns eligible instances until nil
  - `TestEvaluate_SkipsWhenStartFails` — continues to next eligible on error
  - `TestEvaluate_NoEligible` — returns nil immediately
  - `TestEnqueue_StartsImmediately` — canStart true, calls StartInstance
  - `TestEnqueue_StaysQueued` — canStart false, does not call StartInstance
  - `TestNotifyCapacityFreed_TriggersEvaluate` — verify evaluate is called (use channel-based mock)

## Acceptance Criteria
- `domain/queue/manager.go` exists with `Manager` struct
- `CanStart` correctly checks both caps with 0-means-unlimited semantics
- `Evaluate` drains the queue starting as many instances as capacity allows
- `Enqueue` returns `started=true` when instance starts immediately, `false` when queued
- `NotifyCapacityFreed` triggers asynchronous queue evaluation
- All unit tests pass with mock repositories
- No circular imports (engine depends on queue via interface, queue depends on engine via `InstanceStarter` interface)
