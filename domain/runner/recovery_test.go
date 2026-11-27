// domain/runner/recovery_test.go
package runner_test

import (
	"context"
	"errors"
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

type recordingRestorer struct {
	calls []string
	err   error
	trail *[]string
}

func (r *recordingRestorer) RestoreBuiltIn(_ context.Context, runID uuid.UUID, dir string) error {
	r.calls = append(r.calls, runID.String()+"|"+dir)
	if r.trail != nil {
		*r.trail = append(*r.trail, "restore")
	}
	return r.err
}

// recordingStopper is the runner.BuiltInStopper fake (the real one is
// builtin.ProcessSupervisor, Cycles 2c-2d). A confirmed stop writes the
// 'stopped' evidence that Supersede(kind stopped) reads.
type recordingStopper struct {
	leases *p3MemLeases
	err    error
	calls  []runner.Fence
	trail  *[]string
}

func (s *recordingStopper) StopAndWait(_ context.Context, f runner.Fence) error {
	s.calls = append(s.calls, f)
	if s.trail != nil {
		*s.trail = append(*s.trail, "stop")
	}
	if s.err != nil {
		return s.err
	}
	s.leases.MarkStopped(f)
	return nil
}

// memInbox is a runner.RemoteInbox holding terminal receipts only.
type memInbox struct {
	results map[runner.Fence]runner.RemoteResult
}

func (m *memInbox) AcceptOutput(context.Context, runner.Fence, runner.OutputFrame) (uint64, error) {
	return 0, errors.New("not used")
}
func (m *memInbox) AcceptResult(_ context.Context, f runner.Fence, r runner.RemoteResult) error {
	m.results[f] = r
	return nil
}
func (m *memInbox) Receipt(_ context.Context, f runner.Fence) (runner.InboxReceipt, bool, error) {
	r, ok := m.results[f]
	if !ok {
		return runner.InboxReceipt{}, false, nil
	}
	return runner.InboxReceipt{Fence: f, Result: &r}, true, nil
}
func (m *memInbox) Outputs(context.Context, runner.Fence, uint64) ([]runner.OutputFrame, error) {
	return nil, nil
}

type committedSet map[uuid.UUID]bool

func (c committedSet) CommitStep(context.Context, engine.StepCommit) error {
	return errors.New("not used")
}
func (c committedSet) CommittedResult(_ context.Context, inv uuid.UUID) (*engine.StepCommit, bool, error) {
	if c[inv] {
		return &engine.StepCommit{InvocationID: inv}, true, nil
	}
	return nil, false, nil
}

type graceRecorder struct{ until []time.Time }

func (g *graceRecorder) BeginRestartGrace(until time.Time) { g.until = append(g.until, until) }

type recoveryFixture struct {
	clock    *p3Clock
	ledger   *p3MemLedger
	homes    *p3MemHomes
	leases   *p3MemLeases
	stopper  *recordingStopper
	inbox    *memInbox
	restorer *recordingRestorer
	commits  committedSet
	grace    *graceRecorder
	log      *memoryEventLog
	bus      *recordingBus
	trail    []string // stop / claim / supersede / restore / release, in call order
	rec      *runner.PoolRecovery
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	f := &recoveryFixture{clock: newP3Clock(), commits: committedSet{}, grace: &graceRecorder{}, log: &memoryEventLog{}, bus: &recordingBus{},
		inbox: &memInbox{results: map[runner.Fence]runner.RemoteResult{}}}
	f.ledger = newP3Ledger(f.clock.Now)
	f.homes = newP3Homes(f.clock.Now)
	f.leases = newP3Leases(f.ledger, "boot-2", &f.trail)
	f.stopper = &recordingStopper{leases: f.leases, trail: &f.trail}
	f.restorer = &recordingRestorer{trail: &f.trail}
	status := runner.NewExecutionStatusProjector(f.ledger)
	status.AttachBus(f.bus)
	rec, err := runner.NewPoolRecovery(runner.RecoveryDeps{
		Inbox: f.inbox, Leases: f.leases, Stopper: f.stopper,
		Ledger: f.ledger, Homes: f.homes, Restorer: f.restorer, Commits: f.commits, Grace: f.grace,
		Events: runner.NewEventRecorder(f.log, f.bus, 100, f.clock.Now), Status: status,
		Clock: f.clock.Now, RestartGrace: 3 * time.Minute,
	})
	require.NoError(t, err)
	f.rec = rec
	return f
}

// claim gives job j the run's workspace claim exactly as WorkspaceSync.BeginAttempt does.
func (f *recoveryFixture) claim(t *testing.T, j runner.Job) engine.ResourceFenceRef {
	t.Helper()
	ref, err := f.leases.ClaimJob(context.Background(), "run:"+j.Key.RunID.String(), runner.JobHolder(j), j.Fence())
	require.NoError(t, err)
	return ref
}

func (f *recoveryFixture) job(run uuid.UUID, node string, rid runner.RunnerID, st runner.JobState) runner.Job {
	lease := f.clock.Now().Add(30 * time.Second)
	return f.ledger.put(runner.Job{Key: runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1},
		NodeID: node, Op: runner.OpAgent, RunnerID: rid, Generation: 1, State: st, LeaseUntil: &lease})
}

