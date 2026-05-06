# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### API-003: UpdateQueueConfig does not write back to config struct — GetConfig returns stale caps
**Area**: API Layer (`adapter/http/server.go` — `UpdateQueueConfig`, `GetConfig`, `SystemHandler`)
**Fixed**: FA13 Pass 2
**Detail**: `UpdateQueueConfig` read `h.config.Queue.GlobalCap/PerUserCap` to compute the old values, called `h.queueManager.UpdateConfig`, then returned — but never wrote the new values back into `h.config.Queue`. Consequently: (1) `GET /system/config` always returned the startup caps regardless of updates; (2) a second `PUT` would use stale old values when deciding whether the cap was raised, potentially triggering a spurious `Evaluate` call. Fixed by adding a `sync.Mutex` to `SystemHandler` and updating `h.config.Queue.GlobalCap/PerUserCap` under that lock before releasing, with corresponding lock in `GetConfig` for the Queue field reads. Added regression test `TestRouter_GetConfig_ReflectsUpdatedQueueCaps`. Also added `TestLoad_QueueYAMLOverride` to the config loader tests, and `assert.Equal(t, cfg1.Queue, cfg2.Queue)` to `TestLoad_MissingFileCreatesDefault`.

### ENGINE-006: Missing NotifyCapacityFreed in run() error exit paths
**Area**: Engine (`domain/engine/engine.go` — `run()`)
**Fixed**: FA6 Pass 2
**Detail**: Seven error return paths inside `run()` called `SetFailed` and returned without calling `NotifyCapacityFreed`, unlike the halt path which correctly notifies. Affected paths: initial `Get` failure, `parseWorkflowGraph` failure, missing entrypoint, loop `Get` failure, `UpdateCurrentNode` failure, `UpdateVariables` in skip path, route path with no error node ID, and `UpdateVariables` in success path. If any of these DB errors occurred, the running instance would transition to failed but the queue manager would not be triggered, leaving queued instances stuck until another event (completion, trigger, timer fire) happened. Fixed by adding `NotifyCapacityFreed` to all affected return sites. Added regression test `TestEngine_RunLoop_ParseFailure_NotifiesCapacity`.

### HTTP-W-001: Silent error suppression in webhook handler
**Area**: Webhook HTTP Handler (`adapter/http/webhook_handler.go`)
**Fixed**: FA8 Pass 1
**Detail**: Three errors were silently discarded with `_ =`: (1) `nodeExecs.Update` — payload lost on failure; (2) `SetWebhookReceived` — paused instance won't see the received webhook on Resume, returning it to waiting instead of enqueuing; (3) `SetQueued` — instance stuck in waiting with deactivated token and no active timer. Fixed by replacing `_ =` with `slog.Error` calls. For `SetQueued` specifically, `NotifyCapacityFreed` is only called on success. Added tests for valid secret happy path, missing node exec record, and SetQueued failure path.

### API-001: Silent errors and missing validation in UpdateQueueConfig
**Area**: API Layer (`adapter/http/server.go` — `UpdateQueueConfig`)
**Fixed**: FA9 Pass 1
**Detail**: Two issues: (1) `go queueManager.Evaluate(...)` goroutine error was silently dropped with `_ =`; fixed by logging via `slog.Error`. (2) No validation that `globalCap`/`perUserCap` are non-negative — a negative cap would silently be treated as unlimited (since `CanStart` guards with `> 0`); fixed by adding 400 validation.

### API-002: Missing test coverage for new queue API surface
**Area**: API Layer (`adapter/http/instance_handler.go`, `adapter/http/server.go`, `adapter/http/workflow_handler.go`)
**Fixed**: FA9 Pass 1
**Detail**: No tests existed for: (a) `SetPriority` handler — entire handler untested; (b) Trigger returning 202+queued when `started=false`; (c) `UpdateQueueConfig` endpoint; (d) `GET /system/config` queue section fields; (e) instance response fields for `queuedAt`, `waitingUntil`, `webhookUrl`, `webhookReceived`. Added 15 tests covering all these paths including happy paths, validation errors, and route wiring.

### ENGINE-005: Resume of paused webhook-wait with expired timeout goes back to waiting (stuck instance)
**Area**: Engine (`domain/engine/engine.go` — `Resume`)
**Fixed**: FA16 Pass 1
**Detail**: When a webhook-wait instance (with `WebhookToken` AND `WaitingUntil`) is paused, and the timer fires while paused, `OnTimerFired` sees `StatePaused` (not `StateWaiting`) and no-ops. The timer slot is then gone. When `Resume` is later called, the webhook branch checked `WebhookReceivedAt == nil` and transitioned back to `StateWaiting` — but with no active timer and the timeout already passed. Fixed by adding a `WaitingUntil` expiry check in the webhook branch of `Resume`: if `WaitingUntil != nil && time.Now().After(*WaitingUntil)`, deactivate the webhook token in the registry and call `SetFailed` instead of going back to waiting. Added regression tests `TestEngine_ResumeFromWebhookWaiting_TimeoutExpiredWhilePaused_FailsInstance` and `TestEngine_ResumeFromWebhookWaiting_TimeoutNotExpired_RestoresWaiting`.

