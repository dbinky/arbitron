package runner_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pullFailed(msg string) error {
	return runner.WithOutcome(errors.New(msg), runner.SourceOutcome{Class: runner.OutcomePullFailed})
}

func rejected(code, detail string) error {
	return runner.WithOutcome(fmt.Errorf("%w: %s: %s", runner.ErrJobRejected, code, detail), runner.SourceOutcome{Class: runner.OutcomePreparationFailed})
}

// A pull failure excludes that runner for this operation and re-places,
// without spending a workflow retry or touching source health.
func TestDispatch_PullFailureReplacesWithRunnerIneligible(t *testing.T) {
	f := newWDFixture(t, "mac-1")
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	_, err := f.homes.HandOff(context.Background(), f.run, "mac-1", "seed", 0, f.home(t).Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	f.runners["mac-1"].set(nil, pullFailed("pull: manifest unreachable"))
	f.runners[builtin].set(map[string]string{"a.txt": "v2"}, nil)

	d := f.dispatch(t)
	require.NoError(t, d.err)
	assert.Equal(t, 1, f.runners["mac-1"].count())
	assert.Equal(t, 1, f.runners[builtin].count())
	assert.Equal(t, string(builtin), d.out.Completion.JobFence.RunnerID)
	job := f.jobOf(t, d)
	attempts, err := f.pool.Ledger.Attempts(context.Background(), job.ID)
	require.NoError(t, err)
	var outcomes []string
	for _, at := range attempts {
		outcomes = append(outcomes, at.Outcome)
	}
	assert.Contains(t, outcomes, string(runner.OutcomePullFailed))
	assert.Empty(t, f.pool.Health.Calls(), "a pull failure is never source exhaustion")
	rerouted := f.pool.EventsOf(t, runner.EventJobRerouted)
	require.Len(t, rerouted, 1)
	assert.Equal(t, runner.OutcomePullFailed, rerouted[0].Cause.Outcome)
	assert.Equal(t, "mac-1", rerouted[0].Detail["excludedRunner"])
}

// Bounded: MaxPullFailures pull failures fail the step; no tight loop.
func TestDispatch_RepeatedPullFailuresFailTheStep(t *testing.T) {
	f := newWDFixture(t, "mac-1", "mac-2", "mac-3")
	f.offline(t, builtin)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	for _, id := range []runner.RunnerID{"mac-1", "mac-2", "mac-3"} {
		f.runners[id].set(nil, pullFailed("pull: checksum mismatch"))
	}

	d := f.dispatch(t)
	require.Error(t, d.err)
	assert.Equal(t, runner.OutcomePullFailed, runner.ClassifyError(d.err).Class)
	total := 0
	for _, id := range []runner.RunnerID{"mac-1", "mac-2", "mac-3"} {
		assert.LessOrEqual(t, f.runners[id].count(), 1, "an excluded runner is never offered again")
		total += f.runners[id].count()
	}
	assert.Equal(t, runner.MaxPullFailures, total)
	assert.Equal(t, runner.JobFailed, f.jobOf(t, d).State)
	_, ok := f.active(t)
	assert.False(t, ok)
	assert.Empty(t, f.pool.Health.Calls())
}

// Preparation reject: the incompatible runner is excluded once; the step runs
// once on a capable runner.
func TestDispatch_MissingInterpreterReplacesOnceOnCapableRunner(t *testing.T) {
	f := newWDFixture(t, "mac-1", "mac-2")
	f.offline(t, builtin)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners["mac-1"].set(nil, rejected(runner.RejectCommandUnavailable, "python3"))
	f.runners["mac-2"].set(map[string]string{"out.txt": "ran on mac-2"}, nil)

	d := f.dispatch(t)
	require.NoError(t, d.err)
	assert.Equal(t, 1, f.runners["mac-1"].count())
	assert.Equal(t, 1, f.runners["mac-2"].count())
	assert.Equal(t, "mac-2", d.out.Completion.JobFence.RunnerID)
	ref, err := runner.ParseCheckpointRef(d.out.Completion.Workspace.Ref)
	require.NoError(t, err)
	assert.Equal(t, "ran on mac-2", readIn(t, f.restoreTo(t, ref), "out.txt"))
	assert.Empty(t, f.pool.Health.Calls())
}

// RR-09: when every remote rejects command_unavailable, the built-in runner
// runs the step exactly once; the step is never parked or failed by rejects.
func TestDispatch_MissingCommandOnAllRemotesFallsBackToBuiltIn(t *testing.T) {
	f := newWDFixture(t, "mac-1", "mac-2")
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	other := f.pool.SeedRun(t) // the built-in runner is busier, so the remotes rank first
	f.pool.ReserveJob(t, other, builtin, hubtest.BuiltInAnthropic().ID, runner.OpChat)
	f.pool.ReserveJob(t, other, builtin, hubtest.BuiltInAnthropic().ID, runner.OpChat)
	f.runners["mac-1"].set(nil, rejected(runner.RejectCommandUnavailable, "jq"))
	f.runners["mac-2"].set(nil, rejected(runner.RejectCommandUnavailable, "jq"))
	f.runners[builtin].set(map[string]string{"jq.txt": "built-in ran it"}, nil)

	d := f.dispatch(t)
	require.NoError(t, d.err)
	assert.Equal(t, 1, f.runners["mac-1"].count())
	assert.Equal(t, 1, f.runners["mac-2"].count())
	assert.Equal(t, 1, f.runners[builtin].count())
	assert.Equal(t, string(builtin), d.out.Completion.JobFence.RunnerID)
	for _, st := range statuses(f.bus, f.run) {
		assert.NotEqual(t, instance.ExecWaitingCapacity, st.State, "a capability reject never parks the step")
	}
}

// Other reject codes with no remaining candidate: a visible wait, and the
// incompatible runner is never offered again.
func TestDispatch_PreparationRejectWithNoCandidateWaitsVisibly(t *testing.T) {
	f := newWDFixture(t, "mac-1")
	f.offline(t, builtin)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.runners["mac-1"].set(nil, rejected(runner.RejectWorkspacePreparationFailed, "disk full"))

	ctx, cancel := context.WithCancel(context.Background())
	sctx, _, _ := f.stepCtx(t, ctx, true)
	done := make(chan error, 1)
	go func() {
		_, err := f.d.Dispatch(sctx, f.req(), *f.job())
		done <- err
	}()
	require.Eventually(t, func() bool {
		for _, st := range statuses(f.bus, f.run) {
			if st.State == instance.ExecWaitingCapacity {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not return after cancellation")
	}
	assert.Equal(t, 1, f.runners["mac-1"].count())
}
