# Arbitron Phase 5 — MCP Server & Skills

## Overview

Phase 5 adds the MCP server and three Claude Code skills to the Arbitron, making it possible to build, run, and manage workflows entirely from Claude Code without ever opening the dashboard. The MCP server exposes all Arbitron operations as MCP tools with real-time streaming and role-based access. Three skills — workflow authoring, workflow scheduling, and workflow running — provide guided conversational experiences on top of those tools. Interactive step participation from Claude Code is the primary experience, with full server-side persistence enabling seamless switching between Claude Code and the dashboard.

---

## 1. MCP Server

The MCP server exposes the full Arbitron API surface as MCP tools. Every operation available through the REST API — workflow management, instance operations, execution traces, queue management, notifications, and system configuration — has a corresponding MCP tool.

### Workflow Write Operations

Phase 5 adds write operations to the API that were previously absent. The MCP server (and REST API) can now:

- Create a new workflow folder with its manifest
- Update a workflow's manifest (nodes, edges, input schema, metadata)
- Add, update, or remove files within a workflow folder (templates, scripts, configs)
- Delete a workflow
- Validate a workflow definition

These operations enable workflow authoring from Claude Code without direct filesystem access.

### Real-Time Streaming

The MCP server pushes live updates to Claude Code:

- **Node lifecycle events** — node started, completed, failed, with output summaries
- **LLM token streaming** — real-time token-by-token output from LLM nodes
- **Interactive session messages** — full bidirectional message exchange for interactive steps
- **Instance state changes** — running, waiting, completed, failed, cancelled
- **Variable updates** — instance variable changes as nodes execute
- **Notifications** — interactive step waiting, workflow failed, workflow completed, and other notification events pushed proactively to Claude Code

This is the same event stream the dashboard receives via WebSocket, delivered over the MCP protocol.

### Access Control

All MCP tools respect the same role-based permissions as the REST API. In local mode, the single user has admin access to everything. In shared/org mode (Phase 6), tools are gated by the authenticated user's role — admin-only operations (system configuration, intervening in other users' instances) are available as tools but rejected for non-admin users.

### Connection & Reconnection

When an MCP connection drops (laptop sleep, network interruption, Claude Code restart):

- No server-side state is lost — all instance state, conversation history, and execution progress are persisted
- When the user reconnects, they can resume exactly where they left off
- Active interactive sessions are recoverable — reconnecting loads the full conversation history and the user can continue the session
- Workflows in progress continue executing regardless of whether any client is connected
- If a workflow completed, failed, or reached an interactive step while disconnected, the user sees the current state on reconnection — there is no "you missed it" scenario

---

## 2. Skill Distribution

Skills are distributed through the Claude Code plugin marketplace. Users install them with standard plugin commands (e.g., `/plugin marketplace add dbinky/arbitron`).

Two installation scenarios:

- **Local mode** — skills are bundled with the server installation. The installer script registers the skills via marketplace commands automatically, so the user gets the full experience out of the box.
- **Remote/shared mode** — users install skills independently from the marketplace. The skills connect to the remote Arbitron server. Users don't need to install the server software to use the skills.

In both cases, the skills are the same — the only difference is whether the server is local or remote.

---

## 3. Workflow Authoring Skill

The authoring skill guides users through building and modifying workflows conversationally. It's a collaborative experience — the skill asks questions, proposes structure, and writes files, but the user can jump in with specific instructions at any point.

### Building a New Workflow

The skill starts by understanding what the user wants the workflow to accomplish. It asks clarifying questions one at a time — what the inputs are, what steps are involved, what decisions need to be made, what the outputs look like. As the picture becomes clear, the skill proposes a graph structure (nodes, edges, flow) and walks through it with the user.

Once the user approves the design, the skill creates the workflow: manifest, templates, scripts, and any supporting files. It validates the result and reports any issues.

### Modifying an Existing Workflow

The user can ask the skill to modify an existing workflow — add a node, change a template, rewire a conditional, update the input schema. The skill reads the current workflow definition, understands the graph, makes the requested changes, and validates the result. The user doesn't need to know the manifest format or remember node IDs.

### What the Skill Doesn't Do

The skill doesn't execute workflows — that's the running skill. It doesn't configure triggers — that's the scheduling skill. It focuses on the definition: what the workflow is, what it does, and how its pieces connect.

---

## 4. Workflow Scheduling Skill

The scheduling skill helps users configure how and when their workflows are triggered.

### Phase 5 Capabilities

In Phase 5, the skill covers manual trigger configuration:

- Setting up default input values so the user doesn't have to fill them in every time
- Configuring default priority for new instances
- Reviewing and updating existing trigger configurations

### Future Capabilities (Phase 7)