func inst(run uuid.UUID, node, workspace string) *instance.Instance {
	n := node
	return &instance.Instance{ID: run, State: instance.StateRunning, CurrentNodeID: &n, Variables: map[string]any{engine.ReservedVarWorkspace: workspace}}
}

func (f *recoveryFixture) kinds() []string {
	var out []string
	for _, e := range f.log.events {
		out = append(out, e.Kind)
	}
	return out
}

var _ engine.RecoveryPolicy = (*runner.PoolRecovery)(nil)

func TestPoolRecovery_RecoverExtendsRemoteLeasesAndOpensGrace(t *testing.T) {
	f := newRecoveryFixture(t)
	run := uuid.New()
	remote := f.job(run, "edit", "mac-1", runner.JobRunning)
	builtin := f.job(uuid.New(), "edit", runner.BuiltInRunnerID, runner.JobRunning)

	require.NoError(t, f.rec.Recover(context.Background()))
	want := f.clock.Now().Add(3 * time.Minute)
	got, _ := f.ledger.Get(context.Background(), remote.ID)
	require.NotNil(t, got.LeaseUntil)
	assert.Equal(t, want, *got.LeaseUntil, "remote leases are extended by the restart grace")
	b, _ := f.ledger.Get(context.Background(), builtin.ID)
	assert.NotEqual(t, want, *b.LeaseUntil, "built-in jobs are not extended: they cannot survive a restart")
	assert.Equal(t, []time.Time{want}, f.grace.until, "the hub refuses to declare runners lost before the grace ends")
}

func TestPoolRecovery_BuiltInJobIsFencedRestoredAndRepeated(t *testing.T) {
	f := newRecoveryFixture(t)
	run := uuid.New()
	j := f.job(run, "edit", runner.BuiltInRunnerID, runner.JobRunning)

	action, err := f.rec.PrepareRun(context.Background(), inst(run, "edit", "/srv/ws/instances/"+run.String()))
	require.NoError(t, err)
	assert.Equal(t, engine.RecoveryResume, action)

	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobQueued, got.State, "the interrupted built-in attempt is fenced and requeued")
	require.ErrorIs(t, f.ledger.FenceCheck(context.Background(), runner.Fence{JobID: j.ID, RunnerID: runner.BuiltInRunnerID, Generation: 1}), runner.ErrFenced)
	att, _ := f.ledger.Attempts(context.Background(), j.ID)
	assert.Equal(t, string(runner.OutcomeRunnerLost), att[len(att)-1].Outcome)
	assert.Equal(t, []string{run.String() + "|/srv/ws/instances/" + run.String()}, f.restorer.calls, "the last committed checkpoint is restored before the step repeats")
	assert.Contains(t, f.kinds(), runner.EventJobRecovered)
	assert.Equal(t, instance.RecoveryNoticeText, f.log.events[0].Detail["notice"])
	assert.Equal(t, []runner.Fence{{JobID: j.ID, RunnerID: runner.BuiltInRunnerID, Generation: 1}}, f.stopper.calls, "stopped before the restore")
	_, held, err := f.leases.Active(context.Background(), "run:"+run.String())
	require.NoError(t, err)
	assert.False(t, held, "recovery's restore claim is released for the repeat")
}

