# Assess: Refinement Done?

You are assessing whether refinement is complete. Reason only from the information below; emit a structured verdict.

## Context
- Tracking files live at `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
- The most recent refinement pass reported: {{refine}}

## Decide
- `done`: true only when the definition of done in RALPH.md is satisfied — all plan tasks implemented, the full test suite green (including happy/success/failure/error/edge), no remaining focus areas or gaps.
- `notes`: one sentence on the remaining blocker, or "done".

Be conservative: if any focus area or gap remains, `done = false`.