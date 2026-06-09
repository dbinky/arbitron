# Schedule "Run Now" — Phase 1, Task 1: Domain Core (Runner + Service.RunNow)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an on-demand "run now" fire path to the `schedule` domain core — a shared `Runner.Fire` core reused by the cron evaluator, plus `Service.RunNow` with owner-or-admin authz — without advancing the cron position.

**Architecture:** Extract the identity-resolve + trigger core that `Evaluator.fireOne` already performs into a new `schedule.Runner` collaborator (`domain/schedule/runner.go`). `Evaluator.fireOne` is refactored to call the same shared core so there is exactly one identity-resolve+trigger implementation. `Service` gains an optional `Firer` dependency (nil when unwired) and a `RunNow` method gated by the existing `caller()`/`authorize()` (owner-or-admin) path. The domain core imports only stdlib, `github.com/google/uuid`, and sibling domain packages (`identity`, `notification`, `workflow`) — no adapter/infra imports.

**Tech Stack:** Go, `github.com/google/uuid`, `github.com/stretchr/testify` (assert/require), `domain/identity` context injection.

**Scope note:** This task is the domain core only. Task 2 (`final-org-gaps-implementation-phase-1-task-2.md`) wires the REST handler, MCP tool, and composition root over what this task builds. Do Task 1 first; it must compile and be green before Task 2.

**Phase-ordering note (do NOT skip):** `instance.Origin` / `instance.WithOrigin` / `instance.OriginRunNow` do **not** exist in the repo yet — they are introduced in Phase 3. This task must **not** import them or require them to exist. `Runner.Fire` leaves a clearly-marked comment seam where the origin injection will go in Phase 3. Verify before starting:

```bash
grep -rn "WithOrigin\|OriginRunNow\|type Origin" ~/Repos/arbitron/domain/instance/ || echo "instance.Origin absent (expected — leave the seam as a comment)"
```

---

## Background: existing code this task touches

- `domain/schedule/schedule.go` — sentinels (`ErrNotFound`, validation errors) and the `Schedule` entity (fields: `ID`, `WorkflowName`, `CronExpr`, `Timezone`, `CreatorID`, `Department`, `Enabled`, `Variables map[string]any`, `LastFiredAt *time.Time`, `CreatedAt`, `UpdatedAt`).
- `domain/schedule/evaluator.go` — defines the ports `Trigger` (`TriggerWithDepartment(ctx, workflowName string, variables map[string]any, priority, department string) (uuid.UUID, bool, error)`), `ActiveRunChecker` (`HasActiveRun(ctx, workflowName string, ownerID uuid.UUID) (bool, error)`), `IdentityResolver` (`IdentityForUser(ctx, userID uuid.UUID) (identity.Identity, error)`). `Evaluator.fireOne(ctx, s Schedule, fireTime time.Time)` resolves identity, injects it with `identity.WithIdentity(ctx, id)`, calls `trigger.TriggerWithDepartment(fireCtx, s.WorkflowName, s.Variables, "normal", s.Department)`, then does cron-only work (alerts on creator-gone via `notification.TypeScheduleMissed`, alerts + `SetLastFired` on deleted-workflow `workflow.ErrNotFound`, and `SetLastFired` on success).
- `domain/schedule/service.go` — `Service` struct (`repo ScheduleRepository`, `canTrigger CanTrigger`, `now func() time.Time`, `bounds Bounds`); `ServiceOption func(*Service)`; `NewService(repo, canTrigger, opts...)`; `caller(ctx) (identity.Identity, error)` (returns `ErrForbidden` when no identity); `authorize(c identity.Identity, s Schedule) bool` = `c.IsAdmin() || s.CreatorID == c.UserID`; existing sentinel `ErrForbidden`.
- `domain/identity/identity.go` — `Identity{UserID uuid.UUID, Username string, Role}`, `(Identity).IsAdmin() bool`, `WithIdentity(ctx, id) context.Context`, `FromContext(ctx) (Identity, bool)`, `RoleUser`, `RoleAdmin`.

