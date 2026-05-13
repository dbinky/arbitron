---
name: workflow-authoring
description: Build and modify Arbitron workflows collaboratively from Claude Code
---

# Workflow Authoring

## When to use

Use this skill when the user wants to:

- Create a new workflow from scratch
- Modify an existing workflow (add, remove, or reconfigure nodes)
- Change prompt templates or script files associated with a workflow
- Update a workflow's input schema or variable definitions
- Understand the structure of an existing workflow
- Troubleshoot validation errors in a workflow definition

## Available MCP tools

These tools interact with the Arbitron workflow engine:

| Tool | Description |
|------|-------------|
| `workflow_list` | List all workflows |
| `workflow_get` | Get a workflow definition by name |
| `workflow_create` | Create a new workflow with a manifest |
| `workflow_update` | Update an existing workflow manifest |
| `workflow_delete` | Delete a workflow |
| `workflow_validate` | Validate a workflow definition |
| `workflow_list_files` | List files in a workflow folder |
| `workflow_read_file` | Read a file from a workflow folder |
| `workflow_write_file` | Write or overwrite a file in a workflow folder |
| `workflow_delete_file` | Delete a file from a workflow folder |

## Node types

Workflows are built from nodes connected together into a graph. Each node does one thing:

### LLM Call
Sends a prompt to an AI model and gets a structured response back. Use this when the workflow needs to generate text, analyze content, make a decision using natural language reasoning, or extract structured data from unstructured input. The prompt can reference workflow variables using template syntax.

### Script
Runs a script in a language like bash, Python, or Node.js. Use this for data processing, file manipulation, API calls to external services, or any logic that is easier to express in code than in a prompt. The script receives workflow variables as environment variables and can write outputs back.

### Tool Call
Runs a CLI command. Use this for invoking system utilities, running database queries, calling APIs with curl, or any operation that maps naturally to a single command-line invocation.

### Conditional
A decision point that routes the workflow down different paths based on conditions. Use this when the next step depends on a value -- for example, branching based on whether a previous LLM call classified something as positive or negative, or whether a script returned a success or failure code.

### Transform
Lightweight variable manipulation without running a script or calling an LLM. Supports operations like set, increment, append, and delete on workflow variables. Use this for bookkeeping -- counters, accumulating results into a list, or reshaping data between nodes.

### Timer Delay
Pauses the workflow for a configured duration. Use this when you need to wait before proceeding -- for example, rate-limiting API calls, waiting for an external process to complete, or spacing out retries.

### Webhook Wait
Pauses the workflow until an external system sends a request to a callback URL. Use this for integrations where an external service needs to signal that something has happened -- a deployment finished, an approval was granted, or a third-party process completed.

### Interactive
Pauses the workflow for a human conversation with an LLM. Use this when the workflow needs human judgment, clarification, or creative input before it can proceed. The human sees the conversation context and can respond naturally.

## Creating a new workflow

Follow these steps when the user wants to build a new workflow:

1. **Understand the goal.** Ask what the workflow should accomplish end to end. Get a clear picture of the inputs, the desired outputs, and what happens in between.

2. **Ask clarifying questions one at a time.** Do not front-load a list of questions. Ask the most important question, wait for the answer, then ask the next one. Typical questions include: What triggers this workflow? What data does it start with? Are there external services involved? Does it need human review at any point?

3. **Propose a graph structure.** Present the workflow as a list of named nodes with their types and connections. For example:
   - `fetch-data` (Script) -- pulls records from the API
   - `analyze` (LLM Call) -- summarizes the records
   - `check-quality` (Conditional) -- routes based on confidence score
     - High confidence -> `publish` (Tool Call)
     - Low confidence -> `human-review` (Interactive)

4. **Iterate until the user agrees.** Adjust nodes, connections, and configuration based on feedback. Do not proceed to creation until the user confirms the design.

5. **Create the workflow.** Call `workflow_create` with the complete manifest.

6. **Write supporting files.** Use `workflow_write_file` to create prompt templates, scripts, or any other files the workflow references.

7. **Validate.** Call `workflow_validate` to check the workflow definition for errors.

8. **Report results.** Tell the user whether validation passed. If there are errors, fix them and re-validate.

## Modifying an existing workflow

Follow these steps when the user wants to change a workflow:

1. **Load the workflow.** Call `workflow_get` to retrieve the manifest and `workflow_list_files` to see all associated files.

2. **Present the current structure.** Show the workflow as named nodes and connections in a readable format, not raw JSON. Briefly explain what each node does.

3. **Understand the requested changes.** Confirm exactly what the user wants to add, remove, or modify.

4. **Propose the changes and confirm.** Describe what you are going to do — which nodes or files will be added, changed, or removed. Get the user's agreement before making any destructive changes such as deleting files or removing nodes.

5. **Apply the changes.** Use `workflow_update` for manifest changes and `workflow_write_file` or `workflow_delete_file` for associated files.

6. **Validate and report.** Call `workflow_validate` and share the results. Fix any errors before finishing.

## Behavioral guidelines

- **Always validate.** After creating or modifying a workflow, call `workflow_validate` before considering the task complete.
- **Present structure readably.** Show workflows as node names and connections, not raw JSON. Explain what each node does in plain language.
- **Explain node types in context.** When proposing a node, explain why that type fits. Do not assume the user knows the difference between a Script node and a Tool Call node.
- **Follow specific instructions directly.** If the user says exactly what they want, do it. Do not second-guess or over-question clear requirements.
- **Stay in scope.** This skill is for authoring workflow definitions. Do not execute workflows -- that is the running skill. Do not configure triggers or schedules -- that is the scheduling skill.
- **One question at a time.** During the clarification phase, ask a single question and wait for the answer. Batching multiple questions makes the conversation harder to follow.
