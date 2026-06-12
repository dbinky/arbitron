# Plan — Phase 1, Task 2: real `mcpStdioSession` (mcp-go ↔ `codex mcp-server`)

**Slug:** `codex-interactive`
**Design:** `docs/superpowers/specs/codex-interactive-design-phase-1.md` (§4, §6)
**Depends on:** Task 1.1 (the `mcpSession` port + types)
**Package:** `adapter/codex`

## Goal

Implement the production `mcpSession` backed by `github.com/mark3labs/mcp-go`
v0.53.0 (already a dep) driving `codex mcp-server` over stdio, reusing the
existing subscription env + version probe. Pin the codex mcp-server protocol via
the spike and record findings.

## Spike first (record findings in design-phase-1.md appendix)

Against real codex-cli 0.139, pin: (1) the conversation-start tool name + param
schema (model, prompt, cwd, `sandbox_mode=read-only`, approval policy); (2)
fresh-process resume-by-id; (3) the streaming notification shape; (4) the
approval-request server→client method + payload + response schema; (5) how the
sandbox/approval config is passed. **Decision gate:** if mcp-server cannot express
all five, implement against `codex app-server` instead — behind the SAME port, so
Tasks 1.1, 2.1, 2.2 are unaffected. Record the chosen surface in the appendix.

## Files

- `adapter/codex/mcp_stdio_session.go` (new) — the real impl.
- `adapter/codex/mcp_stdio_session_test.go` (new) — framing tests against canned
  mcp-go messages (NO live codex — drive the mcp-go client against an in-memory /
  scripted server transport, or unit-test the request/notification mappers in
  isolation).

## Implementation

- **Construct** from the shared `core` (binPath via `resolveBinary`, the memoized
  version probe, timeout). Launch `codex mcp-server` with `codexEnv()` —
  PATH-augmented, allowlist-filtered, **NO `OPENAI_API_KEY`**, `HOME`/`CODEX_HOME`
  preserved. Prompt + config flow via the protocol, never argv (SECURITY).
- **Probe** via `ensureCompatible(ctx)` before the first turn → incompatible /
  missing binary fails closed with zero events.
- **Start/resume** — call the pinned conversation tool; for resume pass
  `cfg.ResumeID`.
- **Notifications → events** — register an mcp-go notification handler mapping
  assistant-text increments to `turnEvent{turnToken}` and the terminal result to
  `turnEvent{turnDone, SessionID, tokens}`. A `turn.failed`/error →
  `turnEvent{turnFailed, Err}`.
- **Approvals** — register the mcp-go handler for codex's server→client approval
  request; build an `approvalRequest` (argv and/or write paths + reason), call
  `cfg.Approver`, return the `approvalDecision` over the protocol. The transport
  makes NO judgement of its own.
- **Teardown** — close the stdio client + reap the child at end of turn
  (spawn-per-turn). Context cancel/timeout → `Next` returns the ctx error, child
  reaped, no leak.

## Tests (TDD — all five categories; no live codex)

- **Happy path:** scripted server notifications → ordered `turnToken`s + a
  terminal `turnDone` carrying the conversation id + tokens.
- **Success:** resume — `ResumeID` rides the start call; the recorded launch env
  has no API key; prompt delivered via protocol not argv.
- **Failure:** a `turn.failed` notification → `turnFailed` event, not `turnDone`.
- **Error:** launch error / failed probe → `Start` error, zero events; malformed
  frame → `turnFailed` (fail closed), child reaped.
- **Edge case:** an approval request before any token; a notification with empty
  text; a terminal event missing token counts; ctx cancelled mid-stream.

## Verification

- `go test ./adapter/codex/...` green; `go vet` clean.
- `var _ mcpSession = (*mcpStdioSession)(nil)`.
- Spike findings + decision-gate outcome appended to design-phase-1.md.

## Notes / risk

- If the spike is blocked (no live codex in the automated run), implement the
  mapper logic + framing against the documented mcp-go + codex surface and mark
  the protocol constants `// SPIKE: verify against codex-cli 0.139` so the real
  binary check is a small, isolated follow-up (ralph or manual). All
  fake-transport-based tests (this task's mappers, and all of Phase 2) stay green
  regardless.
