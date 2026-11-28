package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedMoveExec(t *testing.T, db testDB, runID uuid.UUID, gen int64) *instance.NodeExecution {
	t.Helper()
	pin := "mac-a"
	exec := &instance.NodeExecution{
		ID: uuid.New(), InstanceID: runID, NodeID: "talk", Attempt: 1, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{"sessionId": "old-sid", "nonce": "n", "messages": []any{
			map[string]any{"role": "user", "content": "p"}, map[string]any{"role": "assistant", "content": "a"}}},
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	repo := NewNodeExecutionRepo(db.DB)
	require.NoError(t, repo.Create(context.Background(), exec))
	require.NoError(t, repo.SetSessionState(context.Background(), exec.ID, instance.SessionState{PinRunner: &pin, Generation: gen}))
	return exec
}

func moveCommit(runID, execID uuid.UUID, gen int64) runner.SessionMoveCommit {
	return runner.SessionMoveCommit{
		ExecutionID: execID, SessionID: "", PendingRebuild: true,
		Move: instance.SessionMoveRecord{ID: uuid.New(), RunID: runID, InvocationID: uuid.New(), NodeID: "talk",
			FromRunner: "mac-a", ToRunner: "mac-b", Reason: "exhausted", Lossy: true,
			Notice: "tool details and reasoning may be lost", Generation: gen, At: time.Now().UTC()},
	}
}

func TestNodeExecutionRepo_RecordSessionMoveUnchangedByRefactor(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	runID := seedInstance(t, db, localUserID(t), "waiting")
	exec := seedMoveExec(t, db, runID, 1)
	c := moveCommit(runID, exec.ID, 2)
	require.NoError(t, NewNodeExecutionRepo(db.DB).RecordSessionMove(ctx, exec.ID, c.Move))
	got, err := NewNodeExecutionRepo(db.DB).GetByInstanceAndNode(ctx, runID, "talk")
	require.NoError(t, err)
	assert.Equal(t, "old-sid", got.LLMConversation["sessionId"], "RecordSessionMove alone never touches the conversation")
	assert.Len(t, got.SessionMoves, 1)
}

// adapter/sqlite/session_move_repo_test.go (Cycle 3b: fixture + replaced/added tests)

// moveRepoFixture: session "talk" pinned to mac-a at storedGen (checkpoint
// ckpt-1, invocation inv); the turn lease held at leaseGen; a BEGUN move to the
// built-in runner (state importing) owning a pending kind=move intent; its
// reserved RUNNING session_import job; and the move's active restore claim.
type moveRepoFixture struct {
	lf       ledgerFixture
	db       testDB
	inst     *instance.Instance
	exec     *instance.NodeExecution
	inv      uuid.UUID
	lease    engine.TurnLease
	intent   engine.TurnIntent
	state    runner.SessionMoveState
	job      runner.Job
	resource engine.ResourceFenceRef
	repo     *SessionMoveRepo
}

func newMoveRepoFixture(t *testing.T, storedGen, leaseGen int64) moveRepoFixture {
	t.Helper()
	ctx := context.Background()
	lf := newLedgerFixture(t)
	inst, exec := seedSessionExec(t, lf.db, "talk", 1, time.Now().UTC().Add(-time.Minute))
	inv, ref, pin := uuid.New(), "ckpt-1", "mac-a"
	require.NoError(t, NewNodeExecutionRepo(lf.db.DB).SetSessionState(ctx, exec.ID,
		instance.SessionState{InvocationID: inv, CheckpointRef: &ref, PinRunner: &pin, Generation: storedGen}))

	lease := engine.TurnLease{Key: engine.RunLeaseKey(inst.ID), Token: uuid.New(), Generation: leaseGen}
	_, err := lf.db.DB.Exec(`INSERT INTO turn_leases (key, token, generation, expires_at, holder) VALUES (?, ?, ?, ?, 'proc-a')`,
		lease.Key, lease.Token.String(), leaseGen, time.Now().Add(time.Minute).UnixMilli())
	require.NoError(t, err)

	op := fmt.Sprintf("move/%d", leaseGen)
	req := anthropicRequirements(inst.ID)
	req.Op = runner.OpSessionImport
	job, err := lf.ledger.EnsureIntent(ctx, runner.JobKey{RunID: inst.ID, InvocationID: inv, OperationID: op, Attempt: 1}, "talk", req)
	require.NoError(t, err)
	job, err = lf.ledger.Reserve(ctx, job.ID, lf.decision(lf.src), 1, ledgerNow.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, lf.ledger.MarkRunning(ctx, job.Fence()))

	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(),
		Binding:  engine.AcceptedTurnBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"},
		LeaseKey: lease.Key, RunID: inst.ID, InvocationID: inv, NodeID: "talk", TurnIndex: 2, Kind: engine.TurnIntentMove,
		PriorCheckpointRef: ref, SessionGeneration: leaseGen - 1, OperationID: op}
	st := runner.SessionMoveState{
		Move: instance.SessionMoveRecord{ID: uuid.New(), RunID: inst.ID, InvocationID: inv, NodeID: "talk", FromRunner: "mac-a",
			ToRunner: string(runner.BuiltInRunnerID), Reason: "runner_lost", Generation: leaseGen, At: ledgerNow},
		State: runner.MoveStatePreparing, IntentID: in.ID, OwnsIntent: true, ExecutionID: exec.ID, TargetSource: lf.src,
		JobID: job.ID, Requirements: req, WorkDir: "/ws", RestoreWorkspace: true,
	}
	repo := NewSessionMoveRepo(lf.db.DB)
	st, err = repo.BeginMove(ctx, runner.SessionMoveBegin{State: st, Intent: &in})
	require.NoError(t, err)

	res := engine.ResourceFenceRef{Key: "run:" + inst.ID.String(), Owner: string(runner.BuiltInRunnerID), Generation: 1, Token: uuid.New()}
	_, err = lf.db.DB.Exec(`INSERT INTO workspace_leases (resource_key, holder, owner, generation, token, active) VALUES (?, ?, ?, 1, ?, 1)`,
		res.Key, "session-move:"+st.Move.ID.String(), res.Owner, res.Token.String())
	require.NoError(t, err)
	st.State, st.NewResource = runner.MoveStateImporting, &res
	require.NoError(t, repo.AdvanceMove(ctx, st))

	return moveRepoFixture{lf: lf, db: lf.db, inst: inst, exec: exec, inv: inv, lease: lease, intent: in, state: st, job: job,
		resource: res, repo: repo}
}

