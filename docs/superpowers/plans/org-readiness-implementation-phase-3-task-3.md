# Org-Readiness — Phase 3, Task 3: in-process schedule evaluator (fire-as-creator, skip, no-backfill, alerts)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Strict TDD with an **injected clock** so all time-dependent behavior is deterministic — never call `time.Now()` inside the evaluator.

**Goal:** Add a single in-process evaluator that, on a ticker, fires due enabled schedules by injecting the creator's identity into the context and calling the existing `Engine.TriggerWithDepartment` (owner + department stamped by the existing `resolveStamp`). Implement the **skip-when-busy** collision policy and the **no-backfill** misfire/recovery policy, each raising a notification (`schedule_skipped` / `schedule_missed`). Wire `Start`/`Stop`/recovery into the composition root, mirroring `domain/queue/scheduler.go`.

**Architecture:** A new application service `domain/schedule/evaluator.go`. It depends on ports only: the `ScheduleRepository` (Task 1/2), a narrow trigger port (the engine satisfies it), a narrow "active-run" port (the instance repo satisfies it), the `notification.Repository` + `notification.ExternalNotifier`, and an injected `Clock`. The run loop is **our own** (not `robfig/cron`'s runner) so it integrates with the queue + graceful lifecycle. Next-fire math is `schedule.NextFire` from Task 1. Lifecycle (`Start(ctx)`/`Stop()`, `stopCh`/`doneCh`) mirrors `queue.Scheduler`.

**Tech stack:** Go 1.25.5, `testing` + `testify`, fakes for every port, a fake clock.

---

## Dependencies

- **Task 1** (entity, `NextFire`, port) and **Task 2** (persistence — recovery reads `last_fired_at`). Task 4 (management surface) is independent of this task but shares the same repo.

## Spec / design references

- Spec `docs/specs/org-readiness-spec.md` (Phase 3) — collision "skip when busy"; misfire "no backfill"; "skips and misses are surfaced" via alert; "each resulting scheduled run is owned by and attributed to the schedule's creator (carrying the creator's identity and department, consistent with how manually triggered runs are attributed)"; script-only workflows schedulable.
- Design `docs/superpowers/specs/org-readiness-design-phase-3.md` §"Evaluator", §"Alerts", §"Data flow (scheduled run)", §"Error handling / edge cases".

## Integration anchors (read before coding)

- `domain/engine/engine.go` — `triggerOwner` (ll. 156-169: context identity wins, else default user) and `TriggerWithDepartment` (ll. 226-373). A system trigger with NO context identity skips visibility gating (ll. 242-258); a trigger WITH a context identity is gated. **The evaluator injects the creator identity** so the run is owned by + attributed to the creator (gating passes because authz was checked at schedule-create time in Task 4).
- `domain/engine/trigger_stamp.go` — `resolveStamp` stamps visibility+department from the publication and the optional department override. The evaluator passes `schedule.Department` as the `departmentOverride` argument, so the run is stamped under the creator's department exactly like a manual trigger.
- `domain/identity/identity.go` — `WithIdentity(ctx, Identity{...})` / `FromContext`. Build the creator `Identity` from the schedule's `CreatorID` (+ the creator's departments, looked up via the user repo) so `resolveStamp`'s `id.InDepartment(override)` check passes.
- `domain/queue/scheduler.go` — `Start(ctx)`/`Stop()`/`stopCh`/`doneCh`/`loop` pattern to mirror.
- `domain/engine/engine.go` `emitNotification` (ll. 967-998) — the notification-create + external-notify pattern to mirror; note `Notification` is keyed by `InstanceID` (see Notes for the no-instance schedule-alert handling).
- `cmd/arbitron/main.go` — `initDomain` step "13a"/"14a" (scheduler `Start` + `RecoverTimers`) and `domainStack.Close` (ll. 181-207) for graceful shutdown ordering.

## Files

- **Modify:** `domain/notification/notification.go` — add two `Type` constants:
  ```go
  TypeScheduleSkipped Type = "schedule_skipped"
  TypeScheduleMissed  Type = "schedule_missed"
  ```
