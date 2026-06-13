# Codex Interactive — Design Phase 3: Wiring, Sandbox Config & Real-Repro Verification

**Slug:** `codex-interactive`
**Phase:** 3 of 3
**Status:** Draft
**Date:** 2026-06-12
**Spec:** `docs/specs/codex-interactive-spec.md`
**Depends on:** Phase 1 (transport seam), Phase 2 (provider + policy)

## 1. Purpose

Make the Phase-2 provider **live** for codex-backed interactive nodes — and only
those — by swapping a single entry in the composition root, supplying the
concrete read-only-sandbox + approval posture and the `docs/` write boundary, and
proving the autonomous codex path and existing codex tests are untouched. Then
verify with the real repro from the spec.

## 2. The one wiring change

In `cmd/arbitron/main.go` (section 12c, the interactive stream selector), the
interactive `agentStreams` map currently routes the codex backend to the
exec-based stream provider:

```
codexStreamAgent := codex.NewStreamProvider(codex.Config{ ... })   // `codex exec --json`
interactiveStreamSel := providersadapter.NewInteractiveStreamSelector(
    providerFactory,
    map[string]engine.ChatProvider{
        workflow.BackendClaudeCode: claudeStreamAgent,
        workflow.BackendCodex:      codexStreamAgent,    // <-- changes
    },
    cfg.LLM.DefaultAgentBackend,
)
```

Phase 3 replaces the `BackendCodex` value with the Phase-2 interactive provider:

```
codexInteractive := codex.NewInteractiveProvider(codex.InteractiveConfig{
    Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
    MaxResponseBytes: cfg.LLM.MaxResponseBytes,
    CapabilityRules:  capabilityRules,   // effort emission, preserved
    WritableRoot:     "docs",            // the deliverable boundary (spec §3)
    // read-only sandbox + auto-approval posture are the provider's fixed
    // interactive contract, not an operator knob.
})
// agentStreams: workflow.BackendCodex -> codexInteractive
```

**What does NOT change:**

- `codex.NewStreamProvider` / `codex.Provider` (the `codex exec` path) and the
  factory's `AgentProviderFor` codex arm — the **autonomous** `agent_call` path
  is byte-for-byte unchanged (still `codex exec
  --dangerously-bypass-approvals-and-sandbox`).
- The chat path, claude-cli, opencode, every workflow definition, and every
  profile.
- The `InteractiveStreamSelector` itself — it already dispatches codex by backend
  key; only the value behind that key changes. (`claude-code` still maps to the
  claude stream adapter; `opencode`/unknown still fail closed.)

Because the selector keys on the **agent backend** and the autonomous path uses a
**separate selector** (`AgentProviderFor`), interactive vs. autonomous codex are
already separated in the wiring — no per-node-type branching is added.

## 3. Concrete posture values

- **Sandbox:** `read-only` — codex may read + run informational commands, no
  silent writes. (Exact config key/flag pinned by the Phase-1 spike.)
- **Approval policy:** the mode that forces every mutation through a client-
  answered approval request, wired to the Phase-2 `decide` auto-policy.
- **Writable root:** `docs` (relative to the node's `WorkDir`). Covers
  `docs/specs/` (spec node) and `docs/superpowers/specs/` (design node); excludes
  all source/test/config. Default lives in code; no per-node or workflow input.
- **Subscription auth:** `codexEnv()` — no API key, `HOME`/`CODEX_HOME` preserved.

## 4. Regression guard (autonomous path stays green)

- The existing `adapter/codex` exec tests (`provider_test.go`,
  `stream_adapter_test.go`, `args_test.go`, `parse_test.go`, `env_test.go`,
  `version_test.go`, `readiness_test.go`) must pass unchanged — they exercise the
  untouched exec path.
- A composition/selection test asserts: an **agent_call** codex binding still
  resolves to the exec `Provider`; an **interactive_agent** codex binding now
  resolves to `InteractiveProvider`; `claude-code` and `opencode`/unknown routing
  is unchanged (opencode still fails closed with `ErrUnsupportedAgentBackend`).

## 5. Testing (strict TDD — all five categories)

At this phase the unit surface is the wiring/selection; the provider's own
behavior is covered in Phase 2.

- **Happy path:** the interactive selector returns `InteractiveProvider` for a
  codex agent-kind interactive binding.
- **Success:** a codex agent-kind binding with an empty backend is floored to the
  default backend and still routes to the interactive provider; `claude-code`
  still routes to the claude stream adapter.
- **Failure:** `opencode` interactive binding still fails closed
  (`ErrUnsupportedAgentBackend`) — unchanged.
- **Error:** an unknown/typo backend (`"Codex"`) still fails closed (exact-match,
  no silent fallback) — unchanged.
- **Edge case:** the autonomous `AgentProviderFor` codex arm still returns the
  exec `Provider` (interactive swap did not leak into the batch path); chat-kind
  interactive bindings still route to the chat selector.

## 6. Real-repro verification (spec §5, manual acceptance)

Run against real codex (codex-cli 0.139, logged-in subscription), reproducing the
spec's scenario:

1. Re-run feature-pipeline-v2's `brainstorm-spec` node (profile
   `interviewer-codex`) on a feature like *"remove the Queue Status section from
   Settings."*
2. **Expected (after):** codex asks clarifying questions one at a time and waits;
   it may read files / check available CLIs, but it **does not** edit
   `Settings.tsx` and **does not** run a build; on approval it writes the spec
   under `docs/specs/` and commits it; a human reply resumes the same codex
   session.
3. **Regression:** confirm an autonomous codex `agent_call` node (e.g. an
   implement-task on the same profile family) still edits source and builds as
   before.
4. Capture the run trace and confirm any mid-interview write attempt outside
   `docs/` appears as a **denied** approval step (the operator's guardrail
   signal) — with no human-facing notification or approval prompt.

## 7. Deliverables

- `cmd/arbitron/main.go` — the one-line map-value swap + `InteractiveConfig`
  construction.
- (`InteractiveConfig` + `NewInteractiveProvider` are defined in Phase 2; Phase 3
  only constructs and wires them — it adds no new adapter constructor.)
- Selection/regression tests (composition seam).
- A short operating-posture note (read-only sandbox + auto-approval + `docs/`
  boundary) appended to the codex backend docs.

## 8. Out of scope for Phase 3

- Any change to autonomous codex, claude-cli, opencode, workflows, or profiles.
- Surfacing blocked attempts to the human (explicitly a non-goal — invisible
  guardrail).
- Multi-user / shared-deployment codex auth (inherits the codex backend non-goal).
