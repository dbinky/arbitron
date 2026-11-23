package claudecli

import (
	"errors"
	pool "github.com/arbitron/arbitron/domain/runner"
)

// SanitizeProcessError hides untrusted subprocess diagnostics before they reach
// verified-call errors or logs. It preserves errors.Is classification (including
// cancellation and response limits). As exposes only a redacted outcome;
// callers cannot recover a credential-bearing StderrError tail.
func SanitizeProcessError(err error) error {
	if err == nil {
		return nil
	}
	return &sanitizedProcessError{cause: err}
}

type sanitizedProcessError struct{ cause error }

func (*sanitizedProcessError) Error() string { return "harness process failed" }

func (e *sanitizedProcessError) Is(target error) bool { return errors.Is(e.cause, target) }

func (e *sanitizedProcessError) As(target any) bool {
	t, ok := target.(**pool.OutcomeError)
	if !ok {
		return false
	}
	var oe *pool.OutcomeError
	if !errors.As(e.cause, &oe) {
		return false
	}
	*t = &pool.OutcomeError{Outcome: oe.Outcome, Err: e}
	return true
}
