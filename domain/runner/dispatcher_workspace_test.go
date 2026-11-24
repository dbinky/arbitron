package runner_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsStepRunner executes one workspace process step. The built-in one edits the
// server working directory; a remote one plays the runner agent: it pulls the
// pinned base, edits its own copy and pushes through StageDelta/AcceptPush,
// the same path P3 T7's handler uses.
type wsStepRunner struct {
	f     *wsFixture
	id    runner.RunnerID
	mu    sync.Mutex
	calls int
	edit  map[string]string
	fail  error
}

func (r *wsStepRunner) set(edit map[string]string, fail error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edit, r.fail = edit, fail
}

func (r *wsStepRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *wsStepRunner) Run(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	r.mu.Lock()
	r.calls++
	edit, fail := r.edit, r.fail
	r.mu.Unlock()
	if a.Fence.RunnerID != r.id {
		return runner.StepOutput{}, fmt.Errorf("assignment for %s delivered to %s", a.Fence.RunnerID, r.id)
	}
	if r.id != runner.BuiltInRunnerID && fail != nil {
		return runner.StepOutput{}, fail // a remote fails before touching anything (pull/reject)
	}
	if r.id == runner.BuiltInRunnerID {
		for name, body := range edit {
			if err := os.WriteFile(filepath.Join(job.Process.WorkDir, name), []byte(body), 0o644); err != nil {
				return runner.StepOutput{}, err
			}
		}
		if fail != nil {
			return runner.StepOutput{}, fail // the step edited files and then failed
		}
	} else {
		plan, err := r.f.sync.PreparePull(ctx, a, a.Job.Key.RunID)
		if err != nil {
			return runner.StepOutput{}, err
		}
		staged, err := r.f.stage(ctx, runner.WorkspaceAttempt{RunID: plan.RunID, Base: plan.Ref, Resource: plan.Resource}, edit)
		if err != nil {
			return runner.StepOutput{}, err
		}
		if _, err := r.f.sync.AcceptPush(ctx, a.Fence, plan.RunID, staged); err != nil {
			return runner.StepOutput{}, err
		}
	}
	return runner.StepOutput{Process: &engine.ProcessResult{Stdout: `{"ok":true}`}}, nil
}

// recWorkspace is a recording StepWorkspace: the dispatcher-side contract only.
type recWorkspace struct {
	mu     sync.Mutex
	reqs   []runner.ExecRequirements
	begun  []runner.WorkspaceAttempt
	failed int
	bytes  int64
}

func (w *recWorkspace) WorkspaceBytes(context.Context, uuid.UUID) int64 { return w.bytes }

func (w *recWorkspace) BeginAttempt(_ context.Context, a runner.Assignment, req runner.ExecRequirements, _ runner.Decision, job *runner.StepJob) (runner.WorkspaceAttempt, error) {
	dir := ""
	if job.Agent != nil {
		dir = job.Agent.WorkDir
	} else if job.Process != nil {
		dir = job.Process.WorkDir
	}
	att := runner.WorkspaceAttempt{RunID: req.RunID, WorkDir: dir, Runner: a.Fence.RunnerID,
		Resource: engine.ResourceFenceRef{Key: "run:" + req.RunID.String(), Owner: string(a.Fence.RunnerID), Generation: 1, Token: uuid.New()}}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reqs, w.begun = append(w.reqs, req), append(w.begun, att)
	return att, nil
}

func (w *recWorkspace) FinishSuccess(_ context.Context, _ runner.Assignment, att runner.WorkspaceAttempt) (engine.WorkspaceCompletion, error) {
	ref := runner.CheckpointRef{RunID: att.RunID, Version: 2, ManifestHash: strings.Repeat("a", 64)}
	return engine.WorkspaceCompletion{Ref: ref.String(), Resource: att.Resource}, nil
}

func (w *recWorkspace) FinishFailure(context.Context, runner.Assignment, runner.WorkspaceAttempt) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failed++
	return nil
}

func (w *recWorkspace) attempts() ([]runner.WorkspaceAttempt, []runner.ExecRequirements) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]runner.WorkspaceAttempt(nil), w.begun...), append([]runner.ExecRequirements(nil), w.reqs...)
}

type wdFixture struct {
	*wsFixture
	d       *runner.StepDispatcher
	runners map[runner.RunnerID]*wsStepRunner
}

