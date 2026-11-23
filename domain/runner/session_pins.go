package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// SessionPins reads the runner an interactive session is pinned to: the pin
// of the newest session record of (run, node). ok=false means no pin (first
// turn, or a session recorded before Phase 4).
type SessionPins interface {
	PinFor(ctx context.Context, runID uuid.UUID, nodeID string) (RunnerID, bool, error)
}

// ErrSessionPinUnavailable marks an interactive turn whose pinned runner can
// no longer serve the session for a move-trigger reason.
var ErrSessionPinUnavailable = errors.New("runner: the pinned runner can no longer serve the session")

// SessionPinMoveError is returned by Dispatch for a must_move session
// decision. Reason is the move reason the mover accepts.
type SessionPinMoveError struct {
	Runner RunnerID
	Reason string // "exhausted" | "logged_out" | "drain" | "runner_lost"
	Detail string // the placement Decision.Reason
}

func (e *SessionPinMoveError) Error() string {
	return fmt.Sprintf("runner: session pinned to %s must move (%s)", e.Runner, e.Reason)
}

// Is makes errors.Is(err, ErrSessionPinUnavailable) true.
func (e *SessionPinMoveError) Is(target error) bool { return target == ErrSessionPinUnavailable }
