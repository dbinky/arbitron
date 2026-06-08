package schedule_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Happy-path ---

func TestValidate_AcceptsStandardCronAndUTC(t *testing.T) {
	err := schedule.Validate(schedule.Schedule{
		WorkflowName: "report",
		CronExpr:     "0 9 * * 1-5",
		Timezone:     "UTC",
	})
	require.NoError(t, err)
}

func TestValidate_EmptyTimezoneDefaultsToUTC(t *testing.T) {
	err := schedule.Validate(schedule.Schedule{
		WorkflowName: "report",
		CronExpr:     "0 9 * * 1-5",
		Timezone:     "",
	})
	require.NoError(t, err)
	assert.Equal(t, "UTC", schedule.NormalizeTimezone(""))
}

func TestNextFire_WeekdaysNineAM_UTC(t *testing.T) {
	// Monday 2026-06-08 08:00 UTC.
	after := time.Date(2026, 6, 8, 8, 0, 0, 0, time.UTC)
	next, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "0 9 * * 1-5",
		Timezone: "UTC",
	}, after)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC), next.UTC())
}

// --- Input-validation ---

func TestValidate_RejectsEmptyWorkflowName(t *testing.T) {
	err := schedule.Validate(schedule.Schedule{
		WorkflowName: "   ",
		CronExpr:     "0 9 * * 1-5",
		Timezone:     "UTC",
	})
	assert.ErrorIs(t, err, schedule.ErrEmptyWorkflow)
}

func TestValidate_RejectsInvalidCron(t *testing.T) {
	err := schedule.Validate(schedule.Schedule{
		WorkflowName: "report",
		CronExpr:     "not a cron",
		Timezone:     "UTC",
	})
	assert.ErrorIs(t, err, schedule.ErrInvalidCron)
	assert.Contains(t, err.Error(), "not a cron")

	err2 := schedule.Validate(schedule.Schedule{
		WorkflowName: "report",
		CronExpr:     "99 99 * * *",
		Timezone:     "UTC",
	})
	assert.ErrorIs(t, err2, schedule.ErrInvalidCron)
}

func TestValidate_RejectsInvalidTimezone(t *testing.T) {
	err := schedule.Validate(schedule.Schedule{
		WorkflowName: "report",
		CronExpr:     "0 9 * * 1-5",
		Timezone:     "Mars/Phobos",
	})
	assert.ErrorIs(t, err, schedule.ErrInvalidTimezone)
}

// --- Error-handling ---

func TestNextFire_InvalidCronReturnsError(t *testing.T) {
	next, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "not a cron",
		Timezone: "UTC",
	}, time.Now())
	assert.ErrorIs(t, err, schedule.ErrInvalidCron)
	assert.True(t, next.IsZero())
}

func TestNextFire_InvalidTimezoneReturnsError(t *testing.T) {
	next, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "0 9 * * 1-5",
		Timezone: "Mars/Phobos",
	}, time.Now())
	assert.ErrorIs(t, err, schedule.ErrInvalidTimezone)
	assert.True(t, next.IsZero())
}

// --- Edge-cases / boundary ---

func TestNextFire_TimezoneAware_AmericaNewYork(t *testing.T) {
	// 2026-06-08 12:00 UTC is 08:00 EDT (Monday). Next weekday 09:00 local is
	// the same day at 09:00 EDT == 13:00 UTC.
	after := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	next, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "0 9 * * 1-5",
		Timezone: "America/New_York",
	}, after)
	require.NoError(t, err)

	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	local := next.In(loc)
	assert.Equal(t, 9, local.Hour())
	assert.Equal(t, 0, local.Minute())
	// EDT offset is -04:00 in June.
	_, offset := next.In(loc).Zone()
	assert.Equal(t, -4*3600, offset, "expected EDT (-04:00) in June")
}

func TestNextFire_DSTSpringForward(t *testing.T) {
	// US spring-forward 2027 is 2027-03-14; 02:30 local does not exist that day.
	// robfig/cron normalizes the non-existent wall-clock time rather than
	// erroring; we only pin that a valid, monotonic next instant is returned.
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	after := time.Date(2027, 3, 14, 0, 0, 0, 0, loc)
	next, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "30 2 * * *",
		Timezone: "America/New_York",
	}, after)
	require.NoError(t, err)
	assert.True(t, next.After(after), "next-fire must advance past the spring-forward boundary")
}

func TestNextFire_DSTFallBack(t *testing.T) {
	// US fall-back 2026 is 2026-11-01; 01:30 occurs twice. Exactly one valid
	// next instant must be returned, after the reference instant.
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	after := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	next, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "30 1 * * *",
		Timezone: "America/New_York",
	}, after)
	require.NoError(t, err)
	assert.True(t, next.After(after))
}

func TestNormalizeTimezone_TrimsWhitespace(t *testing.T) {
	assert.Equal(t, "America/New_York", schedule.NormalizeTimezone("  America/New_York  "))
}

// edge — all-whitespace collapses to empty after trim, so NormalizeTimezone
// returns DefaultTimezone rather than forwarding a blank string to LoadLocation.
func TestNormalizeTimezone_AllWhitespaceIsUTC(t *testing.T) {
	assert.Equal(t, "UTC", schedule.NormalizeTimezone("   "))
}

// edge — NextFire with an empty Timezone field normalises to UTC, identical
// to an explicit "UTC" timezone.
func TestNextFire_EmptyTimezoneNormalizesToUTC(t *testing.T) {
	after := time.Date(2026, 6, 8, 8, 0, 0, 0, time.UTC)
	withUTC, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "0 9 * * 1-5",
		Timezone: "UTC",
	}, after)
	require.NoError(t, err)

	withEmpty, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "0 9 * * 1-5",
		Timezone: "",
	}, after)
	require.NoError(t, err)
	assert.Equal(t, withUTC, withEmpty, "empty timezone must behave identically to UTC")
}

// edge — NextFire is strictly after the reference instant; passing a reference
// that falls exactly on a cron tick must advance to the next occurrence, not
// return the reference itself. This pins the determinism guarantee the evaluator
// relies on (no double-fire when LastFiredAt == tick instant).
func TestNextFire_ExactlyAtTick_AdvancesToNext(t *testing.T) {
	// Tick instant: Monday 2026-06-08 09:00 UTC.
	tick := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	next, err := schedule.NextFire(schedule.Schedule{
		CronExpr: "0 9 * * 1-5",
		Timezone: "UTC",
	}, tick)
	require.NoError(t, err)
	assert.True(t, next.After(tick), "NextFire must be strictly after the reference instant")
	// Verify the exact next occurrence: Tuesday 2026-06-09 09:00 UTC.
	assert.Equal(t, time.Date(2026, 6, 9, 9, 0, 0, 0, time.UTC), next.UTC())
}
