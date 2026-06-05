package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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

// scopeFilter builds the row-scope WHERE fragment (visibility + department) and
// its args for one aggregate query.
//
//   - AllDepartments (admin, no filter): every department-visible or public row,
//     no department clause.
//   - scoped: rows in one of the requested departments OR any public run. The
//     public lane (visibility='public') is ALWAYS included because a public run's
//     audience is everyone and, since PR #21, public runs carry no department, so
//     a department IN (…) clause alone would drop them entirely.
func scopeFilter(p dashboard.AggregateParams) (string, []any) {
	if p.AllDepartments {
		// Admin whole-org view: no department restriction (the visibilityFilter
		// each query appends still excludes private runs).
		return "1=1", nil
	}
	if len(p.Departments) == 0 {
		// No visible departments, but PUBLIC runs are visible to everyone — they
		// are the cross-department lane (since PR #21 public runs carry no
		// department). So a zero-department caller still sees only the public lane.
		return "visibility = 'public'", nil
	}
	marks := make([]string, len(p.Departments))
	args := make([]any, len(p.Departments))
	for i, d := range p.Departments {
		marks[i] = "?"
		args[i] = d
	}
	// Department lane (the requested departments) plus the always-visible public
	// (cross-department) lane. visibilityFilter is appended by each query.
	clause := "(department IN (" + strings.Join(marks, ",") + ") OR visibility = 'public')"
	return clause, args
}

// Aggregate runs all four aggregates and assembles the envelope. There is no
// zero-envelope short-circuit: PUBLIC runs are the cross-department lane visible
// to everyone (PR #21), so even a caller with no visible departments sees public
// runs. scopeFilter keeps an admin's whole-org view distinct from a scoped or
// public-only view.
func (r *DashboardRepo) Aggregate(ctx context.Context, p dashboard.AggregateParams) (dashboard.Stats, error) {
	deptClause, deptArgs := scopeFilter(p)
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
	var total int
	var success, failure sql.NullInt64
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
		out.SuccessRate = float64(success.Int64) / float64(total)
		out.FailureRate = float64(failure.Int64) / float64(total)
	}
	return out, nil
}

// p95Duration computes the 95th-percentile completed-run duration over the
// window. The percentile is pushed DOWN into SQL — a single sorted row is
// returned via LIMIT 1 OFFSET <nearest-rank index> — instead of materializing
// every completed-run duration in Go and indexing in memory (PR #21). The
// nearest-rank index (ceil(0.95*N)-1, 0-based, clamped to [0,N-1]) is computed in
// a correlated count subquery so the whole computation runs in the database.
//
// Nearest-rank is preserved exactly: index = CAST(0.95*N + 0.999999 AS INTEGER) -
// 1, matching the prior in-Go formula. The same filter args are bound twice (the
// inner count and the outer ordered scan).
func (r *DashboardRepo) p95Duration(ctx context.Context, deptClause string, deptArgs []any, windowStart string) (float64, error) {
	const where = ` AND ` + visibilityFilter + `
  AND created_at >= ?
  AND state = 'completed' AND completed_at IS NOT NULL`

	query := `
SELECT (julianday(completed_at) - julianday(created_at)) * 86400.0 AS secs
FROM instances
WHERE ` + deptClause + where + `
ORDER BY secs ASC
LIMIT 1 OFFSET (
  SELECT MAX(0, MIN(
    CAST(0.95 * COUNT(*) + 0.999999 AS INTEGER) - 1,
    COUNT(*) - 1
  ))
  FROM instances
  WHERE ` + deptClause + where + `
)`

	// Args order matches placeholder order: outer scan filter, then the inner
	// count subquery filter (deptArgs + windowStart each, twice).
	args := make([]any, 0, 2*(len(deptArgs)+1))
	args = append(args, deptArgs...)
	args = append(args, windowStart)
	args = append(args, deptArgs...)
	args = append(args, windowStart)

	var secs float64
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&secs)
	if err == sql.ErrNoRows {
		return 0, nil // no completed runs in the window
	}
	if err != nil {
		return 0, err
	}
	return secs, nil
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
