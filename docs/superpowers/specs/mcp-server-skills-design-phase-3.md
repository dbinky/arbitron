# Phase 5 Implementation Design — Phase 3: Skills & Distribution

## Overview

This phase delivers the three Claude Code skills (workflow authoring, workflow scheduling, workflow running) and packages everything for distribution via the plugin marketplace. The skills are markdown files with behavioral instructions that guide Claude Code through conversational workflows using the MCP tools built in Phases 1 and 2.

---

## 1. Plugin Marketplace Package

The Arbitron plugin is distributed as `dbinky/arbitron` on the Claude Code plugin marketplace.

### Package Contents

- MCP server configuration (tells Claude Code how to launch `arbitron mcp` for stdio, or connect to a Streamable HTTP endpoint for remote)
- Three skill files (authoring, scheduling, running)
- Plugin metadata (name, description, version, author)

### Installation Scenarios

- **Local:** The Arbitron installer script runs `plugin marketplace add dbinky/arbitron` automatically. The MCP server config points to the locally installed `arbitron mcp` binary. Zero additional setup.
- **Remote:** User runs `/plugin marketplace add dbinky/arbitron` manually. The MCP server config is updated to point to the remote Arbitron server's Streamable HTTP endpoint. The user provides the server URL during setup.

### Server Discovery

The plugin needs to know where the Arbitron server is. In local/stdio mode, it launches the binary directly. In remote mode, the Streamable HTTP URL is configured as a plugin setting that the user provides once on first use.

---

## 2. Workflow Authoring Skill

The authoring skill guides users through building and modifying workflows collaboratively. It uses the workflow management MCP tools from Phase 1.

### MCP Tools Used

- `workflow_list`, `workflow_get` — to understand what already exists
- `workflow_create`, `workflow_update` — to create and modify manifests
- `workflow_write_file`, `workflow_delete_file`, `workflow_list_files`, `workflow_read_file` — to manage templates, scripts, and supporting files
- `workflow_validate` — to check the result after changes

### Conversational Flow for New Workflows

1. Ask the user what the workflow should accomplish
2. Ask clarifying questions one at a time — inputs, steps, decision points, outputs
3. Propose a graph structure: which node types, how they connect, what each does
4. Iterate with the user until the design is agreed
5. Create the workflow folder and manifest via `workflow_create`
6. Write template files, scripts, and supporting files via `workflow_write_file`
7. Validate the result via `workflow_validate`
8. Report the result — what was created, any validation warnings

### Conversational Flow for Modifications

1. Load the existing workflow via `workflow_get` and list its files via `workflow_list_files`
2. Understand what the user wants to change
3. Propose the changes and confirm with the user
4. Apply changes via `workflow_update` and file management tools
5. Validate and report

### Behavioral Guidelines

- Always validate after creating or modifying a workflow
- Present the graph structure in human-readable form (node names and connections), not raw JSON
- When proposing a graph, explain what each node does and why it's connected that way
- Don't assume the user knows node type names — describe capabilities and suggest the right type
- If the user gives specific instructions ("add an LLM node here"), follow them directly rather than asking more questions

---

## 3. Workflow Scheduling Skill

The scheduling skill helps users configure manual trigger defaults for their workflows. It expands with cron and webhook capabilities when Phase 7 lands.

### MCP Tools Used

- `workflow_get` — to read the current workflow definition including input schema
- `workflow_update` — to persist trigger configuration changes
- `workflow_list` — to help the user find workflows by name

### Phase 5 Conversational Flow

1. Ask which workflow the user wants to configure (or accept it as input)
2. Load the workflow and show its current input schema and any existing trigger defaults
3. Walk through the inputs — for each, ask if the user wants a default value so they don't have to provide it every time
4. Ask about a default priority level for new instances
5. Save the trigger configuration via `workflow_update`
6. Confirm what was configured

### Behavioral Guidelines

- Show the input schema in a readable format — name, type, required/optional, current default
- For required inputs with no default, suggest that setting a default makes manual triggering faster
- Don't overwhelm — if there are many inputs, offer to set defaults for all or let the user pick specific ones
- When Phase 7 capabilities arrive, the skill should explain cron syntax in plain language (e.g., "every weekday at 9am") rather than requiring the user to write cron expressions

---

## 4. Workflow Running Skill

The running skill is the most complex of the three. It handles triggering, adaptive monitoring, interactive participation, instance management, and reconnection — all the MCP tools from Phases 1 and 2.

### MCP Tools Used

- `workflow_list`, `workflow_get` — to find and inspect workflows before triggering
- `instance_trigger` — to start a workflow
- `instance_get`, `instance_list` — to check status
- `instance_subscribe`, `instance_unsubscribe` — to stream live events
- `interactive_send_message`, `interactive_end_session`, `interactive_get_history` — for interactive participation
- `instance_pause`, `instance_resume`, `instance_cancel`, `instance_retry`, `instance_rerun` — for instance management
- `trace_get`, `trace_get_node`, `variables_get`, `variables_history` — for inspecting execution details
- `notification_list`, `notification_unread_count` — for checking what needs attention

### Triggering Flow

