package sqlite

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// turnCommitFixture: a waiting session (turn 1 committed as session:ref-1 on
// builtin, generation 1), a held lease row, a pending turn-2 intent, a reserved
// RUNNING stream job (step limit 1), an active workspace resource lease and the
// prepared v2 candidate that job produced. Reused by Cycle 7 and Cycle 8.
type turnCommitFixture struct {
	lf       ledgerFixture
	db       testDB
	inst     *instance.Instance
	exec     *instance.NodeExecution
	inv      uuid.UUID
	lease    engine.TurnLease
	intent   engine.TurnIntent
	job      runner.Job
	resource engine.ResourceFenceRef
	hash     string
	repo     *TurnCommitRepo
}

const priorRef = "session:ref-1"

func newTurnCommitFixture(t *testing.T) turnCommitFixture {
	t.Helper()
	ctx := context.Background()
	lf := newLedgerFixture(t)
	inst, exec := seedSessionExec(t, lf.db, "chat", 1, time.Now().UTC().Add(-time.Minute))
	inv := uuid.New()
	prior, pin := priorRef, "builtin"
	require.NoError(t, NewNodeExecutionRepo(lf.db.DB).SetSessionState(ctx, exec.ID,
		instance.SessionState{InvocationID: inv, CheckpointRef: &prior, PinRunner: &pin, Generation: 1}))

	lease := engine.TurnLease{Key: engine.RunLeaseKey(inst.ID), Token: uuid.New(), Generation: 1}
	_, err := lf.db.DB.Exec(`INSERT INTO turn_leases (key, token, generation, expires_at, holder) VALUES (?, ?, 1, ?, 'proc-a')`,
		lease.Key, lease.Token.String(), time.Now().Add(time.Minute).UnixMilli())
	require.NoError(t, err)

	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), Binding: engine.AcceptedTurnBinding{Model: "claude-sonnet-4-5"},
		LeaseKey: lease.Key, RunID: inst.ID, InvocationID: inv, NodeID: "chat", TurnIndex: 2, Kind: engine.TurnIntentMessage,
		Message: "next", PriorCheckpointRef: priorRef, SessionGeneration: 1, OperationID: "turn/2"}
	require.NoError(t, NewTurnIntentRepo(lf.db.DB).Persist(ctx, in))

	key := runner.JobKey{RunID: inst.ID, InvocationID: inv, OperationID: "turn/2", Attempt: 1}
	job, err := lf.ledger.EnsureIntent(ctx, key, "chat", anthropicRequirements(inst.ID))
	require.NoError(t, err)
	job, err = lf.ledger.Reserve(ctx, job.ID, lf.decision(lf.src), 1, ledgerNow.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, lf.ledger.MarkRunning(ctx, job.Fence()))

	res := engine.ResourceFenceRef{Key: "run:" + inst.ID.String(), Owner: string(runner.BuiltInRunnerID), Generation: 1, Token: uuid.New()}
	_, err = lf.db.DB.Exec(`INSERT INTO workspace_leases (resource_key, holder, owner, generation, token, active) VALUES (?, ?, ?, 1, ?, 1)`,
		res.Key, "job:"+job.ID.String(), res.Owner, res.Token.String())
	require.NoError(t, err)
	_, err = lf.db.DB.Exec(`INSERT INTO workspace_homes (run_id, runner_id, generation, checkpoint_version, dirty, updated_at) VALUES (?, '', 1, 1, 1, ?)`,
		inst.ID.String(), timeToString(ledgerNow))
	require.NoError(t, err)
	hash := strings.Repeat("b", 64)
	insertCandidate(t, lf.db, inst.ID, 2, hash, job, res, "prepared")
	// Durable P3 evidence (authorized fixture change, unstick 2026-11-24): the
	// committed v1 base the home points at, the v2 candidate's base_ref, and the
	// exact job holder row for the active claim.
	baseHash := strings.Repeat("a", 64)
	insertCandidate(t, lf.db, inst.ID, 1, baseHash, job, res, "committed")
	_, err = lf.db.DB.Exec(`UPDATE workspace_checkpoints SET base_ref = ? WHERE run_id = ? AND version = 2`,
		inst.ID.String()+"@1:"+baseHash, inst.ID.String())
	require.NoError(t, err)
	_, err = lf.db.DB.Exec(`INSERT INTO workspace_lease_holders(resource_key, generation, token, holder, owner, job_id, job_runner_id, job_generation, boot_id, claimed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		res.Key, res.Generation, res.Token.String(), "job:"+job.ID.String(), res.Owner, job.ID.String(), string(job.RunnerID), job.Generation, timeToString(ledgerNow))
	require.NoError(t, err)

	return turnCommitFixture{lf: lf, db: lf.db, inst: inst, exec: exec, inv: inv, lease: lease, intent: in, job: job,
		resource: res, hash: hash, repo: NewTurnCommitRepo(lf.db.DB)}
}

func insertCandidate(t *testing.T, db testDB, run uuid.UUID, version int64, hash string, job runner.Job, res engine.ResourceFenceRef, state string) {
	t.Helper()
	_, err := db.DB.Exec(`INSERT INTO workspace_checkpoints (run_id, version, manifest_hash, archive_path, size_bytes, job_id, job_generation, state,
		resource_key, resource_owner, resource_generation, resource_token, created_at) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.String(), version, hash, "/ck/"+run.String(), job.ID.String(), job.Generation, state,
		res.Key, res.Owner, res.Generation, res.Token.String(), timeToString(ledgerNow))
	require.NoError(t, err)
}