- **Create:** `domain/schedule/evaluator.go`
  - **Clock seam:**
    ```go
    type Clock interface{ Now() time.Time }
    type realClock struct{}
    func (realClock) Now() time.Time { return time.Now() }
    ```
  - **Ports (defined here, satisfied by existing types):**
    ```go
    // Trigger fires a workflow as the (context-injected) creator. Engine satisfies it.
    type Trigger interface {
        TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority, department string) (uuid.UUID, bool, error)
    }
    // ActiveRunChecker reports whether the schedule's workflow has a live run for the creator.
    // The instance repo satisfies it via a CountActive/List(running|queued|waiting|paused) query.
    type ActiveRunChecker interface {
        HasActiveRun(ctx context.Context, workflowName string, ownerID uuid.UUID) (bool, error)
    }
    // IdentityResolver builds the creator identity (with departments) for context injection.
    // The user repo / user service satisfies it.
    type IdentityResolver interface {
        IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error)
    }
    // Alerter raises a schedule_skipped / schedule_missed notification. A thin
    // wrapper over notification.Repository + ExternalNotifier (mirrors engine.emitNotification).
    type Alerter interface {
        Alert(ctx context.Context, s schedule.Schedule, t notification.Type) error
    }
    ```
  - **Evaluator struct + constructor:**
    ```go
    type Evaluator struct {
        repo     ScheduleRepository
        trigger  Trigger
        active   ActiveRunChecker
        idents   IdentityResolver
        alerter  Alerter
        clock    Clock
        interval time.Duration   // default 30s
        stopCh, doneCh chan struct{}
        logger   *slog.Logger
    }
    func NewEvaluator(repo ScheduleRepository, trigger Trigger, active ActiveRunChecker, idents IdentityResolver, alerter Alerter, opts ...Option) *Evaluator
    ```
    `Option` functions inject the clock and interval (default `realClock{}`, 30s) so tests pass a fake clock + drive ticks directly.
  - **`Tick(ctx)`** — the unit-testable core (the loop just calls this on each ticker beat). For each `repo.ListEnabled`:
    - compute `prev := s.LastFiredAt` (or `s.CreatedAt` when nil) and `next := schedule.NextFire(s, prev)`; on `NextFire` error, log + skip (a corrupt cron should not crash the loop; Task 4 prevents persisting invalid crons, this is defense-in-depth).
    - **due** when `!next.After(clock.Now())` (i.e. `next <= now`).
    - if due:
      - `active.HasActiveRun(ctx, s.WorkflowName, s.CreatorID)` true ⇒ **skip**: `alerter.Alert(ctx, s, TypeScheduleSkipped)`, then `repo.SetLastFired(ctx, s.ID, next)` (advance position so the same occurrence is not retried every tick), and continue. **No instance created.**
      - else **fire** (see `fireOne`).
  - **`fireOne(ctx, s, fireTime)`** —
    - `id, err := idents.IdentityForUser(ctx, s.CreatorID)`; on error → alert/log + skip (creator gone).
    - `ctx = identity.WithIdentity(ctx, id)`.
    - `_, _, err = trigger.TriggerWithDepartment(ctx, s.WorkflowName, s.Variables, "normal", s.Department)`.
    - on success: `repo.SetLastFired(ctx, s.ID, fireTime)`.
    - on `workflow.ErrNotFound` (deleted workflow): alert (`TypeScheduleMissed` or skipped) + log; optionally disable the schedule (design: "the schedule may be disabled") — implement as: leave enabled but alert, to keep behavior simple; document the choice in a comment. Still advance `SetLastFired` so it doesn't hot-loop.
  - **`Recover(ctx)`** (no-backfill, called once at startup before `Start`):
    - for each `repo.ListEnabled`: `prev := LastFiredAt or CreatedAt`; `next := NextFire(s, prev)`.
    - if `next` is in the **past** relative to `clock.Now()` (i.e. occurrences were missed during downtime): **do NOT fire**; advance the schedule's position to the latest missed occurrence (`SetLastFired(ctx, s.ID, lastMissedBefore(now))`) so the next future tick computes the *next* future occurrence, and raise ONE `TypeScheduleMissed` alert noting the gap. (Compute `lastMissedBefore(now)` by iterating `NextFire` from `prev` until the result is `>= now`, taking the previous value — or simply set position to `now` so `NextFire(s, now)` is the next future occurrence; the simpler `now` approach is acceptable and documented.)
    - if `next` is in the future: leave as-is (normal).
  - **Lifecycle:** `Start(ctx)` launches `loop` (ticker at `interval`, each beat `Tick(ctx)`, also selecting on `ctx.Done()`/`stopCh`, closing `doneCh` on exit). `Stop()` closes `stopCh` and waits on `doneCh` — identical shape to `queue.Scheduler.Stop()`.