// commit is the exact commit a correct mover would send for the fixture move.
func (f moveRepoFixture) commit() runner.SessionMoveCommit {
	mv := f.state.Move
	mv.Lossy, mv.Notice = true, engine.SessionLossNotice
	return runner.SessionMoveCommit{ExecutionID: f.exec.ID, Move: mv, SessionID: "sid-b", Provisional: true,
		Lease: f.lease, InvocationID: f.inv, IntentID: f.intent.ID, Resource: f.resource,
		Completions: []engine.StepCompletion{{JobFence: engine.JobFenceRef{JobID: f.job.ID, RunnerID: string(f.job.RunnerID), Generation: f.job.Generation}}}}
}

func (f moveRepoFixture) stored(t *testing.T) *instance.NodeExecution {
	t.Helper()
	got, err := NewNodeExecutionRepo(f.db.DB).GetByInstanceAndNode(context.Background(), f.inst.ID, "talk")
	require.NoError(t, err)
	return got
}

func (f moveRepoFixture) pending(t *testing.T) int {
	t.Helper()
	p, err := NewTurnIntentRepo(f.db.DB).Pending(context.Background())
	require.NoError(t, err)
	return len(p)
}

func (f moveRepoFixture) jobState(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, f.db.DB.QueryRow(`SELECT state FROM runner_jobs WHERE id = ?`, f.job.ID.String()).Scan(&s))
	return s
}

