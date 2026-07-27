# Mission: Ralph Refinement Pass

Working **unattended** in the worktree at `./work`, advance exactly **one** review
area, then stop. The user is unavailable.

## Test command(s)
Use these exact, pre-detected command(s) — do NOT guess a different one:
`{{record-baseline.test_commands}}`

## Pre-existing failures are not your job
A branch-point baseline of failures that existed BEFORE this branch was recorded:
`{{record-baseline.test_baseline}}`
"Done" gates on introducing **no NEW failures vs that baseline** (and no new build
break), NOT on a fully green suite. Do not spend passes chasing pre-existing
failures; if you touch one, treat it as out of scope and log it under
`docs/reference/gaps-identified.md`'s "Won't Fix" section.

## RALPH.md is the authority

`./work/RALPH.md` carries the loop protocol — Constraints, Iteration Structure and
Wrap Up. **Read it first and follow it.** The summary below must agree with it; if
they ever disagree, RALPH.md wins.

## Constraints

- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **ONE review area per pass. Fix the most important issue, then stop.** Do not sweep several areas in one pass — the loop cannot distinguish progress from churn if you do.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **Approvals are independent.** Never tick both status boxes of a single-system area in one pass. The second approval exists to catch what the first missed; awarding both at once destroys the only redundancy in this design.

## Each pass

1. Read `./work/RALPH.md`, `./work/docs/reference/focus-areas.md`, `./work/docs/reference/gaps-identified.md`.
2. Run the test command(s) above (and build) to see current status; compare
   failures against the baseline to distinguish NEW failures from pre-existing.
3. Pick the **next review with an unchecked status** — single-system reviews
   first (lowest number), then paired integration reviews. For a paired review
   (A, B), review the completeness, correctness and spec alignment of **A** as it
   is impacted by updates in **B**.
4. Apply the wired-and-fed criteria from RALPH.md: grep each new symbol for
   construction outside `*_test.go`, confirm composition-root wiring, look for
   `nil`/empty/hardcoded production inputs, and check five-category coverage.
   Make the smallest correct change that advances the area — strict TDD when
   adding behaviour.
5. Re-run the affected tests.
6. **Assess the review**, then proceed to Wrap Up regardless of the outcome.
   **Do not go back to step 4** to squeeze in another fix.

Most review areas need several passes. That is normal — do not try to finish
everything at once, and never record an approval just to end the loop sooner.

## Wrap Up (every pass, without exception)

**Step A** — Did the review area meet every approval criterion?

**Step B** — If it did: tick exactly ONE status box for it in
`docs/reference/focus-areas.md`. If it did not: leave the boxes alone. Either way,
record anything newly discovered in `docs/reference/gaps-identified.md`.

**Step C** — Commit and push. **Your work is valuable regardless of review status:**
`git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): <what you changed>" && git -C ./work push --quiet -u origin HEAD`

**Step D** — Check both files:
1. Does every single-system review have BOTH approvals, and every paired review its one approval?
2. Are there ANY unchecked `[ ]` items in `docs/reference/gaps-identified.md`?

Then output exactly one tag as the last line of your response:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.** The workflow reads this tag to decide whether
to run another pass, so emit exactly one and emit it last.
