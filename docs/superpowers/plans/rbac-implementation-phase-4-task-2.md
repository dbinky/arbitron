# RBAC Phase 4 — Task 2: SQLite StatsRepository (aggregate queries)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `dashboard.StatsRepository` over SQLite with four read-only aggregates — queue health, throughput, outcomes, per-workflow — all filtered by `department IN (…) AND visibility IN ('department','public')` and a `created_at >= window` bound, served by `idx_instances_dept_vis`.

**Architecture:** A new `adapter/sqlite/dashboard_repo.go` `DashboardRepo` implementing the port via raw parameterized `database/sql` queries (the same approach the existing `InstanceRepo.Stats` uses for aggregates — dynamic `IN (…)` lists do not fit sqlc's static codegen). Queue health is point-in-time (no window); the other three respect the window. An empty department set short-circuits to a zero envelope.

**Tech Stack:** Go, `database/sql` with `modernc.org/sqlite` (in-tests), SQLite date functions (`strftime`, `julianday`), strict TDD against an in-memory DB with migrations applied.

**Dependencies:** Task 1 (the `dashboard.StatsRepository` port + result structs). Phase 3 schema (`instances.department`, `instances.visibility`, `idx_instances_dept_vis`) assumed present. No dependency on Tasks 3-8.

---

## Context You Need

- `adapter/sqlite/instance_repo.go` `Stats` (lines ~284) shows the house style for an aggregate: build a raw SQL string, `r.db.QueryRowContext(...).Scan(...)`, wrap errors with `fmt.Errorf("...: %w", err)`. Times are stored as text (`strftime('%Y-%m-%dT%H:%M:%SZ')`); use the existing `timeToString(t)` helper from `adapter/sqlite/convert.go`.
- `instances` columns relevant here: `state`, `workflow_name`, `created_at`, `completed_at`, `department`, `visibility`.
- Test DB helper: existing repo tests (e.g. `adapter/sqlite/dismissal_repo_test.go`) open an in-memory DB and run migrations. Reuse the same `newTestDB(t)` / `openTestDB` helper they use — grep the test file you are mirroring to find the exact name before writing.
- `idx_instances_dept_vis` is `(department, visibility, state, created_at)` — every aggregate's WHERE leads with `department IN (…) AND visibility IN ('department','public')` so the planner can use it.

## File Structure

- Create `adapter/sqlite/dashboard_repo.go` — `DashboardRepo` with `Aggregate` + four private helpers.
- Create `adapter/sqlite/dashboard_repo_test.go` — TDD scenarios over a seeded in-memory DB.

---

### Task 2: SQLite StatsRepository

**Files:**
- Create: `adapter/sqlite/dashboard_repo.go`
- Test: `adapter/sqlite/dashboard_repo_test.go`

- [ ] **Step 1: Find the test-DB helper used by sibling repo tests**

Run: `grep -n "func newTestDB\|func openTestDB\|func setupTestDB\|Migrate(" adapter/sqlite/*_test.go adapter/sqlite/migrate.go | head`
Expected: identifies the helper name (e.g. `newTestDB(t)`) and how migrations are applied. Use that exact helper in Step 2. This plan assumes `newTestDB(t)` returns `(*sql.DB)`; substitute the real name.

- [ ] **Step 2: Write the failing test — empty department set short-circuits to zero envelope**

Create `adapter/sqlite/dashboard_repo_test.go`:

```go
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/dashboard"
	sqlite "github.com/arbitron/arbitron/adapter/sqlite"
)

func TestDashboardAggregate_EmptyDepartmentsReturnsZeroEnvelope(t *testing.T) {
	db := newTestDB(t) // substitute the real helper name from Step 1
	repo := sqlite.NewDashboardRepo(db)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: nil,
		Window:      24 * time.Hour,
		Now:         time.Now().UTC(),
	})
	require.NoError(t, err)
	assert.Equal(t, dashboard.Stats{}, out, "no visible departments ⇒ all-zero stats, no DB hit needed")
}
```

- [ ] **Step 3: Run it to verify it fails to compile**

Run: `go test ./adapter/sqlite/ -run TestDashboardAggregate_EmptyDepartmentsReturnsZeroEnvelope -v`
Expected: FAIL — `undefined: sqlite.NewDashboardRepo`.

- [ ] **Step 4: Create the repo skeleton with the empty short-circuit**

Create `adapter/sqlite/dashboard_repo.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/dashboard"
)

// DashboardRepo implements dashboard.StatsRepository over SQLite. Aggregates are
// raw parameterized queries (not sqlc) because the department IN (…) list is
// dynamic; all are filtered to department-visible/public rows in the requested
// departments and ride idx_instances_dept_vis from Phase 3.
type DashboardRepo struct {
	db *sql.DB
}

// NewDashboardRepo builds the repo over an open database handle.
func NewDashboardRepo(db *sql.DB) *DashboardRepo {
	return &DashboardRepo{db: db}
}

// Compile-time assertion that the repo satisfies the port.
var _ dashboard.StatsRepository = (*DashboardRepo)(nil)

// visibilityFilter is the shared predicate: only department-visible or public
// runs are aggregated (members' private experiments are excluded, per spec).
const visibilityFilter = "visibility IN ('department','public')"

// deptPlaceholders builds "department IN (?,?,…)" and the matching args slice.
func deptPlaceholders(depts []string) (string, []any) {
	marks := make([]string, len(depts))
	args := make([]any, len(depts))
	for i, d := range depts {
		marks[i] = "?"
		args[i] = d
	}
	return "department IN (" + strings.Join(marks, ",") + ")", args
}

// Aggregate runs all four aggregates and assembles the envelope. An empty
// department set means the caller can see nothing ⇒ zero envelope, no queries.
func (r *DashboardRepo) Aggregate(ctx context.Context, p dashboard.AggregateParams) (dashboard.Stats, error) {
	if len(p.Departments) == 0 {
		return dashboard.Stats{}, nil
	}

	deptClause, deptArgs := deptPlaceholders(p.Departments)
	windowStart := timeToString(p.Now.Add(-p.Window))

	queue, err := r.queueHealth(ctx, deptClause, deptArgs)
	if err != nil {
		return dashboard.Stats{}, fmt.Errorf("dashboard queue health: %w", err)
	}
	throughput, err := r.throughput(ctx, deptClause, deptArgs, windowStart)
	if err != nil {
		return dashboard.Stats{}, fmt.Errorf("dashboard throughput: %w", err)
	}
	outcomes, err := r.outcomes(ctx, deptClause, deptArgs, windowStart)
	if err != nil {
		return dashboard.Stats{}, fmt.Errorf("dashboard outcomes: %w", err)
	}
	perWorkflow, err := r.perWorkflow(ctx, deptClause, deptArgs, windowStart)
	if err != nil {
		return dashboard.Stats{}, fmt.Errorf("dashboard per-workflow: %w", err)
	}

	return dashboard.Stats{
		Queue:       queue,
		Throughput:  throughput,
		Outcomes:    outcomes,
		PerWorkflow: perWorkflow,
	}, nil
}
```

- [ ] **Step 5: Run the empty-envelope test to verify it passes**

Run: `go test ./adapter/sqlite/ -run TestDashboardAggregate_EmptyDepartmentsReturnsZeroEnvelope -v`
Expected: FAIL to COMPILE — the four helpers are not defined yet. Add them in Step 6 before re-running.

- [ ] **Step 6: Implement the four aggregate helpers**

Append to `adapter/sqlite/dashboard_repo.go`:

```go
// queueHealth returns CURRENT counts by state (no window) for the visible set.
func (r *DashboardRepo) queueHealth(ctx context.Context, deptClause string, deptArgs []any) (dashboard.QueueHealth, error) {
	query := `
SELECT
  SUM(CASE WHEN state = 'running'  THEN 1 ELSE 0 END) AS running,
  SUM(CASE WHEN state = 'queued'   THEN 1 ELSE 0 END) AS queued,
  SUM(CASE WHEN state = 'waiting'  THEN 1 ELSE 0 END) AS waiting,
  SUM(CASE WHEN state = 'paused'   THEN 1 ELSE 0 END) AS paused,
  SUM(CASE WHEN state = 'failed'   THEN 1 ELSE 0 END) AS failed
FROM instances
WHERE ` + deptClause + ` AND ` + visibilityFilter

	var running, queued, waiting, paused, failed sql.NullInt64
	if err := r.db.QueryRowContext(ctx, query, deptArgs...).Scan(&running, &queued, &waiting, &paused, &failed); err != nil {
		return dashboard.QueueHealth{}, err
	}
	return dashboard.QueueHealth{
		Running:                int(running.Int64),
		Queued:                 int(queued.Int64),
		Waiting:                int(waiting.Int64),
		Paused:                 int(paused.Int64),
		FailedNeedingAttention: int(failed.Int64),
	}, nil
}

// throughput returns run counts grouped by hour over the window.
func (r *DashboardRepo) throughput(ctx context.Context, deptClause string, deptArgs []any, windowStart string) ([]dashboard.ThroughputBucket, error) {
	query := `
SELECT strftime('%Y-%m-%dT%H:00:00Z', created_at) AS bucket, COUNT(*) AS runs
FROM instances
WHERE ` + deptClause + ` AND ` + visibilityFilter + `
  AND created_at >= ?
GROUP BY bucket
ORDER BY bucket ASC`

	args := append(append([]any{}, deptArgs...), windowStart)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dashboard.ThroughputBucket
	for rows.Next() {
		var bucketStr string
		var runs int
		if err := rows.Scan(&bucketStr, &runs); err != nil {
			return nil, err
		}
		bucket, err := parseTime(bucketStr)
		if err != nil {
			return nil, err
		}
		out = append(out, dashboard.ThroughputBucket{Bucket: bucket, Runs: runs})
	}
	return out, rows.Err()
}

// outcomes returns success/failure rates and duration aggregates over the window.
func (r *DashboardRepo) outcomes(ctx context.Context, deptClause string, deptArgs []any, windowStart string) (dashboard.Outcomes, error) {
	query := `
SELECT
  COUNT(*) AS total,
  SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END) AS success,
  SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END) AS failure,
  COALESCE(AVG(CASE WHEN state = 'completed' AND completed_at IS NOT NULL
    THEN (julianday(completed_at) - julianday(created_at)) * 86400.0 END), 0) AS avg_secs
