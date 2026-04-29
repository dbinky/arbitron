# The Arbitron — Product Specification

## 1. Product Overview & Core Concepts

**The Arbitron** is a workflow orchestration platform where workflows are directed graphs of steps executed by LLM calls, scripts, and tools. It generalizes the pattern established by ralph-o-matic — iterative LLM-driven loops controlled by structured output — into an arbitrarily configurable system where any sequence of automated and interactive steps can be composed, executed, monitored, and shared across users.

The Arbitron ultimately replaces both ralph-o-matic (the refinement engine) and the dbinky-skill-set feature pipeline. The entire feature pipeline — brainstorm, alignment, planning, implementation, refinement loop, PR review — should be expressible as a single Arbitron workflow.

### Core Concepts

- **Workflow** — a reusable definition stored as a self-contained folder: one JSON manifest describing a directed graph of nodes and edges, plus all accompanying files (markdown templates, scripts, etc.). Workflows are authored via MCP tools from Claude Code, not through a UI.

- **Workflow Instance** — a single execution of a workflow. Has its own variable state, execution trace, and lifecycle (queued, running, waiting, completed, failed, cancelled, paused). Instances snapshot the workflow definition at creation time — later edits to the workflow don't affect running instances.

- **Node** — a step in the workflow. Nine types across three categories:

  **Execution Nodes** (do work, produce output):
  1. **LLM Call** — sends a markdown template (with `{{variable}}` substitution) to an LLM and collects structured JSON output. Model is selectable per node.
  2. **Script** — executes a script (TS, JS, Python, shell, PowerShell, etc.) and collects structured JSON output from stdout.
  3. **Tool Call** — invokes a CLI tool or API and collects structured JSON output.
  4. **Interactive** — an LLM call that requires human participation via an embedded conversational interface. The workflow pauses and notifies the user.

  **Flow Control Nodes** (route and shape execution):
  5. **Conditional (If)** — evaluates conditions against the current instance state (including upstream node output) and routes to one of multiple outputs. One input, two or more outputs based on configurable conditions. Includes a default/fallback output.
  6. **Transform** — lightweight variable manipulation (increment, concatenate, set, conditional assign) with no external execution environment.
  7. **Fan-out / Fan-in** — fan-out splits execution into parallel branches; fan-in waits for all parallel branches to complete and collects their outputs as an ordered array.

  **Wait Nodes** (pause execution):
  8. **Timer Delay** — pauses execution for a configured duration.
  9. **Webhook Wait** — pauses execution until a specific webhook endpoint is hit by an external system.

- **Edge** — a simple, unconditional connection between two nodes. Edges carry no logic — they are just wires that define the graph topology. All routing decisions, branching, and conditional logic happen inside nodes.

- **Instance Variables** — a key-value store that lives for the duration of a workflow instance. Initialized from the workflow's input schema when triggered. Nodes can read variables (via `{{substitution}}` in templates, or as input to scripts) and write variables (via their structured JSON output). Transform nodes exist specifically for lightweight variable manipulation between heavier steps.

- **Structured JSON Output** — every execution node emits arbitrary JSON when it completes. This output is stored in the instance state and available to downstream nodes (conditional nodes can evaluate it, templates can reference it via substitution). There is no enforced schema — each node defines its own output shape.

---

## 2. Workflow Definition & Storage

### Folder Structure

Each workflow is a self-contained folder. Everything needed to execute the workflow lives within it.

```
my-workflow/
  workflow.json          # The manifest — graph definition, metadata, input schema
  steps/
    brainstorm.md        # Markdown template for an LLM node
    validate.sh          # Script for a script node
    review-prompt.md     # Another markdown template
  config/
    edge-conditions.json # Or whatever organization the author prefers
```

The author (or the AI building the workflow) can organize files however they want — flat, in subfolders, by type, by phase. The JSON manifest references files via relative paths within the folder.

### The JSON Manifest (workflow.json)

