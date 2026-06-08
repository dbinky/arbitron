# Org-Readiness — Phase 3, Task 1: `domain/schedule` entity, repository port, validation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Strict TDD: write each test, watch it fail, then write the minimal implementation to make it pass.

**Goal:** Introduce the pure-domain core of recurring schedules — a `Schedule` entity, a `ScheduleRepository` port, and validation (cron parse, timezone resolve, non-empty workflow name) — plus add `github.com/robfig/cron/v3` to `go.mod`. No persistence, no evaluator, no adapters in this task.

**Architecture:** Hexagonal. `domain/schedule` owns the entity + port + validation and depends only on the stdlib (`time`), `github.com/google/uuid`, and `github.com/robfig/cron/v3` (the approved cron dependency, used here only for parse-validation and next-fire computation). It imports nothing from `adapter/*` and nothing from `domain/engine`/`domain/instance` — those wire in later tasks. Mirrors the shape of other domain packages (`domain/provider`, `domain/notification`): typed entity, sentinel errors, a repository interface, free-function validators with unit tests.

**Tech stack:** Go 1.25.5, `testing` + `testify` (`assert`/`require`), `github.com/robfig/cron/v3`.

---

## Dependencies

- **None within Phase 3.** This is the foundational task. Tasks 2 (persistence), 3 (evaluator), and 4 (management surface) all depend on the entity, port, and validators defined here.

## Spec / design references

- Spec `docs/specs/org-readiness-spec.md` (Phase 3) — "Recurring schedules use cron expressions with a timezone (defaulting to UTC)"; "Any workflow type can be scheduled"; "scheduled run is owned by and attributed to the schedule's creator (carrying the creator's identity and department)".
- Design `docs/superpowers/specs/org-readiness-design-phase-3.md` §"Domain — `domain/schedule`" (entity fields, port methods, validation rules) and §"Dependency" (robfig/cron/v3, parser/schedule types only).

## The cron dependency

- [ ] Add `github.com/robfig/cron/v3` to `go.mod`:
  ```
  go get github.com/robfig/cron/v3@v3.0.1
  go mod tidy
  ```
  Then `go mod verify`. Pure-Go, Apache-2.0, no transitive cgo. Only `cron.ParseStandard` (5-field standard cron) and the returned `cron.Schedule.Next(t time.Time) time.Time` are used in this feature; we do not use the package's own `cron.Cron` runner (the evaluator loop in Task 3 is our own so it integrates with the queue + lifecycle).
- [ ] Confirm `go.mod`/`go.sum` updated and `go build ./...` still succeeds before writing any schedule code.

## Files

- **Create:** `domain/schedule/schedule.go`
  - Package doc comment describing the package as the recurring-trigger domain core, importing nothing from adapters/engine.
  - `Schedule` entity struct:
    ```go
    type Schedule struct {
        ID           uuid.UUID
        WorkflowName string
        CronExpr     string            // standard 5-field cron, validated via robfig/cron
        Timezone     string            // IANA name; "" normalizes to "UTC"
        CreatorID    uuid.UUID         // the user the run is attributed to (owner + department source)
        Department   string            // department override to stamp the run under ("" => publication default)
        Enabled      bool
        Variables    map[string]any    // inputs passed to the workflow at fire time
        LastFiredAt  *time.Time        // nil until first fire; drives no-backfill recovery
        CreatedAt    time.Time
        UpdatedAt    time.Time
    }
    ```
  - Sentinel errors (mirroring `provider.ErrConnectionExists`/`ErrConnectionNotFound`, `notification.ErrNotFound`):
    ```go
    var ErrNotFound = errors.New("schedule not found")
    var ErrExists   = errors.New("schedule already exists")
    var ErrInvalidCron     = errors.New("schedule: invalid cron expression")
    var ErrInvalidTimezone = errors.New("schedule: invalid timezone")
    var ErrEmptyWorkflow   = errors.New("schedule: workflow name must not be empty")
    ```
