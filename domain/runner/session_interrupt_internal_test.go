package runner

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatch_MustMovePinIsEngineSessionMoveRequired(t *testing.T) {
	err := sessionPinMoveRequired(&SessionPinMoveError{Runner: "mac-a", Reason: engine.MoveReasonExhausted, Detail: "must_move: pinned runner=mac-a reason=exhausted"})
	var mv *engine.SessionMoveRequiredError
	require.True(t, errors.As(err, &mv), "the engine recovery loops only detect this type")
	assert.Equal(t, engine.MoveReasonExhausted, mv.Reason)
	assert.Equal(t, "mac-a", mv.From)
	assert.ErrorIs(t, err, engine.ErrSessionMoveRequired)
	assert.ErrorIs(t, err, ErrSessionPinUnavailable)
	var pin *SessionPinMoveError
	require.True(t, errors.As(err, &pin))
	assert.Equal(t, RunnerID("mac-a"), pin.Runner)
}
