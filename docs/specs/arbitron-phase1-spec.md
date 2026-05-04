# Arbitron Phase 1 — Workflow Engine & REST API

## Overview

Phase 1 delivers the core execution engine, instance lifecycle management, and REST API for the Arbitron workflow orchestration platform. It is a single Go binary that reads workflow definitions from the filesystem, executes them as directed graphs of nodes, persists all runtime state to Postgres, and exposes operations via a RESTful API. Single-user local mode with no dashboard, no MCP server, and no multi-user authentication.

---

## 1. Workflow Definition

A workflow is a self-contained folder on disk. Everything needed to execute the workflow lives within it.

### Folder Structure

```
my-workflow/
  workflow.json          # Manifest — graph definition, metadata, input schema
  steps/
    brainstorm.md        # Markdown template for an LLM node
    validate.sh          # Script for a script node
    review-prompt.md     # Another markdown template
```

The author can organize supporting files however they want — flat, in subfolders, by type, by phase. The manifest references files via relative paths within the folder.

### Manifest (workflow.json)

**Metadata:**
- `name` — unique within the workspace
- `description` — human-readable summary
- `author` — who created this workflow
- `defaultErrorBehavior` — workflow-level default for node failure handling (default: `halt`). Individual nodes can override this with their own `errorBehavior`.

**Input Schema:**

Declares the variables the workflow expects when triggered. Each input has:
- `name` — variable name
- `type` — one of `string`, `number`, `boolean`, `object`, `array`
- `description` — what this input is for
- `default` — default value (optional)
- `required` — boolean

Input values become the initial instance variables when an instance is created.

**Nodes:**

Each node has:
- `id` — unique within the workflow
- `type` — one of `llm_call`, `script`, `tool_call`, `conditional`, `transform`
- `config` — type-specific configuration (see Node Types below)
- `label` — human-readable display name
- `description` — what this node does
- `errorBehavior` — optional override of the default error handling (see Error Handling)
- `entrypoint` — boolean; exactly one node must be marked as the entry point

**Edges:**

Each edge has:
- `source` — node ID
- `target` — node ID
- `output` — optional; the named output port on the source node this edge connects from. Required for edges originating from conditional nodes (to specify which condition branch). Ignored for non-conditional source nodes.
- `label` — optional human-readable label

Edges are unconditional connections — just wires. All routing logic lives inside conditional nodes.

### Node Types

**LLM Call** — sends a markdown template to an LLM and collects structured JSON output.
- `templateFile` — relative path to the markdown template
- `model` — model identifier (e.g. `claude-sonnet-4-6`)
- `outputSchema` — JSON schema describing expected structured output (optional)
- `maxTokens` — maximum tokens for the response
- `temperature` — sampling temperature

**Script** — executes a script file and collects structured JSON output from stdout.
- `scriptFile` — relative path to the script
- `interpreter` — runtime to use (e.g. `bash`, `python3`, `node`)
- `args` — array of string arguments (supports `{{substitution}}`)
- `timeoutSeconds` — kill the process if exceeded (treated as a failure)

**Tool Call** — invokes a CLI command and collects structured JSON output from stdout.
- `command` — CLI command string (supports `{{substitution}}`)
- `args` — array of string arguments (supports `{{substitution}}`)
- `timeoutSeconds` — kill the process if exceeded (treated as a failure)