**Files:**
- Create: `domain/schedule/runner.go`
- Create: `domain/schedule/runner_test.go`
- Modify: `domain/schedule/evaluator.go` (refactor `fireOne` to call the shared core; **no behavior change**)
- Modify: `domain/schedule/service.go` (add `Firer`, `WithFirer`, `RunNowInput`, `RunNowResult`, `RunNow`, new sentinels `ErrRunNowUnavailable` and `ErrCreatorUnresolved`, and an exported `HasFirer()` predicate for adapter gating)
- Modify: `domain/schedule/service_test.go` (add `RunNow` scenarios) — alternatively a new `service_runnow_test.go`; this plan uses a new file `domain/schedule/service_runnow_test.go` to avoid clashing with existing fixtures.
- Create: `domain/schedule/service_runnow_test.go`

---

### Task 1: Domain core for run-now

- [ ] **Step 1: Write the failing Runner test**

Create `domain/schedule/runner_test.go`. These fakes are local to this file (the existing `evaluator_test.go` fixtures live in the same `schedule_test` package, so name these distinctly to avoid redeclaration — `runnerTrigger`, `runnerIdents`, `runnerActive`).

```go
package schedule_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- runner fakes ---

type runnerTrigger struct {
	id       uuid.UUID
	created  bool
	err      error
	calls    int
	gotWF    string
	gotVars  map[string]any
	gotPrio  string
	gotDept  string
	gotIdent identity.Identity // identity read from the injected context
}

func (t *runnerTrigger) TriggerWithDepartment(ctx context.Context, wf string, vars map[string]any, prio, dept string) (uuid.UUID, bool, error) {
	t.calls++
	t.gotWF, t.gotVars, t.gotPrio, t.gotDept = wf, vars, prio, dept
	t.gotIdent, _ = identity.FromContext(ctx)
	return t.id, t.created, t.err
}

type runnerIdents struct {
	id  identity.Identity
	err error
}

func (r runnerIdents) IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error) {
	return r.id, r.err
}

type runnerActive struct {
	busy bool
	err  error
}

func (a runnerActive) HasActiveRun(ctx context.Context, wf string, owner uuid.UUID) (bool, error) {
	return a.busy, a.err
}

func runnerSchedule(creator uuid.UUID) schedule.Schedule {
	return schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "wf-report",
		CreatorID:    creator,
		Department:   "eng",
		Variables:    map[string]any{"k": "v"},
	}
}

// Happy path: Fire resolves the creator identity, injects it, and triggers the
// workflow with the schedule's variables/department. fired=true, instance id
// returned, LastFiredAt is NEVER touched (Runner has no repo).
func TestRunner_Fire_HappyPath(t *testing.T) {
	creator := uuid.New()
	wantInst := uuid.New()
	trig := &runnerTrigger{id: wantInst, created: true}
	idents := runnerIdents{id: identity.Identity{UserID: creator, Username: "creator", Role: identity.RoleUser}}
	r := schedule.NewRunner(trig, idents, runnerActive{})

	s := runnerSchedule(creator)
	inst, fired, err := r.Fire(context.Background(), s, false)

	require.NoError(t, err)
	assert.True(t, fired)
	assert.Equal(t, wantInst, inst)
	assert.Equal(t, 1, trig.calls)
	assert.Equal(t, "wf-report", trig.gotWF)
	assert.Equal(t, map[string]any{"k": "v"}, trig.gotVars)
	assert.Equal(t, "normal", trig.gotPrio)
	assert.Equal(t, "eng", trig.gotDept)
	assert.Equal(t, creator, trig.gotIdent.UserID, "trigger must run as the resolved creator identity")
}

// Collision, skip-when-busy=true + active run => skipped: fired=false, no
// trigger call, no error, nil instance id.
func TestRunner_Fire_SkipWhenBusy_Active(t *testing.T) {
	creator := uuid.New()
	trig := &runnerTrigger{id: uuid.New(), created: true}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{busy: true})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), true)

	require.NoError(t, err)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
	assert.Equal(t, 0, trig.calls, "no run must be started when busy and skipWhenBusy")
}

// Collision, skip-when-busy=true + NOT active => fires normally.
func TestRunner_Fire_SkipWhenBusy_NotActive(t *testing.T) {
	creator := uuid.New()
	wantInst := uuid.New()
	trig := &runnerTrigger{id: wantInst, created: true}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{busy: false})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), true)

	require.NoError(t, err)
	assert.True(t, fired)
	assert.Equal(t, wantInst, inst)
	assert.Equal(t, 1, trig.calls)
}

// Default collision (skipWhenBusy=false) + active run => fires ANYWAY. The
// active checker must not even be consulted; assert by leaving it erroring and
// confirming Fire still triggers.
func TestRunner_Fire_DefaultFiresWhileBusy(t *testing.T) {
	creator := uuid.New()
	wantInst := uuid.New()
	trig := &runnerTrigger{id: wantInst, created: true}
	// active returns busy=true AND an error; neither must affect a default fire.
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{busy: true, err: errors.New("boom")})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), false)

	require.NoError(t, err)
	assert.True(t, fired)
	assert.Equal(t, wantInst, inst)
	assert.Equal(t, 1, trig.calls)
}

// Failure: creator identity unresolvable => wrapped ErrCreatorUnresolved, no
// trigger call.
func TestRunner_Fire_CreatorUnresolved(t *testing.T) {
	creator := uuid.New()
	trig := &runnerTrigger{id: uuid.New(), created: true}
	r := schedule.NewRunner(trig, runnerIdents{err: errors.New("user gone")}, runnerActive{})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), false)

	require.Error(t, err)
	assert.ErrorIs(t, err, schedule.ErrCreatorUnresolved)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
	assert.Equal(t, 0, trig.calls)
}

// Failure: skip-when-busy active-check itself errors => surfaced as an error
// (not silently fired), no trigger call.
func TestRunner_Fire_ActiveCheckError(t *testing.T) {
	creator := uuid.New()
	trig := &runnerTrigger{id: uuid.New(), created: true}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{err: errors.New("db down")})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), true)

	require.Error(t, err)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
	assert.Equal(t, 0, trig.calls)
}

// Failure: trigger error surfaced (e.g. deleted workflow). Runner does NOT
// translate workflow.ErrNotFound (that cron-only handling stays in fireOne);
// it returns the error wrapped as-is with fired=false.
func TestRunner_Fire_TriggerError(t *testing.T) {
	creator := uuid.New()
	trigErr := errors.New("trigger failed")
	trig := &runnerTrigger{err: trigErr}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), false)

	require.Error(t, err)
	assert.ErrorIs(t, err, trigErr)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
}
```

