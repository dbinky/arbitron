# Codex Interactive Interview Posture — Product Spec

**Slug:** `codex-interactive`
**Status:** Draft
**Date:** 2026-06-12

## 1. Problem / Motivation

Arbitron's `interactive_agent` nodes are meant to hold a turn-taking conversation
with a human: ask one question at a time, wait for the answer, and produce a
deliverable only once the human approves it. The feature-pipeline's
spec-brainstorm and design-brainstorm nodes are exactly this kind of node.

When such a node is backed by a **codex / GPT** profile, it does the wrong thing.
Instead of interviewing, codex **autonomously implements the feature** against the
live repository — editing source, running builds — the moment it is handed a
concrete ask. The human is bypassed, no interview happens, and the codebase is
mutated before any spec or design exists.

This is specific and narrow:

- **claude-cli** and **opencode** interactive nodes already behave correctly —
  they conduct a real interview.
- **Autonomous** codex nodes (`agent_call`: `implement-task`, `ralph-refine`)
  already behave correctly — building the thing is their job.
- The broken case is **codex on interactive nodes** — and it breaks every one of
  them.

This is not a prompt problem. The identical prompt conducts a clean interview in
codex's own interactive surface; prompt-hardening was tried and codex still
drifted. The fix must be an **enforced** posture, not a request.

## 2. Goals & Outcomes

- A codex-backed `interactive_agent` node conducts a **real interview**: asks one
  question at a time, waits for the human, and resumes the same conversation when
  the human replies.
- During the interview, codex **modifies nothing** in the codebase — no
  source / test / config edits, and no builds / tests / installs.
- The node still **completes its contract**: on human approval it produces and
  commits its declared deliverable (e.g. the spec under `docs/specs/`, or design
  documents under `docs/superpowers/specs/`).
- The guarantee is **enforced, not requested**. It does not depend on prompt
  wording or model goodwill — prompt-hardening was already tried and failed.
- **Autonomous codex nodes are completely unchanged.** `implement-task` and
  `ralph-refine` keep their current "build the thing" behavior exactly.
- The change is **invisible to claude-cli and opencode** backends — their
  interactive nodes are unaffected.

## 3. User-Facing Behavior

- **Turn-taking interview.** A codex-backed interactive node asks a question and
  yields control. The human answers; the **same conversation resumes**. This is
  the same conversational UX as the existing Claude interactive path.
- **Codebase is read-only to the interviewer.** Codex may **read** repository
  files during the interview so it can ask grounded, informed clarifiers
  (for example, referencing what a file actually contains). It cannot change
  anything. "Never modify the codebase" means no writes; reads are safe and
  improve interview quality.
- **Invisible guardrail.** If codex attempts a forbidden action mid-interview
  (edit a file, run a build), the attempt is **silently blocked** and codex
  continues interviewing. The human is **not** interrupted and sees a clean
  interview. The blocked attempt is **observable in the run trace** for
  debugging, but never surfaces as a notification or an approval prompt.
- **Deliverable still produced.** On human approval, codex writes **only** the
  node's declared deliverable document(s) and makes the commit. For the spec node
  that is `docs/specs/`; for the design node that is `docs/superpowers/specs/`.
  Any write **outside** the node's declared deliverable location — source, tests,
  config, anything — is denied.
- **Subscription auth unchanged.** The interview runs on the user's existing
  ChatGPT / Codex subscription, with **no OpenAI API key** required or stored
  anywhere — identical to the existing codex backend.
- **Fail-closed.** If codex cannot run the interview cleanly (not installed, not
  logged in, transport failure, an interrupted turn), the node **fails with a
  clear, named cause**. A failed turn is never reported as a silent partial
  success; there are no half-finished interviews presented as complete.

## 4. Scope

**In scope:**

- Codex-backed `interactive_agent` nodes only.
- The interview posture is applied **automatically** for those nodes, based on
  node type. It is **not** a global switch, **not** a per-profile opt-in, and
  requires **no** change to any workflow definition. A workflow author does
  nothing differently; a codex-backed interactive node simply interviews.

**Out of scope / Non-goals:**

- **Autonomous codex nodes** (`agent_call`) — unchanged; they still build.
- **claude-cli and opencode backends** — unchanged and unaffected.
- **Workflow definitions, profiles, and the sample feature-pipeline profiles** —
  unchanged.
- **The chat-tier OpenAI path** — unaffected.
- **Surfacing blocked attempts to the human** as notifications or approval gates —
  explicitly rejected in favor of the invisible guardrail.
- **Multi-user / shared-deployment auth** for codex — inherits the existing codex
  backend's non-goal.

## 5. Success Criteria

- A codex-backed `interactive_agent` node asks a question and yields; across the
  **whole conversation** it modifies no source / test / config file and runs no
  build / test / install.
- The same node still completes its contract: on approval, the deliverable is
  written and committed under its declared location.
- **Human replies resume the same codex session** — conversation continuity
  round-trips through the existing resume path.
- **Autonomous codex nodes still build as before** — their behavior is unchanged.
- **Real repro (verification).** Re-run the feature-pipeline `brainstorm-spec`
  node (profile `interviewer-codex`) on a feature like "remove the Queue Status
  section from Settings."
  - **Before:** codex edits `Settings.tsx` and runs the build.
  - **After:** codex interviews (asks clarifiers, waits), never touches code, and
    writes the spec only after approval.

## 6. Risks & Considerations

- **Enforcement-vs-authoring boundary.** The interviewer must be blocked from
  editing everything except its one declared deliverable, yet still able to
  produce that deliverable. The boundary is the node's declared deliverable
  location; getting it right — neither so tight the spec can't be written, nor so
  loose that code can be touched — is the central correctness concern.
- **Trace-only visibility of blocks.** Because blocks are invisible to the human,
  the run trace is the **only** place a blocked attempt is recorded. That trace
  signal is what an operator relies on to confirm the guardrail is working.
- **Behavioral parity with the Claude interview.** Success means the codex
  interview *feels* the same as the Claude one — one question at a time, resumes
  on reply. Divergence in the turn-taking feel is a quality risk to watch.
