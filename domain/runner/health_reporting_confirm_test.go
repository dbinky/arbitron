package runner

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hrConfirmRunner struct{ res *ConfirmationResult }

func (r hrConfirmRunner) Run(context.Context, Assignment, StepJob) (StepOutput, error) {
	return StepOutput{Confirmation: r.res}, nil
}

func TestHealthReporting_ConfirmSuccessRequiresExactEvidence(t *testing.T) {
	src := MakeSourceID("mac-a", CredAnthropicSubscription, "")
	a := Assignment{Job: Job{RunnerID: "mac-a", SourceID: src}}
	job := StepJob{Op: OpConfirm, Binding: provider.ResolvedBinding{Model: "claude-opus-4-1"}}
	for name, res := range map[string]*ConfirmationResult{
		"missing":            nil,
		"different executed": {RequestedModel: "claude-opus-4-1", ExecutedModel: "claude-sonnet-4-5", SessionID: "s"},
	} {
		h := &hrTransitioner{}
		r, ok := NewHealthReportingRunnerSet(StaticRunnerSet{"mac-a": hrConfirmRunner{res: res}}, h).For("mac-a")
		require.True(t, ok)
		_, err := r.Run(context.Background(), a, job)
		assert.ErrorIs(t, err, ErrConfirmationModelMismatch, name)
		assert.Equal(t, OutcomeOther, ClassifyError(err).Class, name)
		assert.Empty(t, h.got[src], "%s: no success is reported", name)
	}
	exact := &ConfirmationResult{RequestedModel: "claude-opus-4-1", ExecutedModel: "claude-opus-4-1", SessionID: "s"}
	h := &hrTransitioner{}
	r, _ := NewHealthReportingRunnerSet(StaticRunnerSet{"mac-a": hrConfirmRunner{res: exact}}, h).For("mac-a")
	out, err := r.Run(context.Background(), a, job)
	require.NoError(t, err)
	assert.Equal(t, exact, out.Confirmation)
	assert.Empty(t, h.got[src], "exact evidence alone is insufficient: the dispatcher must commit and release the slot before recovery")
}