- [ ] **Step 2: Run the Runner test to verify it fails to compile**

Run: `go test ./domain/schedule/ -run TestRunner_Fire`
Expected: FAIL — build error `undefined: schedule.NewRunner` and `undefined: schedule.ErrCreatorUnresolved`.

- [ ] **Step 3: Implement `runner.go` and the `ErrCreatorUnresolved` sentinel**

Create `domain/schedule/runner.go`:

```go
package schedule

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// ErrCreatorUnresolved is returned when a run cannot be attributed because the
// schedule's creator identity cannot be resolved (e.g. the user was deleted). It
// maps to HTTP 409 / MCP conflict at the adapter seam: the run is not startable
// in its current attributed form, which is a conflict, not a server fault.
var ErrCreatorUnresolved = errors.New("schedule: creator identity could not be resolved")

// Runner is the shared fire core: resolve the schedule's creator identity,
// inject it into the context, and trigger the workflow as that creator. It is
// reused by the cron Evaluator (via fireCore) and by Service.RunNow. It holds NO
// repository and therefore NEVER advances LastFiredAt — advancing the cron
// position is the Evaluator's responsibility alone. Run-now is out of cadence
// and must not perturb the cron position.
type Runner struct {
	trigger Trigger
	idents  IdentityResolver
	active  ActiveRunChecker
}

// NewRunner builds a Runner from the same three collaborators the Evaluator
// already holds (Trigger, IdentityResolver, ActiveRunChecker).
func NewRunner(t Trigger, i IdentityResolver, a ActiveRunChecker) *Runner {
	return &Runner{trigger: t, idents: i, active: a}
}

// Fire resolves the schedule's creator, optionally enforces skip-when-busy,
// injects the creator identity, and triggers the workflow.
//
//   - skipWhenBusy && an active run exists => returns (uuid.Nil, false, nil):
//     skipped, NOT an error.
//   - default (skipWhenBusy == false) fires regardless of any active run; the
//     active checker is not consulted.
//   - creator identity unresolvable => (uuid.Nil, false, wrapped
//     ErrCreatorUnresolved).
//   - trigger failure => (uuid.Nil, false, wrapped trigger error).
//
// Fire NEVER advances LastFiredAt (run-now is out of cadence).
func (r *Runner) Fire(ctx context.Context, s Schedule, skipWhenBusy bool) (uuid.UUID, bool, error) {
	if skipWhenBusy {
		busy, err := r.active.HasActiveRun(ctx, s.WorkflowName, s.CreatorID)
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("schedule: checking active run: %w", err)
		}
		if busy {
			return uuid.Nil, false, nil // skipped, not an error
		}
	}
	id, ident, err := fireResolve(ctx, r.idents, s)
	if err != nil {
		return uuid.Nil, false, err
	}
	fireCtx := identity.WithIdentity(ctx, ident)
	// Phase 3 seam: stamp the run's origin so run-now occurrences are attributed
	// to their schedule in run history. instance.Origin / WithOrigin / OriginRunNow
	// are introduced in Phase 3; until then this is a no-op comment. Do NOT import
	// domain/instance here in Phase 1.
	//
	//   fireCtx = instance.WithOrigin(fireCtx, instance.Origin{
	//       Source: instance.OriginRunNow, ScheduleID: &s.ID,
	//   })
	_ = id // (placeholder removed below; see note)
	instID, _, err := r.trigger.TriggerWithDepartment(fireCtx, s.WorkflowName, s.Variables, "normal", s.Department)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("schedule: triggering run-now: %w", err)
	}
	return instID, true, nil
}

// fireResolve resolves the creator identity for a schedule, mapping a resolver
// failure to ErrCreatorUnresolved. It is the SHARED identity-resolution core used
// by both Runner.Fire and Evaluator.fireOne so there is exactly one
// implementation. Returns (uuid.Nil-ignorable, identity, error); the first return
// value is unused by callers and exists only to keep a single signature — see the
// simpler form below.
func fireResolve(ctx context.Context, idents IdentityResolver, s Schedule) (uuid.UUID, identity.Identity, error) {
	ident, err := idents.IdentityForUser(ctx, s.CreatorID)
	if err != nil {
		return uuid.Nil, identity.Identity{}, fmt.Errorf("%w: creator %s: %v", ErrCreatorUnresolved, s.CreatorID, err)
	}
	return uuid.Nil, ident, nil
}
```

