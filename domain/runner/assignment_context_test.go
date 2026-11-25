package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssignmentContext_RoundTripsTheExactAssignment(t *testing.T) {
	id := uuid.New()
	a := runner.Assignment{
		Job:   runner.Job{ID: id, RunnerID: runner.BuiltInRunnerID, Generation: 3, State: runner.JobRunning},
		Fence: runner.Fence{JobID: id, RunnerID: runner.BuiltInRunnerID, Generation: 3},
	}
	derived, cancel := context.WithCancel(runner.ContextWithAssignment(context.Background(), a))
	defer cancel()

	got, ok := runner.AssignmentFromContext(derived)
	require.True(t, ok, "derived contexts keep the stamp")
	assert.Equal(t, a, got)
}

func TestAssignmentContext_MissingOrIncompleteIsAbsent(t *testing.T) {
	id := uuid.New()
	stamp := func(a runner.Assignment) context.Context {
		return runner.ContextWithAssignment(context.Background(), a)
	}
	cases := map[string]context.Context{
		"never stamped":   context.Background(),
		"zero assignment": stamp(runner.Assignment{}),
		"no runner":       stamp(runner.Assignment{Fence: runner.Fence{JobID: id, Generation: 1}}),
		"generation zero": stamp(runner.Assignment{Fence: runner.Fence{JobID: id, RunnerID: runner.BuiltInRunnerID}}),
		"job and fence disagree": stamp(runner.Assignment{
			Job:   runner.Job{ID: uuid.New()},
			Fence: runner.Fence{JobID: id, RunnerID: runner.BuiltInRunnerID, Generation: 1},
		}),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok := runner.AssignmentFromContext(ctx)
			assert.False(t, ok, "a supervised launch must fail closed rather than guess a job identity")
		})
	}
}