Contains:
- **Metadata** — workflow name (unique within its scope), description, author
- **Input Schema** — declares the variables the workflow expects when triggered (names, types, descriptions, defaults, required/optional). These become the initial instance variables.
- **Nodes** — each node has an ID, type, configuration (file references, model selection for LLM nodes, timeout, error handling overrides), and display hints (label, description)
- **Edges** — each edge has a source node, target node, and optionally a label. Edges are unconditional connections — just wires. Conditional nodes handle all routing by having multiple named outputs, each connected to a different downstream node via a simple edge.
- **Trigger Configuration** — how the workflow can be started: manual (always available), plus optional scheduled (cron expression) and webhook (generates a unique endpoint) triggers
- **Default Error Behavior** — workflow-level default for node failure handling (halt and notify), which individual nodes can override

### Workspace Locations

- **Private workspace** — each user has a directory where their workflow folders live. Only they can see and edit these.
- **Shared directory** — a common directory where published workflows live. Read-only. Publishing copies the folder from private to shared.

### Publishing Rules

- Workflow names are unique in the shared directory
- Only the original publisher can overwrite their shared workflow by re-publishing
- Re-publishing replaces the shared copy but does not affect instances already queued or running
- Any user can copy a shared workflow to their private workspace, modify it, and publish under a new unique name
- Nobody can edit a shared workflow in place — the original publisher edits their private copy and re-publishes; other users copy to their own workspace first

### Future .arb Packaging (Out of Scope)

The folder structure is designed so that in the future, a workflow folder could be zipped and renamed as a `.arb` file for portable distribution outside the built-in sharing system.

---

## 3. Execution Engine

### Instance Lifecycle

A workflow instance progresses through these states:
- **Queued** — waiting for capacity (counts against no limits)
- **Running** — actively executing nodes (counts against global and per-user parallelism caps)
- **Waiting** — paused on an interactive step, webhook wait, or timer delay (does NOT count against parallelism caps)
- **Completed** — all nodes finished successfully, terminal state
- **Failed** — a node failed and the error behavior is set to halt, terminal state (until retried)
- **Cancelled** — manually stopped by user or admin, terminal state
- **Paused** — manually paused by user or admin (does NOT count against parallelism caps)

### Graph Execution

The engine walks the directed graph starting from a designated entry node. For each node:
1. Resolve inputs — perform `{{variable}}` substitution on templates, pass current instance variables to scripts
2. Execute the node according to its type
3. Capture the structured JSON output
4. Store the output in the instance state
5. Follow the outgoing edge(s) to the next node(s)

For flow control nodes:
- **Conditional (If)** — evaluates its conditions against instance state, activates the matching output
- **Transform** — performs variable manipulation, passes through to its single output
- **Fan-out** — activates all outgoing branches simultaneously
- **Fan-in** — waits until all incoming branches have completed, collects their outputs as an ordered array, then continues

### Parallel Execution (Fan-out / Fan-in)

When a fan-out node activates, all downstream branches execute simultaneously. Each branch operates on its own copy of the instance variables at the point of fan-out. When branches converge at a fan-in node, the fan-in receives an array of outputs (one per branch, preserving order). Instance variables are NOT automatically merged from branches — the fan-in output is the array, and a downstream transform or script node can reconcile variables as needed.

### Error Handling

- **Default behavior** — node fails, workflow halts at that node, instance moves to Failed state, user is notified (in-app + external notification stub)
- **Per-node overrides** — the workflow author can configure:
  - Retry N times before failing
  - Skip and continue with a default output
  - Halt (same as default)
  - Route to a specific error-handling node (the node has a designated "error" output edge in addition to its "success" output edge)

### Instance Operations

- **Pause** — user or admin manually pauses a running instance. Completes the current node, then stops.
- **Resume** — continues a paused instance from where it left off.
- **Cancel** — stops execution. Current node is terminated.
- **Re-run** — creates a new instance from the same workflow definition and inputs, starting from step 1. Available on completed, failed, or cancelled instances.
- **Retry** — only available on failed instances. User can inspect and edit instance variables, then execution resumes at the failed node.

### LLM Backend

- **Local mode** — uses the local Claude Code installation with existing authentication (Anthropic account or API key)
- **Org/shared mode** — uses LiteLLM as a proxy for resilience across suppliers (Bedrock, Azure, direct Anthropic), configured at the system level
- Each LLM node specifies which model to use (Opus, Sonnet, Haiku, etc.). The backend determines how that model is reached; the workflow definition is backend-agnostic.

