# Phase 2 — Task 1: QueueRepository.ListQueued read method + SQLite impl

**Goal:** Add a read-only `ListQueued` method to `instance.QueueRepository` that returns all currently-queued instances with the fields the position simulation needs (ID, Priority, QueuedAt, OwnerID), and implement it as a single SQLite query. This is the read the `queue.Manager.QueuePositions` simulation consumes (Phase 2 Task 2).

**Architecture:** The queue position is computed inside `queue.Manager` (the single ordering authority). Today `NextEligible` returns only one item; the position simulation needs the whole queued set. `ListQueued` is an additive read-only method on the existing `instance.QueueRepository` port, implemented as a hand-written query in `adapter/sqlite/queue_repo.go` (matching the `AggregateByInstances` precedent — the generated tree is not regenerated for additive reads). No wiring here — Phase 2 Task 2 consumes it; Phase 2 Task 3 wires the position provider into the enricher.

**Tech Stack:** Go, `database/sql`, SQLite. Tests with `testing` + `testify` + the in-memory `setupTestDB` helper.

---

### Task 1: ListQueued

**Files:**
- Modify: `domain/instance/ports.go:32-41` (extend `QueueRepository` interface)
- Modify: `adapter/sqlite/queue_repo.go` (add `ListQueued`)
- Create: `adapter/sqlite/queue_repo_list_queued_test.go`
- Modify: `domain/queue/manager_test.go:26-103` (extend `mockQueueRepo`)

- [x] Step 1: Add `ListQueued` to the port and run to confirm the SQLite adapter no longer satisfies the interface

In `domain/instance/ports.go`, extend the `QueueRepository` interface (after `ListWaitingWithWebhook`):

```go
// QueueRepository defines the port for queue-specific persistence operations.
type QueueRepository interface {
	CountRunning(ctx context.Context) (int, error)
	CountRunningByOwner(ctx context.Context, ownerID uuid.UUID) (int, error)
	CountQueued(ctx context.Context) (int, error)
	CountWaiting(ctx context.Context) (int, error)
	NextEligible(ctx context.Context, globalCap int, perUserCap int) (*Instance, error)
	SetQueued(ctx context.Context, id uuid.UUID, queuedAt time.Time) error
	ListWaitingWithTimer(ctx context.Context) ([]Instance, error)
	ListWaitingWithWebhook(ctx context.Context) ([]Instance, error)
	// ListQueued returns all currently-queued instances ordered by priority
	// (high → normal → low) then QueuedAt FIFO within a priority — the same
	// ordering NextEligible uses to pick the next item. An empty queue returns
	// an empty slice, not an error.
	ListQueued(ctx context.Context) ([]Instance, error)
}
```

Run:

```bash
go build ./...
```

Expected: build failure — `*sqlite.QueueRepo` and every test mock of `QueueRepository` are missing `ListQueued`.

- [x] Step 2: Write the failing SQLite test

Create `adapter/sqlite/queue_repo_list_queued_test.go`:

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListQueued_OrdersByPriorityThenFIFO(t *testing.T) {
	td := setupTestDB(t)
	instRepo := NewInstanceRepo(td.DB)
	queueRepo := NewQueueRepo(td.DB)

	owner := uuid.New()
	// Seed a users row so the instances FK is satisfied.
	_, err := td.DB.ExecContext(context.Background(),
		"INSERT INTO users (id, username, role) VALUES (?, 'testuser', 'user')", owner.String())
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	mkQueued := func(id uuid.UUID, priority instance.Priority, queuedAt time.Time) *instance.Instance {
		return &instance.Instance{
			ID: id, OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: map[string]any{"name": "wf"},
			State: instance.StateQueued, Priority: priority, QueuedAt: &queuedAt, CreatedAt: now, UpdatedAt: now,
		}
	}
	low1 := uuid.New(); mkLow := mkQueued(low1, instance.PriorityLow, now.Add(2*time.Second))
	normal1 := uuid.New(); mkNormal := mkQueued(normal1, instance.PriorityNormal, now.Add(time.Second))
	high1 := uuid.New(); mkHigh := mkQueued(high1, instance.PriorityHigh, now)
	high2 := uuid.New(); mkHigh2 := mkQueued(high2, instance.PriorityHigh, now.Add(3*time.Second))

	for _, inst := range []*instance.Instance{mkLow, mkNormal, mkHigh, mkHigh2} {
		require.NoError(t, instRepo.Create(context.Background(), inst))
	}

	got, err := queueRepo.ListQueued(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, high1, got[0].ID, "high priority first")
	assert.Equal(t, high2, got[1].ID, "FIFO within high (earlier QueuedAt first)")
	assert.Equal(t, normal1, got[2].ID, "normal next")
	assert.Equal(t, low1, got[3].ID, "low last")
}

func TestListQueued_EmptyQueue(t *testing.T) {
	td := setupTestDB(t)
	queueRepo := NewQueueRepo(td.DB)
	got, err := queueRepo.ListQueued(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "empty queue -> empty slice, not error")
}

