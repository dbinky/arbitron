# Assess: Refinement Done?

You are assessing whether refinement is complete. Reason only from the information below; emit a structured verdict.

## Context
- Tracking files live at `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
- The most recent refinement pass reported: {{refine.response}}
- **Branch-point test baseline** (failures that existed BEFORE this feature — these do NOT block): `{{record-baseline.test_baseline}}`
- Consecutive prior passes that made no progress: {{no_progress_streak}}

## How to judge "done"
Gate on NEW failures vs the baseline, NOT on a fully green suite. `done` is true
only when ALL hold:
- All plan tasks implemented, with five-category coverage for the feature's behaviors.
- No remaining focus areas or open gaps.
- **No NEW test failures** beyond the baseline (the current failing set is a
  subset of the baseline) AND nothing that built at baseline is now broken.
A test already failing in the baseline must NOT, by itself, keep `done` false.

## Emit
- `done`: boolean per the rule above. Be conservative: if any focus area or
  non-baseline gap remains, `done = false`.
- `notes`: one sentence on the remaining blocker, or "done".
- `progressed`: boolean — did THIS pass make real forward progress (advanced a
  focus area, fixed a real gap, or removed a NEW failure)? A pass that only
  re-ran tests, churned, or got blocked counts as `false`.
- `consecutive_no_progress`: integer. If `progressed` is true, emit `0`.
  Otherwise emit {{no_progress_streak}} + 1. (This lets the loop bail out when it
  is stuck rather than spinning to the iteration cap.)
