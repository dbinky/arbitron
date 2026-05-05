# Phase 1 Task 4: Domain Instance Types, Ports & Database

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define domain instance types (Instance, NodeExecution, VariableSnapshot, State enum), port interfaces, database migrations, sqlc queries, and Postgres adapter implementations.

**Architecture:** Domain types and interfaces in `domain/instance/`. SQL migrations in `adapter/postgres/migrations/`. sqlc queries in `adapter/postgres/queries/`. Generated code and adapters in `adapter/postgres/`.

**Tech Stack:** Go 1.23+, sqlc, golang-migrate, pgx v5, testcontainers-go for integration tests

---

### Task 1: Domain Instance Types

**Files:**
- Create: `domain/instance/instance.go`
- Create: `domain/instance/ports.go`

- [ ] **Step 1: Define domain instance types**

Create `domain/instance/instance.go`:

```go
package instance

import (
	"time"

	"github.com/google/uuid"
)

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StatePaused    State = "paused"
)

type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

type Instance struct {
	ID                uuid.UUID
	OwnerID           uuid.UUID
	WorkflowName      string
	WorkflowSnapshot  map[string]any
	State             State
	Priority          Priority
	Variables         map[string]any
	OriginalVariables map[string]any
	CurrentNodeID     *string
	FailedNodeID      *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

type InstanceSummary struct {
	ID           uuid.UUID  `json:"id"`
	WorkflowName string     `json:"workflowName"`
	State        State      `json:"state"`
	Priority     Priority   `json:"priority"`
	CurrentNodeID *string   `json:"currentNodeId,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

type NodeExecutionStatus string

const (
	NodeStatusSucceeded NodeExecutionStatus = "succeeded"
	NodeStatusFailed    NodeExecutionStatus = "failed"
	NodeStatusSkipped   NodeExecutionStatus = "skipped"
)

type NodeExecution struct {
	ID              uuid.UUID
	InstanceID      uuid.UUID
	NodeID          string
	Attempt         int
	Status          NodeExecutionStatus
	ResolvedInputs  map[string]any
	Output          map[string]any
	Error           map[string]any
	LLMConversation map[string]any
	LLMModel        *string
	LLMTokensIn     *int
	LLMTokensOut    *int
	ScriptStdout    *string
	ScriptStderr    *string
	ScriptExitCode  *int
	StartedAt       time.Time
	CompletedAt     *time.Time
}

type VariableSnapshot struct {
	ID         uuid.UUID
	InstanceID uuid.UUID
	NodeID     string
	Variables  map[string]any
	CapturedAt time.Time
}

type ListFilters struct {
	State        *State
	WorkflowName *string
	Limit        int
	Offset       int
}
```

- [ ] **Step 2: Install uuid dependency**

```bash
go get github.com/google/uuid
```

- [ ] **Step 3: Define port interfaces (including UserRepository)**

Create `domain/instance/ports.go`:

```go
package instance

import (
	"github.com/google/uuid"
)

type InstanceRepository interface {
	Create(inst *Instance) error
	Get(id uuid.UUID) (*Instance, error)
	List(filters ListFilters) ([]InstanceSummary, error)
	UpdateState(id uuid.UUID, state State) error
	UpdateVariables(id uuid.UUID, variables map[string]any) error
	UpdateCurrentNode(id uuid.UUID, nodeID string) error
	SetFailed(id uuid.UUID, failedNodeID string) error
	SetCompleted(id uuid.UUID) error
	ListRunning() ([]Instance, error)
}

type NodeExecutionRepository interface {
	Create(exec *NodeExecution) error
	Update(exec *NodeExecution) error
	ListByInstance(instanceID uuid.UUID) ([]NodeExecution, error)
	GetByInstanceAndNode(instanceID uuid.UUID, nodeID string) (*NodeExecution, error)
}

type VariableSnapshotRepository interface {
	Create(snapshot *VariableSnapshot) error
	ListByInstance(instanceID uuid.UUID) ([]VariableSnapshot, error)
}

type UserRepository interface {
	GetDefaultUser() (uuid.UUID, error)
}
```

- [ ] **Step 4: Commit domain types**

```bash
git add domain/instance/
git commit -m "feat: domain instance types and port interfaces"
```

---

### Task 2: Database Migrations

**Files:**
- Create: `adapter/postgres/migrations/001_create_users.up.sql`
- Create: `adapter/postgres/migrations/001_create_users.down.sql`
- Create: `adapter/postgres/migrations/002_create_instances.up.sql`
- Create: `adapter/postgres/migrations/002_create_instances.down.sql`
- Create: `adapter/postgres/migrations/003_create_node_executions.up.sql`
- Create: `adapter/postgres/migrations/003_create_node_executions.down.sql`
- Create: `adapter/postgres/migrations/004_create_variable_snapshots.up.sql`
- Create: `adapter/postgres/migrations/004_create_variable_snapshots.down.sql`
- Create: `adapter/postgres/migrate.go`

- [ ] **Step 5: Create migration 001 — users**

Create `adapter/postgres/migrations/001_create_users.up.sql`:

```sql
CREATE TYPE user_role AS ENUM ('user', 'admin');

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username TEXT NOT NULL UNIQUE,
    role user_role NOT NULL DEFAULT 'user',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO users (username, role) VALUES ('local', 'admin') ON CONFLICT DO NOTHING;
