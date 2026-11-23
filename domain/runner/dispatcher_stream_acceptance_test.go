package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type completionFailure struct{ runner.Ledger }

func (completionFailure) Complete(context.Context, runner.Fence, string) error { return errDisk }

func TestDispatchStream_ClosedBeforeDoneFails(t *testing.T) {
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: streamOf(engine.ChatChunk{Token: "partial"})}
	f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityBuiltIn), chatJob(runner.OpStream))
	require.NoError(t, err)
	chunks := collect(t, out.Stream)
	require.NotEmpty(t, chunks)
	assert.Error(t, chunks[len(chunks)-1].Error)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
	require.Len(t, sink.Facts(), 1)
	assert.Equal(t, "other", sink.Facts()[0].Outcome)
}
func TestDispatchStream_CompletionWriteErrorIsTerminalError(t *testing.T) {
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: streamOf(engine.ChatChunk{Done: true})}
	f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)
	d := runner.NewStepDispatcher(runner.DispatcherDeps{Registry: f.pool, Ledger: completionFailure{f.pool}, Runners: runner.StaticRunnerSet{runner.BuiltInRunnerID: r}, Events: runner.NewEventRecorder(f.log, f.bus, 1000, f.clock.Now), Status: runner.NewExecutionStatusProjector(f.pool), Health: f.health, Clock: f.clock.Now, TransientMaxAttempts: 3, BackoffMin: time.Second, BackoffMax: 30 * time.Second})
	defer d.Close()
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	out, err := d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityBuiltIn), chatJob(runner.OpStream))
	require.NoError(t, err)
	chunks := collect(t, out.Stream)
	require.Len(t, chunks, 1)
	assert.ErrorIs(t, chunks[0].Error, errDisk)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State, "failed completion releases the slot")
	for _, fact := range sink.Facts() {
		assert.NotEqual(t, "completed", fact.Outcome)
	}
}
func TestDispatchStream_CancelReleasesSlot(t *testing.T) {
	in := make(chan engine.ChatChunk)
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: func(context.Context, runner.Assignment, runner.StepJob) (runner.StepOutput, error) {
		return runner.StepOutput{Stream: in}, nil
	}}
	f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)
	run := uuid.New()
	base, _, sink := stamped(run, 1)
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityBuiltIn), chatJob(runner.OpStream))
	require.NoError(t, err)
	assert.Equal(t, runner.JobRunning, f.pool.jobFor(run).State)
	cancel()
	close(in)
	collect(t, out.Stream)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
	require.Len(t, sink.Facts(), 1)
	snap, err := f.pool.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Zero(t, snap.SlotsInUse[runner.BuiltInRunnerID])
}
func TestDispatchStream_TransientBeforeTokenBacksOff(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	busy := runner.WithOutcome(errors.New("busy"), runner.SourceOutcome{Class: runner.OutcomeTransient, RetryAfter: 10 * time.Minute})
	r := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{src.ID: {streamOf(engine.ChatChunk{Error: busy, Done: true}), streamOf(engine.ChatChunk{Token: "ok"}, engine.ChatChunk{Done: true})}}}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityBuiltIn), chatJob(runner.OpStream))
	require.NoError(t, err)
	chunks := collect(t, out.Stream)
	require.Len(t, chunks, 2)
	assert.Equal(t, "ok", chunks[0].Token)
	assert.NoError(t, chunks[1].Error)
	calls := r.callList()
	require.Len(t, calls, 2)
	assert.Equal(t, calls[0].a.Fence, calls[1].a.Fence)
	assert.Equal(t, []time.Duration{10 * time.Minute}, f.clock.slept())
	assert.Empty(t, f.health.outcomes())
	require.Len(t, sink.Facts(), 2)
	assert.Equal(t, "transient", sink.Facts()[0].Outcome)
	assert.Equal(t, "completed", sink.Facts()[1].Outcome)
}

func TestDispatchStream_TransientBudgetIncludesInitialCallErrors(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	busy := runner.WithOutcome(errors.New("busy"), runner.SourceOutcome{Class: runner.OutcomeTransient})
	r := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{src.ID: {failWith(busy), streamOf(engine.ChatChunk{Error: busy, Done: true}), streamOf(engine.ChatChunk{Error: busy, Done: true}), streamOf(engine.ChatChunk{Token: "too late"}, engine.ChatChunk{Done: true})}}}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityBuiltIn), chatJob(runner.OpStream))
	require.NoError(t, err)
	chunks := collect(t, out.Stream)
	require.Len(t, chunks, 1)
	assert.ErrorIs(t, chunks[0].Error, busy)
	assert.Len(t, r.callList(), 3)
	assert.Len(t, f.clock.slept(), 2)
	assert.Len(t, sink.Facts(), 3)
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
}
