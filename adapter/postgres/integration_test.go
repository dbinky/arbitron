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

	id, err := repo.GetDefaultUser(context.Background())
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

	ownerID, err := userRepo.GetDefaultUser(context.Background())
	require.NoError(t, err)

	inst := newTestInstance(t, ownerID)
	err = repo.Create(context.Background(), inst)
	require.NoError(t, err)

	got, err := repo.Get(context.Background(), inst.ID)
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

	_, err := repo.Get(context.Background(), uuid.New())
	require.Error(t, err)
}

func TestInstanceRepo_UpdateState(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst))

	require.NoError(t, repo.UpdateState(context.Background(), inst.ID, instance.StatePaused))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StatePaused, got.State)
}

func TestInstanceRepo_UpdateState_Cancelled(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst))

	require.NoError(t, repo.UpdateState(context.Background(), inst.ID, instance.StateCancelled))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCancelled, got.State)
}

func TestInstanceRepo_UpdateVariables(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst))

	newVars := map[string]any{"key": "updated", "extra": float64(42)}
	require.NoError(t, repo.UpdateVariables(context.Background(), inst.ID, newVars))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Variables["key"])
	assert.Equal(t, float64(42), got.Variables["extra"])
}

func TestInstanceRepo_UpdateVariables_PreservesOriginalVariables(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	inst.OriginalVariables = map[string]any{"original": "yes"}
	require.NoError(t, repo.Create(context.Background(), inst))

	require.NoError(t, repo.UpdateVariables(context.Background(), inst.ID, map[string]any{"key": "changed"}))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "changed", got.Variables["key"])
	assert.Equal(t, "yes", got.OriginalVariables["original"], "UpdateVariables must not overwrite original_variables")
}

func TestInstanceRepo_UpdateCurrentNode(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst))

	require.NoError(t, repo.UpdateCurrentNode(context.Background(), inst.ID, "node-b"))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentNodeID)
	assert.Equal(t, "node-b", *got.CurrentNodeID)
}

func TestInstanceRepo_SetFailed(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst))

	require.NoError(t, repo.SetFailed(context.Background(), inst.ID, "node-b"))

	got, err := repo.Get(context.Background(), inst.ID)
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

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst))

	require.NoError(t, repo.SetCompleted(context.Background(), inst.ID))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, got.State)
	assert.NotNil(t, got.CompletedAt)
}

func TestInstanceRepo_List(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	repo := NewInstanceRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())

	inst1 := newTestInstance(t, ownerID)
	inst2 := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst1))
	require.NoError(t, repo.Create(context.Background(), inst2))
	require.NoError(t, repo.UpdateState(context.Background(), inst2.ID, instance.StateCompleted))

	// List all
	all, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)

	// Filter by state
	running := instance.StateRunning
	filtered, err := repo.List(context.Background(), instance.ListFilters{State: &running, Limit: 100})
	require.NoError(t, err)
	for _, s := range filtered {
		assert.Equal(t, instance.StateRunning, s.State)
	}

	// Filter by workflow name
	wfName := "test-workflow"
	byName, err := repo.List(context.Background(), instance.ListFilters{WorkflowName: &wfName, Limit: 100})
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

	ownerID, _ := userRepo.GetDefaultUser(context.Background())

	inst := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst))

	running, err := repo.ListRunning(context.Background())
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

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	now := time.Now().UTC().Truncate(time.Millisecond)
	exec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Attempt:    1,
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  now,
	}
	require.NoError(t, repo.Create(context.Background(), exec))

	executions, err := repo.ListByInstance(context.Background(), inst.ID)
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

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	now := time.Now().UTC().Truncate(time.Millisecond)
	exec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Attempt:    1,
		Status:     instance.NodeStatusFailed,
		StartedAt:  now,
	}
	require.NoError(t, repo.Create(context.Background(), exec))

	// Update to skipped
	exec.Status = instance.NodeStatusSkipped
	require.NoError(t, repo.Update(context.Background(), exec))

	executions, err := repo.ListByInstance(context.Background(), inst.ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, instance.NodeStatusSkipped, executions[0].Status)
}

// ---------------------------------------------------------------------------
// VariableSnapshotRepo tests
// ---------------------------------------------------------------------------

