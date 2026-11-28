package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
)

// ErrSessionMoveRequired is shared by the dispatcher and its engine caller.
var ErrSessionMoveRequired = engine.ErrSessionMoveRequired

func sessionMoveReason(c OutcomeClass) (string, bool) {
	switch c {
	case OutcomeExhausted:
		return engine.MoveReasonExhausted, true
	case OutcomeLoggedOut:
		return engine.MoveReasonLoggedOut, true
	case OutcomeRunnerLost:
		return engine.MoveReasonRunnerLost, true
	}
	return "", false
}

// NewSessionMoveRequired preserves the classified cause for the session owner.
func NewSessionMoveRequired(a Assignment, o SourceOutcome, cause error) error {
	reason, _ := sessionMoveReason(o.Class)
	return &engine.SessionMoveRequiredError{Reason: reason, From: string(a.Job.RunnerID), Source: string(a.Job.SourceID), Err: WithOutcome(cause, o)}
}

func sessionPinMoveRequired(e *SessionPinMoveError) error {
	return &engine.SessionMoveRequiredError{Reason: e.Reason, From: string(e.Runner), Err: e}
}

// interruptSession fences the interrupted attempt rather than silently
// replacing a session's pin. Transient and non-session failures keep their
// existing retry policy.
func interruptSession(ctx context.Context, health HealthTransitioner, ledger Ledger, req ExecRequirements, a Assignment, o SourceOutcome, cause error) (error, bool) {
	if req.Affinity != AffinitySession {
		return nil, false
	}
	if _, ok := sessionMoveReason(o.Class); !ok {
		return nil, false
	}
	// Revoke ownership even if persisting the source outcome fails.
	if err := ledger.Fail(ctx, a.Fence, o.Class); err != nil && !errors.Is(err, ErrFenced) {
		return fmt.Errorf("dispatch: fence interrupted session attempt: %w", err), true
	}
	if o.Class == OutcomeExhausted || o.Class == OutcomeLoggedOut {
		if err := health.ApplyOutcome(ctx, a.Job.SourceID, o); err != nil {
			return fmt.Errorf("dispatch: apply %s to %s: %w", o.Class, a.Job.SourceID, err), true
		}
	}
	return NewSessionMoveRequired(a, o, cause), true
}
