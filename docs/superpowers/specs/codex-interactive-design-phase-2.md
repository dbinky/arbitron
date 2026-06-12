# Codex Interactive — Design Phase 2: Interactive Provider + Approval Policy

**Slug:** `codex-interactive`
**Phase:** 2 of 3
**Status:** Draft
**Date:** 2026-06-12
**Spec:** `docs/specs/codex-interactive-spec.md`
**Depends on:** Phase 1 (the `mcpSession` transport seam + fake)

## 1. Purpose

Implement the **interactive codex provider** — a `engine.ChatProvider` that runs
codex conversationally over the Phase-1 `mcpSession` seam, with the **invisible
auto-approval policy** that is the heart of the feature: codex can read, run
informational commands, write its `docs/` deliverable, and commit — but every
other write is denied. This phase is pure adapter code + the policy; it is still
not wired into the running engine (Phase 3).

## 2. The provider — `InteractiveProvider`

A new type in `adapter/codex` implementing `engine.ChatProvider`:

```
func (p *InteractiveProvider) Chat(ctx, req engine.ChatRequest) (*engine.ChatResponse, error)
func (p *InteractiveProvider) StreamChat(ctx, req engine.ChatRequest) (<-chan engine.ChatChunk, error)
```

It depends only on the `engine.ChatProvider` port (consumed) and the Phase-1
`mcpSession` (transport). It carries:

- the `mcpSession` factory (real in prod, fake in tests),
- the `core` it shares with the exec adapters (binding, version probe, timeout),
  so construction + compatibility cannot drift,
- the **approval policy** (its `writableRoot`, default `docs/`).

**Constructor (owned by this phase).** Phase 2 defines both the `InteractiveConfig`
struct (`Timeout`, `MaxResponseBytes`, `CapabilityRules`, `WritableRoot`, plus the
`mcpSession` factory) and `NewInteractiveProvider(InteractiveConfig) *InteractiveProvider`,
building the shared `core` via `newCore` exactly as the exec adapters do. Phase 3
only *constructs and wires* it — it adds no new constructor.

### Turn flow (`StreamChat`)

1. **Guard** — empty `WorkDir` → `ErrWorkDirRequired` (reuse the existing
   sentinel); empty model → fall back to the binding model exactly as the exec
   stream adapter does. A failed guard yields **no** stream (no half-open).
2. **Build `turnConfig`** — model, prompt (`req.Prompt`), `WorkDir`,
   `ResumeID = req.SessionID` (empty → new conversation), the read-only +
   approval posture, and `Approver = p.decide` (the policy, §3). Effort rides via
   the existing `codexEffortArgs`-equivalent if the surface accepts it
   (preserved; a default/empty effort emits nothing).
3. **Start the turn** over `mcpSession`; an error → `nil, err` (fail closed).
4. **Fold the stream** in an internal goroutine: each `turnEvent{token}` →
   `ChatChunk{Token}`; the terminal `done` → `ChatChunk{Done, SessionID,
   tokens}`; a `failed`/transport error → `ChatChunk{Error}` with **no `Done`**.
   The goroutine always drains the stream and reaps the child (Phase-1 teardown).
   Reuse the existing non-blocking `makeSendChunk` pattern so a gone/timed-out
   consumer never blocks the drainer.

`Chat` (non-streaming) folds the same turn to completion and returns a
`ChatResponse{Output, SessionID, Model, tokens}` — the same fold, mirroring the
exec adapter's Chat/StreamChat symmetry.

### SessionID round-trip (resume)

The terminal `ChatChunk.SessionID` carries codex's conversation id. The engine
already persists it (`conversation.go`) and feeds it back as `req.SessionID` on
the human's next turn → `ResumeID` → the same codex conversation continues. The
provider holds **no** in-memory session state between turns (spawn-per-turn), so
resume survives `WAITING`, server restarts, and long human delays.

## 3. The approval policy — `decide(approvalRequest) approvalDecision`

The auto-policy invoked by the transport for **every** codex approval request.
It is the enforcement point. It never prompts the human (invisible guardrail) and
records each decision (especially denials) to the trace.

Classification:

