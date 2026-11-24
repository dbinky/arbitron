package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLeaseClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeLeaseClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeLeaseClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func TestTurnLease_AcquireReleasedAndRenew(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	clk := &fakeLeaseClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	repo := NewTurnLeaseRepo(db.DB, "proc-a")
	repo.SetClock(clk.Now)

	l, err := repo.Acquire(ctx, "run:1", time.Minute)
	require.NoError(t, err)
	assert.EqualValues(t, 1, l.Generation)
	assert.True(t, l.ExpiresAt.Equal(clk.Now().Add(time.Minute)))

	_, err = repo.Acquire(ctx, "run:1", time.Minute)
	assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress)

	clk.Advance(50 * time.Second)
	r, err := repo.Renew(ctx, l, time.Minute)
	require.NoError(t, err)
	assert.True(t, r.ExpiresAt.Equal(clk.Now().Add(time.Minute)))

	clk.Advance(50 * time.Second) // 100s after acquire, but renewed at 50s: still held
	_, err = repo.Acquire(ctx, "run:1", time.Minute)
	assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress, "renewal prevents takeover")

	b, err := repo.BumpGeneration(ctx, r)
	require.NoError(t, err)
	assert.EqualValues(t, 2, b.Generation)

	cur, ok, err := repo.Current(ctx, "run:1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, l.Token, cur.Token)
	assert.EqualValues(t, 2, cur.Generation)

	require.NoError(t, repo.Release(ctx, b))
	require.NoError(t, repo.Release(ctx, b), "release is idempotent")
	_, ok, err = repo.Current(ctx, "run:1")
	require.NoError(t, err)
	assert.False(t, ok)

	again, err := repo.Acquire(ctx, "run:1", time.Minute)
	require.NoError(t, err)
	assert.EqualValues(t, 2, again.Generation, "clean re-acquire keeps the generation")
}

func TestTurnLease_ExpiredButLiveOwnerRejectsSecondDispatch(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	clk := &fakeLeaseClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	owner := NewTurnLeaseRepo(db.DB, "owner") // e.g. waiting on a remote harness that is still running
	owner.SetClock(clk.Now)
	other := NewTurnLeaseRepo(db.DB, "other")
	other.SetClock(clk.Now)

	held, err := owner.Acquire(ctx, "run:2", 30*time.Second)
	require.NoError(t, err)

	clk.Advance(10 * time.Minute) // long past expiry, never renewed
	_, err = other.Acquire(ctx, "run:2", 30*time.Second)
	assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress, "expiry alone never permits a second dispatch")

	cur, ok, err := other.Current(ctx, "run:2")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, held.Token, cur.Token, "the occupied row is untouched")
	assert.Equal(t, held.Generation, cur.Generation, "no generation was consumed by the refused attempt")

	renewed, err := owner.Renew(ctx, held, 30*time.Second)
	require.NoError(t, err, "a slow but live owner keeps its lease")
	assert.Equal(t, held.Generation, renewed.Generation)
}

func TestTurnLeaseRepo_TwoProcessesRaceExactlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.db")
	dbA, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { dbA.Close() })
	require.NoError(t, RunMigrations(dbA))
	dbB, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { dbB.Close() })
	require.NoError(t, RunMigrations(dbB))

	a, b := NewTurnLeaseRepo(dbA, "proc-a"), NewTurnLeaseRepo(dbB, "proc-b")
	key := engine.RunLeaseKey(uuid.New())
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, repo := range []*TurnLeaseRepo{a, b} {
		wg.Add(1)
		go func(i int, repo *TurnLeaseRepo) {
			defer wg.Done()
			_, errs[i] = repo.Acquire(context.Background(), key, time.Minute)
		}(i, repo)
	}
	wg.Wait()
	wins, busy := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			wins++
		case assert.ErrorIs(t, e, instance.ErrInteractiveTurnInProgress):
			busy++
		}
	}
	assert.Equal(t, 1, wins)
	assert.Equal(t, 1, busy)
}

func TestTurnLeaseRepo_RejectsNonPositiveTTL(t *testing.T) {
	db := setupTestDB(t)
	_, err := NewTurnLeaseRepo(db.DB, "p").Acquire(context.Background(), "run:3", 0)
	assert.Error(t, err)
}
