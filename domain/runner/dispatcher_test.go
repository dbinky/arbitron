package runner_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func builtinOnly(t *testing.T, src runner.Source, r *scriptedRunner) *dispatchFixture {
	return newDispatchFixture(t, []runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{src}, runner.StaticRunnerSet{runner.BuiltInRunnerID: r})
}

func TestStaticRunnerSet_For(t *testing.T) {
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	set := runner.StaticRunnerSet{runner.BuiltInRunnerID: r}
	got, ok := set.For(runner.BuiltInRunnerID)
	require.True(t, ok)
	assert.Same(t, r, got)
	_, ok = set.For("mac-mini")
	assert.False(t, ok)
	assert.Empty(t, runner.EmptyTelemetry{}.Latest())
}

func TestNewStepDispatcher_RejectsMissingDeps(t *testing.T) {
	ok := func() runner.DispatcherDeps {
		pool := newMemPool(time.Now, nil, nil)
		return runner.DispatcherDeps{Registry: pool, Ledger: pool, Runners: runner.StaticRunnerSet{},
			Events: runner.NewEventRecorder(&memoryEventLog{}, &recordingBus{}, 10, nil), Status: runner.NewExecutionStatusProjector(pool),
			Health: &fakeHealth{pool: pool}, TransientMaxAttempts: 3, BackoffMin: time.Millisecond, BackoffMax: time.Second}
	}
	assert.NotPanics(t, func() { runner.NewStepDispatcher(ok()) })
	for name, mut := range map[string]func(*runner.DispatcherDeps){
		"registry": func(d *runner.DispatcherDeps) { d.Registry = nil },
		"ledger":   func(d *runner.DispatcherDeps) { d.Ledger = nil },
		"runners":  func(d *runner.DispatcherDeps) { d.Runners = nil },
		"events":   func(d *runner.DispatcherDeps) { d.Events = nil },
		"status":   func(d *runner.DispatcherDeps) { d.Status = nil },
		"health":   func(d *runner.DispatcherDeps) { d.Health = nil },
		"attempts": func(d *runner.DispatcherDeps) { d.TransientMaxAttempts = 0 },
		"backoff":  func(d *runner.DispatcherDeps) { d.BackoffMax = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			d := ok()
			mut(&d)
			assert.Panics(t, func() { runner.NewStepDispatcher(d) })
		})
	}
}

