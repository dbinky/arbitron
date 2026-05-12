# Phase 2, Task 3: Event Type Definitions & Integration Test

## Goal

Define the new interactive event type constants in the event bus domain, ensure they flow correctly through the existing pub/sub infrastructure, and write an end-to-end integration test that verifies the full message round-trip.

## Prerequisites

- Tasks 1 and 2 complete (WebSocket bidirectional messaging and history hydration work)

## Files to Modify

### Modify: `adapter/eventbus/eventbus.go` (or relevant event type file)

Add event type constants for the new interactive events:
- `EventTypeInteractiveMessage` = `"interactive.message"`
- `EventTypeInteractiveToken` = `"interactive.token"`
- `EventTypeInteractiveEnded` = `"interactive.ended"`
- `EventTypeInteractiveHistory` = `"interactive.history"`
- `EventTypeInteractiveError` = `"interactive.error"`

Ensure the Event struct can carry the interactive event data payloads (it should already support arbitrary JSON data via an interface{} or map field — verify and extend if needed).

### Create: `adapter/http/interactive_handler_test.go` (or extend existing test file)

**End-to-end integration test:**

1. Set up: create an instance, run it to an interactive node, let the executor transition it to waiting
2. Connect WebSocket to the instance
3. Verify `interactive.history` is received with the initial conversation
4. Send `{"type": "interactive.send", "content": "test message"}` over WebSocket
5. Verify `interactive.message` (user) event is received
6. Verify `interactive.token` events flow during LLM streaming (use a mock LLM that emits known tokens)
7. Verify `interactive.message` (assistant) event is received after streaming completes
8. Send `{"type": "interactive.end"}` over WebSocket
9. Verify `interactive.ended` event is received with reason `user_ended`
10. Verify instance transitioned to `queued`

**Tool completion integration test:**

1. Same setup as above
2. Send a message that causes the mock LLM to invoke `complete_session`
3. Verify `interactive.ended` event with reason `llm_completed` and structured output
4. Verify instance transitioned to `queued`
5. Verify node execution record has the structured output

## Testing

This task IS the integration test. The tests verify the full round-trip through all components built in Phase 2.

## Acceptance Criteria

- All interactive event type constants are defined and documented
- Events flow correctly through the event bus from ConversationHandler → EventBus → WebSocket → client
- Full round-trip integration test passes: send message → receive streaming → receive complete message
- Session completion via tool use integration test passes
- Session completion via user end integration test passes
