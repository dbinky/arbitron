// domain/runner/adopt_test.go
package runner_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdopterHub honours the assignment fence: it answers only for the
// fence it was primed with and otherwise blocks until ctx ends.
type fakeAdopterHub struct {
	results map[runner.Fence]runner.StepOutput
	errs    map[runner.Fence]error
	calls   []runner.Fence
}

func (h *fakeAdopterHub) Adopt(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	h.calls = append(h.calls, a.Fence)
	if a.Fence != a.Job.Fence() || job.Op != a.Job.Op {
		return runner.StepOutput{}, fmt.Errorf("adoption does not match the job's exact fence and operation")
	}
	if out, ok := h.results[a.Fence]; ok {
		return out, nil
	}
	if err, ok := h.errs[a.Fence]; ok {
		return runner.StepOutput{}, err
	}
	<-ctx.Done()
	return runner.StepOutput{}, ctx.Err()
}

// livenessRegistry answers GetRunner with a settable liveness.
type livenessRegistry struct {
	runner.Registry
	live map[runner.RunnerID]runner.Liveness
}

func (r *livenessRegistry) GetRunner(_ context.Context, id runner.RunnerID) (runner.Runner, []runner.Source, error) {
	return runner.Runner{ID: id, Liveness: r.live[id]}, nil, nil
}

type adoptFixture struct {
	clock   *p3Clock
	ledger  *p3MemLedger
	hub     *fakeAdopterHub
	reg     *livenessRegistry
	leases  *p3MemLeases
	audit   *recAudit
	stopper *recordingStopper
	log     *memoryEventLog
	ad      *runner.JobAdopter
}

func newAdoptFixture(t *testing.T) *adoptFixture {
	t.Helper()
	f := &adoptFixture{clock: newP3Clock(), hub: &fakeAdopterHub{results: map[runner.Fence]runner.StepOutput{}, errs: map[runner.Fence]error{}},
		reg: &livenessRegistry{live: map[runner.RunnerID]runner.Liveness{}}, log: &memoryEventLog{}, audit: &recAudit{}}
	f.ledger = newP3Ledger(f.clock.Now)
	f.leases = newP3Leases(f.ledger, "boot-2", nil)
	f.leases.lost = func(id runner.RunnerID) bool { return f.reg.live[id] == runner.LivenessLost }
	f.stopper = &recordingStopper{leases: f.leases}
	bus := &recordingBus{}
	status := runner.NewExecutionStatusProjector(f.ledger)
	status.AttachBus(bus)
	ad, err := runner.NewJobAdopter(runner.AdoptDeps{Ledger: f.ledger, Registry: f.reg, Hub: f.hub, Leases: f.leases, Stopper: f.stopper,
		Events: runner.NewEventRecorder(f.log, bus, 100, f.clock.Now), Status: status, Audit: f.audit, Clock: f.clock.Now, Poll: 5 * time.Millisecond})
	require.NoError(t, err)
	f.ad = ad
	return f
}

func (f *adoptFixture) live(rid runner.RunnerID, op runner.OpType) runner.Job {
	lease := f.clock.Now().Add(time.Minute)
	return f.ledger.put(runner.Job{Key: runner.JobKey{RunID: uuid.New(), InvocationID: uuid.New(), OperationID: "node", Attempt: 1},
		NodeID: "edit", Op: op, RunnerID: rid, SourceID: runner.SourceID(rid + "/anthropic_subscription/-"), Generation: 3, State: runner.JobRunning, LeaseUntil: &lease})
}

func (f *adoptFixture) claim(t *testing.T, j runner.Job) engine.ResourceFenceRef {
	t.Helper()
	ref, err := f.leases.ClaimJob(context.Background(), "run:"+j.Key.RunID.String(), runner.JobHolder(j), j.Fence())
	require.NoError(t, err)
	return ref
}

func (f *adoptFixture) claimed(t *testing.T, j runner.Job) bool {
	t.Helper()
	_, ok, err := f.leases.Active(context.Background(), "run:"+j.Key.RunID.String())
	require.NoError(t, err)
	return ok
}

