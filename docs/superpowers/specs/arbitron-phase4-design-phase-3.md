# Phase 4 Implementation Design — Phase 3: Dashboard

## Overview

This phase replaces the `InteractiveCallout` placeholder with a functional chat interface in the execution view's side panel. It implements the callout connector, token-by-token streaming display, markdown rendering, auto-open behavior, and snap-back on session end.

---

## 1. Side Panel Takeover

### Conditional Rendering

The execution view's side panel currently renders two tabs: Node Output and Variables. When the instance is in an interactive wait state (`interactiveActive: true` from the instance detail response), the panel switches entirely to the interactive chat interface. This is a conditional render at the panel container level — same DOM container, different content.

### Panel Sizing

When the interactive chat is active, the default split between graph and panel shifts to approximately 40/60 (graph/panel), giving more room to the conversation. The draggable divider (already present in the execution view) continues to work. When the session ends and the panel snaps back, the split returns to its previous ratio.

---

## 2. Callout Connector

A horizontal SVG element rendered as an overlay in the graph area:

- **Source:** A filled circle anchored to the right edge of the interactive node, at the node's vertical midpoint
- **Line:** A horizontal path from the source circle to the panel border
- **Target:** An arrowhead touching the left edge of the panel

The connector uses the `--state-waiting` orange color variable for consistency with the node's waiting state styling. The line's vertical position tracks the interactive node's Y position in the graph layout (updates if the graph is scrolled or the node is repositioned).

The connector is only visible when the interactive chat panel is active. It disappears on snap-back.

---

## 3. Chat Message Component

Each message in the conversation is rendered as a styled block:

- **Role label** — "ASSISTANT" or "YOU" in small uppercase text, color-coded (blue for assistant, green for user)
- **Left border** — color-coded accent matching the role
- **Content** — rendered markdown via `react-markdown` (or equivalent lightweight markdown library)
- **Scroll behavior** — the message area auto-scrolls to the bottom when new content arrives

### Typing Indicator

When `interactive.token` events are flowing (assistant is generating), a typing indicator appears below the last complete message: a pulsing dot with "Thinking..." text. As tokens arrive, the partial response replaces the typing indicator and grows in real time.

---

## 4. Token Streaming Display

As `interactive.token` events arrive over the WebSocket:

1. The current partial assistant message is built up by concatenating tokens
2. The partial content is displayed in a message bubble styled as an in-progress assistant message
3. The message area auto-scrolls to keep the latest content visible
4. When streaming completes (signaled by `interactive.message` with the full content), the partial display is replaced by the final rendered markdown message

This provides the real-time generation experience users expect from modern chat interfaces.

---

## 5. Input Area

A multiline textarea at the bottom of the panel:

- **Send behavior:** Enter to send, Shift+Enter for newline
- **Send button:** Adjacent to the textarea
- **Disabled state:** Input and button are disabled while the assistant is generating (prevents sending during streaming)
- **Hint text:** "Markdown supported" on the left, "Enter to send · Shift+Enter for newline" on the right
- **Focus:** Input auto-focuses when the panel opens

---

## 6. WebSocket Integration

Extends the existing `useInstanceWebSocket` hook to handle the new event types and support outbound messages:

### Outbound

- `interactive.send` — sends user message content to the backend
- `interactive.end` — signals user-initiated session end

### Inbound Handlers

- `interactive.history` — received on connection, populates the message list with the full conversation history
- `interactive.message` — appends a complete message (user or assistant) to the message list
- `interactive.token` — appends a token to the current streaming buffer for real-time display
- `interactive.ended` — triggers the snap-back to normal panel tabs
- `interactive.error` — displays an error notification to the user

---

## 7. Auto-Open and Snap-Back

### Auto-Open

On component mount (when the execution view loads):

1. Fetch instance details
2. If `interactiveActive` is true, render the chat panel immediately
3. The WebSocket connection sends `interactive.history` on connect, providing the conversation data to hydrate the chat

This means navigating to an instance that's waiting on an interactive step shows the chat immediately — no extra click required.

### Snap-Back

When an `interactive.ended` event is received:

1. Switch the panel back to the normal Node Output / Variables tabs
2. Restore the previous panel width ratio
3. Remove the callout connector from the graph

The transition is immediate — no animation or delay.

### Completed Session Viewing

After a session has ended, clicking the interactive node in the graph loads the completed conversation transcript in the Node Output tab. This uses the existing node execution trace endpoint which includes `llmConversation` — the same data, rendered read-only in the normal tab format.

---

## 8. End Session Button

Located in the panel header, next to the node name. When clicked:

1. Sends `interactive.end` over the WebSocket
2. Button enters a disabled/loading state until `interactive.ended` is received
3. On `interactive.ended`, the snap-back occurs as described above

---

## 9. Testing Strategy

- **Component tests:** Test the chat panel renders messages correctly, handles streaming tokens, and snaps back on session end
- **WebSocket hook tests:** Test that the extended hook sends outbound messages and correctly routes inbound events to state updates
- **Integration tests:** Test the full flow — navigate to a waiting instance, verify auto-open, send a message, verify streaming display, end session, verify snap-back
- **Edge cases:** Test navigation away and back (conversation should reload from history), test rapid messages, test error display
