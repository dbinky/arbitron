# Phase 4 — Task 1: `SpawnWaitRepository` — the awaited-child lookups

**Goal:** Add the two link-addressed queries wait-for-child needs: find the awaited child of a `(parent, node, mode)` triple, and list every waiting parent whose awaited child is already terminal.

**Architecture:** A second narrow port, `instance.SpawnWaitRepository`, in a new `domain/instance/spawn_wait.go`, satisfied by the same `*sqlite.InstanceRepo`. It is separate from `SpawnLinkRepository` (Phase 1) by interface segregation: each has exactly one consumer — the executor's resumption short-circuit and the startup reconciler respectively. Both queries are served by the composite `idx_instances_parent` index added in migration 018. `State.IsTerminal` moves the "completed | failed | cancelled" definition into one place so the short-circuit, the wake, and the reconciler cannot disagree.

**Tech Stack:** Go 1.25.5, `modernc.org/sqlite`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 1: awaited-child lookups

**Files:**
- Create: `domain/instance/spawn_wait.go`
- Modify: `domain/instance/instance.go:31-41` (append `State.IsTerminal` after the state constants)
- Modify: `adapter/sqlite/instance_repo.go` (append after `ListActiveChildren`)
- Modify: `adapter/sqlite/queries/instances.sql` (append the two source-of-truth queries)
- Test:   `domain/instance/spawn_wait_test.go`
- Test:   `adapter/sqlite/instance_spawn_wait_test.go`

---

- [ ] **Step 1: Write the failing domain test**

Create `domain/instance/spawn_wait_test.go`:

```go
package instance

import "testing"

// The terminal-state definition lives in ONE place so the resumption
// short-circuit, the child-terminal wake, and the startup reconciler cannot
// disagree about when a child is "done".
func TestStateIsTerminal(t *testing.T) {
	cases := map[State]bool{
		StateCompleted: true,
		StateFailed:    true,
		StateCancelled: true,
		StateQueued:    false,
		StateRunning:   false,
		StateWaiting:   false,
		StatePaused:    false,
		State("bogus"): false,
	}
	for st, want := range cases {
		if got := st.IsTerminal(); got != want {
			t.Errorf("State(%q).IsTerminal() = %v, want %v", st, got, want)
		}
	}
}

// A cancelled child is TERMINAL and routes the parent through failure — the
// spec is explicit that there is no separate cancellation path.
func TestCancelledChildIsTerminal(t *testing.T) {
	if !StateCancelled.IsTerminal() {
		t.Fatal("a cancelled child must be terminal so its parent resumes through failure")
	}
}

func TestChildLinkShape(t *testing.T) {
	l := ChildLink{ChildState: StateCompleted}
	if !l.ChildState.IsTerminal() {
		t.Fatal("ChildLink must carry the child's state so the executor can pick a port")
	}
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/instance/ -run 'TestStateIsTerminal|TestCancelledChildIsTerminal|TestChildLinkShape'
```

Expected output:

```
# github.com/arbitron/arbitron/domain/instance [github.com/arbitron/arbitron/domain/instance.test]
domain/instance/spawn_wait_test.go:...: st.IsTerminal undefined (type State has no field or method IsTerminal)
domain/instance/spawn_wait_test.go:...: undefined: ChildLink
FAIL	github.com/arbitron/arbitron/domain/instance [build failed]
```

- [ ] **Step 3: Implement the domain port**

In `domain/instance/instance.go`, add immediately after the `State` constant
block (line 41):

```go
// IsTerminal reports whether s is a final state a run can no longer leave.
// This is the ONE definition of "done": the awaited-spawn resumption
// short-circuit, the child-terminal wake, and the startup reconciler all read
// it, so they cannot disagree about when a child has finished.
func (s State) IsTerminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}
```

Create `domain/instance/spawn_wait.go`:

```go
package instance

import (
	"context"

	"github.com/google/uuid"
)

// ChildLink is the awaited child of ONE (parent instance, spawn node) pair,
// carrying just enough to decide the parent's next move: keep waiting, or route
// by the child's outcome.
type ChildLink struct {
	ChildID    uuid.UUID
	ChildState State
}

// SpawnWaitCandidate pairs a waiting parent with an awaited child that has
// ALREADY reached a terminal state — a parent the in-process wake missed
// (typically because the process died between the child's terminal write and
// the parent's re-queue).
type SpawnWaitCandidate struct {
	ParentID     uuid.UUID
	ParentNodeID string
	ChildID      uuid.UUID
	ChildState   State
}

// SpawnWaitRepository serves the two awaited-child lookups.
//
// It is a SECOND narrow port rather than an extension of SpawnLinkRepository by
// interface segregation: each method here has exactly one consumer — the
// executor's resumption short-circuit and the startup reconciler respectively —
// and neither consumer needs the counting/cascade capabilities.
type SpawnWaitRepository interface {
	// FindChildBySpawnNode returns the most recently created child linked to
	// (parentID, nodeID, mode), or (nil, nil) when there is none.
	//
	// Keying on the LINK — not on node-execution status — is deliberate: the
	// spawn node needs to know HOW the child ended in order to choose a port, and
	// the child's persisted state is the single authority for that. A second copy
	// in the node record could disagree with it after a retry or a late
	// cancellation.
	FindChildBySpawnNode(ctx context.Context, parentID uuid.UUID, nodeID string, mode SpawnMode) (*ChildLink, error)

	// ListWaitingOnTerminalChild returns every parent in the waiting state whose
	// awaited child (matching the parent's CURRENT node id, with
	// mode = wait_for_child) is already terminal. An empty result is not an error.
	ListWaitingOnTerminalChild(ctx context.Context) ([]SpawnWaitCandidate, error)
}
```

