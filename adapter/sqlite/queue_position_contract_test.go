package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueuePositionContract_Position1IsNextEligibleHead is the contract test
// binding the two queue-ordering authorities: the read-only position simulator
// (queue.Manager.QueuePositions) and the dispatch picker (QueueRepo.NextEligible).
// For any queue state, the instance the simulator puts at position 1 must be the
// instance NextEligible would dispatch next — otherwise the UI would show a
// "you're next" badge on a row the scheduler would not actually start. Both run
// against the REAL SQLite QueueRepo so the SQL ordering and the in-memory
// simulation cannot drift apart. Covered cases: priority ordering, FIFO within a
// priority, and per-owner-cap deferral.
func TestQueuePositionContract_Position1IsNextEligibleHead(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	// queuedAt builds a distinct, monotonically-increasing QueuedAt so FIFO is
	// deterministic.
	queuedAt := func(offset time.Duration) *time.Time {
		v := now.Add(offset)
		return &v
	}

	type seed struct {
		owner    uuid.UUID
		priority instance.Priority
		queuedAt *time.Time
		running  bool // pre-seed as running (consumes per-owner capacity)
	}

	cases := []struct {
		name       string
		globalCap  int
		perUserCap int
		seeds      func(ownerA, ownerB uuid.UUID) []seed
	}{
		{
			name:       "priority ordering: high before normal before low",
			globalCap:  0,
			perUserCap: 0,
			seeds: func(ownerA, ownerB uuid.UUID) []seed {
				return []seed{
					{owner: ownerA, priority: instance.PriorityLow, queuedAt: queuedAt(0)},
					{owner: ownerB, priority: instance.PriorityNormal, queuedAt: queuedAt(time.Second)},
					{owner: ownerA, priority: instance.PriorityHigh, queuedAt: queuedAt(2 * time.Second)},
				}
			},
		},
		{
			name:       "FIFO within a priority: earliest QueuedAt wins",
			globalCap:  0,
			perUserCap: 0,
			seeds: func(ownerA, ownerB uuid.UUID) []seed {
				return []seed{
					{owner: ownerA, priority: instance.PriorityHigh, queuedAt: queuedAt(2 * time.Second)},
					{owner: ownerB, priority: instance.PriorityHigh, queuedAt: queuedAt(time.Second)},
				}
			},
		},
		{
			name:       "per-owner cap defers the at-cap owner's head",
			globalCap:  0,
			perUserCap: 1,
			seeds: func(ownerA, ownerB uuid.UUID) []seed {
				return []seed{
					// ownerA already has one running -> at cap. Its high-priority
					// queued item must be deferred behind ownerB's, which still has
					// headroom — so position 1 (and NextEligible) is ownerB's.
					{owner: ownerA, running: true},
					{owner: ownerA, priority: instance.PriorityHigh, queuedAt: queuedAt(0)},
					{owner: ownerB, priority: instance.PriorityNormal, queuedAt: queuedAt(time.Second)},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			instRepo := NewInstanceRepo(db.DB)
			queueRepo := NewQueueRepo(db.DB)

			ownerA, err := NewUserRepo(db.DB).GetDefaultUser(context.Background())
			require.NoError(t, err)
			ownerB := uuid.New()
			_, err = db.DB.ExecContext(context.Background(),
				"INSERT INTO users (id, username, role) VALUES (?, 'ownerB', 'user')", ownerB.String())
			require.NoError(t, err)

			for _, s := range tc.seeds(ownerA, ownerB) {
				inst := newTestInstance(t, s.owner)
				if s.running {
					inst.State = instance.StateRunning
				} else {
					inst.State = instance.StateQueued
					inst.Priority = s.priority
					inst.QueuedAt = s.queuedAt
				}
				require.NoError(t, instRepo.Create(context.Background(), inst))
			}

			cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: tc.globalCap, PerUserCap: tc.perUserCap}}
			mgr := queue.NewManager(queueRepo, instRepo, cfg, nil)

			positions, err := mgr.QueuePositions(context.Background())
			require.NoError(t, err)
			require.NotEmpty(t, positions, "queue is non-empty")

			var pos1 uuid.UUID
			for id, p := range positions {
				if p == 1 {
					pos1 = id
				}
			}
			require.NotEqual(t, uuid.Nil, pos1, "some instance occupies position 1")

			head, err := queueRepo.NextEligible(context.Background(), tc.globalCap, tc.perUserCap)
			require.NoError(t, err)
			require.NotNil(t, head, "NextEligible has a head")

			assert.Equal(t, head.ID, pos1,
				"QueuePositions position-1 must equal the NextEligible dispatch head")
		})
	}
}