func TestDispatch_MissingIdentityRejected(t *testing.T) {
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)
	_, err := f.d.Dispatch(context.Background(), chatReq(uuid.New(), runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	assert.ErrorIs(t, err, engine.ErrMissingStepIdentity)

	invalid := engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{RunID: uuid.New(), NodeID: "n", OperationID: "node", Attempt: 1})
	_, err = f.d.Dispatch(invalid, chatReq(uuid.New(), runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	assert.ErrorIs(t, err, engine.ErrMissingStepIdentity, "an identity without an invocation is as bad as none")

	assert.Zero(t, f.pool.jobCount(), "nothing is recorded")
	assert.Empty(t, r.callList(), "there is never a silent unplaced run")
}

func TestDispatch_LedgerWriteFailureFailsStepAndNeverRuns(t *testing.T) {
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)
	f.pool.ensureErr = errDisk
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.Error(t, err)
	assert.ErrorIs(t, err, errDisk)
	assert.Contains(t, err.Error(), "record intent")
	assert.Empty(t, r.callList(), "execution never runs unrecorded")
	assert.Empty(t, sink.Facts())
}

func TestDispatch_HappyPathRecordsBuiltInJobAndFact(t *testing.T) {
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	src := anthropicSub(runner.BuiltInRunnerID)
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, id, sink := stamped(run, 1)

	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.NoError(t, err)
	require.NotNil(t, out.Chat)
	assert.Equal(t, "ok@builtin", out.Chat.Output)

	calls := r.callList()
	require.Len(t, calls, 1)
	assert.Equal(t, id, calls[0].id, "the runner receives the stamped identity")
	assert.Equal(t, runner.JobKey{RunID: run, InvocationID: id.InvocationID, OperationID: "node", Attempt: 1}, calls[0].a.Job.Key)
	assert.Equal(t, runner.Fence{JobID: calls[0].a.Job.ID, RunnerID: runner.BuiltInRunnerID, Generation: 1}, calls[0].a.Fence)

	job := f.pool.jobFor(run)
	assert.Equal(t, runner.JobCompleted, job.State)
	assert.Equal(t, job.ID.String(), job.ResultRef, "result ref = job id")
	assert.Equal(t, "draft", job.NodeID)

	facts := sink.Facts()
	require.Len(t, facts, 1)
	assert.Equal(t, engine.PlacementFact{JobID: job.ID, RunnerID: "builtin", SourceID: string(src.ID),
		Reason: "rank: runner=builtin source=builtin/anthropic_subscription/- headroom=unknown slots=0/8", Generation: 1, Outcome: "completed", At: placeNow}, facts[0])

	st := statuses(f.bus, run)
	require.NotEmpty(t, st)
	assert.Equal(t, instance.ExecRunning, st[len(st)-1].State)
	assert.Equal(t, "builtin", st[len(st)-1].RunnerID)
}

func TestDispatch_MissingStepRunnerFailsStep(t *testing.T) {
	f := newDispatchFixture(t, []runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{anthropicSub(runner.BuiltInRunnerID)}, runner.StaticRunnerSet{})
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no step runner for "builtin"`)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State, "the slot is released")
}
func remoteRunner(id runner.RunnerID) runner.Runner { return online(id) }

func TestDispatch_ExhaustedReplacesWithoutSpendingRetries(t *testing.T) {
	reset := placeNow.Add(4 * time.Hour)
	builtinSrc := headroomSource(runner.BuiltInRunnerID, f64(0.9))
	macSrc := withEvidenceFor(headroomSource("mac-mini", f64(0.5)), chatReq(uuid.Nil, runner.OpChat, runner.AffinityNone))
	builtin := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{
		builtinSrc.ID: {failWith(runner.WithOutcome(errors.New("claude exited 1"), runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset}))},
	}}
	mac := &scriptedRunner{id: "mac-mini"}
	f := newDispatchFixture(t, []runner.Runner{online(runner.BuiltInRunnerID), remoteRunner("mac-mini")}, []runner.Source{builtinSrc, macSrc},
		runner.StaticRunnerSet{runner.BuiltInRunnerID: builtin, "mac-mini": mac})
	run := uuid.New()
	ctx, id, sink := stamped(run, 1)
	job := chatJob(runner.OpChat)

	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityNone), job)
	require.NoError(t, err)
	assert.Equal(t, "ok@mac-mini", out.Chat.Output, "re-placed onto another eligible source")

	b, m := builtin.callList(), mac.callList()
	require.Len(t, b, 1)
	require.Len(t, m, 1)
	assert.Equal(t, b[0].a.Job.Key, m[0].a.Job.Key, "the logical operation never changes")
	assert.Equal(t, 1, m[0].a.Job.Key.Attempt, "no workflow retry was spent")
	assert.Equal(t, id, m[0].id)
	assert.Equal(t, int64(1), b[0].a.Fence.Generation)
	assert.Equal(t, int64(3), m[0].a.Fence.Generation, "requeue and re-reserve each bump the generation")
	// Exact model pin and tool scope survive re-placement.
	assert.Equal(t, job.Chat, m[0].job.Chat)
	assert.Equal(t, "claude-sonnet-4-5", m[0].job.Binding.Model)
	assert.True(t, m[0].job.Binding.Pinned)
	assert.Equal(t, []string{"Read", "Grep"}, m[0].job.Binding.AllowedTools)
	assert.True(t, m[0].job.Chat.RequireExecutedModel)

	attempts := f.pool.attemptsOf(m[0].a.Job.ID)
	require.Len(t, attempts, 2, "two ledger attempts")
	assert.Equal(t, "exhausted", attempts[0].Outcome)
	assert.Equal(t, builtinSrc.ID, attempts[0].SourceID)
	assert.Equal(t, "completed", attempts[1].Outcome)
	assert.Equal(t, macSrc.ID, attempts[1].SourceID)

	applied := f.health.outcomes()
	require.Len(t, applied, 1)
	assert.Equal(t, builtinSrc.ID, applied[0].src)
	assert.Equal(t, runner.OutcomeExhausted, applied[0].o.Class)
	assert.Equal(t, runner.CapacityExhausted, f.pool.health(builtinSrc.ID).Capacity)

	rerouted := f.bus.ofType("job.rerouted")
	require.Len(t, rerouted, 1)
	assert.Equal(t, run, rerouted[0].InstanceID, "re-route events are run-scoped")
	assert.Contains(t, f.log.kinds(), "job.rerouted")

	facts := sink.Facts()
	require.Len(t, facts, 2)
	assert.Equal(t, "exhausted", facts[0].Outcome)
	assert.Equal(t, "builtin", facts[0].RunnerID)
	assert.Equal(t, "completed", facts[1].Outcome)
	assert.Equal(t, "mac-mini", facts[1].RunnerID)
}

func TestDispatch_LoggedOutAppliesHealthThenWaitsVisibly(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	r := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{
		src.ID: {failWith(runner.WithOutcome(errors.New("auth"), runner.SourceOutcome{Class: runner.OutcomeLoggedOut}))},
	}}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	base, _, _ := stamped(run, 1)
	ctx, cancel := context.WithCancel(base)
	done := make(chan error, 1)
	go func() {
		_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
		done <- err
	}()

	require.Eventually(t, func() bool {
		st := statuses(f.bus, run)
		return len(st) > 0 && st[len(st)-1].State == instance.ExecWaitingCapacity
	}, 2*time.Second, 5*time.Millisecond, "with its only source logged out the step waits · capacity")
	assert.Equal(t, runner.AuthLoggedOut, f.pool.health(src.ID).Auth)
	require.Len(t, f.health.outcomes(), 1)
	assert.Equal(t, runner.OutcomeLoggedOut, f.health.outcomes()[0].o.Class)
	assert.Len(t, f.bus.ofType("job.rerouted"), 1)

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not end the wait")
	}
	assert.Len(t, r.callList(), 1)
}

func TestDispatch_CapacityWaitNamesGroupAndResetAndEndsOnCancel(t *testing.T) {
	reset := placeNow.Add(3 * time.Hour)
	src := anthropicSub(runner.BuiltInRunnerID)
	src.Health = runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ResetAt: &reset}
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	base, _, _ := stamped(run, 1)
	ctx, cancel := context.WithCancel(base)
	done := make(chan error, 1)
	go func() {
		_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
		done <- err
	}()

	require.Eventually(t, func() bool { return len(statuses(f.bus, run)) > 0 }, 2*time.Second, 5*time.Millisecond)
	st := statuses(f.bus, run)[0]
	assert.Equal(t, instance.ExecWaitingCapacity, st.State)
	assert.Equal(t, "anthropic_subscription:anthropic", st.Group)
	require.NotNil(t, st.NextReset)
	assert.Equal(t, reset, *st.NextReset)
	assert.Contains(t, st.Reason, "waiting · capacity")

	proj, err := runner.NewExecutionStatusProjector(f.pool).ExecutionStatuses(context.Background(), []uuid.UUID{run})
	require.NoError(t, err)
	require.Len(t, proj[run], 1, "the enricher's source agrees while waiting")
	assert.Equal(t, instance.ExecWaitingCapacity, proj[run][0].State)

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled, "capacity waits never time out by themselves; cancel ends them")
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not end the wait")
	}
	assert.Empty(t, r.callList())
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State, "the abandoned intent no longer shows as waiting")
	proj, err = runner.NewExecutionStatusProjector(f.pool).ExecutionStatuses(context.Background(), []uuid.UUID{run})
	require.NoError(t, err)
	assert.Empty(t, proj[run])
}

func TestDispatch_CapacityWaitResumesWhenPoolNotifies(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	src.Health = runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityExhausted}
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	done := make(chan error, 1)
	go func() {
		_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
		done <- err
	}()
	require.Eventually(t, func() bool { return len(statuses(f.bus, run)) > 0 }, 2*time.Second, 5*time.Millisecond)

	require.NoError(t, f.pool.RecordHealth(context.Background(), src.ID, runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityProbation}))
	f.notifier.Notify() // what HealthMonitor does on a transition
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("a pool notification did not wake the waiting step")
	}
	calls := r.callList()
	require.Len(t, calls, 1)
	assert.Equal(t, src.ID, calls[0].a.Job.SourceID, "the probation source takes the woken step")
	assert.Equal(t, runner.JobCompleted, f.pool.jobFor(run).State)
	assert.Empty(t, f.health.outcomes(), "successes reach health via NewHealthReportingRunnerSet (P1 T13), not the dispatcher")
}

func TestDispatch_TransientBackoffHonoursRetryAfterOnSameSource(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	r := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{
		src.ID: {
			failWith(fmt.Errorf("chat: %w", errBusy)), // LunaRoute-style busy, Retry-After 7s
			failWith(runner.WithOutcome(errors.New("overloaded"), runner.SourceOutcome{Class: runner.OutcomeTransient})),
			succeed("ok"),
		},
	}}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.NoError(t, err)

	calls := r.callList()
	require.Len(t, calls, 3)
	for _, c := range calls {
		assert.Equal(t, src.ID, c.a.Job.SourceID, "backoff stays on the same source")
		assert.Equal(t, int64(1), c.a.Fence.Generation, "and the same assignment")
	}
	assert.Equal(t, []time.Duration{7 * time.Second, 2 * time.Second}, f.clock.slept(), "Retry-After raises the first backoff; the second is min*2")
	assert.Empty(t, f.health.outcomes(), "busy is never exhaustion")
	assert.Equal(t, runner.CapacityAvailable, f.pool.health(src.ID).Capacity)

	facts := sink.Facts()
	require.Len(t, facts, 3)
	assert.Equal(t, []string{"transient", "transient", "completed"}, []string{facts[0].Outcome, facts[1].Outcome, facts[2].Outcome})
	assert.Equal(t, 7*time.Second, facts[1].At.Sub(facts[0].At), "the fake clock gated the wait")
	assert.Len(t, f.pool.attemptsOf(calls[0].a.Job.ID), 1)
}

func TestDispatch_TransientRetryAfterMinimumIsHonoured(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	r := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{
		src.ID: {failWith(runner.WithOutcome(errors.New("busy"), runner.SourceOutcome{Class: runner.OutcomeTransient, RetryAfter: 10 * time.Minute})), succeed("ok")},
	}}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.NoError(t, err)
	assert.Equal(t, []time.Duration{10 * time.Minute}, f.clock.slept(), "configured backoff cap never shortens the provider minimum")
}

func TestDispatch_TransientExhaustsAttemptsThenFails(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	busy := runner.WithOutcome(errors.New("overloaded"), runner.SourceOutcome{Class: runner.OutcomeTransient})
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: failWith(busy)}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.Error(t, err)
	assert.ErrorIs(t, err, busy, "the provider error reaches the workflow's errorBehavior")
	assert.Len(t, r.callList(), 3, "TransientMaxAttempts tries")
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, f.clock.slept())
	job := f.pool.jobFor(run)
	assert.Equal(t, runner.JobFailed, job.State)
	assert.Equal(t, "transient", f.pool.attemptsOf(job.ID)[0].Outcome)
}

func TestDispatch_OtherErrorFailsWithoutReplacement(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	boom := errors.New("exit status 2")
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: failWith(boom)}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	assert.ErrorIs(t, err, boom)
	assert.Len(t, r.callList(), 1)
	assert.Empty(t, f.health.outcomes())
	assert.Empty(t, f.bus.ofType("job.rerouted"))
	assert.Equal(t, "other", sink.Facts()[0].Outcome)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
}

func TestDispatch_HealthWriteFailureFailsStep(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: failWith(runner.WithOutcome(errors.New("x"), runner.SourceOutcome{Class: runner.OutcomeExhausted}))}
	f := builtinOnly(t, src, r)
	f.health.err = errDisk
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	assert.ErrorIs(t, err, errDisk)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
}

func TestDispatch_RunnerLostRequeuesWithoutHealthChange(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	r := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{
		src.ID: {failWith(fmt.Errorf("hub: %w", runner.ErrRunnerLost)), succeed("ok")},
	}}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.NoError(t, err)
	job := f.pool.jobFor(run)
	attempts := f.pool.attemptsOf(job.ID)
	require.Len(t, attempts, 2)
	assert.Equal(t, "runner_lost", attempts[0].Outcome)
	assert.Equal(t, "completed", attempts[1].Outcome)
	assert.Empty(t, f.health.outcomes(), "a lost runner says nothing about the source")
}

func TestDispatch_OrphanedActiveJobIsRequeuedAsRunnerLost(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, id, _ := stamped(run, 1)
	key := runner.JobKey{RunID: run, InvocationID: id.InvocationID, OperationID: "node", Attempt: 1}
	orphan, err := f.pool.EnsureIntent(context.Background(), key, "draft", chatReq(run, runner.OpChat, runner.AffinityBuiltIn))
	require.NoError(t, err)
	_, err = f.pool.Reserve(context.Background(), orphan.ID, runner.Decision{Placed: true, Runner: runner.BuiltInRunnerID, Source: src.ID}, 8, placeNow)
	require.NoError(t, err) // left "offered" by a crashed server

	_, err = f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
	require.NoError(t, err)
	attempts := f.pool.attemptsOf(orphan.ID)
	require.Len(t, attempts, 2)
	assert.Equal(t, "runner_lost", attempts[0].Outcome)
	assert.Equal(t, int64(3), attempts[1].Generation)
	assert.Equal(t, runner.JobCompleted, f.pool.jobFor(run).State)
}

func TestDispatch_NinthStepWaitsForSlotThenRuns(t *testing.T) {
	release := make(chan struct{})
	started := make(chan uuid.UUID, 16)
	block := func(ctx context.Context, a runner.Assignment, _ runner.StepJob) (runner.StepOutput, error) {
		started <- a.Job.Key.RunID
		select {
		case <-release:
		case <-ctx.Done():
			return runner.StepOutput{}, ctx.Err()
		}
		return runner.StepOutput{Chat: &engine.ChatResponse{Output: "ok"}}, nil
	}
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: block}
	f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)

	errs := make(chan error, 9)
	dispatch := func(run uuid.UUID) {
		ctx, _, _ := stamped(run, 1)
		go func() {
			_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		dispatch(uuid.New())
	}
	for i := 0; i < 8; i++ {
		<-started
	}
	ninth := uuid.New()
	dispatch(ninth)

	require.Eventually(t, func() bool {
		st := statuses(f.bus, ninth)
		return len(st) > 0 && st[len(st)-1].State == instance.ExecQueuedSlot
	}, 2*time.Second, 5*time.Millisecond, "the ninth concurrent step queues · slot")
	snap, err := f.pool.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 8, snap.SlotsInUse[runner.BuiltInRunnerID], "never more than the limit")
	assert.Len(t, r.callList(), 8)

	release <- struct{}{} // one step finishes and frees its slot
	select {
	case got := <-started:
		assert.Equal(t, ninth, got, "the ninth starts when a slot frees")
	case <-time.After(2 * time.Second):
		t.Fatal("the ninth step never started after a slot freed")
	}
	close(release)
	for i := 0; i < 9; i++ {
		require.NoError(t, <-errs)
	}
}
