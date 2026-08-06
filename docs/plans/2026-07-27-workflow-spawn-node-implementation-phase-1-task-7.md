# Phase 1 — Task 7: `SpawnLinkRepository` implementation and node vocabulary docs

**Goal:** Implement the batched child-count and active-children queries on `*sqlite.InstanceRepo`, assert at compile time that it satisfies `instance.SpawnLinkRepository`, and add `spawn_workflow` to the node vocabulary `AGENTS.md` documents.

**Architecture:** Two hand-written queries appended to `adapter/sqlite/instance_repo.go`, following the existing `CountActiveByWorkflowID` precedent (raw SQL on the repo's `*sql.DB`, no sqlc regeneration). The count query takes the caller's already-computed `instance.ChildVisibility` so counts are post-authorization — a count that included invisible children would render an expander whose expansion yields an empty list, leaking existence. The active-children query is mode-agnostic: the cancel cascade covers every active child.

**Tech Stack:** Go 1.25.5, `modernc.org/sqlite`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 7: link-addressed queries + vocabulary docs

**Files:**
- Modify: `adapter/sqlite/instance_repo.go` (append after `CountActiveByWorkflowID`, ~line 449)
- Modify: `adapter/sqlite/queries/instances.sql` (append the two source-of-truth queries)
- Modify: `AGENTS.md:41-52` (node type list)
- Test:   `adapter/sqlite/instance_spawn_queries_test.go`

---

- [x] **Step 1: Write the failing test**

Create `adapter/sqlite/instance_spawn_queries_test.go`:

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

// compile-time proof that the concrete repo satisfies the narrow link port.
var _ instance.SpawnLinkRepository = (*InstanceRepo)(nil)

// childOf creates a child of parentID in the given state and mode.
func childOf(t *testing.T, repo *InstanceRepo, parentID uuid.UUID, node string,
	mode instance.SpawnMode, state instance.State, owner uuid.UUID, vis instance.Visibility, dept string) uuid.UUID {
	t.Helper()
	c := newLinkedInstance(instance.SpawnLink{ParentInstanceID: parentID, ParentNodeID: node, Mode: mode})
	c.OwnerID = owner
	c.State = state
	c.Visibility = vis
	c.Department = dept
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

// ---------- ChildCountsByParents ----------

func TestChildCounts_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p1 := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p1))
	p2 := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p2))

	childOf(t, repo, p1.ID, "n1", instance.SpawnModeWaitForChild, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, p1.ID, "n2", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, p2.ID, "n1", instance.SpawnModeFireAndForget, instance.StateFailed, owner, instance.VisibilityPrivate, "")

	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p1.ID, p2.ID},
		instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err)
	assert.Equal(t, 2, got[p1.ID])
	assert.Equal(t, 1, got[p2.ID])
}

// Absence means zero, not an error — the contract AggregateByInstances sets.
func TestChildCounts_ParentWithNoChildrenIsAbsent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err)
	_, present := got[p.ID]
	assert.False(t, present, "a parent with zero children must be ABSENT from the map, not mapped to 0")
}

// An empty ids slice returns an empty map without touching storage.
func TestChildCounts_EmptyInputTouchesNoStorage(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	got, err := repo.ChildCountsByParents(context.Background(), nil, instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err)
	assert.Empty(t, got)

	// Closing the DB proves no query was issued for the empty input.
	require.NoError(t, db.DB.Close())
	got, err = repo.ChildCountsByParents(context.Background(), []uuid.UUID{},
		instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err, "an empty id slice must short-circuit before any query")
	assert.Empty(t, got)
}

// Counts are POST-AUTHORIZATION: children the caller cannot see are not counted,
// so no expander appears for a run whose children are invisible.
func TestChildCounts_ExcludesInvisibleChildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	viewer := uuid.MustParse(spawnTestOwner)

	otherOwner := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		otherOwner.String(), "other")
	require.NoError(t, err)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	// One child the viewer owns, one private child owned by someone else.
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, viewer, instance.VisibilityPrivate, "")
	childOf(t, repo, p.ID, "n2", instance.SpawnModeFireAndForget, instance.StateRunning, otherOwner, instance.VisibilityPrivate, "")

	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{
		OwnerID:       &viewer,
		IncludePublic: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got[p.ID], "only children the caller may see are counted")
}

func TestChildCounts_AllChildrenInvisibleIsAbsent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	otherOwner := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		otherOwner.String(), "other")
	require.NoError(t, err)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, otherOwner, instance.VisibilityPrivate, "")

	stranger := uuid.New()
	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{OwnerID: &stranger})
	require.NoError(t, err)
	_, present := got[p.ID]
	assert.False(t, present, "a parent whose children are all invisible must report no count (no existence leak)")
}

func TestChildCounts_DepartmentVisibleChildrenAreCounted(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	otherOwner := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		otherOwner.String(), "other")
	require.NoError(t, err)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning,
		otherOwner, instance.VisibilityDepartment, "engineering")

	stranger := uuid.New()
	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{
		OwnerID:     &stranger,
		Departments: []string{"engineering"},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got[p.ID])
}

