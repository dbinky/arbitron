# Phase 3, Task 1: WebSocket Hook Extension & Chat State Management

## Goal

Extend the existing `useInstanceWebSocket` hook to support bidirectional messaging (sending `interactive.send` and `interactive.end`) and handle new inbound event types (`interactive.history`, `interactive.message`, `interactive.token`, `interactive.ended`, `interactive.error`). Create the React state management for the chat interface.

## Prerequisites

- Implementation Phase 2 complete (backend sends/accepts all interactive event types)

## Files to Create/Modify

### Create: `dashboard/src/hooks/useInteractiveSession.ts`

A custom hook that wraps the instance WebSocket and provides a chat-specific interface:

**State:**
- `messages: Message[]` — complete messages in the conversation
- `streamingContent: string` — partial content being streamed (current assistant response)
- `isStreaming: boolean` — true while tokens are arriving
- `isActive: boolean` — true while the interactive session is live
- `error: string | null` — last error message

**Message type:**
```typescript
interface Message {
  role: 'assistant' | 'user'
  content: string
}
```

**Actions (returned by hook):**
- `sendMessage(content: string)` — sends `{type: "interactive.send", content}` over WebSocket
- `endSession()` — sends `{type: "interactive.end"}` over WebSocket

**Inbound event handling:**
- `interactive.history` → set `messages` to the received array, set `isActive` to true
- `interactive.message` → append to `messages` array, clear `streamingContent` if role is assistant
- `interactive.token` → append token to `streamingContent`, set `isStreaming` to true
- `interactive.ended` → set `isActive` to false, clear streaming state
- `interactive.error` → set `error` field

**Streaming → message transition:**
When `interactive.message` with role `assistant` arrives after streaming tokens, clear `streamingContent` and `isStreaming`. The `interactive.message` event contains the full content, so the partial streaming display is replaced.

### Modify: `dashboard/src/hooks/useInstanceWebSocket.ts` (or `useWebSocket.ts`)

If the existing hook doesn't support sending messages (only receiving), extend it:
- Add a `send(data: object)` function that JSON-serializes and sends over the WebSocket
- Expose the send function to consumers

### Create: `dashboard/src/api/types.ts` additions

Add TypeScript types for the interactive event payloads:
```typescript
interface InteractiveHistoryEvent {
  type: 'interactive.history'
  data: { nodeId: string; messages: Message[] }
}
interface InteractiveMessageEvent {
  type: 'interactive.message'
  data: { role: 'assistant' | 'user'; content: string }
}
interface InteractiveTokenEvent {
  type: 'interactive.token'
  data: { token: string }
}
interface InteractiveEndedEvent {
  type: 'interactive.ended'
  data: { reason: 'llm_completed' | 'user_ended'; output?: any }
}
interface InteractiveErrorEvent {
  type: 'interactive.error'
  data: { message: string }
}
```

## Testing

- Unit test: `useInteractiveSession` processes `interactive.history` → messages populated
- Unit test: `interactive.token` events accumulate in `streamingContent`
- Unit test: `interactive.message` (assistant) clears streaming state and appends to messages
- Unit test: `interactive.ended` sets `isActive` to false
- Unit test: `sendMessage` serializes and sends correct JSON over WebSocket
- Unit test: `endSession` sends correct JSON

## Acceptance Criteria

- Hook provides clean state interface for building the chat UI
- Sending messages produces correctly formatted WebSocket frames
- All five inbound event types are handled and update state correctly
- Streaming → complete message transition is smooth (no flicker or duplication)