---

## 4. Queue & Concurrency

### Parallelism Caps

- **Global cap** — configurable maximum number of simultaneously running workflow instances across the entire system
- **Per-user cap** — configurable maximum number of simultaneously running workflow instances per user
- Both caps only count instances in the **Running** state. Instances in Waiting, Paused, or Queued states do not count.

### Priority

- Each workflow instance is assigned a priority at creation time (not defined in the workflow itself)
- Priority levels: High, Normal, Low (or a similar tiered scheme)
- When capacity opens up, the highest-priority queued instance starts next
- Within the same priority level, instances are processed first-in-first-out

### Queue Behavior

- When a user or trigger starts a workflow and capacity is available, it begins immediately
- When capacity is full, the instance enters the Queued state and waits
- When a running instance completes, fails, cancels, or transitions to a Waiting state, the queue checks for the next eligible instance
- An instance is eligible if both the global cap and the per-user cap have room

---

## 5. Multi-User Model & Authentication

### User Identity

- The system always operates with a user identity — there is no anonymous mode
- **Local mode** — a default user is automatically created and every request is auto-authenticated as that user. No login flow, no friction. All data models (ownership, workspaces, permissions) work identically to org mode, just with a single user.
- **Org/shared mode** — user identity is resolved via OAuth (Cloudflare Zero Trust or Entra, to be decided later). Users authenticate through the standard OAuth flow.

### Roles

- **User** — can create, edit, and delete their own workflows. Can trigger workflows, manage their own instances, view their own execution traces. Can publish to and copy from the shared directory.
- **Admin** — everything a user can do, plus: view all users' workflow instances and execution traces, intervene in any user's instances (cancel, pause, resume, respond to interactive steps), configure system-level settings (parallelism caps, LLM backend configuration, notification settings).

### Workspaces

- Each user has a private workspace where their workflow folders live. Only they (and admins) can see them.
- The shared directory is visible to all users. Workflows there are read-only.
- Publishing copies a workflow folder from private to shared. Only the original publisher can overwrite. Any user can copy a shared workflow into their own workspace and modify it. Re-sharing requires a unique name.

### Visibility Rules

- Users see only their own workflow instances, execution traces, and notifications
- Admins see everything across all users
- The shared workflow directory is visible to all authenticated users

---

## 6. Dashboard

### Overview

The dashboard is a web application that provides visual monitoring and interaction for the Arbitron. It is optional — everything the dashboard can do, the MCP tools and REST API can also do. The dashboard is a client of the REST API.

### Queue View (Home)

- Shows all workflow instances the user has access to (own instances for users, all instances for admins)
- Instances grouped or filterable by state: Running, Waiting, Queued, Paused, Completed, Failed, Cancelled
- Each instance shows: workflow name, current state, current node, priority, elapsed time, owner
- Instances needing attention (interactive steps, failures) are visually flagged with an alert indicator
- Live-updating — states change in real time without manual refresh

### Workflow Instance Execution View

- Shows the workflow graph as a visual diagram with nodes and edges
- Nodes are highlighted by state: pending, active/running, completed, failed, waiting
- Live-updating — nodes light up as execution progresses
- Any node can be clicked to open its live output stream:
  - LLM nodes show the conversation as it unfolds in real time
  - Script nodes show stdout/stderr in real time
  - Transform/conditional nodes show the evaluation and result
- Instance variables panel shows the current state of all variables, updated live as execution progresses
- For interactive steps: a comic-book-style callout box points at the interactive node, containing an embedded terminal/conversational interface where the user participates. When the conversation completes, the callout closes and the workflow continues automatically.
- Full execution history is available: logs per node, structured JSON output, variable state at each step, timing data, error details

### Workflow Review Panel

- Shows how a workflow is configured: the graph structure, node types, edge connections, input schema, trigger configuration
- Live-updating — if the workflow is being edited via MCP tools, changes appear instantly
- Nodes can be dragged for better visual layout (visual preference only, does not change the workflow definition). The auto-layout engine should produce clean, readable graphs by default.
- Usage statistics displayed live: run count, success/failure rates, average execution time

