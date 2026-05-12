# Phase 5 Implementation Design — Phase 1: Workflow CRUD & MCP Foundation

## Overview

This phase extends the workflow repository with write operations, adds corresponding REST endpoints, and builds the MCP server adapter with stdio transport. The MCP server exposes core tools for workflow management, instance management, and execution traces. No real-time streaming and no interactive session participation — those come in Phase 2.

---

## 1. Workflow Write Operations

Extend `WorkflowRepository` with these methods:

- **Create** — creates a new workflow folder and writes the manifest (`workflow.json`)
- **Update** — replaces the workflow manifest
- **Delete** — removes a workflow folder and all its contents
- **WriteFile** — writes or overwrites a file within a workflow folder (templates, scripts, configs)
- **DeleteFile** — removes a file from a workflow folder
- **ListFiles** — lists all files within a workflow folder (relative paths)
- **ReadFile** — already exists on `FileReader`; no change needed

The filesystem adapter (`adapter/filesystem/workflow_repo.go`) implements these with the same path-traversal protections already used in `Load` and `SaveNodePositions` (clean path, verify prefix).

### REST Endpoints

New routes added to the API:

| Method | Path | Description |
|--------|------|-------------|
| POST | `/workflows` | Create a new workflow (body: manifest JSON) |
| PUT | `/workflows/{name}` | Update a workflow's manifest |
| DELETE | `/workflows/{name}` | Delete a workflow and its folder |
| GET | `/workflows/{name}/files` | List files in a workflow folder |
| GET | `/workflows/{name}/files/{path}` | Read a file from a workflow folder |
| PUT | `/workflows/{name}/files/{path}` | Write/overwrite a file in a workflow folder |
| DELETE | `/workflows/{name}/files/{path}` | Delete a file from a workflow folder |

The existing `GET /workflows`, `GET /workflows/{name}`, and `POST /workflows/{name}/validate` remain unchanged.

---

## 2. MCP Server Adapter

A new adapter at `adapter/mcp/` that implements an MCP server. It depends on the same domain ports as the HTTP adapter — engine service, repositories, event bus, config — following the established pattern where adapters receive domain interfaces, not concrete types.

### stdio Transport

The MCP server communicates over stdin/stdout using the standard MCP JSON-RPC protocol. This is the default transport for local Claude Code usage.

### Server Modes

- **`arbitron serve`** — starts the full Arbitron: HTTP server, dashboard, and domain engine. This is the existing command, unchanged.
- **`arbitron mcp`** — starts only the MCP stdio server. Boots the domain layer (database, engine, event bus) but does not start the HTTP server or dashboard. Intended for local Claude Code use where the dashboard isn't needed.
- **`arbitron serve --mcp`** — starts both the HTTP server and the MCP stdio server in the same process, sharing the domain layer. For users who want both the dashboard and Claude Code access.

In all modes, the MCP server adapter calls domain ports directly — same engine, same repositories, same event bus.

---

## 3. MCP Tool Inventory

The MCP server exposes tools that mirror the REST API surface. Each tool maps to an existing domain operation. In Phase 1, these are request-response tools with no streaming.

**Workflow Management:**
- `workflow_list` — list all workflows
- `workflow_get` — get a workflow definition by name
- `workflow_create` — create a new workflow with manifest
- `workflow_update` — update a workflow's manifest
- `workflow_delete` — delete a workflow
- `workflow_validate` — validate a workflow definition
- `workflow_list_files` — list files in a workflow folder
- `workflow_read_file` — read a file from a workflow folder
- `workflow_write_file` — write/overwrite a file in a workflow folder
- `workflow_delete_file` — delete a file from a workflow folder

**Instance Management:**
- `instance_list` — list instances with optional state/workflow filters
- `instance_get` — get instance details
- `instance_trigger` — trigger a new workflow instance with inputs and priority
- `instance_pause` — pause a running instance
- `instance_resume` — resume a paused instance
- `instance_cancel` — cancel an instance
- `instance_retry` — retry a failed instance with optional variable edits
- `instance_rerun` — re-run a terminal instance
- `instance_set_priority` — change priority of a queued or waiting instance

**Execution Traces:**
- `trace_get` — get the full execution trace for an instance
- `trace_get_node` — get a single node's execution record
- `variables_get` — get current instance variables
- `variables_history` — get variable snapshots at each node boundary

**System:**
- `system_config` — get system configuration
- `system_update_queue_config` — update queue parallelism caps

