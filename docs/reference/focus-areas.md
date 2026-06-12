# Focus Areas Review Tracking

This document tracks the systematic review of the **codex-interactive** feature (driving `codex mcp-server` for `interactive_agent` nodes under a read-only sandbox + invisible auto-approval, so codex interviews instead of autonomously implementing) for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | mcpSession port + types (`adapter/codex/mcp_session.go` — `mcpSession`/`turnConfig`/`turnStream`/`turnEvent`/`approvalRequest`/`approvalDecision`/`approvalFunc`, `errStreamDone` sentinel; hexagonal: no domain import) | [x] | [x] | Done |
| 2 | Fake transport (`adapter/codex/fake_mcp_session_test.go` + `mcp_session_test.go` — scripted events, scripted approval requests + recorded decisions, `startErr`/`blockOnCtx`, recorded `turnConfig`; mirrors `fake_runner_test.go`) | [x] | [x] | Done |
| 3 | Real transport launch/probe/env (`adapter/codex/mcp_stdio_session.go` — `codex mcp-server` launch via mcp-go stdio, `codexEnv()` no-key, shared `ensureCompatible` probe, prompt-via-protocol, spawn-per-turn teardown/reap) | [x] | [ ] | In progress |
| 4 | Real transport mappers (`mcp_stdio_session.go` — notification→`turnEvent`, elicitation payload→`approvalRequest`, `approvalDecision`→protocol response, start-tool request build incl. resume + effort; fail-closed decode) | [ ] | [ ] | Not started |
| 5 | Spike-gated protocol constants + app-server fallback seam (`mcp_stdio_session.go` SPIKE constants + `design-phase-1.md` Spike Findings — provisional vs verified, fallback localized to this file) | [ ] | [ ] | Not started |
| 6 | Approval policy (`adapter/codex/approval_policy.go` — `decide` patch/command classification, default-deny, deliverable-commit allow, info-command allow) | [ ] | [ ] | Not started |
| 7 | Path-scope helper (`approval_policy.go` `withinRoot` — clean/absolutize, `..`-escape, absolute-outside, prefix-sibling `docs-archive`, root-itself, `./docs/`/trailing-slash normalization) | [ ] | [ ] | Not started |
| 8 | InteractiveProvider stream fold (`adapter/codex/interactive_provider.go` `StreamChat` — token→`ChatChunk`, terminal→`Done`+`SessionID`, `makeSendChunk`, drain+reap, WorkDir guard, model resolution) | [ ] | [ ] | Not started |
| 9 | InteractiveProvider Chat + constructor + effort + trace (`interactive_provider.go` — `Chat` fold, `NewInteractiveProvider`/`newInteractiveProviderWithSession`, `core` reuse, `codexEffortValue`, `tracingApprover` slog) | [ ] | [ ] | Not started |
| 10 | Fail-closed streaming invariant (cross-cutting — failed turn / transport break / malformed frame → terminal `Error` no `Done`; zero-stream on guard/launch fail; `-race`) | [ ] | [ ] | Not started |
| 11 | Subscription-auth / no-API-key invariant (cross-cutting — `codexEnv()` on launch, `HOME`/`CODEX_HOME` preserved, no `OPENAI_API_KEY`, `RevealKey` never consulted, prompt off-argv) | [ ] | [ ] | Not started |
| 12 | Session lifecycle & resume (spawn-per-turn; `ChatRequest.SessionID`→`turnConfig.ResumeID`; terminal `ChatChunk.SessionID` back out; no in-memory session state; no engine edit) | [ ] | [ ] | Not started |
| 13 | Composition-root wiring + selector routing (`cmd/arbitron/main.go` §12c swap; `adapter/provider/interactive_stream_selector_test.go`; dead `codexStreamAgent` removed; autonomous arm untouched) | [ ] | [ ] | Not started |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 14 | InteractiveProvider + approval policy (the mandatory guardrail end-to-end: a source-path patch mid-interview is DENIED, the decision is traced via slog, the stream still reaches a clean `Done`, no chunk carries an Error, no human surfacing) | [ ] | Not started |
| 15 | InteractiveProvider + mcpSession transport (`turnConfig` built correctly from `ChatRequest`; `turnStream` folded to `errStreamDone`; resume id flows; transport break → fail-closed `Error` chunk) | [ ] | Not started |
| 16 | Real transport mappers + spike constants (notification/elicitation/decision mappers consume the SPIKE protocol keys consistently; an app-server/elicitation-vs-sampling change stays contained to `mcp_stdio_session.go`) | [ ] | Not started |
| 17 | Composition root + selector + autonomous factory (interactive codex → `InteractiveProvider`; batch `AgentProviderFor` codex → exec `Provider`; no leak; `opencode`/unknown still fail closed) | [ ] | Not started |
| 18 | Approval policy + sandbox posture + docs boundary (policy is the PRIMARY gate, read-only sandbox is defense-in-depth only; `docs` root covers `docs/specs/` + `docs/superpowers/specs/`; no source/test/config lives under `docs/`) | [ ] | Not started |
| 19 | InteractiveProvider + engine seam (`ChatChunk` contract honored; `SessionID` persisted by `conversation.go`; resume round-trips through the existing `StreamProviderFor` path; zero engine edits) | [ ] | Not started |
| 20 | Subscription-auth + transport launch + version probe (`codexEnv()` on the mcp-server launch shares the no-key invariant with the exec path; the memoized `ensureCompatible` probe is shared so the two cannot drift) | [ ] | Not started |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All package functions have test coverage (happy / success / failure / error / edge)
- The code does what the design doc says it should (the fold, the policy classification, the launch/teardown)
- Error handling is thorough — no silent failures; the fail-closed terminal-error path is exercised; sentinels (`errStreamDone`, `ErrWorkDirRequired`) matched with `errors.Is`
- Module boundaries are clean (no domain import in `mcp_session.go`/`approval_policy.go`; mcp-go only in the transport; no secret/key across a boundary)

**Pass 2 (Robustness & Security):**
- Code aligns to the spirit of the design, not just surface conformance
- The no-mutation guardrail and subscription-auth invariants hold on every path (not just the happy one)
- Test assertions test real risks (a source-path write denied with the whole patch rejected, a `..`-escape denied, a failed turn never a `Done`, no key in any recorded env), not happy-path ceremony
- Edge cases handled (approval before any token, empty-text notification, terminal missing tokens, context cancelled mid-stream + child reaped, unknown approval kind default-denied, empty/absolute/mixed patch paths)

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary (`ChatRequest` → `turnConfig` → transport → `turnEvent` → `ChatChunk` → `SessionID` back out)
- The policy decision made in the provider's `Approver` is honored and the transport never overrides it
- No assumptions in one component the other doesn't satisfy (the `docs` writable root == both deliverable dirs; the SPIKE protocol keys decoded by the mappers match what the start-tool request encodes)
- Integration tests cover the seams (the mandatory deny-mid-interview scenario, resume round-trip, autonomous-vs-interactive routing), not just unit-level happy paths
