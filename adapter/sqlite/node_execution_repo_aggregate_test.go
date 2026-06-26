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
	userRepo := NewUserRepo(td.DB)
	instRepo := NewInstanceRepo(td.DB)
	repo := NewNodeExecutionRepo(td.DB)

	ownerID, err := userRepo.GetDefaultUser(context.Background())
	require.NoError(t, err)

	instA := newTestInstance(t, ownerID)
	instB := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), instA))
	require.NoError(t, instRepo.Create(context.Background(), instB))

	now := time.Now().UTC().Truncate(time.Second)

	modelA1 := "gpt-4o"
	modelA2 := "gpt-4o-2024-11" // started LATER than modelA1 -> this is "latest"
	modelB := "claude-3"
	execA1 := &instance.NodeExecution{ID: uuid.New(), InstanceID: instA.ID, NodeID: "n1", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: now, LLMModel: &modelA1}
	execA2 := &instance.NodeExecution{ID: uuid.New(), InstanceID: instA.ID, NodeID: "n2", Attempt: 3, Status: instance.NodeStatusFailed, StartedAt: now.Add(time.Minute), LLMModel: &modelA2}
	execB1 := &instance.NodeExecution{ID: uuid.New(), InstanceID: instB.ID, NodeID: "n1", Attempt: 2, Status: instance.NodeStatusSucceeded, StartedAt: now.Add(2 * time.Minute), LLMModel: &modelB}

	require.NoError(t, repo.Create(context.Background(), execA1))
	require.NoError(t, repo.Create(context.Background(), execA2))
	require.NoError(t, repo.Create(context.Background(), execB1))

	got, err := repo.AggregateByInstances(context.Background(), []uuid.UUID{instA.ID, instB.ID, uuid.New()})
	require.NoError(t, err)
	assert.Len(t, got, 2, "instances with no executions are absent")

	a := got[instA.ID]
	assert.Equal(t, "gpt-4o-2024-11", a.LatestModel, "latest model = most-recently-started execution's model")
	assert.Equal(t, 3, a.MaxAttempt, "max attempt across nodes")

	b := got[instB.ID]
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
	userRepo := NewUserRepo(td.DB)
	instRepo := NewInstanceRepo(td.DB)
	repo := NewNodeExecutionRepo(td.DB)

	ownerID, err := userRepo.GetDefaultUser(context.Background())
	require.NoError(t, err)
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	got, err := repo.AggregateByInstances(context.Background(), []uuid.UUID{inst.ID})
	require.NoError(t, err)
	assert.NotContains(t, got, inst.ID, "queued instance absent from map")
}

func TestAggregateByInstances_NoModelColumn(t *testing.T) {
	td := setupTestDB(t)
	userRepo := NewUserRepo(td.DB)
	instRepo := NewInstanceRepo(td.DB)
	repo := NewNodeExecutionRepo(td.DB)

	ownerID, err := userRepo.GetDefaultUser(context.Background())
	require.NoError(t, err)
	inst := newTestInstance(t, ownerID)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: "n1", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: now,
	}))
	got, err := repo.AggregateByInstances(context.Background(), []uuid.UUID{inst.ID})
	require.NoError(t, err)
	assert.Equal(t, "", got[inst.ID].LatestModel, "script-only node: model empty")
}
