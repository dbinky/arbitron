# Phase 4 Implementation Design — Phase 2: API & WebSocket

## Overview

This phase extends the instance WebSocket to support bidirectional messaging for interactive sessions, adds new outbound event types for conversation streaming, provides a REST endpoint for ending sessions, and modifies the instance detail response to expose interactive state.

---

## 1. WebSocket Bidirectional Messaging

### Inbound Message Handling

Extend the existing `StreamHandler` in `adapter/http/stream_handler.go`. Currently, the read goroutine discards all inbound frames (it exists only to detect client disconnect). Change it to parse inbound JSON messages and route by type.

**Inbound message format:**

```json
{"type": "interactive.send", "content": "user's message text"}
{"type": "interactive.end"}
```

**Routing logic:**

- `interactive.send` — validate that the instance is in `waiting` state on an interactive node, then pass the content to the `ConversationHandler.HandleMessage()` from Phase 1. Responses flow back through the event bus and the existing outbound event loop.
- `interactive.end` — validate state, then trigger user-initiated session end (same logic as the REST endpoint). Publishes `interactive.ended` event which the outbound loop delivers.
- Unknown types — ignore (forward compatibility).

**Error handling:**

If validation fails (instance not in interactive wait state) or the conversation handler returns an error, send an error event back over the WebSocket:

```json
{"type": "interactive.error", "message": "description of what went wrong"}
```

### Connection Behavior

The existing outbound event loop remains unchanged — it still reads from the event bus channel and writes events to the WebSocket. The inbound message handling runs in the existing read goroutine (which previously discarded frames). The two directions are independent and do not block each other.

---

## 2. New Outbound Event Types

### interactive.history

Sent once on WebSocket connection when the instance is in `waiting` state on an interactive node. Contains the full conversation array so the client can hydrate the chat panel without a separate REST call.

```json
{
  "type": "interactive.history",
  "timestamp": "...",
  "data": {
    "nodeId": "review-design",
    "messages": [
      {"role": "assistant", "content": "I've reviewed the spec..."},
      {"role": "user", "content": "Let's go with option B."},
      {"role": "assistant", "content": "Great choice..."}
    ]
  }
}
```

This event is sent proactively by the server-side handler immediately after the WebSocket connection is established and the subscription is set up — before any live events flow. The handler checks if the instance is in `waiting` state on an interactive node; if so, it reads the node execution record's `llm_conversation` field and sends this event. If the instance is not in an interactive state, this event is not sent.

### interactive.message

A complete message was added to the conversation. Sent after each user message is persisted and after each assistant response finishes streaming.

```json
{
  "type": "interactive.message",
  "timestamp": "...",
  "data": {
    "role": "assistant",
    "content": "The full message content with **markdown**..."
  }
}
```

### interactive.token

A single streaming token from the LLM response. Sent during assistant response generation for real-time display.

```json
{
  "type": "interactive.token",
  "timestamp": "...",
  "data": {
    "token": "the"
  }
}
```

### interactive.ended

The session has concluded. Delivered when either the LLM completes the session or the user ends it.

```json
{
  "type": "interactive.ended",
  "timestamp": "...",
  "data": {
    "reason": "llm_completed",
    "output": {"design": "...", "approved": true}
  }
}
```

### interactive.error

An error occurred processing an inbound message.

```json
{
  "type": "interactive.error",
  "timestamp": "...",
  "data": {
    "message": "instance is not in interactive wait state"
  }
}
```

---

## 3. REST Endpoint

### POST /api/v1/instances/{id}/interact/end

Ends an interactive session via REST (alternative to the WebSocket `interactive.end` message).

**Preconditions:** Instance must be in `waiting` state on an interactive node.

**Behavior:**
1. Capture whatever structured output exists (from a prior `complete_session` tool call), or empty object `{}` if none
2. Store the output on the node execution record
3. Update the instance variable store with the node output
4. Transition instance from `waiting` to `queued`
5. Publish `interactive.ended` event (reason: `user_ended`)
6. Call `QueueManager.NotifyCapacityFreed()`

**Response:** `200 OK` with the captured output:

```json
{"output": {"design": "...", "approved": true}}
```

**Error responses:**
- `404` — instance not found
- `409` — instance is not in interactive wait state

---

## 4. Modified Instance Detail Response

### GET /api/v1/instances/{id}

Add two fields to the existing response:

- `interactiveNodeId` — the node ID of the interactive node currently waiting for input. Null if the instance is not in an interactive wait state.
- `interactiveActive` — boolean. True when the instance is in `waiting` state on an interactive node.

These fields are derived from the instance's current state and the node type of the current node — no new database columns needed.

---

## 5. Testing Strategy

- **WebSocket inbound routing:** Integration test that connects via WebSocket, sends `interactive.send` and `interactive.end` messages, and verifies the correct events flow back
- **Event hydration:** Test that connecting to a WebSocket for an instance in interactive wait immediately receives `interactive.history` with the correct conversation
- **REST endpoint:** Test `POST /interact/end` transitions instance state and returns output
- **Instance detail:** Test that the response includes `interactiveNodeId` and `interactiveActive` fields when appropriate
- **Error cases:** Test that sending messages to non-interactive instances returns `interactive.error`
