# Workflow Management — Phase 1, Task 1: Workflow identity + registry foundation

**Goal:** Give every workflow a stable UUID and a global-uniqueness-enforcing
name↔id registry, without changing any caller behavior yet.

**Architecture:** Add an `ID` to the `workflow.Workflow` domain type (persisted in
`workflow.json`). Introduce a `WorkflowRegistry` port (`domain/workflow`) with a
SQLite adapter backed by a new `workflow_identity` table whose `UNIQUE(name)` is the
authoritative global-uniqueness rule and whose rows are the name↔id resolver. The
filesystem definition store stays name-keyed and unchanged.

**Tech Stack:** Go, hexagonal (domain ports / adapter impls), SQLite via sqlc +
golang-migrate, `github.com/google/uuid`, testify.

---

## Files
- Modify: `domain/workflow/workflow.go` (add `ID` field + JSON tag)
- Create: `domain/workflow/registry.go` (the `WorkflowRegistry` port + `RegistryEntry` type)
- Create: `adapter/sqlite/migrations/01X_workflow_identity.up.sql` / `.down.sql`
- Create: `adapter/sqlite/queries/workflow_identity.sql` (sqlc queries)
- Create: `adapter/sqlite/workflow_identity_repo.go` (port impl)
- Test:   `adapter/sqlite/workflow_identity_repo_test.go`

## Steps

- [ ] **Step 1 — Failing test: the registry port round-trips and enforces unique names.**
  In `workflow_identity_repo_test.go`, against a migrated in-memory SQLite DB
  (reuse the existing test-DB helper used by other `adapter/sqlite/*_test.go`),
  write tests:
  - `Register(entry)` then `ResolveID(name)` returns the entry's id; `ResolveName(id)`
    returns the name.
  - `Register` a second entry with the **same name** returns `workflow.ErrAlreadyExists`.
  - `ResolveID("missing")` returns `workflow.ErrNotFound`.
  Run: `go test ./adapter/sqlite/ -run WorkflowIdentity` → expect FAIL (no impl).

- [ ] **Step 2 — Migration.** Create `01X_workflow_identity.up.sql`:
  ```sql
  CREATE TABLE workflow_identity (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    scope      TEXT NOT NULL,           -- 'private' | 'shared'
    owner_id   TEXT,                    -- nullable for shared
    archived   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  CREATE INDEX idx_workflow_identity_owner ON workflow_identity(owner_id);
  ```
  `.down.sql` drops the table. Confirm migration runs in the test harness.

- [ ] **Step 3 — sqlc queries** in `queries/workflow_identity.sql`: `RegisterWorkflow`
  (INSERT), `ResolveWorkflowID` (SELECT by name), `ResolveWorkflowName` (SELECT by id),
  `ListWorkflowIdentities`, `RenameWorkflow` (UPDATE name WHERE id), `SetArchived`
  (UPDATE archived WHERE id), `SetOwner` (UPDATE owner_id WHERE id),
  `DeleteWorkflowIdentity` (DELETE by id). Run `make sqlc` (or the project's sqlc-gen
  step) and confirm generated code compiles.

- [ ] **Step 4 — Port** in `domain/workflow/registry.go`:
  ```go
  type RegistryEntry struct {
      ID       uuid.UUID
      Name     string
      Scope    ScopeKind
      OwnerID  uuid.UUID // zero for shared
      Archived bool
  }
  type WorkflowRegistry interface {
      Register(ctx context.Context, e RegistryEntry) error // ErrAlreadyExists on dup name
      ResolveID(ctx context.Context, name string) (uuid.UUID, error)
      ResolveName(ctx context.Context, id uuid.UUID) (string, error)
      Rename(ctx context.Context, id uuid.UUID, newName string) error // ErrAlreadyExists on dup
      SetArchived(ctx context.Context, id uuid.UUID, archived bool) error
      SetOwner(ctx context.Context, id, owner uuid.UUID) error
      Delete(ctx context.Context, id uuid.UUID) error
      List(ctx context.Context) ([]RegistryEntry, error)
  }
  ```

- [ ] **Step 5 — Adapter** `workflow_identity_repo.go`: implement the port over the sqlc
  queries; map a UNIQUE-constraint violation (SQLite error 2067 / `constraint failed:
  workflow_identity.name`) to `workflow.ErrAlreadyExists`, and `sql.ErrNoRows` to
  `workflow.ErrNotFound`. Run Step 1's tests → expect PASS.

- [ ] **Step 6 — Add `ID` to the domain type.** In `workflow.go`, add
  `ID uuid.UUID \`json:"id,omitempty"\`` to `Workflow`. Add a test asserting a
  `Workflow` round-trips its `ID` through JSON marshal/unmarshal.

- [ ] **Step 7 — Edge tests:** concurrent `Register` of the same name (two goroutines)
  → exactly one succeeds, the other gets `ErrAlreadyExists`; `Rename` to an existing
  name → `ErrAlreadyExists`. Run `go test ./adapter/sqlite/ ./domain/workflow/` → PASS.

- [ ] **Step 8 — Commit:** `git add -A && git commit -m "feat(workflow): add stable id + name-uniqueness registry"`