func (f turnCommitFixture) wsRef(version int64) string {
	return f.inst.ID.String() + "@" + strconv.FormatInt(version, 10) + ":" + f.hash
}

func (f turnCommitFixture) receipt() engine.StepCompletion {
	return engine.StepCompletion{
		JobFence:  engine.JobFenceRef{JobID: f.job.ID, RunnerID: string(f.job.RunnerID), Generation: f.job.Generation},
		Workspace: &engine.WorkspaceCompletion{Ref: f.wsRef(2), Resource: f.resource},
	}
}

func (f turnCommitFixture) turn() engine.TurnCommit {
	exec := *f.exec
	exec.LLMConversation = map[string]any{"sessionId": "s2", "nonce": "n", "messages": []any{
		map[string]any{"role": "user", "content": "next"}, map[string]any{"role": "assistant", "content": "done"}},
		engine.ConversationKeyWorkspaceCheckpointRef: f.wsRef(2)}
	return engine.TurnCommit{Execution: &exec, RequireWorkspace: true, CheckpointRef: "session:ref-2", PinRunner: "builtin",
		SessionGeneration: f.lease.Generation, LeaseKey: f.lease.Key, LeaseToken: f.lease.Token, IntentID: f.intent.ID,
		RequestID: f.intent.RequestID, InvocationID: f.inv, TurnIndex: 2, AcceptedBinding: f.intent.Binding,
		Completions: []engine.StepCompletion{f.receipt()}}
}

func (f turnCommitFixture) stored(t *testing.T) *instance.NodeExecution {
	t.Helper()
	got, err := NewNodeExecutionRepo(f.db.DB).GetByInstanceAndNode(context.Background(), f.inst.ID, "chat")
	require.NoError(t, err)
	return got
}

func (f turnCommitFixture) pending(t *testing.T) int {
	t.Helper()
	p, err := NewTurnIntentRepo(f.db.DB).Pending(context.Background())
	require.NoError(t, err)
	return len(p)
}

func (f turnCommitFixture) jobState(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, f.db.DB.QueryRow(`SELECT state FROM runner_jobs WHERE id = ?`, f.job.ID.String()).Scan(&s))
	return s
}

func (f turnCommitFixture) homeVersion(t *testing.T) int64 {
	t.Helper()
	var v int64
	require.NoError(t, f.db.DB.QueryRow(`SELECT checkpoint_version FROM workspace_homes WHERE run_id = ?`, f.inst.ID.String()).Scan(&v))
	return v
}

func (f turnCommitFixture) candidateState(t *testing.T, version int64) string {
	t.Helper()
	var s string
	require.NoError(t, f.db.DB.QueryRow(`SELECT state FROM workspace_checkpoints WHERE run_id = ? AND version = ?`, f.inst.ID.String(), version).Scan(&s))
	return s
}