func (f moveRepoFixture) resourceActive(t *testing.T) bool {
	t.Helper()
	var active int
	require.NoError(t, f.db.DB.QueryRow(`SELECT active FROM workspace_leases WHERE resource_key = ? AND token = ?`,
		f.resource.Key, f.resource.Token.String()).Scan(&active))
	return active == 1
}

// assertUncommitted: a rejected commit changed NOTHING (one transaction).
func (f moveRepoFixture) assertUncommitted(t *testing.T, gen int64, jobState string, resourceActive bool) {
	t.Helper()
	got := f.stored(t)
	require.NotNil(t, got.SessionPinRunner)
	assert.Equal(t, "mac-a", *got.SessionPinRunner, "pin unchanged")
	assert.Equal(t, gen, got.SessionGeneration, "generation unchanged")
	assert.Equal(t, "s1", got.LLMConversation["sessionId"], "conversation patch rolled back")
	assert.Nil(t, got.LLMConversation["provisionalResume"])
	assert.Empty(t, got.SessionMoves, "no committed history row")
	assert.Equal(t, 1, f.pending(t), "the move intent stays pending for ResumeMove")
	assert.Equal(t, jobState, f.jobState(t), "the import job was neither completed nor released by the rejected commit")
	assert.Equal(t, resourceActive, f.resourceActive(t), "the restore claim was not released by the rejected commit")
	st, found, err := f.repo.MoveForIntent(context.Background(), f.intent.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, runner.MoveStateImporting, st.State, "the move stays open and resumable")
}

// Happy (replaces Cycle 2's generation-only version): pin, generation,
// conversation, history row, owned intent, import job and restore claim all
// change in ONE transaction; an exact retry is idempotent.
func TestSessionMoveRepo_CommitMoveUpdatesPinGenerationConversationAndRecordsRow(t *testing.T) {
	f := newMoveRepoFixture(t, 3, 4)
	ctx := context.Background()

	require.NoError(t, f.repo.CommitMove(ctx, f.commit()))

	got := f.stored(t)
	require.NotNil(t, got.SessionPinRunner)
	assert.Equal(t, string(runner.BuiltInRunnerID), *got.SessionPinRunner)
	assert.Equal(t, int64(4), got.SessionGeneration)
	assert.Equal(t, "sid-b", got.LLMConversation["sessionId"])
	assert.Equal(t, false, got.LLMConversation["pendingRebuild"])
	assert.Equal(t, true, got.LLMConversation["provisionalResume"], "a native import's first resume is provisional")
	assert.Equal(t, "n", got.LLMConversation["nonce"], "nonce untouched")
	assert.Len(t, got.LLMConversation["messages"], 0, "messages stay the source of truth, untouched")
	require.Len(t, got.SessionMoves, 1)
	assert.Equal(t, f.state.Move.ID, got.SessionMoves[0].ID)
	assert.Equal(t, "mac-a", got.SessionMoves[0].FromRunner)
	assert.Equal(t, string(runner.BuiltInRunnerID), got.SessionMoves[0].ToRunner)
	assert.Equal(t, "runner_lost", got.SessionMoves[0].Reason)
	assert.True(t, got.SessionMoves[0].Lossy)
	assert.Equal(t, engine.SessionLossNotice, got.SessionMoves[0].Notice)
	assert.Equal(t, int64(4), got.SessionMoves[0].Generation)

	st, found, err := f.repo.MoveForIntent(ctx, f.intent.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, runner.MoveStateCommitted, st.State)
	assert.Zero(t, f.pending(t), "the owned move intent completed in the same transaction")
	assert.Equal(t, "completed", f.jobState(t), "the import job completes with the move")
	assert.Zero(t, f.lf.slots(t), "no slot stays reserved")
	assert.False(t, f.resourceActive(t), "the restore claim is released with the move")

	require.NoError(t, f.repo.CommitMove(ctx, f.commit()), "an exact retry of a committed move is idempotent")
	assert.Len(t, f.stored(t).SessionMoves, 1)
}

