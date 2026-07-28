# Phase 1 — Task 5: migration 018 and link-aware instance persistence

**Goal:** Add the three link columns plus their index, and make `InstanceRepo.Create` / `Get` write and read them atomically in a single statement each.

**Architecture:** Migration `018_instance_spawn_link` adds `parent_instance_id`, `parent_node_id`, `spawn_mode` (all nullable, no FK — the `ScheduleID` precedent) and one composite index for the parent-keyed lookups. `Create` and `Get` move from the sqlc-generated `CreateInstance`/`GetInstance` calls to hand-written SQL in `instance_repo.go`, following the existing `CountActiveByWorkflowID` precedent — the link MUST ride the same INSERT, because `TriggerWithDepartment` creates *and* enqueues, so a post-insert stamp leaves a crash window in which a child could reach a terminal state with no link for the wake to match. `queries/instances.sql` is updated in lockstep so it stays the readable source of truth if `sqlc generate` is ever re-run.

**Tech Stack:** Go 1.25.5, `modernc.org/sqlite`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 5: link columns + atomic Create/Get

**Files:**
- Create: `adapter/sqlite/migrations/018_instance_spawn_link.up.sql`
- Create: `adapter/sqlite/migrations/018_instance_spawn_link.down.sql`
- Create: `adapter/sqlite/instance_spawn_rows.go`
- Modify: `adapter/sqlite/instance_repo.go:29-107` (`Create`, `Get`)
- Modify: `adapter/sqlite/queries/instances.sql:1-7` (`CreateInstance`, `GetInstance` — source-of-truth SQL)
- Modify: `adapter/sqlite/convert.go` (append two link converters)
- Test:   `adapter/sqlite/instance_spawn_link_test.go`

---

- [x] **Step 1: Write the failing test**

Create `adapter/sqlite/instance_spawn_link_test.go`:

```go
package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const spawnTestOwner = "00000000-0000-0000-0000-000000000001"

// newLinkedInstance builds a queued instance carrying the given spawn link.
// Pass a zero SpawnLink for a root run.
func newLinkedInstance(link instance.SpawnLink) *instance.Instance {
	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse(spawnTestOwner),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{"name": "wf"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
	}
	if link.Valid() {
		p := link.ParentInstanceID
		n := link.ParentNodeID
		inst.ParentInstanceID = &p
		inst.ParentNodeID = &n
		inst.SpawnMode = link.Mode
	}
	return inst
}

// ---------- migration ----------

func TestMigration018_AddsLinkColumnsAndIndex(t *testing.T) {
	db := setupTestDB(t)

	cols := map[string]bool{}
	rows, err := db.DB.Query(`PRAGMA table_info(instances)`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt any
		var pk int
		require.NoError(t, rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk))
		cols[name] = notNull == 1
	}
	for _, want := range []string{"parent_instance_id", "parent_node_id", "spawn_mode"} {
		notNull, ok := cols[want]
		require.True(t, ok, "migration 018 must add column %s", want)
		assert.False(t, notNull, "%s must be NULLABLE: every pre-existing and manually triggered run has a null link", want)
	}

	var idx int
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_instances_parent'`).Scan(&idx))
	assert.Equal(t, 1, idx, "migration 018 must create idx_instances_parent for parent-keyed lookups")
}

// ---------- success: round trip ----------

func TestInstanceRepo_SpawnLinkRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))

	link := instance.SpawnLink{
		ParentInstanceID: parent.ID,
		ParentNodeID:     "spawn-1",
		Mode:             instance.SpawnModeWaitForChild,
	}
	child := newLinkedInstance(link)
	require.NoError(t, repo.Create(ctx, child))

	got, err := repo.Get(ctx, child.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ParentInstanceID, "the link must be readable back off the child row")
	assert.Equal(t, parent.ID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeWaitForChild, got.SpawnMode)
	assert.False(t, got.IsRoot())
}

