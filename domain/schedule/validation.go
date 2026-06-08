package schedule

import (
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
)

// DefaultTimezone is applied when a schedule's timezone is empty (spec default).
const DefaultTimezone = "UTC"

// NormalizeTimezone returns DefaultTimezone when tz is empty (after trimming),
// else the trimmed value. It is pure and does NOT resolve the zone.
func NormalizeTimezone(tz string) string {
	trimmed := strings.TrimSpace(tz)
	if trimmed == "" {
		return DefaultTimezone
	}
	return trimmed
}

// Validate is the single create/update gate. It checks, in order: a non-empty
// workflow name, a parseable standard cron expression, and a resolvable IANA
// timezone. Each failure wraps a distinct sentinel.
func Validate(s Schedule) error {
	if strings.TrimSpace(s.WorkflowName) == "" {
		return ErrEmptyWorkflow
	}
	if _, err := cron.ParseStandard(s.CronExpr); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidCron, s.CronExpr, err)
	}
	if _, err := time.LoadLocation(NormalizeTimezone(s.Timezone)); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidTimezone, NormalizeTimezone(s.Timezone), err)
	}
	return nil
}

// NextFire computes the next occurrence of s strictly after the reference
// instant, in the schedule's IANA timezone. The reference instant is a
// parameter (not time.Now) so the evaluator can drive it from an injected
// clock; this is the determinism seam. robfig/cron's Next advances in after's
// location, so passing after.In(loc) yields calendar-aligned, DST-correct
// next-fire times.
func NextFire(s Schedule, after time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(NormalizeTimezone(s.Timezone))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q: %v", ErrInvalidTimezone, NormalizeTimezone(s.Timezone), err)
	}
	sched, err := cron.ParseStandard(s.CronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q: %v", ErrInvalidCron, s.CronExpr, err)
	}
	return sched.Next(after.In(loc)), nil
}
