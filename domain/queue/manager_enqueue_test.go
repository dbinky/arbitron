package queue_test

import (
	"context"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Model the engine's synchronous state transition, using the exact id handed
// over by the manager and the same persisted rows used by queue evaluation.
type persistedQueueStarter struct {
	instances instance.InstanceRepository
	starts    []uuid.UUID
}

func (s *persistedQueueStarter) StartInstance(ctx context.Context, id uuid.UUID) error {
	if err := s.instances.UpdateState(ctx, id, instance.StateRunning); err != nil {
		return err
	}
	s.starts = append(s.starts, id)
	return nil
}

func TestEnqueue_EvaluatorMayStartBetweenPersistenceAndEnqueue(t *testing.T) {
	ctx := context.Background()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "queue.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	instances := adaptsqlite.NewInstanceRepo(db)
	starter := &persistedQueueStarter{instances: instances}
	m := queue.NewManager(adaptsqlite.NewQueueRepo(db), instances,
		&config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}, starter)

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001") // migration-seeded local owner
	id := uuid.New()
	require.NoError(t, instances.Create(ctx, &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               id, OwnerID: owner, WorkflowName: "queued-child", State: instance.StateQueued,
		Priority: instance.PriorityNormal, WorkflowSnapshot: map[string]any{},
		Variables: map[string]any{"inherited": "real-payload"},
	}))
	// A capacity notification can evaluate this row as soon as Create commits,
	// before createAndEnqueue reaches its explicit Enqueue. No timing needed.
	require.NoError(t, m.Evaluate(ctx))
	require.Equal(t, []uuid.UUID{id}, starter.starts)
	started, err := m.Enqueue(ctx, id, owner)
	require.NoError(t, err)
	assert.True(t, started, "the evaluator already started this instance")
	assert.Equal(t, []uuid.UUID{id}, starter.starts, "never launch a second walker or overwrite the live cancellation context")
	stored, err := instances.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateRunning, stored.State)
	assert.Equal(t, "real-payload", stored.Variables["inherited"])

	for _, state := range []instance.State{instance.StateCompleted, instance.StateFailed, instance.StateCancelled, instance.StateWaiting, instance.StatePaused} {
		t.Run(string(state), func(t *testing.T) {
			require.NoError(t, instances.UpdateState(ctx, id, state))
			started, err := m.Enqueue(ctx, id, owner)
			require.NoError(t, err)
			assert.False(t, started)
			assert.Equal(t, []uuid.UUID{id}, starter.starts, "an already advanced instance must not be resurrected")
			stored, err := instances.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, state, stored.State)
		})
	}
}

func TestEnqueue_MissingPersistedInstanceCannotStart(t *testing.T) {
	starter := newMockStarter()
	m := queue.NewManager(newMockQueueRepo(), newMockInstanceRepo(), &config.Config{}, starter)
	started, err := m.Enqueue(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.False(t, started)
	assert.Empty(t, starter.getCalls())
}
