package claudecli

import "errors"

// SanitizeProcessError hides untrusted subprocess diagnostics before they reach
// verified-call errors or logs. It preserves errors.Is classification (including
// cancellation and response limits), but deliberately offers no Unwrap or As:
// callers must not recover and log a credential-bearing StderrError tail.
func SanitizeProcessError(err error) error {
	if err == nil {
		return nil
	}
	return &sanitizedProcessError{cause: err}
}

type sanitizedProcessError struct{ cause error }

func (*sanitizedProcessError) Error() string { return "harness process failed" }

func (e *sanitizedProcessError) Is(target error) bool { return errors.Is(e.cause, target) }
