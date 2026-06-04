# RBAC Phase 3 — Task 3: ListInstances Disjunctive Visibility Authorization

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the owner-only list filter with a disjunctive visibility filter — a non-admin sees instances where `owner_id = self` OR `visibility = public` OR (`visibility = department` AND `department ∈ my departments`) — built service-side from the caller's identity, never from request input.

**Architecture:** `instance.ListFilters` gains `Departments []string` + `IncludePublic bool` (service-set, same security posture as the existing service-set `OwnerID`). The engine's list authz (`domain/engine/authz.go`) computes a `listConstraint` from the identity: admin => unconstrained; non-admin => owner + public + my-departments. The sqlc `ListInstances` query translates this into an index-backed disjunctive `WHERE` using the `idx_instances_dept_vis` index from Task 1. The SQLite repo maps the slice of departments into the query (sqlc cannot template a dynamic `IN`, so the query uses a JSON-array param matched with `json_each`, keeping a single prepared statement — Postgres-portable to `= ANY($1)`).

**Tech Stack:** Go 1.23+, SQLite (`json_each`), sqlc v2, testify.

**Cross-phase dependencies:**
- **Phase 1:** `identity.Identity.Departments`.
- **Phase 3 Task 1:** the `instances.department`/`instances.visibility` columns + `idx_instances_dept_vis`.

**Security invariant (preserved + extended):** `ListInstances` overwrites any caller-supplied `OwnerID`/`Departments`/`IncludePublic` with service-derived values, so a non-admin can never widen scope via request input. Missing identity => `ErrNotFound`, repo never queried (unchanged).

---

### Task 3.1: Extend `ListFilters` (service-set visibility fields)

**Files:**
- Modify: `domain/instance/instance.go` (`ListFilters`)
- Test: `domain/instance/instance_visibility_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `domain/instance/instance_visibility_test.go`:

```go
func TestListFilters_HasVisibilityFields(t *testing.T) {
	f := instance.ListFilters{
		Departments:   []string{"engineering", "platform"},
		IncludePublic: true,
	}
	assert.Equal(t, []string{"engineering", "platform"}, f.Departments)
	assert.True(t, f.IncludePublic)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/instance/ -run TestListFilters_HasVisibilityFields -v`
Expected: FAIL — `unknown field Departments`.

- [ ] **Step 3: Add the fields to `ListFilters`**

In `domain/instance/instance.go`, in the `ListFilters` struct, add after the `OwnerID *uuid.UUID` block (line 137):

```go
	// Departments and IncludePublic widen a non-admin's list to the disjunction
	// owner_id=self OR visibility=public (when IncludePublic) OR
	// (visibility=department AND department IN Departments).
	// SECURITY: like OwnerID above, these are SERVICE-SET by ListInstances from
	// the request identity; list callers (HTTP/MCP) MUST NOT set them from
	// request input, or a non-admin could widen scope to other departments.
	Departments   []string
	IncludePublic bool
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./domain/instance/ -run TestListFilters_HasVisibilityFields -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/instance/instance.go domain/instance/instance_visibility_test.go
git commit -m "feat(rbac): add service-set Departments/IncludePublic to instance.ListFilters"
```

---

### Task 3.2: `listConstraint` in authz + rewrite `ListInstances`

**Files:**
- Modify: `domain/engine/authz.go` (add `listConstraint`)
- Modify: `domain/engine/engine.go` (`ListInstances`)
- Modify: `domain/engine/engine_test.go` (extend `mockInstanceRepo.List` to honor the disjunction)
- Test: `domain/engine/engine_list_visibility_test.go`

- [ ] **Step 1: Write the failing engine test**

Create `domain/engine/engine_list_visibility_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxUserDepts(u uuid.UUID, depts ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Username: "u", Role: identity.RoleUser, Departments: depts,
	})
}

// seedVisInstance puts an instance with explicit owner/visibility/department.
func seedVisInstance(repo *mockInstanceRepo, owner uuid.UUID, vis instance.Visibility, dept string) uuid.UUID {
	id := uuid.New()
	repo.mu.Lock()
	repo.instances[id] = &instance.Instance{
		ID: id, OwnerID: owner, WorkflowName: "wf",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		Visibility: vis, Department: dept,
	}
	repo.mu.Unlock()
	return id
}

// Owner sees their own private run.
func TestList_OwnerSeesOwnPrivate(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	own := seedVisInstance(repo, owner, instance.VisibilityPrivate, "")

	got, err := eng.ListInstances(ctxUserDepts(owner, "engineering"), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, own), "owner sees their own private run")
}

