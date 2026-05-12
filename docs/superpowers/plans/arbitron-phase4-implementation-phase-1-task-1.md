# Phase 1, Task 1: Interactive Node Type & External Notification Interface

## Goal

Add the `interactive` node type to the workflow definition domain and create the external notification interface with its logging-only adapter.

## Files to Create/Modify

### Modify: `domain/workflow/workflow.go`

- Add `NodeTypeInteractive NodeType = "interactive"` constant
- Add interactive-specific config fields to the node config struct (or ensure the existing config map supports): `systemPrompt`, `orchestrationFile`, `templateFile`, `model`, `outputSchema`, `maxTokens`, `temperature`

### Create: `domain/notification/external.go`

- Define the `ExternalNotifier` interface:
  ```go
  type ExternalNotifier interface {
      Notify(ctx context.Context, notification *Notification) error
  }
  ```

### Create: `adapter/notification/log_notifier.go`

- Implement `ExternalNotifier` with a logging-only adapter
- Constructor: `NewLogNotifier(logger *slog.Logger) *LogNotifier`
- `Notify()` logs at Info level with structured fields: notification ID, instance ID, workflow name, type, timestamp
- Always returns nil

### Modify: `domain/engine/engine.go`

- Add an `ExternalNotifier` field to the Engine struct
- Add a setter: `SetExternalNotifier(n notification.ExternalNotifier)`
- In `emitNotification()`: after creating the in-app notification, call `e.externalNotifier.Notify(ctx, n)` if the notifier is set

### Modify: `cmd/arbitron/main.go`

- Instantiate the `LogNotifier` with the application logger
- Call `eng.SetExternalNotifier(logNotifier)` after engine creation

## Testing

- Unit test for `LogNotifier`: verify it logs the correct structured fields and returns nil
- Unit test for engine's `emitNotification()`: verify it calls ExternalNotifier when set, and doesn't panic when nil
- Verify workflow definition parsing accepts `interactive` node type without error

## Acceptance Criteria

- `workflow.NodeTypeInteractive` constant exists and is recognized by the workflow parser
- `ExternalNotifier` interface is defined in the notification domain
- `LogNotifier` adapter logs notifications at Info level with all required fields
- Engine's `emitNotification()` calls the external notifier for all notification types
- All existing tests continue to pass