### Notification Bell

- Located in the upper right corner
- Badge shows count of unread notifications
- Clicking opens a scrollable popover, newest on top
- Notification types: workflow needs interaction, workflow failed, workflow completed, and others as appropriate
- Unread notifications have a red dot indicator
- Opening the popover marks all visible notifications as read (badge clears, dots disappear)
- Each notification has an [x] to dismiss it from the list
- Clicking a notification navigates to the relevant workflow instance

---

## 7. API, MCP Server & Skills

### REST API

The REST API is the core interface to the Arbitron. All other clients (dashboard, MCP server, skills, webhooks) consume it.

The API provides operations for:
- **Workflow Management** — CRUD operations on workflow definitions (create folder, update manifest, add/update/remove files, publish, copy from shared)
- **Instance Management** — trigger a workflow (with input variables and priority), pause, resume, cancel, re-run, retry (with variable editing)
- **Instance Monitoring** — list instances (filtered by state, workflow, user), get instance details, stream live execution output, get execution history (logs, outputs, variable state per node)
- **Queue Management** — view queue state, adjust priority of queued instances
- **Interactive Steps** — retrieve pending interactions, submit responses to interactive nodes
- **System Configuration** (admin) — parallelism caps, LLM backend settings, notification configuration, user management
- **Triggers** — manage scheduled triggers (CRUD for cron configurations), webhook trigger endpoints (auto-generated per workflow)
- **Notifications** — list notifications, mark as read, dismiss

Authentication is handled at the API level — local mode auto-authenticates as the default user, org mode validates OAuth tokens.

### MCP Server

The MCP server wraps the REST API, exposing the same operations as MCP tools that Claude Code can call. It is the primary interface for workflow authoring — users build and modify workflows conversationally through Claude Code, which uses the MCP tools to make changes.

The MCP server provides tools mirroring the REST API capabilities: create/edit/delete workflows, manage files within a workflow folder, trigger and manage instances, check status, interact with interactive steps, publish/copy workflows, and configure the system.

### Claude Code Skills

Three skills provide guided, conversational experiences on top of the MCP tools:

1. **Workflow Authoring Skill** — guides the user through building a new workflow. Asks about the goal, helps design the graph, creates the folder structure, generates the JSON manifest, writes markdown templates and scripts. Uses MCP tools under the hood.

2. **Workflow Scheduling Skill** — helps the user configure triggers for an existing workflow: cron schedules, webhook endpoints. Explains the options, sets them up via MCP tools.

3. **Workflow Running Skill** — triggers a workflow, monitors progress, surfaces interactive steps to the user within their Claude Code session, reports results. Enables the fully MCP-first experience where the dashboard is optional.

---

## 8. Triggers

Three trigger types determine how a workflow instance is created:

### Manual

- Always available for every workflow
- Initiated by a user via the REST API, MCP tools, or a dashboard action
- The user provides values for the workflow's input schema and selects a priority level

### Scheduled

- Configured per workflow via a cron expression
- When the schedule fires, a new instance is created with preconfigured input values and priority (defined as part of the trigger configuration)
- Multiple schedules can be configured for the same workflow
- If the queue is full when a schedule fires, the instance is queued normally

### Webhook

- Each webhook trigger generates a unique endpoint URL for the workflow
- When the endpoint receives an HTTP request, a new instance is created
- The request body can provide values for the workflow's input schema (mapped in the trigger configuration)
- Priority is preconfigured as part of the trigger definition
- Multiple webhook triggers can be configured for the same workflow (different endpoints for different integrations)

### Trigger vs. Webhook Wait Node

Triggers start new workflow instances. The webhook wait node is different — it pauses a running instance mid-execution until a specific endpoint is hit. They are separate concepts that happen to both involve webhooks.

---

## 9. Observability & Debugging

### Full Transparency

Every user can see the complete execution trace of their own workflow instances. Admins can see all users' traces. Nothing is hidden.

### Per-Node Execution Record

Each node execution captures:
- **Inputs** — the resolved values after `{{variable}}` substitution, script arguments, tool call parameters
- **Output** — the full structured JSON emitted by the node
- **Timing** — start time, end time, duration
- **Status** — succeeded, failed, skipped, retried
- **Error details** — if failed, the full error information (stack traces, exit codes, error messages)

