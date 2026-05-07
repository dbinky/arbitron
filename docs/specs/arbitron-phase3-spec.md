# Arbitron Phase 3 — Dashboard Product Specification

## Overview

The dashboard is a web application that provides visual monitoring and interaction for the Arbitron. It is optional — everything the dashboard can do, the REST API can also do. The dashboard is a client of the REST API.

Desktop-only. Dark mode only. All pages have deep-linkable URLs. Live-updating across the app via WebSocket — no manual refresh needed.

---

## Navigation

A persistent top navigation bar appears on every page:

- **Arbitron** logo/wordmark (links to home)
- **Queue** — the home page
- **Workflows** — workflow catalog
- **Settings** — system configuration
- **Notification Bell** — upper right, with unread badge

Breadcrumb navigation below the nav bar on detail pages (e.g., Queue > data-pipeline > a1b2c3d4) provides deep-linking context and clickable navigation back up the hierarchy.

---

## Queue View (Home)

The primary dashboard surface. Three collapsible sections, in this order:

### Active Instances

Shows all instances in running, waiting, queued, or paused states. Each row displays:

- Workflow name
- Current state (color-coded badge)
- Current node ID (or "—" if queued/not yet started)
- Priority (high, normal, low)
- Elapsed time (live-updating)
- Owner ("local" in single-user mode)

Instances needing attention are flagged with a visual alert indicator on their row, regardless of which section they appear in. Two conditions trigger the flag:

1. An interactive step is waiting for user participation
2. The instance has failed

The section has independent filter and sort controls.

### Failed Instances

Shows instances in the failed state from the last 5 days. Same row format as active instances, but the time column shows how long ago the instance failed (e.g., "2h ago", "1d ago"). Independently collapsible, sortable, and filterable.

### Completed Instances

Shows instances in the completed state from the last 5 days. Same row format, but the time column shows how long ago the instance completed. Independently collapsible, sortable, and filterable.

### Behavior

- All three sections live-update in real time — instances move between sections as their state changes
- Clicking any instance row navigates to the Workflow Instance Execution View for that instance
- Each section shows a count badge next to its heading

---

## Workflow Instance Execution View

Reached by clicking an instance from the Queue View, or via direct URL. Shows the workflow graph with live execution state.

### Graph Visualization

- Top-to-bottom layout (entry node at top, flow downward)
- Auto-layout engine produces clean, readable graphs by default
- Vertical scrolling for tall workflows; horizontal scroll available but not the default
- Nodes display: node type label, node ID/name, current execution state
- Node state coloring:
  - **Completed** — green border/accent
  - **Running** — blue border/accent with subtle glow
  - **Pending** — gray border, muted
  - **Failed** — red border/accent
  - **Waiting** — orange border/accent
- Conditional nodes show labeled branch outputs on their outgoing edges
- Live-updating — nodes change state in real time as execution progresses

### Clickable Nodes

Clicking any node in the graph loads its execution details into the side panel. The selected node is visually highlighted.

### Side Panel

A resizable panel on the right side of the graph. The divider between the graph and the side panel is draggable to adjust the split.

Two tabs:

**Node Output Tab**
- Shows the execution output for the currently selected node
- Content varies by node type:
  - LLM nodes: the conversation as it unfolds in real time (streaming)
  - Script nodes: stdout/stderr streaming in real time
  - Transform/conditional nodes: the evaluation and result
- The output area within the tab is also resizable (draggable divider) to show more or fewer lines
- Timing information: start time, elapsed/completed time
- For completed nodes: full output, resolved inputs, error details if failed

**Variables Tab**
- Shows the current state of all instance variables, updated live as execution progresses
- Key-value display of the variable store

### Instance Controls

Pause, resume, cancel, retry, and re-run controls are accessible from this view, appropriate to the instance's current state.

### Interactive Step Callout

When a workflow reaches an interactive node, a comic-book-style callout box appears pointing at that node in the graph. The callout contains an embedded terminal/conversational interface. In Phase 3, the terminal within the callout is a placeholder — the full interactive experience is wired up in Phase 4. The callout's visual design and positioning on the graph node are built in this phase.

### Breadcrumb

Shows: Queue > workflow-name > instance-id, with state badge and priority indicator. Each segment is a clickable deep link.

---

## Workflow Review Panel

Reached from the Workflows catalog page. Shows how a workflow is configured.

### Graph Visualization

- Same top-to-bottom auto-layout as the Execution View, but without execution state coloring (all nodes use a neutral style with node-type accent colors)
- Nodes are draggable for visual layout adjustment — this is visual preference only and does not change the workflow logic
- Drag positions persist in the workflow definition (workflow.json) so the layout is consistent everywhere the workflow is viewed, including after copying or sharing
- Each node type has a distinct accent color for quick visual identification

### Clickable Nodes

Clicking a node in the graph shows its configuration details in the side panel: template file, model selection (for LLM nodes), timeout, error handling overrides, conditions (for conditional nodes), operations (for transform nodes).

### Workflow Metadata

Displayed above the graph: workflow name, description, author, node count, default error behavior.

### Trigger Button

A manual trigger button is available directly from the review panel, allowing users to start a new instance of the workflow without navigating elsewhere.

### Side Panel

Contains:

**Usage Statistics**
- Date range picker with preset options: today, yesterday, last 7 days, last 30 days
- Custom date range selection for arbitrary windows
- Default view: last 30 days
- Stats displayed: total runs, success rate, failure rate, average execution time

**Input Schema**
- Lists all input fields defined by the workflow
- Each field shows: name, type, required/optional, default value if any

**Node Details**
- Populated when a node is clicked in the graph
- Shows the full configuration for the selected node

### Live-Updating

The review panel loads the workflow definition on page open. Refresh hooks are built into the infrastructure so that when MCP-based editing arrives in Phase 5, changes to the workflow appear instantly without additional plumbing.

---

## Workflows Catalog

Reached from "Workflows" in the nav bar. A listing of all workflows available in the workspace.

- Each entry shows: workflow name, description, author, node count
- Clicking a workflow navigates to its Review Panel
- Searchable and filterable by name

---

## Settings Page

Reached from "Settings" in the nav bar. Displays and allows editing of system configuration.

- Server host and port (display only)
- Queue caps: global and per-user parallelism caps (editable, takes effect immediately)
- Current queue counts: running, queued, waiting (live-updating)
- Future phases extend this page with LLM backend configuration, notification transport settings, and user management

---

## Notification Bell

Located in the nav bar, upper right corner.

### Bell Icon

- Outline-style bell icon
- Badge showing unread notification count; badge hidden when count is zero

### Popover

- Clicking the bell opens a scrollable popover
- Notifications listed newest first
- All notifications are instance-scoped — each links to a specific workflow instance

### Notification Types

- Workflow needs interaction (interactive step waiting)
- Workflow failed
- Workflow completed

### Read/Unread State

- Unread notifications have a red dot indicator
- Opening the popover marks all visible notifications as read (badge clears, dots disappear)

### Dismissing Notifications

- Each notification has an [x] button to dismiss it
- Dismissed notifications are not gone permanently
- At the bottom of the active notification list: a "Show dismissed" link
- Clicking "Show dismissed" lazy-loads the next 20 dismissed notifications
- Below those: a "Load more" link to page through dismissed notifications in batches of 20 (manual paging, not infinite scroll)

### Cross-Tab Sync

- Notification state (read/dismissed) syncs across all open browser tabs
- Dismissing or reading in one tab reflects immediately in all others

### Navigation

- Clicking a notification navigates to the Execution View for that instance