func TestPoolRecovery_CommittedStepReplaysWithoutRestore(t *testing.T) {
	f := newRecoveryFixture(t)
	run := uuid.New()
	done := f.job(run, "edit", runner.BuiltInRunnerID, runner.JobCompleted)
	f.commits[done.Key.InvocationID] = true

	action, err := f.rec.PrepareRun(context.Background(), inst(run, "edit", "/ws"))
	require.NoError(t, err)
	assert.Equal(t, engine.RecoveryReplayCommitted, action)
	assert.Empty(t, f.restorer.calls, "a committed step is never restored or repeated")
	assert.Empty(t, f.log.events)
}

func TestPoolRecovery_RemoteJobWaitsForAdoption(t *testing.T) {
	f := newRecoveryFixture(t)
	run := uuid.New()
	j := f.job(run, "edit", "mac-1", runner.JobRunning)
	held := f.claim(t, j)

	action, err := f.rec.PrepareRun(context.Background(), inst(run, "edit", "/ws"))
	require.NoError(t, err)
	assert.Equal(t, engine.RecoveryResume, action)
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobRunning, got.State, "a remote job is left for the dispatcher to adopt")
	assert.Empty(t, f.restorer.calls)
	assert.Empty(t, f.stopper.calls)
	require.NoError(t, f.leases.Check(context.Background(), held), "suspect/restart grace never takes over a remote claim")
	ev := f.bus.ofType("execution.status")
	require.Len(t, ev, 1)
	st, ok := ev[0].Event.Data.(instance.ExecutionStatus)
	require.True(t, ok)
	assert.Equal(t, instance.ExecRecovering, st.State)
	assert.Equal(t, run, ev[0].InstanceID)
}

func TestPoolRecovery_RestoreFailureIsAnError(t *testing.T) {
	f := newRecoveryFixture(t)
	f.restorer.err = errors.New("disk full")
	run := uuid.New()
	f.job(run, "edit", runner.BuiltInRunnerID, runner.JobOffered)
	_, err := f.rec.PrepareRun(context.Background(), inst(run, "edit", "/ws"))
	require.Error(t, err, "the engine fails this run rather than repeating on a dirty tree")
}

func TestPoolRecovery_RemoteHomeIsNotRestoredLocally(t *testing.T) {
	f := newRecoveryFixture(t)
	run := uuid.New()
	_, err := f.homes.AcquireResource(context.Background(), run, "mac-1", 0)
	require.NoError(t, err)
	f.job(run, "edit", runner.BuiltInRunnerID, runner.JobRunning)
	_, err = f.rec.PrepareRun(context.Background(), inst(run, "edit", "/ws"))
	require.NoError(t, err)
	assert.Empty(t, f.restorer.calls, "a workspace homed on a remote runner is re-pulled there, not restored on the server")
}

func TestNewPoolRecovery_RequiresEveryDependency(t *testing.T) {
	_, err := runner.NewPoolRecovery(runner.RecoveryDeps{})
	require.Error(t, err)
}