func newWDFixture(t *testing.T, remotes ...runner.RunnerID) *wdFixture {
	t.Helper()
	f := &wdFixture{wsFixture: newWSFixture(t), runners: map[runner.RunnerID]*wsStepRunner{}}
	set := runner.StaticRunnerSet{}
	for _, id := range append([]runner.RunnerID{runner.BuiltInRunnerID}, remotes...) {
		if id != runner.BuiltInRunnerID {
			f.addRemote(t, id, 2)
		}
		r := &wsStepRunner{f: f.wsFixture, id: id}
		f.runners[id] = r
		set[id] = r
	}
	f.d = runner.NewStepDispatcher(runner.DispatcherDeps{
		Registry: f.pool.Registry, Ledger: f.pool.Ledger, Runners: set, Events: f.pool.Events, Status: f.status, Health: f.pool.Health,
		Telemetry: runner.EmptyTelemetry{}, Clock: f.pool.Clock.Now, TransientMaxAttempts: 2, BackoffMin: time.Millisecond, BackoffMax: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }, Workspace: f.sync,
	})
	t.Cleanup(f.d.Close)
	return f
}

type dispatched struct {
	out  runner.StepOutput
	sink *engine.StepCompletionSink
	inv  engine.Invocation
	err  error
}

// stepCtx opens the node invocation and the engine's per-attempt completion sink (P3 T5).
func (f *wdFixture) stepCtx(t *testing.T, ctx context.Context, withSink bool) (context.Context, *engine.StepCompletionSink, engine.Invocation) {
	t.Helper()
	inv, err := adaptsqlite.NewInvocationRepo(f.pool.DB).Begin(context.Background(), f.run, "edit")
	require.NoError(t, err)
	ctx = engine.ContextWithStepIdentity(ctx, engine.StepIdentity{RunID: f.run, NodeID: "edit", InvocationID: inv.ID,
		OperationID: "node", Kind: engine.OperationNode, Attempt: 1})
	var sink *engine.StepCompletionSink
	if withSink {
		sink = engine.NewStepCompletionSink()
		ctx = engine.ContextWithStepCompletionSink(ctx, sink)
	}
	return ctx, sink, inv
}

func (f *wdFixture) dispatch(t *testing.T) dispatched {
	t.Helper()
	ctx, sink, inv := f.stepCtx(t, context.Background(), true)
	out, err := f.d.Dispatch(ctx, f.req(), *f.job())
	return dispatched{out: out, sink: sink, inv: inv, err: err}
}

func (f *wdFixture) jobOf(t *testing.T, d dispatched) runner.Job {
	t.Helper()
	jobs, err := f.pool.Ledger.ListByRun(context.Background(), f.run)
	require.NoError(t, err)
	for _, j := range jobs {
		if j.Key.InvocationID == d.inv.ID {
			return j
		}
	}
	t.Fatalf("no job for invocation %s", d.inv.ID)
	return runner.Job{}
}

func (f *wdFixture) commit(ctx context.Context, d dispatched, instanceID uuid.UUID) error {
	exec := &instance.NodeExecution{ID: uuid.New(), InstanceID: instanceID, NodeID: "edit", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"ok": true}, StartedAt: f.pool.Clock.Now()}
	return adaptsqlite.NewStepCommitRepo(f.pool.DB).CommitStep(ctx, engine.StepCommit{RunID: f.run, InvocationID: d.inv.ID, NodeID: "edit",
		Execution: exec, Variables: map[string]any{}, Completions: d.sink.Completions(), Mode: engine.StepCommitAdvance})
}

// Happy path: transport success leaves the job running and Latest unchanged;
// only P3 T5's validated transaction completes it and advances the pointer.
func TestDispatch_ValidatedCommitIsOnlyCompletionBoundary(t *testing.T) {
	f := newWDFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners[builtin].set(map[string]string{"a.txt": "v2"}, nil)

	d := f.dispatch(t)
	require.NoError(t, d.err)
	require.NotNil(t, d.out.Completion)
	ws := d.out.Completion.Workspace
	require.NotNil(t, ws)
	assert.Equal(t, runner.JobRunning, f.jobOf(t, d).State, "transport success is not completion")
	assert.Equal(t, base, f.latest(t))
	rec, ok := f.active(t)
	require.True(t, ok)
	assert.Equal(t, ws.Resource, rec.Fence)

	require.NoError(t, f.commit(context.Background(), d, f.run))
	assert.Equal(t, runner.JobCompleted, f.jobOf(t, d).State)
	ref, err := runner.ParseCheckpointRef(ws.Ref)
	require.NoError(t, err)
	assert.Equal(t, ref, f.latest(t))
	assert.False(t, f.home(t).Dirty)
	_, ok = f.active(t)
	assert.False(t, ok)
	assert.Equal(t, "v2", readIn(t, f.restoreTo(t, f.latest(t)), "a.txt"))
}

