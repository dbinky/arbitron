# Mission: Ralph Refinement Pass

Working **unattended** in the worktree at `./work`, make the **smallest correct improvement** toward "done". The user is unavailable.

## Test command(s)
Use these exact, pre-detected command(s) — do NOT guess a different one:
`{{record-baseline.test_commands}}`

## Pre-existing failures are not your job
A branch-point baseline of failures that existed BEFORE this feature was recorded:
`{{record-baseline.test_baseline}}`
"Done" gates on introducing **no NEW failures vs that baseline** (and no new
build break), NOT on a fully green suite. Do not spend passes chasing
pre-existing failures that are already in the baseline; if you touch one, treat
it as out of scope and log it under `gaps-identified.md`'s "Won't Fix" section.

## RALPH.md is the authority

`./work/RALPH.md` carries the loop protocol — Constraints, Iteration Structure,
and Wrap Up. **Read it first and follow it.** The summary below must agree with
it; if they ever disagree, RALPH.md wins.

## Constraints

- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **ONE focus area per pass. Fix the most important issue, then stop.** Do not sweep several areas in one pass — the loop cannot distinguish progress from churn if you do.
- **Do NOT invent new functionality.** Log gaps in `gaps-identified.md`; out-of-scope items go under `Won't Fix`.

## Each pass
1. Read `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
2. Run the test command(s) above (and build) to see current status; compare
   failures against the baseline to distinguish NEW failures from pre-existing.
3. Pick the **single highest-priority** item from `focus-areas.md` (NEW failures
   first). Make the smallest correct change that advances it — strict TDD when
   adding behavior (failing test for happy/success/failure/error/edge → minimal
   code → green).
4. Re-run the affected tests.
5. **Assess the checklist**, then proceed to Wrap Up regardless of its state.
   **Do not go back to step 3.**

Most focus areas need several passes. That is normal and expected — do not try to
finish everything in one pass, and never mark an area complete just to end the loop.

## Wrap Up (every pass, without exception)

**Step A** — Did the focus area pass every checklist item in RALPH.md?

**Step B** — If it passed: mark it complete in `focus-areas.md`. If not: do **NOT**
update tracking. Add any newly discovered gaps to `gaps-identified.md` either way.

**Step C** — Commit and push. **Your work is valuable regardless of checklist status:**
`git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): <what you changed>" && git -C ./work push --quiet -u origin HEAD`

**Step D** — Check both files:
1. Are ALL focus areas in `focus-areas.md` marked complete?
2. Are there ANY unchecked `[ ]` items in `gaps-identified.md`?

Then output exactly one tag as the last line of your response:
- All complete AND no open issues: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.** The workflow reads this tag literally to decide
whether to run another pass, so emit exactly one and emit it last.
