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

func collect(t *testing.T, ch <-chan engine.ChatChunk) []engine.ChatChunk {
	t.Helper()
	var out []engine.ChatChunk
	timeout := time.After(2 * time.Second)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
		case <-timeout:
			t.Fatal("stream never closed")
		}
	}
}

func exhaustedErr() error {
	return runner.WithOutcome(errors.New("usage limit"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
}

func twoRunnerStreamFixture(t *testing.T, builtinScript, macScript []runStep) (*dispatchFixture, runner.Source, runner.Source, *scriptedRunner, *scriptedRunner) {
	bSrc := headroomSource(runner.BuiltInRunnerID, f64(0.9))
	mSrc := headroomSource("mac-mini", f64(0.5))
	b := &scriptedRunner{id: runner.BuiltInRunnerID, script: map[runner.SourceID][]runStep{bSrc.ID: builtinScript}}
	m := &scriptedRunner{id: "mac-mini", script: map[runner.SourceID][]runStep{mSrc.ID: macScript}}
	f := newDispatchFixture(t, []runner.Runner{online(runner.BuiltInRunnerID), online("mac-mini")}, []runner.Source{bSrc, mSrc},
		runner.StaticRunnerSet{runner.BuiltInRunnerID: b, "mac-mini": m})
	return f, bSrc, mSrc, b, m
}

func TestDispatchStream_ExhaustedBeforeFirstTokenReplacesTransparently(t *testing.T) {
	f, bSrc, _, b, m := twoRunnerStreamFixture(t,
		[]runStep{streamOf(engine.ChatChunk{Error: exhaustedErr(), Done: true})},
		[]runStep{streamOf(engine.ChatChunk{Token: "hel"}, engine.ChatChunk{Token: "lo"}, engine.ChatChunk{Done: true, TokensOut: 2})})
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityNone), chatJob(runner.OpStream))
	require.NoError(t, err)
	chunks := collect(t, out.Stream)

	var text string
	for _, c := range chunks {
		assert.NoError(t, c.Error, "the caller never sees the pre-token exhaustion")
		text += c.Token
	}
	assert.Equal(t, "hello", text)
	assert.True(t, chunks[len(chunks)-1].Done)
	assert.Len(t, b.callList(), 1)
	assert.Len(t, m.callList(), 1)
	assert.Equal(t, runner.CapacityExhausted, f.pool.health(bSrc.ID).Capacity)

	facts := sink.Facts()
	require.Len(t, facts, 2, "facts are complete when the terminal chunk arrives")
	assert.Equal(t, "exhausted", facts[0].Outcome)
	assert.Equal(t, "completed", facts[1].Outcome)
	assert.Equal(t, runner.JobCompleted, f.pool.jobFor(run).State)
}

func TestDispatchStream_ErrorAfterTokenIsForwardedAndApplied(t *testing.T) {
	f, bSrc, _, _, m := twoRunnerStreamFixture(t,
		[]runStep{streamOf(engine.ChatChunk{Token: "partial"}, engine.ChatChunk{Error: exhaustedErr(), Done: true})}, nil)
	run := uuid.New()
	ctx, _, sink := stamped(run, 1)
	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityNone), chatJob(runner.OpStream))
	require.NoError(t, err)
	chunks := collect(t, out.Stream)
	require.Len(t, chunks, 2)
	assert.Equal(t, "partial", chunks[0].Token)
	require.Error(t, chunks[1].Error)
	var oe *runner.OutcomeError
	assert.True(t, errors.As(chunks[1].Error, &oe), "the classified error reaches the caller")

	assert.Empty(t, m.callList(), "no transparent re-placement once tokens were forwarded (Phase 4 adds turn restart)")
	assert.Equal(t, runner.CapacityExhausted, f.pool.health(bSrc.ID).Capacity, "the outcome still parks the source")
	assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
	assert.Equal(t, "exhausted", sink.Facts()[0].Outcome)
}

func TestDispatchStream_HoldsSlotUntilTerminalChunk(t *testing.T) {
	gate := make(chan struct{})
	src := anthropicSub(runner.BuiltInRunnerID)
	r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: func(ctx context.Context, _ runner.Assignment, _ runner.StepJob) (runner.StepOutput, error) {
		ch := make(chan engine.ChatChunk)
		go func() {
			defer close(ch)
			ch <- engine.ChatChunk{Token: "a"}
			<-gate
			ch <- engine.ChatChunk{Done: true}
		}()
		return runner.StepOutput{Stream: ch}, nil
	}}
	f := builtinOnly(t, src, r)
	run := uuid.New()
	ctx, _, _ := stamped(run, 1)
	out, err := f.d.Dispatch(ctx, chatReq(run, runner.OpStream, runner.AffinityBuiltIn), chatJob(runner.OpStream))
	require.NoError(t, err)
	first := <-out.Stream
	assert.Equal(t, "a", first.Token)
	snap, err := f.pool.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, snap.SlotsInUse[runner.BuiltInRunnerID], "the slot is held while the stream is open")
	close(gate)
	collect(t, out.Stream)
	snap, err = f.pool.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, snap.SlotsInUse[runner.BuiltInRunnerID])
}
