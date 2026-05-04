# Arbitron Phase 1 — Design Phase 1: Foundation

## Overview

This design phase covers project scaffolding, database schema, configuration loading, workflow loading from disk, and workflow validation. The goal: the system can start up, connect to Postgres, read workflows from the filesystem, and validate their structure.

---

## 1. Package Structure (Hexagonal Architecture)

The project follows strict hexagonal (ports & adapters) architecture with domain-driven package organization.

```
cmd/
  arbitron/
    main.go                  # Bootstrap: wire adapters to ports, start server

domain/
  workflow/
    workflow.go              # Workflow, Node, Edge, InputSchema types
    ports.go                 # WorkflowRepository interface (Load, List, Validate)
    validation.go            # Graph validation logic (pure domain)
  instance/
    instance.go              # Instance, State enum, NodeExecution types
    ports.go                 # InstanceRepository, NodeExecutionRepository, VariableSnapshotRepository interfaces
  variable/
    variable.go              # VariableStore type
    substitution.go          # {{dot.path}} resolver (pure domain)
  engine/
    ports.go                 # LLMProvider, FileReader (ReadFile + ResolvePath), EventBus interfaces
  config/
    config.go                # Config struct (pure data, no loading logic)

adapter/
  postgres/
    migrations/              # SQL migration files (embedded)
    queries/                 # SQL query files for sqlc
    sqlc.yaml                # sqlc config
    instance_repo.go         # InstanceRepository adapter
    snapshot_repo.go         # VariableSnapshotRepository adapter
    user_repo.go             # User seeding
  filesystem/
    workflow_repo.go         # WorkflowRepository adapter (reads from disk)
    file_reader.go           # FileReader adapter (reads template/script files)
  anthropic/
    provider.go              # LLMProvider adapter (Anthropic API)
  http/
    server.go                # chi router setup
    workflow_handler.go      # REST handlers for /workflows
    instance_handler.go      # REST handlers for /instances
    stream_handler.go        # WebSocket streaming handler
  config/
    loader.go                # koanf-based config loading adapter
  observability/
    logging.go               # slog setup
    tracing.go               # OTel setup
```

**Dependency rule:** Everything under `domain/` defines types and interfaces only. It imports nothing from `adapter/` or any third-party framework. Go standard library types (`context.Context`, `time.Time`, `uuid.UUID`) may cross domain boundaries. Third-party types (chi, sqlc, koanf, OTel) must not.

Everything under `adapter/` imports the domain to implement its port interfaces.

`cmd/arbitron/main.go` imports both — it creates adapter instances and injects them into domain services.

---

## 2. Database Migrations

Four migrations run on startup via `golang-migrate`, embedded in the binary using `embed.FS`.

**`001_create_users.up.sql`:**
- Creates `user_role` enum (`user`, `admin`)
- Creates `users` table (id UUID PK, username text unique, role user_role, created_at timestamp)
- Seeds the default user: `INSERT INTO users (id, username, role) VALUES (gen_random_uuid(), 'local', 'admin') ON CONFLICT DO NOTHING`

**`002_create_instances.up.sql`:**
- Creates `instance_state` enum (`queued`, `running`, `completed`, `failed`, `cancelled`, `paused`)
- Creates `instance_priority` enum (`high`, `normal`, `low`)
- Creates `instances` table with all columns per the product spec
- FK constraint on `owner_id` referencing `users(id)`

**`003_create_node_executions.up.sql`:**
- Creates `node_execution_status` enum (`succeeded`, `failed`, `skipped`)
- Creates `node_executions` table with all columns per the product spec
- FK constraint on `instance_id` referencing `instances(id)` with `ON DELETE CASCADE`
- Index on `(instance_id, started_at)`

**`004_create_variable_snapshots.up.sql`:**
- Creates `variable_snapshots` table
- FK constraint on `instance_id` referencing `instances(id)` with `ON DELETE CASCADE`
- Index on `(instance_id, captured_at)`

Each migration has a corresponding `.down.sql` that drops the table and enum in reverse order.

---

## 3. sqlc Query Organization

Queries grouped by domain concept. sqlc generates Go code into `adapter/postgres/generated/`. The adapter layer translates between generated sqlc types and domain types.

**`queries/users.sql`:**
- `GetDefaultUser` — find the user with username `local`

**`queries/instances.sql`:**
- `CreateInstance` — insert a new instance
- `GetInstance` — get by ID
- `ListInstances` — with optional filters (state, workflow_name) and pagination (limit, offset)
- `UpdateInstanceState` — set state, updated_at
- `UpdateInstanceVariables` — set variables JSONB, updated_at
- `UpdateInstanceCurrentNode` — set current_node_id, updated_at
- `SetInstanceFailed` — set state to failed, failed_node_id, updated_at
- `SetInstanceCompleted` — set state to completed, completed_at, updated_at
- `ListRunningInstances` — find all instances where state = 'running' (for crash recovery)

**`queries/node_executions.sql`:**
- `CreateNodeExecution` — insert a new execution record
- `UpdateNodeExecution` — set output, status, completed_at, error, LLM/script fields
- `ListByInstance` — ordered by started_at
- `GetByInstanceAndNode` — get specific node execution for an instance

**`queries/variable_snapshots.sql`:**
- `CreateSnapshot` — insert a new snapshot
- `ListByInstance` — ordered by captured_at

---

## 4. Configuration

**`domain/config/config.go`** defines a pure data struct:

```go
type Config struct {
    Server    ServerConfig
    Database  DatabaseConfig
    Workspace WorkspaceConfig
    LLM       LLMConfig
    Logging   LoggingConfig
    Tracing   TracingConfig
}

type ServerConfig struct {
    Host string
    Port int
}

type DatabaseConfig struct {
    URL string
}

type WorkspaceConfig struct {
    Path string
}

type LLMConfig struct {
    Provider string
    APIKey   string
}

type LoggingConfig struct {
    Level string
    File  string
}

type TracingConfig struct {
    Enabled   bool
    ExportDir string
}
```

**`adapter/config/loader.go`** uses koanf to:
1. Set defaults (host `127.0.0.1`, port `8090`, database URL `postgres://localhost:5432/arbitron`, workspace `~/.arbitron/workflows/`, etc.)
2. Load from `~/.arbitron/config.yaml` if it exists
3. If the file doesn't exist, create `~/.arbitron/config.yaml` with the defaults
4. Override with `ARBITRON_` prefixed env vars (nested via `_` separator: `ARBITRON_SERVER_PORT`, `ARBITRON_DATABASE_URL`)
5. `ANTHROPIC_API_KEY` env var maps to `LLM.APIKey` as a special case
6. Return a populated `domain/config.Config` struct

---

## 5. Workflow Loading

**`domain/workflow/ports.go`** defines:

```go
type WorkflowRepository interface {
    Load(name string) (*Workflow, error)
    List() ([]WorkflowSummary, error)
}
```

**`adapter/filesystem/workflow_repo.go`** implements the repository:
- `List()` — scan the workspace directory, read `workflow.json` from each subdirectory, return name/description/author summaries. Skip subdirectories without a `workflow.json`.
- `Load(name)` — read `<workspace>/<name>/workflow.json`, parse into domain `Workflow` type. Return an error if the directory or manifest doesn't exist.

Parsing maps the JSON manifest into domain types: `Workflow`, `Node`, `Edge`, `InputField`, and type-specific config structs (`LLMCallConfig`, `ScriptConfig`, `ToolCallConfig`, `ConditionalConfig`, `TransformConfig`).

---

## 6. Workflow Validation

**`domain/workflow/validation.go`** — pure domain logic, no adapters needed.

`Validate(workflow *Workflow) []ValidationError` checks:

1. **Entry node** — exactly one node has `entrypoint: true`
2. **Unique node IDs** — no duplicate IDs
3. **Edge integrity** — every edge's `source` and `target` reference existing node IDs
4. **Conditional edge completeness** — every condition `output` and the `defaultOutput` in a conditional node has a corresponding edge with matching `source` and `output`
5. **Graph connectivity** — all nodes are reachable from the entry node via a breadth-first traversal of edges
6. **File references** — `templateFile` and `scriptFile` paths in node configs resolve to existing files within the workflow folder. This check requires filesystem access, so it takes a `FileReader` interface to check existence without importing filesystem code.
7. **Input schema validity** — types are valid enum values, required fields without defaults must be provided at trigger time
8. **No orphan edges** — no edges whose source or target doesn't exist (covered by edge integrity, but called out explicitly)

Returns an array of `ValidationError` (each with a human-readable message and the node/edge ID it relates to). Empty array means valid.

---

## 7. Startup Sequence

In `cmd/arbitron/main.go`, the cobra `serve` command:

1. Load config via `adapter/config.Load()`
2. Set up slog via `adapter/observability.SetupLogging(config.Logging)`
3. Connect to Postgres via `pgx` connection pool
4. Run migrations via `golang-migrate` with embedded SQL files
5. Initialize sqlc querier from the connection pool
6. Create `adapter/postgres.InstanceRepo` from querier
7. Create `adapter/postgres.SnapshotRepo` from querier
8. Create `adapter/filesystem.WorkflowRepo` with workspace path
9. Create `adapter/filesystem.FileReader` with workspace path
10. Scan for crashed instances: query `ListRunningInstances`, set each to `failed`
11. Create `adapter/anthropic.Provider` with API key
12. Initialize OTel tracer via `adapter/observability.SetupTracing(config.Tracing)`
13. Create domain `Engine` service, inject all adapters via port interfaces
14. Create HTTP handlers, inject engine and repositories
15. Set up chi router with middleware (recovery, request logging)
16. Start HTTP server on configured host:port
17. Log startup summary
18. Wait for SIGINT/SIGTERM, then graceful shutdown (stop accepting requests, wait for in-flight, flush OTel, close DB)

---

## 8. Testing Strategy

**Config loading tests:**
- Defaults are populated when no file or env vars exist
- YAML file values override defaults
- Env vars override YAML values
- `ANTHROPIC_API_KEY` maps to LLM.APIKey
- Malformed YAML returns a clear error
- Missing config file creates one with defaults

**Workflow loading tests:**
- Valid workflow.json parses into correct domain types
- All five node type configs parse correctly
- Missing workflow directory returns not-found error
- Missing workflow.json returns a clear error
- Malformed JSON returns a clear error
- List returns summaries for all valid workflows, skips directories without workflow.json

**Workflow validation tests:**
- Valid workflow passes all checks
- Missing entry node returns error
- Multiple entry nodes returns error
- Duplicate node IDs returns error
- Edge referencing non-existent node returns error
- Conditional node with missing edge for an output returns error
- Unreachable node returns error
- Missing template file returns error (via FileReader)
- Valid input schema passes, invalid type returns error
- Complex valid workflow (multiple branches, multiple conditionals) passes

**Migration tests:**
- Up migrations run cleanly against a fresh Postgres (via testcontainers)
- Down migrations reverse cleanly
- Migrations are idempotent (running twice doesn't error)
- Default user is seeded on first run
