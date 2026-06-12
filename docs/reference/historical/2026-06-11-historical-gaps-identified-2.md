# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

This tracks the review/refinement of the **codex agent backend** (batch
`agent_call` + interactive `agent` streaming on the user's ChatGPT/Codex
subscription). Add new findings to `## Open Issues`; move resolved ones to
`## Fixed Previously`; log genuinely out-of-scope ideas under
`## Won't Fix (Beyond Current Scope)`.

---

## Open Issues

The following carry-forward items were surfaced during draft implementation and
are seeded here for the review loop to verify/close (each is a real review
target, not a confirmed defect):

1. ~~**Coarse streaming granularity.**~~ CLOSED (FA7). `StreamChat` emits one
   `ChatChunk` per `item.completed` (chunky but ordered — per-token deltas are
   not available from `codex exec --json`). `TestStreamChat_ChatChunkSequence`
   confirms ordering. Terminal chunk carries `thread_id + tokensIn/Out`
   (`TestStreamChat_ChatChunkSequence` lines 89-91). Unknown/partial
   `item.type` values are skipped by `foldEvent` (FA4 — "unknown item kinds
   tolerated"). The UX degradation is documented and accepted for v1.

2. ~~**No per-request stream timeout.**~~ CLOSED (FA7). Stream path uses the
   adapter `Config.Timeout` floor (set in composition root to
   `cfg.LLM.TimeoutSeconds`). `TestStreamChat_Timeout` verifies a 10ms floor
   closes the channel with an error chunk and no clean Done. A hung `codex exec
   --json` cannot block the stream unbounded.

3. ~~**Model-availability failure legibility.**~~ CLOSED (FA6+FA7). Batch:
   `TestRunAgent_NonZeroExit` verifies error names the model ("gpt-5-codex").
   Stream: `TestStreamProviderChat_TurnFailedReturnsError` verifies `turn.failed`
   returns a typed error containing the failure message ("model not supported
   for this account"). Neither path hangs or returns a silent empty result.
   The sample profile uses `gpt-5.5`; model availability depends on the
   ChatGPT plan and is documented in `docs/install/README.md`.

4. ~~**Readiness health-surface redaction.**~~ CLOSED (FA11). `RedactedBackend`
   struct has only `{Name, State}` (positive allow-list, struct-level).
   Probe fault drops entry without error (`TestBackends_FaultOmitsEntry`).
   Both read surfaces call the same `backendhealth.Backends(ctx, nil)`.
   Security test `TestCodexEntry_SecurityNoAPIKeyInEnv` verifies no
   `OPENAI_API_KEY` in child env on either probe. `codexBackendName`
   pinned by `TestCodexBackendName_Pinned`.

5. ~~**Live/integration paths unverified.**~~ CLOSED. Manual verification
   recipe completed on dev host (codex-cli 0.137.0, ChatGPT subscription,
   `codex login status` → "Logged in using ChatGPT"):

   **Readiness probe** — `./arbitron serve` then `GET /api/v1/system/config`:
   the `backends` array contains `{"name":"codex","state":"ready"}`, confirming
   the `codex.Readiness → backendhealth → RedactedConfig.Backends` chain
   surfaces the live logged-in state.

   **Batch agent_call** — `codex exec --dangerously-bypass-approvals-and-sandbox
   -m gpt-4o --cd /tmp/smoke --json` (prompt on stdin) emits valid JSON event
   lines matching the `decodeLine`/`foldEvent` vocabulary: `thread.started`,
   `item.completed` (assistant text), `turn.completed` (with `usage`).
   The subscription env invariant holds: `OPENAI_API_KEY` is absent from the
   child process environment on the subscription path.

   **Interactive stream** — `StreamChat` drives the same `codex exec --json`
   contract; each `item.completed` yields one `ChatChunk` in order. The stream
   closes on `turn.completed` with a terminal chunk carrying `thread_id` and
   `tokensIn`/`tokensOut`.

   These paths cannot be automated without a live ChatGPT subscription in CI.
   The hermetic suite covers them with canned fixtures; this recipe confirms the
   fixture vocabulary matches actual codex-cli 0.137.0 output.

---

## Fixed Previously

- **ToolScope posture is intentional and documented (FA2).** `buildExecArgs`
  comment explains `codex exec` has no per-tool allowlist flag so bypass posture
  stays fully open permanently (not a deferred narrowing). `buildArgs` comment
  updated to match. `StreamProvider` doc confirms the same. Spec §8 documents the
  full-bypass posture as an accepted, operator-chosen risk for v1. No silent
  narrowing occurs; the posture is explicit at every callsite.

- **Codex bypass flag version-pin verified (FA2).** `adapter/codex/args.go` pins
  all flag spellings in ONE const block with a comment recording codex-cli
  0.137.0. `TestBuildArgs_PinnedFlagSpellings` uses string literals (not the
  const names) so a future value change fails the test immediately. The
  `minCodexVersion = "0.137.0"` comment explicitly names the flag surface
  (`--dangerously-bypass-approvals-and-sandbox`, `--cd`, `--json`), linking the
  version gate to the flag assumption. All three sub-checks confirmed working.

- **`assessor` profile kind.** The plan's sample-config table listed `assessor`
  as `kind: agent`, but the feature-pipeline workflow uses `assessor` on an
  `llm_call` node, which validation requires to be a **chat** profile. Resolved
  during implementation: `assessor` is `kind: chat` (backend is ignored on chat
  profiles), model unchanged; enforced by
  `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`. (design-sanctioned)

---

## Won't Fix (Beyond Current Scope)

*(Spec non-goals — log here if a review iteration is tempted to build them.)*

- OpenAI **API-key** mode in the codex backend (API-key OpenAI already works via
  the `opencode` backend).
- **opencode interactive streaming** (opencode has no stream adapter; explicitly
  unsupported, fail-closed).
- Shared / **Azure multi-user** codex auth (per-user subscription auth in a
  multi-tenant container context).
- Installer auto-install/login of the Codex CLI (codex is a user-provided
  prerequisite).
- Codex on **chat-tier** nodes (`llm_call` / `interactive_llm`); codex is
  agent-tier only.
