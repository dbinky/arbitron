package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

type testDB struct {
	Pool    *pgxpool.Pool
	ConnStr string
}

// setupTestDB starts a Postgres container, runs migrations, and returns a connection pool.
// The container and pool are cleaned up when t ends.
func setupTestDB(t *testing.T) testDB {
	t.Helper()

	ctx := context.Background()

	pgc, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("arbitron_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = pgc.Terminate(ctx)
	})

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	err = RunMigrations(connStr)
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return testDB{Pool: pool, ConnStr: connStr}
}

// ---------------------------------------------------------------------------
// Migration tests
// ---------------------------------------------------------------------------

func TestRunMigrations_Up(t *testing.T) {
	// setupTestDB already runs migrations — this test verifies it succeeds.
	db := setupTestDB(t)
	pool := db.Pool

	// Verify key tables exist by querying them.
	row := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM users")
	var count int
	require.NoError(t, row.Scan(&count))
	// Default admin user should be seeded by migration 001.
	assert.Equal(t, 1, count)
}

func TestRunMigrations_Idempotent(t *testing.T) {
	db := setupTestDB(t)

	// Running migrations a second time must not error (ErrNoChange is swallowed).
	err := RunMigrations(db.ConnStr)
	require.NoError(t, err)
}

func TestRunMigrations_DefaultAdminSeeded(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool

	row := pool.QueryRow(context.Background(),
		"SELECT username, role FROM users WHERE username = 'local'")
	var username, role string
	require.NoError(t, row.Scan(&username, &role))
	assert.Equal(t, "local", username)
	assert.Equal(t, "admin", role)
}

// ---------------------------------------------------------------------------
// UserRepo tests
// ---------------------------------------------------------------------------

func TestUserRepo_GetDefaultUser(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	repo := NewUserRepo(pool)

	id, err := repo.GetDefaultUser()
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
}

// ---------------------------------------------------------------------------
// InstanceRepo tests
// ---------------------------------------------------------------------------

func newTestInstance(t *testing.T, ownerID uuid.UUID) *instance.Instance {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &instance.Instance{
		ID:      uuid.New(),
		OwnerID: ownerID,
		WorkflowName: "test-workflow",
		WorkflowSnapshot: map[string]any{"name": "test-workflow"},
		State:             instance.StateRunning,
		Priority:          instance.PriorityNormal,
		Variables:         map[string]any{"key": "value"},
		OriginalVariables: map[string]any{"key": "value"},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestInstanceRepo_CreateAndGet(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, err := userRepo.GetDefaultUser()
	require.NoError(t, err)

	inst := newTestInstance(t, ownerID)
	err = repo.Create(inst)
	require.NoError(t, err)

	got, err := repo.Get(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, inst.ID, got.ID)
	assert.Equal(t, inst.WorkflowName, got.WorkflowName)
	assert.Equal(t, instance.StateRunning, got.State)
	assert.Equal(t, instance.PriorityNormal, got.Priority)
	assert.Equal(t, "value", got.Variables["key"])
	assert.Equal(t, "value", got.OriginalVariables["key"])
	assert.Nil(t, got.CurrentNodeID)
	assert.Nil(t, got.FailedNodeID)
}

func TestInstanceRepo_GetNotFound(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	repo := NewInstanceRepo(pool)

	_, err := repo.Get(uuid.New())
	require.Error(t, err)
}

func TestInstanceRepo_UpdateState(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(inst))

	require.NoError(t, repo.UpdateState(inst.ID, instance.StatePaused))

	got, err := repo.Get(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StatePaused, got.State)
}

func TestInstanceRepo_UpdateVariables(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(inst))

	newVars := map[string]any{"key": "updated", "extra": float64(42)}
	require.NoError(t, repo.UpdateVariables(inst.ID, newVars))

	got, err := repo.Get(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Variables["key"])
	assert.Equal(t, float64(42), got.Variables["extra"])
}

