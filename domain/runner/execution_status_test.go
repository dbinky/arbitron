package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// activeLedger serves ListActive from a fixed job list; every other Ledger
// method panics via the nil embedded interface (the projector must not call them).
type activeLedger struct {
	runner.Ledger
	jobs  []runner.Job
	err   error
	calls int
}

func (l *activeLedger) ListActive(context.Context) ([]runner.Job, error) {
	l.calls++
	return l.jobs, l.err
}

var anthropicGroup = runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}

func job(run uuid.UUID, node string, state runner.JobState) runner.Job {
	return runner.Job{ID: uuid.New(), Key: runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1},
		NodeID: node, Op: runner.OpAgent, Group: anthropicGroup, Model: "claude-sonnet-4-5", State: state}
}

func TestExecutionStatusProjector_MapsJobsPerRequestedRun(t *testing.T) {
	runA, runB, hidden := uuid.New(), uuid.New(), uuid.New()
	reset := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)

	capacity := job(runA, "draft", runner.JobQueued)
	capacity.Wait = &runner.WaitReason{Kind: runner.WaitCapacity, Group: anthropicGroup, Model: "claude-sonnet-4-5", NextReset: &reset,
		Detail: "waiting · capacity: no eligible source for anthropic_subscription:anthropic"}
	slot := job(runA, "review", runner.JobQueued)
	slot.Wait = &runner.WaitReason{Kind: runner.WaitSlot, Group: anthropicGroup, Detail: "queued · slot: every eligible runner is at its step limit"}
	running := job(runB, "build", runner.JobRunning)
	running.RunnerID, running.SourceID, running.Reason = runner.BuiltInRunnerID, "builtin/anthropic_subscription/-", "rank: runner=builtin"
	other := job(hidden, "x", runner.JobOffered)

	l := &activeLedger{jobs: []runner.Job{capacity, slot, running, other}}
	p := runner.NewExecutionStatusProjector(l)
	got, err := p.ExecutionStatuses(context.Background(), []uuid.UUID{runA, runB})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls, "one ledger read for the whole page")
	assert.NotContains(t, got, hidden, "only the requested (authorized) runs are projected")

	require.Len(t, got[runA], 2)
	c := got[runA][0]
	assert.Equal(t, instance.ExecWaitingCapacity, c.State)
	assert.Equal(t, "anthropic_subscription:anthropic", c.Group)
	assert.Equal(t, &reset, c.NextReset)
	assert.Contains(t, c.Reason, "waiting · capacity")
	assert.Equal(t, capacity.ID, c.JobID)
	assert.Equal(t, "draft", c.NodeID)
	assert.Equal(t, "node", c.OperationID)

	assert.Equal(t, instance.ExecQueuedSlot, got[runA][1].State)
	assert.Contains(t, got[runA][1].Reason, "queued · slot")

	require.Len(t, got[runB], 1)
	r := got[runB][0]
	assert.Equal(t, instance.ExecRunning, r.State)
	assert.Equal(t, "builtin", r.RunnerID)
	assert.Equal(t, "builtin/anthropic_subscription/-", r.SourceID)
	assert.Equal(t, "rank: runner=builtin", r.Reason)
}

func TestExecutionStatusProjector_ErrorsAndEmpty(t *testing.T) {
	p := runner.NewExecutionStatusProjector(&activeLedger{err: errDisk})
	_, err := p.ExecutionStatuses(context.Background(), []uuid.UUID{uuid.New()})
	assert.ErrorIs(t, err, errDisk)

	l := &activeLedger{}
	got, err := runner.NewExecutionStatusProjector(l).ExecutionStatuses(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, 0, l.calls, "no runs, no read")
	assert.Panics(t, func() { runner.NewExecutionStatusProjector(nil) })
}

func TestExecutionStatusProjector_PublishIsRunScoped(t *testing.T) {
	bus := &recordingBus{}
	p := runner.NewExecutionStatusProjector(&activeLedger{})
	run := uuid.New()
	j := job(run, "draft", runner.JobQueued)
	j.Wait = &runner.WaitReason{Kind: runner.WaitCapacity, Group: anthropicGroup, Detail: "waiting · capacity"}

	assert.NotPanics(t, func() { p.Publish(context.Background(), j, instance.ExecWaitingCapacity) }, "no bus attached yet: a no-op")
	p.AttachBus(bus)
	p.Publish(context.Background(), j, instance.ExecWaitingCapacity)

	pub := bus.ofType("execution.status")
	require.Len(t, pub, 1)
	assert.Equal(t, run, pub[0].InstanceID, "execution.status is ALWAYS run-scoped")
	st, ok := pub[0].Event.Data.(instance.ExecutionStatus)
	require.True(t, ok)
	assert.Equal(t, instance.ExecWaitingCapacity, st.State)
	assert.Equal(t, "anthropic_subscription:anthropic", st.Group)
}