- **Create:** `domain/schedule/validation.go`
  - `const DefaultTimezone = "UTC"`.
  - `func NormalizeTimezone(tz string) string` — returns `DefaultTimezone` when `tz == ""` (after `strings.TrimSpace`), else the trimmed value. Pure; does NOT resolve.
  - `func Validate(s Schedule) error` — the single create/update gate:
    - `strings.TrimSpace(s.WorkflowName) == ""` ⇒ `ErrEmptyWorkflow`.
    - `cron.ParseStandard(s.CronExpr)` returns error ⇒ wrap `%w` over `ErrInvalidCron` (include the offending expr in the message, NOT a secret).
    - `time.LoadLocation(NormalizeTimezone(s.Timezone))` returns error ⇒ wrap `%w` over `ErrInvalidTimezone`.
    - returns `nil` when all pass.
  - `func NextFire(s Schedule, after time.Time) (time.Time, error)` — the deterministic next-occurrence helper reused by the evaluator (Task 3) and tested here in isolation:
    - `loc, err := time.LoadLocation(NormalizeTimezone(s.Timezone))`; on error wrap `ErrInvalidTimezone`.
    - `sched, err := cron.ParseStandard(s.CronExpr)`; on error wrap `ErrInvalidCron`.
    - `return sched.Next(after.In(loc)), nil` — `robfig/cron`'s `Next` is timezone-aware (it advances in `after`'s location), so passing `after.In(loc)` yields calendar-aligned, DST-correct next-fire times.
