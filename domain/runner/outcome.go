// domain/runner/outcome.go (cycle 1 stub — replaced in cycle 3)
package runner

import "time"

type OutcomeClass string

type LimitWindow struct {
	LimitID      string
	ModelScope   string
	DurationMins int
	UsedPercent  *float64
	ResetAt      *time.Time
}
