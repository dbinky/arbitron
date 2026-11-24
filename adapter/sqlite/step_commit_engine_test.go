package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stepNoopBus struct{}

func (stepNoopBus) Publish(uuid.UUID, engine.Event) {}
func (stepNoopBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}
func (stepNoopBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) {
	ch := make(chan engine.ScopedEvent)
	close(ch)
	return ch, func() {}
}

type stepNoopQueue struct{}

func (stepNoopQueue) Enqueue(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }
func (stepNoopQueue) NotifyCapacityFreed(context.Context)                         {}

// countingExec records which nodes ran (the provider/process call count) and
// lets a test script one node.
type countingExec struct {
	mu    sync.Mutex
	calls []string
	fns   map[string]func(ctx context.Context) (*engine.NodeResult, error)
}

func newCountingExec() *countingExec {
	return &countingExec{fns: map[string]func(ctx context.Context) (*engine.NodeResult, error){}}
}

func (c *countingExec) on(node string, fn func(ctx context.Context) (*engine.NodeResult, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fns[node] = fn
}

func (c *countingExec) Execute(ctx context.Context, node workflow.Node, _ *variable.VariableStore, _ string) (*engine.NodeResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, node.ID)
	fn := c.fns[node.ID]
	c.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return &engine.NodeResult{Output: map[string]any{"ran": node.ID}}, nil
}

func (c *countingExec) ran() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// newStepEngine is the server's engine over real SQLite repositories.
func newStepEngine(db *sql.DB, executor engine.NodeExecutor, committer engine.StepCommitter) *engine.Engine {
	executors := map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: executor}
	eng := engine.NewEngine(nil, NewInstanceRepo(db), NewNodeExecutionRepo(db), NewVariableSnapshotRepo(db), NewUserRepo(db),
		executors, stepNoopBus{}, stepNoopQueue{}, nil, "")
	eng.SetInvocationStore(NewInvocationRepo(db))
	eng.SetStepCommitter(committer)
	return eng
}

// seedLinearRun creates a running A -> B -> C script run.
func seedLinearRun(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	raw, err := json.Marshal(workflow.Workflow{Name: "linear",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{{Source: "A", Target: "B"}, {Source: "B", Target: "C"}}})
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	owner, err := NewUserRepo(db).GetDefaultUser(context.Background())
	require.NoError(t, err)
	inst := newTestInstance(t, owner)
	inst.WorkflowName, inst.WorkflowSnapshot = "linear", snapshot
	require.NoError(t, NewInstanceRepo(db).Create(context.Background(), inst))
	return inst.ID
}