// assertPriorPair: nothing from the rejected turn became visible.
func (f turnCommitFixture) assertPriorPair(t *testing.T) {
	t.Helper()
	got := f.stored(t)
	assert.Len(t, got.LLMConversation["messages"], 0, "messages unchanged")
	require.NotNil(t, got.SessionCheckpointRef)
	assert.Equal(t, priorRef, *got.SessionCheckpointRef, "prior pair kept")
	assert.Equal(t, 1, f.pending(t), "intent still pending for recovery")
	assert.Equal(t, "running", f.jobState(t), "the slot is not released by a failed commit")
	assert.EqualValues(t, 1, f.homeVersion(t), "the committed workspace pointer did not move")
	assert.Equal(t, "prepared", f.candidateState(t, 2))
}

// Happy: every write becomes visible in one transaction.
func TestTurnCommitRepo_CommitsPairMessagesStateIntentAndJobTogether(t *testing.T) {
	f := newTurnCommitFixture(t)
	require.Equal(t, 1, f.lf.slots(t), "one slot held by the stream at limit one")
	require.NoError(t, f.repo.CommitTurn(context.Background(), f.turn()))
	got := f.stored(t)
	assert.Len(t, got.LLMConversation["messages"], 2)
	assert.Equal(t, f.wsRef(2), got.LLMConversation[engine.ConversationKeyWorkspaceCheckpointRef])
	require.NotNil(t, got.SessionCheckpointRef)
	assert.Equal(t, "session:ref-2", *got.SessionCheckpointRef)
	assert.Equal(t, "builtin", *got.SessionPinRunner)
	assert.Equal(t, f.lease.Generation, got.SessionGeneration)
	assert.Equal(t, f.inv, got.SessionInvocationID)
	assert.Zero(t, f.pending(t), "the intent completes in the same transaction")
	assert.Equal(t, "completed", f.jobState(t))
	assert.Equal(t, 0, f.lf.slots(t), "job completion frees the slot")
	assert.EqualValues(t, 2, f.homeVersion(t), "the committed workspace pointer advanced inside the transaction")
	assert.Equal(t, "committed", f.candidateState(t, 2))
}

// Success: an exact retry after commit returns the committed outcome and writes nothing.
func TestTurnCommitRepo_ExactRetryIsIdempotent(t *testing.T) {
	f := newTurnCommitFixture(t)
	ctx := context.Background()
	require.NoError(t, f.repo.CommitTurn(ctx, f.turn()))
	before := f.stored(t)
	require.NoError(t, f.repo.CommitTurn(ctx, f.turn()), "crash after SQL commit: replay is the committed result")
	after := f.stored(t)
	assert.Equal(t, before.LLMConversation, after.LLMConversation, "no duplicate transcript")
	assert.Equal(t, *before.SessionCheckpointRef, *after.SessionCheckpointRef)
	assert.Zero(t, f.pending(t))

	other := f.turn()
	other.CheckpointRef = "session:different"
	assert.ErrorIs(t, f.repo.CommitTurn(ctx, other), engine.ErrTurnFenced, "a different payload for a completed intent is fenced")
}

// Failure: a stale lease token or generation, old job generation, foreign/stale
// resource, wrong candidate or closed group session rolls back every write.
func TestTurnCommitRepo_StaleFenceRollsBackEverything(t *testing.T) {
	cases := map[string]struct {
		mut func(*turnCommitFixture, *engine.TurnCommit)
		is  error
	}{
		"other lease token": {func(_ *turnCommitFixture, c *engine.TurnCommit) { c.LeaseToken = uuid.New() }, engine.ErrTurnFenced},
		"lease generation":  {func(_ *turnCommitFixture, c *engine.TurnCommit) { c.SessionGeneration = 2 }, engine.ErrTurnFenced},
		"old job generation": {func(_ *turnCommitFixture, c *engine.TurnCommit) {
			c.Completions[0].JobFence.Generation--
		}, engine.ErrTurnFenced},
		"job of another invocation": {func(f *turnCommitFixture, c *engine.TurnCommit) {
			c.InvocationID = uuid.New()
		}, engine.ErrTurnFenced},
		"resource token": {func(_ *turnCommitFixture, c *engine.TurnCommit) {
			w := *c.Completions[0].Workspace
			w.Resource.Token = uuid.New()
			c.Completions[0].Workspace = &w
		}, engine.ErrTurnFenced},
		"candidate of another job": {func(f *turnCommitFixture, c *engine.TurnCommit) {
			other := f.job
			other.ID = uuid.New()
			insertCandidate(t, f.db, f.inst.ID, 3, f.hash, other, f.resource, "prepared")
			w := *c.Completions[0].Workspace
			w.Ref = f.wsRef(3)
			c.Completions[0].Workspace = &w
		}, engine.ErrInvalidStepCompletion},
		"missing workspace receipt": {func(_ *turnCommitFixture, c *engine.TurnCommit) { c.Completions[0].Workspace = nil }, engine.ErrInvalidStepCompletion},
		"no completions":            {func(_ *turnCommitFixture, c *engine.TurnCommit) { c.Completions = nil }, engine.ErrInvalidStepCompletion},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnCommitFixture(t)
			c := f.turn()
			tc.mut(&f, &c)
			require.ErrorIs(t, f.repo.CommitTurn(context.Background(), c), tc.is)
			f.assertPriorPair(t)
		})
	}

	t.Run("closed group session", func(t *testing.T) {
		f := newTurnCommitFixture(t)
		ctx := context.Background()
		ended := *f.exec
		ended.LLMConversation = map[string]any{"sessionId": "s1", "messages": []any{}, "endedAt": "2026-11-20T00:00:00Z"}
		require.NoError(t, NewNodeExecutionRepo(f.db.DB).Update(ctx, &ended))
		c := f.turn()
		c.RequireOpenSession = true
		require.ErrorIs(t, f.repo.CommitTurn(ctx, c), instance.ErrNotInteractiveWait)
		assert.Contains(t, f.stored(t).LLMConversation, "endedAt", "a closed session stays closed")
		assert.Equal(t, 1, f.pending(t))
		assert.Equal(t, "running", f.jobState(t))
	})
}

