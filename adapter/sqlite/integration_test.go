package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testDB struct {
	DB *sql.DB
}

func setupTestDB(t *testing.T) testDB {
	t.Helper()
	db, err := Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	err = RunMigrations(db)
	require.NoError(t, err)
	return testDB{DB: db}
}

// ---------------------------------------------------------------------------
// Migration tests
// ---------------------------------------------------------------------------

func TestRunMigrations_Up(t *testing.T) {
	db := setupTestDB(t)
	var count int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestRunMigrations_Idempotent(t *testing.T) {
	db := setupTestDB(t)
	err := RunMigrations(db.DB)
	require.NoError(t, err)
}

func TestRunMigrations_DefaultAdminSeeded(t *testing.T) {
	db := setupTestDB(t)
	var username, role string
	require.NoError(t, db.DB.QueryRow(
		"SELECT username, role FROM users WHERE username = 'local'").Scan(&username, &role))
	assert.Equal(t, "local", username)
	assert.Equal(t, "admin", role)
}

// ---------------------------------------------------------------------------
// UserRepo tests
// ---------------------------------------------------------------------------

func TestUserRepo_GetDefaultUser(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)

	id, err := repo.GetDefaultUser(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
}

// ---------------------------------------------------------------------------
// InstanceRepo tests
// ---------------------------------------------------------------------------

func newTestInstance(t *testing.T, ownerID uuid.UUID) *instance.Instance {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	return &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           ownerID,
		WorkflowName:      "test-workflow",
		WorkflowSnapshot:  map[string]any{"name": "test-workflow"},
		State:             instance.StateRunning,
		Priority:          instance.PriorityNormal,
		Variables:         map[string]any{"key": "value"},
		OriginalVariables: map[string]any{"key": "value"},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func TestInstanceRepo_CreateAndGet(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	repo := NewInstanceRepo(db.DB)

	_, err := repo.Get(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestInstanceRepo_UpdateState(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	inst.OriginalVariables = map[string]any{"original": "yes"}
	require.NoError(t, repo.Create(context.Background(), inst))

	require.NoError(t, repo.UpdateVariables(context.Background(), inst.ID, map[string]any{"key": "changed"}))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "changed", got.Variables["key"])
	assert.Equal(t, "yes", got.OriginalVariables["original"])
}

func TestInstanceRepo_UpdateCurrentNode(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())

	inst1 := newTestInstance(t, ownerID)
	inst2 := newTestInstance(t, ownerID)
	require.NoError(t, repo.Create(context.Background(), inst1))
	require.NoError(t, repo.Create(context.Background(), inst2))
	require.NoError(t, repo.UpdateState(context.Background(), inst2.ID, instance.StateCompleted))

	all, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)

	running := instance.StateRunning
	filtered, err := repo.List(context.Background(), instance.ListFilters{State: &running, Limit: 100})
	require.NoError(t, err)
	for _, s := range filtered {
		assert.Equal(t, instance.StateRunning, s.State)
	}

	wfName := "test-workflow"
	byName, err := repo.List(context.Background(), instance.ListFilters{WorkflowName: &wfName, Limit: 100})
	require.NoError(t, err)
	for _, s := range byName {
		assert.Equal(t, "test-workflow", s.WorkflowName)
	}
}

func TestInstanceRepo_List_OwnerFilter(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	ownerA, err := userRepo.GetDefaultUser(ctx)
	require.NoError(t, err)

	ownerB := uuid.New()
	subjectB := "owner-b-subject"
	require.NoError(t, userRepo.Create(ctx, &instance.User{
		ID:       ownerB,
		Subject:  &subjectB,
		Username: "owner-b",
		Role:     "user",
	}))

	instA := newTestInstance(t, ownerA)
	instB := newTestInstance(t, ownerB)
	require.NoError(t, repo.Create(ctx, instA))
	require.NoError(t, repo.Create(ctx, instB))

	// OwnerID = ownerA -> only ownerA's row.
	scoped, err := repo.List(ctx, instance.ListFilters{OwnerID: &ownerA, Limit: 100})
	require.NoError(t, err)
	ids := summaryIDs(scoped)
	assert.Contains(t, ids, instA.ID)
	assert.NotContains(t, ids, instB.ID, "owner filter must hide another owner's instances")

	// OwnerID = nil -> both rows (admin/unconstrained).
	all, err := repo.List(ctx, instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	allIDs := summaryIDs(all)
	assert.Contains(t, allIDs, instA.ID)
	assert.Contains(t, allIDs, instB.ID)
}

func summaryIDs(s []instance.InstanceSummary) []uuid.UUID {
	ids := make([]uuid.UUID, len(s))
	for i, x := range s {
		ids[i] = x.ID
	}
	return ids
}

func TestInstanceRepo_ListRunning(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	repo := NewInstanceRepo(db.DB)

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
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNodeExecutionRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	now := time.Now().UTC().Truncate(time.Second)
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
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNodeExecutionRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	now := time.Now().UTC().Truncate(time.Second)
	exec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Attempt:    1,
		Status:     instance.NodeStatusFailed,
		StartedAt:  now,
	}
	require.NoError(t, repo.Create(context.Background(), exec))

	exec.Status = instance.NodeStatusSkipped
	require.NoError(t, repo.Update(context.Background(), exec))

	executions, err := repo.ListByInstance(context.Background(), inst.ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, instance.NodeStatusSkipped, executions[0].Status)
}

func TestNodeExecutionRepo_FullyPopulated(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNodeExecutionRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	model := "claude-sonnet-4-6"
	tokensIn := 512
	tokensOut := 256
	stdout := "hello\n"
	stderr := "warn\n"
	exitCode := 0
	now := time.Now().UTC().Truncate(time.Second)
	completed := now.Add(time.Second)

	exec := &instance.NodeExecution{
		ID:             uuid.New(),
		InstanceID:     inst.ID,
		NodeID:         "node-llm",
		Attempt:        1,
		Status:         instance.NodeStatusSucceeded,
		ResolvedInputs: map[string]any{"prompt": "hello"},
		Output:         map[string]any{"result": "world"},
		LLMModel:       &model,
		LLMTokensIn:    &tokensIn,
		LLMTokensOut:   &tokensOut,
		ScriptStdout:   &stdout,
		ScriptStderr:   &stderr,
		ScriptExitCode: &exitCode,
		StartedAt:      now,
		CompletedAt:    &completed,
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
}

func TestNodeExecutionRepo_GetByInstanceAndNode_MultipleAttempts(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNodeExecutionRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	now := time.Now().UTC().Truncate(time.Second)

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

	got, err := repo.GetByInstanceAndNode(context.Background(), inst.ID, "node-a")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Attempt)
	assert.Equal(t, instance.NodeStatusSucceeded, got.Status)
}

func TestNodeExecutionRepo_GetByInstanceAndNode_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNodeExecutionRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	_, err := repo.GetByInstanceAndNode(context.Background(), inst.ID, "nonexistent-node")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

// ---------------------------------------------------------------------------
// VariableSnapshotRepo tests
// ---------------------------------------------------------------------------

func TestVariableSnapshotRepo_CreateAndListByInstance(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewVariableSnapshotRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	snap := &instance.VariableSnapshot{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "node-a",
		Variables:  map[string]any{"result": "ok"},
		CapturedAt: time.Now().UTC().Truncate(time.Second),
	}
	require.NoError(t, repo.Create(context.Background(), snap))

	snaps, err := repo.ListByInstance(context.Background(), inst.ID)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, snap.ID, snaps[0].ID)
	assert.Equal(t, "node-a", snaps[0].NodeID)
	assert.Equal(t, "ok", snaps[0].Variables["result"])
}

