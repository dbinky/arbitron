# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_



## Fixed Previously

- **FA#1 Pass 1 — nil Approver default-deny not tested.** The `fakeMCPSession.Start` correctly records a zero `approvalDecision{Allow:false}` when `cfg.Approver` is nil (fail-closed by Go zero value), but this was not exercised. Added `TestFakeMCPSession_Edge_NilApproverDefaultDeny` to verify the fail-closed property explicitly.
- **FA#1 Pass 2 — nil-safety contract undocumented at port level.** `turnConfig.Approver` and `approvalFunc` type comment did not state that nil must be treated as default-deny. Added a one-line clarification to both so transport implementations know to nil-check before calling, not panic.
- **FA#1 Pass 2 — FAILURE test did not assert no-Done-after-failure.** `TestFakeMCPSession_Failure_TurnFailedSurfacesError` verified `turnFailed` is returned but did not assert that a subsequent `Next` call yields `errStreamDone` (not `turnDone`). Added the assertion to prove a failed turn never silently becomes a success.
- **FA#2 Pass 1 — single-approval SUCCESS test insufficient.** No test verified all N approval requests from a mixed scenario (allow+deny) are each delivered to the approver in order, with each decision recorded separately. Added `TestFakeMCPSession_Success_MultipleApprovalsAllDeliveredInOrder` to cover this gap and prove a denial doesn't terminate the event stream.
- **FA#2 Pass 2 — no compile-time check for fakeTurnStream implementing turnStream.** The fake's stream type was only implicitly checked via the `Start()` return type. Added `var _ turnStream = (*fakeTurnStream)(nil)` alongside the existing mcpSession check so any future interface expansion is caught at the test-seam level.
- **FA#3 Pass 1 — compatible probe + launch fails was untested.** `mcpStdioSession.Start` has three fail-closed paths (empty WorkDir, incompatible probe, compatible probe but exec fails). Only the first two were tested. Added `TestMCPStdioSession_Error_LaunchFailsAfterCompatibleProbe` using `multiRunner{compatibleProbe()}` to exercise the third path: probe passes, but `/nonexistent/codex mcp-server` fails at OS exec time.
- **FA#3 Pass 1 — WorkDir guard ordering not asserted.** `TestMCPStdioSession_Error_EmptyWorkDirFailsClosed` verified the error sentinel but did not assert that the runner was never called. Added `assert.False(t, runner.runCalled)` to prove the WorkDir guard fires before the probe, not after.
- **FA#3 Pass 2 — malformed notification payload fail-closed path untested.** `mapNotification` maps a decode error to `(turnFailed, ok=true)` — fail-closed so a corrupted mcp frame never silently drops from the stream. This path had no test. Added `TestMapNotification_Error_MalformedPayloadFailsClosed` using a non-marshalable channel value in `AdditionalFields` to force the decode error and assert `turnFailed` is returned (not a silent drop).
- **FA#4 Pass 1 — conversationID thread_id fallback untested.** `conversationID` prefers `conversation_id` and falls back to `thread_id` for session resume, but the fallback path had no test. Added `TestMapNotification_Edge_TurnCompletedWithThreadIDFallback` to assert that a `turn.completed` with only `thread_id` still produces a `turnDone` with the thread id as `SessionID`.
- **FA#4 Pass 1 — agentText top-level message fallback untested.** `agentText` falls back to `ev.Message` when `item.text` is absent — if broken, tokens would be silently dropped. Added `TestMapNotification_Edge_AgentMessageFallsBackToTopLevelMessage` to exercise the fallback path.
- **FA#4 Pass 1 — mapApprovalRequest both-present precedence untested.** When both `command` and `paths` are present (ambiguous payload), command takes precedence. Without a test, removing the command-first switch case would silently change behavior. Added `TestMapApprovalRequest_Edge_CommandTakesPrecedenceOverPaths` to pin the precedence.
- **FA#4 Pass 2 — turnError empty-message fallback untested.** A `turn.failed` or `error` event with neither a nested error message nor a top-level message produces `fmt.Errorf("codex turn failed: turn failed")` via the fallback string. Without a test, a refactor that removed the fallback would silently produce empty-cause failures. Added `TestMapNotification_Edge_TurnFailedNoMessageFallback` to assert the error is always non-empty.
- **FA#5 Pass 1 — SPIKE constants not value-pinned.** All mapper tests use constants in both input and expected output, so if a constant drifts from its intended string value the mappers silently use the wrong value and the mapper tests still pass. Added `TestSpikeConstants_ReusedExecVocabularyArePinned` (pins `agent_message`/`turn.completed`/`turn.failed`/`error` against `parse.go` literals) and `TestSpikeConstants_AssumedPostureConstantsArePinned` (pins `read-only`/`on-request`/`conversation_id`).

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Candidate out-of-scope items per the spec's Non-Goals (do NOT build these; log here if encountered):
- Surfacing blocked approval attempts to the human as a notification or dashboard approval gate (the guardrail is deliberately invisible — slog/OTel only).
- A per-node / per-profile configurable writable root (the boundary is the fixed `docs/` default; both interactive deliverables live there).
- Changing the autonomous codex `agent_call` path, the claude-cli adapter, or the opencode adapter.
- Giving opencode an interactive streaming posture (it is explicitly unsupported for interactive streaming and fails closed).
- A live-codex integration test that spawns the real `codex mcp-server` (unit tests mock the `mcpSession` seam; live verification of the SPIKE protocol constants is a manual/real-binary follow-up, not a unit test to build here).
- Multi-user / shared-deployment ChatGPT-subscription auth for codex.
-->
