package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatch_MismatchedRunOrOperationRejected(t *testing.T) {
	for _, mismatch := range []string{"run", "op"} {
		t.Run(mismatch, func(t *testing.T) {
			r := &scriptedRunner{id: runner.BuiltInRunnerID}
			f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)
			ctx, _, _ := stamped(uuid.New(), 1)
			req := chatReq(uuid.Nil, runner.OpChat, runner.AffinityBuiltIn)
			if mismatch == "run" {
				req.RunID = uuid.New()
			} else {
				req.Op = runner.OpAgent
			}
			_, err := f.d.Dispatch(ctx, req, chatJob(runner.OpChat))
			require.Error(t, err)
			assert.Zero(t, f.pool.jobCount())
			assert.Empty(t, r.callList())
		})
	}
}

func TestDispatch_MalformedOutputFailsAndReleases(t *testing.T) {
	for _, output := range []runner.StepOutput{{}, {Stream: make(chan engine.ChatChunk)}, {Chat: &engine.ChatResponse{}, Agent: &engine.AgentResult{}}} {
		r := &scriptedRunner{id: runner.BuiltInRunnerID, fallback: func(context.Context, runner.Assignment, runner.StepJob) (runner.StepOutput, error) {
			return output, nil
		}}
		f := builtinOnly(t, anthropicSub(runner.BuiltInRunnerID), r)
		run := uuid.New()
		ctx, _, sink := stamped(run, 1)
		_, err := f.d.Dispatch(ctx, chatReq(run, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
		require.Error(t, err)
		assert.Equal(t, runner.JobFailed, f.pool.jobFor(run).State)
		require.Len(t, sink.Facts(), 1)
		assert.Equal(t, "other", sink.Facts()[0].Outcome)
	}
}

func TestDispatch_OneNotificationWakesAllCapacityWaiters(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	src.Health.Capacity = runner.CapacityExhausted
	r := &scriptedRunner{id: runner.BuiltInRunnerID}
	f := builtinOnly(t, src, r)
	done := make(chan error, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 3; i++ {
		base, _, _ := stamped(uuid.New(), 1)
		c, stop := context.WithCancel(base)
		defer stop()
		go func() {
			select {
			case <-ctx.Done():
				stop()
			case <-c.Done():
			}
		}()
		go func() {
			_, err := f.d.Dispatch(c, chatReq(uuid.Nil, runner.OpChat, runner.AffinityBuiltIn), chatJob(runner.OpChat))
			done <- err
		}()
	}
	require.Eventually(t, func() bool { return len(f.bus.ofType("execution.status")) == 3 }, time.Second, time.Millisecond)
	require.NoError(t, f.pool.RecordHealth(ctx, src.ID, runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}))
	f.notifier.Notify()
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("broadcast missed a waiter")
		}
	}
	assert.Len(t, r.callList(), 3)
}
