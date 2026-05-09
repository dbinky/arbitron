# Focus Areas Review Tracking

This document tracks the systematic review of the Arbitron Phase 4 Interactive Steps implementation for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

**Progression order:** Complete all Pass 1 single area reviews first. Then do paired area reviews (which catch integration issues while the code is fresh). Then do Pass 2 single area reviews.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Interactive Node Type (`domain/workflow/workflow.go` — NodeTypeInteractive constant, config fields, validation for interactive nodes) | [x] | [x] | Done |
| 2 | InteractiveExecutor (`domain/engine/executor_interactive.go`, `domain/engine/executor_interactive_test.go` — initial LLM call, streaming, notification creation, IsWait transition) | [x] | [x] | Done |
| 3 | ConversationHandler (`domain/engine/conversation.go`, `domain/engine/conversation_test.go` — HandleMessage, EndSession, tool-use detection, state transitions) | [x] | [x] | Done |
| 4 | External Notification Interface (`domain/notification/external.go`, `adapter/notification/log_notifier.go`, `adapter/notification/log_notifier_test.go` — interface, logging adapter, engine integration) | [x] | [x] | Done |
| 5 | LLM Provider Extensions (`domain/engine/ports.go` — LLMRequest.SystemPrompt, LLMRequest.Tools, LLMRequest.Messages, LLMStreamChunk.ToolUse, LLMTool, LLMMessage types) | [x] | [x] | Done |
| 6 | WebSocket Bidirectional Messaging (`adapter/http/stream_handler.go`, `adapter/http/stream_handler_test.go` — inbound message routing, write mutex, interactive.send/end handling) | [x] | [ ] | Pass 1 complete |
| 7 | History Hydration & Instance Detail (`adapter/http/stream_handler.go`, `adapter/http/instance_handler.go` — interactive.history on connect, interactiveActive/interactiveNodeId fields) | [x] | [ ] | Pass 1 complete |
| 8 | Interactive REST Endpoint (`adapter/http/interactive_handler.go`, `adapter/http/interactive_handler_test.go` — POST /interact/end, error responses, router wiring) | [x] | [ ] | Pass 1 complete |
| 9 | Integration Tests (`adapter/http/interactive_integration_test.go` — full round-trip WebSocket tests, tool completion, error flows, multi-message) | [x] | [ ] | Pass 1 complete |
| 10 | useInteractiveSession Hook (`dashboard/src/hooks/useInteractiveSession.ts` — state management, event handling, streaming-to-message transition, send/end actions) | [x] | [ ] | Pass 1 complete |
| 11 | Chat Panel Components (`dashboard/src/components/InteractivePanel.tsx`, `ChatMessage.tsx`, `ChatInput.tsx` — panel layout, markdown rendering, input behavior, auto-scroll) | [x] | [ ] | Pass 1 complete |
| 12 | Callout Connector & Panel Resizing (`dashboard/src/graph/CalloutConnector.tsx`, `dashboard/src/components/ResizablePanel.tsx`, `InteractiveCallout.tsx` — SVG overlay, 40/60 split, snap-back) | [x] | [ ] | Pass 1 complete |
| 13 | Execution View Integration (`dashboard/src/routes/ExecutionView.tsx`, `dashboard/src/components/SidePanel.tsx` — conditional rendering, auto-open, transcript viewing) | [x] | [ ] | Pass 1 complete |
| 14 | Main Wiring (`cmd/arbitron/main.go` — executor registration, ConversationHandler instantiation, router wiring, dependency injection) | [x] | [ ] | Pass 1 complete |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 15 | InteractiveExecutor + ConversationHandler (conversation data format consistency, llm_conversation JSONB shape agreement, tool definition sharing) | [x] | Pass complete |
| 16 | ConversationHandler + WebSocket Handler (HandleMessage/EndSession call contracts, error propagation, blocking behavior during streaming) | [x] | Pass complete |
| 17 | WebSocket Handler + useInteractiveSession Hook (event type naming consistency, message payload shapes, inbound/outbound format agreement) | [x] | Pass complete |
| 18 | InteractiveExecutor + Engine Loop (IsWait pattern correctness, node execution record ownership, state transition consistency with timer/webhook) | [x] | Pass complete |
| 19 | External Notification + Engine (emitNotification integration, ExternalNotifier called for all notification types, nil-safety) | [x] | Pass complete |
| 20 | History Hydration + useInteractiveSession (interactive.history event shape, message array format, auto-open behavior on connect) | [x] | Pass complete |
| 21 | Chat Panel + Callout Connector (panel takeover triggers connector visibility, snap-back removes connector, position tracking across scroll/zoom) | [x] | Pass complete |
| 22 | LLM Provider Extensions + InteractiveExecutor + ConversationHandler (SystemPrompt/Tools/Messages fields used correctly across both, tool-use streaming detection) | [x] | Pass complete |
| 23 | Spec Alignment: Product Spec + All Backend (every spec requirement has corresponding implementation, no scope creep, no missing behaviors) | [x] | Pass complete |
| 24 | Spec Alignment: Product Spec + All Dashboard (every UI spec requirement has corresponding implementation, side panel behavior matches spec) | [x] | Pass complete |

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
- The component is ready to be extended by future work
- Test assertions test real risks, not just happy-path ceremony
- Edge cases are handled (empty inputs, concurrent access, boundary values)

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary
- Error propagation works end-to-end
- No assumptions in one component that the other doesn't satisfy
- The integration tests cover the seams between the pair
