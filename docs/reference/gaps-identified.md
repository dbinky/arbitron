# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### PANEL-002: No tests for ChatInput, ChatMessage, and InteractiveTranscript components
**Area**: Chat Panel Components (`dashboard/src/components/`)
**Detail**: ChatInput, ChatMessage, and InteractiveTranscript have zero test coverage. ChatInput has testable keyboard behavior (Enter to send, Shift+Enter for newline, empty content guard). ChatMessage renders markdown content which has edge cases. These components need tests covering all 5 scenario categories.

## Fixed Previously

### PANEL-001: Missing typing indicator when streaming starts (spec-required feature)
**Area**: Chat Panel Components (`dashboard/src/components/InteractivePanel.tsx`)
**Fixed**: Iteration 14
**Detail**: The design spec requires a pulsing "Thinking..." indicator when `isStreaming=true` but no tokens have arrived yet. `InteractivePanel` only used `isStreaming` to disable the input — users had no feedback for several seconds until the first token arrived. Fixed: added typing indicator (pulsing dot + "Thinking..." text with CSS keyframe animation) shown when `isStreaming && !streamingContent`. Also fixed the empty state condition to exclude the `isStreaming` case. Added 15 tests for `InteractivePanel` covering all 5 scenario categories.

### HOOK-001: No test framework and streaming state not cleared on interactive.error
**Area**: useInteractiveSession Hook (`dashboard/src/hooks/useInteractiveSession.ts`)
**Fixed**: Iteration 13
**Detail**: The dashboard had no test runner configured. Additionally, the `interactive.error` event handler did not clear `isStreaming` or `streamingContent` — if an error fired mid-stream, the UI would show a typing indicator alongside an error message simultaneously. Set up vitest + @testing-library/react + jsdom with 16 tests covering all 5 scenario categories. The streaming-state test failed first (proving the bug), then fixed by adding `setIsStreaming(false)` and `setStreamingContent('')` to the error handler.

### INT-001: No integration test for EndSession (interactive.end) error path
**Area**: Integration Tests (`adapter/http/interactive_integration_test.go`)
**Fixed**: Iteration 12
**Detail**: `TestIntegration_InteractiveError_FlowsBackOverWebSocket` proved that `HandleMessage` errors reach the client as `interactive.error` events, but there was no corresponding test for `EndSession` errors. The `handleInbound` function routes both `interactive.send` and `interactive.end` through the same error-event path (stream_handler.go:203–235). Added `TestIntegration_InteractiveEndSession_ErrorFlowsBack` which wires `endSessionFn` to return `assert.AnError`, then verifies the client receives an `interactive.error` event with a non-empty message field. This closes the gap where a future refactor of the EndSession branch could silently break error propagation.

### REST-001: No test for 500 response when EndSession returns unexpected error
**Area**: InteractiveHandler (`adapter/http/interactive_handler_test.go`)
**Fixed**: Iteration 11
**Detail**: Added `TestInteractiveHandler_EndSession_InternalError` which injects a database-style error (`fmt.Errorf("db: connection reset by peer")`) that matches neither the "not found" nor "not in interactive wait state" patterns. Verifies the handler returns 500 with `"code": "INTERNAL_ERROR"`. Completes the error-response coverage: 400, 404, 409, 500 are all now tested.

### HIST-001: No test for history hydration when node execution record is missing
**Area**: WebSocket StreamHandler (`adapter/http/stream_handler_test.go`)
**Fixed**: Iteration 10
**Detail**: Added `TestStreamHandler_HistoryHydration_NodeExecNotFound_SkipsHistory`. Uses an empty `mockNodeExecRepo` so `GetByInstanceAndNode` returns "not found". Verifies the connection stays open (normal bus event is received as first message) and no `interactive.history` event is sent. Covers the crash-recovery scenario where an instance reaches waiting state but the executor never persisted the node execution record.

### WS-001: Typed-nil interface trap in newStreamTestServerFull masked nil nodeExecRepo guard
**Area**: WebSocket StreamHandler (`adapter/http/stream_handler_test.go`)
**Fixed**: Iteration 9
**Detail**: `newStreamTestServerFull` had parameter type `*mockNodeExecRepo`. Passing `nil` for that parameter created a typed nil, which when assigned to the `instance.NodeExecutionRepository` interface inside `NewStreamHandler` produced a non-nil interface — bypassing the `if h.nodeExecs == nil` nil guard in `sendInteractiveHistory`. Changed the parameter type to `instance.NodeExecutionRepository` so that passing `nil` creates a true nil interface. Added `TestStreamHandler_HistoryHydration_NilNodeExecRepo_SkipsHistory` to prove the guard works for a waiting interactive instance, which previously panicked.