// A department peer (member, not owner) sees a department run in their dept.
func TestList_PeerSeesDepartmentRun(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	peer := uuid.New()
	deptRun := seedVisInstance(repo, owner, instance.VisibilityDepartment, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(peer, "engineering"), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, deptRun), "a member of engineering sees an engineering department run they did not trigger")
}

// A non-member does NOT see another department's department run.
func TestList_NonMemberDoesNotSeeDepartmentRun(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	outsider := uuid.New()
	deptRun := seedVisInstance(repo, owner, instance.VisibilityDepartment, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(outsider, "finance"), instance.ListFilters{})
	require.NoError(t, err)
	assert.False(t, containsID(got, deptRun), "a finance member must not see an engineering department run")
}

// Everyone sees a public run.
func TestList_EveryoneSeesPublic(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	stranger := uuid.New()
	pub := seedVisInstance(repo, owner, instance.VisibilityPublic, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(stranger, "finance"), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, pub), "a public run is visible to everyone")
}

// Admin sees all (no constraint).
func TestList_AdminSeesAll(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	a := seedVisInstance(repo, uuid.New(), instance.VisibilityPrivate, "")
	b := seedVisInstance(repo, uuid.New(), instance.VisibilityDepartment, "engineering")

	got, err := eng.ListInstances(adminCtx(uuid.New()), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, a) && containsID(got, b), "admin sees every run regardless of visibility")
}

// A non-admin does NOT see another user's private run.
func TestList_PeerDoesNotSeeOthersPrivate(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	other := uuid.New()
	priv := seedVisInstance(repo, owner, instance.VisibilityPrivate, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(other, "engineering"), instance.ListFilters{})
	require.NoError(t, err)
	assert.False(t, containsID(got, priv), "a private run is owner-only even within the same department")
}

func containsID(s []instance.InstanceSummary, id uuid.UUID) bool {
	for _, x := range s {
		if x.ID == id {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/engine/ -run 'TestList_' -v`
Expected: FAIL — the current `mockInstanceRepo.List` only filters on `OwnerID`, so a peer/public/department case returns the wrong set; the engine still forces owner-only.

- [ ] **Step 3: Add `listConstraint` to authz.go**

In `domain/engine/authz.go`, add:

```go
// listFilter is the service-derived visibility scope a caller may list.
type listFilter struct {
	OwnerID       *uuid.UUID // nil => unconstrained (admin)
	Departments   []string
	IncludePublic bool
	Unconstrained bool // admin: no visibility narrowing at all
}

// listConstraint computes the visibility scope for ListInstances from the
// context identity. The bool reports whether an identity was present (false =>
// reveal nothing). An admin is unconstrained. A non-admin is scoped to the
// disjunction owner=self OR public OR department-member.
func listConstraint(ctx context.Context) (listFilter, bool) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return listFilter{}, false
	}
	if id.IsAdmin() {
		return listFilter{Unconstrained: true}, true
	}
	uid := id.UserID
	return listFilter{
		OwnerID:       &uid,
		Departments:   id.Departments,
		IncludePublic: true,
	}, true
}
```

Leave the existing `ownerConstraint`/`canView` in place for now (Task 4 splits `canView`); only `ListInstances` changes here.

- [ ] **Step 4: Rewrite `ListInstances` in engine.go**

Replace the body of `ListInstances` (engine.go lines ~279–286) with:

```go
func (e *Engine) ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	lc, ok := listConstraint(ctx)
	if !ok {
		return nil, instance.ErrNotFound // no identity: reveal nothing
	}
	if lc.Unconstrained {
		// Admin: clear any visibility narrowing so the repo returns all rows.
		filters.OwnerID = nil
		filters.Departments = nil
		filters.IncludePublic = false
		return e.instances.List(ctx, filters)
	}
	// Non-admin: service-set the disjunction; ignore any caller-supplied values.
	filters.OwnerID = lc.OwnerID
	filters.Departments = lc.Departments
	filters.IncludePublic = lc.IncludePublic
	return e.instances.List(ctx, filters)
}
```

- [ ] **Step 5: Extend `mockInstanceRepo.List` to honor the disjunction**

In `domain/engine/engine_test.go`, replace the filter body of `mockInstanceRepo.List` (the loop, lines ~166–179) with:

```go
	for _, inst := range r.instances {
		if filters.State != nil && inst.State != *filters.State {
			continue
		}
		if !listVisible(inst, filters) {
			continue
		}
		summaries = append(summaries, instance.InstanceSummary{
			ID:           inst.ID,
			WorkflowName: inst.WorkflowName,
			State:        inst.State,
			Priority:     inst.Priority,
		})
	}