### DB-001: Generated code missing Phase 2 columns — crash recovery completely broken
**Area**: Database Layer (`adapter/postgres/generated/models.go`, `generated/instances.sql.go`, `generated/queue.sql.go`, `adapter/postgres/instance_repo.go`, `adapter/postgres/queue_repo.go`)
**Fixed**: FA10 Pass 1
**Detail**: Four critical issues found together: (1) `generated.Instance` struct was missing `WaitingUntil`, `WebhookToken`, `WebhookSecretHash`, `WebhookReceivedAt` — all 4 columns added by migrations 006/007; (2) all SELECT/RETURNING queries in `instances.sql.go` and `nextEligible` in `queue.sql.go` only selected the original 14 columns and only scanned 14 values — reading back `nil` for all Phase 2 fields, making `Resume` always see no webhook token and no waiting-until, causing incorrect behavior; (3) `rowToInstance` in `instance_repo.go` did not map the 4 new fields; (4) `ListWaitingWithTimer` and `ListWaitingWithWebhook` in `queue_repo.go` were stubs returning `(nil, nil)` — crash recovery would find no instances to recover. Fixed by: updating `generated.Instance` struct with the 4 new fields; updating all column lists and scan calls in generated SQL files; adding `ListWaitingWithTimer` and `ListWaitingWithWebhook` as proper generated queries in `queue.sql.go` and documenting them in `queries/queue.sql`; implementing both methods in `queue_repo.go` via the generated queries; updating `rowToInstance` to map all new fields.

### DB-002: ListWaitingWithTimer incorrectly includes webhook-wait instances
**Area**: Database Layer (`adapter/postgres/queries/queue.sql`, `generated/queue.sql.go`)
**Fixed**: FA10 Pass 2
**Detail**: `ListWaitingWithTimer` queried `WHERE state = 'waiting' AND waiting_until IS NOT NULL`, which also matched webhook-wait instances that have a timeout configured (`waiting_until IS NOT NULL` AND `webhook_token IS NOT NULL`). `RecoverTimers` would then call `SetQueued` on an expired webhook-wait instance instead of letting `RecoverWebhooks` call `SetFailed`. This would queue (and possibly restart) an instance whose webhook timeout expired during downtime, bypassing the failure semantics. Fixed by adding `AND webhook_token IS NULL` to the query so webhook-wait instances are exclusively processed by `RecoverWebhooks`. Added regression test `TestRecoverTimers_WebhookInstanceNotInList`.

## Fixed Previously

### QUEUE-001: Data race in UpdateConfig / CanStart
**Area**: Queue Manager (`domain/queue/manager.go`)
**Fixed**: FA1 Pass 1
**Detail**: `UpdateConfig` wrote `GlobalCap`/`PerUserCap` on the shared `*config.Config` without holding `m.mu`, while `Evaluate` read them under `m.mu` and `CanStart` read them with no lock at all — a live data race under `-race` for any concurrent trigger+config-update scenario. Fixed by: (1) having `UpdateConfig` acquire `m.mu` before writing, (2) having `CanStart` take a brief snapshot of the cap values under `m.mu` before releasing and doing I/O.

### QUEUE-002: SetFailed error swallowed in webhook timeout handler
**Area**: Queue Manager (`domain/queue/scheduler.go` — `OnTimerFired`)
**Fixed**: FA1 Pass 1
**Detail**: In the `TimerTypeWebhook` branch, both `SetWebhookFields` and `SetFailed` errors were discarded with `_ =`. If `SetFailed` fails the instance is permanently stuck in `waiting` with no active timer. Fixed by: returning the `SetFailed` error so the scheduler can log it; logging (but not returning) the `SetWebhookFields` error since it is best-effort cleanup.

### ENGINE-001: Magic string "timer_delay" in executor_timer.go
**Area**: Timer Delay Executor (`domain/engine/executor_timer.go`)
**Fixed**: FA4 Pass 1
**Detail**: The scheduler `Schedule` call used the raw string `"timer_delay"` instead of a constant. The queue package defines `TimerTypeDelay = "timer_delay"` but the engine domain cannot import the queue domain. Fixed by adding `TimerTypeDelay` and `TimerTypeWebhook` constants to `domain/engine/ports.go` (alongside the `TimerScheduler` interface where they belong) and updating the executor to use the constant.

