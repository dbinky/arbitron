package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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

// stepFixture is one run with an open invocation of node "A", the built-in
// runner registered, and the real ledger / lease / home / catalog repos over
// the same single-connection database the server uses.
type stepFixture struct {
	db     *sql.DB
	repo   *StepCommitRepo
	ledger *RunnerLedgerRepo
	leases *ResourceLeaseRepo
	homes  *WorkspaceHomeRepo
	run    uuid.UUID
	inv    uuid.UUID
	key    string
}

func newStepFixture(t *testing.T, db *sql.DB) stepFixture {
	t.Helper()
	run, inv := seedOpenInvocation(t, db)
	return stepFixtureFor(t, db, run, inv)
}

func stepFixtureFor(t *testing.T, db *sql.DB, run, inv uuid.UUID) stepFixture {
	t.Helper()
	require.NoError(t, NewRunnerRegistryRepo(db).UpsertRunner(context.Background(), runner.Runner{
		ID: runner.BuiltInRunnerID, Name: "builtin", BuiltIn: true, StepLimit: 8, Liveness: runner.LivenessOnline}))
	return stepFixture{db: db, repo: NewStepCommitRepo(db), ledger: NewRunnerLedgerRepo(db),
		leases: NewResourceLeaseRepo(db, "boot-step-commit-test"), homes: NewWorkspaceHomeRepo(db),
		run: run, inv: inv, key: "run:" + run.String()}
}

func stepHash(c byte) string { return strings.Repeat(string(c), 64) }

// baseline seeds the committed version-1 checkpoint exactly as P3 T2's
// Bootstrap leaves it: a committed catalog row and the home pointer at 1.
func (fx stepFixture) baseline(t *testing.T) runner.CheckpointRef {
	t.Helper()
	ref := runner.CheckpointRef{RunID: fx.run, Version: 1, ManifestHash: stepHash('1')}
	now := timeToString(time.Now())
	_, err := fx.db.Exec(`INSERT INTO workspace_checkpoints(run_id,version,manifest_hash,archive_path,size_bytes,state,base_ref,
		resource_key,resource_owner,resource_generation,resource_token,created_at) VALUES(?,1,?,?,1,'committed','',?,'builtin',1,?,?)`,
		fx.run.String(), ref.ManifestHash, "/ckpt/"+fx.run.String()+"/v1", fx.key, uuid.NewString(), now)
	require.NoError(t, err)
	_, err = fx.db.Exec(`INSERT INTO workspace_homes(run_id,runner_id,generation,checkpoint_version,dirty,updated_at) VALUES(?,'',0,1,0,?)`,
		fx.run.String(), now)
	require.NoError(t, err)
	return ref
}

// jobFor reserves one running built-in job of invocation inv. Phase-3
// workspace placement belongs to P3 T4/T6; the fixture only needs a running
// job whose persisted requirements name the affinity.
func (fx stepFixture) jobFor(t *testing.T, inv uuid.UUID, op string, affinity runner.Affinity) (runner.Job, runner.Fence) {
	t.Helper()
	ctx := context.Background()
	key := runner.JobKey{RunID: fx.run, InvocationID: inv, OperationID: op, Attempt: 1}
	j, err := fx.ledger.EnsureIntent(ctx, key, "A", runner.ExecRequirements{
		Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess, Affinity: runner.AffinityBuiltIn, RunID: fx.run})
	require.NoError(t, err)
	j, err = fx.ledger.Reserve(ctx, j.ID, runner.Decision{Placed: true, Runner: runner.BuiltInRunnerID, Reason: "test"}, 8, time.Now().Add(time.Hour))
	require.NoError(t, err)
	f := runner.Fence{JobID: j.ID, RunnerID: j.RunnerID, Generation: j.Generation}
	require.NoError(t, fx.ledger.MarkRunning(ctx, f))
	if affinity != runner.AffinityBuiltIn {
		_, err = fx.db.Exec(`UPDATE runner_jobs SET requirements = json_set(requirements, '$.Affinity', ?) WHERE id = ?`, string(affinity), j.ID.String())
		require.NoError(t, err)
	}
	return j, f
}