// Alignment (Failure): an unconfirmed stop blocks restore AND retry.
func TestPoolRecovery_StopFailurePreventsRestoreAndRetry(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	run := uuid.New()
	j := f.job(run, "edit", runner.BuiltInRunnerID, runner.JobRunning)
	old := f.claim(t, j)
	f.stopper.err = errors.New("operation not permitted")

	_, err := f.rec.PrepareRun(ctx, inst(run, "edit", "/ws"))
	require.Error(t, err, "an unconfirmed stop fails this run's recovery")
	assert.Empty(t, f.restorer.calls, "never restore over a writer that may still be running")
	rec, ok, err := f.leases.Active(ctx, "run:"+run.String())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, old, rec.Fence, "the old writer's claim is neither superseded nor released")
	again, err := f.ledger.Reserve(ctx, j.ID, runner.Decision{Runner: runner.BuiltInRunnerID}, 4, f.clock.Now().Add(time.Minute))
	require.NoError(t, err)
	_, err = f.leases.ClaimJob(ctx, "run:"+run.String(), runner.JobHolder(again), again.Fence())
	require.ErrorIs(t, err, runner.ErrResourceBusy, "a retry cannot take the workspace while the old process is unconfirmed")
}

// Alignment: fence -> stop -> supersede -> restore committed -> free for the repeat.
func TestPoolRecovery_OldProcessCannotWriteAfterRestore(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	run := uuid.New()
	j := f.job(run, "edit", runner.BuiltInRunnerID, runner.JobRunning)
	old := f.claim(t, j)

	action, err := f.rec.PrepareRun(ctx, inst(run, "edit", "/ws"))
	require.NoError(t, err)
	assert.Equal(t, engine.RecoveryResume, action)
	assert.Equal(t, []string{"stop", "supersede", "restore", "release"}, f.trail)
	assert.Equal(t, []runner.Fence{j.Fence()}, f.stopper.calls, "the exact interrupted generation is stopped once")
	require.ErrorIs(t, f.leases.Check(ctx, old), runner.ErrResourceFenced, "the old writer's claim is dead after the restore")
	_, err = f.leases.ClaimJob(ctx, "run:"+run.String(), runner.JobHolder(j), j.Fence())
	require.ErrorIs(t, err, runner.ErrFenced, "the old job generation can never re-claim the workspace")
	again, err := f.ledger.Reserve(ctx, j.ID, runner.Decision{Runner: runner.BuiltInRunnerID}, 4, f.clock.Now().Add(time.Minute))
	require.NoError(t, err)
	_, err = f.leases.ClaimJob(ctx, "run:"+run.String(), runner.JobHolder(again), again.Fence())
	require.NoError(t, err, "the repeat claims the restored workspace")
}

// Alignment (Edge-case): a candidate PREPARED by the interrupted attempt is
// bound to that attempt's job and resource fences (P3 T2/T6). Recovery kills
// both before restoring and restores only through RestoreBuiltIn (committed
// Latest only), so the candidate can neither be the base nor commit later
// (P3 T5 checks both fences). P3 T13's root test proves it on real storage.
func TestPoolRecovery_PreparedCandidateIsNotRecoveryBase(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	run := uuid.New()
	j := f.job(run, "edit", runner.BuiltInRunnerID, runner.JobRunning)
	candidateResource := f.claim(t, j)
	candidateJob := j.Fence()

	_, err := f.rec.PrepareRun(ctx, inst(run, "edit", "/ws"))
	require.NoError(t, err)
	require.ErrorIs(t, f.ledger.FenceCheck(ctx, candidateJob), runner.ErrFenced)
	require.ErrorIs(t, f.leases.Check(ctx, candidateResource), runner.ErrResourceFenced)
	assert.Equal(t, []string{run.String() + "|/ws"}, f.restorer.calls, "the base is the committed checkpoint, never the candidate")
}

