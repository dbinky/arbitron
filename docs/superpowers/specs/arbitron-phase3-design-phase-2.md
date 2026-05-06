# Arbitron Phase 3 — Design Phase 2: Workflow Graph & Execution View

## Overview

Phase 2 builds the execution view — the primary monitoring surface for a running workflow instance. It renders the workflow graph with live node state updates, provides a resizable side panel for viewing node output and variables, and wires up instance control actions.

---

## React Flow Integration

React Flow renders the workflow graph. The workflow definition (from the instance's `workflowSnapshot`) provides the node and edge data. Each Arbitron node type maps to a custom React Flow node component.

### Custom Node Components

One custom React Flow node component per Arbitron node type:

- **LLM Call** — indigo accent
- **Script** — orange accent
- **Tool Call** — teal accent
- **Conditional** — yellow accent
- **Transform** — purple accent
- **Timer Delay** — cyan accent
- **Webhook Wait** — pink accent

Each component renders:
- Node type label (uppercase, small, accent colored)
- Node ID/name (primary text)
- Execution state indicator (text + icon)
- State-dependent border and styling (see below)

### Node State Visualization

Each node component accepts an execution state prop:

- **Completed** — green border, checkmark icon, green accent text
- **Running** — blue border, subtle CSS glow animation, blue accent text
- **Pending** — gray border, muted text throughout
- **Failed** — red border, red accent text
- **Waiting** — orange border, orange accent text

### Edges

Standard React Flow edges with arrowhead markers. Conditional node outputs use labeled edges (the label comes from the edge's `output` field in the workflow definition, e.g., "pass", "fail"). Edge styling is neutral — thin, muted color.

---

## Dagre Auto-Layout

On load, the execution view reads node positions from the instance's workflow snapshot.

**All-or-nothing rule:** If every node in the workflow has a `position` field with `x` and `y` values, those positions are used directly. If any node is missing position data, dagre computes a full top-to-bottom layout for the entire graph and those computed positions are used for display.

In the execution view, computed positions are not saved back to the workflow definition — this is a read-only view of a running instance's snapshot. Position persistence only happens in the review panel (Phase 3).

Dagre configuration:
- `rankdir: 'TB'` (top to bottom)
- Reasonable node spacing (`nodesep`, `ranksep`) to prevent overlap and produce a clean layout
- Edges route automatically

---

## Execution View Page

Route: `/instances/:id`.

### Data Loading

1. Fetch instance: `GET /api/v1/instances/:id` — provides state, workflow name, current node, priority, variables
2. The instance's `workflowSnapshot` provides the graph structure (nodes, edges)
3. Fetch execution trace: `GET /api/v1/instances/:id/trace` — provides per-node execution records (status, output, timing)
4. Connect to per-instance WebSocket: `GET /api/v1/instances/:id/ws` — streams live events

### Breadcrumb

Below the nav bar: **Queue** > **workflow-name** > **instance-short-id**. Each segment is a clickable link. Inline after the instance ID: state badge and priority indicator.

### Layout

The page body is a horizontal split: graph area on the left, side panel on the right. A draggable vertical divider controls the split ratio.

---

## Clickable Nodes

Clicking a node in the graph:
- Sets it as "selected" with a highlighted border
- Loads its execution data into the side panel's Node Output tab
- Fetches from `GET /api/v1/instances/:id/trace/:nodeId` if not already loaded

If the selected node is currently running, its output streams in real time via WebSocket events.

---

## Resizable Side Panel

A panel on the right side of the graph. The divider between the graph area and the panel is draggable to resize the width split. Minimum and maximum widths prevent either side from collapsing entirely.

### Tabs

Two tabs at the top of the panel: **Node Output** and **Variables**.

### Node Output Tab

Displays execution details for the currently selected node.

**Script nodes:**
- Monospace output area showing stdout (and stderr if present)
- For running nodes: output streams in real time as WebSocket events arrive with new output chunks
- Exit code displayed on completion

**LLM nodes:**
- Conversation display: system prompt, user messages, assistant responses in a chat-like format
- For running nodes: the assistant response streams in real time
- Model, token counts (in/out) displayed on completion

**Transform/Conditional nodes:**
- The evaluation result: which condition matched (conditional), which operations ran (transform), and the resulting output JSON

**All node types:**
- Resolved inputs (the template/args after variable substitution)
- Timing: start time, elapsed or completed duration
- Error details if failed (error message, stack trace, exit code)

The output area within the tab has a draggable horizontal divider so the user can resize it vertically to show more or fewer lines of output.

### Variables Tab

- Key-value table of current instance variables
- Fetched from `GET /api/v1/instances/:id/variables`
- Updates live as WebSocket events arrive with variable changes (the frontend listens for variable-related events and refetches or patches the display)

---

## Instance Controls

A toolbar in the breadcrumb bar area with action buttons. Buttons are conditionally visible based on the instance's current state:

- **Pause** — visible when running. Calls `POST /api/v1/instances/:id/pause`
- **Resume** — visible when paused. Calls `POST /api/v1/instances/:id/resume`
- **Cancel** — visible when running, paused, or queued. Calls `POST /api/v1/instances/:id/cancel`
- **Retry** — visible when failed. Calls `POST /api/v1/instances/:id/retry`
- **Re-run** — visible when completed, failed, or cancelled. Calls `POST /api/v1/instances/:id/rerun`

On success, the UI updates via the WebSocket event stream (no manual refetch needed).

---

## Backend Changes

### Instance Response Enhancement

The `GET /api/v1/instances/:id` response needs to include the `workflowSnapshot` field so the frontend can render the graph. Currently `toInstanceResponse` omits it. Add a `workflowSnapshot` field to the `instanceResponse` struct and populate it.

### Streaming Node Output

The existing per-instance WebSocket already streams events including `node.output` chunks. The frontend consumes these for real-time output display. No new backend endpoints needed for streaming — the existing event bus and WebSocket handler cover this.

---

## Tests

### Frontend

- React Flow graph renders correct number of nodes and edges from a workflow snapshot
- Custom node components render correct accent colors per type
- Node state visualization: correct styling per state (completed/running/pending/failed/waiting)
- Dagre layout: computes positions when nodes lack position data, uses existing positions when all present, re-layouts when any missing
- Side panel tabs: switching between Node Output and Variables
- Resizable dividers: drag interaction updates panel sizes
- Instance controls: correct buttons shown per state, API calls on click

### Backend

- Instance response includes workflowSnapshot