- **Create:** `domain/schedule/ports.go`
  - `ScheduleRepository` interface (matches the design's method set):
    ```go
    type ScheduleRepository interface {
        Create(ctx context.Context, s *Schedule) error
        Get(ctx context.Context, id uuid.UUID) (*Schedule, error)
        List(ctx context.Context, filters ListFilters) ([]Schedule, error)
        ListEnabled(ctx context.Context) ([]Schedule, error)
        Update(ctx context.Context, s *Schedule) error
        Delete(ctx context.Context, id uuid.UUID) error
        SetLastFired(ctx context.Context, id uuid.UUID, at time.Time) error
    }
    ```
  - `ListFilters` for the management surface (mirrors `notification.ListFilters` / `instance.ListFilters` nil=admin convention):
    ```go
    type ListFilters struct {
        // CreatorID, when non-nil, restricts results to schedules owned by that
        // user. nil => admin (no owner restriction). SECURITY: set by the
        // management service from the request identity (non-admin => own UserID);
        // REST/MCP handlers MUST NOT set it from request input.
        CreatorID    *uuid.UUID
        WorkflowName *string
        Enabled      *bool
    }
    ```
- **Test:** `domain/schedule/validation_test.go`, `domain/schedule/schedule_test.go` (package `schedule_test`).

## TDD test list (five categories)

Package `schedule_test`. No clock injection needed in this task — `NextFire` takes the reference time as a parameter, which IS the determinism seam reused by Task 3's injected clock. Use fixed `time.Date(...)` instants.

### Happy-path

- [ ] `TestValidate_AcceptsStandardCronAndUTC` — `Validate(Schedule{WorkflowName:"report", CronExpr:"0 9 * * 1-5", Timezone:"UTC"})` ⇒ `require.NoError`.
- [ ] `TestValidate_EmptyTimezoneDefaultsToUTC` — same but `Timezone:""` ⇒ `require.NoError`; assert `NormalizeTimezone("") == "UTC"`.
- [ ] `TestNextFire_WeekdaysNineAM_UTC` — `NextFire(Schedule{CronExpr:"0 9 * * 1-5", Timezone:"UTC"}, time.Date(2026,6,8,8,0,0,0,time.UTC))` (Monday 08:00) ⇒ `2026-06-08 09:00:00 UTC`. (`require.NoError`, `assert.Equal`.)

### Input-validation

- [ ] `TestValidate_RejectsEmptyWorkflowName` — `WorkflowName:"   "` ⇒ `assert.ErrorIs(err, ErrEmptyWorkflow)`.
- [ ] `TestValidate_RejectsInvalidCron` — `CronExpr:"not a cron"` (and a second case `"99 99 * * *"`) ⇒ `assert.ErrorIs(err, ErrInvalidCron)`; `assert.Contains(err.Error(), "not a cron")`.
- [ ] `TestValidate_RejectsInvalidTimezone` — `Timezone:"Mars/Phobos"` ⇒ `assert.ErrorIs(err, ErrInvalidTimezone)`.

### Error-handling

- [ ] `TestNextFire_InvalidCronReturnsError` — `assert.ErrorIs(err, ErrInvalidCron)` and the returned time is zero.
- [ ] `TestNextFire_InvalidTimezoneReturnsError` — `assert.ErrorIs(err, ErrInvalidTimezone)`.

### Edge-cases / boundary

- [ ] `TestNextFire_TimezoneAware_AmericaNewYork` — `NextFire(Schedule{CronExpr:"0 9 * * 1-5", Timezone:"America/New_York"}, time.Date(2026,6,8,12,0,0,0,time.UTC))` ⇒ a `time.Time` whose value in `America/New_York` is `09:00` the next weekday and whose UTC offset is `-04:00` (EDT). Assert `next.In(loc).Hour()==9` and the next-day boundary, proving the schedule fires at 09:00 *local*, not 09:00 UTC.
- [ ] `TestNextFire_DSTSpringForward` — pick a US spring-forward date (2nd Sunday of March; for 2027 that is `2027-03-14`). With `CronExpr:"30 2 * * *"` (02:30, a wall-clock time that does NOT exist on spring-forward day) in `America/New_York`, assert `NextFire` returns a valid, monotonic next instant (robfig/cron skips/normalizes the non-existent local time rather than erroring) — `require.NoError`, `assert.True(next.After(after))`. Document the observed behavior in a comment so the contract is pinned.
- [ ] `TestNextFire_DSTFallBack` — a fall-back date (1st Sunday of November) with `CronExpr:"30 1 * * *"` (01:30 occurs twice): assert exactly one valid next instant is returned and it is `After(after)` — pins the duplicate-hour contract.
- [ ] `TestNormalizeTimezone_TrimsWhitespace` — `NormalizeTimezone("  America/New_York  ") == "America/New_York"`.

### Integration (port shape)

- [ ] `TestScheduleRepository_PortShape` — a small in-test fake `type fakeRepo struct{ schedule.ScheduleRepository }` plus a `var _ schedule.ScheduleRepository = (*...)(nil)` compile-time assertion proves the interface is satisfiable and the method set matches the design (CRUD + `ListEnabled` + `SetLastFired`). This locks the port signature that Tasks 2–4 depend on.

## Implementation steps (after each test goes red)

1. [ ] Add `robfig/cron/v3` to `go.mod`; `go build ./...` green.
2. [ ] Write `schedule.go` (entity + sentinel errors) to satisfy the entity/port-shape tests.
3. [ ] Write `validation.go` `NormalizeTimezone` to pass the normalize tests.
4. [ ] Write `Validate` to pass the validation tests (workflow → cron → tz order).
5. [ ] Write `NextFire` to pass the next-fire / timezone / DST tests.
6. [ ] Write `ports.go` (`ScheduleRepository`, `ListFilters`) to pass the port-shape test.
7. [ ] `go test ./domain/schedule/...` green; `make test` (or `go test ./...`) stays green (purely additive package).

## Acceptance criteria (what `go test ./...` proves)

- `Validate` rejects empty workflow names, unparseable cron, and unknown timezones, each with a distinct sentinel error (maps cleanly to HTTP 400 in Task 4).
- `NextFire` computes the correct next occurrence in the schedule's IANA timezone, including DST spring-forward and fall-back boundaries — the determinism guarantee the spec's "starts at the correct time in the schedule's timezone" acceptance depends on.
- An empty timezone normalizes to UTC (spec default).
- `ScheduleRepository` compiles as a satisfiable port with the full CRUD + `ListEnabled` + `SetLastFired` method set.
- `github.com/robfig/cron/v3` is present in `go.mod`/`go.sum` and the build is green.

## Notes

- **Determinism seam:** `NextFire(s, after)` takes the reference instant as a parameter — this is the single seam the evaluator (Task 3) drives from an injected clock, so no `time.Now()` is called inside this package. Persisting `LastFiredAt` and recomputing `NextFire(s, lastFired)` on restart is what makes scheduling position deterministic across restarts (no in-memory-only authoritative state), per the design's "persistence of position".
- **Why robfig/cron only for parse + Next:** keeping our own loop (Task 3) means cancellation, the injected clock, collision/misfire policy, and lifecycle all live in our code and integrate with the existing queue; the library contributes only correct, DST-aware calendar math.