- [ ] **Step 4: Run the domain test, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/instance/
go test ./domain/instance/ -run 'TestStateIsTerminal|TestCancelledChildIsTerminal|TestChildLinkShape' -v
```

Expected output:

```
--- PASS: TestStateIsTerminal (0.00s)
--- PASS: TestCancelledChildIsTerminal (0.00s)
--- PASS: TestChildLinkShape (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/instance	0.2s
```

- [ ] **Step 5: Write the failing adapter test**

Create `adapter/sqlite/instance_spawn_wait_test.go`:

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

var _ instance.SpawnWaitRepository = (*InstanceRepo)(nil)

// waitingParent creates a parent in the waiting state whose current node is the
// given node id.
func waitingParent(t *testing.T, repo *InstanceRepo, nodeID string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	p := newLinkedInstance(instance.SpawnLink{})
	p.State = instance.StateWaiting
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, repo.UpdateCurrentNode(ctx, p.ID, nodeID))
	require.NoError(t, repo.UpdateState(ctx, p.ID, instance.StateWaiting))
	return p.ID
}

// ---------- FindChildBySpawnNode ----------

func TestFindChildBySpawnNode_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	child := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.FindChildBySpawnNode(ctx, parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, child, got.ChildID)
	assert.Equal(t, instance.StateRunning, got.ChildState)
	assert.False(t, got.ChildState.IsTerminal())
}

func TestFindChildBySpawnNode_NoChildReturnsNilNilNotError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parent := waitingParent(t, repo, "spawn-1")

	got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.NoError(t, err, "\"no child yet\" is the FIRST-EXECUTION signal, not an error")
	assert.Nil(t, got)
}

// The node id is part of the key: a child from a DIFFERENT spawn node of the
// same parent must not be matched.
func TestFindChildBySpawnNode_DifferentNodeDoesNotMatch(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-B")
	childOf(t, repo, parent, "spawn-A", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-B", instance.SpawnModeWaitForChild)
	require.NoError(t, err)
	assert.Nil(t, got, "node A's child must not answer for node B")
}

// The mode is part of the key: a fire-and-forget child of the SAME node must
// not be matched by an awaited lookup.
func TestFindChildBySpawnNode_DifferentModeDoesNotMatch(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindChildBySpawnNode_TerminalChildIsReturnedWithItsState(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	for _, st := range []instance.State{
		instance.StateCompleted, instance.StateFailed, instance.StateCancelled,
	} {
		parent := waitingParent(t, repo, "spawn-1")
		childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild, st, owner, instance.VisibilityPrivate, "")

		got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, st, got.ChildState)
		assert.True(t, got.ChildState.IsTerminal())
	}
}

func TestFindChildBySpawnNode_StorageFailureErrors(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parent := waitingParent(t, repo, "spawn-1")
	require.NoError(t, db.DB.Close())

	_, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.Error(t, err, "a storage failure must not be indistinguishable from \"no child yet\", "+
		"which would cause a SECOND child to be spawned")
}

// ---------- ListWaitingOnTerminalChild ----------

func TestListWaitingOnTerminalChild_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	child := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateFailed, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, parent, got[0].ParentID)
	assert.Equal(t, "spawn-1", got[0].ParentNodeID)
	assert.Equal(t, child, got[0].ChildID)
	assert.Equal(t, instance.StateFailed, got[0].ChildState)
}

func TestListWaitingOnTerminalChild_ActiveChildIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "a parent whose child is still running is correctly waiting")
}

// The three-part match: a parent waiting at node B is NOT a candidate because
// node A's child finished.
func TestListWaitingOnTerminalChild_NodeMismatchIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-B")
	childOf(t, repo, parent, "spawn-A", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListWaitingOnTerminalChild_FireAndForgetIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "a fire-and-forget child must never wake its parent")
}

func TestListWaitingOnTerminalChild_NonWaitingParentIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	require.NoError(t, repo.UpdateState(ctx, parent, instance.StateCancelled))
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "a cancelled parent must never be resurrected by its dying child")
}

func TestListWaitingOnTerminalChild_EmptyDatabaseReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestListWaitingOnTerminalChild_StorageFailureErrors(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	require.NoError(t, db.DB.Close())

	_, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.Error(t, err)
}
```

- [ ] **Step 6: Run the adapter test, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./adapter/sqlite/ -run 'TestFindChildBySpawnNode|TestListWaitingOnTerminalChild'
```

Expected output:

```
# github.com/arbitron/arbitron/adapter/sqlite [github.com/arbitron/arbitron/adapter/sqlite.test]
adapter/sqlite/instance_spawn_wait_test.go:13:38: cannot use (*InstanceRepo)(nil) ... missing method FindChildBySpawnNode
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
```

- [ ] **Step 7: Implement the two queries**

Append to `adapter/sqlite/instance_repo.go`:

```go
// FindChildBySpawnNode implements instance.SpawnWaitRepository.
//
// All THREE key parts are load-bearing. Dropping the node id would let a child
// of a different spawn node in the same parent answer here; dropping the mode
// would let a fire-and-forget child answer an awaited lookup. Either mistake
// routes a parent by an event that had nothing to do with it.
//
// (nil, nil) means "no child yet" — the FIRST-EXECUTION signal — and is
// deliberately distinguishable from an error, because treating a storage
// failure as "no child" would spawn a second child.
//
// ORDER BY created_at DESC LIMIT 1 picks the most recent child. A node-level
// retry after a partially failed suspension can leave more than one row; the
// newest is the one this pass is waiting on.
func (r *InstanceRepo) FindChildBySpawnNode(ctx context.Context, parentID uuid.UUID, nodeID string, mode instance.SpawnMode) (*instance.ChildLink, error) {
	const q = `SELECT id, state FROM instances
		WHERE parent_instance_id = ? AND parent_node_id = ? AND spawn_mode = ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1`

	var idRaw, state string
	err := r.db.QueryRowContext(ctx, q, uuidToString(parentID), nodeID, string(mode)).Scan(&idRaw, &state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("finding child of %s node %q: %w", parentID, nodeID, err)
	}
	childID, err := parseUUID(idRaw)
	if err != nil {
		return nil, fmt.Errorf("finding child of %s node %q: %w", parentID, nodeID, err)
	}
	return &instance.ChildLink{ChildID: childID, ChildState: instance.State(state)}, nil
}

// ListWaitingOnTerminalChild implements instance.SpawnWaitRepository.
//
// The WHERE clause is the reconciler's correctness core and mirrors the
// in-process wake's three-part match exactly:
//
//	the parent is WAITING
//	∧ the parent's CURRENT node id == the child's recorded parent node id
//	∧ the link's mode is wait_for_child
//
// plus "the child is already terminal". Any one of the three conjuncts dropped
// would resume a parent by an unrelated event.
func (r *InstanceRepo) ListWaitingOnTerminalChild(ctx context.Context) ([]instance.SpawnWaitCandidate, error) {
	const q = `SELECT p.id, p.current_node_id, c.id, c.state
		FROM instances p
		JOIN instances c ON c.parent_instance_id = p.id
		WHERE p.state = 'waiting'
		  AND p.current_node_id IS NOT NULL
		  AND c.parent_node_id = p.current_node_id
		  AND c.spawn_mode = 'wait_for_child'
		  AND c.state IN ('completed','failed','cancelled')`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
	}
	defer rows.Close()

	out := []instance.SpawnWaitCandidate{}
	for rows.Next() {
		var parentRaw, nodeID, childRaw, state string
		if err := rows.Scan(&parentRaw, &nodeID, &childRaw, &state); err != nil {
			return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
		}
		parentID, err := parseUUID(parentRaw)
		if err != nil {
			return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
		}
		childID, err := parseUUID(childRaw)
		if err != nil {
			return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
		}
		out = append(out, instance.SpawnWaitCandidate{
			ParentID:     parentID,
			ParentNodeID: nodeID,
			ChildID:      childID,
			ChildState:   instance.State(state),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 8: Record the queries in the source-of-truth SQL file**

Append to `adapter/sqlite/queries/instances.sql`:

```sql
-- name: FindChildBySpawnNode :one
-- The awaited child of ONE (parent, spawn node, mode) triple. All three key
-- parts are load-bearing: without the node id a child of a different spawn node
-- would answer; without the mode a fire-and-forget child would.
SELECT id, state FROM instances
WHERE parent_instance_id = ? AND parent_node_id = ? AND spawn_mode = ?
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: ListWaitingOnTerminalChild :many
-- Startup reconciliation: parents stuck in the waiting state whose awaited
-- child is already terminal (the in-process wake was lost to a crash).
SELECT p.id, p.current_node_id, c.id, c.state
FROM instances p
JOIN instances c ON c.parent_instance_id = p.id
WHERE p.state = 'waiting'
  AND p.current_node_id IS NOT NULL
  AND c.parent_node_id = p.current_node_id
  AND c.spawn_mode = 'wait_for_child'
  AND c.state IN ('completed','failed','cancelled');
```

- [ ] **Step 9: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/instance/ adapter/sqlite/
go test ./domain/instance/ ./adapter/sqlite/ -run 'TestStateIsTerminal|TestCancelledChild|TestChildLink|TestFindChildBySpawnNode|TestListWaitingOnTerminalChild' -v 2>&1 | tail -30
```

Expected output:

```
--- PASS: TestFindChildBySpawnNode_HappyPath (0.01s)
--- PASS: TestFindChildBySpawnNode_NoChildReturnsNilNilNotError (0.01s)
--- PASS: TestFindChildBySpawnNode_DifferentNodeDoesNotMatch (0.01s)
--- PASS: TestFindChildBySpawnNode_DifferentModeDoesNotMatch (0.01s)
--- PASS: TestFindChildBySpawnNode_TerminalChildIsReturnedWithItsState (0.02s)
--- PASS: TestFindChildBySpawnNode_StorageFailureErrors (0.01s)
--- PASS: TestListWaitingOnTerminalChild_HappyPath (0.01s)
--- PASS: TestListWaitingOnTerminalChild_ActiveChildIsNotACandidate (0.01s)
--- PASS: TestListWaitingOnTerminalChild_NodeMismatchIsNotACandidate (0.01s)
--- PASS: TestListWaitingOnTerminalChild_FireAndForgetIsNotACandidate (0.01s)
--- PASS: TestListWaitingOnTerminalChild_NonWaitingParentIsNotACandidate (0.01s)
--- PASS: TestListWaitingOnTerminalChild_EmptyDatabaseReturnsEmpty (0.01s)
--- PASS: TestListWaitingOnTerminalChild_StorageFailureErrors (0.01s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.7s
```

- [ ] **Step 10: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/instance/spawn_wait.go domain/instance/spawn_wait_test.go domain/instance/instance.go adapter/sqlite/instance_repo.go adapter/sqlite/queries/instances.sql adapter/sqlite/instance_spawn_wait_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(instance): add SpawnWaitRepository awaited-child lookups"
```