### LLMPX-001: anthropic.Provider had no tests and no compile-time interface assertion
**Area**: LLM Provider Adapter (`adapter/anthropic/provider.go`)
**Fixed**: Iteration 8
**Detail**: Added `var _ engine.LLMProvider = (*Provider)(nil)` to `provider.go` (matching project convention in postgres/assert.go, filesystem/workflow_repo.go). Created `adapter/anthropic/provider_test.go` with 4 tests covering: empty key → `ErrNotConfigured` for both `Call` and `StreamCall`, non-empty key → stub error (not ErrNotConfigured) for both methods, and a package-level compile-time assertion. Prevents silent interface drift when new methods are added to `engine.LLMProvider`.

### NOTIF-001: Missing test for ExternalNotifier error non-propagation in InteractiveExecutor
**Area**: InteractiveExecutor (`domain/engine/executor_interactive_test.go`)
**Fixed**: Iteration 7
**Detail**: When `externalNotifier.Notify` returns an error, the executor logs it but must not propagate it — the instance must still transition to waiting and return `IsWait: true`. Added `notifyFn` hook to `mockExternalNotifierForInteractive` (matching the engine's mock pattern) and added `TestInteractiveExecutor_ExternalNotifier_ErrorDoesNotAbortExecution` to verify the executor returns `IsWait: true` and the instance reaches `StateWaiting` even when the notifier fails.

### CONV-002: Missing edge test for empty conversation history in HandleMessage
**Area**: ConversationHandler (`domain/engine/conversation_test.go`)
**Fixed**: Iteration 6
**Detail**: Added `TestConversationHandler_HandleMessage_NilConversationHistory_StartsFirstMessage`. Clears `LLMConversation` on the existing nodeExec to nil, then verifies: no error, LLM receives exactly 1 message (the user message), and the conversation is persisted with user + assistant. Covers the crash-recovery scenario where an instance is in waiting state but the executor never wrote its conversation record.

### CONV-001: Missing error tests for `GetByInstanceAndNode` failure in ConversationHandler
**Area**: ConversationHandler (`domain/engine/conversation_test.go`)
**Fixed**: Iteration 5
**Detail**: Added `TestConversationHandler_HandleMessage_NodeExecNotFound_ReturnsError` and `TestConversationHandler_EndSession_NodeExecNotFound_ReturnsError`. Both use an empty nodeExec repo so `GetByInstanceAndNode` returns "not found", verifying the error message contains "load node execution".

### EXEC-002: Missing test for nil external notifier in InteractiveExecutor
**Area**: InteractiveExecutor (`domain/engine/executor_interactive_test.go`)
**Fixed**: Iteration 4
**Detail**: Added `TestInteractiveExecutor_NilExternalNotifier_DoesNotPanic` which passes `nil` as the ExternalNotifier and verifies no panic occurs, IsWait is true, the notification is still created in the in-app repo, and the instance transitions to waiting — confirming all non-external side effects still complete correctly.

### EXEC-001: Missing test for `UpdateState()` failure in InteractiveExecutor
**Area**: InteractiveExecutor (`domain/engine/executor_interactive_test.go`)
**Fixed**: Iteration 3
**Detail**: The executor returns an error when `instances.UpdateState()` fails but no test covered this path. Added `TestInteractiveExecutor_UpdateStateFailure_ReturnsError` which uses an empty instance repo (so UpdateState returns "instance not found") to verify that the error propagates correctly with the expected message.

### WFVAL-001: `validateFileReferences` did not check `OrchestrationFile` for interactive nodes
**Area**: Interactive Node Type (`domain/workflow/validation.go`)
**Fixed**: Iteration 1
**Detail**: The `validateFileReferences` function validated `TemplateFile` and `ScriptFile` but silently ignored `OrchestrationFile`. An interactive node with a nonexistent orchestration file would pass validation and fail at runtime in the executor. Added the check alongside the existing file checks, plus two new tests: one verifying the error is produced when the file is missing (with a FileChecker), and one verifying the check is skipped when the checker is nil._

<!--
Example of a fixed issue entry:

### EXEC-001: InteractiveExecutor missing error handling for empty orchestration file
**Area**: InteractiveExecutor (`domain/engine/executor_interactive.go`)
**Fixed**: Iteration 3
**Detail**: The executor did not check for empty orchestration file content after reading. Added validation that returns a descriptive error. Added test case for empty file content.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### CONV-W01: No rate limiting on interactive message sends
**Area**: ConversationHandler (`domain/engine/conversation.go`)
**Detail**: Users can send messages as fast as they want during an interactive session. Rate limiting would prevent abuse but is an enhancement beyond the Phase 4 scope.
-->
