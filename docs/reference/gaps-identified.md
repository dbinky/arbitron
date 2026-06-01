# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

_(none yet — as issues are fixed during iterations, move them here with a summary of what was wrong and how it was fixed)_

<!--
Example of a fixed issue entry:

### COMP-A-001: Input validation missing for empty strings
**Area**: Component A (`path/to/component_a.py`)
**Fixed**: Iteration 3
**Detail**: ...
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are accepted MVP limitations or out-of-scope enhancements — only the user can add to or move items in this section. The entries below are pre-seeded from the spec's documented non-goals/known risks (§9) and the opencode spike finding, so the loop does NOT spend iterations "fixing" them.)_

### LOOP-W01: No engine-level runaway guard
**Area**: Engine loop (`domain/engine/engine.go`), native ralph loop
**Detail**: Loop termination depends solely on the author's `conditional` (spec §9, accepted MVP risk). A malformed loop can run indefinitely. Do NOT add a max-node-execution or wall-clock cap — that is explicitly deferred future work, not an MVP gap.

### OPENCODE-W02: opencode multi-provider routing / session resume limited by installed binary
**Area**: opencode agent backend (`adapter/opencode/`)
**Detail**: The installed opencode (v0.0.55) has no `run --model <provider/model>`, no `--resume`/`serve`, and authenticates via `ANTHROPIC_API_KEY` only (see `docs/superpowers/specs/vendor-abstraction-opencode-spike-finding.md`). The adapter is intentionally single-shot mission-per-step with `buildArgs` isolated for a future build. Do NOT try to add `--model`/resume against this binary; re-verify only if a newer opencode build is targeted.

### SCOPE-W03: Fan-out / fan-in parallelism and a first-class loop primitive
**Area**: Engine
**Detail**: Parallel execution (Phase 8) and a dedicated loop/iteration construct are explicit non-goals (spec §9). The cyclic-edge idiom is the supported mechanism; do not build a new primitive.

### SCOPE-W04: interactive node future Messages-API backend
**Area**: Interactive executor (`domain/engine/executor_interactive.go`)
**Detail**: The `interactive` node remains on its existing claude-cli streaming path; its rework to a Messages-API chat adapter that resumes via a conversation array is deferred (spec §4/§9). Out of scope for this MVP.

### SCOPE-W05: Coarse cost accounting for agent_call
**Area**: Agent executors / harness backends
**Detail**: Token/cost visibility for subprocess harnesses (claude-cli agent, opencode) is coarser than native chat calls. Accepted for MVP (spec §9).