func TestSessionMoveRepo_CommitMoveRejectsStaleGenerationAndRollsBack(t *testing.T) {
	f := newMoveRepoFixture(t, 5, 5)
	err := f.repo.CommitMove(context.Background(), f.commit())
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrStaleSessionGeneration, "a move must be strictly newer than the committed generation")
	f.assertUncommitted(t, 5, "running", true)
}

func TestSessionMoveRepo_CommitMoveUnknownExecution(t *testing.T) {
	f := newMoveRepoFixture(t, 1, 2)
	_, err := f.db.DB.Exec(`DELETE FROM node_executions WHERE id = ?`, f.exec.ID.String())
	require.NoError(t, err)
	err = f.repo.CommitMove(context.Background(), f.commit())
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.Equal(t, 1, f.pending(t))
	assert.Equal(t, "running", f.jobState(t))
}

// Error (alignment): a stale mover cannot commit after another holder takes
// the lease, even with a numerically newer generation; a stale/foreign job or
// resource fence is refused; nothing becomes visible.
func TestSessionMoveRepo_RejectsWrongLeaseTokenJobAndResourceAtomically(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutate         func(t *testing.T, f moveRepoFixture, c *runner.SessionMoveCommit)
		want           error
		jobState       string
		resourceActive bool
	}{
		{"wrong lease token", func(_ *testing.T, _ moveRepoFixture, c *runner.SessionMoveCommit) { c.Lease.Token = uuid.New() },
			engine.ErrTurnFenced, "running", true},
		{"lease taken by another holder", func(t *testing.T, f moveRepoFixture, _ *runner.SessionMoveCommit) {
			_, err := f.db.DB.Exec(`UPDATE turn_leases SET token = ? WHERE key = ?`, uuid.New().String(), f.lease.Key)
			require.NoError(t, err)
		}, engine.ErrTurnFenced, "running", true},
		{"lease generation moved past the move", func(t *testing.T, f moveRepoFixture, _ *runner.SessionMoveCommit) {
			_, err := f.db.DB.Exec(`UPDATE turn_leases SET generation = generation + 1 WHERE key = ?`, f.lease.Key)
			require.NoError(t, err)
		}, engine.ErrTurnFenced, "running", true},
		{"stale import job generation", func(_ *testing.T, _ moveRepoFixture, c *runner.SessionMoveCommit) {
			c.Completions[0].JobFence.Generation++
		}, engine.ErrTurnFenced, "running", true},
		{"another job", func(_ *testing.T, _ moveRepoFixture, c *runner.SessionMoveCommit) {
			c.Completions[0].JobFence.JobID = uuid.New()
		},
			engine.ErrTurnFenced, "running", true},
		{"import job already reaped", func(t *testing.T, f moveRepoFixture, _ *runner.SessionMoveCommit) {
			require.NoError(t, f.lf.ledger.Fail(context.Background(), f.job.Fence(), runner.OutcomeOther))
		}, engine.ErrTurnFenced, "failed", true},
		{"no import receipt", func(_ *testing.T, _ moveRepoFixture, c *runner.SessionMoveCommit) { c.Completions = nil },
			engine.ErrInvalidStepCompletion, "running", true},
		{"wrong resource token", func(_ *testing.T, _ moveRepoFixture, c *runner.SessionMoveCommit) { c.Resource.Token = uuid.New() },
			engine.ErrTurnFenced, "running", true},
		{"restore claim no longer active", func(t *testing.T, f moveRepoFixture, _ *runner.SessionMoveCommit) {
			_, err := f.db.DB.Exec(`UPDATE workspace_leases SET active = 0 WHERE resource_key = ?`, f.resource.Key)
			require.NoError(t, err)
		}, engine.ErrTurnFenced, "running", false},
		{"another intent", func(_ *testing.T, _ moveRepoFixture, c *runner.SessionMoveCommit) { c.IntentID = uuid.New() },
			engine.ErrTurnFenced, "running", true},
		{"another target", func(_ *testing.T, _ moveRepoFixture, c *runner.SessionMoveCommit) { c.Move.ToRunner = "mac-z" },
			engine.ErrTurnFenced, "running", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveRepoFixture(t, 3, 4)
			c := f.commit()
			tc.mutate(t, f, &c)
			err := f.repo.CommitMove(context.Background(), c)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
			f.assertUncommitted(t, 3, tc.jobState, tc.resourceActive)
		})
	}
}

