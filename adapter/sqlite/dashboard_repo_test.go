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

func TestDashboardAggregate_EmptyDepartmentsReturnsZeroEnvelope(t *testing.T) {
	db := setupTestDB(t).DB
	repo := NewDashboardRepo(db)

	out, err := repo.Aggregate(context.Background(), dashboard.AggregateParams{
		Departments: nil,
		Window:      24 * time.Hour,
		Now:         time.Now().UTC(),
	})
	require.NoError(t, err)
	assert.Equal(t, dashboard.Stats{}, out, "no visible departments ⇒ all-zero stats, no DB hit needed")
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
