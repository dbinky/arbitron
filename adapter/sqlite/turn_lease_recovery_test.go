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

// recFixture: a crashed holder's lease, its pending intent "turn/1" and the
// stream job it dispatched (live on mac-a at generation 3).
type recFixture struct {
	db       testDB
	repo     *TurnLeaseRepo
	clk      *fakeLeaseClock
	old      engine.TurnLease
	intent   engine.TurnIntent
	job      engine.JobFenceRef
	resource engine.ResourceFenceRef
}

func newRecFixture(t *testing.T) *recFixture {
	t.Helper()
	db := setupTestDB(t)
	ctx := context.Background()
	clk := &fakeLeaseClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	crashed := NewTurnLeaseRepo(db.DB, "crashed")
	crashed.SetClock(clk.Now)
	run := uuid.New()
	old, err := crashed.Acquire(ctx, engine.RunLeaseKey(run), 30*time.Second)
	require.NoError(t, err)
	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), LeaseKey: old.Key, RunID: run, InvocationID: uuid.New(),
		NodeID: "talk", TurnIndex: 1, Kind: engine.TurnIntentMessage, Message: "hello", SessionGeneration: old.Generation, OperationID: "turn/1"}
	require.NoError(t, NewTurnIntentRepo(db.DB).Persist(ctx, in))
	f := &recFixture{db: db, clk: clk, old: old, intent: in,
		job:      engine.JobFenceRef{JobID: uuid.New(), RunnerID: "mac-a", Generation: 3},
		resource: engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: "mac-a", Generation: 2, Token: uuid.New()}}
	f.insertJob(t, f.job, "turn/1/stream", "running")
	_, err = db.DB.Exec(`INSERT INTO workspace_leases (resource_key, holder, owner, generation, token, active) VALUES (?, 'job', ?, ?, ?, 1)`,
		f.resource.Key, f.resource.Owner, f.resource.Generation, f.resource.Token.String())
	require.NoError(t, err)
	f.repo = NewTurnLeaseRepo(db.DB, "survivor")
	f.repo.SetClock(clk.Now)
	clk.Advance(10 * time.Minute) // the crashed holder's lease is long expired
	return f
}

func (f *recFixture) insertJob(t *testing.T, j engine.JobFenceRef, op, state string) {
	t.Helper()
	now := timeToString(f.clk.Now())
	_, err := f.db.DB.Exec(`INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind,
		runner_id, generation, state, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 'talk', 'stream', 'family', ?, ?, ?, ?, ?)`,
		j.JobID.String(), f.intent.RunID.String(), f.intent.InvocationID.String(), op, j.RunnerID, j.Generation, state, now, now)
	require.NoError(t, err)
}

func (f *recFixture) setJob(t *testing.T, state string, generation int64) {
	t.Helper()
	_, err := f.db.DB.Exec(`UPDATE runner_jobs SET state = ?, generation = ? WHERE id = ?`, state, generation, f.job.JobID.String())
	require.NoError(t, err)
}

func (f *recFixture) releaseResource(t *testing.T) {
	t.Helper()
	_, err := f.db.DB.Exec(`UPDATE workspace_leases SET active = 0 WHERE resource_key = ?`, f.resource.Key)
	require.NoError(t, err)
}

func (f *recFixture) assertUntouched(t *testing.T) {
	t.Helper()
	cur, ok, err := f.repo.Current(context.Background(), f.old.Key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, f.old.Token, cur.Token)
	assert.Equal(t, f.old.Generation, cur.Generation)
}

// Success: adopting the exact live job hands the server a new token at the SAME generation.
func TestTurnLease_AdoptExactLiveJobPreservesGeneration(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	got, err := f.repo.RecoverTurnLease(ctx, f.old, engine.TurnRecoveryProof{IntentID: f.intent.ID, Adopted: true,
		Jobs: []engine.JobFenceRef{f.job}, Resource: &f.resource}, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, f.old.Token, got.Token)
	assert.Equal(t, f.old.Generation, got.Generation, "adoption keeps the session/job/resource generation")
	assert.True(t, got.ExpiresAt.Equal(f.clk.Now().Add(time.Minute)))

	_, err = f.repo.Renew(ctx, f.old, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnLeaseLost, "the crashed holder's token is fenced")
	cur, ok, err := f.repo.Current(ctx, f.old.Key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, got.Token, cur.Token)
}

