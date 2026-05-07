# Focus Areas Review Tracking

This document tracks the systematic review of the Arbitron Phase 2 Queue & Concurrency implementation for correctness, concurrency safety, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

**Progression order:** Complete all Pass 1 single area reviews first. Then do paired area reviews (which catch integration issues while the code is fresh). Then do Pass 2 single area reviews.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| FA1 | Queue Manager (`domain/queue/manager.go` — capacity checking, next eligible selection, evaluation loop, enqueue, cap semantics) | [x] | [x] | |
| FA2 | Timer Scheduler (`domain/queue/scheduler.go` — hybrid single-timer design, Schedule/Cancel, timer fire flow, graceful shutdown) | [x] | [x] | |
| FA3 | Crash Recovery (`domain/queue/recovery.go` — RecoverTimers, RecoverWebhooks, past-due detection, re-registration) | [x] | [x] | |
| FA4 | Timer Delay Executor (`domain/engine/executor_timer.go` — substitution, duration parsing, state transition, WaitResult, scheduler notification) | [x] | [x] | |
| FA5 | Webhook Wait Executor (`domain/engine/executor_webhook.go` — token generation, HMAC hashing, timeout setup, registry registration, WaitResult) | [x] | [x] | |
| FA6 | Engine Queue Integration (`domain/engine/engine.go` — Trigger refactor, StartInstance, run() loop WaitResult handling, Cancel/Pause/Resume for waiting/webhook states) | [x] | [x] | |
| FA7 | Webhook Registry (`adapter/http/webhook_registry.go` — in-memory map, RWMutex, Register/Lookup/Deactivate lifecycle, concurrency safety) | [x] | [x] | |
| FA8 | Webhook HTTP Handler (`adapter/http/webhook_handler.go` — token lookup, state validation, secret verification, payload parsing, state transitions, error responses) | [x] | [x] | |
| FA9 | API Layer Queue Endpoints (`adapter/http/instance_handler.go`, `adapter/http/server.go` — priority endpoint, trigger 201/202, system config queue section, queue-ordered listing) | [x] | [x] | |
| FA10 | Database Layer (`adapter/postgres/queue_repo.go`, `adapter/postgres/queries/queue.sql`, migrations 005-007 — query correctness, index usage, enum updates) | [x] | [x] | |
| FA11 | Instance Domain Model (`domain/instance/instance.go`, `domain/instance/ports.go` — StateWaiting, QueuedAt, WaitingUntil, Webhook fields, new errors, QueueRepository interface) | [x] | [x] | |
| FA12 | Workflow Validation (`domain/workflow/workflow.go`, `domain/workflow/validation.go` — timer_delay and webhook_wait node types, config fields, validation rules) | [x] | [x] | |
| FA13 | Configuration (`domain/config/config.go`, `adapter/config/loader.go` — QueueConfig, defaults 20/5, env var overrides, config update persistence) | [x] | [x] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| FA14 | Queue Manager + Engine (enqueue flow, capacity notification via NotifyCapacityFreed, StartInstance contract, Trigger→Enqueue→Evaluate chain) | [x] | |
| FA15 | Timer Scheduler + Timer Delay Executor (Schedule interface contract, timer type constants, instanceID in context, WaitingUntil calculation agreement) | [x] | |
| FA16 | Timer Scheduler + Webhook Timeout (same scheduler for webhook timeouts, TimerTypeWebhook discrimination, deactivate-on-fire flow) | [x] | |
| FA17 | Webhook Registry + Webhook Handler (Register/Lookup/Deactivate contract, entry struct fields, concurrency between registration and lookup) | [x] | |
| FA18 | Webhook Handler + Engine (state transition correctness for waiting+paused, Resume logic for webhook-received-while-paused, Cancel deactivating tokens) | [x] | |
| FA19 | Queue Manager + Crash Recovery (RecoverTimers/RecoverWebhooks → scheduler registration → evaluation pass, startup ordering) | [x] | |
| FA20 | API Layer + Queue Manager (trigger capacity check path, priority mutation → no re-evaluation needed, PUT config → cap raise triggers evaluation) | [x] | |
| FA21 | Engine + Instance Model (SetWaiting/SetWebhookFields/SetQueued usage correctness, state transition preconditions, field nullability) | [x] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy path, failure, error, edge cases)
- The code does what the spec and design docs say it should
- Error handling is thorough — no silent failures, errors wrapped with context
- Concurrency is safe — shared state protected, no data races under `-race`
- Module boundaries are clean (no leaking types across boundaries)

**Pass 2 (Robustness & Extensibility):**
- Code aligns to the spirit of the design, not just surface conformance
- The component is ready to be extended by future phases (Phase 3 dashboard, Phase 4 interactive)
- Test assertions test real risks, not just happy-path ceremony
- Edge cases are handled (zero/negative durations, concurrent pause+timer fire, cap changes during evaluation)
- Graceful shutdown paths work correctly

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary (types match, nullability agrees)
- Error propagation works end-to-end (one side's error triggers correct behavior in the other)
- No assumptions in one component that the other doesn't satisfy
- The integration tests cover the seams between the pair
- Concurrency interactions are safe (e.g., timer fires while webhook handler is processing)
