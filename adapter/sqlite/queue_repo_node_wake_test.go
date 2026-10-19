package sqlite

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parentAt creates a root instance whose current node is nodeID, then moves it
// to state (via the same repository calls the engine uses).
func parentAt(t *testing.T, repo *InstanceRepo, nodeID string, state instance.State) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, repo.UpdateCurrentNode(ctx, p.ID, nodeID))
	require.NoError(t, repo.UpdateState(ctx, p.ID, state))
	return p.ID
}

// stateAndQueuedAt reads the row back through the repository's own Get.
func stateAndQueuedAt(t *testing.T, repo *InstanceRepo, id uuid.UUID) (instance.State, *time.Time) {
	t.Helper()
	got, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	return got.State, got.QueuedAt
}

// ---------- happy path ----------

func TestSetQueuedIfWaitingAtNode_WaitingAtThatNodeIsQueued(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	id := parentAt(t, instRepo, "group-1", instance.StateWaiting)
	queuedAt := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), id, "group-1", queuedAt)
	require.NoError(t, err)
	assert.True(t, ok)

	state, at := stateAndQueuedAt(t, instRepo, id)
	assert.Equal(t, instance.StateQueued, state)
	require.NotNil(t, at, "queued_at must be stamped so the queue orders the wake FIFO")
	assert.True(t, queuedAt.Equal(*at))

	// The woken parent is now dequeuable through the normal queue path.
	next, err := queueRepo.NextEligible(context.Background(), 0, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, id, next.ID)
}

// ---------- failure: not this node / not waiting ----------

func TestSetQueuedIfWaitingAtNode_WaitingAtAnotherNodeIsUntouched(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	id := parentAt(t, instRepo, "spawn-2", instance.StateWaiting)

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), id, "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok, "a parent that moved on to a later node must not be woken by this group")

	state, at := stateAndQueuedAt(t, instRepo, id)
	assert.Equal(t, instance.StateWaiting, state)
	assert.Nil(t, at, "queued_at must not be touched by a losing wake")
}

func TestSetQueuedIfWaitingAtNode_NonWaitingStatesAreNeverWoken(t *testing.T) {
	for _, state := range []instance.State{
		instance.StateRunning, instance.StateQueued, instance.StatePaused,
		instance.StateCompleted, instance.StateFailed, instance.StateCancelled,
	} {
		t.Run(string(state), func(t *testing.T) {
			db := setupTestDB(t)
			instRepo := NewInstanceRepo(db.DB)
			queueRepo := NewQueueRepo(db.DB)
			id := parentAt(t, instRepo, "group-1", state)

			ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), id, "group-1", time.Now().UTC())
			require.NoError(t, err)
			assert.False(t, ok)

			got, _ := stateAndQueuedAt(t, instRepo, id)
			assert.Equal(t, state, got, "a %s parent must keep its state (a cancelled parent is never resurrected)", state)
		})
	}
}

// A parent with no current node (it never reached one) cannot be addressed.
func TestSetQueuedIfWaitingAtNode_NullCurrentNodeNeverMatches(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()
	p := newLinkedInstance(instance.SpawnLink{})
	p.State = instance.StateWaiting
	require.NoError(t, instRepo.Create(ctx, p))

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(ctx, p.ID, "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok)
}

// ---------- edge: idempotence / unknown ids ----------

func TestSetQueuedIfWaitingAtNode_SecondWakeLoses(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	id := parentAt(t, instRepo, "group-1", instance.StateWaiting)
	ctx := context.Background()

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(ctx, id, "group-1", time.Now().UTC())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = queueRepo.SetQueuedIfWaitingAtNode(ctx, id, "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok, "a duplicate wake (coordinator + sweep) re-queues once")
}

func TestSetQueuedIfWaitingAtNode_UnknownInstanceIsFalseNotError(t *testing.T) {
	db := setupTestDB(t)
	ok, err := NewQueueRepo(db.DB).SetQueuedIfWaitingAtNode(context.Background(), uuid.New(), "group-1", time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, ok)
}

// ---------- error ----------

func TestSetQueuedIfWaitingAtNode_EmptyNodeIsRefused(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	id := parentAt(t, instRepo, "group-1", instance.StateWaiting)

	ok, err := NewQueueRepo(db.DB).SetQueuedIfWaitingAtNode(context.Background(), id, "", time.Now().UTC())
	require.Error(t, err)
	assert.False(t, ok)
	state, _ := stateAndQueuedAt(t, instRepo, id)
	assert.Equal(t, instance.StateWaiting, state)
}

func TestSetQueuedIfWaitingAtNode_StorageErrorsSurface(t *testing.T) {
	db := setupTestDB(t)
	queueRepo := NewQueueRepo(db.DB)
	require.NoError(t, db.DB.Close())

	ok, err := queueRepo.SetQueuedIfWaitingAtNode(context.Background(), uuid.New(), "group-1", time.Now().UTC())
	assert.Error(t, err)
	assert.False(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db2 := setupTestDB(t)
	_, err = NewQueueRepo(db2.DB).SetQueuedIfWaitingAtNode(ctx, uuid.New(), "group-1", time.Now().UTC())
	assert.Error(t, err, "a cancelled context aborts the wake")
}

// ---------- concurrency: mutual exclusion with ClaimWaitingParent ----------

// The coordinator's wake and the executor's lost-wake claim race for the same
// waiting parent. Exactly one may win every time, and the row must end in the
// winner's state — never both queued and running.
func TestSetQueuedIfWaitingAtNode_ExactlyOneWinsAgainstClaimWaitingParent(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		id := parentAt(t, instRepo, "group-1", instance.StateWaiting)

		var wg sync.WaitGroup
		var woke, claimed bool
		var wakeErr, claimErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			woke, wakeErr = queueRepo.SetQueuedIfWaitingAtNode(ctx, id, "group-1", time.Now().UTC())
		}()
		go func() {
			defer wg.Done()
			<-start
			claimed, claimErr = instRepo.ClaimWaitingParent(ctx, id, "group-1")
		}()
		close(start)
		wg.Wait()

		require.NoError(t, wakeErr)
		require.NoError(t, claimErr)
		require.True(t, woke != claimed, "iteration %d: exactly one mover must win (woke=%v claimed=%v)", i, woke, claimed)

		state, _ := stateAndQueuedAt(t, instRepo, id)
		if woke {
			assert.Equal(t, instance.StateQueued, state)
		} else {
			assert.Equal(t, instance.StateRunning, state)
		}
	}
}