**Cleanup note for the implementer:** the `_ = id` line and the `uuid.UUID` first return of `fireResolve` above are an artifact of a draft; remove them. Use this cleaner shared helper instead and call it from both `Fire` and `fireOne`:

```go
// resolveCreatorIdentity is the SINGLE identity-resolution implementation shared
// by Runner.Fire and Evaluator.fireOne. A resolver failure is mapped to
// ErrCreatorUnresolved.
func resolveCreatorIdentity(ctx context.Context, idents IdentityResolver, s Schedule) (identity.Identity, error) {
	ident, err := idents.IdentityForUser(ctx, s.CreatorID)
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: creator %s: %v", ErrCreatorUnresolved, s.CreatorID, err)
	}
	return ident, nil
}
```

Final `runner.go` `Fire` body (use this, not the draft above):

```go
func (r *Runner) Fire(ctx context.Context, s Schedule, skipWhenBusy bool) (uuid.UUID, bool, error) {
	if skipWhenBusy {
		busy, err := r.active.HasActiveRun(ctx, s.WorkflowName, s.CreatorID)
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("schedule: checking active run: %w", err)
		}
		if busy {
			return uuid.Nil, false, nil // skipped, not an error
		}
	}
	ident, err := resolveCreatorIdentity(ctx, r.idents, s)
	if err != nil {
		return uuid.Nil, false, err
	}
	fireCtx := identity.WithIdentity(ctx, ident)
	// Phase 3 seam (do NOT implement / import domain/instance in Phase 1):
	// Phase 3 (final-org-gaps-implementation-phase-3-task-1.md, Step 25) replaces
	// this comment by wrapping fireCtx with instance.WithOrigin, taking the address
	// of a LOCAL copy of s.ID (not &s.ID inline). The identity var here is `ident`.
	//   scheduleID := s.ID
	//   fireCtx = instance.WithOrigin(fireCtx, instance.Origin{
	//       Source: instance.OriginRunNow, ScheduleID: &scheduleID,
	//   })
	instID, _, err := r.trigger.TriggerWithDepartment(fireCtx, s.WorkflowName, s.Variables, "normal", s.Department)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("schedule: triggering run-now: %w", err)
	}
	return instID, true, nil
}
```