// Error: a failure at each statement (execution, pointer, session state, intent,
// job completion) leaves no half pair and no slot release. Triggers make the
// named statement fail inside the real transaction.
func TestTurnCommitRepo_StatementFailureLeavesNoHalfPair(t *testing.T) {
	triggers := map[string]string{
		"execution":     `CREATE TRIGGER fail_exec BEFORE UPDATE OF llm_conversation ON node_executions BEGIN SELECT RAISE(ABORT, 'inject exec'); END`,
		"pointer":       `CREATE TRIGGER fail_ptr BEFORE UPDATE OF checkpoint_version ON workspace_homes BEGIN SELECT RAISE(ABORT, 'inject pointer'); END`,
		"session state": `CREATE TRIGGER fail_state BEFORE UPDATE OF session_checkpoint_ref ON node_executions BEGIN SELECT RAISE(ABORT, 'inject state'); END`,
		"intent":        `CREATE TRIGGER fail_intent BEFORE UPDATE OF completed_at ON turn_intents BEGIN SELECT RAISE(ABORT, 'inject intent'); END`,
		"job":           `CREATE TRIGGER fail_job BEFORE UPDATE OF state ON runner_jobs BEGIN SELECT RAISE(ABORT, 'inject job'); END`,
	}
	for name, ddl := range triggers {
		t.Run(name, func(t *testing.T) {
			f := newTurnCommitFixture(t)
			_, err := f.db.DB.Exec(ddl)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // single-connection deadlock guard (contracts §21)
			defer cancel()
			err = f.repo.CommitTurn(ctx, f.turn())
			require.Error(t, err)
			assert.False(t, errors.Is(err, context.DeadlineExceeded), "the commit must not deadlock on its own connection")
			_, err = f.db.DB.Exec(`DROP TRIGGER ` + strings.Fields(ddl)[2])
			require.NoError(t, err)
			f.assertPriorPair(t)
		})
	}
}