- **Create:** `domain/schedule/alerter.go` (or fold into evaluator) — `type ScheduleAlerter struct { notifs notification.Repository; external notification.ExternalNotifier; logger *slog.Logger }` implementing `Alert`, mirroring `engine.emitNotification`. See Notes for the `InstanceID` handling on instance-less alerts.
- **Modify:** `adapter/sqlite/instance_repo.go` (or wherever `instance.InstanceRepository` is implemented) and `domain/instance/ports.go` — add a `HasActiveRun(ctx, workflowName, ownerID)` (or reuse `List` with `State`+`WorkflowName`+`OwnerID` filters and a non-empty check). Prefer reusing the existing `ListFilters{State, WorkflowName, OwnerID}` via a tiny adapter method over a new SQL query, to minimize surface. "Active" = state in {queued, running, waiting, paused}. Add a `queries/instances.sql` `CountActiveByWorkflowOwner` only if the filter approach can't express the state set.
- **Modify:** `cmd/arbitron/main.go` — in `initDomain`:
  - construct `scheduleRepo := adaptsqlite.NewScheduleRepo(db)` (after `notifRepo`).
  - construct the alerter from `notifRepo` + `externalNotifier`.
  - construct `scheduleEval := schedule.NewEvaluator(scheduleRepo, eng, activeChecker, userSvc, alerter)`.
  - add `scheduleRepo` + `scheduleEval` + `scheduleEvalCancel context.CancelFunc` fields to `domainStack`.
  - call `scheduleEval.Recover(context.Background())` during startup recovery (step ~14b, AFTER timer/webhook recovery, BEFORE `Start`) so no-backfill is applied once.
  - `scheduleEvalCtx, scheduleEvalCancel := context.WithCancel(...)`; `scheduleEval.Start(scheduleEvalCtx)`.
  - in `domainStack.Close` (ll. 181-207): stop the evaluator **before** the engine drains / DB closes — i.e. cancel + `scheduleEval.Stop()` near the top, alongside `ds.eng.Shutdown()` / `ds.scheduler.Stop()`. Order: stop evaluator (no new triggers) → engine shutdown (drain) → scheduler stop → DB close.
- **Test:** `domain/schedule/evaluator_test.go`, `domain/schedule/alerter_test.go` (package `schedule_test`), plus an `instance_repo` test for `HasActiveRun`.

## TDD test list (five categories)

All evaluator tests use fakes: `fakeRepo` (in-memory schedules), `fakeTrigger` (records calls + the `identity.FromContext(ctx)` it sees + injectable error), `fakeActive` (returns programmed bool), `fakeIdents` (returns a programmed `identity.Identity`), `fakeAlerter` (records alerts), and a `fakeClock{ t time.Time }`. Drive behavior by calling `Tick`/`Recover` directly (no real ticker).

### Happy-path

- [ ] `TestEvaluator_DueSchedule_FiresAsCreator` — schedule due (clock past its next-fire), `fakeActive` false. After `Tick`: `fakeTrigger` was called once with the workflow name, `priority="normal"`, `department=s.Department`, the exact `Variables`, AND the context it received carries `identity.FromContext` == the creator identity (UserID == `CreatorID`). Proves attribution-to-creator (owner + department).
- [ ] `TestEvaluator_AfterFire_SetsLastFired` — after a successful fire, `repo.SetLastFired` was called with the computed `next` time; a second `Tick` at the same clock does NOT fire again (position advanced).
- [ ] `TestEvaluator_VariablesPassedThrough` — non-empty `Variables` reach the trigger unchanged.

### Input-validation / policy

- [ ] `TestEvaluator_NotDue_DoesNotFire` — clock before next-fire ⇒ no trigger, no alert.
- [ ] `TestEvaluator_DisabledNotInListEnabled` — disabled schedules are never returned by `ListEnabled`, so `Tick` ignores them (assert via repo fake only returning enabled).

### Error-handling

