# Phase 4 Implementation Design — Phase 1: Domain & Executor

## Overview

This phase adds the interactive node type to the domain layer, builds the executor that initiates conversations and transitions to waiting, creates the conversation turn handler for processing subsequent messages, implements the session completion mechanism via tool-use, and adds the external notification interface with a logging-only implementation.

---

## 1. Interactive Node Type

Add `interactive` to the `workflow.NodeType` constants in `domain/workflow/workflow.go`, alongside the existing seven types.

### Workflow Definition Config

The `interactive` node type in `workflow.json` uses these config fields:

- `systemPrompt` — inline string; persona and behavioral instructions
- `orchestrationFile` — relative path to a markdown file with skill-like process instructions
- `templateFile` — relative path to a markdown template with `{{variable}}` substitution
- `model` — LLM model identifier
- `outputSchema` — JSON schema for the structured output the LLM produces at session completion
- `maxTokens` — maximum tokens per LLM response
- `temperature` — sampling temperature

---

## 2. InteractiveExecutor

A new `NodeExecutor` implementation following the pattern established by `TimerDelayExecutor` and `WebhookWaitExecutor`.

### Dependencies

- `InstanceRepository` — to transition instance state
- `NodeExecutionRepository` — to write and update execution records
- `LLMProvider` — to make the initial LLM call
- `FileReader` — to read orchestration and template files from the workflow folder
- `EventBus` — to publish events for real-time streaming
- `QueueManager` — to notify capacity freed on wait transition
- `NotificationRepository` — to create the needs_interaction notification
- `ExternalNotifier` — to fire the external notification stub

### Execute() Flow

1. Read the orchestration file and template file via `FileReader`
2. Resolve `{{variable}}` substitutions in the template using the current variable store
3. Compose the initial LLM request:
   - System message: the `systemPrompt` config value
   - User message: orchestration prompt content + substituted template content
   - Include the `complete_session` tool definition (see Section 4)
4. Call `LLMProvider.StreamCall()` to get the first assistant response
5. Stream tokens via `EventBus.Publish()` as `interactive.token` events
6. When streaming completes, assemble the full assistant message
7. Write the node execution record with the initial conversation in `llm_conversation`
8. Create a `needs_interaction` notification and persist it
9. Call `ExternalNotifier.Notify()` with the notification
10. Publish `notification.created` event to the event bus
11. Transition the instance to `waiting` state
12. Call `QueueManager.NotifyCapacityFreed()`
13. Return `NodeResult{IsWait: true}`

### Registration

Add to the executor map in `cmd/arbitron/main.go`:

```
workflow.NodeTypeInteractive: engine.NewInteractiveExecutor(...)
```

---

## 3. Conversation Turn Handler

A new domain service responsible for processing user messages after the initial executor call. This is not an executor — it is invoked by the API/WebSocket layer when a user sends a message during an active interactive session.

### Interface

```
ConversationHandler interface {
    HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error
}
```

### HandleMessage() Flow

1. Load the instance to verify it's in `waiting` state on an interactive node
2. Load the node execution record to get the conversation history from `llm_conversation`
3. Append the user message to the conversation history
4. Persist the updated history immediately (user message is durable before LLM call)
5. Publish `interactive.message` event (role: user, content)
6. Reconstruct the full LLM request from the conversation history:
   - System message: system prompt from the node config (read from workflow snapshot)
   - Messages: the full conversation history
   - Include the `complete_session` tool definition
7. Call `LLMProvider.StreamCall()` with the full conversation
8. Stream tokens via `EventBus.Publish()` as `interactive.token` events
9. When streaming completes:
   - If the LLM invoked `complete_session`: extract the tool arguments as structured output, store as the node's `output`, transition instance from `waiting` to `queued`, publish `interactive.ended` event with reason `llm_completed`
   - Otherwise: append the assistant message to the conversation history, persist, publish `interactive.message` event (role: assistant, content)

### Dependencies

- `InstanceRepository` — to read instance state and transition on completion
- `NodeExecutionRepository` — to read/update conversation history
- `WorkflowRepository` or instance's `workflow_snapshot` — to access node config for system prompt and tool definition
- `LLMProvider` — for streaming LLM calls
- `EventBus` — for real-time event publishing
- `QueueManager` — to notify capacity freed when session completes
- `variable.VariableStore` — to update node output in the instance variable store

---

## 4. Session Completion Tool

The LLM is provided with a tool definition that it invokes to end the session:

- **Tool name:** `complete_session`
- **Description:** "Call this tool when the conversation has reached its natural conclusion and you have the final output ready."
- **Parameters:** Derived from the node's `outputSchema` config field

When the LLM calls this tool during a streaming response:

1. The backend detects the tool use in the stream
2. Extracts the tool arguments as the structured JSON output
3. Stores the output on the node execution record
4. Updates the instance's variable store with the node output (namespaced under the node ID)
5. Transitions the instance from `waiting` to `queued`
6. Publishes `interactive.ended` event with reason `llm_completed` and the structured output
7. Calls `QueueManager.NotifyCapacityFreed()`

### User-Initiated End

When the user ends the session (via API or WebSocket):

1. Check if the LLM has previously called `complete_session` in any turn — if so, that output is already stored
2. If no structured output exists, store an empty object `{}` as the node output
3. Transition the instance from `waiting` to `queued`
4. Publish `interactive.ended` event with reason `user_ended`
5. Call `QueueManager.NotifyCapacityFreed()`

---

## 5. External Notification Interface

### Port (Domain Layer)

A new interface in `domain/notification/`:

```
ExternalNotifier interface {
    Notify(ctx context.Context, notification *Notification) error
}
```

### Adapter (Logging Implementation)

A new adapter that implements `ExternalNotifier` by writing notification details to `slog`:

- Logs at `Info` level with structured fields: notification ID, instance ID, workflow name, type, timestamp
- Returns nil (never errors)

### Engine Integration

The engine's `emitNotification()` function (which already creates in-app notifications for `failed` and `completed` instances) is extended to also call `ExternalNotifier.Notify()`. The `InteractiveExecutor` does the same when creating the `needs_interaction` notification.

### Wiring

The logging notifier is instantiated in `main.go` and passed to the engine and the interactive executor.

---

## 6. Testing Strategy

- **InteractiveExecutor:** Unit test that verifies the executor reads files, calls the LLM, writes the execution record, creates a notification, transitions to waiting, and returns `IsWait: true`
- **ConversationHandler:** Unit test that verifies message appending, LLM call with full history, streaming event publishing, and session completion detection
- **Session completion:** Unit test that verifies tool-use detection extracts structured output and transitions instance state
- **External notifier:** Unit test for the logging adapter (verify slog output)
- **Integration:** Test the full flow from executor through conversation handler to session end, using mock LLM provider and in-memory repositories
