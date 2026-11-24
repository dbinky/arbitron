package runner

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
)

func TestConfirmation_OutputShapeRequiresExactEvidence(t *testing.T) {
	assert.Equal(t, OpType("confirm"), OpConfirm)
	exact := &ConfirmationResult{RequestedModel: "m", ExecutedModel: "m", SessionID: "s"}
	assert.True(t, validOutput(OpConfirm, StepOutput{Confirmation: exact}))
	assert.False(t, validOutput(OpConfirm, StepOutput{}))
	assert.False(t, validOutput(OpConfirm, StepOutput{Chat: &engine.ChatResponse{}}))
	assert.False(t, validOutput(OpConfirm, StepOutput{Confirmation: exact, Chat: &engine.ChatResponse{}}))
	assert.False(t, validOutput(OpChat, StepOutput{Confirmation: exact}))

	assert.NoError(t, VerifyConfirmation("m", exact))
	for name, res := range map[string]*ConfirmationResult{
		"nil":               nil,
		"requested differs": {RequestedModel: "x", ExecutedModel: "m", SessionID: "s"},
		"executed differs":  {RequestedModel: "m", ExecutedModel: "x", SessionID: "s"},
		"no session":        {RequestedModel: "m", ExecutedModel: "m"},
	} {
		assert.ErrorIs(t, VerifyConfirmation("m", res), ErrConfirmationModelMismatch, name)
	}
	assert.ErrorIs(t, VerifyConfirmation("", exact), ErrConfirmationModelMismatch, "no requested model is never proof")
}
