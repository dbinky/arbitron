# Phase 1, Task 6: Integration Tests (End-to-End Queue Scenarios)

## Objective
Write integration tests that exercise the full queue lifecycle with real database interactions, verifying that capacity enforcement, priority ordering, and queue evaluation work end-to-end.

## Dependencies
- Phase 1, Tasks 1-5 (all queue infrastructure must be complete)

## Files to Create/Modify
- `tests/integration/queue_test.go` — end-to-end queue scenario tests
- `tests/integration/helpers_test.go` — shared test utilities (if not already existing)

## Implementation Steps

1. **Set up test infrastructure** — ensure integration tests can:
   - Start with a clean database (use testcontainers or test database)
   - Create a minimal workflow definition for triggering
   - Configure queue caps per test
   - Access the Engine, Queue Manager, and repositories

2. **Test: Trigger at capacity queues instance**:
   ```go
   func TestQueue_TriggerAtCapacity_InstanceStaysQueued(t *testing.T) {
       // Set globalCap = 1
       // Trigger workflow A → starts immediately (running)
       // Trigger workflow B → stays queued
       // Verify B is in queued state with QueuedAt set
       // Complete A → B automatically starts
       // Verify B transitions to running
   }
   ```

3. **Test: Per-user cap enforcement**:
   ```go
   func TestQueue_PerUserCap_SkipsUserAtCap(t *testing.T) {
       // Set perUserCap = 1, globalCap = 10
       // User A triggers instance 1 → starts (running)
       // User A triggers instance 2 → queued (per-user cap)
       // User B triggers instance 3 → starts (different user, has capacity)
       // Complete instance 1 → instance 2 starts (User A now has room)
   }
   ```

4. **Test: Priority ordering**:
   ```go
   func TestQueue_PriorityOrdering(t *testing.T) {
       // Set globalCap = 1
       // Fill capacity with one running instance
       // Queue 3 instances: low priority (first), normal (second), high (third)
       // Complete the running instance
       // Verify high-priority instance starts first
   }
   ```

5. **Test: FIFO within same priority**:
   ```go
   func TestQueue_FIFOWithinPriority(t *testing.T) {
       // Set globalCap = 1
       // Fill capacity
       // Queue 3 normal-priority instances with different queued_at times
       // Complete running → verify oldest queued_at starts first
   }
   ```

6. **Test: Priority mutation affects ordering**:
   ```go
   func TestQueue_PriorityMutation(t *testing.T) {
       // Set globalCap = 1, fill capacity
       // Queue instance A (normal) then instance B (normal)
       // Change B's priority to high
       // Complete running → B starts (now higher priority than A)
   }
   ```

7. **Test: Cap raised triggers evaluation**:
   ```go
   func TestQueue_CapRaised_StartsQueuedInstances(t *testing.T) {
       // Set globalCap = 1
       // Fill capacity, queue 2 more instances
       // Raise globalCap to 3 via system config endpoint
       // Verify both queued instances start
   }
   ```

8. **Test: Cap lowered graceful drain**:
   ```go
   func TestQueue_CapLowered_GracefulDrain(t *testing.T) {
       // Set globalCap = 3, start 3 instances
       // Lower globalCap to 1
       // All 3 instances continue running (no interruption)
       // Complete instance 1 → no new starts (still at 2, above new cap)
       // Complete instance 2 → no new starts (still at 1, at cap)
       // Queue a new instance → stays queued
       // Complete instance 3 → queued instance starts (now under cap)
   }
   ```

9. **Test: Unlimited cap (0)**:
   ```go
   func TestQueue_UnlimitedCap_AlwaysStarts(t *testing.T) {
       // Set globalCap = 0 (unlimited)
       // Trigger 50 instances → all start immediately
   }
   ```

10. **Test: Full lifecycle**:
    ```go
    func TestQueue_FullLifecycle(t *testing.T) {
        // Trigger → queued → running → completed
        // Verify all state transitions, timestamps, and events
    }
    ```

## Tests
All tests listed in implementation steps above, plus:
- `TestQueue_CancelQueuedInstance` — cancelling a queued instance removes it from the queue
- `TestQueue_PauseRunningInstance_FreesCapacity` — pausing frees a slot, queued instance starts

## Acceptance Criteria
- All integration tests pass against a real Postgres database
- Tests demonstrate correct behavior for: capacity enforcement, priority ordering, FIFO within priority, per-user cap skipping, cap changes, graceful drain, and full lifecycle
- Tests run with `-race` flag without data races
- Each test is independent (no shared state between tests)
- Tests complete within 30 seconds total (no actual time delays — use fast workflows)
