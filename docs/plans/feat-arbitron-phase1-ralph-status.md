# Progress

## Remaining
- [ ] Implement Anthropic LLM provider (adapter/anthropic/provider.go is a stub — LLM nodes always fail)
- [ ] Verify DB context propagation (repo methods use context.Background() — low priority for Phase 1 single-user mode)

## Completed
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
