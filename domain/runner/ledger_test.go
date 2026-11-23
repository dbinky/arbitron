package runner_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestJobState_ActiveAndHoldsSlot(t *testing.T) {
	cases := []struct {
		s              runner.JobState
		active, holds bool
	}{
		{runner.JobQueued, true, false},
		{runner.JobOffered, true, true},
		{runner.JobRunning, true, true},
		{runner.JobCompleted, false, false},
		{runner.JobFailed, false, false},
		{runner.JobFenced, false, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.active, c.s.Active(), string(c.s))
		assert.Equal(t, c.holds, c.s.HoldsSlot(), string(c.s))
	}
}

func TestJobKey_ValidAndFence(t *testing.T) {
	k := runner.JobKey{RunID: uuid.New(), InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
	assert.True(t, k.Valid())
	for _, bad := range []runner.JobKey{
		{InvocationID: k.InvocationID, OperationID: "node", Attempt: 1},
		{RunID: k.RunID, OperationID: "node", Attempt: 1},
		{RunID: k.RunID, InvocationID: k.InvocationID, Attempt: 1},
		{RunID: k.RunID, InvocationID: k.InvocationID, OperationID: "node"},
	} {
		assert.False(t, bad.Valid())
	}
	j := runner.Job{ID: uuid.New(), RunnerID: "builtin", Generation: 3}
	assert.Equal(t, runner.Fence{JobID: j.ID, RunnerID: "builtin", Generation: 3}, j.Fence())
}

func TestLedgerErrorsAreDistinct(t *testing.T) {
	assert.NotErrorIs(t, runner.ErrSlotTaken, runner.ErrFenced)
	assert.NotErrorIs(t, runner.ErrFenced, runner.ErrJobNotFound)
}