**Conditional (If)** — evaluates conditions against instance state and routes to one of multiple outputs.
- `conditions` — ordered array of conditions, each with:
  - `field` — dot-path reference to a variable or node output (e.g. `validate.score`)
  - `operator` — one of `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `contains`, `exists` (`exists` is true when the field is present in the variable store and is not null)
  - `value` — the value to compare against
  - `output` — named output port (connects to a downstream node via an edge)
- `defaultOutput` — the output port to use if no conditions match

First matching condition wins. Conditional nodes produce no JSON output — they only route.

**Transform** — lightweight variable manipulation with no external execution.
- `operations` — ordered array of operations, each with:
  - `variable` — the variable name to modify
  - `operation` — one of `set`, `increment`, `decrement`, `append`, `delete`
  - `value` — the value to use (supports `{{substitution}}`; not used for `delete`)

Operations execute in order. Transform nodes produce no JSON output — they only mutate variables.

### Workspace

A single configurable directory for the local user's workflows (default: `~/.arbitron/workflows/`). Each subdirectory is a workflow. No shared directory in Phase 1.

---

## 2. Instance Variables & Substitution

### Variable Store

Each workflow instance has a key-value variable store. Values are JSON — a variable can be a string, number, boolean, object, or array.

**Initialization:** When an instance is created, the variable store is populated from the workflow's input schema defaults, overlaid with the values provided at trigger time.

**Node output namespacing:** Each execution node's structured JSON output is stored under a namespace keyed by the node ID. After node `validate` emits `{"score": 85, "passed": true}`, the variable store contains `validate.score = 85` and `validate.passed = true`.

**Transform writes:** Transform nodes write directly to named top-level variables.

### Substitution

Templates, script args, tool call commands, and transform values support `{{dot.path}}` substitution.

- Top-level input variables: `{{myInputVar}}`
- Node outputs: `{{nodeId.field}}` or `{{nodeId.field.nested.path}}`
- Array indexing: `{{nodeId.items.0.name}}`

If a substitution path doesn't resolve, the literal `{{path}}` string is left in place. This makes debugging straightforward — unresolved references are visible in the execution trace.

### Variable History

At each node boundary (before a node starts executing), the engine snapshots the full variable state to a history table in Postgres. This creates a queryable timeline of how variables evolved throughout the instance.

---

## 3. Execution Engine

### Graph Walking

When an instance is triggered:
1. Load the snapshotted workflow definition
2. Find the entry node (`entrypoint: true`)
3. Execute nodes sequentially, following edges from each node's output to the next
4. At each node boundary: snapshot variables to history, write a node execution record, update the instance's current node pointer in Postgres

### Node Execution

**LLM Call:**
1. Read the template file from the workflow folder
2. Perform `{{substitution}}` on the template content
3. Send to the configured model via the LLM provider interface
4. If `outputSchema` is provided, instruct the model to respond in that shape (via tool use)
5. Parse the structured JSON response
6. Store as the node's output under the node ID namespace

**Script:**
1. Resolve `{{substitution}}` in args
2. Execute the script file using the configured interpreter
3. Pass current instance variables as JSON on stdin
4. Capture stdout (expected to be JSON) as the node's output
5. Capture stderr for the execution record
6. Respect `timeoutSeconds` — kill the process and treat as failure if exceeded

**Tool Call:**
1. Resolve `{{substitution}}` in command and args
2. Execute as a subprocess
3. Capture stdout as JSON output
4. Same timeout and stderr handling as Script

**Conditional:**
1. Evaluate conditions in order against current instance variables (using dot-path field references)
2. First matching condition determines the output port to follow
3. If no conditions match, follow `defaultOutput`

**Transform:**
1. Execute operations in order against instance variables
2. `set` assigns a value (with substitution), `increment`/`decrement` adjust numeric values, `append` adds to an array, `delete` removes a variable

### Edge Following

After a node completes, the engine finds the edge(s) where `source` matches the current node ID (and `output` matches for conditional nodes). In Phase 1 there is no fan-out, so exactly one matching edge is expected (or zero for terminal nodes). Zero outgoing edges means the workflow is complete — the instance moves to `completed`.

### Error Handling

**Default behavior:** A node fails, the instance moves to `failed` state, the failed node ID is recorded.

**Per-node overrides (configured in the node's `errorBehavior`):**
- `halt` — same as default
- `retry` — retry up to `retries` times with a fixed `retryDelaySeconds` delay between attempts. Each attempt is recorded. If all retries fail, the instance moves to `failed`.
- `skip` — skip the failed node and continue with `defaultOutput` as the node's output
- `route` — follow the edge connected to the node's error output port (identified by `errorNodeId`) instead of the normal output

### Crash Resilience

The engine writes instance state (current node, status) to Postgres before starting each node. If the process crashes:
- On restart, scan for instances in `running` state
- Move any found to `failed` at the node they were executing
- The user can retry via the API

---

## 4. Instance Lifecycle

### States

- **Queued** — waiting for capacity (Phase 1 transitions through this immediately since there are no concurrency caps)
- **Running** — actively executing nodes
- **Completed** — all nodes finished successfully (terminal)
- **Failed** — a node failed with halt behavior (terminal until retried)
- **Cancelled** — manually stopped (terminal)
- **Paused** — manually paused

### State Transitions

```
trigger → queued → running → completed
                      ↓
                    failed ←── (node error + halt behavior)
                      ↓
                    retry → running (resumes at failed node)

running → paused → resumed → running
running → cancelled
queued → cancelled

