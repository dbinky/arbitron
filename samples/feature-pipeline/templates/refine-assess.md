# Assess: Refinement Done?

You are assessing whether refinement is complete. Reason only from the information below; emit a structured verdict.

## Context
- Tracking files live at `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
- The most recent refinement pass reported: {{refine.response}}
- **Branch-point test baseline** (failures that existed BEFORE this feature — these do NOT block): `{{record-baseline.test_baseline}}`
- Consecutive prior passes that made no progress: {{no_progress_streak}}

## How to judge "done"
Gate on NEW failures vs the baseline AND on wired-and-fed. `done` is true only when
ALL hold:
- All plan tasks implemented and **wired-and-fed** — constructed at the real
  composition root and fed real data from an existing producer. A component that
  is present and green in isolation but fed `nil`/empty/hardcoded, or constructed
  only in tests, is NOT done.
- No remaining focus areas or open gaps — including the whole-pipeline
  provenance/wiring focus items. A path logged in `gaps-identified.md` as an
  inert/no-op "acceptable" is an OPEN gap, not a reason for done.
- Feature behaviors have five-category coverage, and the tests are not
  green-theater (would fail if the production input were nil/empty).
- **No NEW test failures** beyond the baseline (current failing set ⊆ baseline)
  AND nothing that built at baseline is now broken.
A test already failing in the baseline must NOT, by itself, keep `done` false.
A fully green suite does NOT by itself imply done — green over-mocked tests can
coexist with an unwired feature; weigh the wiring/provenance evidence too.

## Emit
- `done`: boolean per the rule above. Be conservative: if any focus area, open
  gap, or unwired/unfed component remains, `done = false`.
- `notes`: one sentence on the remaining blocker, or "done".
- `progressed`: boolean — did THIS pass make real forward progress (advanced a
  focus area, fixed a real gap, wired/fed a component, or removed a NEW failure)?
  A pass that only re-ran tests, churned, or got blocked counts as `false`.
- `consecutive_no_progress`: integer. If `progressed` is true, emit `0`.
  Otherwise emit {{no_progress_streak}} + 1. (This lets the loop bail out when it
  is stuck rather than spinning to the iteration cap.)
