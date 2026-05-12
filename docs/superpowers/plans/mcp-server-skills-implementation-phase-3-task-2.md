# Phase 3, Task 2: Scheduling & Running Skills

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Write the workflow scheduling and workflow running skills. The scheduling skill covers manual trigger configuration. The running skill handles triggering, adaptive monitoring, interactive participation, instance management, and reconnection.

**Architecture:** Both skills are markdown files in the plugin skills directory. They reference MCP tools from Phases 1 and 2. The running skill is the most complex — it describes adaptive monitoring behavior, interactive session flow, and reconnection patterns.

**Tech Stack:** Markdown

---

## Prerequisites

- Phase 3 Task 1 complete (plugin structure and authoring skill)

## Files to Create

- `plugin/skills/workflow-scheduling.md`
- `plugin/skills/workflow-running.md`

---

### Task 2.1: Workflow Scheduling Skill

- [ ] **Step 1: Write the scheduling skill**

Create `plugin/skills/workflow-scheduling.md`:

**Header/frontmatter:**
- name: workflow-scheduling
- description: Configure trigger defaults for Arbitron workflows

**When to use:** When the user wants to set up default input values for a workflow, configure a default priority, or review/update trigger configurations.

**Available MCP tools:** `workflow_list`, `workflow_get`, `workflow_update`

**Conversational flow:**
1. Ask which workflow to configure (or accept from context). Use `workflow_list` if the user isn't sure of the name.
2. Load the workflow with `workflow_get`
3. Show the current input schema in readable format:
   - For each input: name, type, required/optional, current default value
4. Ask if the user wants to set default values for any inputs
   - For required inputs with no default: suggest that a default makes triggering faster
   - Offer to set all defaults at once or pick specific ones
5. Ask about default priority (high, normal, low) for new instances
6. Save configuration via `workflow_update`
7. Confirm what was configured

**Phase 7 note:** When cron and webhook triggers become available, this skill will expand to cover:
- Creating and managing cron schedules
- Setting up webhook endpoints
- Configuring input mapping for webhook payloads
Explain cron in plain language (e.g., "every weekday at 9am") rather than requiring cron expressions.

**Behavioral guidelines:**
- Show input schema clearly — don't dump raw JSON
- Don't overwhelm with questions if there are many inputs
- This skill only configures triggers, not workflow definitions (that's authoring) or execution (that's running)

- [ ] **Step 2: Commit**

```bash
git add plugin/skills/workflow-scheduling.md
git commit -m "feat: write workflow scheduling skill"
```

---

### Task 2.2: Workflow Running Skill

- [ ] **Step 1: Write the running skill**

Create `plugin/skills/workflow-running.md`:

**Header/frontmatter:**
- name: workflow-running
- description: Trigger, monitor, and participate in Arbitron workflows

**When to use:** When the user wants to run a workflow, check on a running workflow, participate in an interactive step, or manage workflow instances (pause, cancel, retry, etc.).

**Available MCP tools:** List all relevant tools:
- Workflow discovery: `workflow_list`, `workflow_get`
- Triggering: `instance_trigger`
- Status: `instance_get`, `instance_list`
- Streaming: `instance_subscribe`, `instance_unsubscribe`, `system_subscribe`, `system_unsubscribe`
- Interactive: `interactive_send_message`, `interactive_end_session`, `interactive_get_history`
- Management: `instance_pause`, `instance_resume`, `instance_cancel`, `instance_retry`, `instance_rerun`, `instance_set_priority`
- Inspection: `trace_get`, `trace_get_node`, `variables_get`, `variables_history`
- Notifications: `notification_list`, `notification_unread_count`

**Triggering flow:**
1. Identify the workflow — user names it, or list available with `workflow_list`
2. Load the input schema with `workflow_get`
3. Prompt for input values, validating types
4. Ask about priority (default to Normal)
5. Trigger with `instance_trigger`
6. Report instance ID and state, transition to monitoring

