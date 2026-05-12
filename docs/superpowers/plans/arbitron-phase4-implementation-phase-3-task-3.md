# Phase 3, Task 3: Callout Connector & Panel Resizing

## Goal

Implement the visual callout connector line from the interactive node to the panel border, adjust panel sizing defaults for the interactive state, and handle completed session viewing in the normal Node Output tab.

## Prerequisites

- Task 2 complete (InteractivePanel renders in the side panel)

## Files to Create/Modify

### Create: `dashboard/src/components/CalloutConnector.tsx`

An SVG overlay that draws the connector line from the interactive node to the panel.

**Props:**
```typescript
interface CalloutConnectorProps {
  nodeId: string       // the interactive node to connect from
  visible: boolean     // only render when interactive session is active
}
```

**Implementation:**
- Get the interactive node's position and dimensions from the graph layout (XYFlow provides node positions via `useNodes()` or similar)
- Calculate the right edge midpoint of the node (source anchor)
- Calculate the left edge of the panel at the same vertical position (target anchor)
- Render an SVG with:
  - A filled circle (r=4) at the source anchor, color `var(--state-waiting)`
  - A horizontal line from source to target, stroke `var(--state-waiting)`, stroke-width 2, slight opacity (0.7)
  - An arrowhead (triangle) at the target anchor pointing right
- Position the SVG as an absolute overlay on top of the graph area (pointer-events: none)
- Update positions if the graph is scrolled or nodes are repositioned (listen to XYFlow viewport changes)

### Modify: Execution view panel divider logic

The existing draggable divider controls the graph/panel split ratio. Modify it to:

- When `interactiveActive` transitions to `true`: save the current split ratio, then animate/set to 40/60 (graph/panel)
- When `interactiveActive` transitions to `false` (snap-back): restore the saved split ratio
- The divider remains draggable during the interactive session (user can resize)

### Modify: `dashboard/src/graph/nodes/InteractiveCallout.tsx`

Replace the Phase 3 placeholder component. This component is rendered by the graph visualization when the node type is `interactive`:

- Remove the placeholder text "Interactive session — available in a future release."
- When the node is in `waiting` state: render with the orange pulsing border (existing CSS) but no content inside (the conversation is in the side panel, not in the node)
- When the node is completed: render with green border (standard completed styling)
- The comic-book callout CSS (triangular tail) can be removed since the connector line replaces it, OR kept as a subtle visual embellishment pointing toward the panel — match whatever looks better with the connector

### Modify: Node Output tab for interactive nodes

When a user clicks a completed interactive node in the graph, the Node Output tab should display the conversation transcript. The existing Node Output tab renders `llmConversation` for LLM nodes — extend this to also handle interactive nodes:

- Check node type from the execution trace
- If `interactive`: render the `llmConversation` field as a read-only conversation view (reuse `ChatMessage` component in a read-only mode)
- Display the structured output below the conversation (same as how regular LLM node output is shown)

## Testing

- Component test: `CalloutConnector` renders SVG with correct source/target positions
- Component test: Connector visibility toggles with `visible` prop
- Visual test: verify the connector line aligns with the node's right edge and the panel's left edge
- Test: panel split ratio changes to 40/60 on interactive activation and restores on snap-back
- Test: clicking a completed interactive node shows conversation transcript in Node Output tab

## Acceptance Criteria

- Orange connector line visually links the interactive node to the panel border
- Connector has a dot at the node end and arrowhead at the panel end
- Connector position updates if the graph is scrolled
- Connector disappears on snap-back
- Panel defaults to 60% width during interactive sessions
- Panel restores previous width on snap-back
- Completed interactive nodes show their conversation transcript in the Node Output tab
- The old placeholder text is removed
