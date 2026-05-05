# Phase 1, Task 1: Domain Model Changes

## Objective
Add the `StateWaiting` constant, new sentinel errors, `QueuedAt` field to Instance/InstanceSummary, `QueueConfig` to Config, and define the `QueueRepository` port interface.

## Dependencies
- None (first task in the phase)

## Files to Create/Modify
- `domain/instance/instance.go` — add `StateWaiting`, `ErrNotQueued`, `ErrNotWaiting`, `QueuedAt` field to `Instance` and `InstanceSummary`
- `domain/instance/ports.go` — add `QueueRepository` interface
- `domain/config/config.go` — add `QueueConfig` struct and embed in `Config`

## Implementation Steps

1. **Add StateWaiting constant** in `domain/instance/instance.go`:
   ```go
   StateWaiting State = "waiting"
   ```
   Place it between `StateRunning` and `StateCompleted` in the const block.

2. **Add sentinel errors** in `domain/instance/instance.go`:
   ```go
   ErrNotQueued  = errors.New("instance is not queued")
   ErrNotWaiting = errors.New("instance is not waiting")
   ```

3. **Add QueuedAt field** to the `Instance` struct:
   ```go
   QueuedAt *time.Time
   ```
   Place after `CompletedAt`.

4. **Add QueuedAt field** to the `InstanceSummary` struct:
   ```go
   QueuedAt *time.Time `json:"queuedAt,omitempty"`
   ```
   Place after `CompletedAt`.

5. **Define QueueRepository port** in `domain/instance/ports.go`:
   ```go
   // QueueRepository defines the port for queue-specific persistence operations.
   type QueueRepository interface {
       CountRunning(ctx context.Context) (int, error)
       CountRunningByOwner(ctx context.Context, ownerID uuid.UUID) (int, error)
       NextEligible(ctx context.Context, globalCap int, perUserCap int) (*Instance, error)
       SetQueued(ctx context.Context, id uuid.UUID, queuedAt time.Time) error
   }
   ```

6. **Add QueueConfig** in `domain/config/config.go`:
   ```go
   // QueueConfig holds queue management settings.
   type QueueConfig struct {
       GlobalCap  int // 0 = unlimited
       PerUserCap int // 0 = unlimited
   }
   ```

7. **Embed QueueConfig in Config** struct:
   ```go
   type Config struct {
       Server    ServerConfig
       Database  DatabaseConfig
       Workspace WorkspaceConfig
       LLM       LLMConfig
       Logging   LoggingConfig
       Tracing   TracingConfig
       Queue     QueueConfig
   }
   ```

8. **Set default values** — wherever the config is initialized (check `cmd/` or config loading code), ensure defaults of `GlobalCap: 20`, `PerUserCap: 5` are applied when not specified.

## Tests
- `domain/instance/instance_test.go` — `TestStateWaitingConstant`: verify `StateWaiting` equals `"waiting"`
- `domain/config/config_test.go` — `TestQueueConfigDefaults`: verify zero-value QueueConfig has expected semantics (0 = unlimited)

## Acceptance Criteria
- `StateWaiting` constant exists and equals `"waiting"`
- `ErrNotQueued` and `ErrNotWaiting` sentinel errors are exported
- `Instance.QueuedAt` and `InstanceSummary.QueuedAt` fields exist as `*time.Time`
- `QueueRepository` interface is defined in `domain/instance/ports.go` with 4 methods
- `Config.Queue` is a `QueueConfig` with `GlobalCap` and `PerUserCap` int fields
- All existing tests pass without modification
