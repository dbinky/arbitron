# Phase 2, Task 6: API Changes (waitingUntil Field) + Integration Tests

> **Historical note (added 2026-06-02):** This document predates Arbitron's migration from Postgres to SQLite. All Postgres references below are obsolete — the live persistence layer is the SQLite adapter (`adapter/sqlite/`, configured via `database.path`, default `./arbitron.db`). Retained as a historical record.

## Objective
Add the `waitingUntil` field to instance API responses and write integration tests covering the full timer delay lifecycle.

## Dependencies
- Phase 2, Tasks 1-5 (all timer delay infrastructure must be complete)

## Files to Create/Modify
- `adapter/http/instance_handler.go` — add `waitingUntil` field to response
- `adapter/postgres/instance_repo.go` — ensure `WaitingUntil` is read/written in Get/List queries _(Postgres has since been replaced by SQLite.)_
- `tests/integration/timer_delay_test.go` — end-to-end timer delay tests

## Implementation Steps

1. **Add waitingUntil to instanceResponse** in `adapter/http/instance_handler.go`:
   ```go
   type instanceResponse struct {
       // ... existing fields ...
       QueuedAt     *string `json:"queuedAt,omitempty"`
       WaitingUntil *string `json:"waitingUntil,omitempty"`
   }
   ```

2. **Update toInstanceResponse** to populate the new field:
   ```go
   func toInstanceResponse(inst *instance.Instance) instanceResponse {
       resp := instanceResponse{
           // ... existing mappings ...
       }
       if inst.QueuedAt != nil {
           s := inst.QueuedAt.Format("2006-01-02T15:04:05Z07:00")
           resp.QueuedAt = &s
       }
       if inst.WaitingUntil != nil {
           s := inst.WaitingUntil.Format("2006-01-02T15:04:05Z07:00")
           resp.WaitingUntil = &s
       }
       return resp
   }
   ```

3. **Update Postgres queries** — ensure the `GetInstance` and `ListInstances` sqlc queries include `waiting_until` in their SELECT and that the mapping code populates `Instance.WaitingUntil`. _(Postgres has since been replaced by SQLite.)_

4. **Update sqlc query for CreateInstance** — include `waiting_until` parameter for instances that start in waiting state (or null for normal creation).

5. **Write integration tests** in `tests/integration/timer_delay_test.go`:

   ```go
   func TestTimerDelay_FullFlow(t *testing.T) {
       // Create workflow with: entrypoint → timer_delay(2s) → script_node
       // Trigger the workflow
       // Verify: instance goes running → executes entrypoint → reaches timer_delay → waiting
       // Verify: waitingUntil is set correctly (now + 2s)
       // Verify: capacity is freed (a second instance can start)
       // Wait for timer to fire (sleep 2.5s or use polling)
       // Verify: instance transitions waiting → queued → running
       // Verify: script_node executes after the timer delay
       // Verify: instance completes
   }

   func TestTimerDelay_WithPause(t *testing.T) {
       // Trigger workflow with timer_delay(5s)
       // Wait until instance is in waiting state
       // Pause the instance
       // Verify state is paused
       // Wait for timer to expire (>5s from start)
       // Resume the instance
       // Verify: instance transitions to queued (timer expired while paused)
       // Verify: instance continues execution
   }

   func TestTimerDelay_WithCancel(t *testing.T) {
       // Trigger workflow with timer_delay(60s)
       // Wait until instance is in waiting state
       // Cancel the instance
       // Verify: state is cancelled
       // Verify: timer does not fire (wait briefly, instance stays cancelled)
   }

   func TestTimerDelay_CrashRecovery(t *testing.T) {
       // Create an instance directly in the database in waiting state
       // Set waiting_until to 1 second ago (past-due)
       // Call RecoverTimers
       // Verify: instance transitions to queued
       // Verify: queue evaluation starts the instance (if capacity available)
   }

   func TestTimerDelay_ZeroDuration(t *testing.T) {
       // Trigger workflow with timer_delay(0)
       // Verify: instance goes to waiting with immediate waitingUntil
       // Verify: scheduler fires immediately (or nearly so)
       // Verify: instance proceeds through queued → running → continues
   }

   func TestTimerDelay_SubstitutionVariable(t *testing.T) {
       // Trigger workflow with timer_delay("{{delay}}") and variables: {"delay": "2"}
       // Verify: resolved to 2 seconds, waitingUntil set correctly
   }

   func TestTimerDelay_NonNumericDuration(t *testing.T) {
       // Trigger workflow with timer_delay("{{delay}}") and variables: {"delay": "abc"}
       // Verify: node fails per error behavior
       // Verify: instance fails (if no error handling configured)
   }

   func TestTimerDelay_APIResponse_IncludesWaitingUntil(t *testing.T) {
       // Trigger workflow with timer_delay
       // GET /instances/{id}
       // Verify: response includes "waitingUntil" field with correct ISO timestamp
   }
   ```

## Tests
All integration tests listed in implementation steps above.

## Acceptance Criteria
- `GET /instances/{id}` response includes `waitingUntil` field when instance is waiting on timer
- `waitingUntil` is null/omitted for instances not in timer wait
- Full timer delay lifecycle works end-to-end: trigger → run → timer_delay → waiting → timer fires → queued → running → continue
- Pause during wait + resume after expiry correctly transitions to queued
- Cancel during wait correctly cancels the timer and stops the instance
- Crash recovery correctly handles past-due and pending timers
- Variable substitution works for dynamic durations
- Non-numeric durations fail the node per error behavior
- All integration tests pass with `-race` flag
- Tests complete within 30 seconds (use short timer durations)