func TestJobAdopter_AdoptsWithoutRedispatch(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	j := f.live("mac-1", runner.OpAgent)
	held := f.claim(t, j)
	fence := j.Fence()
	f.hub.results[fence] = runner.StepOutput{Agent: &engine.AgentResult{Summary: "finished while we were down"}}

	out, done, err := f.ad.Resolve(ctx, j, runner.StepJob{Op: runner.OpAgent})
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "finished while we were down", out.Agent.Summary)
	assert.Equal(t, []runner.Fence{fence}, f.hub.calls, "awaited with the job's own fence")
	got, _ := f.ledger.Get(ctx, j.ID)
	assert.Equal(t, runner.JobRunning, got.State, "the dispatcher completes it through the receipt path, not the adopter")
	assert.Equal(t, int64(3), got.Generation, "no new generation: the job was not re-dispatched")
	require.NoError(t, f.leases.Check(ctx, held), "the adopted writer keeps its claim for FinishSuccess")
	require.Len(t, f.log.events, 1)
	assert.Equal(t, runner.EventJobAdopted, f.log.events[0].Kind)
}

func TestJobAdopter_RunnerLostAfterGraceIsRecovered(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live("mac-1", runner.OpAgent)
	f.claim(t, j)
	f.reg.live["mac-1"] = runner.LivenessLost

	_, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpAgent})
	require.NoError(t, err)
	assert.False(t, done, "normal placement re-places the step")
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobQueued, got.State)
	att, _ := f.ledger.Attempts(context.Background(), j.ID)
	assert.Equal(t, string(runner.OutcomeRunnerLost), att[len(att)-1].Outcome, "the interrupted attempt is a recovery notice")
	assert.False(t, f.claimed(t, j), "the lost runner's claim is superseded on runner_lost evidence and freed")
	assert.Equal(t, runner.EventJobRecovered, f.log.events[len(f.log.events)-1].Kind)
}

// Fail closed: the runner is online and the adoption failed with a generic loss
// error that carries no reconnect report (not ErrJobAbsentOnReconnect), so no
// durable evidence proves its writer stopped. The job is fenced, the claim is
// kept and the step fails visibly instead of re-placing onto a possibly dirty
// workspace. Decision D6 covers only the durable reconnect-absence report.
func TestJobAdopter_UnprovenTakeoverFailsClosed(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live("mac-1", runner.OpProcess)
	f.claim(t, j)
	f.reg.live["mac-1"] = runner.LivenessOnline
	f.hub.errs[j.Fence()] = runner.WithOutcome(runner.ErrRunnerLost, runner.SourceOutcome{Class: runner.OutcomeRunnerLost})

	_, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpProcess})
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven)
	assert.False(t, done)
	require.ErrorIs(t, f.ledger.FenceCheck(context.Background(), j.Fence()), runner.ErrFenced, "a late write from the old generation is rejected")
	assert.True(t, f.claimed(t, j), "the claim is never taken over without evidence")
}

func TestJobAdopter_KeepsWaitingWhileTheRunnerIsAlive(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live("mac-1", runner.OpProcess)
	f.reg.live["mac-1"] = runner.LivenessOnline
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, done, err := f.ad.Resolve(ctx, j, runner.StepJob{Op: runner.OpProcess})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, done)
	assert.Greater(t, len(f.hub.calls), 1, "it polls instead of giving up while the runner is online")
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobRunning, got.State, "a live runner's job is never requeued")
}

func TestJobAdopter_BuiltInJobIsStoppedThenRequeued(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live(runner.BuiltInRunnerID, runner.OpAgent)
	f.claim(t, j)

	_, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpAgent})
	require.NoError(t, err)
	assert.False(t, done)
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobQueued, got.State)
	assert.Equal(t, []runner.Fence{j.Fence()}, f.stopper.calls, "a built-in writer is stopped before its claim is taken over")
	assert.False(t, f.claimed(t, j))
	assert.Empty(t, f.hub.calls)
}

// Alignment: a remote chat job is adopted (its durable result consumed), not requeued.
func TestJobAdopter_RemoteChatIsAdoptedNotRequeued(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live("mac-1", runner.OpChat)
	f.hub.results[j.Fence()] = runner.StepOutput{Chat: &engine.ChatResponse{Output: "once"}}

	out, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpChat})
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "once", out.Chat.Output)
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobRunning, got.State)
	assert.Equal(t, int64(3), got.Generation)
}