// Success: a remote step's receipt is the exact pushed candidate with the exact job fence.
func TestDispatch_WorkspaceCompletionCarriesExactCandidate(t *testing.T) {
	f := newWDFixture(t, "mac-1")
	f.offline(t, builtin)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners["mac-1"].set(map[string]string{"a.txt": "from mac-1"}, nil)

	d := f.dispatch(t)
	require.NoError(t, d.err)
	job := f.jobOf(t, d)
	assert.Equal(t, engine.JobFenceRef{JobID: job.ID, RunnerID: "mac-1", Generation: job.Generation}, d.out.Completion.JobFence)
	assert.Equal(t, []engine.StepCompletion{*d.out.Completion}, d.sink.Completions())
	ref, err := runner.ParseCheckpointRef(d.out.Completion.Workspace.Ref)
	require.NoError(t, err)
	assert.Equal(t, base.Version+1, ref.Version)
	_, jobID, _, _, found := f.preparedRow(t, ref.Version)
	require.True(t, found)
	assert.Equal(t, job.ID.String(), jobID)
	assert.Equal(t, "from mac-1", readIn(t, f.restoreTo(t, ref), "a.txt"))
	assert.Equal(t, base, f.latest(t))
}

func TestDispatch_BuiltInCompletionIsNotEmpty(t *testing.T) {
	f := newWDFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners[builtin].set(map[string]string{"a.txt": "v2"}, nil)
	d := f.dispatch(t)
	require.NoError(t, d.err)
	ws := d.out.Completion.Workspace
	require.NotNil(t, ws)
	assert.NotEmpty(t, ws.Ref)
	_, err := runner.ParseCheckpointRef(ws.Ref)
	require.NoError(t, err)
	require.NoError(t, ws.Resource.Validate())
	assert.Equal(t, string(builtin), d.out.Completion.JobFence.RunnerID)
}

// Failure: a failed built-in step restores the committed base and frees everything.
func TestDispatch_WorkspaceStepFailureRestoresBuiltIn(t *testing.T) {
	f := newWDFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners[builtin].set(map[string]string{"a.txt": "broken", "extra.txt": "x"}, errors.New("exit status 2"))

	d := f.dispatch(t)
	require.Error(t, d.err)
	assert.Equal(t, "v1", f.read(t, "a.txt"))
	assert.NoFileExists(t, filepath.Join(f.dir(), "extra.txt"))
	assert.Equal(t, runner.JobFailed, f.jobOf(t, d).State)
	_, ok := f.active(t)
	assert.False(t, ok)
	assert.False(t, f.home(t).Dirty)
	assert.Empty(t, d.sink.Completions())
	assert.Equal(t, base, f.latest(t))
}

// Error: a commit failure persists nothing; releasing the uncommitted receipt
// restores the prior files and pointer and frees the slot and claim.
func TestDispatch_CommitFailurePreservesPriorFilesAndPointer(t *testing.T) {
	f := newWDFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners[builtin].set(map[string]string{"a.txt": "v2"}, nil)
	d := f.dispatch(t)
	require.NoError(t, d.err)
	ctx := context.Background()

	require.Error(t, f.commit(ctx, d, uuid.New()), "an execution for another run is rejected by the transaction")
	assert.Equal(t, base, f.latest(t))
	assert.Equal(t, runner.JobRunning, f.jobOf(t, d).State)

	require.NoError(t, f.d.ReleaseCompletions(ctx, d.sink.Completions(), errors.New("step commit failed")))
	assert.Equal(t, "v1", f.read(t, "a.txt"))
	assert.Equal(t, base, f.latest(t))
	assert.Equal(t, runner.JobFailed, f.jobOf(t, d).State)
	_, ok := f.active(t)
	assert.False(t, ok)
	held, err := f.pool.Ledger.ListActiveByRunner(ctx, builtin)
	require.NoError(t, err)
	assert.Empty(t, held, "the slot is free")
}

// Edge: releasing after a durable commit (lost acknowledgement) is harmless:
// the committed result is replayed, never rolled back.
func TestDispatch_ReleaseAfterDurableCommitIsHarmless(t *testing.T) {
	f := newWDFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners[builtin].set(map[string]string{"a.txt": "v2"}, nil)
	d := f.dispatch(t)
	require.NoError(t, d.err)
	require.NoError(t, f.commit(context.Background(), d, f.run))
	committed := f.latest(t)

	require.NoError(t, f.d.ReleaseCompletions(context.Background(), d.sink.Completions(), errors.New("commit acknowledgement lost")))
	assert.Equal(t, runner.JobCompleted, f.jobOf(t, d).State)
	assert.Equal(t, committed, f.latest(t))
	assert.Equal(t, "v2", f.read(t, "a.txt"))
}