// Happy (replay): after the old job is fenced and the resource released, replay reserves generation+1.
func TestTurnLease_ReplayAfterFenceReservesNextGeneration(t *testing.T) {
	f := newRecFixture(t)
	f.setJob(t, "failed", f.job.Generation) // confirmed loss: P4 T10 fenced it
	f.releaseResource(t)
	got, err := f.repo.RecoverTurnLease(context.Background(), f.old, engine.TurnRecoveryProof{IntentID: f.intent.ID,
		Jobs: []engine.JobFenceRef{f.job}, Resource: &f.resource}, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, f.old.Generation+1, got.Generation)
}

// Error: a stale token/generation or a storage failure leaves the owner untouched.
func TestTurnLease_RecoveryCASOrDBFailureLeavesOwnerUntouched(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	proof := engine.TurnRecoveryProof{IntentID: f.intent.ID, Adopted: true, Jobs: []engine.JobFenceRef{f.job}}

	stale := f.old
	stale.Generation--
	_, err := f.repo.RecoverTurnLease(ctx, stale, proof, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnLeaseLost)
	foreign := f.old
	foreign.Token = uuid.New()
	_, err = f.repo.RecoverTurnLease(ctx, foreign, proof, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnLeaseLost)
	_, err = f.repo.RecoverTurnLease(ctx, f.old, proof, 0)
	assert.Error(t, err, "non-positive ttl")
	f.assertUntouched(t)

	_, err = f.db.DB.Exec(`DROP TABLE runner_jobs`) // injected storage failure mid-transaction
	require.NoError(t, err)
	_, err = f.repo.RecoverTurnLease(ctx, f.old, proof, time.Minute)
	require.Error(t, err)
	assert.NotErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid)
	f.assertUntouched(t)
}

// Edge: replay is refused while any owner is still live; TTL-only and foreign-operation proofs never recover.
func TestTurnLease_RecoveryFencesBeforeGenerationChangeAndNeverAcceptsTTLOnlyProof(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	replay := engine.TurnRecoveryProof{IntentID: f.intent.ID, Jobs: []engine.JobFenceRef{f.job}, Resource: &f.resource}

	_, err := f.repo.RecoverTurnLease(ctx, f.old, engine.TurnRecoveryProof{IntentID: f.intent.ID}, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "elapsed TTL alone is not proof")

	_, err = f.repo.RecoverTurnLease(ctx, f.old, replay, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "the job is still live at its fence")

	f.setJob(t, "queued", f.job.Generation+1) // requeued: old fence superseded...
	_, err = f.repo.RecoverTurnLease(ctx, f.old, replay, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "...but the resource is still owned")
	f.releaseResource(t)

	live := engine.JobFenceRef{JobID: uuid.New(), RunnerID: "mac-b", Generation: 1}
	f.insertJob(t, live, "turn/1/export", "offered") // a second live sub-operation of the same turn
	_, err = f.repo.RecoverTurnLease(ctx, f.old, replay, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "no live job may remain for the operation")
	_, err = f.db.DB.Exec(`UPDATE runner_jobs SET state = 'failed' WHERE id = ?`, live.JobID.String())
	require.NoError(t, err)

	turn10 := engine.JobFenceRef{JobID: uuid.New(), RunnerID: "mac-a", Generation: 1}
	f.insertJob(t, turn10, "turn/10", "running")
	_, err = f.repo.RecoverTurnLease(ctx, f.old, engine.TurnRecoveryProof{IntentID: f.intent.ID, Adopted: true,
		Jobs: []engine.JobFenceRef{turn10}}, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "turn/1 never adopts turn/10")
	_, err = f.repo.RecoverTurnLease(ctx, f.old, engine.TurnRecoveryProof{IntentID: uuid.New(), Jobs: []engine.JobFenceRef{f.job}}, time.Minute)
	assert.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid, "unknown intent")
	f.assertUntouched(t)

	got, err := f.repo.RecoverTurnLease(ctx, f.old, replay, time.Minute)
	require.NoError(t, err, "turn/10 is a different operation and does not block replay of turn/1")
	assert.Equal(t, f.old.Generation+1, got.Generation)
}

func TestTurnLease_AdoptionRequiresExactResourceOwner(t *testing.T) {
	f := newRecFixture(t)
	foreign := f.resource
	foreign.Owner = "other-runner"
	_, err := f.repo.RecoverTurnLease(context.Background(), f.old, engine.TurnRecoveryProof{
		IntentID: f.intent.ID, Adopted: true, Jobs: []engine.JobFenceRef{f.job}, Resource: &foreign,
	}, time.Minute)
	require.ErrorIs(t, err, engine.ErrTurnRecoveryProofInvalid)
	f.assertUntouched(t)
}