func waitForRun(t *testing.T, db *sql.DB, id uuid.UUID, states ...instance.State) *instance.Instance {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		inst, err := NewInstanceRepo(db).Get(context.Background(), id)
		if err == nil {
			for _, s := range states {
				if inst.State == s {
					return inst
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached %v (err %v)", id, states, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var errSimulatedCrash = errors.New("simulated process death")

// crashingCommitter stands in for a process that dies right before (before =
// true) or right after a node's durable commit: it never returns until test
// cleanup, so the first engine never advances in memory.
type crashingCommitter struct {
	inner   engine.StepCommitter
	node    string
	before  bool
	crashed chan struct{}
	release chan struct{}
	once    sync.Once
}

func crashAt(t *testing.T, inner engine.StepCommitter, node string, before bool) *crashingCommitter {
	c := &crashingCommitter{inner: inner, node: node, before: before, crashed: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(c.release) })
	return c
}

func (c *crashingCommitter) CommitStep(ctx context.Context, s engine.StepCommit) error {
	if s.NodeID != c.node {
		return c.inner.CommitStep(ctx, s)
	}
	if !c.before {
		if err := c.inner.CommitStep(ctx, s); err != nil {
			return err
		}
	}
	c.once.Do(func() { close(c.crashed) })
	<-c.release
	return errSimulatedCrash
}

func (c *crashingCommitter) CommittedResult(ctx context.Context, inv uuid.UUID) (*engine.StepCommit, bool, error) {
	return c.inner.CommittedResult(ctx, inv)
}

func (c *crashingCommitter) wait(t *testing.T) {
	t.Helper()
	select {
	case <-c.crashed:
	case <-time.After(10 * time.Second):
		t.Fatalf("node %s never reached its commit", c.node)
	}
}

// prepareWorkspaceStep does what P3 T6's built-in workspace path does for the
// executing operation: reserve its job, claim the run resource, prepare the
// next candidate on the committed base, and return the exact receipt.
func prepareWorkspaceStep(ctx context.Context, fx stepFixture, base runner.CheckpointRef) (engine.StepCompletion, error) {
	id, ok := engine.StepIdentityFromContext(ctx)
	if !ok {
		return engine.StepCompletion{}, errors.New("no step identity on the executor context")
	}
	key := runner.JobKey{RunID: id.RunID, InvocationID: id.InvocationID, OperationID: id.OperationID, Attempt: id.Attempt}
	j, err := fx.ledger.EnsureIntent(ctx, key, id.NodeID, runner.ExecRequirements{
		Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess, Affinity: runner.AffinityBuiltIn, RunID: id.RunID})
	if err != nil {
		return engine.StepCompletion{}, err
	}
	if j, err = fx.ledger.Reserve(ctx, j.ID, runner.Decision{Placed: true, Runner: runner.BuiltInRunnerID, Reason: "test"}, 8, time.Now().Add(time.Hour)); err != nil {
		return engine.StepCompletion{}, err
	}
	f := runner.Fence{JobID: j.ID, RunnerID: j.RunnerID, Generation: j.Generation}
	if err := fx.ledger.MarkRunning(ctx, f); err != nil {
		return engine.StepCompletion{}, err
	}
	res, err := fx.leases.ClaimJob(ctx, fx.key, runner.JobHolder(j), f)
	if err != nil {
		return engine.StepCompletion{}, err
	}
	ref := runner.CheckpointRef{RunID: fx.run, Version: base.Version + 1, ManifestHash: stepHash('b')}
	if err := NewCheckpointCatalogRepo(fx.db).Publish(ctx, runner.CheckpointCandidate{
		Ref: ref, Base: base, Job: &f, Resource: res, ArchivePath: "/ckpt/next", SizeBytes: 10}); err != nil {
		return engine.StepCompletion{}, err
	}
	return workspaceReceipt(f, ref, res), nil
}

func recordAndSucceed(ctx context.Context, rc engine.StepCompletion) (*engine.NodeResult, error) {
	sink, ok := engine.StepCompletionSinkFromContext(ctx)
	if !ok {
		return nil, errors.New("no completion sink on the attempt context")
	}
	if err := sink.Record(rc); err != nil {
		return nil, err
	}
	return &engine.NodeResult{Output: map[string]any{"wrote": true}}, nil
}

func homePointer(t *testing.T, db *sql.DB, run uuid.UUID) int64 {
	t.Helper()
	var v int64
	require.NoError(t, db.QueryRow(`SELECT checkpoint_version FROM workspace_homes WHERE run_id = ?`, run.String()).Scan(&v))
	return v
}

func candidateState(t *testing.T, db *sql.DB, run uuid.UUID, version int64) string {
	t.Helper()
	var s string
	require.NoError(t, db.QueryRow(`SELECT state FROM workspace_checkpoints WHERE run_id = ? AND version = ?`, run.String(), version).Scan(&s))
	return s
}

func TestEngine_CrashBeforeCommitRestoresPriorCheckpoint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "arbitron.db")
	db1 := openStepDB(t, path)
	run := seedLinearRun(t, db1)
	fx1 := stepFixtureFor(t, db1, run, uuid.Nil)
	base := fx1.baseline(t)

	var mu sync.Mutex
	var receipt engine.StepCompletion
	exec1 := newCountingExec()
	exec1.on("B", func(ctx context.Context) (*engine.NodeResult, error) {
		rc, err := prepareWorkspaceStep(ctx, fx1, base)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		receipt = rc
		mu.Unlock()
		return recordAndSucceed(ctx, rc)
	})
	crash := crashAt(t, NewStepCommitRepo(db1), "B", true) // artifact prepared, DB commit never happens
	require.NoError(t, newStepEngine(db1, exec1, crash).StartInstance(ctx, run))
	crash.wait(t)
	require.NoError(t, db1.Close())

	db2 := openStepDB(t, path)
	t.Cleanup(func() { _ = db2.Close() })
	assert.Equal(t, int64(1), homePointer(t, db2, run), "the committed recovery base is still the prior checkpoint")
	assert.Equal(t, "prepared", candidateState(t, db2, run, 2), "the candidate never became progress")

	mu.Lock()
	adopted := receipt
	mu.Unlock()
	exec2 := newCountingExec()
	// The adopted live job (P3 T10) re-reports its exact receipt on re-entry.
	exec2.on("B", func(ctx context.Context) (*engine.NodeResult, error) { return recordAndSucceed(ctx, adopted) })
	require.NoError(t, newStepEngine(db2, exec2, NewStepCommitRepo(db2)).StartInstance(ctx, run))
	inst := waitForRun(t, db2, run, instance.StateCompleted, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, []string{"A", "B"}, exec1.ran())
	assert.Equal(t, []string{"B", "C"}, exec2.ran(), "only the uncommitted operation repeats")
	assert.Equal(t, int64(2), homePointer(t, db2, run))
	assert.Equal(t, "committed", candidateState(t, db2, run, 2))
}

func TestEngine_CrashAfterCommitReplaysWithoutProviderCall(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "arbitron.db")
	db1 := openStepDB(t, path)
	run := seedLinearRun(t, db1)
	exec1 := newCountingExec()
	crash := crashAt(t, NewStepCommitRepo(db1), "B", false) // committed, never advanced in memory
	require.NoError(t, newStepEngine(db1, exec1, crash).StartInstance(ctx, run))
	crash.wait(t)
	require.NoError(t, db1.Close())

	db2 := openStepDB(t, path)
	t.Cleanup(func() { _ = db2.Close() })
	exec2 := newCountingExec()
	require.NoError(t, newStepEngine(db2, exec2, NewStepCommitRepo(db2)).StartInstance(ctx, run))
	inst := waitForRun(t, db2, run, instance.StateCompleted, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, []string{"A", "B"}, exec1.ran())
	assert.Equal(t, []string{"C"}, exec2.ran(), "the committed step B never runs again")
	assert.Contains(t, inst.Variables, "B", "B's durable variables are consumed")
	assert.Equal(t, 3, countRows(t, db2, `SELECT COUNT(*) FROM step_commits WHERE run_id = ?`, run.String()))
}

