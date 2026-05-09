# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_


## Fixed Previously

### CONN-002: CalloutConnector tests missing integration category (visible prop transitions)
**Area**: Chat Panel + Callout Connector (`dashboard/src/graph/CalloutConnector.tsx`)
**Fixed**: Iteration 22
**Detail**: The CalloutConnector tests covered happy path, not-visible, missing node, and geometry guard — but had no integration category test verifying dynamic visible transitions (panel takeover: false→true, snap-back: true→false). These transitions are the core behavior of the pair: when the interactive session starts, the connector must appear; when it ends, the connector must disappear. Added two integration tests using `rerender()` to verify both transitions work correctly.

### HIST-002: useInteractiveSession did not clear streaming state on interactive.history (reconnect gap)
**Area**: History Hydration + useInteractiveSession (`dashboard/src/hooks/useInteractiveSession.ts`)
**Fixed**: Iteration 21
**Detail**: The `interactive.history` handler set `messages`, `nodeId`, `isActive`, and `error`, but did not clear `streamingContent` or `isStreaming`. If a WebSocket reconnect occurred mid-stream, the client would retain the pre-disconnect partial content and the typing indicator (`isStreaming=true`), resulting in a phantom streaming UI alongside the hydrated history. Fixed by adding `setStreamingContent('')` and `setIsStreaming(false)` to the history handler. Test: `'clears streaming state when history arrives mid-stream (reconnect)'`.

### NOTIF-002: Zero CreatedAt on notifications forwarded to external notifier and event bus
**Area**: External Notification + Engine (`domain/engine/engine.go`, `domain/engine/executor_interactive.go`)
**Fixed**: Iteration 20
**Detail**: Both `emitNotification` (for TypeFailed/TypeCompleted) and `InteractiveExecutor` (for TypeNeedsInteraction) created `notification.Notification` structs without setting `CreatedAt`. The Postgres `Create` method uses a DB default (`NOW()`) but does not read it back, so the in-memory struct's `CreatedAt` remains at zero value (`time.Time{}`). This zero value was then serialized as `"0001-01-01T00:00:00.000Z"` in both the `notification.created` event payload (used by the frontend) and in external notifier calls (e.g., LogNotifier logs a bogus timestamp). Fixed by setting `CreatedAt: time.Now()` on notification creation in both code paths. Tests: `TestEngine_ExternalNotifier_NotificationHasNonZeroCreatedAt` and `TestInteractiveExecutor_ExternalNotifier_NotificationHasNonZeroCreatedAt`.

### PAIR15-001: complete_session tool name duplicated as string literal in 3 production-code locations
**Area**: InteractiveExecutor + ConversationHandler (`domain/engine/executor_interactive.go`, `domain/engine/conversation.go`, `domain/engine/ports.go`)
**Fixed**: Iteration 19
**Detail**: The `"complete_session"` tool name was a string literal in (1) the executor's tool definition, (2) the conversation handler's tool definition, and (3) the conversation handler's detection check `toolUse.Name == "complete_session"`. A mismatch would cause session completion to silently break — the LLM calls the tool but the handler never detects it, leaving the session in waiting state indefinitely. Added `InteractiveCompletionToolName = "complete_session"` and `InteractiveCompletionToolDescription = "..."` constants to `ports.go` alongside the existing EventType constants. Updated both executor and handler to use the constants.

### MAIN-001: Missing compile-time assertions for *InteractiveExecutor / NodeExecutor and *LogNotifier / ExternalNotifier
**Area**: Main Wiring (`domain/engine/executor_interactive.go`, `adapter/notification/log_notifier.go`)
**Fixed**: Iteration 18
**Detail**: Project convention (established in `adapter/postgres/assert.go`, `adapter/anthropic/provider.go`, `adapter/filesystem/workflow_repo.go`) is to add `var _ Interface = (*Impl)(nil)` in each implementation file so interface drift is caught at compile time with a clear error message. Neither `*InteractiveExecutor` (satisfies `engine.NodeExecutor`) nor `*LogNotifier` (satisfies `notification.ExternalNotifier`) had these assertions. Added `var _ NodeExecutor = (*InteractiveExecutor)(nil)` to `executor_interactive.go` and `var _ notification.ExternalNotifier = (*LogNotifier)(nil)` to `log_notifier.go`. While usage patterns already provide implicit compile-time checking, the explicit assertions document intent and produce clearer diagnostics on interface changes.

### EXEC-001: No tests for SidePanel — transcript extraction, tab switching, interactive detection
**Area**: Execution View Integration (`dashboard/src/components/SidePanel.tsx`)
**Fixed**: Iteration 17
**Detail**: Added 22 tests for `SidePanel` covering all 5 scenario categories. Key scenarios: `extractTranscript` with all 3 code paths (output.interactiveMessages, llmConversation direct array, llmConversation.messages object), auto-switch to Transcript tab when transcript found, interactive node detection via type ('interactive', 'interactive_call') and config flag, fall-back to output tab when node deselected, empty/null edge cases, tab interaction boundaries. Child components (NodeOutput, VariablesPanel, InteractiveTranscript) mocked to keep tests focused on SidePanel logic.

### CONN-001: No tests for Callout Connector, ResizablePanel, InteractiveCallout components
**Area**: Callout Connector & Panel Resizing (`dashboard/src/graph/CalloutConnector.tsx`, `dashboard/src/components/ResizablePanel.tsx`, `dashboard/src/graph/nodes/InteractiveCallout.tsx`)
**Fixed**: Iteration 16
**Detail**: Added tests for all three components: `InteractiveCallout` (7 tests — renders children, waiting class toggled, rerender removes class, nested children), `ResizablePanel` (8 tests — renders L/R content, divider present, defaultRightWidth applied, fraction width computation, clamping to minRightWidth, snap-back to saved width, drag activates class, boundary constraints), `CalloutConnector` (4 tests via mocked @xyflow/react — SVG rendered when visible+node exists, null when not visible, null when node missing, null when geometry guard fires).

### PANEL-002: No tests for ChatInput, ChatMessage, and InteractiveTranscript components
**Area**: Chat Panel Components (`dashboard/src/components/`)
**Fixed**: Iteration 15
**Detail**: Added 44 tests across ChatInput (11), ChatMessage (7), and InteractiveTranscript (8) covering all 5 scenario categories. ChatInput tests cover Enter-to-send, Shift+Enter newline, whitespace guard, disabled state, and sequential sends. ChatMessage tests cover role labels, markdown rendering (bold, inline code, multi-paragraph), empty content, and distinct role classes. InteractiveTranscript tests cover message rendering, empty/null state, singular/plural count, and role label delegation to ChatMessage.

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
