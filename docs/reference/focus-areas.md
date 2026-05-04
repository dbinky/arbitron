# Focus Areas Review Tracking

This document tracks the systematic review of the Arbitron Phase 1 workflow engine and REST API for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

**Progression order:** Complete all Pass 1 single area reviews first. Then do paired area reviews (which catch integration issues while the code is fresh). Then do Pass 2 single area reviews.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Domain Workflow Types & Validation (`domain/workflow/` — type definitions, JSON tags, graph validation, BFS connectivity, input schema validation, file reference checking) | [x] | [x] | |
| 2 | Domain Instance Types & Ports (`domain/instance/` — state enums, instance/execution/snapshot types, repository interface contracts, filter types) | [x] | [x] | |
| 3 | Variable Store & Substitution (`domain/variable/` — dot-path resolution, input/nodeOutput scoping, snapshot round-tripping, `{{}}` pattern matching, value formatting) | [x] | [x] | |
| 4 | Node Executors: Conditional & Transform (`domain/engine/executor_conditional.go`, `executor_transform.go` — condition evaluation, operator semantics, type coercion, variable mutation, operation ordering) | [x] | [x] | |
| 5 | Node Executors: Script, Tool Call & LLM (`domain/engine/executor_script.go`, `executor_toolcall.go`, `executor_llm.go` — subprocess management, timeout handling, stdin/stdout, JSON parsing, template substitution, provider delegation) | [x] | [x] | |
| 6 | Engine Graph Walker & Lifecycle (`domain/engine/engine.go` — adjacency map, execution loop, error behavior resolution, retry logic, pause/cancel/resume/retry/rerun, crash recovery, goroutine management) | [x] | [x] | |
| 7 | Configuration & Config Loader (`domain/config/`, `adapter/config/` — config struct, koanf loading, YAML/env var precedence, ANTHROPIC_API_KEY mapping, default file creation) | [x] | [x] | |
| 8 | Filesystem Adapters (`adapter/filesystem/` — workflow repo loading/listing, file reader read/resolve/exists, error handling for missing files, testdata fixtures) | [x] | [x] | |
| 9 | Database Migrations & Postgres Adapters (`adapter/postgres/` — migration SQL, enum types, FK constraints, indexes, sqlc queries, type conversion helpers, repo implementations) | [x] | [x] | |
| 10 | REST API: Workflow Handlers (`adapter/http/workflow_handler.go`, `errors.go` — list/get/validate/trigger endpoints, error response format, input validation, status codes) | [x] | [x] | |
| 11 | REST API: Instance Handlers (`adapter/http/instance_handler.go` — CRUD, lifecycle operations, trace/variables/history endpoints, UUID parsing, state conflict handling) | [x] | [x] | |
| 12 | Event Bus & WebSocket Streaming (`adapter/eventbus/eventbus.go`, `adapter/http/stream_handler.go` — publish/subscribe, buffer overflow, client lifecycle, terminal state handling, WebSocket upgrade) | [x] | [x] | |
| 13 | Observability: Logging & Tracing (`adapter/observability/` — MultiHandler, JSON+text fan-out, level filtering, OTel tracer provider, file exporter, span attributes, request logging middleware) | [x] | [x] | |
| 14 | Bootstrap & CLI (`cmd/arbitron/main.go` — cobra setup, dependency wiring, startup sequence, graceful shutdown, crash recovery invocation) | [x] | [x] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 15 | Engine + Node Executors (executor map wiring, NodeResult handling, error propagation from executors to engine, output port routing) | [x] | |
| 16 | Engine + Instance Ports (state transitions match port interface, variable updates persisted correctly, node execution recording, snapshot timing) | [x] | |
| 17 | Engine + Variable Store (substitution called at correct points, node output namespacing, snapshot/fromSnapshot round-trip through engine lifecycle) | [x] | |
| 18 | REST API Handlers + Engine Service Interface (WorkflowService/InstanceService contracts match engine methods, error string mapping to HTTP codes, request/response shapes) | [x] | |
| 19 | REST API Handlers + Instance Repos (handler queries match repo interface, filter params wired correctly, UUID parsing before repo calls, 404 vs 409 distinction) | [x] | |
| 20 | WebSocket Handler + Event Bus (subscribe/unsubscribe lifecycle during connection, event serialization, terminal state detection and close, concurrent access safety) | [x] | |
| 21 | Workflow Validation + FileReader (FileChecker interface subset, validation calls FileExists correctly, nil checker skip path, error codes propagated) | [x] | |
| 22 | Postgres Adapters + Domain Types (type conversion correctness, JSONB marshaling/unmarshaling, nullable field handling, enum string mapping, original_variables preserved) | [x] | |
| 23 | Bootstrap + All Adapters (dependency injection order, nil checks, adapter interface satisfaction, graceful shutdown sequence covers all resources) | [x] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy path, failure, error, edge cases)
- The code does what the design doc says it should
- Error handling is thorough — no silent failures
- Module boundaries are clean (no leaking types across boundaries)

**Pass 2 (Robustness & Extensibility):**
- Code aligns to the spirit of the design, not just surface conformance
- The component is ready to be extended by future work (Phase 2-8)
- Test assertions test real risks, not just happy-path ceremony
- Edge cases are handled (empty inputs, concurrent access, boundary values)

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary
- Error propagation works end-to-end
- No assumptions in one component that the other doesn't satisfy
- The integration tests cover the seams between the pair
