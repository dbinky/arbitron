package runner

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
)

// OutcomeClass is the classified result of a dispatched operation against a
// capacity source.
type OutcomeClass string

const (
	OutcomeNone       OutcomeClass = ""
	OutcomeExhausted  OutcomeClass = "exhausted"
	OutcomeLoggedOut  OutcomeClass = "logged_out"
	OutcomeTransient  OutcomeClass = "transient"
	OutcomeRunnerLost OutcomeClass = "runner_lost"
	OutcomeOther      OutcomeClass = "other"
)

// LimitWindow is one provider rate-limit window (keyed by limit id + duration;
// same-duration per-model buckets are separate windows).
type LimitWindow struct {
	LimitID      string
	ModelScope   string
	DurationMins int
	UsedPercent  *float64
	ResetAt      *time.Time
}

// SourceOutcome is a structured classification attached by an adapter.
type SourceOutcome struct {
	Model        string // exact requested model stamped by dispatcher, never inferred from error prose
	Class        OutcomeClass
	ResetAt      *time.Time
	RetryAfter   time.Duration
	Windows      []LimitWindow
	HeadroomHint *float64
}

// OutcomeError carries a SourceOutcome alongside the adapter error. It adds no
// text: Error() is the wrapped error's text, so sanitizing/redaction above it
// is unchanged.
type OutcomeError struct {
	Outcome SourceOutcome
	Err     error
}

func (e *OutcomeError) Error() string {
	if e.Err == nil {
		return "runner: " + string(e.Outcome.Class) + " outcome"
	}
	return e.Err.Error()
}

func (e *OutcomeError) Unwrap() error { return e.Err }

// WithOutcome attaches o to err. A nil err stays nil.
func WithOutcome(err error, o SourceOutcome) error {
	if err == nil {
		return nil
	}
	return &OutcomeError{Outcome: o, Err: err}
}

// ErrRunnerLost marks an operation whose runner disappeared mid-flight.
var ErrRunnerLost = errors.New("runner: runner lost")

// ClassifyError maps an error to an outcome using STRUCTURE only (typed
// errors and sentinels), never message text: unknown shapes fail closed to
// OutcomeOther, which can never park a source.
func ClassifyError(err error) SourceOutcome {
	return classifyErrorAt(err, time.Now())
}

func classifyErrorAt(err error, now time.Time) SourceOutcome {
	if err == nil {
		return SourceOutcome{Class: OutcomeNone}
	}
	var oe *OutcomeError
	if errors.As(err, &oe) && oe.Outcome.Class != OutcomeNone {
		return oe.Outcome
	}
	if errors.Is(err, provider.ErrLunaRouteBusy) {
		o := SourceOutcome{Class: OutcomeTransient}
		var lf *provider.LunaRouteFailure
		if errors.As(err, &lf) {
			o.RetryAfter = parseRetryAfter(lf.RetryAfter, now)
		}
		return o
	}
	if errors.Is(err, ErrRunnerLost) {
		return SourceOutcome{Class: OutcomeRunnerLost}
	}
	return SourceOutcome{Class: OutcomeOther}
}

// parseRetryAfter reads the provider's normalized Retry-After: delta-seconds
// digits or an HTTP date. Anything else, or a past date, yields 0.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseUint(v, 10, 32); err == nil {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
