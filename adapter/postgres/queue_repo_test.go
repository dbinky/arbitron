//go:build postgres

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/postgres/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestInstance creates an instance in the DB with the given state and priority.
func createTestInstance(t *testing.T, repo *InstanceRepo, state instance.State, priority instance.Priority, ownerID uuid.UUID, queuedAt *time.Time) *instance.Instance {
	t.Helper()
	inst := &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           ownerID,
		WorkflowName:      "test-wf",
		WorkflowSnapshot:  map[string]any{"name": "test-wf"},
		State:             state,
		Priority:          priority,
		Variables:         map[string]any{},
		OriginalVariables: map[string]any{},
		QueuedAt:          queuedAt,
	}
	err := repo.Create(context.Background(), inst)
	require.NoError(t, err)
	return inst
}

func TestQueueRepo_CountRunning_Empty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewQueueRepo(db.Pool)

	count, err := repo.CountRunning(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestQueueRepo_CountRunning_WithRunning(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)
	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)
	createTestInstance(t, instRepo, instance.StateCompleted, instance.PriorityNormal, ownerID, nil)

	count, err := queueRepo.CountRunning(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestQueueRepo_CountRunningByOwner(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)
	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)

	count, err := queueRepo.CountRunningByOwner(context.Background(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Different owner should be 0.
	otherOwner := uuid.New()
	count, err = queueRepo.CountRunningByOwner(context.Background(), otherOwner)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestQueueRepo_NextEligible_PriorityOrdering(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	now := time.Now()
	low := createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityLow, ownerID, &now)
	time.Sleep(time.Millisecond)
	now2 := time.Now()
	normal := createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now2)
	time.Sleep(time.Millisecond)
	now3 := time.Now()
	high := createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityHigh, ownerID, &now3)

	// High priority should come first regardless of queued_at order.
	next, err := queueRepo.NextEligible(context.Background(), 0, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, high.ID, next.ID)

	_ = low
	_ = normal
}

func TestQueueRepo_NextEligible_FIFOWithinPriority(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	now1 := time.Now()
	first := createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now1)
	time.Sleep(10 * time.Millisecond)
	now2 := time.Now()
	_ = createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now2)

	// First queued (oldest queued_at) should come first.
	next, err := queueRepo.NextEligible(context.Background(), 0, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, first.ID, next.ID)
}

func TestQueueRepo_NextEligible_GlobalCapFull(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	// Create 2 running instances, then 1 queued.
	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)
	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)
	now := time.Now()
	createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now)

	// Global cap = 2, running = 2 → no eligible.
	next, err := queueRepo.NextEligible(context.Background(), 2, 0)
	require.NoError(t, err)
	assert.Nil(t, next)
}

func TestQueueRepo_NextEligible_UnlimitedCaps(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	// Create many running instances.
	for i := 0; i < 5; i++ {
		createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)
	}
	now := time.Now()
	queued := createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now)

	// Cap 0 = unlimited → queued instance should be eligible.
	next, err := queueRepo.NextEligible(context.Background(), 0, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, queued.ID, next.ID)
}

func TestQueueRepo_NextEligible_SkipsUserAtCap(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	// Owner has 1 running.
	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)

	// Owner has a queued instance.
	now := time.Now()
	createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now)

	// Per-user cap = 1, owner already has 1 running → no eligible.
	next, err := queueRepo.NextEligible(context.Background(), 0, 1)
	require.NoError(t, err)
	assert.Nil(t, next)
}

func TestQueueRepo_SetQueued(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	// Create as running.
	inst := createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)

	// Set to queued.
	queuedAt := time.Now()
	err := queueRepo.SetQueued(context.Background(), inst.ID, queuedAt)
	require.NoError(t, err)

	// Verify state and queued_at.
	got, err := instRepo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateQueued, got.State)
	require.NotNil(t, got.QueuedAt)
	assert.WithinDuration(t, queuedAt, *got.QueuedAt, time.Second)
}

func TestQueueRepo_CountQueued(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	now := time.Now()
	createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now)
	createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now)
	createTestInstance(t, instRepo, instance.StateRunning, instance.PriorityNormal, ownerID, nil)

	count, err := queueRepo.CountQueued(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestQueueRepo_CountWaiting(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.Pool)
	instRepo := NewInstanceRepo(db.Pool)
	ownerID := getDefaultUserID(t, db.Pool)

	// We need to manually set state to waiting since the migration added it.
	now := time.Now()
	inst := createTestInstance(t, instRepo, instance.StateQueued, instance.PriorityNormal, ownerID, &now)

	// Update to waiting state directly.
	queries := generated.New(db.Pool)
	err := queries.UpdateInstanceState(context.Background(), generated.UpdateInstanceStateParams{
		ID:    toPgUUID(inst.ID),
		State: generated.InstanceStateWaiting,
	})
	require.NoError(t, err)

	count, err := queueRepo.CountWaiting(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// getDefaultUserID retrieves the seeded admin user ID.
func getDefaultUserID(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), "SELECT id FROM users LIMIT 1").Scan(&id)
	require.NoError(t, err)
	return id
}
