# Arbitron Phase 3 — Design Phase 3: Workflow Catalog, Review Panel & Settings

## Overview

Phase 3 adds the workflow catalog listing, the review panel for inspecting workflow configuration and usage, and the settings page. It also introduces node position persistence and usage statistics — both requiring new backend endpoints and a domain model change.

---

## Workflows Catalog Page

Route: `/workflows`.

### Data Loading

Fetches `GET /api/v1/workflows` — returns an array of workflow summaries (name, description, author, node count). The `WorkflowSummary` struct is extended to include `NodeCount` so the catalog can display it without fetching full definitions.

### Display

A list/table with columns: workflow name, description, author, node count. Searchable by name via a text input that filters client-side (workflow count is bounded by local filesystem, not a scalability concern).

Clicking a row navigates to `/workflows/:name`.

---

## Workflow Review Panel

Route: `/workflows/:name`.

### Data Loading

Fetches `GET /api/v1/workflows/:name` — returns the full workflow definition including nodes, edges, input schema, and node positions (if any).

### Graph Visualization

Same React Flow rendering as the execution view but with different styling:
- No execution state coloring — all nodes use a neutral dark background with node-type accent colors
- Nodes are draggable (React Flow's built-in drag behavior is enabled)

### Position Persistence

**All-or-nothing rule:** If every node has a `position` field, those positions are used. If any node is missing position data, dagre computes a full top-to-bottom layout for the entire graph.

**Auto-save on first view:** When dagre computes positions (because some or all were missing), the frontend immediately saves all positions via the layout endpoint. This means the first time anyone views a workflow, it gets a persisted layout automatically.

**Save on drag:** When the user finishes dragging a node, the frontend sends all current node positions to the backend.

**API endpoint:** `PUT /api/v1/workflows/:name/layout`

Request body:
```json
{
  "positions": {
    "fetch-data": {"x": 120, "y": 0},
    "validate-input": {"x": 120, "y": 200},
    ...
  }
}
```

The backend reads the workflow.json file, updates each node's `position` field, and writes it back to disk.

### Clickable Nodes

Clicking a node in the graph shows its configuration in the side panel's Node Details section. The configuration varies by node type:
- **LLM Call:** template file, model, max tokens, temperature, output schema
- **Script:** script file, interpreter, timeout
- **Tool Call:** command, args, timeout
- **Conditional:** conditions array (field, operator, value, output), default output
- **Transform:** operations array (variable, operation, value)
- **Timer Delay:** duration seconds
- **Webhook Wait:** secret (redacted), wait timeout

### Workflow Metadata

Displayed above the graph: workflow name, description, author, node count, default error behavior (type, retries, retry delay).

### Trigger Button

A button in the metadata bar that opens a modal/form for manual triggering:
- Input fields generated from the workflow's input schema (respecting types, required/optional, defaults)
- Priority selector (high, normal, low)
- Submit calls `POST /api/v1/workflows/:name/trigger`
- On success: navigates to the new instance's execution view, or shows a confirmation with a link

### Side Panel

The review panel's side panel contains three sections (not tabs — all visible, scrollable):

**Usage Statistics**

Date range picker at the top with preset options:
- Today
- Yesterday
- Last 7 days
- Last 30 days (default)
- Custom date range (two date pickers)

Stats displayed:
- Total runs (count)
- Success rate (percentage, green)
- Failure rate (percentage, red)
- Average duration (formatted time)

Data fetched from: `GET /api/v1/workflows/:name/stats?from=DATE&to=DATE`

**Input Schema**

Lists all fields from the workflow's `inputs` array. Each field shows:
- Field name (accent colored)
- Type
- Required/optional indicator
- Default value (if any)

**Node Details**

Populated when a node is clicked in the graph. Shows the full configuration for the selected node type (see Clickable Nodes above). Empty state: "Click a node in the graph to see its configuration."

### Live-Update Hooks

The review panel establishes a subscription mechanism (e.g., a callback or event listener) that can trigger a refetch of the workflow definition. In Phase 3, nothing triggers it — the workflow loads once on page open. When MCP arrives in Phase 5, it pushes "workflow changed" events through the system-wide WebSocket, and the review panel's listener triggers a refetch.

---

## Settings Page

Route: `/settings`.

### Data Loading

Fetches `GET /api/v1/system/config` on page load.

### Display

**Server Configuration** (read-only):
- Host
- Port

**Queue Configuration** (editable):
- Global parallelism cap — number input
- Per-user parallelism cap — number input
- Save button calls `PUT /api/v1/system/config/queue`

**Queue Status** (live-updating):
- Running instance count
- Queued instance count
- Waiting instance count

Queue status updates via the system-wide WebSocket — the frontend listens for state change events and refetches counts, or the backend publishes periodic queue status events.

---

## Backend Changes

### Domain Model Change

Add `Position` field to the `Node` struct in `domain/workflow/workflow.go`:

```go
type NodePosition struct {
    X float64 `json:"x"`
    Y float64 `json:"y"`
}
```

Add to `Node`:
```go
Position *NodePosition `json:"position,omitempty"`
```

This field is optional — workflows without persisted positions omit it.

### Workflow Repository Port Change

Add to `WorkflowRepository` in `domain/workflow/ports.go`:

```go
SaveNodePositions(name string, positions map[string]NodePosition) error
```

### Filesystem Adapter Change

Implement `SaveNodePositions` in `adapter/filesystem/workflow_repo.go`:
1. Read the raw workflow.json from disk
2. Unmarshal into a generic `map[string]any` (to preserve unknown fields)
3. Walk the nodes array, match by node ID, set the `position` field
4. Marshal back and write to disk

Using generic map manipulation rather than the typed `Workflow` struct avoids dropping any fields the struct doesn't model.

### New HTTP Endpoints

**`PUT /api/v1/workflows/:name/layout`**

New handler in `adapter/http/workflow_handler.go`:
- Parses request body: `{"positions": {"node-id": {"x": 0, "y": 0}, ...}}`
- Calls `workflowRepo.SaveNodePositions(name, positions)`
- Returns 200 on success, 404 if workflow not found

**`GET /api/v1/workflows/:name/stats?from=DATE&to=DATE`**

New handler in `adapter/http/workflow_handler.go`:
- Parses `from` and `to` query parameters (ISO 8601 timestamps)
- Defaults: `from` = 30 days ago, `to` = now
- Queries the instances table for instances matching the workflow name within the date range
- Aggregates: total count, completed count, failed count, average duration (completed_at - created_at for completed instances)
- Returns JSON: `{"totalRuns": 47, "successCount": 42, "failureCount": 5, "avgDurationSeconds": 263}`

### Instance Repository Port Change

Add to `InstanceRepository` in `domain/instance/ports.go`:

```go
Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*WorkflowStats, error)
```

New type:
```go
type WorkflowStats struct {
    TotalRuns        int
    SuccessCount     int
    FailureCount     int
    AvgDurationSecs  float64
}
```

### Workflow Summary Enhancement

Extend `WorkflowSummary` to include `NodeCount int` so the catalog page can display it without fetching full definitions.

---

## Tests

### Frontend

- Workflow catalog: renders list, search filters correctly, click navigates
- Review panel graph: renders nodes with type-specific colors, drag updates positions
- Position auto-save: dagre layout triggers save on first view when positions missing
- Usage statistics: date range picker changes trigger refetch, stats display correctly
- Input schema: renders fields with correct types and required/optional indicators
- Trigger modal: generates inputs from schema, submits correctly
- Settings page: displays config, edits queue caps, shows live counts

### Backend

- `PUT /api/v1/workflows/:name/layout`: positions written to workflow.json, round-trips correctly
- `GET /api/v1/workflows/:name/stats`: correct aggregation, date range filtering, handles zero instances
- `SaveNodePositions`: preserves all existing workflow.json fields, updates only positions
- `Stats` query: correct counts and averages across various instance states and date ranges