func TestJobAdopter_FailureIsReturnedForTheDispatcherToRollBack(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live("mac-1", runner.OpProcess)
	f.hub.errs[j.Fence()] = runner.WithOutcome(assert.AnError, runner.SourceOutcome{Class: runner.OutcomeOther})
	_, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpProcess})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, done)
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobRunning, got.State, "FinishFailure needs the live fence; the dispatcher rolls back, then releases")
}

func TestJobAdopter_QueuedJobIsNotItsBusiness(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.ledger.put(runner.Job{Key: runner.JobKey{RunID: uuid.New(), InvocationID: uuid.New(), OperationID: "node", Attempt: 1}, State: runner.JobQueued})
	_, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpAgent})
	require.NoError(t, err)
	assert.False(t, done)
	assert.Empty(t, f.hub.calls)
}

func TestNewJobAdopter_RequiresEveryDependency(t *testing.T) {
	_, err := runner.NewJobAdopter(runner.AdoptDeps{})
	require.Error(t, err)
}

// Decision D6: the runner reconnected without the job and the hub recorded
// that report durably. The report is the stop evidence: the job is requeued,
// the claim is superseded (reconnect_absent) and freed, and the takeover is
// audit-logged and recorded as job.recovered.
func TestJobAdopter_ReconnectAbsenceIsStopEvidence(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live("mac-1", runner.OpProcess)
	f.claim(t, j)
	f.reg.live["mac-1"] = runner.LivenessOnline
	f.leases.MarkAbsent(j.Fence()) // RemoteInboxRepo.RecordAbsent, done by the hub
	f.hub.errs[j.Fence()] = runner.WithOutcome(fmt.Errorf("%w: %w", runner.ErrJobAbsentOnReconnect, runner.ErrRunnerLost),
		runner.SourceOutcome{Class: runner.OutcomeRunnerLost})

	_, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpProcess})
	require.NoError(t, err)
	assert.False(t, done, "normal placement re-places the step")
	got, _ := f.ledger.Get(context.Background(), j.ID)
	assert.Equal(t, runner.JobQueued, got.State)
	assert.False(t, f.claimed(t, j), "the claim is superseded on the reconnect report and freed")
	ev := f.log.events[len(f.log.events)-1]
	assert.Equal(t, runner.EventJobRecovered, ev.Kind)
	assert.Equal(t, "reconnect_absent", ev.Detail["reason"])
	require.Len(t, f.audit.calls, 1, "the takeover is audit-logged once")
	assert.Equal(t, audit.ActionRunnerJobReconnectAbsent, f.audit.calls[0].action)
	assert.Equal(t, "job:"+j.ID.String(), f.audit.calls[0].target)
	assert.Contains(t, f.audit.calls[0].detail, `"evidence":"reconnect_absent"`)
	assert.Empty(t, f.stopper.calls, "a remote writer is never signalled by the server")
}

// D6 needs the DURABLE report: an absence error whose report was never stored
// (recorder missing or failed) proves nothing, so the takeover fails closed
// and nothing is audited.
func TestJobAdopter_UnrecordedAbsenceStaysFailClosed(t *testing.T) {
	f := newAdoptFixture(t)
	j := f.live("mac-1", runner.OpProcess)
	f.claim(t, j)
	f.reg.live["mac-1"] = runner.LivenessOnline
	f.hub.errs[j.Fence()] = runner.WithOutcome(fmt.Errorf("%w: %w", runner.ErrJobAbsentOnReconnect, runner.ErrRunnerLost),
		runner.SourceOutcome{Class: runner.OutcomeRunnerLost})

	_, done, err := f.ad.Resolve(context.Background(), j, runner.StepJob{Op: runner.OpProcess})
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven)
	assert.False(t, done)
	require.ErrorIs(t, f.ledger.FenceCheck(context.Background(), j.Fence()), runner.ErrFenced)
	assert.True(t, f.claimed(t, j), "the claim is never taken over without the stored report")
	assert.Empty(t, f.audit.calls)
}