When Phase 7 delivers cron and webhook triggers, the same skill gains those capabilities. The user doesn't learn a new tool — the scheduling skill they already know expands to include:

- Creating and managing cron schedules (recurring workflow runs)
- Setting up webhook endpoints (external systems triggering workflows)
- Configuring input mapping for webhook payloads

The skill is the stable interface; the capabilities grow as the platform does.

---

## 5. Workflow Running Skill

The running skill is how users trigger, monitor, and participate in workflows from Claude Code. It's the most feature-rich of the three skills.

### Triggering a Workflow

The user tells the skill which workflow to run and provides input values. The skill validates the inputs against the workflow's input schema, lets the user set a priority, and triggers the instance. If inputs are missing or invalid, the skill asks for corrections rather than failing silently.

### Adaptive Monitoring

How the skill monitors a running workflow depends on how long it takes:

- **Short-lived workflows** — the skill stays attached, streaming live updates into the Claude Code session. The user sees nodes starting and completing, output summaries, and the final result. The experience feels like watching the workflow execute in real time.
- **Long-running workflows or waiting states** — when a workflow enters a waiting state (timer delay, webhook wait, interactive step) or has been running for an extended period, the skill detaches. It confirms the workflow is still in progress and returns control to the user. From that point, the user can ask for a status update anytime, and notifications surface if something needs attention (interactive step, failure, completion).

The transition from attached to detached is smooth — the skill explains what's happening and how to check back.

### Interactive Step Participation

When a workflow reaches an interactive step, the running skill is the primary way users participate. The experience is native to Claude Code — a multi-turn conversation between the user and the workflow's LLM, with streaming responses.

- The full conversation history loads on entry, whether this is a new session or a resumption
- The user and the LLM exchange messages naturally within the Claude Code session
- Responses stream token-by-token in real time
- The user can end the session explicitly at any time
- Everything is persisted server-side as it happens — every message, every response

### Seamless Switching

Because sessions are server-persisted, the user can move freely between Claude Code and the dashboard:

- Start an interactive session in Claude Code, close the terminal, resume on the dashboard
- Begin on the dashboard, switch to Claude Code to continue
- Reconnect from a new Claude Code session after a disconnection and pick up where you left off

Neither client "owns" the session — the server does.

### Instance Management

The running skill also provides instance operations: pause, resume, cancel, retry (with variable editing), and re-run. The user can manage any instance they have access to, not just ones they triggered through the skill.

---

## 6. Reconnection & Resumption

When a Claude Code session is interrupted — laptop sleep, network drop, user quits and restarts — the Arbitron server continues operating independently. Nothing is lost and nothing is blocked.

### What Continues Without a Client

- Running workflows keep executing
- Timer delays keep counting down
- Webhook wait endpoints remain active
- Interactive sessions stay open and waiting
- Queue management and capacity scheduling proceed normally

### What Happens on Reconnect

When the user reconnects from Claude Code:

- The running skill can retrieve the current state of any instance — where it is, what it's doing, what happened while disconnected
- If an interactive session was in progress, the full conversation history is available and the user can continue the conversation immediately
- If a workflow completed, failed, or needs interaction while the user was away, the current state is visible — not a replay of what was missed, just where things stand now
- No special "reconnect" command — the user just asks about their workflow and the skill shows the current state

### Dashboard Continuity

The same applies in the other direction. If the MCP connection drops while a user is participating in an interactive session, the dashboard can pick up that same session with full history. The two clients are interchangeable at any moment.

---

## 7. Scope Boundaries

### In Scope

- MCP server exposing all Arbitron operations as MCP tools
- Workflow write operations added to the API (create, update, delete workflows and their files)
- Real-time streaming over MCP (node events, LLM tokens, interactive messages, state changes, variable updates)
- Role-gated access control on all MCP tools
- Reconnection and resumption behavior with no state loss
- Skill distribution via plugin marketplace, installable independently from the server
- Workflow authoring skill — collaborative workflow building and modification
- Workflow scheduling skill — manual trigger configuration (default inputs, priority)
- Workflow running skill — trigger, adaptive monitoring, interactive step participation, instance management
- Server-persisted interactive sessions with seamless switching between Claude Code and dashboard

### Not In Scope

- Cron and webhook trigger configuration in the scheduling skill (Phase 7)
- Multi-user authentication and role enforcement (Phase 6 — the access control framework is defined here but enforced by the permissions system that Phase 6 delivers)
- Shared workflow directory, publishing, or copying (Phase 6)
- Fan-out / fan-in parallel execution (deferred — future, formerly Phase 8)
- Dashboard changes — Phase 5 adds no new dashboard functionality
- Offline or disconnected skill operation — skills require a running Arbitron server
