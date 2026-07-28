# Phase 1 — Task 6: parent-scoped instance listing

**Goal:** Make `InstanceRepo.List` honour the three-state `ParentScope` narrowing and carry `ParentInstanceID` on every returned summary, without weakening the service-set RBAC disjunction.

**Architecture:** `List` moves from the sqlc-generated `ListInstances` to a hand-built statement in `adapter/sqlite/instance_repo.go`. The RBAC disjunction is preserved clause-for-clause (owner OR public OR department-membership, with "no narrowing at all" meaning admin), and the parent predicate is appended as an additional `AND` — so it can only ever *narrow* the authorized set, never widen it. `queries/instances.sql` is updated in lockstep as the readable source of truth.

**Tech Stack:** Go 1.25.5, `modernc.org/sqlite`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 6: parent scope on the list path

**Files:**
- Modify: `adapter/sqlite/instance_repo.go:109-221` (`List`)
- Modify: `adapter/sqlite/instance_spawn_rows.go` (append the summary column list)
- Modify: `adapter/sqlite/queries/instances.sql:9-33` (`ListInstances` — source-of-truth SQL)
- Test:   `adapter/sqlite/instance_repo_parent_scope_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `adapter/sqlite/instance_repo_parent_scope_test.go`:

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

// parentScopeFixture creates one root with two children and one unrelated root.
// It returns (rootA, childA1, childA2, rootB).
func parentScopeFixture(t *testing.T, repo *InstanceRepo) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	rootA := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, rootA))
	rootB := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, rootB))

	c1 := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: rootA.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeWaitForChild,
	})
	require.NoError(t, repo.Create(ctx, c1))
	c2 := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: rootA.ID, ParentNodeID: "spawn-2", Mode: instance.SpawnModeFireAndForget,
	})
	require.NoError(t, repo.Create(ctx, c2))

	return rootA.ID, c1.ID, c2.ID, rootB.ID
}

func idsOf(summaries []instance.InstanceSummary) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, s := range summaries {
		out[s.ID] = true
	}
	return out
}

// ---------- happy path ----------

func TestList_ParentScopeRoots_ReturnsOnlyRoots(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, c2, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeRoots, Limit: 100,
	})
	require.NoError(t, err)
	ids := idsOf(got)
	assert.True(t, ids[rootA], "rootA must be listed")
	assert.True(t, ids[rootB], "rootB must be listed")
	assert.False(t, ids[c1], "a child must not appear in the roots-only list")
	assert.False(t, ids[c2], "a child must not appear in the roots-only list")
}

func TestList_ParentScopeChildren_ReturnsOnlyThatParentsChildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, c2, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, ParentInstanceID: &rootA, Limit: 100,
	})
	require.NoError(t, err)
	ids := idsOf(got)
	assert.True(t, ids[c1])
	assert.True(t, ids[c2])
	assert.False(t, ids[rootA], "the parent itself must not be in its own child list")
	assert.False(t, ids[rootB])
	assert.Len(t, got, 2)
}

func TestList_ParentScopeAny_ReturnsEverything(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, c2, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	ids := idsOf(got)
	for _, id := range []uuid.UUID{rootA, c1, c2, rootB} {
		assert.True(t, ids[id], "unset parent scope must do no narrowing (missing %s)", id)
	}
}

// ---------- success: summary carries the link ----------

func TestList_SummaryCarriesParentInstanceID(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, _, _ := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	for _, s := range got {
		switch s.ID {
		case c1:
			require.NotNil(t, s.ParentInstanceID, "a child summary must carry its parent id")
			assert.Equal(t, rootA, *s.ParentInstanceID)
		case rootA:
			assert.Nil(t, s.ParentInstanceID, "a root summary must carry a nil parent id")
		}
	}
}

// ---------- edge cases ----------

// The parent selector is a NARROWING and must compose with — never override —
// the service-set owner constraint. A caller scoped to owner X asking for the
// children of a run owned by Y sees nothing.
func TestList_ParentScopeCannotOverrideOwnerConstraint(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	rootA, _, _, _ := parentScopeFixture(t, repo)

	otherOwner := uuid.New()
	got, err := repo.List(ctx, instance.ListFilters{
		OwnerID:          &otherOwner, // service-set: a different user
		ParentScope:      instance.ParentScopeChildren,
		ParentInstanceID: &rootA,
		Limit:            100,
	})
	require.NoError(t, err)
	assert.Empty(t, got, "a parent selector must not widen past the service-set owner constraint")
}

// A parent scope of "children" with no parent id is a caller bug; return an
// empty page rather than silently degrading to an unfiltered list.
func TestList_ParentScopeChildrenWithNoParentID_ReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, Limit: 100,
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestList_ParentWithNoChildren_ReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	_, _, _, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, ParentInstanceID: &rootB, Limit: 100,
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestList_UnknownParentID_ReturnsEmptyNotError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parentScopeFixture(t, repo)

	unknown := uuid.New()
	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, ParentInstanceID: &unknown, Limit: 100,
	})
	require.NoError(t, err, "naming a run the caller cannot see must not leak via an error")
	assert.Empty(t, got)
}

// Existing behaviour must be preserved by the rewrite: state, workflowName,
// since, limit/offset, and the department disjunction.
func TestList_ExistingFiltersStillApplyAfterRewrite(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	a := newLinkedInstance(instance.SpawnLink{})
	a.WorkflowName = "alpha"
	require.NoError(t, repo.Create(ctx, a))

	b := newLinkedInstance(instance.SpawnLink{})
	b.WorkflowName = "beta"
	b.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, b))

	name := "alpha"
	got, err := repo.List(ctx, instance.ListFilters{WorkflowName: &name, Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, a.ID, got[0].ID)

	st := instance.StateCompleted
	got, err = repo.List(ctx, instance.ListFilters{State: &st, Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, b.ID, got[0].ID)

	got, err = repo.List(ctx, instance.ListFilters{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, got, 1, "limit must still apply")
}

func TestList_DepartmentDisjunctionStillApplies(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	dept := newLinkedInstance(instance.SpawnLink{})
	dept.Visibility = instance.VisibilityDepartment
	dept.Department = "engineering"
	require.NoError(t, repo.Create(ctx, dept))

	priv := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, priv))

	stranger := uuid.New()
	got, err := repo.List(ctx, instance.ListFilters{
		OwnerID: &stranger, Departments: []string{"engineering"}, Limit: 100,
	})
	require.NoError(t, err)
	ids := idsOf(got)
	assert.True(t, ids[dept.ID], "a department-visible run must be listed for a member")
	assert.False(t, ids[priv.ID], "a private run of another owner must not be listed")
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./adapter/sqlite/ -run 'TestList_Parent|TestList_Summary|TestList_Unknown|TestList_Existing|TestList_Department'
```