```

Create `adapter/postgres/migrations/001_create_users.down.sql`:

```sql
DROP TABLE IF EXISTS users;
DROP TYPE IF EXISTS user_role;
```

- [ ] **Step 6: Create migration 002 — instances**

Create `adapter/postgres/migrations/002_create_instances.up.sql`:

```sql
CREATE TYPE instance_state AS ENUM ('queued', 'running', 'completed', 'failed', 'cancelled', 'paused');
CREATE TYPE instance_priority AS ENUM ('high', 'normal', 'low');

CREATE TABLE instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL REFERENCES users(id),
    workflow_name TEXT NOT NULL,
    workflow_snapshot JSONB NOT NULL,
    state instance_state NOT NULL DEFAULT 'queued',
    priority instance_priority NOT NULL DEFAULT 'normal',
    variables JSONB NOT NULL DEFAULT '{}',
    original_variables JSONB NOT NULL DEFAULT '{}',
    current_node_id TEXT,
    failed_node_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
```

Create `adapter/postgres/migrations/002_create_instances.down.sql`:

```sql
DROP TABLE IF EXISTS instances;
DROP TYPE IF EXISTS instance_priority;
DROP TYPE IF EXISTS instance_state;
```

- [ ] **Step 7: Create migration 003 — node_executions**

Create `adapter/postgres/migrations/003_create_node_executions.up.sql`:

```sql
CREATE TYPE node_execution_status AS ENUM ('succeeded', 'failed', 'skipped');

CREATE TABLE node_executions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 1,
    status node_execution_status NOT NULL,
    resolved_inputs JSONB,
    output JSONB,
    error JSONB,
    llm_conversation JSONB,
    llm_model TEXT,
    llm_tokens_in INTEGER,
    llm_tokens_out INTEGER,
    script_stdout TEXT,
    script_stderr TEXT,
    script_exit_code INTEGER,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_node_executions_instance_started ON node_executions (instance_id, started_at);
```

Create `adapter/postgres/migrations/003_create_node_executions.down.sql`:

```sql
DROP TABLE IF EXISTS node_executions;
DROP TYPE IF EXISTS node_execution_status;
```

- [ ] **Step 8: Create migration 004 — variable_snapshots**

Create `adapter/postgres/migrations/004_create_variable_snapshots.up.sql`:

```sql
CREATE TABLE variable_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    variables JSONB NOT NULL DEFAULT '{}',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_variable_snapshots_instance_captured ON variable_snapshots (instance_id, captured_at);
```

Create `adapter/postgres/migrations/004_create_variable_snapshots.down.sql`:

```sql
DROP TABLE IF EXISTS variable_snapshots;
```

- [ ] **Step 9: Create migration runner**

Install dependencies:

```bash
go get github.com/golang-migrate/migrate/v4
go get github.com/golang-migrate/migrate/v4/database/postgres
go get github.com/golang-migrate/migrate/v4/source/iofs
go get github.com/jackc/pgx/v5
go get github.com/jackc/pgx/v5/pgxpool
```

Create `adapter/postgres/migrate.go`:

```go
package postgres

import (
	"embed"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func RunMigrations(databaseURL string) error {
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("running migrations: %w", err)
	}

	return nil
}
```

- [ ] **Step 10: Commit migrations**

```bash
git add adapter/postgres/migrations/ adapter/postgres/migrate.go
git commit -m "feat: database migrations and migration runner"
```

---

### Task 3: sqlc Queries & Postgres Adapters

**Files:**
- Create: `adapter/postgres/queries/users.sql`
- Create: `adapter/postgres/queries/instances.sql`
- Create: `adapter/postgres/queries/node_executions.sql`
- Create: `adapter/postgres/queries/variable_snapshots.sql`
- Create: `adapter/postgres/sqlc.yaml`
- Create: `adapter/postgres/instance_repo.go`
- Create: `adapter/postgres/node_execution_repo.go`
- Create: `adapter/postgres/snapshot_repo.go`

- [ ] **Step 11: Create sqlc config**

Create `adapter/postgres/sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "queries/"
    schema: "migrations/"
    gen:
      go:
        package: "generated"
        out: "generated"
        sql_package: "pgx/v5"
