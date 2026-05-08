# Phase 1, Task 2: InteractiveExecutor

## Goal

Build the `InteractiveExecutor` that initiates the LLM conversation, streams the first response, creates the needs_interaction notification, and transitions the instance to waiting.

## Prerequisites

- Task 1 complete (interactive node type exists, ExternalNotifier interface available)

## Files to Create/Modify

### Create: `domain/engine/executor_interactive.go`

Implement `NodeExecutor` interface.

**Constructor:**
```go
func NewInteractiveExecutor(
    instances instance.InstanceRepository,
    nodeExecs instance.NodeExecutionRepository,
    llmProvider LLMProvider,
    fileReader FileReader,
    eventBus EventBus,
    queueMgr QueueManager,
    notifications notification.Repository,
    externalNotifier notification.ExternalNotifier,
) *InteractiveExecutor
```

**Execute() implementation:**

1. Extract instance ID from context via `InstanceIDFromContext(ctx)`
2. Read the orchestration file: `fileReader.ReadFile(workflowName, node.Config["orchestrationFile"])`
3. Read the template file: `fileReader.ReadFile(workflowName, node.Config["templateFile"])`
4. Resolve `{{variable}}` substitutions in the template content using `vars`
5. Build the `complete_session` tool definition from `node.Config["outputSchema"]`
6. Compose the LLM request:
   - System message: `node.Config["systemPrompt"]`
   - User message: orchestration content + "\n\n" + resolved template content
   - Model: `node.Config["model"]`
   - MaxTokens: `node.Config["maxTokens"]`
   - Temperature: `node.Config["temperature"]`
   - Tools: `[complete_session]`
7. Call `llmProvider.StreamCall(ctx, request)`
8. Consume the stream channel:
   - For each token chunk: publish `interactive.token` event via eventBus
   - Accumulate tokens into full response
   - If `chunk.Done`: break
   - If `chunk.Error`: return error
9. Build the initial conversation array: `[{role: "user", content: orchestration+template}, {role: "assistant", content: fullResponse}]`
10. Write node execution record: status `succeeded` (consistent with timer/webhook executors — the executor completed its job of initiating the session), `llm_conversation` = conversation array
11. Create notification: `notification.Notification{ID: uuid.New(), InstanceID, WorkflowName: workflowName, Type: notification.TypeNeedsInteraction}`
12. Persist notification via `notifications.Create(ctx, n)`
13. Call `externalNotifier.Notify(ctx, n)`
14. Publish `notification.created` event via eventBus
15. Load the instance, transition to `instance.StateWaiting`
16. Call `queueMgr.NotifyCapacityFreed()`
17. Return `&NodeResult{IsWait: true, LLMConversation: conversation, LLMModel: model, LLMTokensIn: tokensIn, LLMTokensOut: tokensOut}`

### Modify: `cmd/arbitron/main.go`

- Add `InteractiveExecutor` to the executor map:
  ```go
  workflow.NodeTypeInteractive: engine.NewInteractiveExecutor(instanceRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, logNotifier),
  ```

### Modify: `domain/engine/engine.go` (if needed)

- Ensure the engine loop handles `IsWait: true` correctly for the interactive executor (it should already — same pattern as timer/webhook)
- Ensure the node execution record write doesn't conflict with the executor writing its own record (check if the engine also writes one, and if so, skip the double-write)

## Testing

- Unit test with mock LLM provider that returns a canned stream:
  - Verify file reading is called with correct paths
  - Verify variable substitution occurs in template
  - Verify StreamCall is invoked with correct system prompt, user message, and tool definition
  - Verify interactive.token events are published for each stream chunk
  - Verify notification is created with correct type and instance ID
  - Verify external notifier is called
  - Verify instance transitions to waiting
  - Verify QueueManager.NotifyCapacityFreed() is called
  - Verify NodeResult has IsWait: true

## Acceptance Criteria

- InteractiveExecutor successfully initiates a conversation, streams the first response, and transitions to waiting
- The first LLM response is persisted in llm_conversation on the node execution record
- A needs_interaction notification is created (both in-app and external)
- The instance is in waiting state and capacity is freed
- The engine exits its run loop cleanly (same as timer/webhook)
