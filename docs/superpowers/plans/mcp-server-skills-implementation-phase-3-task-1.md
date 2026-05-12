# Phase 3, Task 1: Plugin Package & Workflow Authoring Skill

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the Claude Code plugin package structure for `dbinky/arbitron` and write the workflow authoring skill.

**Architecture:** The plugin is a directory structure following the Claude Code plugin marketplace format. It contains MCP server configuration (how to connect to the Arbitron MCP server), skill markdown files, and plugin metadata. The authoring skill is a markdown file with behavioral instructions that guide Claude Code through workflow creation and modification using MCP tools.

**Tech Stack:** Markdown, YAML/JSON (plugin config)

---

## Prerequisites

- Phase 2 complete (MCP server fully functional with streaming and interactive tools)

## Files to Create

### Create: `plugin/` directory structure

```
plugin/
  plugin.json              # Plugin metadata
  mcp/
    arbitron.json           # MCP server configuration
  skills/
    workflow-authoring.md   # Authoring skill
    workflow-scheduling.md  # Scheduling skill (Task 2)
    workflow-running.md     # Running skill (Task 2)
```

### Create: `plugin/plugin.json`

Plugin metadata for the marketplace:

```json
{
  "name": "arbitron",
  "displayName": "Arbitron Workflow Engine",
  "description": "Build, run, and manage Arbitron workflows from Claude Code",
  "version": "0.1.0",
  "author": "dbinky",
  "repository": "https://github.com/dbinky/arbitron"
}
```

### Create: `plugin/mcp/arbitron.json`

MCP server configuration:

```json
{
  "name": "arbitron",
  "transport": {
    "stdio": {
      "command": "arbitron",
      "args": ["mcp"]
    }
  }
}
```

For remote mode, users override with Streamable HTTP:
```json
{
  "name": "arbitron",
  "transport": {
    "streamableHttp": {
      "url": "http://arbitron-server:8090/mcp"
    }
  }
}
```

---

### Task 1.1: Plugin Package Structure

- [ ] **Step 1: Create plugin directory and metadata**

```bash
mkdir -p plugin/mcp plugin/skills
```

- [ ] **Step 2: Write plugin.json**

Write `plugin/plugin.json` with the metadata above.

- [ ] **Step 3: Write MCP server configuration**

Write `plugin/mcp/arbitron.json` with the stdio transport config above.

- [ ] **Step 4: Commit**

```bash
git add plugin/
git commit -m "feat: create Arbitron plugin package structure"
```

---

### Task 1.2: Workflow Authoring Skill

- [ ] **Step 1: Write the authoring skill**

Create `plugin/skills/workflow-authoring.md`:

The skill markdown should include:

**Header/frontmatter:**
- name: workflow-authoring
- description: Build and modify Arbitron workflows collaboratively

**When to use:** When the user wants to create a new workflow, modify an existing one, add/remove nodes, change templates, update input schemas, or work on any aspect of a workflow definition.

**Available MCP tools:** List all workflow_* tools with one-line descriptions.

**Conversational flow for new workflows:**
1. Understand what the user wants the workflow to accomplish
2. Ask clarifying questions one at a time — inputs, steps, decision points, outputs
3. Propose a graph structure: which node types, how they connect, what each does
4. Explain node types in plain language (not by technical name):
   - LLM Call: "A step that sends a prompt to an AI and gets a structured response"
   - Script: "A step that runs a script (bash, Python, etc.)"
   - Tool Call: "A step that runs a CLI command"
   - Conditional: "A decision point that routes based on conditions"
   - Transform: "A lightweight step that sets or modifies variables"
   - Timer Delay: "A pause for a configured duration"
   - Webhook Wait: "Waits for an external system to call a URL"
   - Interactive: "Pauses for a human conversation"
5. Iterate with the user until the design is agreed
6. Create the workflow: `workflow_create` with the manifest
7. Write supporting files: `workflow_write_file` for templates, scripts
8. Validate: `workflow_validate` — report results
9. If validation fails, fix issues and re-validate

**Conversational flow for modifications:**
1. Load the workflow: `workflow_get` to read manifest, `workflow_list_files` to see files
2. Present the current structure in human-readable form
3. Understand what the user wants to change
4. Propose changes and confirm
5. Apply: `workflow_update` for manifest changes, `workflow_write_file`/`workflow_delete_file` for files
6. Validate and report

**Behavioral guidelines:**
- Always validate after creating or modifying
- Present graph structure as node names and connections, not raw JSON
- Explain what each node does and why it's connected that way
- Don't assume the user knows node type names
- If the user gives specific instructions, follow them directly
- Don't execute workflows — that's the running skill
- Don't configure triggers — that's the scheduling skill

- [ ] **Step 2: Verify the skill file is well-formed markdown**

Read the file back and check for:
- Valid frontmatter
- No broken markdown links
- All MCP tool names match what's registered in the server

- [ ] **Step 3: Commit**

```bash
git add plugin/skills/workflow-authoring.md
git commit -m "feat: write workflow authoring skill"
```

---

## Acceptance Criteria

- Plugin directory structure follows marketplace conventions
- plugin.json has valid metadata
- MCP server configuration points to `arbitron mcp` for stdio
- Workflow authoring skill provides clear guidance for creating and modifying workflows
- Skill references only MCP tools that exist in the server
- Skill explains node types in plain language
