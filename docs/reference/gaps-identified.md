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

2. **Coarse streaming granularity.** Codex emits whole `item.completed`
   `agent_message` items, not token deltas, so `StreamChat` emits one
   `ChatChunk` per item rather than per token. Confirm the interactive_agent UX
   degrades acceptably (chunky but ordered), that the terminal chunk carries the
   thread id + token counts, and that an empty/partial item is handled.

3. **No per-request stream timeout.** `engine.ChatRequest` has no per-request
   timeout field (unlike `AgentMission.Timeout`), so the stream path uses only
   the adapter's configured timeout floor. Verify a long/hung interactive codex
   run cannot block unbounded and that the floor is sane (and surfaced/clamped
   in the composition root).

4. **Model-availability failure legibility.** This host's ChatGPT account
   rejected `gpt-5`/`gpt-5-codex` at runtime (codex returned `error` +
   `turn.failed`); the sample uses `gpt-5.5`. Confirm a model the account cannot
   use surfaces a clean, legible node failure (typed error naming codex/model),
   never a hang or silent empty result, on BOTH batch and stream paths — and
   that the docs note `gpt-5.5` availability depends on the user's plan.

5. **ToolScope under full-bypass posture.** `ChatRequest.ToolScope` precedence
   is honored at the request boundary, but codex `exec` exposes no per-tool argv
   flag, so under `--dangerously-bypass-approvals-and-sandbox` the posture is
   fully open regardless of ToolScope. Verify this is the intended/documented v1
   behavior (per the spec's full-bypass choice) and that ToolScope does not imply
   a narrowing that silently does not happen.

6. **Readiness health-surface redaction.** Phase 3 added a NEW
   `config.RedactedConfig.Backends` field populated by `adapter/backendhealth`.
   Verify the redaction allow-list projection leaks nothing beyond
   `{name, opaque state}` (no path, no token, no version string if sensitive),
   that a probe fault DROPS the entry rather than failing/500-ing the config
   read, and that both read surfaces (MCP `system_config` + HTTP
   `GET /system/config`) populate it identically.

7. **Live/integration paths unverified.** No test exercises a real `codex exec`
   round-trip through a live engine for either `agent_call` or
   `interactive_agent`, nor the readiness probe against a not-logged-in host (on
   this dev host it resolves `ready` only because codex is installed + logged
   in). Needs a smoke run or a documented manual-verification recipe.

---

## Fixed Previously

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