Delete the draft `fireResolve` and the `_ = id` line; keep only `resolveCreatorIdentity` and the final `Fire`.

- [ ] **Step 4: Run the Runner test to verify it passes**

Run: `go test ./domain/schedule/ -run TestRunner_Fire -v`
Expected: PASS for all seven `TestRunner_Fire_*` cases.

- [ ] **Step 5: Refactor `Evaluator.fireOne` to use the shared `resolveCreatorIdentity` (no behavior change)**

In `domain/schedule/evaluator.go`, change the identity-resolution lines of `fireOne` to call the shared helper so there is one implementation. The cron-only alerting, the `workflow.ErrNotFound` handling, and the `SetLastFired` calls stay exactly as they are.

Replace this opening of `fireOne`:

```go
	id, err := e.idents.IdentityForUser(ctx, s.CreatorID)
	if err != nil {
```

with:

```go
	id, err := resolveCreatorIdentity(ctx, e.idents, s)
	if err != nil {
```

The rest of the creator-gone branch (logging via `e.logger.Error(...)` and `e.alerter.Alert(ctx, s, notification.TypeScheduleMissed)` then `return`) is unchanged — it now logs the `ErrCreatorUnresolved`-wrapped error, which is acceptable (same control flow, richer message). Do **not** otherwise restructure `fireOne`; the deleted-workflow `SetLastFired` and success `SetLastFired` are cron-only and must remain.

- [ ] **Step 6: Run the FULL existing schedule package tests to verify the refactor is safe (refactor-safety category)**

Run: `go test ./domain/schedule/ -v`
Expected: PASS — all pre-existing `Evaluator` tests (skip-when-busy alert+advance, creator-gone skip-without-advance, deleted-workflow alert+advance, success position advance, recovery) stay green, plus the new Runner tests. If any pre-existing evaluator test changed behavior, the refactor was not behavior-preserving — fix `fireOne` to match the original control flow.

- [ ] **Step 7: Commit**

```bash
git add domain/schedule/runner.go domain/schedule/runner_test.go domain/schedule/evaluator.go
git commit -m "feat(schedule): extract shared Runner fire core; reuse in evaluator

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 8: Write the failing `Service.RunNow` tests**

Create `domain/schedule/service_runnow_test.go`. These use a local in-test repo and a stub firer; name fixtures distinctly from `service_test.go` (which already declares its own fixtures in the same `schedule_test` package). Confirm the existing fixture names first:

```bash
grep -n "^type \|^func New" ~/Repos/arbitron/domain/schedule/service_test.go | head -40
```

If a name below collides with an existing one, suffix it with `RN` (e.g. `runNowRepo`). The plan assumes the names below are free.

```go
package schedule_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- run-now service fixtures ---

