# Phase 5 Implementation Design — Phase 2: MCP Streaming & Interactive

## Overview

This phase adds real-time event streaming to the MCP server, implements the Streamable HTTP transport for remote access, wires interactive session participation through MCP, and defines reconnection behavior. After this phase, Claude Code has full feature parity with the dashboard for monitoring and interactive sessions.

---

## 1. MCP Event Streaming

The MCP server subscribes to the domain `EventBus` and forwards events to Claude Code as MCP notifications.

### Subscribing

When a client begins monitoring an instance (or subscribes to system-wide events), the MCP adapter calls `EventBus.Subscribe(instanceID)` or `EventBus.SubscribeAll()` — the same methods the WebSocket stream handler uses. The returned channel delivers domain events; the MCP adapter translates each into an MCP notification and sends it to the client.

### Event-to-Notification Mapping

Each domain event type maps to an MCP notification:

- `node.started`, `node.completed`, `node.failed` — workflow progress notifications
- `interactive.token` — LLM token streaming during interactive sessions
- `interactive.message` — user or assistant message in an interactive session
- `interactive.ended` — interactive session completed
- `instance.completed`, `instance.failed`, `instance.cancelled` — instance lifecycle notifications
- `notification.created` — in-app notification (needs interaction, failed, completed)

### Subscription Lifecycle

Subscriptions are tied to the MCP session. When the MCP connection closes, all subscriptions are cleaned up via the unsubscribe functions returned by the EventBus.

### New MCP Tools

- `instance_subscribe` — start receiving events for a specific instance
- `instance_unsubscribe` — stop receiving events for an instance
- `system_subscribe` — start receiving system-wide events (all instances)
- `system_unsubscribe` — stop receiving system-wide events

---

## 2. Streamable HTTP Transport

The Streamable HTTP transport enables remote Claude Code sessions to connect to the MCP server over HTTP. This is used in shared/org mode where the Arbitron server runs on a different machine.

### Endpoint

The Streamable HTTP transport is served at `/mcp` on the existing HTTP server. When `arbitron serve` runs, the MCP endpoint is always available alongside the REST API and dashboard. No additional flag needed (unlike `--mcp` for stdio).

### How It Works

Streamable HTTP uses standard HTTP POST requests for client-to-server messages, with streaming responses (chunked transfer or SSE) for server-to-client messages. This allows MCP notifications (event streaming) to flow over the same HTTP connection without WebSocket.

### Authentication

In Phase 5 (local mode), the Streamable HTTP endpoint is unauthenticated — same as the REST API. When Phase 6 adds multi-user auth, the MCP endpoint respects the same authentication and role-gating as the REST API.

### Server Mode Summary

- `arbitron serve` — HTTP server + dashboard + Streamable HTTP MCP endpoint. No stdio.
- `arbitron mcp` — stdio MCP only. No HTTP.
- `arbitron serve --mcp` — all of the above plus stdio MCP.

---

## 3. Interactive Session Participation

Claude Code participates in interactive sessions through the same `ConversationHandler` the dashboard uses. The MCP adapter is a thin translation layer between MCP tool calls and the domain service.

### New MCP Tools

- `interactive_send_message` — sends a user message to an active interactive session. Takes an instance ID and message content. Calls `ConversationHandler.HandleMessage()`. The response streams back via MCP notifications (`interactive.token` events as the LLM generates its reply, then `interactive.message` with the complete assistant response).
- `interactive_end_session` — ends an interactive session early (user-initiated). Takes an instance ID. Calls the same end-session logic the dashboard uses. Publishes `interactive.ended` with reason `user_ended`.
- `interactive_get_history` — retrieves the full conversation history for an interactive session. Takes an instance ID. Reads from the node execution record's `llm_conversation` field. Used on reconnection to load prior messages.

### Flow from Claude Code's Perspective

