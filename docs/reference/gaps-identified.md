# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

- **FA#1 Pass 1 — nil Approver default-deny not tested.** The `fakeMCPSession.Start` correctly records a zero `approvalDecision{Allow:false}` when `cfg.Approver` is nil (fail-closed by Go zero value), but this was not exercised. Added `TestFakeMCPSession_Edge_NilApproverDefaultDeny` to verify the fail-closed property explicitly.
- **FA#1 Pass 2 — nil-safety contract undocumented at port level.** `turnConfig.Approver` and `approvalFunc` type comment did not state that nil must be treated as default-deny. Added a one-line clarification to both so transport implementations know to nil-check before calling, not panic.
- **FA#1 Pass 2 — FAILURE test did not assert no-Done-after-failure.** `TestFakeMCPSession_Failure_TurnFailedSurfacesError` verified `turnFailed` is returned but did not assert that a subsequent `Next` call yields `errStreamDone` (not `turnDone`). Added the assertion to prove a failed turn never silently becomes a success.

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