func TestInstanceRepo_UpdateCurrentNode(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(inst))

	require.NoError(t, repo.UpdateCurrentNode(inst.ID, "node-b"))

	got, err := repo.Get(inst.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentNodeID)
	assert.Equal(t, "node-b", *got.CurrentNodeID)
}

func TestInstanceRepo_SetFailed(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(inst))

	require.NoError(t, repo.SetFailed(inst.ID, "node-b"))

	got, err := repo.Get(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, got.State)
	require.NotNil(t, got.FailedNodeID)
	assert.Equal(t, "node-b", *got.FailedNodeID)
	assert.NotNil(t, got.CompletedAt)
}

func TestInstanceRepo_SetCompleted(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(inst))

	require.NoError(t, repo.SetCompleted(inst.ID))

	got, err := repo.Get(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, got.State)
	assert.NotNil(t, got.CompletedAt)
}

func TestInstanceRepo_List(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()

	inst1 := newTestInstance(t, ownerID)
	inst2 := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(inst1))
	require.NoError(t, repo.Create(inst2))
	require.NoError(t, repo.UpdateState(inst2.ID, instance.StateCompleted))

	// List all
	all, err := repo.List(instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)

	// Filter by state
	running := instance.StateRunning
	filtered, err := repo.List(instance.ListFilters{State: &running, Limit: 100})
	require.NoError(t, err)
	for _, s := range filtered {
		assert.Equal(t, instance.StateRunning, s.State)
	}

	// Filter by workflow name
	wfName := "test-workflow"
	byName, err := repo.List(instance.ListFilters{WorkflowName: &wfName, Limit: 100})
	require.NoError(t, err)
	for _, s := range byName {
		assert.Equal(t, "test-workflow", s.WorkflowName)
	}
}

func TestInstanceRepo_ListRunning(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()

	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(inst))

	running, err := repo.ListRunning()
	require.NoError(t, err)

	ids := make([]uuid.UUID, len(running))
	for i, r := range running {
		ids[i] = r.ID
	}
	assert.Contains(t, ids, inst.ID)
}

// ---------------------------------------------------------------------------
// NodeExecutionRepo tests
// ---------------------------------------------------------------------------

func TestNodeExecutionRepo_CreateAndListByInstance(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	instRepo := NewInstanceRepo(pool)
	repo := NewNodeExecutionRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(inst))

	now := time.Now().UTC().Truncate(time.Millisecond)
	exec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Attempt:    1,
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  now,
	}
	require.NoError(t, repo.Create(exec))

	executions, err := repo.ListByInstance(inst.ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, exec.ID, executions[0].ID)
	assert.Equal(t, "node-a", executions[0].NodeID)
	assert.Equal(t, instance.NodeStatusSucceeded, executions[0].Status)
}

func TestNodeExecutionRepo_Update(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	instRepo := NewInstanceRepo(pool)
	repo := NewNodeExecutionRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(inst))

	now := time.Now().UTC().Truncate(time.Millisecond)
	exec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Attempt:    1,
		Status:     instance.NodeStatusFailed,
		StartedAt:  now,
	}
	require.NoError(t, repo.Create(exec))

	// Update to skipped
	exec.Status = instance.NodeStatusSkipped
	require.NoError(t, repo.Update(exec))

	executions, err := repo.ListByInstance(inst.ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, instance.NodeStatusSkipped, executions[0].Status)
}

// ---------------------------------------------------------------------------
// VariableSnapshotRepo tests
// ---------------------------------------------------------------------------

func TestVariableSnapshotRepo_CreateAndListByInstance(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	instRepo := NewInstanceRepo(pool)
	repo := NewVariableSnapshotRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser()
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(inst))

	snap := &instance.VariableSnapshot{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Variables:  map[string]any{"result": "ok"},
		CapturedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, repo.Create(snap))

	snaps, err := repo.ListByInstance(inst.ID)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, snap.ID, snaps[0].ID)
	assert.Equal(t, "node-a", snaps[0].NodeID)
	assert.Equal(t, "ok", snaps[0].Variables["result"])
}