1. The running skill detects a workflow has reached an interactive step (via an `interactive.message` event from the subscription — the LLM's opening message)
2. The skill presents the conversation to the user
3. The user types a response; the skill calls `interactive_send_message`
4. The MCP adapter calls `ConversationHandler.HandleMessage()`
5. LLM tokens stream back as `interactive.token` notifications
6. The complete assistant message arrives as an `interactive.message` notification
7. Steps 3-6 repeat until the LLM ends the session or the user calls `interactive_end_session`

### Session Ownership

There is none. The server doesn't track which client is driving the conversation. Any client (Claude Code or dashboard) can send a message at any time. The conversation history is the single source of truth. If two clients send messages simultaneously, they're processed in the order the server receives them.

---

## 4. Reconnection Behavior

When an MCP connection drops and the client reconnects, the MCP server has no session memory — each connection is stateless from the server's perspective. All state lives in the domain layer (database and event bus).

### What the Client Does on Reconnect

1. Establishes a new MCP session (initialize handshake)
2. Calls `instance_get` or `instance_list` to check the state of instances it was tracking
3. If an interactive session was in progress, calls `interactive_get_history` to load the full conversation, then resumes with `interactive_send_message`
4. Re-subscribes to event streams via `instance_subscribe` for any instances it wants to monitor

### What the Server Does

Nothing special. It doesn't know a reconnection happened. It just handles tool calls and subscriptions as they arrive.

### No Missed Events

Events that occurred while disconnected are not replayed. The client gets the current state, not a history of state changes. This is consistent with how the dashboard works — navigate to an instance and you see where it is now, not a replay of what happened while you were away. The execution trace provides the full history if the client needs it.

### Interactive Session Continuity

The conversation history is persisted message-by-message in the node execution record. A reconnecting client loads the history and continues. The LLM's context is reconstructed from the stored conversation on each turn — there's no in-memory session state that could be lost.

---

## 5. Testing Strategy

All code is developed using strict TDD — tests written before implementation across five scenario categories.

### MCP Event Streaming

- **Happy path:** Subscribe to an instance, trigger a node execution, receive `node.started` and `node.completed` notifications with correct data shapes.
- **Success scenarios:** Subscribe to multiple instances simultaneously, events route to correct subscriptions. System-wide subscription receives events from all instances. Unsubscribe stops event delivery.
- **Failure scenarios:** Subscribe to a non-existent instance ID — returns error, no subscription created. Unsubscribe with an ID that has no active subscription — no-op, no error.
- **Error scenarios:** EventBus channel fills up (slow consumer) — events are dropped, not blocked. MCP connection closes mid-stream — subscriptions are cleaned up, no goroutine leaks.
- **Edge cases:** Subscribe and unsubscribe rapidly in succession. Receive events during the window between subscribing and the subscribe response being sent. Multiple subscriptions to the same instance — each gets its own event stream.

### Streamable HTTP Transport

- **Happy path:** POST a JSON-RPC request to `/mcp`, receive a well-formed response. Tool discovery and tool invocation work over HTTP.
- **Success scenarios:** Streaming responses deliver MCP notifications as chunked data. Multiple concurrent HTTP clients each receive their own event streams. Full round-trip: create workflow, trigger, subscribe, receive events, all over Streamable HTTP.
- **Failure scenarios:** Malformed JSON-RPC over HTTP returns parse error with correct HTTP status. Request to `/mcp` with wrong HTTP method returns 405.
- **Error scenarios:** Client disconnects mid-streaming-response — server cleans up subscriptions without crashing. Server shutdown while Streamable HTTP clients are connected — connections close gracefully.
- **Edge cases:** Very long-lived streaming connections. Client sends next request before previous streaming response completes. Large payloads over Streamable HTTP (full execution traces).

### Interactive Session Participation

- **Happy path:** Send a message via `interactive_send_message`, receive `interactive.token` notifications as the LLM streams, then `interactive.message` with the complete response. End a session via `interactive_end_session`, receive `interactive.ended`.
- **Success scenarios:** Multi-turn conversation — send three messages, each produces a streamed response. LLM calls `complete_session` tool, session ends automatically with structured output. Get history returns messages in order after a multi-turn exchange.
- **Failure scenarios:** Send message to an instance that isn't in waiting state — returns error. Send message to an instance that doesn't exist — returns error. End session on an instance that isn't in an interactive step — returns error.
- **Error scenarios:** LLM provider returns an error mid-stream — `interactive.error` event published, session stays open for retry. ConversationHandler encounters a database error — error propagated cleanly to the MCP client.
- **Edge cases:** Send message while LLM is still generating a response to a previous message. Two clients (MCP and dashboard) send messages to the same session simultaneously. Empty message content. Very long message content.

### Reconnection

- **Happy path:** Connect, subscribe, disconnect, reconnect, call `instance_get` — current state is returned. Call `interactive_get_history` — full conversation is present.
- **Success scenarios:** Instance completed while disconnected — reconnect shows completed state with full trace. Interactive session had messages from dashboard while MCP was disconnected — history includes all messages.
- **Failure scenarios:** Reconnect and try to subscribe to an instance that was cancelled while disconnected — subscription works, no events arrive (terminal state).
- **Error scenarios:** Reconnect with corrupt or partial state on the client side — server doesn't care, responds to whatever tools are called.
- **Edge cases:** Rapid disconnect/reconnect cycles. Reconnect after the server itself was restarted (crash recovery ran, waiting instances restored).
