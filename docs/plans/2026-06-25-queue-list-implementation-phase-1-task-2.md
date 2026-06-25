# Phase 1 — Task 2: NodeExecutionRepository.AggregateByInstances port + SQLite impl + mock

**Goal:** Add a read-only aggregate method to `NodeExecutionRepository` that collapses many `NodeExecution` rows per instance into one per-instance aggregate (latest model, max attempt/retry, first-started), and implement it as a single grouped SQLite query plus a test mock.

**Architecture:** Hexagonal — the new method is an additive contract on the existing `instance.NodeExecutionRepository` port (`domain/instance/ports.go`). The SQLite adapter implements it as one grouped `SELECT ... GROUP BY instance_id` in `adapter/sqlite/node_execution_repo.go` backed by a hand-written query (the repo already uses both sqlc-generated and hand-written queries; the generated tree is not regenerated for additive read methods, matching the `ref_backfill_repo.go` precedent). A test mock is added so the enrichment-step and handler tests can drive the aggregate without a DB. No wiring here — the enrichment step (Task 3) calls it; composition-root wiring is Task 5.

**Tech Stack:** Go, `database/sql`, SQLite. Tests with `testing` + `testify` + an in-memory SQLite DB (the `integration_test.go` pattern).

---

### Task 2: AggregateByInstances

**Files:**
- Modify: `domain/instance/ports.go:44-49` (extend `NodeExecutionRepository` interface)
- Create: `domain/instance/aggregate.go` (the `NodeExecutionAggregate` result type)
- Create: `domain/instance/aggregate_test.go` (port-level type test)
- Modify: `adapter/sqlite/node_execution_repo.go` (add `AggregateByInstances`)
- Modify: `adapter/sqlite/node_execution_repo_test.go` (new; if absent, create) — add SQLite impl test
- Modify: `adapter/http/instance_handler_test.go:212-247` (extend `mockNodeExecRepo`)

- [x] Step 1: Define the `NodeExecutionAggregate` result type and the failing port test

Create `domain/instance/aggregate.go`:

```go
package instance

import (
	"time"

	"github.com/google/uuid"
)

// NodeExecutionAggregate is the per-instance collapse of all NodeExecution
// rows for a list page. It carries the latest model used, the max attempt
// (retry count), and the earliest started_at (first-started). All fields are
// zero-valued when the instance has no node executions (a queued run).
type NodeExecutionAggregate struct {
	InstanceID   uuid.UUID
	LatestModel  string
	MaxAttempt   int
	FirstStarted time.Time
}
```

Create `domain/instance/aggregate_test.go`:

```go
package instance_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNodeExecutionAggregate_ZeroValue(t *testing.T) {
	var a instance.NodeExecutionAggregate
	assert.Equal(t, uuid.Nil, a.InstanceID)
	assert.Equal(t, "", a.LatestModel, "queued instance: no model")
	assert.Equal(t, 0, a.MaxAttempt, "queued instance: 0 retries")
	assert.True(t, a.FirstStarted.IsZero(), "queued instance: no start time")
}

func TestNodeExecutionAggregate_Populated(t *testing.T) {
	id := uuid.New()
	started := time.Now()
	a := instance.NodeExecutionAggregate{
		InstanceID:   id,
		LatestModel:  "gpt-4o",
		MaxAttempt:   3,
		FirstStarted: started,
	}
	assert.Equal(t, id, a.InstanceID)
	assert.Equal(t, "gpt-4o", a.LatestModel)
	assert.Equal(t, 3, a.MaxAttempt)
	assert.Equal(t, started, a.FirstStarted)
}
```

- [x] Step 2: Run it, confirm it passes (the type exists already from this step)

```bash
go test ./domain/instance/
```

Expected:

```
ok  	github.com/arbitron/arbitron/domain/instance	0.00s
```

- [x] Step 3: Add `AggregateByInstances` to the port and run to confirm the SQLite adapter no longer satisfies the interface

In `domain/instance/ports.go`, extend the `NodeExecutionRepository` interface (after `GetByInstanceAndNode` at line 48):

```go
// NodeExecutionRepository defines the port for persisting and querying node executions.
type NodeExecutionRepository interface {
	Create(ctx context.Context, exec *NodeExecution) error
	Update(ctx context.Context, exec *NodeExecution) error
	ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]NodeExecution, error)
	GetByInstanceAndNode(ctx context.Context, instanceID uuid.UUID, nodeID string) (*NodeExecution, error)
	// AggregateByInstances collapses all NodeExecution rows for the given
	// instance IDs into one aggregate per instance (latest model, max attempt,
	// first started). Returns a map keyed by instance ID. Instances with no
	// executions are absent from the map (the caller treats absence as the
	// zero value). An empty ids slice returns an empty map, not an error.
	AggregateByInstances(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]NodeExecutionAggregate, error)
}
```