FROM instances
WHERE ` + deptClause + ` AND ` + visibilityFilter + `
  AND created_at >= ?`

	args := append(append([]any{}, deptArgs...), windowStart)
	var total, success, failure int
	var avgSecs float64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&total, &success, &failure, &avgSecs); err != nil {
		return dashboard.Outcomes{}, err
	}

	p95, err := r.p95Duration(ctx, deptClause, deptArgs, windowStart)
	if err != nil {
		return dashboard.Outcomes{}, err
	}

	out := dashboard.Outcomes{TotalRuns: total, AvgDurationSeconds: avgSecs, P95DurationSeconds: p95}
	if total > 0 {
		out.SuccessRate = float64(success) / float64(total)
		out.FailureRate = float64(failure) / float64(total)
	}
	return out, nil
}

// p95Duration computes the 95th-percentile completed-run duration over the
// window using a row-count offset (portable, no PERCENTILE_CONT in SQLite).
func (r *DashboardRepo) p95Duration(ctx context.Context, deptClause string, deptArgs []any, windowStart string) (float64, error) {
	durs := `
SELECT (julianday(completed_at) - julianday(created_at)) * 86400.0 AS secs
FROM instances
WHERE ` + deptClause + ` AND ` + visibilityFilter + `
  AND created_at >= ?
  AND state = 'completed' AND completed_at IS NOT NULL
