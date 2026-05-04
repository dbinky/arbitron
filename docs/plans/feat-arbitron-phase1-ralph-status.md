# Progress

## Phase Status
- [x] FA1–FA14 Pass 1 (single-area correctness reviews) — COMPLETE
- [x] FA15–FA23 Paired area reviews — COMPLETE  
- [ ] FA1–FA14 Pass 2 (single-area robustness/extensibility reviews) — NEXT

## Remaining
- [ ] Implement Anthropic LLM provider (adapter/anthropic/provider.go is a stub — LLM nodes always fail)
- [ ] Verify DB context propagation (repo methods use context.Background() — low priority for Phase 1 single-user mode)
- [ ] Engine goroutine graceful shutdown (goroutines launched by `go e.run(...)` are not cancelled on SIGTERM; pool can close before goroutines finish)
- [ ] FA1–FA14 Pass 2: robustness and extensibility review pass

## Completed (Iteration 2)
- [x] Fix: `SetInstanceFailed` SQL missing `completed_at = now()` — CompletedAt was always nil on failed instances
- [x] Fix: `InstanceRepo.Get` and `WorkflowRepo.Load` not producing "not found" errors — all 404s from those adapters were becoming 500s
- [x] Fix: `NodeExecutionRepo.GetByInstanceAndNode` not producing "not found" errors — `GetNodeTrace` 404 was becoming 500
- [x] Fix: `GetNodeTrace` missing instance pre-validation — invalid instance ID returned node-not-found instead of instance-not-found
- [x] Fix: `Engine.Rerun` missing terminal state guard — any state accepted for rerun
- [x] Fix: `Engine.executeWithRetry` never populated `ResolvedInputs` — trace endpoint always returned null for resolvedInputs
- [x] Fix: tracing initialized after crash recovery — OTel spans from recovery were silently dropped
- [x] Fix: postgres compile-time assertions only in test file — moved to `adapter/postgres/assert.go` so enforced by `go build`
- [x] Add: `WorkflowRepo` compile-time interface assertion in `workflow_repo.go`
- [x] Add: `FileReaderAdapter` satisfies `workflow.FileChecker` compile-time assertion
- [x] Add: `FileChecker` workflowName forwarding test in `validation_test.go`
- [x] Add: `fromPgTimestamptz` unit tests (missing entirely)
- [x] Add: timestamp round-trip test
- [x] Add: `StateCancelled` enum integration test
- [x] Add: `UpdateVariables` preserves `OriginalVariables` integration test
- [x] Add: fully-populated `NodeExecution` round-trip integration test (LLM + script fields)
- [x] Add: `GetByInstanceAndNode` multi-attempt integration test (verifies ORDER BY attempt DESC)
- [x] Add: `GetByInstanceAndNode` not-found integration test
- [x] Add: `TestStreamHandler_InstanceRepoError` (500 before WebSocket upgrade)
- [x] Add: `TestInstanceHandler_GetNodeTrace_InstanceNotFound` and `GetNodeTrace_InstanceRepoError`
- [x] Paired reviews FA15–FA23 all complete

## Completed (Iteration 1)
- [x] Audit codebase against spec and identify improvements
- [x] Fix: WebSocket connection never closes on workflow completion — `isTerminalEvent` now recognises `instance.completed`, `instance.failed`, `instance.cancelled`
- [x] Fix: NodeExecution `Attempt` always 1 — `executeWithRetry` now creates a per-attempt record with correct attempt number
- [x] Fix: `CompletedAt` always nil — timing now captured inside `executeWithRetry` around each executor call
- [x] Fix: Skip error behavior status not persisted — `run()` now calls `nodeExecutions.Update` after changing status to `skipped`
- [x] Fix: `FromSnapshot` misclassifies object-typed inputs as node outputs — added `FromSnapshotWithNodeIDs`; engine now passes node ID set for correct classification
- [x] Add: node-level events `node.started`, `node.completed`, `node.failed` published to event bus (spec requirement for WebSocket streaming)
- [x] Fix: `GetTrace` and `GetVariableHistory` now 404 when instance doesn't exist (previously returned empty results)
- [x] Add: missing HTTP handler test cases — Resume, Cancel, Retry, Rerun all now have 400/404 coverage; Retry has malformed JSON body test; GetTrace, GetNodeTrace, GetVariables, GetVariableHistory have complete bad-UUID and not-found coverage

## Discovered
- Anthropic provider is an explicit stub — LLM nodes will return an error in production regardless of API key
- `LLMProvider.StreamCall` is defined but never called from `LLMCallExecutor` — streaming path is dead code
- DB repo methods use `context.Background()` instead of request context — acceptable for Phase 1 single-user local mode
- `GET /instances` list response has no total count for pagination
- `queued` state is never used — instances go directly from trigger to `running` (matches Phase 1 spec: "transition through queued immediately")
- Engine goroutines not awaited on shutdown — `pool.Close()` can fire while goroutines are mid-execution (deferred LIFO ordering)
- `WriteTimeout: 10s` on HTTP server may affect long-lived connections, but `nhooyr.io/websocket` hijacks the connection so this likely only affects non-WebSocket responses
