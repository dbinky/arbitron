package engine

import (
	"errors"
	"fmt"
)

// ErrSessionMoveRequired marks a classified interruption that requires moving
// the session and resending, rather than retrying on the same pin.
var ErrSessionMoveRequired = errors.New("engine: interactive session must move to another runner")

// ErrRecoveryBudgetExhausted yields this pass while keeping the accepted intent pending.
var ErrRecoveryBudgetExhausted = errors.New("engine: session recovery budget for this pass is exhausted")

// SessionMoveRequiredError retains the interrupted runner, source and cause.
type SessionMoveRequiredError struct {
	Reason string
	From   string
	Source string
	Err    error
}

func (e *SessionMoveRequiredError) Error() string {
	return fmt.Sprintf("session must move from runner %s (%s): %v", e.From, e.Reason, e.Err)
}

func (e *SessionMoveRequiredError) Unwrap() error { return e.Err }

func (e *SessionMoveRequiredError) Is(target error) bool { return target == ErrSessionMoveRequired }
