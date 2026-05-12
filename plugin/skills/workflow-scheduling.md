---
name: workflow-scheduling
description: Configure trigger defaults for Arbitron workflows
---

# Workflow Scheduling

## When to use

Use this skill when the user wants to:

- Set default input values for a workflow's trigger configuration
- Configure the default priority for workflow runs
- Review or update existing trigger configurations

This skill handles trigger configuration only. For creating or editing workflow
definitions, use the **workflow-authoring** skill. For running workflows, use
the **workflow-running** skill.

## Available MCP tools

| Tool              | Purpose                                  |
| ----------------- | ---------------------------------------- |
| `workflow_list`   | Find workflows by name or browse all     |
| `workflow_get`    | Read workflow definition and input schema |
| `workflow_update` | Save trigger configuration changes       |

## Conversational flow

1. **Identify the workflow.** Ask the user which workflow they want to configure.
   If they are unsure or give a partial name, call `workflow_list` to search and
   present matching results.

2. **Load the workflow.** Call `workflow_get` to retrieve the full definition,
   including its input schema and current trigger configuration.

3. **Present the input schema.** Display each input clearly:
   - Name
   - Type (string, number, boolean, etc.)
   - Required or optional
   - Current default value (if any)

   Do not dump raw JSON. Format the schema as a readable list or table.

4. **Ask about default values.** Offer to set defaults for all inputs at once or
   let the user pick specific ones. For required inputs that have no default,
   suggest setting one so trigger-time prompts are minimized.

5. **Ask about default priority.** Present the three levels: high, normal, low.
   If the workflow already has a default priority, show it and ask if the user
   wants to change it.

6. **Save.** Call `workflow_update` with the updated trigger configuration.

7. **Confirm.** Summarize what was configured: which defaults were set or
   changed, and the priority level.

## Phase 7: cron and webhook triggers

In Phase 7, this skill expands to cover scheduled runs and webhook endpoints.
When that arrives:

- **Cron triggers** let a workflow run on a recurring schedule. Translate cron
  expressions into plain language for the user (e.g., `0 9 * * 1-5` becomes
  "every weekday at 9:00 AM").
- **Webhook triggers** let external systems start a workflow by calling an
  endpoint. Configuration will include URL generation and optional secret
  tokens.

Until those capabilities land, this skill focuses exclusively on default input
values and priority.

## Guidelines

- **Show the schema clearly.** Use a list or table, never raw JSON.
- **Don't overwhelm.** If a workflow has many inputs, offer to set all defaults
  or let the user pick specific ones rather than walking through every field.
- **Stay in scope.** This skill only configures triggers. It does not edit
  workflow definitions (that is authoring) or execute workflows (that is
  running).