// ---------- ListActiveChildren ----------

func TestListActiveChildren_CoversBothModes(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	awaited := childOf(t, repo, p.ID, "n1", instance.SpawnModeWaitForChild, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	fireForget := childOf(t, repo, p.ID, "n2", instance.SpawnModeFireAndForget, instance.StateQueued, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	set := map[uuid.UUID]bool{}
	for _, id := range got {
		set[id] = true
	}
	assert.True(t, set[awaited])
	assert.True(t, set[fireForget],
		"the cascade covers EVERY active child; spawn mode is not a filter on it")
	assert.Len(t, got, 2)
}

func TestListActiveChildren_CoversEveryCancellableState(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	want := map[uuid.UUID]bool{}
	for _, st := range []instance.State{
		instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused,
	} {
		want[childOf(t, repo, p.ID, "n"+string(st), instance.SpawnModeFireAndForget, st, owner, instance.VisibilityPrivate, "")] = true
	}
	for _, st := range []instance.State{
		instance.StateCompleted, instance.StateFailed, instance.StateCancelled,
	} {
		childOf(t, repo, p.ID, "t"+string(st), instance.SpawnModeFireAndForget, st, owner, instance.VisibilityPrivate, "")
	}

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.Len(t, got, 4)
	for _, id := range got {
		assert.True(t, want[id], "terminal children must not be returned as active")
	}
}

func TestListActiveChildren_NoChildrenReturnsEmptySlice(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.NotNil(t, got, "must return an empty slice, not nil")
	assert.Empty(t, got)
}

func TestListActiveChildren_DoesNotReturnGrandchildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	child := childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	grand := childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, got, 1, "the query is ONE level; the cascade recurses in the domain")
	assert.Equal(t, child, got[0])
	assert.NotEqual(t, grand, got[0])
}

// ---------- error ----------

func TestSpawnQueries_StorageFailureSurfacesAsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, db.DB.Close())

	_, err := repo.ListActiveChildren(ctx, p.ID)
	require.Error(t, err, "a repository failure must surface as an error, never as an empty result "+
		"indistinguishable from \"no children\"")

	_, err = repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{Unconstrained: true})
	require.Error(t, err)
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./adapter/sqlite/ -run 'TestChildCounts|TestListActiveChildren|TestSpawnQueries'
```

Expected output:

```
# github.com/arbitron/arbitron/adapter/sqlite [github.com/arbitron/arbitron/adapter/sqlite.test]
adapter/sqlite/instance_spawn_queries_test.go:14:31: cannot use (*InstanceRepo)(nil) ... missing method ChildCountsByParents
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
```

- [x] **Step 3: Minimal implementation**

Append to `adapter/sqlite/instance_repo.go`, after `CountActiveByWorkflowID`:

```go
// childVisibilityClause renders the caller's service-set visibility scope as a
// SQL predicate over the CHILD row, plus its bound arguments.
//
// It is the same disjunction List applies. Reusing the caller's already-computed
// scope (rather than re-deriving one) is what makes the child count
// post-authorization: a count that included children the caller cannot see
// would render an expander whose expansion yields an empty list, leaking the
// existence of runs the caller may not view.
//
// An admin (Unconstrained) yields "1=1". A non-admin with NO scope at all
// yields "0=1" — fail closed, count nothing — rather than admitting every row.
func childVisibilityClause(vis instance.ChildVisibility) (string, []any, error) {
	if vis.Unconstrained {
		return "1=1", nil, nil
	}
	var ors []string
	var args []any
	if vis.OwnerID != nil {
		ors = append(ors, "c.owner_id = ?")
		args = append(args, uuidToString(*vis.OwnerID))
	}
	if vis.IncludePublic {
		ors = append(ors, "c.visibility = 'public'")
	}
	if len(vis.Departments) > 0 {
		b, err := json.Marshal(vis.Departments)
		if err != nil {
			return "", nil, fmt.Errorf("marshalling child visibility departments: %w", err)
		}
		ors = append(ors, "(c.visibility = 'department' AND c.department IN (SELECT value FROM json_each(?)))")
		args = append(args, string(b))
	}
	if len(ors) == 0 {
		return "0=1", nil, nil
	}
	return "(" + strings.Join(ors, " OR ") + ")", args, nil
}