type runNowRepo struct {
	m map[uuid.UUID]*schedule.Schedule
}

func newRunNowRepo(ss ...*schedule.Schedule) *runNowRepo {
	r := &runNowRepo{m: map[uuid.UUID]*schedule.Schedule{}}
	for _, s := range ss {
		r.m[s.ID] = s
	}
	return r
}

func (r *runNowRepo) Create(context.Context, *schedule.Schedule) error { return nil }
func (r *runNowRepo) Get(_ context.Context, id uuid.UUID) (*schedule.Schedule, error) {
	s, ok := r.m[id]
	if !ok {
		return nil, schedule.ErrNotFound
	}
	cp := *s
	return &cp, nil
}
func (r *runNowRepo) List(context.Context, schedule.ListFilters) ([]schedule.Schedule, error) {
	return nil, nil
}
func (r *runNowRepo) ListEnabled(context.Context) ([]schedule.Schedule, error) { return nil, nil }
func (r *runNowRepo) Update(context.Context, *schedule.Schedule) error         { return nil }
func (r *runNowRepo) Delete(context.Context, uuid.UUID) error                  { return nil }
func (r *runNowRepo) SetLastFired(context.Context, uuid.UUID, time.Time) error { return nil }

// allowCanTriggerRN is the can-trigger gate stub (RunNow does NOT call it, but
// NewService requires a non-nil CanTrigger).
type allowCanTriggerRN struct{}

func (allowCanTriggerRN) CanTrigger(context.Context, identity.Identity, string) (bool, error) {
	return true, nil
}

// stubFirer records its Fire call and returns canned results.
type stubFirer struct {
	calls        int
	gotSchedule  schedule.Schedule
	gotSkip      bool
	retInstance  uuid.UUID
	retFired     bool
	retErr       error
}

func (f *stubFirer) Fire(_ context.Context, s schedule.Schedule, skipWhenBusy bool) (uuid.UUID, bool, error) {
	f.calls++
	f.gotSchedule = s
	f.gotSkip = skipWhenBusy
	return f.retInstance, f.retFired, f.retErr
}

func ownerIdent(uid uuid.UUID) identity.Identity {
	return identity.Identity{UserID: uid, Username: "owner", Role: identity.RoleUser}
}

func adminIdent() identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}
}

func ctxAs(id identity.Identity) context.Context {
	return identity.WithIdentity(context.Background(), id)
}

func enabledSchedule(creator uuid.UUID) *schedule.Schedule {
	return &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "wf",
		CreatorID:    creator,
		Department:   "eng",
		Enabled:      true,
		Variables:    map[string]any{"k": "v"},
	}
}

// Happy path: owner runs their schedule => firer called with the loaded schedule
// and the requested skip flag; result mirrors the firer.
func TestService_RunNow_HappyPath(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	wantInst := uuid.New()
	firer := &stubFirer{retInstance: wantInst, retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	res, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{SkipWhenBusy: false})

	require.NoError(t, err)
	assert.Equal(t, wantInst, res.InstanceID)
	assert.True(t, res.Fired)
	require.Equal(t, 1, firer.calls)
	assert.Equal(t, s.ID, firer.gotSchedule.ID)
	assert.Equal(t, "wf", firer.gotSchedule.WorkflowName)
	assert.False(t, firer.gotSkip)
}

// SkipWhenBusy is passed through to the firer.
func TestService_RunNow_PassesSkipWhenBusy(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{SkipWhenBusy: true})

	require.NoError(t, err)
	assert.True(t, firer.gotSkip)
}

// Collision/skipped: firer reports fired=false (busy) => RunNow returns
// Fired=false, nil instance, no error.
func TestService_RunNow_Skipped(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.Nil, retFired: false}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	res, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{SkipWhenBusy: true})

	require.NoError(t, err)
	assert.False(t, res.Fired)
	assert.Equal(t, uuid.Nil, res.InstanceID)
}