func TestEngine_StepCommit_CrashAfterTerminalCommitReplaysWithoutRepeatingStep(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "arbitron.db")
	db1 := openStepDB(t, path)
	run := seedLinearRun(t, db1)
	exec1 := newCountingExec()
	crash := crashAt(t, NewStepCommitRepo(db1), "C", false) // terminal result durable, SetCompleted never ran
	require.NoError(t, newStepEngine(db1, exec1, crash).StartInstance(ctx, run))
	crash.wait(t)
	require.NoError(t, db1.Close())

	db2 := openStepDB(t, path)
	t.Cleanup(func() { _ = db2.Close() })
	assert.Equal(t, 1, countRows(t, db2, `SELECT COUNT(*) FROM node_invocations WHERE run_id = ? AND committed_at IS NULL`, run.String()),
		"the terminal invocation is still open")
	exec2 := newCountingExec()
	require.NoError(t, newStepEngine(db2, exec2, NewStepCommitRepo(db2)).StartInstance(ctx, run))
	inst := waitForRun(t, db2, run, instance.StateCompleted, instance.StateFailed)

	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Empty(t, exec2.ran(), "the committed terminal step is replayed, not executed")
	assert.Contains(t, inst.Variables, "C", "replay keeps the committed output")
	assert.Equal(t, 3, countRows(t, db2, `SELECT COUNT(*) FROM step_commits WHERE run_id = ?`, run.String()), "replay writes no second commit")
	assert.Zero(t, countRows(t, db2, `SELECT COUNT(*) FROM node_invocations WHERE run_id = ? AND committed_at IS NULL`, run.String()),
		"the terminal invocation closes once the run is durably complete")
}

// receiptProcessRunner is a routed process whose transport succeeds (exit 0)
// after preparing B's workspace candidate, but whose stdout fails the REAL
// script executor's JSON validation.
type receiptProcessRunner struct {
	fx    stepFixture
	base  runner.CheckpointRef
	mu    sync.Mutex
	nodes []string
}

