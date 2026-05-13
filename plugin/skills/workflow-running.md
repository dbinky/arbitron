---
name: workflow-running
description: Trigger, monitor, and participate in Arbitron workflows from Claude Code
---

# Workflow Running

## When to use

Use this skill when the user wants to:

- Run a workflow
- Monitor a running workflow instance
- Participate in an interactive step (chat with an LLM node)
- Manage a workflow instance (pause, cancel, retry, rerun, change priority)
- Reconnect to a workflow from a previous session

## Available MCP tools

### Discovery

| Tool            | Purpose                                   |
| --------------- | ----------------------------------------- |
| `workflow_list` | Find workflows by name or browse all      |
| `workflow_get`  | Read workflow definition and input schema  |

### Trigger

| Tool               | Purpose                              |
| ------------------ | ------------------------------------ |
| `instance_trigger` | Start a new workflow instance         |

### Status

| Tool            | Purpose                                   |
| --------------- | ----------------------------------------- |
| `instance_get`  | Get current state of a specific instance  |
| `instance_list` | List instances, optionally filtered       |

### Streaming

| Tool                   | Purpose                                      |
| ---------------------- | -------------------------------------------- |
| `instance_subscribe`   | Stream events from a specific instance       |
| `instance_unsubscribe` | Stop streaming events from an instance       |
| `system_subscribe`     | Subscribe to system-wide notification events |
| `system_unsubscribe`   | Stop system-wide subscription                |

### Interactive

| Tool                       | Purpose                                    |
| -------------------------- | ------------------------------------------ |
| `interactive_send_message` | Send user message during interactive step  |
| `interactive_end_session`  | End an interactive session early           |
| `interactive_get_history`  | Load conversation history for a session    |

### Management

| Tool                    | Purpose                           |
| ----------------------- | --------------------------------- |
| `instance_pause`        | Pause a running instance          |
| `instance_resume`       | Resume a paused instance          |
| `instance_cancel`       | Cancel a running instance         |
| `instance_retry`        | Retry with optionally changed inputs |
| `instance_rerun`        | Start a fresh run of the same workflow with the same inputs |
| `instance_set_priority` | Change instance priority          |

### Inspection

| Tool                 | Purpose                                       |
| -------------------- | --------------------------------------------- |
| `trace_get`          | Get the execution trace for an instance       |
| `trace_get_node`     | Get detailed output for a specific node       |
| `variables_get`      | Get current variable values for an instance   |
| `variables_history`  | Get the change history for instance variables |

### Notifications

| Tool                       | Purpose                       |
| -------------------------- | ----------------------------- |
| `notification_list`        | List notifications            |
| `notification_unread_count`| Get count of unread notifications |

## Triggering flow

1. **Identify the workflow.** The user names the workflow they want to run, or
   call `workflow_list` to browse available workflows.

2. **Load the input schema.** Call `workflow_get` to retrieve the definition and
   input schema. Present each input with its name, type, whether it is required
   or optional, and any configured default.

3. **Prompt for inputs.** Ask the user for values. Validate types before
   submitting (e.g., confirm a number field receives a number). Use defaults
   where the user does not override.

4. **Ask about priority.** Default is Normal. Offer High or Low if the user
   wants to adjust.

5. **Trigger.** Call `instance_trigger` with the workflow ID, inputs, and
   priority.

6. **Report and transition.** Tell the user the instance ID and its initial
   state, then transition into monitoring.

## Adaptive monitoring flow

After triggering (or when the user asks to watch a running instance):

1. **Subscribe.** Call `instance_subscribe` to begin streaming events.

2. **Attached mode.** Summarize node progress concisely as events arrive. For
   example: "Node 'fetch-data' completed in 2.3s. Starting 'transform'..." Do
   not dump raw event payloads.

3. **Detach trigger.** Transition to detached mode when:
   - The instance enters a waiting state (e.g., waiting for an external
     callback or a scheduled delay)
   - There is an extended gap between node completions with no user interaction

   Tell the user: "The workflow is waiting on [X]. I'll notify you when
   something needs attention."

4. **Detached mode.** Call `instance_unsubscribe`. Use `system_subscribe` to
   listen for notification events relevant to this instance. The user is free
   to do other work.

5. **Re-attach.** When the user asks about the workflow again, call
   `instance_get` for a quick status check, or re-subscribe with
   `instance_subscribe` to resume streaming.

## Interactive participation flow

When a workflow reaches an interactive step (detected via a `notification.created`
event with type `needs_interaction`, or directly via an `interactive.message` event):

1. **Load history.** Call `interactive_get_history` to retrieve any prior
   messages in the session.

2. **Present the opening message.** Show the LLM node's first message to the
   user clearly.

3. **Relay user responses.** When the user replies, send their message with
   `interactive_send_message`.

4. **Stream the reply.** As `interactive.token` events arrive, present the
   response in real time. When the full `interactive.message` event arrives,
   present the complete message and wait for the user's next input.

5. **Repeat** until an `interactive.ended` event signals the session is
   complete.

6. **Early exit.** The user can end the interactive session at any time by
   asking to stop. Call `interactive_end_session`.

7. **Resume monitoring.** Report the interactive outcome and return to the
   monitoring flow.

## Reconnection flow

When a user asks about a workflow in a new session:

1. **Find the instance.** Call `instance_get` with a known ID, or
   `instance_list` to search by workflow name or state.

2. **Present current state.** Summarize where the instance is: which node is
   active, whether it is running, paused, waiting, or completed.

3. **Interactive check.** If the instance is in an interactive step, load
   history with `interactive_get_history` and offer to continue the
   conversation.

4. **Monitoring check.** If the user wants to watch progress, re-subscribe with
   `instance_subscribe`.

## Instance management

Map natural language to management tools:

| User says                        | Action                                                                 |
| -------------------------------- | ---------------------------------------------------------------------- |
| "Pause my workflow"              | `instance_pause`                                                       |
| "Resume it"                      | `instance_resume`                                                      |
| "Cancel it"                      | `instance_cancel`                                                      |
| "Retry with different inputs"    | Show current variables, ask which to change, call `instance_retry`     |
| "Run it again"                   | `instance_rerun`                                                       |
| "Change priority"                | `instance_set_priority`                                                |
| "What happened?"                 | `trace_get`                                                            |
| "Show node output"               | `trace_get_node`                                                       |
| "Current variables?"             | `variables_get`                                                        |
| "How did variables change?"      | `variables_history`                                                    |

## Behavioral guidelines

- **Summarize events concisely.** Report node transitions, timing, and outcomes
  in brief sentences. Never dump raw JSON or full event payloads.
- **Explain detachment.** When moving to detached mode, tell the user why (what
  the workflow is waiting on) and how to check back ("ask me about the workflow
  anytime").
- **Stay out of the way during interactive sessions.** Relay messages between
  the user and the LLM node without adding commentary unless the user asks for
  help.
- **Lead with what matters after reconnection.** When resuming a workflow in a
  new session, start with what needs the user's attention right now: errors,
  interactive steps waiting for input, or completion status.
- **Quick status means quick status.** When a user asks "How's my workflow?",
  give a brief state summary. Do not produce a full execution trace unless they
  ask for one.