// Error: a workspace step without a completion sink can never be committed, so it fails closed.
func TestDispatch_WorkspaceStepWithoutCompletionSinkFailsClosed(t *testing.T) {
	f := newWDFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners[builtin].set(map[string]string{"a.txt": "v2"}, nil)
	ctx, _, inv := f.stepCtx(t, context.Background(), false)
	_, err := f.d.Dispatch(ctx, f.req(), *f.job())
	require.ErrorIs(t, err, runner.ErrNoCompletionSink)
	assert.Equal(t, runner.JobFailed, f.jobOf(t, dispatched{inv: inv}).State)
	assert.Equal(t, "v1", f.read(t, "a.txt"))
	_, ok := f.active(t)
	assert.False(t, ok)
}

// Error: production Phase 3 mode without a workspace service never degrades
// silently; legacy fixtures must opt in explicitly.
func TestDispatch_ProductionMissingWorkspaceFailsClosed(t *testing.T) {
	r := &scriptedRunner{id: builtin, fallback: func(context.Context, runner.Assignment, runner.StepJob) (runner.StepOutput, error) {
		return runner.StepOutput{Process: &engine.ProcessResult{Stdout: "{}"}}, nil
	}}
	f := builtinOnly(t, anthropicSub(builtin), r)
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	req := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess, Affinity: runner.AffinityWorkspace}
	job := runner.StepJob{Op: runner.OpProcess, Process: &engine.ProcessSpec{Kind: engine.ProcessScript, Command: "/bin/sh", WorkDir: "/ws/instances/" + run.String()}}

	_, err := f.d.Dispatch(ctx, req, job)
	require.ErrorIs(t, err, runner.ErrNoWorkspace)
	assert.Zero(t, f.pool.jobCount(), "nothing is recorded")
	assert.Empty(t, r.callList(), "nothing runs")

	legacy := runner.NewStepDispatcher(runner.DispatcherDeps{Registry: f.pool, Ledger: f.pool, Runners: runner.StaticRunnerSet{builtin: r},
		Events: runner.NewEventRecorder(f.log, f.bus, 1000, f.clock.Now), Status: runner.NewExecutionStatusProjector(f.pool), Health: f.health,
		Clock: f.clock.Now, TransientMaxAttempts: 1, BackoffMin: time.Millisecond, BackoffMax: time.Millisecond, Sleep: f.clock.Sleep,
		LegacyBuiltInWorkspace: true})
	t.Cleanup(legacy.Close)
	run2 := uuid.New()
	ctx2, _, _ := stamped(run2, 1)
	out, err := legacy.Dispatch(ctx2, req, job)
	require.NoError(t, err)
	require.NotNil(t, out.Process)
	assert.Nil(t, out.Completion, "no sink: immediate legacy completion")
	assert.Equal(t, runner.JobCompleted, f.pool.jobFor(run2).State)
}

// A non-workspace operation under a node sink also defers ledger completion to
// the step transaction; ReleaseCompletions frees it on failure.
func TestDispatch_NonWorkspaceStepWithSinkDefersLedgerCompletion(t *testing.T) {
	r := &scriptedRunner{id: builtin}
	f := builtinOnly(t, anthropicSub(builtin), r)
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	sink := engine.NewStepCompletionSink()
	ctx = engine.ContextWithStepCompletionSink(ctx, sink)

	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.NoError(t, err)
	j := f.pool.jobFor(run)
	assert.Equal(t, runner.JobRunning, j.State)
	want := engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: j.ID, RunnerID: string(builtin), Generation: j.Generation}}
	assert.Equal(t, []engine.StepCompletion{want}, sink.Completions())
	require.NotNil(t, out.Completion)
	assert.Equal(t, want, *out.Completion)

	require.NoError(t, f.d.ReleaseCompletions(context.Background(), sink.Completions(), errors.New("output validation failed")))
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
	require.ErrorIs(t, f.d.ReleaseCompletions(context.Background(), sink.Completions(), errors.New("output validation failed")), runner.ErrUnknownReceipt)
}

// Production Phase 3 mode (workspace service installed): an ordinary operation
// without a node sink fails closed; only the synthetic quota probe completes
// through its own owner.
func TestDispatch_ProductionOperationWithoutSinkFailsClosedExceptProbe(t *testing.T) {
	r := &scriptedRunner{id: builtin}
	f := builtinOnly(t, anthropicSub(builtin), r)
	f.d.SetWorkspace(&recWorkspace{})

	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.ErrorIs(t, err, runner.ErrNoCompletionSink)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State, "the slot is released, never held by a success nobody commits")

	probeRun := uuid.New()
	pctx := engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{RunID: probeRun, NodeID: "probe",
		InvocationID: uuid.New(), OperationID: "probe", Kind: engine.OperationProbe, Attempt: 1})
	_, err = f.d.Dispatch(pctx, chatReq(probeRun, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.NoError(t, err)
	assert.Equal(t, runner.JobCompleted, f.pool.jobFor(probeRun).State)
}
