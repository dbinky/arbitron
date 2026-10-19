# Parallel Workflow Gathering — Phase 2 Task 4: Node-addressed parent wake (`SetQueuedIfWaitingAtNode`)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the group coordinator a wake primitive that re-queues a parent ONLY IF it is still waiting at the group's own node: `(*sqlite.QueueRepo).SetQueuedIfWaitingAtNode`, satisfying the `instance.NodeAddressedWaker` port declared in P2-T1.
**Architecture:** One hand-written conditional `UPDATE … WHERE id = ? AND state = 'waiting' AND current_node_id = ?` on `QueueRepo` — the existing `SetQueuedIfWaiting` (state only) plus the node conjunct `InstanceRepo.ClaimWaitingParent` already uses — so the coordinator's wake and the executor's lost-wake claim stay mutually exclusive on the same row, a parent that has moved on to another node is never woken by an old group, and a cancelled/terminal parent is never resurrected. A compile-time assertion pins the port.
**Tech Stack:** Go 1.25.5, `database/sql`, `modernc.org/sqlite`, testify (real in-memory SQLite, concurrency test under `-race`).
**Spec / Design:** `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §3 ("Queue parent only if it is waiting at **this group node**, has continuation readiness, and is not cancelled/terminal. Current `QueueRepo.SetQueuedIfWaiting` checks state only; add a group/node-addressed claim") and Data Provenance row "Parent wake primitives" (node-addressed group claim is P2-A).

**Interfaces**
- Consumes:
  - P2-T1 `domain/instance/group_run.go`: `type NodeAddressedWaker interface { SetQueuedIfWaitingAtNode(ctx context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error) }`.
  - Existing `adapter/sqlite/queue_repo.go:16-27` (`QueueRepo`, `NewQueueRepo`), `:87-107` (`SetQueuedIfWaiting` — after P2-T2 it is unchanged but sits at lines 111-131 because P2-T2 rewrote `NextEligible` above it); `convert.go:13-15,25-27,55-57` (`uuidToString`, `timeToString`, `stringToNullString`); `instance_repo.go:777-790` (`ClaimWaitingParent`, the competing mover); `assert.go:8-13` (compile-time port assertions); P2-T2's hand-written `NextEligible` (used by the happy-path test to prove the woken parent is dequeuable).
  - Test helpers: `setupTestDB` (`integration_test.go:20`), `newLinkedInstance` (`instance_spawn_link_test.go:17`).
- Produces:
  - `func (r *QueueRepo) SetQueuedIfWaitingAtNode(ctx context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error)` — `true` iff THIS call moved the row `waiting@nodeID → queued` (stamping `queued_at`); `(false, nil)` for a parent waiting elsewhere, in any non-waiting state, with a NULL current node, already woken, or unknown; `(false, error)` for an empty `nodeID` and for storage/context errors.
  - `var _ instance.NodeAddressedWaker = (*QueueRepo)(nil)` in `adapter/sqlite/assert.go`.

**Wiring & data provenance**
- Composition root: P2-T14 passes the existing `queueRepo := adaptsqlite.NewQueueRepo(db)` (`cmd/arbitron/main.go:797`) as the `waker instance.NodeAddressedWaker` argument of `engine.NewGroupCoordinator` (P2-T10, `domain/engine/group_coordinator.go`); `GroupCoordinator.WakeParent` calls it and then `queue.Manager.NotifyCapacityFreed`. P2-T14's `cmd/arbitron/group_e2e_test.go` boots `initDomain` and fails if a decided group never resumes its parent (i.e. the waker is unwired).
- Producers of the arguments: `id` = `GroupRun.ParentInstanceID`, `nodeID` = `GroupRun.NodeID` — both persisted by `GroupRunRepository.CreateWithTasks` (P2-T3) from `GroupLauncher.Launch` (P2-T8), which takes them from the parent `Instance.ID` and the executing `workflow.Node.ID`; the row's `current_node_id` is written by `e.instances.UpdateCurrentNode` in `domain/engine/engine.go:Engine.run` (line 923) before the node's executor runs, and `state = 'waiting'` by the executor's `instances.UpdateState(waiting)` (P2-T11). `queuedAt` = the coordinator's clock (`time.Now()`).
- Provenance probes: the compile-time assertion fails the build if the method is removed or its signature drifts from the port; `TestSetQueuedIfWaitingAtNode_WaitingAtAnotherNodeIsUntouched` fails if the node argument is ignored (a state-only implementation would pass every other test).

### Task 4: Node-addressed wake on QueueRepo
**Files:**
- Modify: `adapter/sqlite/queue_repo.go` — insert immediately after `SetQueuedIfWaiting` (base lines 87-107; lines 111-131 once P2-T2 has landed), before `func (r *QueueRepo) ListWaitingWithTimer`
- Modify: `adapter/sqlite/assert.go:12` (append one assertion after the `QueueRepository` one)
- Test: `adapter/sqlite/queue_repo_node_wake_test.go`

- [x] Step 1: Write the failing tests. Create `adapter/sqlite/queue_repo_node_wake_test.go`:

```go
package sqlite

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parentAt creates a root instance whose current node is nodeID, then moves it
// to state (via the same repository calls the engine uses).
func parentAt(t *testing.T, repo *InstanceRepo, nodeID string, state instance.State) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, repo.UpdateCurrentNode(ctx, p.ID, nodeID))
	require.NoError(t, repo.UpdateState(ctx, p.ID, state))
	return p.ID
}