// ---------------------------------------------------------------------------
// NotificationRepo tests
// ---------------------------------------------------------------------------

func TestNotificationRepo_CreateAndList(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNotificationRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	n := &notification.Notification{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		Type:       notification.TypeFailed,
	}
	require.NoError(t, repo.Create(context.Background(), n))

	list, err := repo.List(context.Background(), notification.ListFilters{Limit: 10})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, n.ID, list[0].ID)
	assert.Equal(t, notification.TypeFailed, list[0].Type)
	assert.False(t, list[0].Read)
	assert.False(t, list[0].Dismissed)
}

func TestNotificationRepo_CountUnread(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNotificationRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	n := &notification.Notification{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		Type:       notification.TypeCompleted,
	}
	require.NoError(t, repo.Create(context.Background(), n))

	count, err := repo.CountUnread(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	require.NoError(t, repo.MarkAllRead(context.Background()))
	count, err = repo.CountUnread(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestNotificationRepo_Dismiss(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	repo := NewNotificationRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	n := &notification.Notification{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		Type:       notification.TypeNeedsInteraction,
	}
	require.NoError(t, repo.Create(context.Background(), n))

	require.NoError(t, repo.Dismiss(context.Background(), n.ID))

	dismissed := true
	list, err := repo.List(context.Background(), notification.ListFilters{Dismissed: &dismissed, Limit: 10})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].Dismissed)
}

func TestNotificationRepo_Dismiss_NotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewNotificationRepo(db.DB)

	err := repo.Dismiss(context.Background(), uuid.New())
	assert.ErrorIs(t, err, notification.ErrNotFound)
}

// ---------------------------------------------------------------------------
// QueueRepo tests
// ---------------------------------------------------------------------------

func TestQueueRepo_CountRunning(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	count, err := queueRepo.CountRunning(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestQueueRepo_NextEligible(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())

	inst := newTestInstance(t, ownerID)
	inst.State = instance.StateQueued
	now := time.Now().UTC().Truncate(time.Second)
	inst.QueuedAt = &now
	require.NoError(t, instRepo.Create(context.Background(), inst))

	eligible, err := queueRepo.NextEligible(context.Background(), 10, 5)
	require.NoError(t, err)
	require.NotNil(t, eligible)
	assert.Equal(t, inst.ID, eligible.ID)
}

func TestQueueRepo_NextEligible_RespectsGlobalCap(t *testing.T) {
	db := setupTestDB(t)
	userRepo := NewUserRepo(db.DB)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)

	ownerID, _ := userRepo.GetDefaultUser(context.Background())

	running := newTestInstance(t, ownerID)
	running.State = instance.StateRunning
	require.NoError(t, instRepo.Create(context.Background(), running))

	queued := newTestInstance(t, ownerID)
	queued.State = instance.StateQueued
	now := time.Now().UTC().Truncate(time.Second)
	queued.QueuedAt = &now
	require.NoError(t, instRepo.Create(context.Background(), queued))

	eligible, err := queueRepo.NextEligible(context.Background(), 1, 0)
	require.NoError(t, err)
	assert.Nil(t, eligible)
}