// Edge: an older turn at the SAME generation can never overwrite a newer
// committed checkpoint (P4 T1 alignment acceptance), and a first-turn create
// for an invocation that already has a committed session is fenced.
func TestSessionState_OldTurnSameGenerationCannotOverwriteNewCheckpoint(t *testing.T) {
	f := newTurnCommitFixture(t)
	ctx := context.Background()
	turn2 := f.turn()
	require.NoError(t, f.repo.CommitTurn(ctx, turn2))

	// Turn 3 commits on top of turn 2 under the same lease generation.
	in3 := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), Binding: f.intent.Binding, LeaseKey: f.lease.Key, RunID: f.inst.ID,
		InvocationID: f.inv, NodeID: "chat", TurnIndex: 3, Kind: engine.TurnIntentMessage, Message: "third",
		PriorCheckpointRef: "session:ref-2", SessionGeneration: 1, OperationID: "turn/3"}
	require.NoError(t, NewTurnIntentRepo(f.db.DB).Persist(ctx, in3))
	job3, err := f.lf.ledger.EnsureIntent(ctx, runner.JobKey{RunID: f.inst.ID, InvocationID: f.inv, OperationID: "turn/3", Attempt: 1}, "chat", anthropicRequirements(f.inst.ID))
	require.NoError(t, err)
	job3, err = f.lf.ledger.Reserve(ctx, job3.ID, f.lf.decision(f.lf.src), 1, ledgerNow.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, f.lf.ledger.MarkRunning(ctx, job3.Fence()))
	// Turn 2's commit released its claim; turn 3's stream job takes a NEW claim
	// (authorized fixture change, unstick 2026-11-24).
	res3, err := NewResourceLeaseRepo(f.db.DB, "").ClaimJob(ctx, f.resource.Key, "job:"+job3.ID.String(), job3.Fence())
	require.NoError(t, err)
	hash3 := strings.Repeat("c", 64)
	insertCandidate(t, f.db, f.inst.ID, 3, hash3, job3, res3, "prepared")
	_, err = f.db.DB.Exec(`UPDATE workspace_checkpoints SET base_ref = ? WHERE run_id = ? AND version = 3`, f.wsRef(2), f.inst.ID.String())
	require.NoError(t, err)
	turn3 := f.turn()
	turn3.CheckpointRef, turn3.IntentID, turn3.RequestID, turn3.TurnIndex = "session:ref-3", in3.ID, in3.RequestID, 3
	turn3.Completions = []engine.StepCompletion{{JobFence: engine.JobFenceRef{JobID: job3.ID, RunnerID: string(job3.RunnerID), Generation: job3.Generation},
		Workspace: &engine.WorkspaceCompletion{Ref: f.inst.ID.String() + "@3:" + hash3, Resource: res3}}}
	require.NoError(t, f.repo.CommitTurn(ctx, turn3))

	// (a) A late replay of the completed turn 2 at the same generation is fenced.
	require.ErrorIs(t, f.repo.CommitTurn(ctx, turn2), engine.ErrTurnFenced)
	// (b) A newly accepted but stale intent whose prior is ref-1 is fenced by the prior-pair CAS.
	stale := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), Binding: f.intent.Binding, LeaseKey: f.lease.Key, RunID: f.inst.ID,
		InvocationID: f.inv, NodeID: "chat", TurnIndex: 2, Kind: engine.TurnIntentMessage, Message: "late",
		PriorCheckpointRef: priorRef, SessionGeneration: 1, OperationID: "turn/2b"}
	require.NoError(t, NewTurnIntentRepo(f.db.DB).Persist(ctx, stale))
	late := f.turn()
	late.IntentID, late.RequestID, late.CheckpointRef = stale.ID, stale.RequestID, "session:late"
	require.ErrorIs(t, f.repo.CommitTurn(ctx, late), engine.ErrTurnFenced)

	got := f.stored(t)
	assert.Equal(t, "session:ref-3", *got.SessionCheckpointRef, "the newest committed checkpoint survives")
	assert.EqualValues(t, 3, f.homeVersion(t))

	// First-turn create for an invocation that already has a committed session.
	first := &instance.NodeExecution{ID: uuid.New(), InstanceID: f.inst.ID, NodeID: "chat", Attempt: 2,
		Status: instance.NodeStatusSucceeded, StartedAt: time.Now().UTC(), LLMConversation: map[string]any{"messages": []any{}}}
	dup := f.turn()
	dup.Execution, dup.Create = first, true
	assert.ErrorIs(t, f.repo.CommitTurn(ctx, dup), engine.ErrTurnFenced)
}

func TestNodeExecutionRepo_UpdateIfSessionOpenStillGuards(t *testing.T) {
	db := setupTestDB(t)
	_, exec := seedSessionExec(t, db, "chat", 1, time.Now().UTC())
	repo := NewNodeExecutionRepo(db.DB)
	exec.LLMConversation = map[string]any{"messages": []any{}, "endedAt": "x"}
	ok, err := repo.UpdateIfSessionOpen(context.Background(), exec)
	require.NoError(t, err)
	assert.True(t, ok)
	exec.LLMConversation = map[string]any{"messages": []any{"late"}}
	ok, err = repo.UpdateIfSessionOpen(context.Background(), exec)
	require.NoError(t, err)
	assert.False(t, ok)
}