func (fx stepFixture) job(t *testing.T, op string, affinity runner.Affinity) (runner.Job, runner.Fence) {
	return fx.jobFor(t, fx.inv, op, affinity)
}

// claim takes the exact run resource for job j (P3 T3 ClaimJob).
func (fx stepFixture) claim(t *testing.T, j runner.Job, f runner.Fence) engine.ResourceFenceRef {
	t.Helper()
	res, err := fx.leases.ClaimJob(context.Background(), fx.key, runner.JobHolder(j), f)
	require.NoError(t, err)
	return res
}

// candidate prepares an immutable candidate through the real P3 T2 catalog.
func (fx stepFixture) candidate(t *testing.T, version int64, hash byte, base runner.CheckpointRef, f runner.Fence, res engine.ResourceFenceRef) runner.CheckpointRef {
	t.Helper()
	ref := runner.CheckpointRef{RunID: fx.run, Version: version, ManifestHash: stepHash(hash)}
	require.NoError(t, NewCheckpointCatalogRepo(fx.db).Publish(context.Background(), runner.CheckpointCandidate{
		Ref: ref, Base: base, Job: &f, Resource: res, ArchivePath: fmt.Sprintf("/ckpt/%s/v%d", fx.run, version), SizeBytes: 10}))
	return ref
}

func jobRef(f runner.Fence) engine.JobFenceRef {
	return engine.JobFenceRef{JobID: f.JobID, RunnerID: string(f.RunnerID), Generation: f.Generation}
}

func workspaceReceipt(f runner.Fence, ref runner.CheckpointRef, res engine.ResourceFenceRef) engine.StepCompletion {
	return engine.StepCompletion{JobFence: jobRef(f), Workspace: &engine.WorkspaceCompletion{Ref: ref.String(), Resource: res}}
}

func (fx stepFixture) advance(next string, comps ...engine.StepCompletion) engine.StepCommit {
	now := time.Now().UTC().Truncate(time.Second)
	return engine.StepCommit{Mode: engine.StepCommitAdvance, RunID: fx.run, InvocationID: fx.inv, NodeID: "A",
		Execution: &instance.NodeExecution{ID: uuid.New(), InstanceID: fx.run, NodeID: "A", Attempt: 1,
			Status: instance.NodeStatusSucceeded, Output: map[string]any{"answer": "42"}, StartedAt: now, CompletedAt: &now},
		Variables:  map[string]any{"key": "value", "A": map[string]any{"answer": "42"}},
		NextNodeID: next, Completions: comps}
}

// stepState is every row a step commit may touch, so a test can prove that a
// failure changed nothing at all.
type stepState struct {
	Execs, StepCommits, Ops, Suspensions int
	Jobs                                 map[uuid.UUID]string
	Candidates                           map[int64]string
	Pointer                              int64
	Dirty                                bool
	Reason                               string
	Vars, Current                        string
	InvCommitted, LeaseActive            bool
}