- [ ] `TestEvaluator_Collision_SkipsAndAlerts` — `fakeActive` true for a due schedule ⇒ NO trigger call, exactly one `TypeScheduleSkipped` alert, and `SetLastFired` advanced (so it doesn't re-alert every tick). Directly maps to spec "occurrence is skipped and an alert is raised."
- [ ] `TestEvaluator_TriggerError_LogsNoCrash` — `fakeTrigger` returns an error ⇒ `Tick` returns without panicking and the loop survives (a subsequent `Tick` still works).
- [ ] `TestEvaluator_DeletedWorkflow_Alerts` — `fakeTrigger` returns `workflow.ErrNotFound` ⇒ an alert is raised and the evaluator keeps running.
- [ ] `TestEvaluator_CreatorUnresolvable_SkipsGracefully` — `fakeIdents` returns an error ⇒ no trigger, no crash.

### Edge-cases / boundary (no-backfill recovery)

- [ ] `TestEvaluator_Recover_MissedOccurrences_NoBackfill` — `LastFiredAt` set well in the past (downtime), clock now well after several missed occurrences. After `Recover`: `fakeTrigger` was NEVER called (no backfill), exactly one `TypeScheduleMissed` alert was raised, and the schedule's position was advanced so the NEXT `Tick` (with clock at the next future occurrence) fires normally. This is the spec's "missed occurrences are not backfilled, an alert reflects the miss, and the next occurrence fires normally."
- [ ] `TestEvaluator_Recover_FutureNextFire_NoAlert` — `LastFiredAt` recent, next-fire in the future ⇒ `Recover` raises no alert and does not advance position.
- [ ] `TestEvaluator_ExactlyDue_Boundary` — `next == clock.Now()` (to the second) counts as due (`!next.After(now)` semantics) and fires.

### Integration

- [ ] `TestEvaluator_StartStop_GracefulShutdown` — `Start(ctx)` then `Stop()` returns promptly (mirrors `queue.Scheduler` Stop test); a real (short-interval) ticker fires `Tick` at least once against a due schedule, then `Stop` halts cleanly with no goroutine leak (use a `done`-channel assertion).
- [ ] `TestScheduleAlerter_CreatesNotificationAndExternalNotifies` — `Alert` writes a `notification.Notification` with `Type == schedule_skipped`, `WorkflowName == s.WorkflowName`, and forwards to the `ExternalNotifier` (fake records the call) — mirrors `engine.emitNotification`.
- [ ] `TestInstanceRepo_HasActiveRun` — seed a running instance for (workflow, owner) ⇒ `HasActiveRun` true; only completed/failed/cancelled instances ⇒ false; different owner/workflow ⇒ false.

### Script-only workflow (cross-task acceptance, lives here)

- [ ] `TestEvaluator_FiresScriptOnlyWorkflow_Completes` — an integration-style test wiring the REAL engine (as in `domain/engine/*_integration_test.go`) with a script-only workflow (no model nodes), a schedule due now, fake clock; assert the instance is created and (driving the engine to completion) reaches `completed`. Confirms spec "a cron schedule starts ... a script-only workflow." If a full engine integration is heavy, at minimum assert the engine's `TriggerWithDepartment` accepts and enqueues a script-only workflow under the injected creator identity.

## Implementation steps (after each test goes red)

1. [ ] Add the two `notification.Type` constants.
2. [ ] `HasActiveRun` on the instance repo (+ test) — the collision primitive.
3. [ ] `evaluator.go` Clock + ports + struct + `fireOne` to pass fire-as-creator.
4. [ ] `Tick` (due detection + SetLastFired) to pass happy/not-due.
5. [ ] Collision branch + alert.
6. [ ] `alerter.go` to pass the alerter test.
7. [ ] `Recover` (no-backfill) + miss alert.
8. [ ] `Start`/`Stop` loop mirroring `queue.Scheduler`.
9. [ ] Wire into `cmd/arbitron/main.go` (`Recover` before `Start`; stop-before-drain in `Close`).
10. [ ] `make test` green; `go build ./...` green; `./arbitron serve` starts and shuts down cleanly.

## Acceptance criteria

- A due schedule starts its workflow at the correct time in its timezone, attributed to the creator (owner == `CreatorID`, department stamped via the existing `resolveStamp` from `schedule.Department`), including a script-only workflow.
- A collision (creator already has a live run of that workflow) skips the occurrence — no new instance — and raises exactly one `schedule_skipped` alert.
- After a restart past one or more scheduled times, missed occurrences are NOT backfilled, a `schedule_missed` alert is raised, and the next future occurrence fires normally.
- The evaluator starts and stops cleanly within the composition root's graceful-shutdown ordering (stops before the engine drains).
- All time-dependent behavior is deterministic under the injected clock (no `time.Now()` in the evaluator).

## Notes

- **Single in-process fit:** one `Evaluator` goroutine on one instance is sufficient per the spec's explicit single-instance, no-scale-out scope. No distributed lock / leader election is needed; the run loop is ours so it composes with the existing queue lifecycle.
- **Deterministic position across restart:** next-fire is always derived from the persisted `LastFiredAt` (+ cron + tz) via `schedule.NextFire`, so a restart recomputes the same position — no in-memory-only authoritative state. This is why `Recover` only needs the DB, not any prior process memory.
- **Instance-less alerts:** `notification.Notification` is keyed by `InstanceID`. For `schedule_skipped`/`schedule_missed` there is no instance. Choose the lowest-friction option and document it: (a) set `InstanceID = uuid.Nil` and rely on `Type` + `WorkflowName` for display, or (b) extend the notification model with a nullable instance id. Prefer (a) for this task (no schema change to the notifications table); if the notifications UI/repo rejects nil instance ids, fall back to (b) as a small migration. Either way the operator sees that a run did not happen, per spec.
- **Why fire as creator (not the default system user):** unlike webhook/timer system triggers (which carry no identity and use the default user per `triggerOwner`), a schedule MUST attribute its run to the human creator for visibility/department stamping — hence the explicit `identity.WithIdentity` injection. Authz was already enforced at schedule-create time (Task 4), so the gated trigger path passes for the creator.
