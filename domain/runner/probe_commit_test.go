package runner_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/require"
)

func TestProbeCompletion_RequiresExactValidatedModel(t *testing.T) {
	ok := runner.ProbeCompletion{RequestedModel: "claude-sonnet-4-5", ExecutedModel: "claude-sonnet-4-5", Validated: true}
	require.NoError(t, ok.Validate())
	for name, p := range map[string]runner.ProbeCompletion{
		"not validated":            {RequestedModel: "claude-sonnet-4-5", ExecutedModel: "claude-sonnet-4-5"},
		"different executed model": {RequestedModel: "claude-sonnet-4-5", ExecutedModel: "claude-haiku-4-5", Validated: true},
		"no requested model":       {Validated: true},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, p.Validate(), runner.ErrInvalidProbeCompletion)
		})
	}
}