// stateAndQueuedAt reads the row back through the repository's own Get.
func stateAndQueuedAt(t *testing.T, repo *InstanceRepo, id uuid.UUID) (instance.State, *time.Time) {
	t.Helper()
	got, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	return got.State, got.QueuedAt
}

// ---------- happy path ----------

func TestSetQueuedIfWaitingAtNode_WaitingAtThatNodeIsQueued(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	id := parentAt(t, instRepo, "group-1", instance.StateWaiting)
	queuedAt := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), id, "group-1", queuedAt)
	require.NoError(t, err)
	assert.True(t, ok)

	state, at := stateAndQueuedAt(t, instRepo, id)
	assert.Equal(t, instance.StateQueued, state)
	require.NotNil(t, at, "queued_at must be stamped so the queue orders the wake FIFO")
	assert.True(t, queuedAt.Equal(*at))

	// The woken parent is now dequeuable through the normal queue path.
	next, err := queueRepo.NextEligible(context.Background(), 0, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, id, next.ID)
}

// ---------- failure: not this node / not waiting ----------

func TestSetQueuedIfWaitingAtNode_WaitingAtAnotherNodeIsUntouched(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	id := parentAt(t, instRepo, "spawn-2", instance.StateWaiting)

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), id, "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok, "a parent that moved on to a later node must not be woken by this group")

	state, at := stateAndQueuedAt(t, instRepo, id)
	assert.Equal(t, instance.StateWaiting, state)
	assert.Nil(t, at, "queued_at must not be touched by a losing wake")
}

func TestSetQueuedIfWaitingAtNode_NonWaitingStatesAreNeverWoken(t *testing.T) {
	for _, state := range []instance.State{
		instance.StateRunning, instance.StateQueued, instance.StatePaused,
		instance.StateCompleted, instance.StateFailed, instance.StateCancelled,
	} {
		t.Run(string(state), func(t *testing.T) {
			db := setupTestDB(t)
			instRepo := NewInstanceRepo(db.DB)
			queueRepo := NewQueueRepo(db.DB)
			id := parentAt(t, instRepo, "group-1", state)

			ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), id, "group-1", time.Now().UTC())
			require.NoError(t, err)
			assert.False(t, ok)

			got, _ := stateAndQueuedAt(t, instRepo, id)
			assert.Equal(t, state, got, "a %s parent must keep its state (a cancelled parent is never resurrected)", state)
		})
	}
}