### ENGINE-002: nodeExecs.Create error silently dropped in TimerDelayExecutor
**Area**: Timer Delay Executor (`domain/engine/executor_timer.go`)
**Fixed**: FA4 Pass 1
**Detail**: After a successful `SetWaiting` call (instance now in waiting state), the node execution record write failure was silently ignored with `_ =`. If the write fails, the audit record is missing. Changed to log via `slog.Error` — the error is not propagated because `SetWaiting` already committed (returning the error would cause the engine to call `SetFailed` on an instance that's already in `waiting` state, creating an inconsistent state).

### ENGINE-003: Duplicate node execution records for timer delay and webhook wait nodes
**Area**: Timer Delay Executor / Webhook Wait Executor / `engine.go` — `executeWithRetry`
**Fixed**: FA6 Pass 1
**Detail**: Both `TimerDelayExecutor` and `WebhookWaitExecutor` write their own node execution records internally, AND `executeWithRetry` also wrote one after any executor returns. Fixed by skipping the `executeWithRetry` record write when `result.IsWait == true`. Added regression test `TestEngine_WaitResult_ExitsLoopCleanlyNoExtraRecord`.

### ENGINE-004: Missing error case tests in TimerDelayExecutor
**Area**: Timer Delay Executor (`domain/engine/executor_timer_test.go`)
**Fixed**: FA4 Pass 1
**Detail**: No tests for (a) context missing instance ID (returns immediate error), (b) `SetWaiting` DB failure (returns wrapped error, scheduler NOT called). Added 2 tests.

### QUEUE-006: Dead code TimerSchedulerForRecovery interface in recovery.go
**Area**: Crash Recovery (`domain/queue/recovery.go`)
**Fixed**: FA3 Pass 1
**Detail**: `TimerSchedulerForRecovery` interface was defined but never used — `RecoverTimers` and `RecoverWebhooks` both take `*Scheduler` as a concrete type. Additionally, the interface's `Schedule` signature (`instanceID interface{ String() string }`) doesn't match `Scheduler.Schedule(uuid.UUID, ...)`, making it unreachable. Removed the interface entirely.

### QUEUE-007: Silent error suppression in RecoverTimers and RecoverWebhooks
**Area**: Crash Recovery (`domain/queue/recovery.go`)
**Fixed**: FA3 Pass 1
**Detail**: Three issues: (1) `RecoverTimers` had a comment "Log error but continue recovery" on its `SetQueued` failure branch but no actual log call, leaving failures invisible. (2) `RecoverWebhooks` timed-out path swallowed `SetWebhookFields` and `SetFailed` errors with `_ =`. Fixed by adding `slog.Error` calls to all three sites.

### QUEUE-008: Missing recovery edge-case tests
**Area**: Crash Recovery (`domain/queue/recovery_test.go`)
**Fixed**: FA3 Pass 1
**Detail**: No tests for: (a) nil `WaitingUntil` in `RecoverTimers` (should skip gracefully), (b) `SetQueued` failure in `RecoverTimers` (error logged, not propagated, other timers still processed), (c) nil `WebhookToken` in `RecoverWebhooks` (should skip), (d) webhook wait node with no timeout configured (re-registers only, no scheduler entry), (e) no node execution record found for a timed-out webhook (no scheduler entry, no crash). Added 5 tests.

### QUEUE-004: Handler errors silently dropped in Scheduler.processExpired
**Area**: Timer Scheduler (`domain/queue/scheduler.go`)
**Fixed**: FA2 Pass 1
**Detail**: `processExpired` discarded `OnTimerFired` errors with `_ =`. Since the timer entry is already removed from the map before the handler is called, a handler failure (e.g., DB write failure in `SetFailed`) leaves the instance permanently stuck in `waiting` with no active timer and no log evidence. Fixed by logging the error via `slog.Error` so operators can detect the stuck instance.

### QUEUE-005: Missing scheduler edge-case tests
**Area**: Timer Scheduler (`domain/queue/scheduler_test.go`)
**Fixed**: FA2 Pass 1
**Detail**: No tests for: (a) context cancellation stopping the loop goroutine, (b) past-due timer scheduled with negative duration fires in ≤1ms, (c) re-scheduling an existing entry (same instanceID, new deadline) correctly updates the deadline, (d) handler error resilience (scheduler continues processing remaining expired timers after one handler returns an error). Added 4 new tests.

### QUEUE-003: Missing test coverage in Queue Manager
**Area**: Queue Manager (`domain/queue/manager_test.go`)
**Fixed**: FA1 Pass 1
**Detail**: No tests for (a) DB error paths in `CanStart` (`CountRunning` error, `CountRunningByOwner` error), (b) `NextEligible` error in `Evaluate`, (c) `StartInstance` failure in `Enqueue`, (d) `CanStart` error propagation in `Enqueue`, (e) `OnTimerFired` with `TimerTypeDelay` (both the happy path and the not-waiting no-op), (f) `SetFailed` error propagation in the webhook timeout path, (g) `UpdateConfig` entirely. Added 9 new tests covering all these cases._

<!--
Example of a fixed issue entry:

### QUEUE-001: Race between timer fire and cancel
**Area**: Timer Scheduler (`domain/queue/scheduler.go`)
**Fixed**: Iteration N
**Detail**: A race existed where Cancel() could be called after the timer fired but before the transition completed, leaving the instance in an inconsistent state. Added a state check in OnTimerFired to verify the instance is still in waiting state before transitioning.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### QUEUE-W01: No distributed locking for multi-instance deployment
**Area**: Queue Manager (`domain/queue/manager.go`)
**Detail**: The queue evaluation logic assumes a single process. In a multi-instance deployment, two processes could dequeue the same instance. This is out of scope for Phase 2 which targets single-user local mode.
-->