// TestInstanceRepo_LinkWrittenInTheCreatingStatement is the anti-regression
// guard for the ordering invariant: the link must exist on the row the moment
// Create returns, because TriggerWithDepartment enqueues immediately after and
// the child may reach a terminal state before any follow-up write could land.
func TestInstanceRepo_LinkWrittenInTheCreatingStatement(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))

	child := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: parent.ID,
		ParentNodeID:     "spawn-1",
		Mode:             instance.SpawnModeFireAndForget,
	})
	require.NoError(t, repo.Create(ctx, child))

	// Read the raw row, bypassing the repo, to prove no second statement was
	// needed to populate the link.
	var pid, pnode, mode any
	require.NoError(t, db.DB.QueryRow(
		`SELECT parent_instance_id, parent_node_id, spawn_mode FROM instances WHERE id = ?`,
		child.ID.String()).Scan(&pid, &pnode, &mode))
	assert.Equal(t, parent.ID.String(), pid)
	assert.Equal(t, "spawn-1", pnode)
	assert.Equal(t, "fire_and_forget", mode)
}

// ---------- edge case: null links ----------

func TestInstanceRepo_RootInstanceHasNullLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	root := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, root))

	got, err := repo.Get(ctx, root.ID)
	require.NoError(t, err)
	assert.Nil(t, got.ParentInstanceID)
	assert.Nil(t, got.ParentNodeID)
	assert.Equal(t, instance.SpawnMode(""), got.SpawnMode)
	assert.True(t, got.IsRoot(), "a run with no spawning parent must read back as a root")
}

// TestInstanceRepo_PreExistingRowReadsAsRoot: a row inserted WITHOUT the link
// columns (the shape every instance created before this feature has) must read
// back as a root rather than erroring.
func TestInstanceRepo_PreExistingRowReadsAsRoot(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, visibility, trigger_source)
		 VALUES (?, ?, 'legacy', '{}', 'completed', 'normal', '{}', '{}', 'private', 'manual')`,
		id.String(), spawnTestOwner)
	require.NoError(t, err)

	got, err := repo.Get(ctx, id)
	require.NoError(t, err)
	assert.True(t, got.IsRoot())
}

// ---------- error ----------

func TestInstanceRepo_GetMissingInstanceIsNotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	_, err := repo.Get(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

// TestInstanceRepo_CreateStillStampsEveryPreExistingField proves the
// hand-written INSERT did not silently drop a column the generated query wrote.
func TestInstanceRepo_CreateStillStampsEveryPreExistingField(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	sid := uuid.New()
	wid := uuid.New()
	inst := newLinkedInstance(instance.SpawnLink{})
	inst.WorkflowID = wid
	inst.Department = "engineering"
	inst.Visibility = instance.VisibilityDepartment
	inst.TriggerSource = instance.OriginSchedule
	inst.ScheduleID = &sid
	inst.Priority = instance.PriorityHigh
	inst.OriginalVariables = map[string]any{"seed": "v"}
	inst.Variables = map[string]any{"seed": "v"}
	require.NoError(t, repo.Create(ctx, inst))
	assert.False(t, inst.CreatedAt.IsZero(), "Create must populate CreatedAt from the RETURNING clause")
	assert.False(t, inst.UpdatedAt.IsZero(), "Create must populate UpdatedAt from the RETURNING clause")

	got, err := repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, wid, got.WorkflowID)
	assert.Equal(t, "engineering", got.Department)
	assert.Equal(t, instance.VisibilityDepartment, got.Visibility)
	assert.Equal(t, instance.OriginSchedule, got.TriggerSource)
	require.NotNil(t, got.ScheduleID)
	assert.Equal(t, sid, *got.ScheduleID)
	assert.Equal(t, instance.PriorityHigh, got.Priority)
	assert.Equal(t, map[string]any{"seed": "v"}, got.OriginalVariables)
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./adapter/sqlite/ -run 'TestMigration018|TestInstanceRepo_SpawnLink|TestInstanceRepo_LinkWritten|TestInstanceRepo_RootInstance|TestInstanceRepo_PreExisting'
```

Expected output:

```
--- FAIL: TestMigration018_AddsLinkColumnsAndIndex (0.01s)
    instance_spawn_link_test.go:...: migration 018 must add column parent_instance_id
--- FAIL: TestInstanceRepo_SpawnLinkRoundTrip (0.01s)
    instance_spawn_link_test.go:...: the link must be readable back off the child row
FAIL	github.com/arbitron/arbitron/adapter/sqlite	0.3s
```

- [x] **Step 3: Write the migration**

Create `adapter/sqlite/migrations/018_instance_spawn_link.up.sql`:

```sql
-- Workflow spawn node Phase 1: link a child run back to the parent NODE that
-- spawned it.
--
-- parent_instance_id: the spawning parent run. NULL => this is a ROOT run,
--   which is the normal state for every instance created before this feature
--   and every manually triggered run after it.
-- parent_node_id: the specific spawn node WITHIN that parent. This is what
--   makes the link ADDRESSABLE. Without it, a fire-and-forget child launched
--   from node A reaching a terminal state while the parent waits at node B
--   would wake the parent and route node B by an event unrelated to it.
-- spawn_mode: how the link was created ('fire_and_forget' | 'wait_for_child').
--   Stored on the LINK rather than re-derived from the parent's workflow
--   snapshot so the resumption short-circuit and the wake match can key on it
--   without parsing every parent's snapshot.
--
-- REFERENTIAL INTEGRITY: there is deliberately NO FOREIGN KEY on
-- parent_instance_id. This follows the existing, documented precedent for
-- schedule_id (migration 014): a referenced row may be removed while
-- historical runs remain, and a purged parent must NOT cascade-delete a
-- child's execution history. A dangling parent id reads back as a link whose
-- parent no longer exists; read paths already treat that as a root-ish orphan
-- rather than a bug.
--
-- The spawn_mode CHECK is deliberately permissive of NULL so the columns stay
-- additive for every existing row.
ALTER TABLE instances ADD COLUMN parent_instance_id TEXT;  -- NULL => root run
ALTER TABLE instances ADD COLUMN parent_node_id     TEXT;  -- NULL => root run
ALTER TABLE instances ADD COLUMN spawn_mode         TEXT
    CHECK (spawn_mode IS NULL OR spawn_mode IN ('fire_and_forget','wait_for_child'));

-- One composite index serves every parent-keyed read this feature adds:
--   * children of a parent (hierarchy expand, cancel cascade)
--   * the awaited child of (parent, node, mode) — the resumption short-circuit
--     and the reconciler join
-- The leading column also makes the roots-only predicate
-- (parent_instance_id IS NULL) index-eligible. Portable to Postgres.
CREATE INDEX IF NOT EXISTS idx_instances_parent
    ON instances (parent_instance_id, parent_node_id, spawn_mode);
```

Create `adapter/sqlite/migrations/018_instance_spawn_link.down.sql`:

```sql
DROP INDEX IF EXISTS idx_instances_parent;
-- SQLite cannot DROP COLUMN portably on the older versions some deployments
-- run, and a table rebuild is out of scope for a down step. Forward-only column
-- removal is the project convention — see the 012 / 014 / 017 down migrations
-- and migrations/README.md.
```

- [x] **Step 4: Add the link converters**

Append to `adapter/sqlite/convert.go`:

```go
// parentIDToNullString maps a nilable parent instance UUID to a nullable SQL
// string: nil (a root run) => SQL NULL; a value => its canonical UUID string.
// It is the inverse of nullStringToParentID.
func parentIDToNullString(id *uuid.UUID) sql.NullString {
	if id == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: id.String(), Valid: true}
}

// nullStringToParentID maps a nullable parent_instance_id column back to
// *uuid.UUID: NULL or empty => nil (a root run). A deleted parent's id is still
// a valid UUID string, so this round-trips a dangling link too — there is no FK
// on the column by design.
func nullStringToParentID(ns sql.NullString) (*uuid.UUID, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	id, err := uuid.Parse(ns.String)
	if err != nil {
		return nil, fmt.Errorf("parsing parent_instance_id: %w", err)
	}
	return &id, nil
}
```

- [x] **Step 5: Add the shared column list and link scanner**

Create `adapter/sqlite/instance_spawn_rows.go`:

```go
package sqlite

import (
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
)

// instanceColumnsWithLink is the explicit, ordered column list for every
// full-row instance read that must also carry the spawn link. It is written out
// (never `SELECT *`) so the scan order below is pinned by this file rather than
// by the physical column order of the table.
//
// The first 23 columns are exactly generated.Instance's field order, so a row
// scanned with this list can be assembled into a generated.Instance and handed
// to the existing rowToInstance converter — the three link columns are the only
// thing scanned separately. That keeps ONE conversion routine for instances.
const instanceColumnsWithLink = `id, owner_id, workflow_name, workflow_snapshot, state, priority, ` +
	`variables, original_variables, current_node_id, failed_node_id, created_at, updated_at, ` +
	`completed_at, queued_at, waiting_until, webhook_token, webhook_secret_hash, ` +
	`webhook_received_at, department, visibility, trigger_source, schedule_id, workflow_id, ` +
	`parent_instance_id, parent_node_id, spawn_mode`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanInstanceWithLink reads one row selected with instanceColumnsWithLink and
// converts it to a domain Instance, including the parent/child spawn link.
func scanInstanceWithLink(s rowScanner) (*instance.Instance, error) {
	var row generated.Instance
	var parentID, parentNode, spawnMode sql.NullString
	if err := s.Scan(
		&row.ID,
		&row.OwnerID,
		&row.WorkflowName,
		&row.WorkflowSnapshot,
		&row.State,
		&row.Priority,
		&row.Variables,
		&row.OriginalVariables,
		&row.CurrentNodeID,
		&row.FailedNodeID,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.CompletedAt,
		&row.QueuedAt,
		&row.WaitingUntil,
		&row.WebhookToken,
		&row.WebhookSecretHash,
		&row.WebhookReceivedAt,
		&row.Department,
		&row.Visibility,
		&row.TriggerSource,
		&row.ScheduleID,
		&row.WorkflowID,
		&parentID,
		&parentNode,
		&spawnMode,
	); err != nil {
		return nil, err
	}

	inst, err := rowToInstance(row)
	if err != nil {
		return nil, err
	}

	pid, err := nullStringToParentID(parentID)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	inst.ParentInstanceID = pid
	inst.ParentNodeID = nullStringToPtr(parentNode)
	inst.SpawnMode = instance.SpawnMode(nullStringToString(spawnMode))
	return inst, nil
}
```

- [x] **Step 6: Rewrite `Create` and `Get` to carry the link**

In `adapter/sqlite/instance_repo.go`, replace the `r.queries.CreateInstance(...)` call and its
result handling (lines 59-93) with a hand-written INSERT. The full method becomes:

```go
// createInstanceSQL writes the row and the spawn link in ONE statement.
//
// The link MUST ride this INSERT rather than a follow-up UPDATE: the trigger
// path creates AND enqueues (Engine.TriggerWithDepartment), so control returns
// to the caller only after the child may already be running. A child that
// reached a terminal state before a follow-up link write landed would leave its
// waiting parent with nothing to match, and the parent would hang forever.
//
// This is hand-written rather than sqlc-generated for the same reason
// CountActiveByWorkflowID is: it lets the column set evolve without a
// regeneration step. queries/instances.sql carries the matching source-of-truth
// SQL.
const createInstanceSQL = `INSERT INTO instances (
	id, owner_id, workflow_name, workflow_snapshot, state, priority,
	variables, original_variables, queued_at, department, visibility,
	trigger_source, schedule_id, workflow_id,
	parent_instance_id, parent_node_id, spawn_mode
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING created_at, updated_at`

func (r *InstanceRepo) Create(ctx context.Context, inst *instance.Instance) error {
	snapshotJSON, err := toJSONString(inst.WorkflowSnapshot)
	if err != nil {
		return fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	varsJSON, err := toJSONString(inst.Variables)
	if err != nil {
		return fmt.Errorf("marshalling variables: %w", err)
	}
	origVarsJSON, err := toJSONString(inst.OriginalVariables)
	if err != nil {
		return fmt.Errorf("marshalling original variables: %w", err)
	}

	// Default an unset visibility to private (owner-only), matching the column
	// default and the pre-Phase-3 behavior of instances created without an
	// explicit audience.
	visibility := inst.Visibility
	if visibility == "" {
		visibility = instance.VisibilityPrivate
	}

	// Default an unset trigger source to manual, matching the column default and
	// the engine's OriginManual default. ScheduleID stays nil (NULL) for manual.
	triggerSource := inst.TriggerSource
	if triggerSource == "" {
		triggerSource = instance.OriginManual
	}

	// A PARTIAL link is never written: a row with a parent id but no node id
	// could not be matched by the wake and would strand its parent forever. All
	// three columns are written together or all three stay NULL.
	var parentID, parentNode, spawnMode sql.NullString
	if inst.ParentInstanceID != nil && inst.ParentNodeID != nil && inst.SpawnMode.Valid() {
		parentID = parentIDToNullString(inst.ParentInstanceID)
		parentNode = ptrToNullString(inst.ParentNodeID)
		spawnMode = sql.NullString{String: string(inst.SpawnMode), Valid: true}
	}

	var createdAtRaw, updatedAtRaw string
	err = r.db.QueryRowContext(ctx, createInstanceSQL,
		uuidToString(inst.ID),
		uuidToString(inst.OwnerID),
		inst.WorkflowName,
		snapshotJSON,
		string(inst.State),
		string(inst.Priority),
		varsJSON,
		origVarsJSON,
		timeToNullString(inst.QueuedAt),
		deptToNullString(inst.Department),
		string(visibility),
		string(triggerSource),
		scheduleIDToNullString(inst.ScheduleID),
		// Stamp the stable workflow_id (migration 017) in the SAME INSERT: there
		// is no crash window in which the row exists with workflow_id=NULL, which
		// would make a live run invisible to the delete-while-running guard.
		workflowIDToNullString(inst.WorkflowID),
		parentID,
		parentNode,
		spawnMode,
	).Scan(&createdAtRaw, &updatedAtRaw)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	createdAt, err := parseTime(createdAtRaw)
	if err != nil {
		return fmt.Errorf("parsing created_at: %w", err)
	}
	updatedAt, err := parseTime(updatedAtRaw)
	if err != nil {
		return fmt.Errorf("parsing updated_at: %w", err)
	}
	inst.CreatedAt = createdAt
	inst.UpdatedAt = updatedAt
	return nil
}
```

Replace `Get` (lines 95-107) with:

```go
func (r *InstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+instanceColumnsWithLink+` FROM instances WHERE id = ?`, uuidToString(id))
	inst, err := scanInstanceWithLink(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
		}
		return nil, fmt.Errorf("getting instance: %w", err)
	}
	return inst, nil
}
```

The `generated` import stays in use (`rowToInstance`, the other repo methods), so no import changes are needed beyond confirming `database/sql` and `errors` are already imported — they are.

- [x] **Step 7: Keep `queries/instances.sql` in lockstep**

Replace lines 1-7 of `adapter/sqlite/queries/instances.sql`:

```sql
-- name: CreateInstance :one
-- NOTE: InstanceRepo.Create executes this statement HAND-WRITTEN (see
-- createInstanceSQL in instance_repo.go) so the column set can evolve without a
-- sqlc regeneration step, the same way CountActiveByWorkflowID does. This
-- definition is kept in lockstep as the readable source of truth.
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at, department, visibility, trigger_source, schedule_id, workflow_id, parent_instance_id, parent_node_id, spawn_mode)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetInstance :one
-- NOTE: executed hand-written by InstanceRepo.Get (instanceColumnsWithLink) so
-- the spawn-link columns are read in the same statement.
SELECT * FROM instances WHERE id = ?;
```

- [x] **Step 8: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w adapter/sqlite/
go test ./adapter/sqlite/ -run 'TestMigration018|TestInstanceRepo_SpawnLink|TestInstanceRepo_LinkWritten|TestInstanceRepo_RootInstance|TestInstanceRepo_PreExisting|TestInstanceRepo_GetMissing|TestInstanceRepo_CreateStill' -v
```

Expected output:

```
--- PASS: TestMigration018_AddsLinkColumnsAndIndex (0.01s)
--- PASS: TestInstanceRepo_SpawnLinkRoundTrip (0.01s)
--- PASS: TestInstanceRepo_LinkWrittenInTheCreatingStatement (0.01s)
--- PASS: TestInstanceRepo_RootInstanceHasNullLink (0.01s)
--- PASS: TestInstanceRepo_PreExistingRowReadsAsRoot (0.01s)
--- PASS: TestInstanceRepo_GetMissingInstanceIsNotFound (0.01s)
--- PASS: TestInstanceRepo_CreateStillStampsEveryPreExistingField (0.01s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.6s
```

- [x] **Step 9: Confirm the whole sqlite suite still passes (Create/Get are hot paths)**

```bash
go test ./adapter/sqlite/
```

Expected: `ok  	github.com/arbitron/arbitron/adapter/sqlite`

- [x] **Step 10: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add adapter/sqlite/migrations/018_instance_spawn_link.up.sql adapter/sqlite/migrations/018_instance_spawn_link.down.sql adapter/sqlite/instance_spawn_rows.go adapter/sqlite/instance_repo.go adapter/sqlite/convert.go adapter/sqlite/queries/instances.sql adapter/sqlite/instance_spawn_link_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(sqlite): add spawn link columns and write them in the creating statement"
```