func (fx stepFixture) snapshot(t *testing.T) stepState {
	t.Helper()
	s := stepState{Jobs: map[uuid.UUID]string{}, Candidates: map[int64]string{}}
	s.Execs = countRows(t, fx.db, `SELECT COUNT(*) FROM node_executions WHERE instance_id = ?`, fx.run.String())
	s.StepCommits = countRows(t, fx.db, `SELECT COUNT(*) FROM step_commits WHERE run_id = ?`, fx.run.String())
	s.Ops = countRows(t, fx.db, `SELECT COUNT(*) FROM step_commit_operations WHERE invocation_id = ?`, fx.inv.String())
	s.Suspensions = countRows(t, fx.db, `SELECT COUNT(*) FROM step_suspensions WHERE invocation_id = ?`, fx.inv.String())
	rows, err := fx.db.Query(`SELECT id, state FROM runner_jobs WHERE run_id = ?`, fx.run.String())
	require.NoError(t, err)
	for rows.Next() {
		var id, state string
		require.NoError(t, rows.Scan(&id, &state))
		s.Jobs[uuid.MustParse(id)] = state
	}
	require.NoError(t, rows.Close())
	rows, err = fx.db.Query(`SELECT version, state FROM workspace_checkpoints WHERE run_id = ?`, fx.run.String())
	require.NoError(t, err)
	for rows.Next() {
		var v int64
		var state string
		require.NoError(t, rows.Scan(&v, &state))
		s.Candidates[v] = state
	}
	require.NoError(t, rows.Close())
	err = fx.db.QueryRow(`SELECT checkpoint_version, dirty, non_portable_reason FROM workspace_homes WHERE run_id = ?`, fx.run.String()).
		Scan(&s.Pointer, &s.Dirty, &s.Reason)
	if err != sql.ErrNoRows {
		require.NoError(t, err)
	}
	var cur, committed sql.NullString
	require.NoError(t, fx.db.QueryRow(`SELECT variables, current_node_id FROM instances WHERE id = ?`, fx.run.String()).Scan(&s.Vars, &cur))
	s.Current = cur.String
	require.NoError(t, fx.db.QueryRow(`SELECT committed_at FROM node_invocations WHERE id = ?`, fx.inv.String()).Scan(&committed))
	s.InvCommitted = committed.Valid
	s.LeaseActive = countRows(t, fx.db, `SELECT COUNT(*) FROM workspace_leases WHERE resource_key = ? AND active = 1`, fx.key) > 0
	return s
}

func (fx stepFixture) busySlots(t *testing.T) int {
	return countRows(t, fx.db, `SELECT COUNT(*) FROM runner_jobs WHERE runner_id = 'builtin' AND state IN ('offered','running')`)
}

func TestStepCommitRepo_CommitIsOneTransactionAndReplayable(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	base := fx.baseline(t)
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	res := fx.claim(t, j, f)
	ref := fx.candidate(t, 2, 'a', base, f, res)
	receipt := workspaceReceipt(f, ref, res)
	c := fx.advance("B", receipt)

	require.NoError(t, fx.repo.CommitStep(ctx, c))
	s := fx.snapshot(t)
	assert.Equal(t, 1, s.Execs)
	assert.Equal(t, 1, s.StepCommits)
	assert.Equal(t, 1, s.Ops)
	assert.Zero(t, s.Suspensions)
	assert.Equal(t, "completed", s.Jobs[j.ID])
	assert.Equal(t, int64(2), s.Pointer, "the exact candidate is now the committed recovery base")
	assert.False(t, s.Dirty)
	assert.Equal(t, "committed", s.Candidates[2])
	assert.Equal(t, "B", s.Current)
	assert.True(t, s.InvCommitted)
	assert.False(t, s.LeaseActive, "the exact claim is released")
	assert.Contains(t, s.Vars, `"A"`)
	assert.Zero(t, fx.busySlots(t), "the job's slot is released only by this commit")
	got, err := fx.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, ref.String(), got.CheckpointRef)
	attempts, err := fx.ledger.Attempts(ctx, j.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	assert.Equal(t, "completed", attempts[0].Outcome)
	assert.NotNil(t, attempts[0].EndedAt)

	replay, ok, err := fx.repo.CommittedResult(ctx, fx.inv)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, engine.StepCommitAdvance, replay.Mode)
	assert.Equal(t, fx.run, replay.RunID)
	assert.Equal(t, "A", replay.NodeID)
	assert.Equal(t, "B", replay.NextNodeID)
	require.NotNil(t, replay.Execution, "replay returns the full execution, not an identity stub")
	assert.Equal(t, c.Execution.ID, replay.Execution.ID)
	assert.Equal(t, instance.NodeStatusSucceeded, replay.Execution.Status)
	assert.Equal(t, map[string]any{"answer": "42"}, replay.Execution.Output)
	assert.Equal(t, map[string]any{"answer": "42"}, replay.Variables["A"])
	assert.Equal(t, []engine.StepCompletion{receipt}, replay.Completions)

	require.NoError(t, fx.repo.CommitStep(ctx, c), "an identical replay is idempotent")
	assert.Equal(t, s, fx.snapshot(t), "the replay writes nothing")
	conflict := c
	conflict.NextNodeID = "C"
	require.ErrorIs(t, fx.repo.CommitStep(ctx, conflict), engine.ErrInvalidStepCompletion)
	assert.Equal(t, s, fx.snapshot(t))

	_, ok, err = fx.repo.CommittedResult(ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestStepCommitRepo_MissingWorkspaceCandidateFailsClosed(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	base := fx.baseline(t)
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	res := fx.claim(t, j, f)
	fx.candidate(t, 2, 'a', base, f, res)
	before := fx.snapshot(t)

	ws := func(ref runner.CheckpointRef) engine.StepCompletion { return workspaceReceipt(f, ref, res) }
	for name, comps := range map[string][]engine.StepCompletion{
		"a workspace job without a workspace receipt (built-in is not exempt)": {{JobFence: jobRef(f)}},
		"a ref that was never prepared":                                        {ws(runner.CheckpointRef{RunID: fx.run, Version: 3, ManifestHash: stepHash('c')})},
		"a ref with another manifest hash":                                     {ws(runner.CheckpointRef{RunID: fx.run, Version: 2, ManifestHash: stepHash('b')})},
		"the committed baseline instead of a prepared candidate":               {ws(base)},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, fx.repo.CommitStep(ctx, fx.advance("B", comps...)), engine.ErrInvalidStepCompletion)
			assert.Equal(t, before, fx.snapshot(t), "nothing is promoted, completed, released or recorded")
		})
	}
	_, ok, err := fx.repo.CommittedResult(ctx, fx.inv)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestStepCommitRepo_ReplayCannotOmitConsumedReceipt(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	_, fence := fx.job(t, "node", runner.AffinityBuiltIn)
	c := fx.advance("B", engine.StepCompletion{JobFence: jobRef(fence)})
	require.NoError(t, fx.repo.CommitStep(ctx, c))
	before := fx.snapshot(t)
	c.Completions = nil
	require.ErrorIs(t, fx.repo.CommitStep(ctx, c), engine.ErrInvalidStepCompletion)
	assert.Equal(t, before, fx.snapshot(t))
}

