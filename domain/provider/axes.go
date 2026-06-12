package provider

import "errors"

// ContextWindow is the per-profile/per-node context-window axis. It selects how
// large a context window a backend/model request opts into: ContextDefault emits
// nothing (the backend/model's own default), while the non-default tiers pin a
// standard window or opt into a metered extended one. The capability ruleset
// decides which tiers a given backend/model actually accepts.
type ContextWindow string

const (
	// ContextDefault emits nothing; the backend/model picks its own default.
	ContextDefault ContextWindow = "default"
	// Context200K pins the standard 200K window.
	Context200K ContextWindow = "200k"
	// Context1M opts into the metered extended 1M window.
	Context1M ContextWindow = "1m"
)

// ErrInvalidContextWindow is returned when a string is not a known
// ContextWindow value. Callers match it with errors.Is.
var ErrInvalidContextWindow = errors.New("provider: invalid context window")

// Valid reports whether c is one of the defined ContextWindow values.
func (c ContextWindow) Valid() bool {
	switch c {
	case ContextDefault, Context200K, Context1M:
		return true
	}
	return false
}

// ParseContextWindow parses a string into a ContextWindow. An empty string
// normalizes to ContextDefault (the "unset" value); any other unknown value
// returns ErrInvalidContextWindow.
func ParseContextWindow(s string) (ContextWindow, error) {
	if s == "" {
		return ContextDefault, nil
	}
	c := ContextWindow(s)
	if !c.Valid() {
		return "", ErrInvalidContextWindow
	}
	return c, nil
}

// Effort is the per-profile/per-node reasoning-effort axis. EffortDefault emits
// nothing (backend/model default) and is outside the graded order; EffortNone
// explicitly disables reasoning. The remaining levels are graded
// low < medium < high < xhigh < max and are floored to a backend/model's
// ceiling by the capability ruleset when a requested level is unsupported.
type Effort string

const (
	// EffortDefault emits nothing; it is outside the graded order.
	EffortDefault Effort = "default"
	// EffortNone explicitly disables reasoning/thinking.
	EffortNone Effort = "none"
	// EffortLow is the lowest graded effort level.
	EffortLow Effort = "low"
	// EffortMedium is the middle graded effort level.
	EffortMedium Effort = "medium"
	// EffortHigh is a high graded effort level.
	EffortHigh Effort = "high"
	// EffortXHigh is an extra-high graded effort level.
	EffortXHigh Effort = "xhigh"
	// EffortMax is the maximum graded effort level.
	EffortMax Effort = "max"
)

// ErrInvalidEffort is returned when a string is not a known Effort value.
// Callers match it with errors.Is.
var ErrInvalidEffort = errors.New("provider: invalid effort")

// effortRank orders the graded levels for floor logic. EffortDefault is unranked
// (rank 0) because it sits outside the graded order; the graded order is
// none < low < medium < high < xhigh < max.
var effortRank = map[Effort]int{
	EffortNone:   1,
	EffortLow:    2,
	EffortMedium: 3,
	EffortHigh:   4,
	EffortXHigh:  5,
	EffortMax:    6,
}

// Valid reports whether e is one of the defined Effort values.
func (e Effort) Valid() bool {
	switch e {
	case EffortDefault, EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	}
	return false
}

// GreaterThan compares graded effort levels. EffortDefault is outside the order,
// so it is never greater than anything and nothing is greater than it.
func (e Effort) GreaterThan(o Effort) bool {
	if e == EffortDefault || o == EffortDefault {
		return false
	}
	return effortRank[e] > effortRank[o]
}

// ParseEffort parses a string into an Effort. An empty string normalizes to
// EffortDefault (the "unset" value); any other unknown value returns
// ErrInvalidEffort.
func ParseEffort(s string) (Effort, error) {
	if s == "" {
		return EffortDefault, nil
	}
	e := Effort(s)
	if !e.Valid() {
		return "", ErrInvalidEffort
	}
	return e, nil
}