**Adaptive monitoring flow:**
1. After triggering, subscribe with `instance_subscribe`
2. **Attached mode:** Stream events as they arrive — when a node starts or completes, summarize concisely. Show output summaries, not raw JSON. Stay attached while nodes complete regularly.
3. **Detach trigger:** When the instance enters a waiting state (you'll see an instance state change to "waiting") or when there's been a long gap between node completions, detach. Tell the user: "The workflow is waiting on [timer/webhook/interactive step]. I'll let you know when something needs your attention."
4. **Detached mode:** Unsubscribe from the instance stream. Subscribe to system events to catch notifications. Alert the user on `notification.created` events for failures, completions, or interactive steps.
5. **Re-attach:** When the user asks about the workflow, call `instance_get` for current state. Re-subscribe if they want to watch live again.

**Interactive participation flow:**
1. Detect interactive step — a `notification.created` (type `needs_interaction`) or `interactive.message` event
2. Load conversation history with `interactive_get_history`
3. Present the LLM's opening message (or full history if resuming)
4. User types a response → call `interactive_send_message`
5. Stream `interactive.token` events to show the LLM's reply in real time
6. On `interactive.message` (complete response), present it and wait for user's next input
7. Repeat until `interactive.ended`
8. If user wants to end early, call `interactive_end_session`
9. Report outcome, resume monitoring

**Reconnection flow:**
1. User starts a new session and asks about a workflow
2. Call `instance_get` or `instance_list` to find the instance
3. Present current state
4. If interactive session in progress: load history with `interactive_get_history`, offer to continue
5. If user wants to monitor: subscribe with `instance_subscribe`

**Instance management:**
- "Pause my workflow" → `instance_pause`
- "Cancel it" → `instance_cancel`
- "Retry with different inputs" → show current variables, ask which to change, call `instance_retry` with edits
- "Run it again" → `instance_rerun` (creates new instance, track new ID)
- "Change priority" → `instance_set_priority`
- "What happened?" → `trace_get` for full execution history
- "Show me the output of [node]" → `trace_get_node`
- "What are the current variables?" → `variables_get`

**Behavioral guidelines:**
- When streaming events, summarize concisely — don't dump raw JSON
- On detach, always explain why and how to check back
- During interactive sessions, stay out of the way — present messages without adding commentary unless asked
- After reconnection, lead with what needs attention (failures, interactive steps), don't recap full history
- When the user says "how's my workflow?" — quick status check, not a full trace

- [ ] **Step 2: Verify all tool references are correct**

Read the skill and cross-check every MCP tool name against the registered tools in `adapter/mcp/register.go`.

- [ ] **Step 3: Commit**

```bash
git add plugin/skills/workflow-running.md
git commit -m "feat: write workflow running skill"
```

---

### Task 2.3: Final Verification

- [ ] **Step 1: Verify plugin structure is complete**

```bash
find plugin/ -type f | sort
```

Expected:
```
plugin/mcp/arbitron.json
plugin/plugin.json
plugin/skills/workflow-authoring.md
plugin/skills/workflow-running.md
plugin/skills/workflow-scheduling.md
```

- [ ] **Step 2: Run full test suite**

Run: `go test ./... -count=1`
Expected: ALL PASS

- [ ] **Step 3: Commit all Phase 3 work**

```bash
git add plugin/
git commit -m "feat: complete Arbitron plugin with all three skills"
```

---

## Acceptance Criteria

- Scheduling skill covers manual trigger defaults (input defaults, priority)
- Scheduling skill mentions Phase 7 future capabilities without implementing them
- Running skill covers all 5 modes: triggering, adaptive monitoring, interactive participation, reconnection, instance management
- Running skill describes attach/detach behavior clearly
- Running skill describes interactive session flow including End Session
- Running skill describes reconnection without replaying missed events
- All MCP tool references in skills match registered tool names
- Plugin directory is complete with 3 skills, MCP config, and metadata
