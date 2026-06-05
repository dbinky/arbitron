package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/dashboard"
)

// defaultOwnerID is the user seeded by migration 001; instances.owner_id has a
// foreign key to users(id) and PRAGMA foreign_keys is ON, so seeds must use a
// real user.
const defaultOwnerID = "00000000-0000-0000-0000-000000000001"

// seedInstance inserts one instance row with explicit department/visibility/
// state/timestamps. createdAt/completedAt are RFC3339 strings (table stores text).
func seedDashInstance(t *testing.T, db *sql.DB, wf, dept, vis, state, createdAt string, completedAt *string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority,
                       variables, original_variables, created_at, updated_at, completed_at,
                       department, visibility)
VALUES (?, ?, ?, '{}', ?, 'normal', '{}', '{}', ?, ?, ?, ?, ?)`,
		uuid.NewString(), defaultOwnerID, wf, state, createdAt, createdAt, completedAt, dept, vis)
	require.NoError(t, err)
}

func dptr(s string) *string { return &s }

func TestDashboardAggregate_EmptyDepartmentsNoRowsReturnsZeroEnvelope(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: nil,
		Window:      24 * time.Hour,
		Now:         time.Now().UTC(),
	})
	require.NoError(t, err)
	assert.Equal(t, dashboard.Stats{}, out, "no visible departments and no public runs ⇒ all-zero stats")
}

// PR #21 (public lane): a caller with NO visible departments still sees PUBLIC
// runs — public is the cross-department lane visible to everyone. Since public
// runs now carry no department (empty), they are matched by visibility, not a
// department clause.
func TestDashboardAggregate_EmptyDepartments_StillSeesPublicLane(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	// A public run carrying NO department (the post-PR-#21 stamp).
	seedDashInstance(t, db, "pub-wf", "", "public", "completed", recent, nil)
	// A department run the caller cannot see (no departments requested).
	seedDashInstance(t, db, "dep-wf", "engineering", "department", "completed", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: nil, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Outcomes.TotalRuns, "only the public run is visible to a zero-department caller")
}

// PR #21 (public lane): a department-scoped caller sees BOTH their department's
// runs AND public (no-department) runs.
func TestDashboardAggregate_ScopedCaller_SeesDepartmentAndPublicLanes(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	seedDashInstance(t, db, "eng-wf", "engineering", "department", "completed", recent, nil)
	seedDashInstance(t, db, "pub-wf", "", "public", "completed", recent, nil) // public, no dept
	seedDashInstance(t, db, "sales-wf", "sales", "department", "completed", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Outcomes.TotalRuns, "engineering department lane + cross-department public lane")
}

// PR #21 (admin zero-state): AllDepartments aggregates EVERY department-visible
// and public run, with no department filter — the admin first-load whole-org view.
func TestDashboardAggregate_AllDepartments_AggregatesWholeOrg(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	seedDashInstance(t, db, "eng-wf", "engineering", "department", "completed", recent, nil)
	seedDashInstance(t, db, "sales-wf", "sales", "department", "completed", recent, nil)
	seedDashInstance(t, db, "pub-wf", "", "public", "completed", recent, nil)
	// Private runs are still excluded even for the whole-org admin view.
	seedDashInstance(t, db, "priv-wf", "engineering", "private", "completed", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		AllDepartments: true, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, out.Outcomes.TotalRuns,
		"all department-visible + public runs across the org, private excluded")
}

func TestDashboardAggregate_ExcludesPrivateAndForeignDepartments(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")
	done := now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05Z")

	// Included: department + public in engineering.
	seedDashInstance(t, db, "triage", "engineering", "department", "completed", recent, dptr(done))
	seedDashInstance(t, db, "triage", "engineering", "public", "failed", recent, nil)
	// Excluded: private run (members' experiments).
	seedDashInstance(t, db, "triage", "engineering", "private", "completed", recent, dptr(done))
	// Excluded: a different department.
	seedDashInstance(t, db, "triage", "sales", "department", "completed", recent, dptr(done))

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Outcomes.TotalRuns, "only department+public engineering runs count")
	assert.InDelta(t, 0.5, out.Outcomes.FailureRate, 0.001)
	assert.InDelta(t, 0.5, out.Outcomes.SuccessRate, 0.001)
}

func TestDashboardAggregate_WindowBoundExcludesOldRuns(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	inWindow := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")
	outWindow := now.Add(-48 * time.Hour).Format("2006-01-02T15:04:05Z")

	seedDashInstance(t, db, "triage", "engineering", "public", "completed", inWindow, nil)
	seedDashInstance(t, db, "triage", "engineering", "public", "completed", outWindow, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Outcomes.TotalRuns, "the 48h-old run is outside a 24h window")
}

func TestDashboardAggregate_QueueHealthIgnoresWindow(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	old := now.Add(-100 * time.Hour).Format("2006-01-02T15:04:05Z")

	// A running instance created well outside the window must still be counted.
	seedDashInstance(t, db, "triage", "engineering", "public", "running", old, nil)
	seedDashInstance(t, db, "triage", "engineering", "department", "queued", old, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 1 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Queue.Running, "queue health is point-in-time, not windowed")
	assert.Equal(t, 1, out.Queue.Queued)
}

func TestDashboardAggregate_PerWorkflowRankedByRuns(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	for i := 0; i < 3; i++ {
		seedDashInstance(t, db, "busy", "engineering", "public", "completed", recent, nil)
	}
	seedDashInstance(t, db, "quiet", "engineering", "public", "completed", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, out.PerWorkflow, 2)
	assert.Equal(t, "busy", out.PerWorkflow[0].WorkflowName, "highest-volume workflow ranks first")
	assert.Equal(t, 3, out.PerWorkflow[0].Runs)
}

func TestDashboardAggregate_UnionOverMultipleDepartments(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	seedDashInstance(t, db, "a", "engineering", "department", "completed", recent, nil)
	seedDashInstance(t, db, "b", "sales", "department", "completed", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering", "sales"}, Window: 24 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Outcomes.TotalRuns, "union of both departments")
}

// ---------------------------------------------------------------------------
// FA #21 Pass 1: throughput buckets and p95 duration
// ---------------------------------------------------------------------------

// TestDashboardAggregate_ThroughputBuckets verifies that instances created in
// different hours each produce a distinct ThroughputBucket, ordered ASC by
// bucket start. Instances within the same hour are counted together.
func TestDashboardAggregate_ThroughputBuckets(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC().Truncate(time.Hour).Add(time.Hour) // top of the next hour

	// Two instances in hour-2 and one in hour-1 (both within a 5-hour window).
	hour1 := now.Add(-3 * time.Hour).Format("2006-01-02T15:04:05Z")
	hour2a := now.Add(-1*time.Hour - 30*time.Minute).Format("2006-01-02T15:04:05Z")
	hour2b := now.Add(-1*time.Hour - 15*time.Minute).Format("2006-01-02T15:04:05Z")
	seedDashInstance(t, db, "wf", "engineering", "public", "completed", hour1, nil)
	seedDashInstance(t, db, "wf", "engineering", "public", "completed", hour2a, nil)
	seedDashInstance(t, db, "wf", "engineering", "public", "completed", hour2b, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 5 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, out.Throughput, 2, "two distinct hourly buckets must appear")
	assert.Equal(t, 1, out.Throughput[0].Runs, "first (older) bucket has 1 run")
	assert.Equal(t, 2, out.Throughput[1].Runs, "second (newer) bucket has 2 runs")
	assert.True(t, out.Throughput[0].Bucket.Before(out.Throughput[1].Bucket),
		"buckets must be ordered ASC by time")
}

// FA #21 Pass 2: robustness

// TestDashboardAggregate_NoCompletedRuns_ZeroP95 verifies that when there are
// no completed runs in the window (only running/queued), P95DurationSeconds is
// 0.0 and the call does not error or produce NaN.
func TestDashboardAggregate_NoCompletedRuns_ZeroP95(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")

	seedDashInstance(t, db, "wf", "engineering", "public", "running", recent, nil)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 6 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, 0.0, out.Outcomes.P95DurationSeconds,
		"p95 must be 0.0 (not NaN) when there are no completed runs")
	assert.Equal(t, 1, out.Outcomes.TotalRuns, "TotalRuns counts all visible-state runs in the window")
	assert.Equal(t, 0.0, out.Outcomes.SuccessRate, "SuccessRate is 0 when there are no completed runs")
}

// TestDashboardAggregate_P95Duration verifies that P95DurationSeconds is
// computed from completed runs. With 10 completed runs of known durations
// (1s..10s), the p95 should be at or near the 10th-percentile upper bound.
func TestDashboardAggregate_P95Duration(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	base := now.Add(-2 * time.Hour)

	// Seed 10 completed runs with durations 1s, 2s, ... 10s.
	for i := 1; i <= 10; i++ {
		created := base.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05Z")
		completed := base.Add(time.Duration(i)*time.Second + time.Duration(i)*time.Second).Format("2006-01-02T15:04:05Z")
		seedDashInstance(t, db, "wf", "engineering", "public", "completed", created, dptr(completed))
	}

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 6 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	// p95 of 10 values [2s, 4s, ..., 20s] should be ≥ the 9th value (18s) and ≤ 20s.
	assert.Greater(t, out.Outcomes.P95DurationSeconds, 0.0,
		"P95DurationSeconds must be positive when completed runs exist")
	assert.Equal(t, 10, out.Outcomes.TotalRuns)
}

// PR #21 (p95 SQL push-down): the SQL nearest-rank percentile must equal the
// prior in-Go formula exactly. Each run i has duration i seconds, so durations
// are [1,2,…,10]; the nearest-rank index ceil(0.95*10)-1 = 9 selects the 10th
// value = 10s.
func TestDashboardAggregate_P95Duration_ExactNearestRank(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	base := now.Add(-2 * time.Hour)

	// 10 completed runs with durations 1s, 2s, ... 10s.
	for i := 1; i <= 10; i++ {
		created := base.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05Z")
		completed := base.Add(time.Duration(i)*time.Second + time.Duration(i)*time.Second).Format("2006-01-02T15:04:05Z")
		seedDashInstance(t, db, "wf", "engineering", "public", "completed", created, dptr(completed))
	}

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 6 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.InDelta(t, 10.0, out.Outcomes.P95DurationSeconds, 0.01,
		"nearest-rank p95 of [1..10] is the 10th value = 10s (SQL push-down matches the in-Go formula)")
}

// A single completed run: nearest-rank index ceil(0.95*1)-1 = 0, so p95 is that
// one run's duration (the offset never goes negative).
func TestDashboardAggregate_P95Duration_SingleRun(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)
	now := time.Now().UTC()
	created := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05Z")
	completed := now.Add(-1*time.Hour + 5*time.Second).Format("2006-01-02T15:04:05Z")
	seedDashInstance(t, db, "wf", "engineering", "public", "completed", created, dptr(completed))

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: []string{"engineering"}, Window: 6 * time.Hour, Now: now,
	})
	require.NoError(t, err)
	assert.InDelta(t, 5.0, out.Outcomes.P95DurationSeconds, 0.05,
		"a single completed run is its own p95 (offset clamped to 0)")
}