1. Identify the workflow (user names it, or skill lists available workflows)
2. Load the input schema via `workflow_get`
3. Prompt the user for input values, validating types as they go
4. Ask about priority (default to Normal if the user doesn't care)
5. Trigger via `instance_trigger`
6. Report the instance ID and initial state, then transition into monitoring

### Adaptive Monitoring Flow

1. After triggering (or when the user asks to watch an instance), subscribe via `instance_subscribe`
2. **Attached mode:** Stream events to the user as they arrive — node started, completed, output summaries. Stay attached as long as nodes are completing at a reasonable pace.
3. **Detach trigger:** When the instance enters a waiting state (timer delay, webhook wait) or when there's been an extended gap between node completions, detach. Explain to the user what's happening: "The workflow is waiting on a timer. I'll let you know when something needs your attention."
4. **Detached mode:** Unsubscribe from the event stream. Rely on notifications (`notification.created` events via system subscription) to alert the user if the instance fails, completes, or needs interaction.
5. **Re-attach:** If the user asks "how's my workflow doing?" — call `instance_get` for a status check. If the user wants to watch again, re-subscribe.

### Interactive Participation Flow

1. A `notification.created` event (type `needs_interaction`) or an `interactive.message` event signals an interactive step
2. Load conversation history via `interactive_get_history`
3. Present the LLM's opening message (or full history if resuming) to the user
4. User types a response; skill calls `interactive_send_message`
5. Stream `interactive.token` events to show the LLM's reply in real time
6. On `interactive.message` (complete assistant response), present it and wait for the user's next input
7. Repeat until `interactive.ended` — either LLM completed the session or user called `interactive_end_session`
8. Report the session outcome and resume monitoring the workflow

### Reconnection Flow

1. User starts a new Claude Code session and asks about a workflow
2. Skill calls `instance_get` or `instance_list` to find the instance
3. Presents current state — running, waiting, completed, failed, etc.
4. If an interactive session is in progress, loads history via `interactive_get_history` and offers to continue
5. If the user wants to monitor, subscribes via `instance_subscribe`

### Instance Management

The skill provides natural-language access to all instance operations: "pause my workflow," "cancel it," "retry with different inputs," "run it again." For retry with variable edits, the skill shows current variables and asks which ones to change before calling `instance_retry`.

### Behavioral Guidelines

- When attached and streaming events, summarize node outputs concisely rather than dumping raw JSON
- On detach, always explain why and how to check back
- During interactive sessions, stay out of the way — present the LLM's messages and the user's responses without adding commentary unless asked
- When presenting instance state after reconnection, lead with what needs attention (failures, interactive steps) rather than recapping the full history

---

## 5. Testing Strategy

All code is developed using strict TDD — tests written before implementation across five scenario categories. Skills are markdown files, so testing focuses on the plugin package structure and the MCP tool interactions that skills depend on.

### Plugin Marketplace Package

- **Happy path:** Plugin package contains valid metadata, MCP server configuration, and all three skill files. Plugin installs successfully via marketplace command.
- **Success scenarios:** Local mode config correctly references the `arbitron mcp` binary path. Remote mode config accepts and stores a Streamable HTTP URL. Plugin update replaces all files cleanly.
- **Failure scenarios:** Plugin install with missing binary path in local mode — reports clear error. Remote mode with unreachable server URL — connection error on first tool use, not on install.
- **Error scenarios:** Plugin metadata with missing required fields — marketplace rejects the package. Skill files with invalid frontmatter — plugin still installs, skill is unusable (graceful degradation).
- **Edge cases:** Installing over a previous version preserves user's server URL configuration. Uninstall removes all skills and MCP config cleanly.

### Authoring Skill (MCP Tool Integration)

- **Happy path:** Create a workflow through the full tool sequence — `workflow_create`, `workflow_write_file` for templates, `workflow_validate` returns clean. Modify an existing workflow — `workflow_get`, `workflow_update`, `workflow_validate`.
- **Success scenarios:** Create a workflow with multiple node types and supporting files. Modify just one file in an existing workflow without touching the manifest. List files after creation matches what was written.
- **Failure scenarios:** Create a workflow that fails validation — error message identifies the problem (missing entrypoint, broken edge reference). Write a file to a workflow that doesn't exist — error.
- **Error scenarios:** Create followed by immediate validate where the manifest has a typo in a node ID — validation catches the dangling edge reference.
- **Edge cases:** Create a workflow with no supporting files (manifest only). Modify a workflow by deleting a template that a node references — validation catches it.

### Scheduling Skill (MCP Tool Integration)

- **Happy path:** Load a workflow's input schema, update the manifest with default values, read it back — defaults are persisted.
- **Success scenarios:** Set defaults for some inputs, leave others without. Change the default priority. Update defaults on a workflow that already has some.
- **Failure scenarios:** Set a default that doesn't match the input's declared type — strict: `workflow_validate` catches type mismatches.
- **Error scenarios:** Update trigger config on a workflow that doesn't exist — error.
- **Edge cases:** Set a default for an optional input that already has a default in the input schema definition — the trigger default takes precedence. Clear a previously set default.

### Running Skill (MCP Tool Integration)

- **Happy path:** Trigger a workflow, subscribe, receive node events through completion, unsubscribe. Full interactive session — receive opening message, send response, receive streamed reply, session ends via LLM.
- **Success scenarios:** Trigger with all inputs provided. Trigger with only required inputs (optionals use defaults). Adaptive monitoring — workflow enters waiting state, skill detaches, re-attaches on user request. Retry a failed instance with variable edits, workflow resumes and completes.
- **Failure scenarios:** Trigger with missing required input — error identifies which input. Trigger a workflow that doesn't exist — error. Send interactive message to a non-waiting instance — error. Pause an already-paused instance — error.
- **Error scenarios:** LLM error during interactive session — error event received, session stays open. Subscribe to an instance, server restarts — subscription lost, reconnection flow recovers state.
- **Edge cases:** Trigger and the workflow completes before the subscribe call — instance_get shows completed, no events to stream. Interactive session — user ends session while LLM is mid-response. Cancel during attached monitoring — cancelled event received, skill reports it. Rerun creates a new instance — skill tracks the new ID.
