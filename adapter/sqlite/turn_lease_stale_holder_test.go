package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staleFixture: a holder from boot "boot-old" took the lease and persisted
// intent turn/1, then crashed before dispatching anything.
func staleFixture(t *testing.T, oldBoot string) (testDB, engine.TurnLease, engine.TurnIntent) {
	t.Helper()
	db := setupTestDB(t)
	ctx := context.Background()
	crashed := NewTurnLeaseRepo(db.DB, "host:111")
	crashed.SetBootID(oldBoot)
	run := uuid.New()
	old, err := crashed.Acquire(ctx, engine.RunLeaseKey(run), 30*time.Second)
	require.NoError(t, err)
	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), LeaseKey: old.Key, RunID: run, InvocationID: uuid.New(),
		NodeID: "talk", TurnIndex: 1, Kind: engine.TurnIntentMessage, Message: "hello", SessionGeneration: old.Generation, OperationID: "turn/1"}
	require.NoError(t, NewTurnIntentRepo(db.DB).Persist(ctx, in))
	return db, old, in
}

func storedBoot(t *testing.T, db testDB, key string) string {
	t.Helper()
	var b string
	require.NoError(t, db.DB.QueryRow(`SELECT boot_id FROM turn_leases WHERE key = ?`, key).Scan(&b))
	return b
}

// Happy: an earlier boot's holder with nothing dispatched is recovered at generation+1.
func TestTurnLease_StaleHolderEarlierBootRecovers(t *testing.T) {
	db, old, in := staleFixture(t, "boot-old")
	assert.Equal(t, "boot-old", storedBoot(t, db, old.Key), "Acquire records the holder's boot")
	repo := NewTurnLeaseRepo(db.DB, "host:222")
	repo.SetBootID("boot-new")

	got, err := repo.RecoverTurnLease(context.Background(), old, engine.TurnRecoveryProof{IntentID: in.ID, StaleHolder: true}, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, old.Token, got.Token)
	assert.Equal(t, old.Generation+1, got.Generation, "the crashed holder is fenced by the next generation")
	assert.Equal(t, "boot-new", storedBoot(t, db, old.Key))
	_, err = repo.Renew(context.Background(), old, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnLeaseLost)
}

// Failure: a holder from the SAME boot is this process's own live turn.
func TestTurnLease_StaleHolderSameBootRefused(t *testing.T) {
	db, old, in := staleFixture(t, "boot-1")
	repo := NewTurnLeaseRepo(db.DB, "host:111")
	repo.SetBootID("boot-1")
	_, err := repo.RecoverTurnLease(context.Background(), old, engine.TurnRecoveryProof{IntentID: in.ID, StaleHolder: true}, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid)
	cur, ok, err := repo.Current(context.Background(), old.Key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, old.Token, cur.Token)
	assert.Equal(t, old.Generation, cur.Generation)
}

// Error: a missing boot ID on either side never proves anything.
func TestTurnLease_StaleHolderMissingBootRefused(t *testing.T) {
	db, old, in := staleFixture(t, "") // legacy/unwired holder recorded no boot
	repo := NewTurnLeaseRepo(db.DB, "host:222")
	repo.SetBootID("boot-new")
	_, err := repo.RecoverTurnLease(context.Background(), old, engine.TurnRecoveryProof{IntentID: in.ID, StaleHolder: true}, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "holder without boot ID")

	db2, old2, in2 := staleFixture(t, "boot-old")
	unwired := NewTurnLeaseRepo(db2.DB, "host:222") // SetBootID never called
	_, err = unwired.RecoverTurnLease(context.Background(), old2, engine.TurnRecoveryProof{IntentID: in2.ID, StaleHolder: true}, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "recovering repo without boot ID")
}

// Edge: once any job was dispatched for the operation, only the job proof (Cycle 2b) applies.
func TestTurnLease_StaleHolderWithDispatchedJobRefused(t *testing.T) {
	db, old, in := staleFixture(t, "boot-old")
	now := timeToString(time.Now())
	_, err := db.DB.Exec(`INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind,
		runner_id, generation, state, created_at, updated_at) VALUES (?, ?, ?, 'turn/1/stream', 1, 'talk', 'stream', 'family', 'mac-a', 1, 'failed', ?, ?)`,
		uuid.NewString(), in.RunID.String(), in.InvocationID.String(), now, now)
	require.NoError(t, err)
	repo := NewTurnLeaseRepo(db.DB, "host:222")
	repo.SetBootID("boot-new")
	_, err = repo.RecoverTurnLease(context.Background(), old, engine.TurnRecoveryProof{IntentID: in.ID, StaleHolder: true}, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid)

	// A turn/10 job does not count as dispatch of turn/1.
	db3, old3, in3 := staleFixture(t, "boot-old")
	_, err = db3.DB.Exec(`INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind,
		runner_id, generation, state, created_at, updated_at) VALUES (?, ?, ?, 'turn/10', 1, 'talk', 'stream', 'family', 'mac-a', 1, 'failed', ?, ?)`,
		uuid.NewString(), in3.RunID.String(), in3.InvocationID.String(), now, now)
	require.NoError(t, err)
	r3 := NewTurnLeaseRepo(db3.DB, "host:222")
	r3.SetBootID("boot-new")
	_, err = r3.RecoverTurnLease(context.Background(), old3, engine.TurnRecoveryProof{IntentID: in3.ID, StaleHolder: true}, time.Minute)
	require.NoError(t, err)
}
