# Phase 2, Task 1: WebSocket Bidirectional Messaging

## Goal

Extend the existing instance WebSocket stream handler to accept inbound messages, route `interactive.send` and `interactive.end` to the ConversationHandler, and send error events back on failure.

## Prerequisites

- Implementation Phase 1 complete (ConversationHandler exists and is wired up)

## Files to Modify

### Modify: `adapter/http/stream_handler.go`

**Inbound message parsing:**

The existing read goroutine currently discards all inbound frames (it only exists to detect client disconnect). Change it to:

1. Read each frame as a JSON message
2. Parse the `type` field
3. Route based on type:
   - `interactive.send` → extract `content` field, call `conversationHandler.HandleMessage(ctx, instanceID, content)`
   - `interactive.end` → call `conversationHandler.EndSession(ctx, instanceID)`
   - Unknown types → ignore (forward compatibility)
4. On error from the handler, write an `interactive.error` event back to the client

**Inbound message struct:**
```go
type inboundMessage struct {
    Type    string `json:"type"`
    Content string `json:"content,omitempty"`
}
```

**Error response:**
```go
type interactiveErrorEvent struct {
    Type      string    `json:"type"` // "interactive.error"
    Timestamp time.Time `json:"timestamp"`
    Data      struct {
        Message string `json:"message"`
    } `json:"data"`
}
```

**Concurrency considerations:**
- The read goroutine runs independently from the write goroutine (which handles outbound events from the event bus)
- The read goroutine must not block the write goroutine
- HandleMessage() is synchronous from the read goroutine's perspective — it blocks until the LLM response is fully streamed. This is intentional: the user can't send another message while the assistant is generating (the UI disables input during streaming)
- While HandleMessage() blocks the read goroutine, it publishes `interactive.token` and `interactive.message` events to the EventBus. These flow to the write goroutine via the event bus channel subscription, which runs independently. The EventBus uses buffered channels (256 buffer), so the read goroutine's publishes don't block waiting for the write goroutine.
- Error writes from the read goroutine must use the same write mutex/coordination as the outbound event loop to prevent concurrent WebSocket writes

### Modify: `StreamHandler` constructor

Add `ConversationHandler` as a dependency:
```go
func NewStreamHandler(instances instance.InstanceRepository, eventBus EventBus, conversationHandler engine.ConversationHandler) *StreamHandler
```

### Modify: `adapter/http/server.go` (router)

Pass the `ConversationHandler` to the `StreamHandler` constructor.

## Testing

- **Integration test:** Connect via WebSocket to an instance in interactive wait state. Send `{"type": "interactive.send", "content": "hello"}`. Verify:
  - ConversationHandler.HandleMessage is called with correct instanceID and content
  - Events published by the handler (interactive.token, interactive.message) flow back over the WebSocket

- **End session test:** Send `{"type": "interactive.end"}`. Verify:
  - ConversationHandler.EndSession is called
  - `interactive.ended` event flows back

- **Error test:** Send `interactive.send` to an instance NOT in interactive wait state. Verify:
  - `interactive.error` event received with descriptive message

- **Unknown type test:** Send `{"type": "unknown.thing"}`. Verify no error, no crash — message is silently ignored.

- **Concurrent write safety:** Verify that error events from the read goroutine don't race with outbound events from the write goroutine.

## Acceptance Criteria

- WebSocket accepts `interactive.send` and routes to ConversationHandler
- WebSocket accepts `interactive.end` and routes to EndSession
- Error responses are sent back as `interactive.error` events
- Unknown message types are ignored
- No concurrent write panics under load
- Existing outbound event streaming continues to work unchanged