func TestNodeExecutionRepo_FullyPopulated(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	instRepo := NewInstanceRepo(pool)
	repo := NewNodeExecutionRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	model := "claude-sonnet-4-6"
	tokensIn := 512
	tokensOut := 256
	stdout := "hello\n"
	stderr := "warn\n"
	exitCode := 0
	now := time.Now().UTC().Truncate(time.Millisecond)
	completed := now.Add(time.Second)

	exec := &instance.NodeExecution{
		ID:              uuid.New(),
		InstanceID:      inst.ID,
		NodeID:          "node-llm",
		Attempt:         1,
		Status:          instance.NodeStatusSucceeded,
		ResolvedInputs:  map[string]any{"prompt": "hello"},
		Output:          map[string]any{"result": "world"},
		LLMModel:        &model,
		LLMTokensIn:     &tokensIn,
		LLMTokensOut:    &tokensOut,
		ScriptStdout:    &stdout,
		ScriptStderr:    &stderr,
		ScriptExitCode:  &exitCode,
		StartedAt:       now,
		CompletedAt:     &completed,
	}
	require.NoError(t, repo.Create(context.Background(), exec))

	execs, err := repo.ListByInstance(context.Background(), inst.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	got := execs[0]

	assert.Equal(t, "hello", got.ResolvedInputs["prompt"])
	assert.Equal(t, "world", got.Output["result"])
	require.NotNil(t, got.LLMModel)
	assert.Equal(t, model, *got.LLMModel)
	require.NotNil(t, got.LLMTokensIn)
	assert.Equal(t, tokensIn, *got.LLMTokensIn)
	require.NotNil(t, got.LLMTokensOut)
	assert.Equal(t, tokensOut, *got.LLMTokensOut)
	require.NotNil(t, got.ScriptStdout)
	assert.Equal(t, stdout, *got.ScriptStdout)
	require.NotNil(t, got.ScriptStderr)
	assert.Equal(t, stderr, *got.ScriptStderr)
	require.NotNil(t, got.ScriptExitCode)
	assert.Equal(t, exitCode, *got.ScriptExitCode)
	require.NotNil(t, got.CompletedAt)
	assert.True(t, got.CompletedAt.Equal(completed), "CompletedAt mismatch: %v vs %v", got.CompletedAt, completed)
}

func TestNodeExecutionRepo_GetByInstanceAndNode_MultipleAttempts(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	instRepo := NewInstanceRepo(pool)
	repo := NewNodeExecutionRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	now := time.Now().UTC().Truncate(time.Millisecond)

	// Create attempt 1 (failed) and attempt 2 (succeeded).
	exec1 := &instance.NodeExecution{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: "node-a",
		Attempt: 1, Status: instance.NodeStatusFailed, StartedAt: now,
	}
	exec2 := &instance.NodeExecution{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: "node-a",
		Attempt: 2, Status: instance.NodeStatusSucceeded, StartedAt: now.Add(time.Second),
	}
	require.NoError(t, repo.Create(context.Background(), exec1))
	require.NoError(t, repo.Create(context.Background(), exec2))

	// GetByInstanceAndNode must return the latest attempt (attempt 2).
	got, err := repo.GetByInstanceAndNode(context.Background(), inst.ID, "node-a")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Attempt)
	assert.Equal(t, instance.NodeStatusSucceeded, got.Status)
}

func TestNodeExecutionRepo_GetByInstanceAndNode_NotFound(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	instRepo := NewInstanceRepo(pool)
	repo := NewNodeExecutionRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	_, err := repo.GetByInstanceAndNode(context.Background(), inst.ID, "nonexistent-node")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestVariableSnapshotRepo_CreateAndListByInstance(t *testing.T) {
	db := setupTestDB(t)
	pool := db.Pool
	userRepo := NewUserRepo(pool)
	instRepo := NewInstanceRepo(pool)
	repo := NewVariableSnapshotRepo(pool)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	snap := &instance.VariableSnapshot{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Variables:  map[string]any{"result": "ok"},
		CapturedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, repo.Create(context.Background(), snap))

	snaps, err := repo.ListByInstance(context.Background(), inst.ID)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, snap.ID, snaps[0].ID)
	assert.Equal(t, "node-a", snaps[0].NodeID)
	assert.Equal(t, "ok", snaps[0].Variables["result"])
}