```

and add the helper (mirrors the SQL disjunction the real repo builds):

```go
// listVisible mirrors the repo's disjunctive visibility filter so engine list
// tests exercise the same owner/public/department logic the SQL implements.
// An admin call arrives with OwnerID=nil, Departments=nil, IncludePublic=false
// (no narrowing) => everything is visible.
func listVisible(inst *instance.Instance, f instance.ListFilters) bool {
	if f.OwnerID == nil && len(f.Departments) == 0 && !f.IncludePublic {
		return true // unconstrained (admin)
	}
	if f.OwnerID != nil && inst.OwnerID == *f.OwnerID {
		return true
	}
	if f.IncludePublic && inst.Visibility == instance.VisibilityPublic {
		return true
	}
	if inst.Visibility == instance.VisibilityDepartment {
		for _, d := range f.Departments {
			if d == inst.Department {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `go test ./domain/engine/ -run 'TestList_' -v`
Expected: PASS.

- [ ] **Step 7: Run the pre-existing list tests — confirm posture preserved**

Run: `go test ./domain/engine/ -run 'TestListInstances_' -v`
Expected: PASS. Note `TestListInstances_NonAdmin_OverwritesSuppliedOwner` and `_PerCallerOwnerFilter` still pass: the service still sets `OwnerID` to the caller, now additionally with the (caller-derived) department/public scope. If `_NonAdmin_OverwritesSuppliedOwner` asserts `Departments`/`IncludePublic` are untouched, update it to assert they are service-set too — the security property (no widening from input) is unchanged.

- [ ] **Step 8: Commit**

```bash
git add domain/engine/authz.go domain/engine/engine.go \
        domain/engine/engine_test.go domain/engine/engine_list_visibility_test.go
git commit -m "feat(rbac): disjunctive owner/public/department list authorization in ListInstances"
```

---

### Task 3.3: SQLite `ListInstances` disjunctive query + repo wiring

**Files:**
- Modify: `adapter/sqlite/queries/instances.sql` (`ListInstances`)
- Regenerate: `adapter/sqlite/generated/*`
- Modify: `adapter/sqlite/instance_repo.go` (`List`)
- Test: `adapter/sqlite/instance_repo_list_visibility_test.go`

- [ ] **Step 1: Write the failing repo test**

Create `adapter/sqlite/instance_repo_list_visibility_test.go`:

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

func mkInst(t *testing.T, repo *InstanceRepo, owner uuid.UUID, vis instance.Visibility, dept string) uuid.UUID {
	t.Helper()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "wf",
		WorkflowSnapshot: map[string]any{}, State: instance.StateRunning,
		Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: vis, Department: dept,
	}
	require.NoError(t, repo.Create(context.Background(), inst))
	return inst.ID
}

func listIDs(t *testing.T, repo *InstanceRepo, f instance.ListFilters) map[uuid.UUID]bool {
	t.Helper()
	got, err := repo.List(context.Background(), f)
	require.NoError(t, err)
	out := map[uuid.UUID]bool{}
	for _, s := range got {
		out[s.ID] = true
	}
	return out
}

// The disjunctive filter: owner + public + my-departments, excluding others.
func TestInstanceRepoList_DisjunctiveVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	me := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	other := uuid.New()
	// other must FK-exist; insert directly.
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, 'other', 'user')`, other.String())
	require.NoError(t, err)

	myPrivate := mkInst(t, repo, me, instance.VisibilityPrivate, "")
	othersPrivate := mkInst(t, repo, other, instance.VisibilityPrivate, "engineering")
	engDept := mkInst(t, repo, other, instance.VisibilityDepartment, "engineering")
	finDept := mkInst(t, repo, other, instance.VisibilityDepartment, "finance")
	anyPublic := mkInst(t, repo, other, instance.VisibilityPublic, "platform")

	got := listIDs(t, repo, instance.ListFilters{
		OwnerID:       &me,
		Departments:   []string{"engineering"},
		IncludePublic: true,
		Limit:         100,
	})

	assert.True(t, got[myPrivate], "own private visible")
	assert.True(t, got[engDept], "engineering department run visible")
	assert.True(t, got[anyPublic], "public run visible")
	assert.False(t, got[othersPrivate], "another user's private run hidden")
	assert.False(t, got[finDept], "a finance department run hidden from an engineering member")
}

// Admin (no narrowing) sees everything.
func TestInstanceRepoList_AdminUnconstrained(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	a := mkInst(t, repo, owner, instance.VisibilityPrivate, "")
	b := mkInst(t, repo, owner, instance.VisibilityDepartment, "finance")

	got := listIDs(t, repo, instance.ListFilters{Limit: 100}) // no owner/dept/public => all
	assert.True(t, got[a] && got[b], "unconstrained list returns all rows")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/sqlite/ -run 'TestInstanceRepoList_' -v`
Expected: FAIL — the current query only narrows by `owner_id`; the department/public disjunction is not applied (finance/others-private leak in or department runs are missing).

- [ ] **Step 3: Rewrite the `ListInstances` query (disjunctive, JSON-array department param)**

In `adapter/sqlite/queries/instances.sql`, replace the `ListInstances` query (lines 9–17) with:

```sql
-- name: ListInstances :many
-- Disjunctive RBAC visibility filter (Phase 3): a caller sees a row when it is
-- owned by them, OR public (when include_public), OR a department run in one of
-- their departments. An admin passes owner_id=NULL, include_public=0 (false),
-- and an empty departments array, in which case the visibility disjunction below
-- collapses to "always true" (the three OR-arms are all false, so the guarded
-- `unconstrained` arm admits every row).
--
-- departments is a JSON array string (e.g. '["engineering","platform"]'); the
-- json_each subquery turns it into a membership test. Portable to Postgres as
-- `instances.department = ANY($departments)`.
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at
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

- [ ] **Step 4: Regenerate sqlc**

Run: `cd adapter/sqlite && sqlc generate && cd -`
Expected: `ListInstancesParams` gains `Unconstrained`, `IncludePublic`, and `Departments` (an `interface{}`/string param). If `sqlc` is unavailable, use `go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate`.

- [ ] **Step 5: Wire the repo `List` to the new params**

In `adapter/sqlite/instance_repo.go`, in `List`, build the params:

```go
	unconstrained := int64(0)
	if filters.OwnerID == nil && len(filters.Departments) == 0 && !filters.IncludePublic {
		unconstrained = 1
	}
	deptJSON := "[]"
	if len(filters.Departments) > 0 {
		b, err := json.Marshal(filters.Departments) // import encoding/json if not present
		if err != nil {
			return nil, fmt.Errorf("marshalling departments filter: %w", err)
		}
		deptJSON = string(b)
	}
	includePublic := int64(0)
	if filters.IncludePublic {
		includePublic = 1
	}

	params := generated.ListInstancesParams{
		Limit:         int64(filters.Limit),
		Offset:        int64(filters.Offset),
		Unconstrained: unconstrained,
		IncludePublic: includePublic,
		Departments:   deptJSON,
	}
	// ... existing State/WorkflowName/Since/OwnerID assignments unchanged ...
```

Keep the existing `if filters.OwnerID != nil { params.OwnerID = uuidToString(*filters.OwnerID) }` block. Verify `encoding/json` is imported in `instance_repo.go` (add it if not).

Note on the generated param Go types: sqlc may type `Unconstrained`/`IncludePublic` as `interface{}` and `Departments` as `interface{}` or `string`. Assign the `int64`/`string` values accordingly; adjust the literal to the exact generated field types after Step 4.

- [ ] **Step 6: Run the repo tests to verify they pass**

Run: `go test ./adapter/sqlite/ -run 'TestInstanceRepoList_' -v`
Expected: PASS.

- [ ] **Step 7: Run the full sqlite + engine suites**

Run: `go test ./adapter/sqlite/... ./domain/engine/...`
Expected: PASS — confirm pre-existing list/queue tests still pass (admin path = `unconstrained=1`; per-user path now also carries public/department but the existing single-owner fixtures have no public/department rows, so results are unchanged).

- [ ] **Step 8: Commit**

```bash
git add adapter/sqlite/queries/instances.sql adapter/sqlite/generated/ \
        adapter/sqlite/instance_repo.go adapter/sqlite/instance_repo_list_visibility_test.go
git commit -m "feat(rbac): index-backed disjunctive visibility filter in sqlite ListInstances"
```

---

## Definition of done (Task 3)

- `instance.ListFilters` has service-set `Departments []string` + `IncludePublic bool`.
- `ListInstances` builds the owner/public/department disjunction from the identity; admin is unconstrained; caller-supplied scope is ignored; no identity => `ErrNotFound` with no repo call.
- The SQLite query implements the disjunction against `idx_instances_dept_vis` using a JSON-array department param (Postgres-portable).
- Engine + repo tests prove: owner sees own private; peer sees department run; non-member does not; everyone sees public; admin sees all.