// A parent with no current node (it never reached one) cannot be addressed.
func TestSetQueuedIfWaitingAtNode_NullCurrentNodeNeverMatches(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()
	p := newLinkedInstance(instance.SpawnLink{})
	p.State = instance.StateWaiting
	require.NoError(t, instRepo.Create(ctx, p))

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(ctx, p.ID, "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok)
}

// ---------- edge: idempotence / unknown ids ----------

func TestSetQueuedIfWaitingAtNode_SecondWakeLoses(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	id := parentAt(t, instRepo, "group-1", instance.StateWaiting)
	ctx := context.Background()

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(ctx, id, "group-1", time.Now().UTC())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = queueRepo.SetQueuedIfWaitingAtNode(ctx, id, "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok, "a duplicate wake (coordinator + sweep) re-queues once")
}

func TestSetQueuedIfWaitingAtNode_UnknownInstanceIsFalseNotError(t *testing.T) {
	db := setupTestDB(t)
	ok, err := NewQueueRepo(db.DB).SetQueuedIfWaitingAtNode(context.Background(), uuid.New(), "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok)
}

// ---------- error ----------

func TestSetQueuedIfWaitingAtNode_EmptyNodeIsRefused(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	id := parentAt(t, instRepo, "group-1", instance.StateWaiting)

	ok, err := NewQueueRepo(db.DB).SetQueuedIfWaitingAtNode(context.Background(), id, "", time.Now().UTC())
	require.Error(t, err)
	assert.False(t, ok)
	state, _ := stateAndQueuedAt(t, instRepo, id)
	assert.Equal(t, instance.StateWaiting, state)
}

func TestSetQueuedIfWaitingAtNode_StorageErrorsSurface(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.DB)
	require.NoError(t, db.DB.Close())

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), uuid.New(), "group-1", time.Now().UTC())
	assert.Error(t, err)
	assert.False(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db2 := setupTestDB(t)
	_, err = NewQueueRepo(db2.DB).SetQueuedIfWaitingAtNode(ctx, uuid.New(), "group-1", time.Now().UTC())
	assert.Error(t, err, "a cancelled context aborts the wake")
}

// ---------- concurrency: mutual exclusion with ClaimWaitingParent ----------

// The coordinator's wake and the executor's lost-wake claim race for the same
// waiting parent. Exactly one may win every time, and the row must end in the
// winner's state — never both queued and running.
func TestSetQueuedIfWaitingAtNode_ExactlyOneWinsAgainstClaimWaitingParent(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		id := parentAt(t, instRepo, "group-1", instance.StateWaiting)

		var wg sync.WaitGroup
		var woke, claimed bool
		var wakeErr, claimErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			woke, wakeErr = queueRepo.SetQueuedIfWaitingAtNode(ctx, id, "group-1", time.Now().UTC())
		}()
		go func() {
			defer wg.Done()
			<-start
			claimed, claimErr = instRepo.ClaimWaitingParent(ctx, id, "group-1")
		}()
		close(start)
		wg.Wait()

		require.NoError(t, wakeErr)
		require.NoError(t, claimErr)
		require.True(t, woke != claimed, "iteration %d: exactly one mover must win (woke=%v claimed=%v)", i, woke, claimed)

		state, _ := stateAndQueuedAt(t, instRepo, id)
		if woke {
			assert.Equal(t, instance.StateQueued, state)
		} else {
			assert.Equal(t, instance.StateRunning, state)
		}
	}
}
```

- [x] Step 2: Run them, confirm they fail.

```bash
go test ./adapter/sqlite/ -run TestSetQueuedIfWaitingAtNode -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/adapter/sqlite [github.com/arbitron/arbitron/adapter/sqlite.test]
adapter/sqlite/queue_repo_node_wake_test.go:44:23: queueRepo.SetQueuedIfWaitingAtNode undefined (type *QueueRepo has no field or method SetQueuedIfWaitingAtNode)
adapter/sqlite/queue_repo_node_wake_test.go:68:23: queueRepo.SetQueuedIfWaitingAtNode undefined (type *QueueRepo has no field or method SetQueuedIfWaitingAtNode)
adapter/sqlite/queue_repo_node_wake_test.go:88:25: queueRepo.SetQueuedIfWaitingAtNode undefined (type *QueueRepo has no field or method SetQueuedIfWaitingAtNode)
...
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
```

- [x] Step 3: Implement. In `adapter/sqlite/queue_repo.go`, insert immediately after the closing `}` of `SetQueuedIfWaiting` (and before `func (r *QueueRepo) ListWaitingWithTimer`):

```go
// SetQueuedIfWaitingAtNode implements instance.NodeAddressedWaker.
//
// It is SetQueuedIfWaiting with the node conjunct ClaimWaitingParent already
// uses: the parent is re-queued ONLY IF it is still `waiting` AND its
// current node is nodeID. A group coordinator waking "the parent waiting at
// group node G" must not re-queue a parent that has since moved on and is
// waiting somewhere else (a later spawn or group node, a timer, a webhook) —
// that parent would resume at the wrong node. Because the predicate also
// requires `waiting`, a cancelled, failed or completed parent is never
// resurrected, and the same ONE conditional UPDATE makes this wake mutually
// exclusive with the executor's ClaimWaitingParent on the same row.
//
// A false return is the normal "somebody else moved it / it is not waiting
// here" outcome, not an error. An empty nodeID is refused: no waiting parent
// can legitimately be addressed by it, and silently matching nothing would
// hide a caller bug.
func (r *QueueRepo) SetQueuedIfWaitingAtNode(ctx context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error) {
	if nodeID == "" {
		return false, fmt.Errorf("re-queueing waiting instance %s: node id is required", id)
	}
	const q = `UPDATE instances
  SET state = 'queued', queued_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE id = ? AND state = 'waiting' AND current_node_id = ?`
	res, err := r.db.ExecContext(ctx, q, stringToNullString(timeToString(queuedAt)), uuidToString(id), nodeID)
	if err != nil {
		return false, fmt.Errorf("re-queueing instance %s waiting at node %q: %w", id, nodeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("re-queueing instance %s waiting at node %q: %w", id, nodeID, err)
	}
	return n > 0, nil
}
```

Then in `adapter/sqlite/assert.go`, add the port assertion after `var _ instance.QueueRepository = (*QueueRepo)(nil)` (line 12), so the file reads:

```go
package sqlite

import (
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
)

var _ instance.InstanceRepository = (*InstanceRepo)(nil)
var _ instance.NodeExecutionRepository = (*NodeExecutionRepo)(nil)
var _ instance.VariableSnapshotRepository = (*VariableSnapshotRepo)(nil)
var _ instance.UserRepository = (*UserRepo)(nil)
var _ instance.QueueRepository = (*QueueRepo)(nil)
var _ instance.NodeAddressedWaker = (*QueueRepo)(nil)
var _ notification.Repository = (*NotificationRepo)(nil)
```

(`queue_repo.go` already imports `context`, `fmt`, `time`, `uuid` and `instance`; no import changes.)

- [x] Step 4: Run the tests, confirm they pass — including under the race detector.

```bash
go test ./adapter/sqlite/ -run TestSetQueuedIfWaitingAtNode -count=1 -v
go test ./adapter/sqlite/ -run TestSetQueuedIfWaitingAtNode -count=1 -race
```
Expected:
```
--- PASS: TestSetQueuedIfWaitingAtNode_WaitingAtThatNodeIsQueued (0.01s)
--- PASS: TestSetQueuedIfWaitingAtNode_WaitingAtAnotherNodeIsUntouched (0.01s)
--- PASS: TestSetQueuedIfWaitingAtNode_NonWaitingStatesAreNeverWoken (0.06s)
--- PASS: TestSetQueuedIfWaitingAtNode_NullCurrentNodeNeverMatches (0.01s)
--- PASS: TestSetQueuedIfWaitingAtNode_SecondWakeLoses (0.01s)
--- PASS: TestSetQueuedIfWaitingAtNode_UnknownInstanceIsFalseNotError (0.01s)
--- PASS: TestSetQueuedIfWaitingAtNode_EmptyNodeIsRefused (0.01s)
--- PASS: TestSetQueuedIfWaitingAtNode_StorageErrorsSurface (0.02s)
--- PASS: TestSetQueuedIfWaitingAtNode_ExactlyOneWinsAgainstClaimWaitingParent (0.02s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.4XXs
ok  	github.com/arbitron/arbitron/adapter/sqlite	X.XXXs
```
(`TestSetQueuedIfWaitingAtNode_NonWaitingStatesAreNeverWoken` additionally prints one indented `--- PASS` per state: running, queued, paused, completed, failed, cancelled.)

- [x] Step 5: Run the package and dependents; gofmt and vet.

```bash
go test ./adapter/sqlite/ ./domain/... ./cmd/... -count=1
gofmt -l adapter/sqlite
go vet ./adapter/sqlite/
```
Expected: every package `ok`; `gofmt -l` and `go vet` print nothing. (`go test ./...` additionally shows the pre-existing, unrelated `adapter/config` failure `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`.)

- [x] Step 6: Commit.

```bash
git add adapter/sqlite/queue_repo.go adapter/sqlite/assert.go adapter/sqlite/queue_repo_node_wake_test.go
git commit -m "feat(sqlite): node-addressed parent wake SetQueuedIfWaitingAtNode for parallel groups" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