// Design D7: a server-side holder (group mutation, drain, recovery) from an
// earlier boot is superseded with server_restart evidence and the base restored.
func TestPoolRecovery_ServerHolderFromAnEarlierBootIsSupersededAndRestored(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	run := uuid.New()
	old := f.leases.seedServerClaim("run:"+run.String(), "group-mutation:inv", "boot-1")

	action, err := f.rec.PrepareRun(ctx, inst(run, "edit", "/ws"))
	require.NoError(t, err)
	assert.Equal(t, engine.RecoveryResume, action)
	require.ErrorIs(t, f.leases.Check(ctx, old), runner.ErrResourceFenced)
	assert.Equal(t, []string{"supersede", "restore", "release"}, f.trail)
	_, ok, err := f.leases.Active(ctx, "run:"+run.String())
	require.NoError(t, err)
	assert.False(t, ok, "the workspace is free for the re-entered step")
}

// RecoveryDeps.Inbox: a remote job whose terminal result is already durable is
// not waiting on its runner, so it is not shown `recovering`; it is still adopted.
func TestPoolRecovery_RetainedRemoteResultIsNotShownRecovering(t *testing.T) {
	f := newRecoveryFixture(t)
	run := uuid.New()
	j := f.job(run, "edit", "mac-1", runner.JobRunning)
	f.inbox.results[j.Fence()] = runner.RemoteResult{OK: true}

	action, err := f.rec.PrepareRun(context.Background(), inst(run, "edit", "/ws"))
	require.NoError(t, err)
	assert.Equal(t, engine.RecoveryResume, action)
	assert.Empty(t, f.bus.ofType("execution.status"))
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobRunning, got.State, "never requeued: the dispatcher's JobAdopter consumes the receipt")
}

// RR-17 B: non-portable homes restore before retry, just like portable homes.
func TestPoolRecovery_NonPortableHomeRestoresLocalCheckpointAndRepeats(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	run := uuid.New()
	require.NoError(t, f.homes.SetNonPortable(ctx, run, runner.NonPortablePromisorReason))
	require.NoError(t, f.homes.BootstrapLocalCheckpoint(ctx, run,
		runner.LocalCheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("a", 64)}.String()))
	j := f.job(run, "edit", runner.BuiltInRunnerID, runner.JobRunning)
	ws := "/srv/ws/instances/" + run.String()

	action, err := f.rec.PrepareRun(ctx, inst(run, "edit", ws))
	require.NoError(t, err)
	assert.Equal(t, engine.RecoveryResume, action, "the interrupted step repeats")
	got, _ := f.ledger.Get(ctx, j.ID)
	assert.Equal(t, runner.JobQueued, got.State)
	require.ErrorIs(t, f.ledger.FenceCheck(ctx, j.Fence()), runner.ErrFenced)
	assert.Equal(t, []string{run.String() + "|" + ws}, f.restorer.calls, "restored before the repeat, never skipped")
	assert.Contains(t, f.kinds(), runner.EventJobRecovered)
	assert.NotContains(t, f.kinds(), runner.EventJobRecoveryBlocked)
}

func TestPoolRecovery_NonPortableMissingBaseShowsRecoveryBlocker(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	run := uuid.New()
	require.NoError(t, f.homes.SetNonPortable(ctx, run, runner.NonPortablePromisorReason))
	f.restorer.err = fmt.Errorf("%w: run %s (%s)", runner.ErrNoRecoveryBase, run, runner.NonPortablePromisorReason)
	f.job(run, "edit", runner.BuiltInRunnerID, runner.JobRunning)

	action, err := f.rec.PrepareRun(ctx, inst(run, "edit", "/ws"))
	require.ErrorIs(t, err, runner.ErrNoRecoveryBase)
	assert.Equal(t, engine.RecoveryFail, action, "the run stops visibly; the step is not re-entered on dirty files")
	var blocked *runner.PoolEvent
	for i := range f.log.events {
		if f.log.events[i].Kind == runner.EventJobRecoveryBlocked {
			blocked = &f.log.events[i]
		}
	}
	require.NotNil(t, blocked, "a durable recovery blocker is recorded")
	require.NotNil(t, blocked.RunID)
	assert.Equal(t, run, *blocked.RunID)
	assert.Contains(t, blocked.Detail["reason"], runner.NonPortablePromisorReason)
}