ORDER BY secs ASC`

	args := append(append([]any{}, deptArgs...), windowStart)
	rows, err := r.db.QueryContext(ctx, durs, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var all []float64
	for rows.Next() {
		var s float64
		if err := rows.Scan(&s); err != nil {
			return 0, err
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(all) == 0 {
		return 0, nil
	}
	// Nearest-rank: index of ceil(0.95*N)-1, already sorted ascending.
	idx := int(0.95*float64(len(all)) + 0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(all) {
		idx = len(all) - 1
	}
	return all[idx], nil
}

// perWorkflow returns per-workflow runs/failure-rate/avg-duration, ranked by
// runs desc then failure-rate desc, over the window.
func (r *DashboardRepo) perWorkflow(ctx context.Context, deptClause string, deptArgs []any, windowStart string) ([]dashboard.WorkflowBreakdown, error) {
	query := `
SELECT
  workflow_name,
  COUNT(*) AS runs,
  SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END) AS failures,
  COALESCE(AVG(CASE WHEN state = 'completed' AND completed_at IS NOT NULL
    THEN (julianday(completed_at) - julianday(created_at)) * 86400.0 END), 0) AS avg_secs
FROM instances
WHERE ` + deptClause + ` AND ` + visibilityFilter + `
  AND created_at >= ?
