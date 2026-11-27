// domain/runner/recovery_notice_test.go
package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ engine.RecoveryNoticeSource = (*runner.RecoveryNoticeSource)(nil)

func TestRecoveryNoticeSource_OneNoticePerLostAttempt(t *testing.T) {
	ctx := context.Background()
	clock := newP3Clock()
	l := newP3Ledger(clock.Now)
	run := uuid.New()
	j := l.put(runner.Job{Key: runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}, NodeID: "edit", RunnerID: "mac-1", Generation: 1, State: runner.JobRunning})
	_, err := l.Requeue(ctx, runner.Fence{JobID: j.ID, RunnerID: "mac-1", Generation: 1}, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	_, err = l.Reserve(ctx, j.ID, runner.Decision{Runner: "mac-2"}, 4, clock.Now())
	require.NoError(t, err)
	require.NoError(t, l.Complete(ctx, runner.Fence{JobID: j.ID, RunnerID: "mac-2", Generation: 2}, "r"))
	other := l.put(runner.Job{Key: runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}, NodeID: "ok", RunnerID: "mac-1", Generation: 1, State: runner.JobRunning})
	require.NoError(t, l.Complete(ctx, runner.Fence{JobID: other.ID, RunnerID: "mac-1", Generation: 1}, "r"))
	l.put(runner.Job{Key: runner.JobKey{RunID: uuid.New(), InvocationID: uuid.New(), OperationID: "node", Attempt: 1}, NodeID: "x", RunnerID: "mac-1", Generation: 1, State: runner.JobRunning})

	n, err := runner.NewRecoveryNoticeSource(l).RecoveryNotices(ctx, run)
	require.NoError(t, err)
	require.Len(t, n, 1, "only the attempt that ended runner_lost, only for this run")
	assert.Equal(t, "edit", n[0].NodeID)
	assert.Equal(t, j.ID, n[0].JobID)
	assert.Equal(t, "mac-1", n[0].RunnerID)
	assert.Equal(t, string(runner.OutcomeRunnerLost), n[0].Reason)
	assert.Equal(t, instance.RecoveryNoticeText, n[0].Text)
}