func TestStepCommitRepo_BuiltInCandidateIsLinked(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	base := fx.baseline(t)
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	require.Equal(t, runner.BuiltInRunnerID, j.RunnerID)
	res := fx.claim(t, j, f)
	ref := fx.candidate(t, 2, 'a', base, f, res)

	require.NoError(t, fx.repo.CommitStep(ctx, fx.advance("B", workspaceReceipt(f, ref, res))))

	var opRef, opRunner, opKey, opToken string
	var opGen int64
	require.NoError(t, fx.db.QueryRow(`SELECT checkpoint_ref, job_runner_id, resource_key, resource_token, job_generation
		FROM step_commit_operations WHERE invocation_id = ? AND job_id = ?`, fx.inv.String(), j.ID.String()).
		Scan(&opRef, &opRunner, &opKey, &opToken, &opGen))
	assert.Equal(t, ref.String(), opRef, "the receipt's exact ref is linked, never a newest-row lookup")
	assert.Equal(t, string(runner.BuiltInRunnerID), opRunner)
	assert.Equal(t, fx.key, opKey)
	assert.Equal(t, res.Token.String(), opToken)
	assert.Equal(t, f.Generation, opGen)
	var candJob string
	require.NoError(t, fx.db.QueryRow(`SELECT job_id FROM workspace_checkpoints WHERE run_id = ? AND version = 2`, fx.run.String()).Scan(&candJob))
	assert.Equal(t, j.ID.String(), candJob)
}

func TestStepCommitRepo_StaleResourceRollsBackAll(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	base := fx.baseline(t)
	_, pf := fx.job(t, "node/child-input", runner.AffinityBuiltIn)
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	res := fx.claim(t, j, f)
	ref := fx.candidate(t, 2, 'a', base, f, res)
	before := fx.snapshot(t)

	staleToken := res
	staleToken.Token = uuid.New()
	err := fx.repo.CommitStep(ctx, fx.advance("B", engine.StepCompletion{JobFence: jobRef(pf)}, workspaceReceipt(f, ref, staleToken)))
	require.ErrorIs(t, err, runner.ErrResourceFenced)
	assert.Equal(t, before, fx.snapshot(t), "the child-input job completed earlier in the transaction is rolled back too")

	staleJob := jobRef(f)
	staleJob.Generation++
	err = fx.repo.CommitStep(ctx, fx.advance("B", engine.StepCompletion{JobFence: staleJob, Workspace: &engine.WorkspaceCompletion{Ref: ref.String(), Resource: res}}))
	require.ErrorIs(t, err, runner.ErrFenced, "a stale result after failover is rejected")
	assert.Equal(t, before, fx.snapshot(t))
}

