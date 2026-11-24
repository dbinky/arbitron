package runner_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestJobHolder_NamesTheDurableOperation(t *testing.T) {
	job := runner.Job{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Key: runner.JobKey{
		RunID: uuid.New(), InvocationID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), OperationID: "node-A", Attempt: 3}}
	assert.Equal(t, "job:11111111-1111-1111-1111-111111111111/inv:22222222-2222-2222-2222-222222222222/op:node-A/attempt:3", runner.JobHolder(job))
	retry := job
	retry.Key.Attempt = 4
	assert.NotEqual(t, runner.JobHolder(job), runner.JobHolder(retry), "a workflow retry is a different holder")
}

func TestResourceLease_VocabularyIsDistinct(t *testing.T) {
	for _, k := range []runner.SupersedeKind{runner.SupersedeRunnerLost, runner.SupersedeRevoked, runner.SupersedeStopped, runner.SupersedeServerRestart} {
		assert.NotEmpty(t, string(k))
	}
	assert.Equal(t, "runner_lost", string(runner.SupersedeRunnerLost))
	assert.Equal(t, "revoked", string(runner.SupersedeRevoked))
	assert.Equal(t, "stopped", string(runner.SupersedeStopped))
	assert.Equal(t, "server_restart", string(runner.SupersedeServerRestart))
	assert.NotErrorIs(t, runner.ErrResourceBusy, runner.ErrResourceFenced)
	assert.NotErrorIs(t, runner.ErrSupersedeUnproven, runner.ErrResourceFenced)
	assert.NotErrorIs(t, runner.ErrResourceFenced, runner.ErrFenced, "a resource fence is not the job fence")
}