func (p *receiptProcessRunner) RunProcess(ctx context.Context, _ engine.ProcessSpec) (engine.ProcessResult, error) {
	id, _ := engine.StepIdentityFromContext(ctx)
	p.mu.Lock()
	p.nodes = append(p.nodes, id.NodeID)
	p.mu.Unlock()
	if id.NodeID != "B" {
		return engine.ProcessResult{Stdout: `{"ok":true}`}, nil
	}
	rc, err := prepareWorkspaceStep(ctx, p.fx, p.base)
	if err != nil {
		return engine.ProcessResult{}, err
	}
	if _, err := recordAndSucceed(ctx, rc); err != nil {
		return engine.ProcessResult{}, err
	}
	return engine.ProcessResult{Stdout: "transport ok, but not JSON", ExitCode: 0}, nil
}

func (p *receiptProcessRunner) ran() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.nodes...)
}

// stepRecordingReleaser stands in for P3 T6's StepDispatcher.ReleaseCompletions.
type stepRecordingReleaser struct {
	mu    sync.Mutex
	calls [][]engine.StepCompletion
	cause error
}

func (r *stepRecordingReleaser) ReleaseCompletions(_ context.Context, cs []engine.StepCompletion, cause error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls, r.cause = append(r.calls, cs), cause
	return nil
}

func (r *stepRecordingReleaser) snapshot() ([][]engine.StepCompletion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]engine.StepCompletion(nil), r.calls...), r.cause
}

func mustParseVersion(t *testing.T, ref string) int64 {
	t.Helper()
	parsed, err := runner.ParseCheckpointRef(ref)
	require.NoError(t, err)
	return parsed.Version
}

type stubScriptFiles struct{}

func (stubScriptFiles) ReadFile(uuid.UUID, string, string) ([]byte, error) {
	return []byte("#!/bin/sh\n"), nil
}
func (stubScriptFiles) ResolvePath(_ uuid.UUID, _ string, rel string) (string, error) {
	return filepath.Join("/scripts", rel), nil
}
func (stubScriptFiles) FileExists(uuid.UUID, string, string) bool { return true }

func TestEngine_OutputValidationFailureKeepsPriorCheckpoint(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	run := seedLinearRun(t, db)
	fx := stepFixtureFor(t, db, run, uuid.Nil)
	base := fx.baseline(t)
	pr := &receiptProcessRunner{fx: fx, base: base}
	eng := newStepEngine(db, process.NewScriptExecutor(stubScriptFiles{}, nil, pr), NewStepCommitRepo(db))
	rel := &stepRecordingReleaser{}
	eng.SetStepCompletionReleaser(rel)

	require.NoError(t, eng.StartInstance(ctx, run))
	inst := waitForRun(t, db, run, instance.StateFailed, instance.StateCompleted)

	require.Equal(t, instance.StateFailed, inst.State, "zero-exit output that fails validation has not completed the step")
	assert.Equal(t, "B", *inst.FailedNodeID)
	assert.Equal(t, []string{"A", "B"}, pr.ran(), "the next node never runs")
	assert.Equal(t, int64(1), homePointer(t, db, run), "no new committed checkpoint")
	assert.Equal(t, "prepared", candidateState(t, db, run, 2))
	assert.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM node_executions WHERE instance_id = ? AND node_id = 'B' AND status = 'succeeded'`, run.String()),
		"no successful execution row")
	assert.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM step_commits WHERE run_id = ?`, run.String()), "only A committed")
	assert.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM runner_jobs WHERE run_id = ? AND state = 'completed'`, run.String()),
		"transport success did not complete the job")
	calls, cause := rel.snapshot()
	require.Len(t, calls, 1, "the failed attempt's receipts are handed to the releaser (P3 T6 FinishFailure)")
	require.Len(t, calls[0], 1)
	require.NotNil(t, calls[0][0].Workspace)
	assert.Equal(t, int64(2), mustParseVersion(t, calls[0][0].Workspace.Ref), "exactly B's prepared candidate")
	assert.ErrorContains(t, cause, "not valid JSON")
}