Run:

```bash
go build ./...
```

Expected: build failure — `*sqlite.NodeExecutionRepo` does not satisfy `instance.NodeExecutionRepository` (missing `AggregateByInstances`); likewise the test mocks in `adapter/http/instance_handler_test.go` and `domain/engine` mocks.

- [x] Step 4: Implement the SQLite `AggregateByInstances` method

In `adapter/sqlite/node_execution_repo.go`, add the import `"strings"` and `"github.com/google/uuid"` (already imported) and this method:

```go
// AggregateByInstances collapses node executions for the given instance IDs
// into one aggregate per instance. It uses a single grouped query with a
// placeholder per id. An empty ids slice returns an empty map.
func (r *NodeExecutionRepo) AggregateByInstances(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	out := make(map[uuid.UUID]instance.NodeExecutionAggregate, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = uuidToString(id)
	}
	// latest_model is the model of the MOST-RECENTLY-STARTED execution that
	// recorded one (the design's "latest model"), NOT MAX(llm_model) — an
	// alphabetical max would pick an arbitrary model for a multi-step run.
	inClause := strings.Join(placeholders, ",")
	query := `SELECT ne.instance_id,
		COALESCE((
			SELECT x.llm_model FROM node_executions x
			WHERE x.instance_id = ne.instance_id
			  AND x.llm_model IS NOT NULL AND x.llm_model != ''
			ORDER BY x.started_at DESC LIMIT 1
		), '') AS latest_model,
		COALESCE(MAX(ne.attempt), 0) AS max_attempt,
		COALESCE(MIN(ne.started_at), '') AS first_started
	FROM node_executions ne
	WHERE ne.instance_id IN (` + inClause + `)
	GROUP BY ne.instance_id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregating node executions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var instIDStr, model, firstStartedStr string
		var maxAttempt int64
		if err := rows.Scan(&instIDStr, &model, &maxAttempt, &firstStartedStr); err != nil {
			return nil, fmt.Errorf("scanning node execution aggregate: %w", err)
		}
		instID, err := parseUUID(instIDStr)
		if err != nil {
			return nil, fmt.Errorf("parsing instance id in aggregate: %w", err)
		}
		var firstStarted time.Time
		if firstStartedStr != "" {
			firstStarted, err = parseTime(firstStartedStr)
			if err != nil {
				return nil, fmt.Errorf("parsing first_started in aggregate: %w", err)
			}
		}
		out[instID] = instance.NodeExecutionAggregate{
			InstanceID:   instID,
			LatestModel:  model,
			MaxAttempt:   int(maxAttempt),
			FirstStarted: firstStarted,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating node execution aggregates: %w", err)
	}
	return out, nil
}
```

Add `"time"` to the import block of `adapter/sqlite/node_execution_repo.go` if not already present (it is not in the current imports — add it).

- [x] Step 5: Write the SQLite implementation test

Create `adapter/sqlite/node_execution_repo_aggregate_test.go`:

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

func TestAggregateByInstances_HappyPath(t *testing.T) {
	td := setupTestDB(t)
	repo := NewNodeExecutionRepo(td.DB)
	instA := uuid.New()
	instB := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	modelA1 := "gpt-4o"
	modelA2 := "gpt-4o-2024-11" // started LATER than modelA1 -> this is "latest"
	modelB := "claude-3"
	execA1 := &instance.NodeExecution{ID: uuid.New(), InstanceID: instA, NodeID: "n1", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: now, LLMModel: &modelA1}
	execA2 := &instance.NodeExecution{ID: uuid.New(), InstanceID: instA, NodeID: "n2", Attempt: 3, Status: instance.NodeStatusFailed, StartedAt: now.Add(time.Minute), LLMModel: &modelA2}
	execB1 := &instance.NodeExecution{ID: uuid.New(), InstanceID: instB, NodeID: "n1", Attempt: 2, Status: instance.NodeStatusSucceeded, StartedAt: now.Add(2 * time.Minute), LLMModel: &modelB}

	require.NoError(t, repo.Create(context.Background(), execA1))
	require.NoError(t, repo.Create(context.Background(), execA2))
	require.NoError(t, repo.Create(context.Background(), execB1))

	got, err := repo.AggregateByInstances(context.Background(), []uuid.UUID{instA, instB, uuid.New()})
	require.NoError(t, err)
	assert.Len(t, got, 2, "instances with no executions are absent")

	a := got[instA]
	assert.Equal(t, "gpt-4o-2024-11", a.LatestModel, "latest model = most-recently-started execution's model")
	assert.Equal(t, 3, a.MaxAttempt, "max attempt across nodes")
	assert.Equal(t, now, a.FirstStarted, "earliest started_at")

	b := got[instB]
	assert.Equal(t, "claude-3", b.LatestModel)
	assert.Equal(t, 2, b.MaxAttempt)
}

func TestAggregateByInstances_EmptyIDs(t *testing.T) {
	td := setupTestDB(t)
	repo := NewNodeExecutionRepo(td.DB)
	got, err := repo.AggregateByInstances(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got, "empty ids -> empty map, not error")
}

func TestAggregateByInstances_QueuedInstanceNoExecutions(t *testing.T) {
	td := setupTestDB(t)
	repo := NewNodeExecutionRepo(td.DB)
	id := uuid.New()
	got, err := repo.AggregateByInstances(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	assert.NotContains(t, got, id, "queued instance absent from map")
}

func TestAggregateByInstances_NoModelColumn(t *testing.T) {
	td := setupTestDB(t)
	repo := NewNodeExecutionRepo(td.DB)
	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: id, NodeID: "n1", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: now,
	}))
	got, err := repo.AggregateByInstances(context.Background(), []uuid.UUID{id})
	require.NoError(t, err)
	assert.Equal(t, "", got[id].LatestModel, "script-only node: model empty")
	assert.Equal(t, now, got[id].FirstStarted)
}
```