completed → re-run → new instance (queued)
failed → re-run → new instance (queued)
cancelled → re-run → new instance (queued)
```

### Operations

**Trigger** — create a new instance from a named workflow. Provide input variable values and priority. The engine validates inputs against the workflow's input schema (type checking, required fields). In Phase 1, instances transition through `queued` immediately to `running`.

**Pause** — moves a running instance to `paused`. The current node completes, then execution stops. The engine checks for pause requests at each node boundary.

**Resume** — moves a paused instance back to `running`. Execution continues from the next node after where it paused.

**Cancel** — terminates the instance. If a node is mid-execution (LLM call in flight, script running), the engine attempts to kill the subprocess/cancel the request, then marks the instance `cancelled`.

**Retry** — only available on `failed` instances. Accepts an optional set of variable edits (key-value pairs to update). Applies the edits, then resumes execution at the failed node.

**Re-run** — available on `completed`, `failed`, or `cancelled` instances. Creates a brand new instance using the same snapshotted workflow definition and the same original input variables.

---

## 5. REST API

All endpoints under `/api/v1`. JSON request/response bodies. No authentication in Phase 1 — every request is the default local user.

### Workflow Management

| Method | Path | Description |
|--------|------|-------------|
| GET | `/workflows` | List all workflows in the workspace (name, description, author) |
| GET | `/workflows/{name}` | Get full workflow definition (parsed workflow.json) |
| POST | `/workflows/{name}/validate` | Validate a workflow definition (graph connectivity, entry node, file references, input schema, edge targets, conditional outputs) |

Workflows are authored as files on disk. The API reads them but does not create or modify them in Phase 1.

### Instance Management

| Method | Path | Description |
|--------|------|-------------|
| POST | `/workflows/{name}/trigger` | Create and start a new instance. Body: `{variables, priority}` |
| GET | `/instances` | List instances. Query params: `state`, `workflowName`, `limit`, `offset` |
| GET | `/instances/{id}` | Get instance details: state, current node, variables, timing |
| POST | `/instances/{id}/pause` | Pause a running instance |
| POST | `/instances/{id}/resume` | Resume a paused instance |
| POST | `/instances/{id}/cancel` | Cancel a running or queued instance |
| POST | `/instances/{id}/retry` | Retry a failed instance. Optional body: `{variables}` |
| POST | `/instances/{id}/rerun` | Re-run from a terminal instance |

### Execution Trace

| Method | Path | Description |
|--------|------|-------------|
| GET | `/instances/{id}/trace` | Full ordered execution trace (all node records) |
| GET | `/instances/{id}/trace/{nodeId}` | Single node execution record |
| GET | `/instances/{id}/variables` | Current instance variables |
| GET | `/instances/{id}/variables/history` | Variable snapshots at each node boundary |

### Streaming

| Method | Path | Description |
|--------|------|-------------|
| GET | `/instances/{id}/ws` | WebSocket: node started/completed/failed, variable updates, state changes, LLM token streaming |

### System

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/system/config` | Current system configuration |

---

## 6. LLM Provider Interface

### Abstraction

A Go interface with two methods:

- **Call** — send a prompt with model config (model name, max tokens, temperature, output schema) and return a complete structured JSON response plus metadata (model, token counts, duration).
- **StreamCall** — same inputs, return a channel of streaming chunks for real-time output via the WebSocket endpoint.

### Anthropic Implementation (Phase 1)

The sole provider implementation calls the Anthropic Messages API directly:
- Maps model names from workflow config to Anthropic API model IDs
- Sends the substituted template as the user message
- Uses Anthropic's tool-use feature to enforce structured JSON output when `outputSchema` is provided
- Supports streaming via Anthropic's SSE streaming API
- API key from `ANTHROPIC_API_KEY` environment variable or config file

### Future Extensibility

Additional providers (LiteLLM, Azure OpenAI) implement the same interface. The engine config specifies which provider to use. No engine code changes required.

---

## 7. Observability

### Per-Node Execution Records (Postgres, User-Facing)

Every node execution records:
- Node ID, type, label
- Status: `succeeded`, `failed`, `skipped`, `retried`
- Start time, end time, duration
- Resolved inputs (template/args after substitution)
- Structured JSON output
- Error details if failed (error message, stack trace, exit code)

LLM nodes additionally record:
- Full conversation (system prompt, user message, assistant response)
- Model used
- Token counts (input and output)

Script and Tool Call nodes additionally record:
- Full stdout and stderr
- Exit code

Retry attempts are individually recorded with their own timing and error details.

### System Logging (Operator-Facing)

**Structured logging via `log/slog`:**
- JSON handler writing to a configurable log file (default `~/.arbitron/logs/arbitron.log`)
- Text handler writing to stdout for development
- Configurable log levels: debug, info, warn, error

**OpenTelemetry tracing:**
- Each workflow instance execution is a trace
- Each node execution is a span within that trace
- Spans carry attributes: node type, workflow name, instance ID, status
- Phase 1 exporter: OTLP file exporter writing to a configurable directory
- Future: swap to Azure Monitor exporter for App Insights integration (config change only)

Log rotation and retention are out of scope for Phase 1.

---

