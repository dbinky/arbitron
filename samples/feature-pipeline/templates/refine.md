# Mission: Ralph Refinement Pass

Working **unattended** in the worktree at `./work`, advance exactly **one** review
area, then stop. The user is unavailable.

## You fix; someone else approves

A separate approver agent runs immediately after you, on a different model. It
re-derives the evidence itself and is the **only** agent permitted to tick a box
in `docs/reference/focus-areas.md`. You must **never** edit an approval box.

This is not bureaucracy. An agent that certifies its own change has performed no
review, and a mistake it approves stops being visible and starts compounding into
every later pass. Your recommendation carries real weight — but it is a
recommendation, and the approver may withhold. That is a working system, not a
setback.

You still own `docs/reference/gaps-identified.md`: record what you find there.

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

A script runs the suite after you commit and diffs the result against that
baseline, so your test claims will be checked mechanically. A regression you
introduce blocks all approvals until it is fixed — there is nothing to be gained
by describing a run you did not complete.

## Tests run in the foreground, or they do not run

Run the test command(s) synchronously and read the result **within this pass**.
**Never end a pass waiting for a background run to finish.**

This is the single most common way this loop fails, and it has ended two runs
already. A pass spent waiting produces no commit and no tracking change, so the
progress gate correctly scores it as churn — two in a row and the loop escalates
or exits with the work half-done.

If a suite is too slow, was started in the background, or is still going when you
are ready to assess:

- **Do not wait for it.** Go to Wrap Up now.
- Record it in `docs/reference/gaps-identified.md`: which suite, the exact command,
  and what remains unverified because of it.
- Base your assessment on the suites that DID complete, and say so.
- If the missing evidence is what would have justified an approval, **recommend
  no approval** and say why. An unticked box costs one more pass; a hollow
  approval costs the whole review.

An assessment made on partial evidence with the gap written down is worth far more
than a pass that ends with nothing committed.

## RALPH.md is the authority

`./work/RALPH.md` carries the loop protocol — Roles, Constraints, Iteration
Structure and Wrap Up. **Read it first and follow it.** The summary below must
agree with it; if they ever disagree, RALPH.md wins.

## Constraints

- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **ONE single-system area per pass. Fix the most important issue, then stop.** Do not sweep several areas in one pass.
- **Paired integration reviews may be batched**: if several unchecked seams share an area you have already loaded, review up to **four** of them in one pass. They are one approval each and usually inspection rather than repair, so paying a whole pass per seam is waste. Examine each seam properly and report them separately — a batch is several reviews done in one sitting, not one review stretched over several rows.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **Never edit an approval box.** Approving your own work is not a review; the approver node does that.

## Each pass

1. Read `./work/RALPH.md`, `./work/docs/reference/focus-areas.md`, `./work/docs/reference/gaps-identified.md`.
2. Run the test command(s) above (and build) to see current status; compare
   failures against the baseline to distinguish NEW failures from pre-existing.
3. Pick the **highest-risk review with an unchecked status**, reading the Risk
   column across BOTH tables — not simply the lowest-numbered single-system row.
   Break ties by table (single-system first), then by number. This matters
   because the loop may be stopped by its iteration cap before the plan is
   finished: work taken in risk order means a truncated run has reviewed the
   things most likely to be broken, rather than everything alphabetically up to
   the point it ran out. For a paired review
   (A, B), review the completeness, correctness and spec alignment of **A** as it
   is impacted by updates in **B**, focusing on the **named seam** in that row.
4. **If you discover a real seam between two areas listed under `## No seam`,
   promote it.** Add the pair in both directions with the seam named, and remove
   it from the `## No seam` list. That list is a record of deliberate decisions,
   not a closed door — a seam found later is exactly what the paired table exists
   to catch, and leaving it unpaired because an earlier pass did not see it
   defeats the point. Conversely, if a listed pair turns out to share no seam,
   move it to `## No seam` with a one-line reason rather than leaving it to be
   approved hollow.
5. Apply the wired-and-fed criteria from RALPH.md: grep each new symbol for
   construction outside `*_test.go`, confirm composition-root wiring, look for
   `nil`/empty/hardcoded production inputs, and check five-category coverage.
   Make the smallest correct change that advances the area — strict TDD when
   adding behaviour.
6. Re-run the affected tests.
7. **Assess the review**, then proceed to Wrap Up regardless of the outcome.
   **Do not go back to step 5** to squeeze in another fix.

Most review areas need several passes. That is normal — do not try to finish
everything at once, and never inflate a recommendation just to end the loop sooner.

## Wrap Up (every pass, without exception)

**Nothing defers Wrap Up** — not a running test, not an inconclusive result, not a
failing suite, not an unfinished fix. If you have done anything at all this pass,
you reach Step C and commit it.

**Step A** — Did the review area meet every approval criterion?

**Step B** — Record your **recommendation**; do not tick anything. Write it as a
single line, exactly one of:

`Recommend: approve area <N> review <1|2> — <the evidence>`
`Recommend: no approval for area <N> — <what is still missing>`

Cite what you actually verified (`file:symbol`, the grep you ran, the test that
now fails without the production input). The approver re-checks independently, so
an unsupported recommendation simply costs a pass. Either way, record anything
newly discovered in `docs/reference/gaps-identified.md`.

**Step C** — Commit and push. **Your work is valuable regardless of review status:**
`git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): <what you changed>" && git -C ./work push --quiet -u origin HEAD`

**Step D** — State what this pass advanced, as a single line:

`Progress: <what advanced>`

Name the concrete thing — the fix you landed, the gap you closed, the component
you wired, or the NEW failure you removed. If nothing advanced, say so outright
and why:
`Progress: no fix landed — area 14 needs a fixture the plan does not define, logged as a gap.`

**This line is the only place your report says what you ACCOMPLISHED.** Step E
below reports what REMAINS, and the progress assessor has no tools — it sees only
your response. Without this line it is scoring "did this pass progress?" against a
narrative that structurally describes unfinished work, so it guesses. Two unlucky
guesses in a row escalate or end the run with the work still outstanding.

**Step E** — Check both files:
1. Does every single-system review have BOTH approvals, and every paired review its one approval?
2. Are there ANY unchecked `[ ]` items in `docs/reference/gaps-identified.md`?

Then output exactly one tag as the last line of your response:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.** The workflow reads this tag alongside a
mechanical count of the approval boxes, so emit exactly one and emit it last.