Note: `setupTestDB(t)` is the existing helper in `adapter/sqlite/integration_test.go` that opens an in-memory SQLite DB and runs migrations. It returns a `testDB` whose `.DB` field is the `*sql.DB`. The test file uses `package sqlite` (internal) to access the helper, matching every other test file in `adapter/sqlite`. Run the test to confirm:

```bash
go test ./adapter/sqlite/ -run TestAggregateByInstances
```

Expected:

```
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.00s
```

- [x] Step 6: Extend the `mockNodeExecRepo` in `instance_handler_test.go` to satisfy the interface

In `adapter/http/instance_handler_test.go`, add to `mockNodeExecRepo` (after `GetByInstanceAndNode`):

```go
func (r *mockNodeExecRepo) AggregateByInstances(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	out := make(map[uuid.UUID]instance.NodeExecutionAggregate, len(ids))
	// latestModelAt tracks the StartedAt of the execution whose model is
	// currently recorded as LatestModel, so "latest" means latest-by-time
	// (matching the SQLite query), not last-in-slice-order.
	latestModelAt := make(map[uuid.UUID]time.Time)
	for _, id := range ids {
		for _, e := range r.executions {
			if e.InstanceID == id {
				agg, ok := out[id]
				if !ok {
					agg = instance.NodeExecutionAggregate{InstanceID: id, FirstStarted: e.StartedAt}
				}
				if e.Attempt > agg.MaxAttempt {
					agg.MaxAttempt = e.Attempt
				}
				if e.StartedAt.Before(agg.FirstStarted) {
					agg.FirstStarted = e.StartedAt
				}
				if e.LLMModel != nil && *e.LLMModel != "" {
					if prev, seen := latestModelAt[id]; !seen || e.StartedAt.After(prev) {
						agg.LatestModel = *e.LLMModel
						latestModelAt[id] = e.StartedAt
					}
				}
				out[id] = agg
			}
		}
	}
	return out, nil
}
```

- [x] Step 7: Extend every other mock that implements `NodeExecutionRepository` to satisfy the interface

Search for all implementations:

```bash
rg -n "func.*GetByInstanceAndNode" --type go | rg "_test\.go"
```

For each mock found (e.g. `domain/engine` test mocks, `adapter/mcp` test stubs), add an `AggregateByInstances` method returning `(map[uuid.UUID]instance.NodeExecutionAggregate, error)` with an empty-map or panic body matching that mock's convention (existing methods either `panic("unused")` or return zero values). Example for an engine-test mock that panics on unused methods:

```go
func (m *mockNodeExecutionRepo) AggregateByInstances(context.Context, []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	return map[uuid.UUID]instance.NodeExecutionAggregate{}, nil
}
```

- [x] Step 8: Confirm the whole module builds and all tests pass

```bash
go build ./... && go test ./...
```

Expected: no build errors; all package tests pass.

- [ ] Step 9: Commit

```bash
git add domain/instance/ports.go domain/instance/aggregate.go domain/instance/aggregate_test.go adapter/sqlite/node_execution_repo.go adapter/sqlite/node_execution_repo_aggregate_test.go adapter/http/instance_handler_test.go
git commit -m "feat(instance): add NodeExecutionRepository.AggregateByInstances + SQLite impl"
```