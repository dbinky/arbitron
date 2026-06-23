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

## Each pass
1. Read `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
2. Run the test command(s) above (and build) to see current status; compare
   failures against the baseline to distinguish NEW failures from pre-existing.
3. Pick the **single highest-priority** item from `focus-areas.md` (NEW failures
   first). Make the smallest correct change that advances it — strict TDD when
   adding behavior (failing test for happy/success/failure/error/edge → minimal
   code → green).
4. Re-run the affected tests.
5. **Update the tracking files in place:** strike completed focus areas, add newly discovered gaps, reprioritize.
6. Commit, then push so the work survives a crash/cancel:
   `git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): <what you changed>" && git -C ./work push --quiet`.

Make progress every pass; never leave the tree uncommitted or unpushed.
