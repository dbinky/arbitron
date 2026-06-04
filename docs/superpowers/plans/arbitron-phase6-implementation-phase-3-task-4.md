# Phase 3, Task 4: `published_workflows` Migration 006 + sqlc Queries + Repository

## Goal

Add the `published_workflows` table (migration `006`) that is the authority for the unique-name and publisher-only-overwrite rules, generate its sqlc accessors, and implement a `PublishedWorkflowRepo` that satisfies both the `workflow.PublisherLookup` port (Task 3) and the publish/copy needs (Task 5).

Source of truth: design §4 (Publisher Tracking).

## Dependencies

- **Task 3** defines `workflow.PublisherLookup` (`PublisherOf`). This task implements it.
- Independent of Tasks 1/2 at the schema level, but ordered here so Task 5 has both the table and the repo.

## Background (current state)

- Migrations live in `adapter/sqlite/migrations/NNN_*.up.sql` / `.down.sql`. Highest existing is `002_connections`. Phase 1 reserves `003` (auth) and Phase 2 likely `004`/`005`; **Phase 3 owns `006`** per the pinned decision. Use `006` regardless of intervening gaps — `migrate.go` parses the numeric prefix and applies in sorted order, skipping already-recorded versions; gaps are fine.
- `adapter/sqlite/migrate.go` `RunMigrations` embeds `migrations/*.sql`, tracks `schema_migrations(version)`, runs each `*.up.sql` once in a transaction. No code change needed — just drop the files in.
- `users` table (`001_initial_schema.up.sql`): `users(id TEXT PRIMARY KEY, username TEXT UNIQUE, role TEXT, created_at TEXT)`; default user `00000000-0000-0000-0000-000000000001` / `local` / `admin`.
- sqlc config `adapter/sqlite/sqlc.yaml`: engine sqlite, `queries: "queries/"`, `schema: "migrations/"`, generated package `generated` in `generated/`. Existing query files: `users.sql`, `instances.sql`, etc. Repos (`adapter/sqlite/user_repo.go`) wrap `r.queries` (the generated `*Queries`).
- sqlc reads **all** files in `migrations/` as the schema, so `006` participates in codegen automatically.

## Files to Create/Modify

### Create: `adapter/sqlite/migrations/006_published_workflows.up.sql`

```sql
-- Publisher tracking for the shared workspace. `name` is the published name and
-- is unique across the shared scope (enforced as PK). Authority for the
-- unique-name and publisher-only-overwrite rules; the shared/<name> folder is
-- the artifact, this row is the record.
CREATE TABLE IF NOT EXISTS published_workflows (
    name         TEXT PRIMARY KEY,
    publisher_id TEXT NOT NULL REFERENCES users(id),
    source_name  TEXT NOT NULL,
    published_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

CREATE INDEX IF NOT EXISTS idx_published_workflows_publisher
    ON published_workflows(publisher_id);
```

### Create: `adapter/sqlite/migrations/006_published_workflows.down.sql`

```sql
DROP TABLE IF EXISTS published_workflows;
```

### Create: `adapter/sqlite/queries/published_workflows.sql`

```sql
-- name: GetPublishedWorkflow :one
SELECT name, publisher_id, source_name, published_at
FROM published_workflows
WHERE name = ?;

-- name: UpsertPublishedWorkflow :exec
INSERT INTO published_workflows (name, publisher_id, source_name)
VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    publisher_id = excluded.publisher_id,
    source_name  = excluded.source_name,
    published_at = strftime('%Y-%m-%dT%H:%M:%SZ');

-- name: ListPublishedWorkflows :many
SELECT name, publisher_id, source_name, published_at
FROM published_workflows
ORDER BY name;

-- name: DeletePublishedWorkflow :exec
DELETE FROM published_workflows WHERE name = ?;
```

(Re-run `sqlc generate` in `adapter/sqlite/` to refresh `generated/`. Note: the upsert intentionally allows the publisher to change ONLY when the application layer has already authorized it — Task 5 checks `publisher_id == caller` *before* calling upsert, so a different publisher never reaches this query.)

### Create: `adapter/sqlite/published_workflow_repo.go`

```go
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Compile-time: satisfies the Task-3 publisher lookup port.
var _ workflow.PublisherLookup = (*PublishedWorkflowRepo)(nil)

type PublishedWorkflowRepo struct {
	queries *generated.Queries
}

func NewPublishedWorkflowRepo(db *sql.DB) *PublishedWorkflowRepo {
	return &PublishedWorkflowRepo{queries: generated.New(db)}
}

// PublisherOf implements workflow.PublisherLookup.
func (r *PublishedWorkflowRepo) PublisherOf(ctx context.Context, name string) (uuid.UUID, bool, error) {
	row, err := r.queries.GetPublishedWorkflow(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("get published %q: %w", name, err)
	}
	id, perr := uuid.Parse(row.PublisherID)
	if perr != nil {
		return uuid.Nil, false, fmt.Errorf("parse publisher id: %w", perr)
	}
	return id, true, nil
}

// Upsert records (or re-records) a publication. The publisher-only-overwrite
// rule is enforced by the caller (Task 5) before this is invoked.
func (r *PublishedWorkflowRepo) Upsert(ctx context.Context, name string, publisher uuid.UUID, sourceName string) error

// Delete removes the publication record (used if a publish rolls back).
func (r *PublishedWorkflowRepo) Delete(ctx context.Context, name string) error
```

Define a domain-facing `PublishedWorkflow{Name, PublisherID uuid.UUID, SourceName string, PublishedAt time.Time}` and a `Get(ctx, name) (*PublishedWorkflow, bool, error)` if Task 5 needs the full row (it needs at least `PublisherID` for the overwrite check — `PublisherOf` already covers that, so the full getter is optional). Parse the SQLite `published_at` TEXT with the project's existing RFC3339 helper if one exists; otherwise `time.Parse`.

### Modify: `cmd/arbitron/main.go`

- `publishedRepo := adaptsqlite.NewPublishedWorkflowRepo(db)` after the other repos (post-`RunMigrations`).
- Pass `publishedRepo` as the `PublisherLookup` into `workflow.NewWorkflowService(...)` (replacing the nil placeholder from Task 3) and hold it for Task 5's publish/copy wiring.

## Testing (strict TDD — five scenario categories)

Repo tests use an in-memory/temp SQLite DB with `RunMigrations` applied (follow `adapter/sqlite/*_repo_test.go` precedent for DB setup), seeding `users` rows for FK satisfaction.

- **Happy path:** `Upsert` then `PublisherOf`/`Get` round-trips name, publisher, source_name; `ListPublishedWorkflows` returns inserted rows sorted by name.
- **Validation:** `Upsert` with a `publisher_id` not present in `users` fails the FK (assert error) — confirms referential integrity.
- **Error/failure:** `PublisherOf`/`Get` of an unknown name returns `(uuid.Nil, false, nil)` (no error); `Delete` of an unknown name is a no-op (no error).
- **Boundary:** re-`Upsert` of the same `name` updates `publisher_id`/`source_name` and bumps `published_at` (assert the timestamp changed / row count stays 1) — single-row idempotency on the PK.
- **Isolation/security:** the migration creates exactly one row per published name (PK uniqueness) — two `Upsert`s with the same name never produce two rows; `migrate.go` applies `006` exactly once (run `RunMigrations` twice, assert no error and table intact).