**Notifications:**
- `notification_list` — list notifications
- `notification_unread_count` — get unread count
- `notification_mark_read` — mark notifications as read
- `notification_dismiss` — dismiss a notification

**Workflow Stats:**
- `workflow_stats` — get usage statistics for a workflow

---

## 4. Testing Strategy

All code is developed using strict TDD — tests written before implementation across five scenario categories.

### Workflow Write Operations (filesystem adapter)

- **Happy path:** Create a workflow with valid manifest, verify folder and `workflow.json` written correctly. Write a file, read it back, verify contents match.
- **Success scenarios:** Update an existing manifest preserves other files in the folder. Delete a file leaves the rest of the workflow intact. ListFiles returns correct relative paths including nested subdirectories.
- **Failure scenarios:** Create with a name that already exists returns an error. Update a workflow that doesn't exist returns not-found. Delete a workflow that doesn't exist returns not-found.
- **Error scenarios:** WriteFile with a path that traverses outside the workflow folder is rejected. DeleteFile with a traversal path is rejected. Create with an invalid manifest (unparseable JSON) fails. Filesystem permission errors propagate cleanly.
- **Edge cases:** Workflow names with special characters (spaces, dots, unicode). Empty manifest fields. Writing a file to a nested path that doesn't exist yet (should create intermediate directories). Deleting the last file in a workflow folder leaves the folder and manifest intact.

### REST Endpoints

- **Happy path:** POST `/workflows` creates and returns 201. PUT `/workflows/{name}` updates and returns 200. DELETE removes and returns 204.
- **Success scenarios:** File CRUD through the file endpoints round-trips correctly. Validation endpoint catches errors in newly created workflows.
- **Failure scenarios:** Create with duplicate name returns 409 Conflict. Update/delete non-existent workflow returns 404. Read non-existent file returns 404.
- **Error scenarios:** Malformed JSON body returns 400. Path traversal attempts in file paths return 400. Empty workflow name returns 400.
- **Edge cases:** File paths with URL-encoded characters. PUT file with empty body (creates empty file). GET files on a workflow with no supporting files (only `workflow.json`).

### MCP Server Adapter

- **Happy path:** Each MCP tool calls the correct domain port and returns a well-formed JSON-RPC response. Tool discovery (`tools/list`) returns all registered tools with correct schemas.
- **Success scenarios:** Tool invocations with all optional parameters. Tool invocations with only required parameters. Multiple sequential tool calls in one session.
- **Failure scenarios:** Tool invocation with missing required parameters returns a JSON-RPC error. Tool invocation for a non-existent workflow propagates the domain not-found error as a tool error. Domain-layer errors map to appropriate MCP error codes.
- **Error scenarios:** Malformed JSON-RPC request returns parse error. Unknown tool name returns method-not-found. Domain port returns unexpected error — MCP server doesn't crash, returns internal error.
- **Edge cases:** Tool parameters with null values vs. omitted values. Very large tool responses (full execution trace with many nodes). Concurrent tool calls from the same session.

### MCP stdio Transport

- **Happy path:** Launch MCP server, send `initialize`, receive capabilities. Send `tools/list`, receive full tool inventory. Invoke a tool, receive result.
- **Success scenarios:** Full round-trip: create a workflow via MCP tool, trigger it, get the trace. Multiple tool calls in a single session.
- **Failure scenarios:** Send malformed JSON on stdin — server responds with parse error, stays alive. Send a request before `initialize` — server rejects it.
- **Error scenarios:** stdin closes unexpectedly — server shuts down cleanly. Server domain dependency fails during startup — process exits with non-zero status and clear error message.
- **Edge cases:** Very large request payloads. Rapid sequential requests. Unicode in tool parameters and responses.

### CLI Commands

- **Happy path:** `arbitron mcp` starts MCP server and exits cleanly on SIGINT. `arbitron serve --mcp` starts both HTTP and MCP, both respond to requests.
- **Success scenarios:** Graceful shutdown closes both servers. MCP and HTTP share the same domain state (create workflow via MCP, list via HTTP).
- **Failure scenarios:** `arbitron mcp` with unreachable database fails with descriptive error. `arbitron serve --mcp` where port is already in use fails for HTTP but MCP still reports the error cleanly.
- **Error scenarios:** Invalid config file — both commands exit with clear error messages.
- **Edge cases:** `arbitron mcp` with stdin already closed (immediate EOF) — exits cleanly rather than hanging.