Expected output:

```
--- FAIL: TestList_ParentScopeRoots_ReturnsOnlyRoots (0.01s)
    instance_repo_parent_scope_test.go:...: a child must not appear in the roots-only list
--- FAIL: TestList_SummaryCarriesParentInstanceID (0.01s)
    instance_repo_parent_scope_test.go:...: a child summary must carry its parent id
FAIL	github.com/arbitron/arbitron/adapter/sqlite	0.4s
```

- [ ] **Step 3: Add the summary column list**

Append to `adapter/sqlite/instance_spawn_rows.go`:

```go
// instanceSummaryColumns is the explicit column list for the LEAN list
// projection. It is the column set the generated ListInstances query selected,
// plus parent_instance_id so the hierarchy read needs no per-row load. Full
// blobs (original_variables, webhook fields) stay out of the page on purpose.
const instanceSummaryColumns = `id, owner_id, workflow_name, state, priority, current_node_id, ` +
	`failed_node_id, variables, workflow_snapshot, created_at, updated_at, completed_at, ` +
	`queued_at, department, visibility, trigger_source, schedule_id, parent_instance_id`
```

- [ ] **Step 4: Rewrite `List`**

Replace the whole `List` method in `adapter/sqlite/instance_repo.go` (lines 109-221) with:

```go
// List returns a page of instance summaries.
//
// The statement is assembled here rather than generated so the parent
// narrowing can be appended without a sqlc regeneration step
// (queries/instances.sql carries the matching source-of-truth SQL).
//
// SECURITY — the disjunctive RBAC visibility filter is preserved clause for
// clause from the query this replaces: a caller sees a row when it is owned by
// them, OR public (when IncludePublic), OR a department run in one of their
// departments. "No narrowing supplied at all" means an ADMIN list and admits
// every row; that is exactly the `unconstrained` arm the generated query
// guarded. The parent predicate is appended as a further AND, so it can only
// ever NARROW the authorized set — never widen it.
func (r *InstanceRepo) List(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	if filters.Limit <= 0 {
		filters.Limit = 50
	}

	var where []string
	var args []any

	if filters.State != nil {
		where = append(where, "state = ?")
		args = append(args, string(*filters.State))
	}
	if filters.WorkflowName != nil {
		where = append(where, "workflow_name = ?")
		args = append(args, *filters.WorkflowName)
	}
	if filters.Since != nil {
		where = append(where, "created_at >= ?")
		args = append(args, timeToString(*filters.Since))
	}

	// Admin lists arrive with no owner/department/public narrowing at all; in
	// that case no visibility clause is emitted and every row is admitted.
	unconstrained := filters.OwnerID == nil && len(filters.Departments) == 0 && !filters.IncludePublic
	if !unconstrained {
		var ors []string
		if filters.OwnerID != nil {
			ors = append(ors, "owner_id = ?")
			args = append(args, uuidToString(*filters.OwnerID))
		}
		if filters.IncludePublic {
			ors = append(ors, "visibility = 'public'")
		}
		if len(filters.Departments) > 0 {
			b, err := json.Marshal(filters.Departments)
			if err != nil {
				return nil, fmt.Errorf("marshalling departments filter: %w", err)
			}
			// json_each turns the JSON array into a membership test. Portable to
			// Postgres as `instances.department = ANY($departments)`.
			ors = append(ors, "(visibility = 'department' AND department IN (SELECT value FROM json_each(?)))")
			args = append(args, string(b))
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	switch filters.ParentScope {
	case instance.ParentScopeRoots:
		where = append(where, "parent_instance_id IS NULL")
	case instance.ParentScopeChildren:
		if filters.ParentInstanceID == nil {
			// A children scope with no parent id is a caller bug. Return an empty
			// page rather than silently degrading to an unfiltered list.
			return []instance.InstanceSummary{}, nil
		}
		where = append(where, "parent_instance_id = ?")
		args = append(args, uuidToString(*filters.ParentInstanceID))
	}

	q := "SELECT " + instanceSummaryColumns + " FROM instances"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, int64(filters.Limit), int64(filters.Offset))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	defer rows.Close()

	summaries := []instance.InstanceSummary{}
	for rows.Next() {
		var (
			idRaw, ownerRaw, workflowName, state, priority string
			currentNodeID, failedNodeID                    sql.NullString
			variablesRaw, snapshotRaw                      string
			createdAtRaw, updatedAtRaw                     string
			completedAtRaw, queuedAtRaw                    sql.NullString
			department                                     sql.NullString
			visibility, triggerSource                      string
			scheduleIDRaw, parentIDRaw                     sql.NullString
		)
		if err := rows.Scan(
			&idRaw, &ownerRaw, &workflowName, &state, &priority, &currentNodeID,
			&failedNodeID, &variablesRaw, &snapshotRaw, &createdAtRaw, &updatedAtRaw,
			&completedAtRaw, &queuedAtRaw, &department, &visibility, &triggerSource,
			&scheduleIDRaw, &parentIDRaw,
		); err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}

		id, err := parseUUID(idRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		ownerID, err := parseUUID(ownerRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		createdAt, err := parseTime(createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		updatedAt, err := parseTime(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		completedAt, err := nullStringToTimePtr(completedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		queuedAt, err := nullStringToTimePtr(queuedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		scheduleID, err := nullStringToScheduleID(scheduleIDRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		parentID, err := nullStringToParentID(parentIDRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		variables, err := fromJSONString(variablesRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: unmarshalling variables: %w", err)
		}
		snapshot, err := fromJSONString(snapshotRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: unmarshalling workflow snapshot: %w", err)
		}

		summaries = append(summaries, instance.InstanceSummary{
			ID:               id,
			OwnerID:          ownerID,
			WorkflowName:     workflowName,
			State:            instance.State(state),
			Priority:         instance.Priority(priority),
			Department:       nullStringToString(department),
			Visibility:       instance.Visibility(visibility),
			CurrentNodeID:    nullStringToPtr(currentNodeID),
			FailedNodeID:     nullStringToPtr(failedNodeID),
			Variables:        variables,
			WorkflowSnapshot: snapshot,
			CreatedAt:        createdAt,
			UpdatedAt:        updatedAt,
			CompletedAt:      completedAt,
			QueuedAt:         queuedAt,
			TriggerSource:    instance.TriggerSource(triggerSource),
			ScheduleID:       scheduleID,
			ParentInstanceID: parentID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	return summaries, nil
}
```

