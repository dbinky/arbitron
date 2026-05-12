# Arbitron Phase 4 — Interactive Steps

## Overview

Phase 4 adds the interactive node type to the Arbitron, enabling workflows to pause and hold multi-turn conversations with users through the dashboard. It completes the conversational interface placeholder built in Phase 3, adds an external notification stub for future delivery transports, and establishes the indefinite wait behavior for interactive steps.

---

## 1. Interactive Node Type

A new node type: **Interactive**. An interactive node is an LLM-driven conversational session that requires human participation before the workflow can continue.

### Configuration

- **System prompt** — persona and behavioral instructions for the LLM (e.g., "You are a senior architect helping design systems")
- **Orchestration prompt file** — a markdown file containing skill-like process instructions that guide the LLM through the conversation (e.g., "Ask clarifying questions one at a time, then propose approaches, then present the design for approval")
- **Template file** — a markdown template with `{{variable}}` substitution, providing instance-specific context for the conversation
- **Model** — which LLM model to use for the conversation
- **Output schema** — the structured JSON shape the LLM produces when the conversation concludes
- **Max tokens** — maximum tokens per LLM response
- **Temperature** — sampling temperature

The LLM receives these in a layered order: the system prompt sets the persona, the orchestration prompt defines the conversational process, and the substituted template provides the specific context for this workflow run.

### Behavior

When the engine reaches an interactive node:

1. The LLM is called with the full prompt stack and produces its first message — the opening of the conversation
2. The workflow instance transitions to **waiting** and does not count against parallelism caps
3. The user is notified that the workflow needs their attention
4. The user and LLM exchange messages in a multi-turn conversation through the dashboard
5. When the conversation concludes, the LLM's structured output becomes the node's output for downstream nodes
6. The full conversation transcript is preserved as part of the node's execution record
7. The instance re-enters the queue and continues from the next node when capacity is available

### Conversation Sessions

Interactive sessions support both extended multi-turn conversations (like a brainstorming session with back-and-forth Q&A) and simple single-response interactions (like approving a generated document).

Each message exchange works the same way: the user sends a message, the LLM responds with its reply streamed token-by-token in real time (consistent with the experience users expect from modern chat interfaces), and both messages are persisted immediately.

If the user navigates away and returns, or if the server restarts, the conversation picks up exactly where it left off — no messages are lost.

### Session Completion

Either party can end the conversation:

- **LLM-initiated (preferred)** — the LLM concludes its process naturally and emits structured output conforming to the output schema. This is the happy path — the orchestration prompt guides the LLM to drive the conversation to completion.
- **User-initiated** — the user clicks "End Session" to bail out early. Whatever structured output exists at that point is captured (or an empty result if the LLM hasn't produced one yet).

The conversation transcript is available in the node's execution record regardless of how the session ended.

---

## 2. Dashboard Conversational Interface

### Side Panel Takeover

When a workflow instance is waiting on an interactive step, the execution view's side panel transforms into a chat interface:

- The normal Node Output and Variables tabs are replaced by the conversation view
- A horizontal callout connector line runs from the right edge of the interactive node to the panel border, visually linking the conversation to its node on the graph
- The panel is resizable via a draggable divider between the graph and panel
- The panel defaults to approximately 60% of the viewport width to provide comfortable room for conversation

### Panel Layout

Top to bottom:

- **Header** — "Interactive Session" label with the node name, and an "End Session" button
- **Message area** — scrollable conversation history. Messages are color-coded by role (assistant vs. user), each with a role label. Message content renders markdown. A typing indicator appears while the LLM is generating its response.
- **Input area** — multiline text input with markdown support and a Send button. Enter to send, Shift+Enter for newline.

### Navigation Behavior

- When the user navigates to the execution view of an instance waiting on an interactive step, the chat panel opens automatically with the full conversation history loaded. No extra click needed.
- When the session ends (by either party), the panel snaps back to the normal Node Output / Variables tabs immediately.
- After a session has ended, clicking the interactive node in the graph shows the completed conversation transcript in the normal Node Output tab (read-only), consistent with how other completed nodes display their output.

---

## 3. Notifications

### In-App Notifications

When an interactive node begins waiting for user participation, the system creates a `needs_interaction` notification. This uses the existing notification infrastructure built in Phase 3 — the notification appears in the bell popover and links to the instance's execution view. No changes to the notification UI or data model.

### External Notification Stub

A pluggable interface for delivering notifications to external systems (Teams, email, Slack, etc.). Phase 4 ships with a logging-only implementation — notification details are written to the system log, but no actual external delivery occurs. Future phases add real transport implementations behind the same interface.

The external notifier fires for all notification types — not just interactive steps. When the system creates any in-app notification (`needs_interaction`, `failed`, `completed`), the external notifier is called as well, giving future transports full coverage from the start.

---

## 4. Waiting Behavior

Interactive nodes use the same waiting state established in Phase 2 for timer delay and webhook wait nodes:

- The instance transitions to **waiting** when the interactive node begins its session
- **Does not count** against global or per-user parallelism caps
- **Waits indefinitely** — there is no timeout. Configurable timeouts for interactive steps are deferred to a future phase.

When the session ends, the instance re-enters the queue with its existing priority. Normal queue rules apply — capacity must be available before execution resumes.

### Operations on Waiting Interactive Instances

Same as other waiting instances:

- **Cancel** — terminates the instance and discards the session
- **Pause** — preserves the conversation. On resume, the user can continue where they left off.
- **Priority change** — takes effect when the instance eventually re-enters the queue

---

## 5. Scope Boundaries

### In Scope

- Interactive node type with system prompt, orchestration prompt file, template file, model, output schema, max tokens, and temperature
- Multi-turn conversational sessions between user and LLM
- Token-by-token streaming of LLM responses
- Conversation persistence across navigation and server restarts
- Session completion by LLM (structured output) or user ("End Session")
- Side panel takeover with callout connector to the interactive node
- Resizable panel with draggable divider
- Auto-open chat panel when navigating to a waiting interactive instance
- Markdown rendering in chat messages
- In-app notification on interactive step via existing notification system
- External notification interface with logging-only implementation
- Indefinite wait with no parallelism cap impact

### Not In Scope

- File attachments or image uploads in the conversation
- Quick action buttons or structured response options in the chat UI
- Tools available to the LLM during the interactive session (file read/write, code execution, etc.)
- Configurable timeout for interactive steps
- Rich media rendering beyond markdown
- External notification delivery (Teams, email, Slack) — only the interface and logging stub
- MCP-based participation in interactive steps (Phase 5)
