# Progress

## Remaining
- [ ] Implement Anthropic LLM provider (adapter/anthropic/provider.go is a stub — LLM nodes always fail)
- [ ] Verify DB context propagation (repo methods use context.Background())
- [ ] Add node-level events to engine event bus (node_started, node_completed for WebSocket streaming)
- [ ] Audit adapter/http handler tests for coverage gaps

## Completed
- [x] Audit codebase against spec and identify improvements
- [x] Fix: WebSocket connection never closes on workflow completion — `isTerminalEvent` now recognises `instance.completed`, `instance.failed`, `instance.cancelled` (not just `state_changed`)
- [x] Fix: NodeExecution `Attempt` always 1 — `executeWithRetry` now creates a per-attempt record with correct attempt number (1, 2, 3…)
- [x] Fix: `CompletedAt` always nil — timing now captured inside `executeWithRetry` around each executor call
- [x] Fix: Skip error behavior status not persisted — `run()` now calls `nodeExecutions.Update` after changing status to `skipped`
- [x] Fix: `FromSnapshot` misclassifies object-typed inputs as node outputs — added `FromSnapshotWithNodeIDs`; engine now passes node ID set for correct classification; `DeleteInput` on object inputs now works after pause/resume/crash-recovery

## Discovered
- Anthropic provider is an explicit stub (comment says "full implementation will be added in a future phase") — LLM nodes will return an error in production regardless of API key
- `LLMProvider.StreamCall` is defined but never called from `LLMCallExecutor` — streaming path is dead code
- DB repo methods use `context.Background()` instead of request context — DB calls cannot be cancelled by HTTP request cancellation
- `GET /instances` list response has no total count for pagination
- `queued` state is never used — instances go directly from trigger to `running`
