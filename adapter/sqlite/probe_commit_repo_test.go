package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeJob reserves a running job bound to f.src. "probe/confirm" is the
// synthetic confirmation P1 T13's DispatchConfirmer dispatches (RequiredSource
// set); any other operation id is an ordinary workflow operation.
func probeJob(t *testing.T, f ledgerFixture, opID string) (runner.Job, runner.Fence) {
	t.Helper()
	ctx := context.Background()
	run := uuid.New()
	req := anthropicRequirements(run)
	if opID == "probe/confirm" {
		req.RequiredSource = f.src
		req.RequireExecutedModel = true
	}
	j, err := f.ledger.EnsureIntent(ctx, runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: opID, Attempt: 1}, "probe:"+string(f.src), req)
	require.NoError(t, err)
	j, err = f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)
	fence := runner.Fence{JobID: j.ID, RunnerID: j.RunnerID, Generation: j.Generation}
	require.NoError(t, f.ledger.MarkRunning(ctx, fence))
	return j, fence
}

func probeFence(f runner.Fence) engine.JobFenceRef {
	return engine.JobFenceRef{JobID: f.JobID, RunnerID: string(f.RunnerID), Generation: f.Generation}
}

func TestProbeCommit_ExactModelValidatedBeforeRelease(t *testing.T) {
	ctx := context.Background()
	f := newLedgerFixture(t)
	repo := NewProbeCommitRepo(f.db.DB)
	j, fence := probeJob(t, f, "probe/confirm")
	require.Equal(t, 1, f.slots(t))

	wrongModel := runner.ProbeCompletion{RequestedModel: j.Model, ExecutedModel: "claude-haiku-4-5", Validated: true}
	require.ErrorIs(t, repo.CommitProbe(ctx, probeFence(fence), wrongModel), runner.ErrInvalidProbeCompletion)
	otherRequest := runner.ProbeCompletion{RequestedModel: "claude-opus-4-5", ExecutedModel: "claude-opus-4-5", Validated: true}
	require.ErrorIs(t, repo.CommitProbe(ctx, probeFence(fence), otherRequest), runner.ErrInvalidProbeCompletion,
		"the evidence must name the job's own requested model")
	got, err := f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobRunning, got.State, "rejected evidence completes nothing")
	assert.Equal(t, 1, f.slots(t))

	ok := runner.ProbeCompletion{RequestedModel: j.Model, ExecutedModel: j.Model, Validated: true}
	stale := probeFence(fence)
	stale.Generation++
	require.ErrorIs(t, repo.CommitProbe(ctx, stale, ok), runner.ErrFenced)

	require.NoError(t, repo.CommitProbe(ctx, probeFence(fence), ok))
	got, err = f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobCompleted, got.State)
	assert.Equal(t, "probe:"+j.Model, got.ResultRef, "the durable probe result names the executed model")
	assert.Equal(t, 0, f.slots(t), "the confirmation released its slot")
	attempts, err := f.ledger.Attempts(ctx, j.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	assert.Equal(t, "completed", attempts[0].Outcome)
	require.NoError(t, repo.CommitProbe(ctx, probeFence(fence), ok), "an identical repeat is idempotent")

	node, nodeFence := probeJob(t, f, "node")
	require.ErrorIs(t, repo.CommitProbe(ctx, probeFence(nodeFence), runner.ProbeCompletion{RequestedModel: node.Model, ExecutedModel: node.Model, Validated: true}),
		runner.ErrInvalidProbeCompletion, "ordinary operations cannot use the probe exception")
	got, err = f.ledger.Get(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobRunning, got.State)
}

func TestProbeCommit_FailureDoesNotLeakSlot(t *testing.T) {
	ctx := context.Background()
	f := newLedgerFixture(t)
	repo := NewProbeCommitRepo(f.db.DB)
	j, fence := probeJob(t, f, "probe/confirm")

	unvalidated := runner.ProbeCompletion{RequestedModel: j.Model, ExecutedModel: j.Model, Validated: false}
	require.ErrorIs(t, repo.CommitProbe(ctx, probeFence(fence), unvalidated), runner.ErrInvalidProbeCompletion)
	got, err := f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.NotEqual(t, runner.JobCompleted, got.State, "malformed evidence is never a success")

	require.NoError(t, f.ledger.Fail(ctx, fence, runner.OutcomeOther)) // the ordinary failed-job branch
	assert.Equal(t, 0, f.slots(t), "the failed probe holds no slot")
	got, err = f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobFailed, got.State)
	require.ErrorIs(t, repo.CommitProbe(ctx, probeFence(fence), runner.ProbeCompletion{RequestedModel: j.Model, ExecutedModel: j.Model, Validated: true}),
		runner.ErrFenced, "a failed probe can never turn into a success")
}
