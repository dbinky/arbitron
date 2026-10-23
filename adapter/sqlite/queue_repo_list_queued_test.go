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

func TestListQueued_OrdersByPriorityThenFIFO(t *testing.T) {
	td := setupTestDB(t)
	instRepo := NewInstanceRepo(td.DB)
	queueRepo := NewQueueRepo(td.DB)

	owner := uuid.New()
	_, err := td.DB.ExecContext(context.Background(),
		"INSERT INTO users (id, username, role) VALUES (?, 'testuser', 'user')", owner.String())
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	mkQueued := func(id uuid.UUID, priority instance.Priority, queuedAt time.Time) *instance.Instance {
		return &instance.Instance{
			ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
			ID: id, OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: map[string]any{"name": "wf"},
			State: instance.StateQueued, Priority: priority, QueuedAt: &queuedAt, CreatedAt: now, UpdatedAt: now,
		}
	}
	low1 := uuid.New()
	mkLow := mkQueued(low1, instance.PriorityLow, now.Add(2*time.Second))
	normal1 := uuid.New()
	mkNormal := mkQueued(normal1, instance.PriorityNormal, now.Add(time.Second))
	high1 := uuid.New()
	mkHigh := mkQueued(high1, instance.PriorityHigh, now)
	high2 := uuid.New()
	mkHigh2 := mkQueued(high2, instance.PriorityHigh, now.Add(3*time.Second))

	for _, inst := range []*instance.Instance{mkLow, mkNormal, mkHigh, mkHigh2} {
		require.NoError(t, instRepo.Create(context.Background(), inst))
	}

	got, err := queueRepo.ListQueued(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, high1, got[0].ID, "high priority first")
	assert.Equal(t, high2, got[1].ID, "FIFO within high (earlier QueuedAt first)")
	assert.Equal(t, normal1, got[2].ID, "normal next")
	assert.Equal(t, low1, got[3].ID, "low last")
}

func TestListQueued_EmptyQueue(t *testing.T) {
	td := setupTestDB(t)
	queueRepo := NewQueueRepo(td.DB)
	got, err := queueRepo.ListQueued(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "empty queue -> empty slice, not error")
}

func TestListQueued_ExcludesNonQueuedStates(t *testing.T) {
	td := setupTestDB(t)
	instRepo := NewInstanceRepo(td.DB)
	queueRepo := NewQueueRepo(td.DB)

	owner := uuid.New()
	_, err := td.DB.ExecContext(context.Background(),
		"INSERT INTO users (id, username, role) VALUES (?, 'testuser', 'user')", owner.String())
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	queued := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: map[string]any{"name": "wf"}, State: instance.StateQueued, Priority: instance.PriorityNormal, QueuedAt: &now, CreatedAt: now, UpdatedAt: now}
	running := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: uuid.New(), OwnerID: owner, WorkflowName: "wf2", WorkflowSnapshot: map[string]any{"name": "wf2"}, State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, instRepo.Create(context.Background(), queued))
	require.NoError(t, instRepo.Create(context.Background(), running))

	got, err := queueRepo.ListQueued(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1, "only queued rows returned")
	assert.Equal(t, queued.ID, got[0].ID)
}