```

- [ ] **Step 12: Create sqlc query files**

Create `adapter/postgres/queries/users.sql`:

```sql
-- name: GetDefaultUser :one
SELECT id, username, role, created_at FROM users WHERE username = 'local' LIMIT 1;
```

Create `adapter/postgres/queries/instances.sql`:

```sql
-- name: CreateInstance :one
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetInstance :one
SELECT * FROM instances WHERE id = $1;

-- name: ListInstances :many
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at
FROM instances
WHERE (sqlc.narg('state')::instance_state IS NULL OR state = sqlc.narg('state')::instance_state)
AND (sqlc.narg('workflow_name')::text IS NULL OR workflow_name = sqlc.narg('workflow_name')::text)
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: UpdateInstanceState :exec
UPDATE instances SET state = $2, updated_at = now() WHERE id = $1;

-- name: UpdateInstanceVariables :exec
UPDATE instances SET variables = $2, updated_at = now() WHERE id = $1;

-- name: UpdateInstanceCurrentNode :exec
UPDATE instances SET current_node_id = $2, updated_at = now() WHERE id = $1;

-- name: SetInstanceFailed :exec
UPDATE instances SET state = 'failed', failed_node_id = $2, updated_at = now() WHERE id = $1;

-- name: SetInstanceCompleted :exec
UPDATE instances SET state = 'completed', completed_at = now(), updated_at = now() WHERE id = $1;

-- name: ListRunningInstances :many
SELECT * FROM instances WHERE state = 'running';
```

Create `adapter/postgres/queries/node_executions.sql`:

```sql
-- name: CreateNodeExecution :one
INSERT INTO node_executions (id, instance_id, node_id, attempt, status, resolved_inputs, output, error, llm_conversation, llm_model, llm_tokens_in, llm_tokens_out, script_stdout, script_stderr, script_exit_code, started_at, completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING *;

-- name: UpdateNodeExecution :exec
UPDATE node_executions SET status = $2, output = $3, error = $4, llm_conversation = $5, llm_model = $6, llm_tokens_in = $7, llm_tokens_out = $8, script_stdout = $9, script_stderr = $10, script_exit_code = $11, completed_at = $12
WHERE id = $1;

-- name: ListNodeExecutionsByInstance :many
SELECT * FROM node_executions WHERE instance_id = $1 ORDER BY started_at;

-- name: GetNodeExecutionByInstanceAndNode :one
SELECT * FROM node_executions WHERE instance_id = $1 AND node_id = $2 ORDER BY attempt DESC LIMIT 1;
```

Create `adapter/postgres/queries/variable_snapshots.sql`:

```sql
-- name: CreateVariableSnapshot :one
INSERT INTO variable_snapshots (id, instance_id, node_id, variables)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListVariableSnapshotsByInstance :many
SELECT * FROM variable_snapshots WHERE instance_id = $1 ORDER BY captured_at;
```

- [ ] **Step 13: Install sqlc and generate code**

```bash
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
cd adapter/postgres && sqlc generate && cd ../..
```

- [ ] **Step 14: Implement Postgres instance repository adapter**

Create `adapter/postgres/instance_repo.go`. This file translates between sqlc generated types and domain types. The implementation wraps each sqlc query call, converting parameters and results. The adapter struct holds a `*generated.Queries` and a `*pgxpool.Pool` for transaction support.

Implement each method of the `InstanceRepository` interface: `Create`, `Get`, `List`, `UpdateState`, `UpdateVariables`, `UpdateCurrentNode`, `SetFailed`, `SetCompleted`, `ListRunning`.

- [ ] **Step 15: Implement Postgres node execution repository adapter**

Create `adapter/postgres/node_execution_repo.go`. Same pattern — wrap sqlc queries, translate types. Implement: `Create`, `Update`, `ListByInstance`, `GetByInstanceAndNode`.

- [ ] **Step 16: Implement Postgres variable snapshot repository adapter**

Create `adapter/postgres/snapshot_repo.go`. Implement: `Create`, `ListByInstance`.

- [ ] **Step 17: Write integration tests with testcontainers**

Install testcontainers:

```bash
go get github.com/testcontainers/testcontainers-go
go get github.com/testcontainers/testcontainers-go/modules/postgres
```

Create `adapter/postgres/integration_test.go` with tests that:
- Spin up a Postgres container
- Run migrations
- Verify default user is seeded
- Create an instance, get it back, verify fields
- Update instance state, verify change
- Create node execution records, list by instance, verify ordering
- Create variable snapshots, list by instance, verify ordering
- List running instances, verify only running ones returned
- SetFailed sets state and failed_node_id
- SetCompleted sets state and completed_at

Each test should be independent (use subtests with t.Run).

- [ ] **Step 18: Run integration tests**

```bash
go test ./adapter/postgres/ -v -tags integration -timeout 120s
```

Expected: all PASS (requires Docker running for testcontainers)

- [ ] **Step 19: Commit**

```bash
git add adapter/postgres/
git commit -m "feat: sqlc queries, Postgres adapters, and integration tests"
```