Add `strings` to the import block of `adapter/sqlite/instance_repo.go`:

```go
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)
```

- [ ] **Step 5: Keep `queries/instances.sql` in lockstep**

Replace the `-- name: ListInstances :many` block (lines 9-33) of `adapter/sqlite/queries/instances.sql` with:

```sql
-- name: ListInstances :many
-- NOTE: InstanceRepo.List assembles this statement in Go so the optional parent
-- narrowing can be appended without a sqlc regeneration step. This definition
-- is kept in lockstep as the readable source of truth for the RBAC clause.
--
-- Disjunctive RBAC visibility filter: a caller sees a row when it is owned by
-- them, OR public (when include_public), OR a department run in one of their
-- departments. An admin passes owner_id=NULL, include_public=0, and an empty
-- departments array, in which case the guarded `unconstrained` arm admits every
-- row.
--
-- The parent narrowing (parent_instance_id IS NULL for roots-only, or
-- parent_instance_id = ? for a specific parent) is appended as a further AND by
-- the Go builder, so it can only NARROW the authorized set, never widen it.
SELECT id, owner_id, workflow_name, state, priority, current_node_id, failed_node_id, variables, workflow_snapshot, created_at, updated_at, completed_at, queued_at, department, visibility, trigger_source, schedule_id, parent_instance_id
FROM instances
WHERE (sqlc.narg('state') IS NULL OR state = sqlc.narg('state'))
AND (sqlc.narg('workflow_name') IS NULL OR workflow_name = sqlc.narg('workflow_name'))
AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
AND (
    sqlc.arg('unconstrained') = 1
    OR (sqlc.narg('owner_id') IS NOT NULL AND owner_id = sqlc.narg('owner_id'))
    OR (sqlc.arg('include_public') = 1 AND visibility = 'public')
    OR (visibility = 'department' AND department IN (
        SELECT value FROM json_each(sqlc.arg('departments'))
    ))
)
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');
```

- [ ] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w adapter/sqlite/
go test ./adapter/sqlite/ -run 'TestList_' -v
```

Expected output:

```
--- PASS: TestList_ParentScopeRoots_ReturnsOnlyRoots (0.01s)
--- PASS: TestList_ParentScopeChildren_ReturnsOnlyThatParentsChildren (0.01s)
--- PASS: TestList_ParentScopeAny_ReturnsEverything (0.01s)
--- PASS: TestList_SummaryCarriesParentInstanceID (0.01s)
--- PASS: TestList_ParentScopeCannotOverrideOwnerConstraint (0.01s)
--- PASS: TestList_ParentScopeChildrenWithNoParentID_ReturnsEmpty (0.01s)
--- PASS: TestList_ParentWithNoChildren_ReturnsEmpty (0.01s)
--- PASS: TestList_UnknownParentID_ReturnsEmptyNotError (0.01s)
--- PASS: TestList_ExistingFiltersStillApplyAfterRewrite (0.01s)
--- PASS: TestList_DepartmentDisjunctionStillApplies (0.01s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.6s
```

- [ ] **Step 7: Run the existing visibility regression suites (they cover the clause just rewritten)**

```bash
go test ./adapter/sqlite/ ./adapter/http/ ./adapter/mcp/ ./domain/engine/
```

Expected: `ok` for all four packages — in particular
`adapter/sqlite/instance_repo_list_visibility_test.go` and
`adapter/sqlite/instance_repo_visibility_test.go` must still pass unchanged.

- [ ] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add adapter/sqlite/instance_repo.go adapter/sqlite/instance_spawn_rows.go adapter/sqlite/queries/instances.sql adapter/sqlite/instance_repo_parent_scope_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(sqlite): parent-scoped instance listing carrying the parent id on summaries"
```
