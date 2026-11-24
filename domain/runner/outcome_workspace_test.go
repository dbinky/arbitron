package runner_test

import (
	"errors"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestPreparationRejectCode_OnlyAllowlistedCodes(t *testing.T) {
	for _, ok := range []string{runner.RejectCommandUnavailable, runner.RejectCapabilityUnavailable, runner.RejectWorkspacePreparationFailed} {
		assert.True(t, runner.PreparationRejectCode(ok), ok)
	}
	assert.Equal(t, []string{"command_unavailable", "capability_unavailable", "workspace_preparation_failed"},
		[]string{runner.RejectCommandUnavailable, runner.RejectCapabilityUnavailable, runner.RejectWorkspacePreparationFailed})
	for _, bad := range []string{"", "exhausted", "logged_out", "command_unavailable: jq", "COMMAND_UNAVAILABLE", "made_up"} {
		assert.False(t, runner.PreparationRejectCode(bad), bad)
	}
}

func TestClassifyError_WorkspaceOutcomesPassThrough(t *testing.T) {
	for _, c := range []runner.OutcomeClass{runner.OutcomePullFailed, runner.OutcomePreparationFailed} {
		err := runner.WithOutcome(errors.New("x"), runner.SourceOutcome{Class: c})
		assert.Equal(t, c, runner.ClassifyError(err).Class)
	}
	assert.Equal(t, runner.OutcomeClass("pull_failed"), runner.OutcomePullFailed)
	assert.Equal(t, runner.OutcomeClass("preparation_failed"), runner.OutcomePreparationFailed)
	assert.Equal(t, runner.OutcomeOther, runner.ClassifyError(runner.WithOutcome(errors.New("x"), runner.SourceOutcome{Class: "made_up"})).Class)
}