// ChildCountsByParents implements instance.SpawnLinkRepository.
//
// Contract (mirrors AggregateByInstances): parents with no VISIBLE children are
// absent from the map — absence means zero, not an error — and an empty
// parentIDs slice returns an empty map without touching storage. It exists so a
// page of the run list resolves every expander in one round trip rather than
// issuing N+1 queries.
func (r *InstanceRepo) ChildCountsByParents(ctx context.Context, parentIDs []uuid.UUID, vis instance.ChildVisibility) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(parentIDs) == 0 {
		return out, nil
	}

	visClause, visArgs, err := childVisibilityClause(vis)
	if err != nil {
		return nil, err
	}

	placeholders := make([]string, len(parentIDs))
	args := make([]any, 0, len(parentIDs)+len(visArgs))
	for i, id := range parentIDs {
		placeholders[i] = "?"
		args = append(args, uuidToString(id))
	}
	args = append(args, visArgs...)

	q := `SELECT c.parent_instance_id, COUNT(*) FROM instances c
		WHERE c.parent_instance_id IN (` + strings.Join(placeholders, ",") + `)
		  AND ` + visClause + `
		GROUP BY c.parent_instance_id`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("counting children by parent: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var parentRaw string
		var n int
		if err := rows.Scan(&parentRaw, &n); err != nil {
			return nil, fmt.Errorf("counting children by parent: %w", err)
		}
		id, err := parseUUID(parentRaw)
		if err != nil {
			return nil, fmt.Errorf("counting children by parent: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting children by parent: %w", err)
	}
	return out, nil
}

// ListActiveChildren implements instance.SpawnLinkRepository.
//
// Spawn mode is deliberately NOT a filter here: the cancel cascade covers every
// active child, both awaited and fire-and-forget. A fire-and-forget child left
// running after its parent is cancelled burns queue capacity and a workspace on
// work whose requester is gone.
//
// It returns ONE level. Recursion belongs in the domain cascade, which carries
// a visited set.
func (r *InstanceRepo) ListActiveChildren(ctx context.Context, parentID uuid.UUID) ([]uuid.UUID, error) {
	const q = `SELECT id FROM instances
		WHERE parent_instance_id = ?
		  AND state IN ('queued','running','waiting','paused')
		ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, q, uuidToString(parentID))
	if err != nil {
		return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
	}
	defer rows.Close()

	out := []uuid.UUID{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
	}
	return out, nil
}
```

- [x] **Step 4: Record the queries in the source-of-truth SQL file**

Append to `adapter/sqlite/queries/instances.sql`:

```sql
-- name: ChildCountsByParents :many
-- NOTE: executed hand-written by InstanceRepo.ChildCountsByParents (the IN list
-- and the caller's visibility disjunction are both variable-arity). Kept here as
-- the readable source of truth. Parents with no visible children simply do not
-- appear in the result — absence means zero.
SELECT parent_instance_id, COUNT(*) AS child_count
FROM instances
WHERE parent_instance_id IN (sqlc.slice('parent_ids'))
GROUP BY parent_instance_id;

-- name: ListActiveChildren :many
-- Every active child of a parent, REGARDLESS of spawn mode: the cancel cascade
-- covers awaited and fire-and-forget children alike.
SELECT id FROM instances
WHERE parent_instance_id = ?
  AND state IN ('queued','running','waiting','paused')
ORDER BY created_at ASC;
```

- [x] **Step 5: Add `spawn_workflow` to the node vocabulary in `AGENTS.md`**

In `AGENTS.md`, in the "Core workflow node type constants" list, add after `interactive_agent`:

```markdown
- `interactive_llm`
- `interactive_agent`
- `spawn_workflow`
```

And immediately after that list, before "Use the code vocabulary above…", insert:

```markdown
`spawn_workflow` starts a CHILD workflow instance from within a parent workflow.
It is ONE node type with two modes (`spawnMode`): `fire_and_forget` (the parent
continues along a single unnamed edge) and `wait_for_child` (the parent suspends
and resumes through its required `success` / `failure` ports). Do not add a
separate node type per mode.
```

- [x] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w adapter/sqlite/
go test ./adapter/sqlite/ -run 'TestChildCounts|TestListActiveChildren|TestSpawnQueries' -v
```

Expected output:

```
--- PASS: TestChildCounts_HappyPath (0.01s)
--- PASS: TestChildCounts_ParentWithNoChildrenIsAbsent (0.01s)
--- PASS: TestChildCounts_EmptyInputTouchesNoStorage (0.01s)
--- PASS: TestChildCounts_ExcludesInvisibleChildren (0.01s)
--- PASS: TestChildCounts_AllChildrenInvisibleIsAbsent (0.01s)
--- PASS: TestChildCounts_DepartmentVisibleChildrenAreCounted (0.01s)
--- PASS: TestListActiveChildren_CoversBothModes (0.01s)
--- PASS: TestListActiveChildren_CoversEveryCancellableState (0.01s)
--- PASS: TestListActiveChildren_NoChildrenReturnsEmptySlice (0.01s)
--- PASS: TestListActiveChildren_DoesNotReturnGrandchildren (0.01s)
--- PASS: TestSpawnQueries_StorageFailureSurfacesAsError (0.01s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.7s
```

- [x] **Step 7: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [x] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add adapter/sqlite/instance_repo.go adapter/sqlite/queries/instances.sql adapter/sqlite/instance_spawn_queries_test.go AGENTS.md
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(sqlite): implement SpawnLinkRepository; document spawn_workflow vocabulary"
```