## 8. Database Schema

### instances

| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key |
| owner_id | UUID (FK → users) | Always the default user in Phase 1 |
| workflow_name | text | Name of the workflow |
| workflow_snapshot | JSONB | Full workflow.json at trigger time |
| state | enum | queued, running, completed, failed, cancelled, paused |
| priority | enum | high, normal, low (stored but not enforced in Phase 1) |
| variables | JSONB | Current instance variable state |
| current_node_id | text (nullable) | Node currently executing |
| failed_node_id | text (nullable) | Node that failed (if in failed state) |
| original_variables | JSONB | Input variables as provided at trigger time (immutable, used by re-run) |
| created_at | timestamp | |
| updated_at | timestamp | |
| completed_at | timestamp (nullable) | |

### node_executions

| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key |
| instance_id | UUID (FK → instances) | |
| node_id | text | Node ID from the workflow definition |
| attempt | integer | 1 for first try, 2+ for retries |
| status | enum | succeeded, failed, skipped |
| resolved_inputs | JSONB | Inputs after substitution |
| output | JSONB (nullable) | Structured output |
| error | JSONB (nullable) | Error details |
| llm_conversation | JSONB (nullable) | Full conversation for LLM nodes |
| llm_model | text (nullable) | |
| llm_tokens_in | integer (nullable) | |
| llm_tokens_out | integer (nullable) | |
| script_stdout | text (nullable) | |
| script_stderr | text (nullable) | |
| script_exit_code | integer (nullable) | |
| started_at | timestamp | |
| completed_at | timestamp (nullable) | |

Index on `(instance_id, started_at)` for ordered trace retrieval.

### variable_snapshots

| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key |
| instance_id | UUID (FK → instances) | |
| node_id | text | Node about to execute |
| variables | JSONB | Full variable state |
| captured_at | timestamp | |

Index on `(instance_id, captured_at)`.

### users

| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key |
| username | text (unique) | |
| role | enum | user, admin |
| created_at | timestamp | |

On first startup, a default user (`local`, role `admin`) is created. All instances are owned by this user in Phase 1.

---

## 9. Configuration & Startup

### Configuration File

`~/.arbitron/config.yaml` — created with sensible defaults on first run.

```yaml
server:
  host: "127.0.0.1"
  port: 8090

database:
  url: "postgres://localhost:5432/arbitron"

workspace:
  path: "~/.arbitron/workflows/"

llm:
  provider: "anthropic"
  apiKey: ""  # or set ANTHROPIC_API_KEY env var

logging:
  level: "info"
  file: "~/.arbitron/logs/arbitron.log"

tracing:
  enabled: true
  exportDir: "~/.arbitron/traces/"
```

All settings can be overridden via environment variables with an `ARBITRON_` prefix (e.g. `ARBITRON_SERVER_PORT=9000`). Environment variables take precedence over the config file.

### Startup Sequence

1. Load configuration (file → env var overrides)
2. Connect to Postgres, run migrations (auto-migrate using embedded migration files)
3. Create default user if not exists
4. Scan for instances in `running` state (crash recovery) — move to `failed`
5. Initialize LLM provider
6. Initialize OpenTelemetry tracer
7. Start HTTP server
8. Log startup summary: version, bind address, workspace path, database, provider

### CLI

The binary is invoked as `arbitron serve`. A single subcommand in Phase 1, with room for future CLI utilities (e.g. `arbitron validate <workflow-path>`).

---

## 10. Scope Boundaries

### In Scope
- Execution engine with graph walking and sequential node execution
- Five node types: LLM Call, Script, Tool Call, Conditional (If), Transform
- Dot-path `{{variable}}` substitution
- Instance lifecycle: queued, running, completed, failed, cancelled, paused
- Instance operations: trigger, pause, resume, cancel, retry (with variable editing), re-run
- REST API for all operations
- WebSocket streaming for live instance output
- Workflow validation endpoint
- LLM provider interface with Anthropic-direct implementation
- Per-node execution records in Postgres
- Structured logging (slog) and OpenTelemetry tracing with file exporters
- Single-user local mode with default admin user
- Postgres for all runtime state
- Filesystem for workflow definitions
- YAML configuration with env var overrides

### Not In Scope
- Dashboard or any UI
- MCP server
- Multi-user authentication (OAuth, roles beyond the default user)
- Shared workflow directory or publishing
- Scheduled triggers (cron)
- Webhook triggers
- Interactive nodes
- Timer Delay and Webhook Wait nodes
- Fan-out / Fan-in (parallel execution)
- LiteLLM or multi-provider support
- Workflow CRUD via API (workflows are files on disk)
- Log rotation or retention policies
- .arb file packaging
