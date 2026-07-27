# Assess: Is the Review Plan Satisfied?

You are assessing whether the ralph review plan is complete. Reason only from the
information below; emit a structured verdict. **You have no tools** — you cannot
read files and must not claim to.

## Context
- Tracking files live at `./work/RALPH.md`, `./work/docs/reference/focus-areas.md`, `./work/docs/reference/gaps-identified.md`.
- The most recent refinement pass reported: {{refine.response}}
- **Branch-point test baseline** (failures that existed BEFORE this branch — these do NOT block): `{{record-baseline.test_baseline}}`
- Consecutive prior passes that made no progress: {{no_progress_streak}}

## The pass emits a promise tag — read it first

RALPH.md requires every pass to end with exactly one tag:

- `<promise>FINIT</promise>` — the agent asserts every review is approved and no open gaps remain.
- `<promise>CLOSER</promise>` — more work remains.

Use it as **strong evidence, not as the verdict**:

- `CLOSER` present → `done` is **false**. The agent doing the work says it is not
  finished; do not overrule that.
- `FINIT` present → `done` is true **only if** the criteria below also hold — in
  particular the NEW-failures-vs-baseline check, which the pass agent may not have
  evaluated. A `FINIT` alongside a new failure, an unapproved review, or an
  unwired component is wrong; emit `done: false` and say so in `notes`.
- **Neither tag present** → the pass violated the loop protocol. Treat as
  `done: false` and `progressed: false`, and say so in `notes` — a pass that does
  not follow its instructions has not demonstrably finished anything, and the
  no-progress streak is what stops a loop whose agent is ignoring the protocol.

## How to judge "done"

This plan uses a two-table review structure with explicit approval counts. `done`
is true only when ALL hold:

- **Every single-system review has BOTH approvals** and every paired integration
  review has its one approval. A partially-approved area is not done. Be suspicious
  of a pass claiming to have completed several reviews at once — approvals are
  meant to be independent, and one pass awarding both boxes of an area defeats the
  redundancy the second approval exists for. Treat that as `progressed: true` but
  NOT as grounds for `done`.
- **No unchecked items remain** in `docs/reference/gaps-identified.md`. A path
  logged there as an inert/no-op "acceptable" is an OPEN gap, not a reason for done.
- Every area is **wired-and-fed** — constructed at the real composition root and
  fed real data from an existing producer. A component that is present and green in
  isolation but fed `nil`/empty/hardcoded, or constructed only in tests, is NOT done.
- **No NEW test failures** beyond the baseline (current failing set ⊆ baseline) and
  nothing that built at baseline is now broken.

A test already failing in the baseline must NOT, by itself, keep `done` false. A
fully green suite does NOT by itself imply done — green over-mocked tests can
coexist with an unwired component, so weigh the wiring evidence too.

## Emit

- `done`: boolean per the rule above. Be conservative: if any review lacks its
  required approvals, any gap is open, or any component is unwired, `done = false`.
- `notes`: one sentence on the remaining blocker, or "done".
- `progressed`: boolean — did THIS pass make real forward progress (advanced or
  approved a review, fixed a real gap, wired a component, removed a NEW failure)?
  A pass that only re-ran tests, churned, or got blocked counts as `false`.
- `consecutive_no_progress`: integer. If `progressed` is true, emit `0`. Otherwise
  emit {{no_progress_streak}} + 1. This lets the loop bail out when it is stuck
  rather than spinning to the iteration cap.