GROUP BY workflow_name
ORDER BY runs DESC, failures DESC`

	args := append(append([]any{}, deptArgs...), windowStart)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dashboard.WorkflowBreakdown
	for rows.Next() {
		var name string
		var runs, failures int
		var avgSecs float64
		if err := rows.Scan(&name, &runs, &failures, &avgSecs); err != nil {
			return nil, err
		}
		row := dashboard.WorkflowBreakdown{WorkflowName: name, Runs: runs, AvgDurationSeconds: avgSecs}
		if runs > 0 {
			row.FailureRate = float64(failures) / float64(runs)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
```

- [ ] **Step 7: Run the empty-envelope test again**

Run: `go test ./adapter/sqlite/ -run TestDashboardAggregate_EmptyDepartmentsReturnsZeroEnvelope -v`
Expected: PASS (and the package now compiles).

- [ ] **Step 8: Add a seed helper and the scope/aggregation tests**

Append to `adapter/sqlite/dashboard_repo_test.go`. The `seedInstance` helper inserts directly via SQL so the test does not depend on the engine's trigger path. Confirm the `instances` insert columns against `adapter/sqlite/migrations/` (department + visibility are Phase 3 additions) before running:

```go
import "github.com/google/uuid" // add to the import block

// seedInstance inserts one instance row with explicit department/visibility/
// state/timestamps. createdAt/completedAt are RFC3339 strings (table stores text).
func seedInstance(t *testing.T, db *sql.DB, wf, dept, vis, state, createdAt string, completedAt *string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority,
                       variables, original_variables, created_at, updated_at, completed_at,
                       department, visibility)
VALUES (?, ?, ?, '{}', ?, 'normal', '{}', '{}', ?, ?, ?, ?, ?)`,
		uuid.NewString(), uuid.NewString(), wf, state, createdAt, createdAt, completedAt, dept, vis)
	require.NoError(t, err)
}

func ptr(s string) *string { return &s }

func TestDashboardAggregate_ExcludesPrivateAndForeignDepartments(t *testing.T) {
	db := newTestDB(t)
	repo := sqlite.NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")
	done := now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05Z")

	// Included: department + public in engineering.
	seedInstance(t, db, "triage", "engineering", "department", "completed", recent, ptr(done))
	seedInstance(t, db, "triage", "engineering", "public", "failed", recent, nil)
	// Excluded: private run (members' experiments).
	seedInstance(t, db, "triage", "engineering", "private", "completed", recent, ptr(done))
	// Excluded: a different department.
	seedInstance(t, db, "triage", "sales", "department", "completed", recent, ptr(done))

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Outcomes.TotalRuns, "only department+public engineering runs count")
	assert.InDelta(t, 0.5, out.Outcomes.FailureRate, 0.001)
	assert.InDelta(t, 0.5, out.Outcomes.SuccessRate, 0.001)
}