func TestListQueued_ExcludesNonQueuedStates(t *testing.T) {
	td := setupTestDB(t)
	instRepo := NewInstanceRepo(td.DB)
	queueRepo := NewQueueRepo(td.DB)

	owner := uuid.New()
	_, err := td.DB.ExecContext(context.Background(),
		"INSERT INTO users (id, username, role) VALUES (?, 'testuser', 'user')", owner.String())
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	queued := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: map[string]any{"name": "wf"}, State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: &now, CreatedAt: now, UpdatedAt: now}
	running := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf2", WorkflowSnapshot: map[string]any{"name": "wf2"}, State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, instRepo.Create(context.Background(), queued))
	require.NoError(t, instRepo.Create(context.Background(), running))

	got, err := queueRepo.ListQueued(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1, "only queued rows returned")
	assert.Equal(t, queued.ID, got[0].ID)
}
```

- [x] Step 3: Implement the SQLite `ListQueued` method

In `adapter/sqlite/queue_repo.go`, add this method:

```go
// ListQueued returns all currently-queued instances ordered by priority
// (high → normal → low) then QueuedAt FIFO within a priority — the same
// ordering NextEligible uses. An empty queue returns an empty slice.
func (r *QueueRepo) ListQueued(ctx context.Context) ([]instance.Instance, error) {
	query := `SELECT * FROM instances WHERE state = 'queued' ORDER BY
		CASE priority
			WHEN 'high' THEN 1
			WHEN 'normal' THEN 2
			WHEN 'low' THEN 3
		END,
		queued_at ASC`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing queued instances: %w", err)
	}
	defer rows.Close()

	var instances []instance.Instance
	for rows.Next() {
		inst, err := scanInstanceRow(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, *inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating queued instances: %w", err)
	}
	return instances, nil
}
```

Note: `scanInstanceRow` is the existing row-scanning helper used by `rowToInstance`. Check its signature:

```bash
rg -n "func rowToInstance|func scanInstanceRow" adapter/sqlite/instance_repo.go adapter/sqlite/queue_repo.go
```

If the repo uses `rowToInstance` with `generated.Instance` rows (not a raw `*sql.Rows`), replace the hand-written scan with the generated approach. The `ListWaitingWithTimer`/`ListWaitingWithWebhook` methods use `r.queries.ListWaitingWithTimer(ctx)` which returns `[]generated.Instance`; for `ListQueued` there is no generated query, so either (a) add a generated query to `adapter/sqlite/queries/queue.sql` and regenerate, or (b) scan the raw rows into the `generated.Instance` struct via a helper. The simplest path matching the existing `rowToInstance` helper: the existing `rowToInstance` accepts a `generated.Instance`. A raw `*sql.Rows` scan needs a `sql.Scanner` into the same struct fields. Use this scan helper instead:

```go
// scanInstanceRow scans a full instance row from *sql.Rows into a domain
// Instance, mirroring the generated Instance struct column order.
func scanInstanceRow(rows *sql.Rows) (*instance.Instance, error) {
	var i generated.Instance
	if err := rows.Scan(
		&i.ID, &i.OwnerID, &i.WorkflowName, &i.WorkflowSnapshot, &i.State, &i.Priority,
		&i.Variables, &i.OriginalVariables, &i.CurrentNodeID, &i.FailedNodeID,
		&i.CreatedAt, &i.UpdatedAt, &i.CompletedAt, &i.QueuedAt, &i.WaitingUntil,
		&i.WebhookToken, &i.WebhookSecretHash, &i.WebhookReceivedAt,
		&i.Department, &i.Visibility, &i.TriggerSource, &i.ScheduleID, &i.WorkflowID,
	); err != nil {
		return nil, fmt.Errorf("scanning instance row: %w", err)
	}
	return rowToInstance(i)
}
```

Add this helper to `adapter/sqlite/queue_repo.go` (or reuse it from `instance_repo.go` if a similar scanner already exists there — check with `rg -n "func.*scan.*Rows|rows.Scan" adapter/sqlite/instance_repo.go`). The `adapter/sqlite/generated` package must be imported (it already is in `queue_repo.go`). The `database/sql` package must be imported (it already is).

- [x] Step 4: Run the SQLite test, confirm it passes

```bash
go test ./adapter/sqlite/ -run TestListQueued
```

Expected:

```
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.00s
```

- [x] Step 5: Extend every `QueueRepository` test mock with `ListQueued`

The mocks are in:
- `domain/queue/manager_test.go` (`mockQueueRepo`)
- `adapter/http/instance_handler_test.go` (if any queue mock exists there)
- `adapter/http/server_test.go` (`mockQueueRepoForRouter`)
- `adapter/http/instance_list_enriched_e2e_test.go` (`mockQueueRepoForRouter` from Phase 1 Task 5)
- `domain/engine/engine_test.go` (`mockQueueRepo`)

For each, add a `ListQueued` method. For the queue manager's mock, it should return a seeded queued set so the `QueuePositions` tests (Phase 2 Task 2) can drive it:

In `domain/queue/manager_test.go`, add a field to `mockQueueRepo`:

```go
	queuedList     []instance.Instance
	listQueuedErr  error
```

and the method:

```go
func (r *mockQueueRepo) ListQueued(_ context.Context) ([]instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queuedList, r.listQueuedErr
}
```

For the other mocks (engine, http), add a no-op `ListQueued` returning `nil, nil`:

```go
func (m *mockQueueRepo) ListQueued(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
```

Find all of them:

```bash
rg -n "func.*ListWaitingWithWebhook.*context.*\[\]instance.Instance" --type go | rg "_test\.go"
```

- [x] Step 6: Confirm the whole module builds and all tests pass

```bash
go build ./... && go test ./...
```

Expected: no build errors; all package tests pass.

- [x] Step 7: Commit

```bash
git add domain/instance/ports.go adapter/sqlite/queue_repo.go adapter/sqlite/queue_repo_list_queued_test.go domain/queue/manager_test.go adapter/http/instance_handler_test.go adapter/http/server_test.go adapter/http/instance_list_enriched_e2e_test.go domain/engine/engine_test.go
git commit -m "feat(queue): add QueueRepository.ListQueued read method + SQLite impl"
```