func TestStepCommitRepo_AllSuboperationsCommitAtomically(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	base := fx.baseline(t)
	pj, pf := fx.job(t, "node/child-input", runner.AffinityBuiltIn)
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	res := fx.claim(t, j, f)
	ref := fx.candidate(t, 2, 'a', base, f, res)
	prep := engine.StepCompletion{JobFence: jobRef(pf)}
	agent := workspaceReceipt(f, ref, res)

	require.NoError(t, fx.repo.CommitStep(ctx, fx.advance("B", prep, agent)))
	s := fx.snapshot(t)
	assert.Equal(t, "completed", s.Jobs[pj.ID])
	assert.Equal(t, "completed", s.Jobs[j.ID])
	assert.Equal(t, 2, s.Ops, "every suboperation's receipt is persisted, not just the last")
	assert.Zero(t, fx.busySlots(t))
	replay, ok, err := fx.repo.CommittedResult(ctx, fx.inv)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []engine.StepCompletion{prep, agent}, replay.Completions)
}

func TestStepCommitRepo_ExecutionRunMismatchRejected(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	_, otherInvJob := fx.jobFor(t, uuid.New(), "node", runner.AffinityBuiltIn)
	before := fx.snapshot(t)

	otherRun := fx.advance("B")
	otherRun.Execution.InstanceID = uuid.New()
	otherNode := fx.advance("B")
	otherNode.Execution.NodeID = "Z"
	wrongInvocationNode := fx.advance("C")
	wrongInvocationNode.NodeID, wrongInvocationNode.Execution.NodeID = "B", "B" // fx.inv is node A's visit
	for name, c := range map[string]engine.StepCommit{
		"execution of another run":            otherRun,
		"execution of another node":           otherNode,
		"invocation of another node":          wrongInvocationNode,
		"receipt of another invocation's job": fx.advance("B", engine.StepCompletion{JobFence: jobRef(otherInvJob)}),
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, fx.repo.CommitStep(ctx, c), engine.ErrInvalidStepCompletion)
			assert.Equal(t, before, fx.snapshot(t))
		})
	}
}