func TestDashboardAggregate_WindowBoundExcludesOldRuns(t *testing.T) {
	db := newTestDB(t)
	repo := sqlite.NewDashboardRepo(db)
	now := time.Now().UTC()
	inWindow := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")
	outWindow := now.Add(-48 * time.Hour).Format("2006-01-02T15:04:05Z")

	seedInstance(t, db, "triage", "engineering", "public", "completed", inWindow, nil)
	seedInstance(t, db, "triage", "engineering", "public", "completed", outWindow, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Outcomes.TotalRuns, "the 48h-old run is outside a 24h window")
}

func TestDashboardAggregate_QueueHealthIgnoresWindow(t *testing.T) {
	db := newTestDB(t)
	repo := sqlite.NewDashboardRepo(db)
	now := time.Now().UTC()
	old := now.Add(-100 * time.Hour).Format("2006-01-02T15:04:05Z")

	// A running instance created well outside the window must still be counted.
	seedInstance(t, db, "triage", "engineering", "public", "running", old, nil)
	seedInstance(t, db, "triage", "engineering", "department", "queued", old, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 1 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Queue.Running, "queue health is point-in-time, not windowed")
	assert.Equal(t, 1, out.Queue.Queued)
}

func TestDashboardAggregate_PerWorkflowRankedByRuns(t *testing.T) {
	db := newTestDB(t)
	repo := sqlite.NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	for i := 0; i < 3; i++ {
		seedInstance(t, db, "busy", "engineering", "public", "completed", recent, nil)
	}
	seedInstance(t, db, "quiet", "engineering", "public", "completed", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, out.PerWorkflow, 2)
	assert.Equal(t, "busy", out.PerWorkflow[0].WorkflowName, "highest-volume workflow ranks first")
	assert.Equal(t, 3, out.PerWorkflow[0].Runs)
}

func TestDashboardAggregate_UnionOverMultipleDepartments(t *testing.T) {
	db := newTestDB(t)
	repo := sqlite.NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	seedInstance(t, db, "a", "engineering", "department", "completed", recent, nil)
	seedInstance(t, db, "b", "sales", "department", "completed", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering", "sales"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Outcomes.TotalRuns, "union of both departments")
}
```

- [ ] **Step 9: Run the full repo test suite**

Run: `go test ./adapter/sqlite/ -run TestDashboardAggregate -v`
Expected: PASS (all scenarios green). If `seedInstance` fails on the INSERT, reconcile its column list with the actual `instances` schema in `adapter/sqlite/migrations/` (the Phase 3 `department`/`visibility` columns must exist).

- [ ] **Step 10: Vet and build**

Run: `go vet ./adapter/sqlite/ && go build ./...`
Expected: exit 0

- [ ] **Step 11: Commit**

```bash
git add adapter/sqlite/dashboard_repo.go adapter/sqlite/dashboard_repo_test.go
git commit -m "feat(dashboard): SQLite StatsRepository with four department-scoped aggregates

Queue health (point-in-time), throughput (hourly buckets), outcomes (rates +
avg/p95 duration), per-workflow ranking. All filtered to department-visible/
public rows over idx_instances_dept_vis; empty department set short-circuits.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Notes

- Spec coverage: "four aggregates filtered by department IN (…) AND visibility IN ('department','public') over a window, served by idx_instances_dept_vis" — all four implemented; queue health intentionally window-less per design ("counts grouped by state (current, no window)").
- TDD categories: happy-path (scope inclusion, ranking, union), edge (window boundary, queue-health-ignores-window, empty p95), error (helper errors wrapped + propagated), authorization (private/foreign exclusion is the security boundary), mode (empty-department short-circuit covers the no-visible-departments case).
- Raw SQL (not sqlc) is the deliberate, codebase-consistent choice for dynamic `IN (…)`; the design doc's "Queries (adapter/sqlite/queries)" describes intent, and `InstanceRepo.Stats` already sets this precedent.
- p95 uses a portable nearest-rank computation (no SQLite percentile function); fine for dashboard granularity.