| Request kind                                   | Decision | Rationale |
|------------------------------------------------|----------|-----------|
| Command exec, read-only / informational        | **Allow** | grounded clarifiers, CLI checks (spec §3) |
| Patch/write whose **every** target path is under `writableRoot` (`docs/`) | **Allow** | the deliverable |
| The git commit of the deliverable              | **Allow** | codex commits its own deliverable (spec §3) |
| Patch/write touching **any** path outside `docs/` (source/test/config) | **Deny** | anti-drift core guarantee |
| Any command whose effect mutates the tree / privilege escalation outside the above | **Deny** | no edits, no build |

Rules that make this robust:

- **Path resolution.** Target paths are resolved relative to `WorkDir` and
  cleaned (`filepath.Clean`) before the prefix test; a `..`-escape or absolute
  path that lands outside `WorkDir/docs` is **denied** (fail closed). A
  multi-file patch is allowed **only if every** target is inside `docs/` — one
  outside path denies the whole patch.
- **Default deny.** Any request the policy does not positively recognize as
  allowed is **denied**. New/unknown codex approval kinds fail closed, never open.
- **Commit recognition.** The git commit is allowed by recognizing the commit
  command shape targeting the repo (it writes `.git`, which is outside `docs/`,
  so it needs an explicit allow). The recognition is narrow; everything else
  shell-side is denied. (If the spike shows codex never elicits for the commit
  under the chosen posture, this arm is inert — documented, not removed.)
- **Trace.** Each decision is recorded as a node-trace step
  (`kind: approval`, the request summary, allow/deny + reason). Denials are the
  operator's only signal the guardrail fired (spec §6) — they are never surfaced
  to the human as a notification or an approval prompt.

The `writableRoot` is a provider field (default `docs/`), set in Phase 3. It is
**not** plumbed per-node — both interactive deliverables live under `docs/` and
no source/test/config does, so one root suffices with no engine/workflow change.

## 4. Fail-closed invariants (preserved from the exec adapter)

- No API key ever in env/argv; `binding.RevealKey` never consulted; the only auth
  surface is `~/.codex/auth.json` via `codexEnv()`.
- A failed turn (`turn.failed`/error), a transport break, or a malformed frame →
  a terminal **error** chunk, never a `Done` → never a silent partial success.
- A failed guard/launch yields **zero** stdout/stream.
- Context cancel/timeout unblocks the sender and reaps the child.

## 5. Testing (strict TDD — all five categories, mock transport only)

All tests drive the `fakeMCPSession` (Phase 1). **No live codex.** The
"codex tries to edit during the interview → denied" scenario is mandatory.

- **Happy path:** scripted start→token→token→done → ordered `ChatChunk` tokens
  then a single terminal `Done` carrying the conversation id; `WorkDir`/model
  fed to `turnConfig` correctly; no API key in the recorded env.
- **Success:** (a) resume — `req.SessionID` flows to `ResumeID`; (b) an
  informational-command approval request → **allowed**; (c) a `docs/specs/…`
  write → **allowed**; (d) the deliverable commit → **allowed**.
- **Failure:** a `turn.failed` frame → terminal `Error` chunk, no `Done`; the
  deliverable is *not* reported as written.
- **Error:** transport `Start` error → `StreamChat` returns `nil, err` (no
  channel); a malformed frame mid-stream → `Error` chunk, drainer still reaps.
- **Edge case (the guardrail):**
  - a patch targeting `domain/.../Settings.tsx` (outside `docs/`) → **denied**,
    decision traced, interview continues (no `Done` suppression, no human notice);
  - a multi-file patch with one `docs/` path and one source path → **denied**
    wholesale;
  - a `..`-escape path resolving outside `WorkDir/docs` → **denied**;
  - an unknown approval kind → **denied** (default-deny);
  - an approval request arriving before any token; empty-text notification;
    terminal event missing token counts; context cancelled mid-stream → context
    error, child reaped.

## 6. Deliverables

- `adapter/codex/interactive_provider.go` — `InteractiveProvider` (Chat +
  StreamChat) over `mcpSession`, sharing `core`, plus the `InteractiveConfig`
  struct and `NewInteractiveProvider` constructor (owned here; used in Phase 3).
- `adapter/codex/approval_policy.go` — `decide` + path-scope classification.
- `adapter/codex/interactive_provider_test.go`,
  `adapter/codex/approval_policy_test.go` — full five-category coverage on the
  fake transport.

## 7. Out of scope for Phase 2

- Composition-root wiring + the concrete sandbox/approval config values (Phase 3).
- Any change to `codex exec`, the batch `Provider`, claude-cli, or opencode.
- The exact mcp-server vocabulary (owned by Phase 1's real impl + spike).