// Authorization: admin may run another user's schedule.
func TestService_RunNow_AdminRunsOthersSchedule(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	res, err := svc.RunNow(ctxAs(adminIdent()), s.ID, schedule.RunNowInput{})

	require.NoError(t, err)
	assert.True(t, res.Fired)
	assert.Equal(t, 1, firer.calls)
}

// Authorization: a non-owner non-admin is forbidden; the firer is NOT called.
func TestService_RunNow_NonOwnerForbidden(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	stranger := ownerIdent(uuid.New()) // different UserID, RoleUser
	_, err := svc.RunNow(ctxAs(stranger), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrForbidden)
	assert.Equal(t, 0, firer.calls)
}

// Authorization: no request identity => forbidden (never leaks existence).
func TestService_RunNow_NoIdentityForbidden(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(context.Background(), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrForbidden)
	assert.Equal(t, 0, firer.calls)
}

// Not found: unknown id => ErrNotFound (authz check loads via repo.Get).
func TestService_RunNow_NotFound(t *testing.T) {
	firer := &stubFirer{}
	svc := schedule.NewService(newRunNowRepo(), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(adminIdent()), uuid.New(), schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrNotFound)
	assert.Equal(t, 0, firer.calls)
}

// Firer unwired => ErrRunNowUnavailable. Authorization is still enforced first:
// here the owner is authorized, so we reach the nil-firer guard.
func TestService_RunNow_FirerUnwired(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}) // no WithFirer

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrRunNowUnavailable)
}

// Firer error (e.g. creator unresolved) is surfaced unchanged.
func TestService_RunNow_FirerError(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retErr: schedule.ErrCreatorUnresolved}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrCreatorUnresolved)
}

// HasFirer reports wiring state for adapter gating.
func TestService_HasFirer(t *testing.T) {
	wired := schedule.NewService(newRunNowRepo(), allowCanTriggerRN{}, schedule.WithFirer(&stubFirer{}))
	unwired := schedule.NewService(newRunNowRepo(), allowCanTriggerRN{})
	assert.True(t, wired.HasFirer())
	assert.False(t, unwired.HasFirer())
}
```

Add the `time` import to the test file (used by `SetLastFired`):

```go
import (
	"context"
	"errors" // used if you add an error-construction case; remove if unused
	"testing"
	"time"
	...
)
```

If `errors` ends up unused after you finalize the cases above, drop it (the listed cases use sentinel values, not `errors.New`, so `errors` is likely unused — remove it to keep the build green).

- [ ] **Step 9: Run the RunNow tests to verify they fail to compile**

Run: `go test ./domain/schedule/ -run "TestService_RunNow|TestService_HasFirer"`
Expected: FAIL — build errors: `undefined: schedule.WithFirer`, `schedule.RunNowInput`, `schedule.RunNowResult`, `(*Service).RunNow`, `(*Service).HasFirer`, `schedule.ErrRunNowUnavailable`.

- [ ] **Step 10: Implement `Firer`, options, types, sentinel, and `RunNow` in `service.go`**

In `domain/schedule/service.go`:

a) Add the new sentinel to the existing authz `var (...)` block (near `ErrForbidden`):

```go
	// ErrRunNowUnavailable is returned when run-now is invoked on a Service that
	// has no Firer wired. It maps to HTTP 503 / MCP failed at the adapter seam:
	// the capability is not available in this deployment, not a client error.
	ErrRunNowUnavailable = errors.New("schedule: run-now is not available")
```

(`ErrCreatorUnresolved` already lives in `runner.go` — do not redeclare it.)

b) Add the `Firer` port, the `firer` field, and the `WithFirer` option. Add `firer Firer` to the `Service` struct:

```go
// Firer starts a schedule's workflow immediately, out of cron cadence. The
// schedule.Runner satisfies it. It is OPTIONAL on the Service: nil when unwired,
// in which case RunNow returns ErrRunNowUnavailable and the adapters do not
// advertise the run-now surface (HasFirer gates registration).
type Firer interface {
	Fire(ctx context.Context, s Schedule, skipWhenBusy bool) (uuid.UUID, bool, error)
}