// Success: the move intent and its row are persisted together before any
// effect; re-beginning returns persisted progress; one open move per intent.
func TestSessionMoveRepo_BeginMovePersistsMoveIntentWithItsRowAndIsIdempotent(t *testing.T) {
	f := newMoveRepoFixture(t, 1, 2)
	ctx := context.Background()

	pending, err := NewTurnIntentRepo(f.db.DB).PendingFor(ctx, f.lease.Key)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, f.intent.ID, pending.ID)
	assert.Equal(t, engine.TurnIntentMove, pending.Kind)
	assert.Equal(t, "move/2", pending.OperationID)
	assert.Equal(t, "claude-cli", pending.Binding.Provider, "the accepted binding is the resend source, not the current profile")

	st, found, err := f.repo.MoveForIntent(ctx, f.intent.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, f.state.Move.ID, st.Move.ID)
	assert.Equal(t, runner.MoveStateImporting, st.State)
	assert.Equal(t, int64(2), st.Move.Generation)
	assert.Equal(t, f.job.ID, st.JobID)
	assert.Equal(t, f.lf.src, st.TargetSource)
	assert.Equal(t, runner.OpSessionImport, st.Requirements.Op)
	assert.Equal(t, f.exec.ID, st.ExecutionID)
	assert.True(t, st.OwnsIntent)
	assert.True(t, st.RestoreWorkspace)
	require.NotNil(t, st.NewResource)
	assert.Equal(t, f.resource, *st.NewResource)
	assert.Empty(t, f.stored(t).SessionMoves, "an open move is not committed history")

	again := f.state
	again.State, again.NewResource = runner.MoveStatePreparing, nil
	got, err := f.repo.BeginMove(ctx, runner.SessionMoveBegin{State: again, Intent: &f.intent})
	require.NoError(t, err)
	assert.Equal(t, runner.MoveStateImporting, got.State, "re-beginning never resets persisted progress")

	other := again
	other.Move.ID = uuid.New()
	_, err = f.repo.BeginMove(ctx, runner.SessionMoveBegin{State: other, Intent: &f.intent})
	assert.ErrorIs(t, err, engine.ErrTurnFenced, "one open move per intent")
	var n int
	require.NoError(t, f.db.DB.QueryRow(`SELECT COUNT(*) FROM session_moves`).Scan(&n))
	assert.Equal(t, 1, n)
}

// Failure: a deterministic failure closes the move and its own intent; the
// old pin stays; a failed move never reopens or commits.
func TestSessionMoveRepo_FailMoveClosesOwnedIntentAndKeepsPin(t *testing.T) {
	f := newMoveRepoFixture(t, 1, 2)
	ctx := context.Background()
	require.NoError(t, f.repo.FailMove(ctx, f.state.Move.ID, "rebuild too large"))
	require.NoError(t, f.repo.FailMove(ctx, f.state.Move.ID, "rebuild too large"), "idempotent")

	st, found, err := f.repo.MoveForIntent(ctx, f.intent.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, runner.MoveStateFailed, st.State)
	assert.Equal(t, "rebuild too large", st.Failure)
	assert.Zero(t, f.pending(t), "a failed move closes its own intent")
	assert.Equal(t, "mac-a", *f.stored(t).SessionPinRunner)
	assert.Empty(t, f.stored(t).SessionMoves)
	assert.ErrorIs(t, f.repo.AdvanceMove(ctx, f.state), engine.ErrTurnFenced, "a failed move never reopens")
	assert.ErrorIs(t, f.repo.CommitMove(ctx, f.commit()), engine.ErrTurnFenced)
}