For LLM nodes specifically:
- The full LLM conversation (system prompt, user messages, assistant responses) — not just the final output
- Model used
- Token usage

For script nodes specifically:
- Full stdout and stderr streams
- Exit code

### Instance Variable History

The state of all instance variables is recorded at each node boundary, creating a timeline of how variables evolved throughout the workflow. This allows the user to trace exactly when and where a variable was set or changed.

### Live Streaming

For running instances, all of the above is available in real time — the dashboard streams LLM conversations and script output as they happen, and the API supports streaming for MCP/programmatic consumers.

### System Logging

The Arbitron itself produces structured, configurable logging with pluggable output destinations. At launch, logs go to files. The logging system is designed so that switching to a centralized provider (application performance monitoring, OpenTelemetry-compatible collectors, etc.) is a configuration change, not a code change.

This is separate from the per-node execution records — system logging covers the Arbitron's own operations (API requests, queue decisions, engine lifecycle, errors), while execution records are the user-facing workflow trace data.

---

## 10. Product Phasing (Core-Out)

The product is structured for Core-Out delivery. Each phase builds on the previous one. These phases are the "big picture" product phases, each suitable for its own feature-pipeline cycle.

### Phase 1: Workflow Engine & REST API

The execution engine (graph walking, node execution, structured JSON output, instance variables, `{{substitution}}`, conditional routing, transform nodes, error handling), the instance lifecycle (queue, run, complete, fail, cancel, pause, retry, re-run), and the REST API exposing all operations. Single-user local mode with default user. File-based structured logging. No dashboard, no MCP server — API-only.

### Phase 2: Queue & Concurrency

Global and per-user parallelism caps. Priority system. Queue management (ordering, state transitions when capacity opens). Waiting states (timer delay, webhook wait nodes) that don't count against caps.

### Phase 3: Dashboard

Queue view, workflow instance execution view with live-updating graph visualization and node output streaming, workflow review panel with usage statistics and live updates, notification bell with persistence and read tracking. Auto-layout engine for workflow graphs with manual node dragging.

### Phase 4: Interactive Steps

Interactive node type. Embedded conversational interface in the dashboard execution view (comic-book callout). Notification system for alerting users that a workflow needs attention (in-app + external notification stub). Indefinite wait with no cap impact.

### Phase 5: MCP Server & Skills

MCP server wrapping the REST API. Three Claude Code skills: workflow authoring, workflow scheduling, workflow running (including interactive step participation from Claude Code).

### Phase 6: Multi-User & Auth

OAuth integration (Cloudflare Zero Trust or Entra, decision deferred). User and admin roles. Private workspaces and shared workflow directory. Publishing model (copy, unique names, read-only shared, re-publish). Visibility rules (users see own, admins see all). Admin intervention capabilities.

### Phase 7: Triggers & Scheduling

Scheduled triggers (cron). Webhook triggers (auto-generated endpoints, request body mapping). Multiple triggers per workflow.

### Phase 8: Parallel Execution

Fan-out / fan-in nodes. Parallel branch execution with isolated variable copies. Fan-in collects outputs as ordered array.

---

## Appendix: Deferred Items

The following items were discussed and explicitly deferred for future consideration:

- **Sub-workflows** — a node that is itself an entire workflow. Excluded due to complexity (nested state, error propagation, variable scoping). Can be approximated by chaining workflows via triggers.
- **Workflow version history with rollback** — the data model should leave architectural space for this, but no version tracking or rollback in v1.
- **Data retention and cleanup policies** — automatic cleanup of old instances, storage limits, archiving. Keep everything for now.
- **.arb file packaging** — zipping workflow folders for portable distribution. The folder structure supports this naturally when ready.
- **External notification transport** — the notification interface is stubbed; the actual delivery mechanism (Teams, email, Slack, etc.) is decided later.
- **Storage watch and MCP result triggers** — originally discussed, simplified to manual, scheduled, and webhook triggers only.
- **Interactive step timeout** — workflows waiting on interactive steps wait indefinitely. Configurable timeouts deferred.
