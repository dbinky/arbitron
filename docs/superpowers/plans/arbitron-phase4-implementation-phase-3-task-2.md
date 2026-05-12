# Phase 3, Task 2: Chat Panel Component & Side Panel Takeover

## Goal

Build the interactive chat panel component that replaces the normal side panel content when an interactive session is active. Implement the panel layout (header, message area, input), side panel takeover logic, auto-open behavior, and snap-back on session end.

## Prerequisites

- Task 1 complete (useInteractiveSession hook provides state and actions)

## Files to Create/Modify

### Create: `dashboard/src/components/InteractivePanel.tsx`

The main chat panel component. Renders when `interactiveActive` is true on the instance.

**Props:**
```typescript
interface InteractivePanelProps {
  instanceId: string
  onSessionEnded: () => void  // callback to trigger snap-back
}
```

**Internal structure:**
- Uses `useInteractiveSession(instanceId)` hook for all state and actions
- Three sections stacked vertically:
  1. **Header** — "Interactive Session" label, node name (from history event's `nodeId`), "End Session" button
  2. **Message area** — scrollable list of `ChatMessage` components + streaming indicator
  3. **Input area** — textarea + send button

**End Session button:**
- Calls `endSession()` from the hook
- Enters disabled/loading state while waiting for `interactive.ended`
- When `isActive` goes false (from the hook), calls `onSessionEnded()` prop

**Auto-scroll:**
- Message area scrolls to bottom when `messages` array changes or `streamingContent` updates
- Use a ref on the scroll container and `scrollIntoView` on a sentinel element at the bottom

### Create: `dashboard/src/components/ChatMessage.tsx`

A single message bubble.

**Props:**
```typescript
interface ChatMessageProps {
  role: 'assistant' | 'user'
  content: string
  isStreaming?: boolean  // if true, content is partial and still growing
}
```

**Rendering:**
- Role label: "ASSISTANT" (blue, `#5577cc`) or "YOU" (green, `#55aa77`) in small uppercase
- Left border: color-coded accent matching role
- Content: rendered via `react-markdown` for complete messages, plain text for streaming (markdown re-renders on every token would be expensive — render markdown only on complete messages)
- Streaming messages get a subtle pulsing opacity animation

### Create: `dashboard/src/components/ChatInput.tsx`

The input area.

**Props:**
```typescript
interface ChatInputProps {
  onSend: (content: string) => void
  disabled: boolean  // true while streaming
}
```

**Behavior:**
- Multiline textarea, auto-growing up to 4 lines
- Enter to send (calls `onSend` with content, clears input)
- Shift+Enter for newline
- Send button adjacent, also calls `onSend`
- Both textarea and button disabled when `disabled` prop is true
- Hint text below: "Markdown supported" left, "Enter to send · Shift+Enter for newline" right
- Auto-focus on mount

### Modify: Execution view side panel container

The execution view currently renders the side panel with Node Output and Variables tabs. Modify the container to conditionally render:

- If `interactiveActive` is true → render `<InteractivePanel>`
- Otherwise → render normal tabs (Node Output / Variables)

The `interactiveActive` flag comes from the instance detail response (already fetched by the execution view).

**Snap-back:** When `InteractivePanel` calls `onSessionEnded`, set `interactiveActive` to false in the parent component's state, which triggers the conditional render to switch back to normal tabs.

### Add dependency: `react-markdown`

Add `react-markdown` to `dashboard/package.json` for markdown rendering in chat messages.

## Testing

- Component test: `InteractivePanel` renders header, messages, and input when given a mock hook state
- Component test: `ChatMessage` renders markdown content correctly for assistant messages
- Component test: `ChatMessage` renders plain text for streaming content
- Component test: `ChatInput` sends on Enter, newline on Shift+Enter, disabled state works
- Integration test: verify snap-back when `onSessionEnded` fires

## Acceptance Criteria

- Chat panel renders correctly with header, scrollable messages, and input area
- Messages display role labels, color-coded borders, and markdown content
- Streaming content displays in real time without expensive re-renders
- Input supports Enter/Shift+Enter behavior and disables during streaming
- Panel auto-opens when navigating to an interactive wait instance
- Panel snaps back to normal tabs immediately when session ends