func TestStepCommitRepo_FailureAfterPromotionRollsBackEverything(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	base := fx.baseline(t)
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	res := fx.claim(t, j, f)
	ref := fx.candidate(t, 2, 'a', base, f, res)
	_, err := fx.db.Exec(`CREATE TRIGGER refuse_step_commit BEFORE INSERT ON step_commits BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	before := fx.snapshot(t)

	err = fx.repo.CommitStep(ctx, fx.advance("B", workspaceReceipt(f, ref, res)))
	require.ErrorContains(t, err, "injected")
	assert.Equal(t, before, fx.snapshot(t), "candidate, pointer, job, claim, execution, variables and invocation all roll back")
	assert.Equal(t, "prepared", before.Candidates[2], "the artifact stays an unreferenced candidate, never current progress")
}

func TestStepCommitRepo_TerminalCommitLeavesInvocationOpen(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	require.NoError(t, fx.repo.CommitStep(ctx, fx.advance("")))

	s := fx.snapshot(t)
	assert.False(t, s.InvCommitted, "a terminal step stays open until the run is durably completed")
	assert.Equal(t, "A", s.Current)
	assert.Equal(t, 1, s.StepCommits)
	replay, ok, err := fx.repo.CommittedResult(ctx, fx.inv)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "", replay.NextNodeID)
	assert.Empty(t, replay.Completions)
}

func TestStepCommitRepo_SuspendCommitCompletesJobsKeepsInvocationOpen(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	pj, pf := fx.job(t, "node/child-input", runner.AffinityBuiltIn)
	child := uuid.New()
	prep := engine.StepCompletion{JobFence: jobRef(pf)}
	suspend := engine.StepCommit{Mode: engine.StepCommitSuspend, RunID: fx.run, InvocationID: fx.inv, NodeID: "A",
		Completions: []engine.StepCompletion{prep},
		Suspension:  &engine.StepSuspension{Kind: "spawn_workflow", ChildLinkID: &child, State: map[string]any{"childInstanceId": child.String()}}}

	require.NoError(t, fx.repo.CommitStep(ctx, suspend))
	s := fx.snapshot(t)
	assert.Equal(t, "completed", s.Jobs[pj.ID])
	assert.Zero(t, fx.busySlots(t), "the waiting parent holds no runner slot")
	assert.Equal(t, 1, s.Suspensions)
	assert.Zero(t, s.StepCommits, "a suspension writes no step commit")
	assert.Zero(t, s.Execs, "a suspension invents no successful execution")
	assert.False(t, s.InvCommitted)
	assert.Equal(t, "A", s.Current)
	var kind, childLink, state string
	require.NoError(t, fx.db.QueryRow(`SELECT kind, child_link_id, state FROM step_suspensions WHERE invocation_id = ? AND operation_id = 'node'`,
		fx.inv.String()).Scan(&kind, &childLink, &state))
	assert.Equal(t, "spawn_workflow", kind)
	assert.Equal(t, child.String(), childLink)
	assert.Contains(t, state, child.String())

	require.NoError(t, fx.repo.CommitStep(ctx, suspend), "an identical suspension replay is idempotent")
	assert.Equal(t, s, fx.snapshot(t))
	other := uuid.New()
	conflicting := suspend
	conflicting.Suspension = &engine.StepSuspension{Kind: "spawn_workflow", ChildLinkID: &other, State: map[string]any{}}
	require.ErrorIs(t, fx.repo.CommitStep(ctx, conflicting), engine.ErrInvalidStepCompletion, "a second child link is never recorded")

	replay, ok, err := fx.repo.CommittedResult(ctx, fx.inv)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, engine.StepCommitSuspend, replay.Mode)
	require.NotNil(t, replay.Suspension)
	assert.Equal(t, child, *replay.Suspension.ChildLinkID)
	assert.Equal(t, []engine.StepCompletion{prep}, replay.Completions)

	wrongGen := engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: pf.JobID, RunnerID: string(pf.RunnerID), Generation: pf.Generation + 1}}
	require.ErrorIs(t, fx.repo.CommitStep(ctx, fx.advance("B", wrongGen)), engine.ErrInvalidStepCompletion)

	// The child finished: the final advance extends the same invocation; the
	// consumed receipt is accepted once and never completed again.
	require.NoError(t, fx.repo.CommitStep(ctx, fx.advance("B", prep)))
	s = fx.snapshot(t)
	assert.Equal(t, 1, s.Ops)
	assert.Equal(t, 1, s.StepCommits)
	assert.True(t, s.InvCommitted)
	replay, ok, err = fx.repo.CommittedResult(ctx, fx.inv)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, engine.StepCommitAdvance, replay.Mode, "an advance commit supersedes the suspension for replay")
}

func nonPortableReceipt(f runner.Fence, res engine.ResourceFenceRef) engine.StepCompletion {
	return engine.StepCompletion{JobFence: jobRef(f), Workspace: &engine.WorkspaceCompletion{NonPortable: true, Resource: res}}
}

func TestStepCommitRepo_NonPortableReceiptPromotesLocalCheckpoint(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	fx.baseline(t)
	require.NoError(t, fx.homes.SetNonPortable(ctx, fx.run, runner.NonPortablePromisorReason))
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	res := fx.claim(t, j, f)
	receipt := nonPortableReceipt(f, res)
	local := runner.LocalCheckpointRef{RunID: fx.run, Version: 1, ManifestHash: stepHash('a')}.String()
	receipt.Workspace.LocalRef = local
	c := fx.advance("B", receipt)

	require.NoError(t, fx.repo.CommitStep(ctx, c))
	s := fx.snapshot(t)
	assert.Equal(t, "completed", s.Jobs[j.ID], "the job completes")
	assert.Zero(t, fx.busySlots(t))
	assert.False(t, s.LeaseActive, "the exact claim is released")
	assert.Equal(t, "B", s.Current, "result, variables and continuation commit")
	assert.Equal(t, 1, s.StepCommits)
	assert.Equal(t, int64(1), s.Pointer, "no candidate is promoted; checkpoint_version is unchanged")
	assert.Equal(t, map[int64]string{1: "committed"}, s.Candidates)
	assert.False(t, s.Dirty, "the local checkpoint matches the working copy")
	home, err := fx.homes.GetHome(ctx, fx.run)
	require.NoError(t, err)
	assert.Equal(t, local, home.LocalCheckpoint)
	assert.Equal(t, runner.NonPortablePromisorReason, s.Reason, "the marker stays until a portable candidate is promoted")
	var ref sql.NullString
	require.NoError(t, fx.db.QueryRow(`SELECT checkpoint_ref FROM step_commit_operations WHERE invocation_id = ? AND job_id = ?`,
		fx.inv.String(), j.ID.String()).Scan(&ref))
	assert.True(t, ref.Valid)
	assert.Equal(t, local, ref.String)

	replay, ok, err := fx.repo.CommittedResult(ctx, fx.inv)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []engine.StepCompletion{receipt}, replay.Completions)
	require.NoError(t, fx.repo.CommitStep(ctx, c), "an identical replay is idempotent")
	assert.Equal(t, s, fx.snapshot(t))
}

func TestStepCommitRepo_NonPortableReceiptRejectedWhenHomePortable(t *testing.T) {
	ctx := context.Background()
	for name, arrange := range map[string]func(t *testing.T, fx stepFixture){
		"a portable home": func(*testing.T, stepFixture) {},
		"a non-portable home that is not the built-in server home": func(t *testing.T, fx stepFixture) {
			require.NoError(t, fx.homes.SetNonPortable(ctx, fx.run, runner.NonPortablePromisorReason))
			_, err := fx.db.Exec(`UPDATE workspace_homes SET runner_id = 'mac-1' WHERE run_id = ?`, fx.run.String())
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newStepFixture(t, setupTestDB(t).DB)
			fx.baseline(t)
			arrange(t, fx)
			j, f := fx.job(t, "node", runner.AffinityWorkspace)
			res := fx.claim(t, j, f)
			before := fx.snapshot(t)

			err := fx.repo.CommitStep(ctx, fx.advance("B", nonPortableReceipt(f, res)))
			require.ErrorIs(t, err, engine.ErrInvalidStepCompletion)
			assert.Equal(t, before, fx.snapshot(t), "the job stays running and the claim stays held")
		})
	}
}

func TestStepCommitRepo_CandidatePromotionClearsNonPortable(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	base := fx.baseline(t)
	require.NoError(t, fx.homes.SetNonPortable(ctx, fx.run, runner.NonPortablePromisorReason))
	j, f := fx.job(t, "node", runner.AffinityWorkspace)
	res := fx.claim(t, j, f)
	_, err := fx.db.Exec(`UPDATE workspace_homes SET local_checkpoint = ? WHERE run_id = ?`, runner.LocalCheckpointRef{RunID: fx.run, Version: 1, ManifestHash: stepHash('b')}.String(), fx.run.String())
	require.NoError(t, err)
	ref := fx.candidate(t, 2, 'a', base, f, res) // all promisor objects present again

	require.NoError(t, fx.repo.CommitStep(ctx, fx.advance("B", workspaceReceipt(f, ref, res))))
	s := fx.snapshot(t)
	assert.Empty(t, s.Reason, "promoting a normal candidate clears the non-portable marker in the same transaction")
	home, err := fx.homes.GetHome(ctx, fx.run)
	require.NoError(t, err)
	assert.Empty(t, home.LocalCheckpoint)
	assert.Equal(t, int64(2), s.Pointer)
	assert.False(t, s.Dirty)
}
