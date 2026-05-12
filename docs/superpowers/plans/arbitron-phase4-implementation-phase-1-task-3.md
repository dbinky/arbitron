# Phase 1, Task 3: Conversation Turn Handler & Session Completion

## Goal

Build the `ConversationHandler` domain service that processes user messages during an active interactive session, manages the LLM conversation loop, detects session completion via tool-use, and handles user-initiated session end.

## Prerequisites

- Task 2 complete (InteractiveExecutor exists, node execution records are being written with llm_conversation)

## Files to Create/Modify

### Create: `domain/engine/conversation.go`

**Interface:**
```go
type ConversationHandler interface {
    HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error
    EndSession(ctx context.Context, instanceID uuid.UUID) error
}
```

**Implementation struct:**
```go
type conversationHandler struct {
    instances   instance.InstanceRepository
    nodeExecs   instance.NodeExecutionRepository
    llmProvider LLMProvider
    eventBus    EventBus
    queueMgr    QueueManager
}
```

**Constructor:**
```go
func NewConversationHandler(
    instances instance.InstanceRepository,
    nodeExecs instance.NodeExecutionRepository,
    llmProvider LLMProvider,
    eventBus EventBus,
    queueMgr QueueManager,
) ConversationHandler
```

### HandleMessage() Implementation

1. Load instance by ID — verify `state == StateWaiting` and current node type is `interactive`. Return error if not.
2. Parse the workflow snapshot from instance to get the interactive node's config (systemPrompt, outputSchema)
3. Load the node execution record for the current node (latest attempt) to get `llm_conversation`
4. Parse `llm_conversation` as a message array
5. Append `{role: "user", content: content}` to the array
6. Persist the updated array to the node execution record immediately
7. Publish `interactive.message` event: `{role: "user", content: content}`
8. Build the `complete_session` tool definition from node config's `outputSchema`
9. Build the LLM request:
   - System message: node config `systemPrompt`
   - Messages: full conversation array (excluding the initial system-level orchestration — that was the first user message)
   - Tools: `[complete_session]`
   - Model, MaxTokens, Temperature from node config
10. Call `llmProvider.StreamCall(ctx, request)`
11. Consume the stream:
    - For each token: publish `interactive.token` event via eventBus with instance ID
    - Accumulate tokens into full response
    - Detect tool use blocks in the stream
12. When streaming completes, check for tool use:

**If `complete_session` tool was called:**
- Extract tool arguments as structured JSON output
- Store output on the node execution record's `output` field
- Update the instance's `variables` field (namespace output under node ID, same as engine does for regular nodes)
- Append the assistant message (including tool call) to conversation history, persist
- Transition instance from `waiting` to `queued`
- Publish `interactive.ended` event: `{reason: "llm_completed", output: structuredOutput}`
- Call `queueMgr.NotifyCapacityFreed()`

**If no tool call (normal response):**
- Append `{role: "assistant", content: fullResponse}` to conversation array
- Persist updated conversation
- Publish `interactive.message` event: `{role: "assistant", content: fullResponse}`

### EndSession() Implementation

1. Load instance — verify state is `waiting` on an interactive node
2. Load node execution record
3. Check if `output` field already has data (from a prior `complete_session` call in a previous turn)
4. If no output exists: store `{}` as the node output
5. Update instance variables with node output (namespaced under node ID)
6. Transition instance from `waiting` to `queued`
7. Publish `interactive.ended` event: `{reason: "user_ended", output: capturedOutput}`
8. Call `queueMgr.NotifyCapacityFreed()`

### Modify: `cmd/arbitron/main.go`

- Instantiate the `ConversationHandler` and make it available to the HTTP layer (pass it to the router or handler constructor)

## Testing

### HandleMessage Tests

- **Happy path:** Mock LLM returns a normal response. Verify:
  - User message appended and persisted before LLM call
  - interactive.message event published for user message
  - interactive.token events published during streaming
  - Assistant message appended and persisted after streaming
  - interactive.message event published for assistant message

- **Session completion:** Mock LLM returns a tool_use response with complete_session. Verify:
  - Structured output extracted from tool arguments
  - Output stored on node execution record
  - Instance variables updated with namespaced output
  - Instance transitions to queued
  - interactive.ended event published with reason "llm_completed"
  - QueueManager.NotifyCapacityFreed() called

- **Invalid state:** Call HandleMessage on an instance that's not in interactive wait. Verify error returned.

### EndSession Tests

- **With existing output:** Node execution record already has output from prior tool call. Verify that output is preserved, instance transitions to queued.
- **Without output:** No prior tool call. Verify empty object stored, instance transitions to queued.
- **Invalid state:** Instance not waiting. Verify error returned.

## Acceptance Criteria

- Users can send messages that are appended to the conversation and trigger LLM responses
- Token-by-token streaming events are published during LLM generation
- When the LLM calls `complete_session`, the session ends cleanly with structured output
- User-initiated end captures whatever output exists and transitions correctly
- All conversation history is durable — survives between calls without loss
- Instance variable store is updated with node output on session completion