// WithFirer wires the run-now fire path. Without it, RunNow is unavailable.
func WithFirer(f Firer) ServiceOption {
	return func(s *Service) { s.firer = f }
}
```

In the `Service` struct add the field:

```go
type Service struct {
	repo       ScheduleRepository
	canTrigger CanTrigger
	now        func() time.Time
	bounds     Bounds
	firer      Firer // optional; nil => run-now unavailable
}
```

c) Add the input/result types and the `RunNow` method (place near the other service methods, e.g. after `Delete`):

```go
// RunNowInput carries the run-now options. SkipWhenBusy defaults to false: by
// default run-now fires even if a prior run of the schedule is still active.
type RunNowInput struct {
	SkipWhenBusy bool
}

// RunNowResult reports the outcome of a run-now. Fired is false when the
// occurrence was skipped because a prior run was active and SkipWhenBusy was set;
// in that case InstanceID is uuid.Nil.
type RunNowResult struct {
	InstanceID uuid.UUID
	Fired      bool
}

// RunNow fires an existing schedule immediately, out of cron cadence, reusing the
// schedule's variables and creator attribution. Authorization is owner-or-admin
// (the same gate as Get/Update/Delete): the caller must be permitted to MANAGE
// the schedule; the run itself is attributed to the schedule's creator. It never
// advances the cron position (the Runner holds no repo).
func (s *Service) RunNow(ctx context.Context, id uuid.UUID, in RunNowInput) (RunNowResult, error) {
	c, err := caller(ctx)
	if err != nil {
		return RunNowResult{}, err
	}
	sched, err := s.repo.Get(ctx, id)
	if err != nil {
		return RunNowResult{}, err
	}
	if !authorize(c, *sched) {
		return RunNowResult{}, ErrForbidden
	}
	if s.firer == nil {
		return RunNowResult{}, ErrRunNowUnavailable
	}
	instID, fired, err := s.firer.Fire(ctx, *sched, in.SkipWhenBusy)
	if err != nil {
		return RunNowResult{}, err
	}
	return RunNowResult{InstanceID: instID, Fired: fired}, nil
}

// HasFirer reports whether the run-now path is wired. Adapters use it to gate
// registration of the run-now surface (consistent with the conditional
// schedule-tools registration pattern).
func (s *Service) HasFirer() bool { return s.firer != nil }
```

- [ ] **Step 11: Run the RunNow tests to verify they pass**

Run: `go test ./domain/schedule/ -run "TestService_RunNow|TestService_HasFirer" -v`
Expected: PASS for all `TestService_RunNow_*` and `TestService_HasFirer`.

- [ ] **Step 12: Run the full domain/schedule suite + vet to confirm nothing regressed**

Run: `go test ./domain/schedule/... && go vet ./domain/schedule/...`
Expected: PASS, no vet complaints. In particular all pre-existing `service_test.go` and `evaluator_test.go` tests stay green.

- [ ] **Step 13: Commit**

```bash
git add domain/schedule/service.go domain/schedule/service_runnow_test.go domain/schedule/runner.go
git commit -m "feat(schedule): add Service.RunNow with owner-or-admin authz and optional Firer

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Definition of done (Task 1)

- `schedule.Runner` exists with `NewRunner(Trigger, IdentityResolver, ActiveRunChecker) *Runner` and `Fire(ctx, Schedule, skipWhenBusy bool) (uuid.UUID, bool, error)`; it never advances `LastFiredAt`.
- `Evaluator.fireOne` resolves the creator via the shared `resolveCreatorIdentity` helper; all pre-existing evaluator tests are green (refactor safety).
- `schedule.Service` has `WithFirer`, `RunNowInput{SkipWhenBusy}`, `RunNowResult{InstanceID, Fired}`, `RunNow`, and `HasFirer`.
- New sentinels: `ErrCreatorUnresolved` (runner.go) and `ErrRunNowUnavailable` (service.go); `ErrForbidden`/`ErrNotFound` reused.
- `go test ./domain/schedule/...` is green. No `domain/instance` import was added (Phase 3 seam left as a comment).
