# Mission: Paired Integration Refinement Pass

Working **unattended** in the worktree at `./work`. The user is unavailable —
make the calls yourself.

If `{{workspace}}/tamper/must-fix.md` exists, address it before anything else this pass.

## Your assignment this pass

- **Work item:** `{{select-work.target_id}}` — {{select-work.target_label}}
- **Phase 2.** Every single-system area is approved; this is the integration
  sweep. A paired row is **one seam**, named `A ↔ B`, and its Seam cell is a trace
  recipe: **SHARED** (the state, contract, flag or ordering the two behaviours
  share), **TRACE** (the path from entry, through the seam, to the observable
  result) and **BREAK** (the failure that must be shown impossible or caught).
  Review it in **both directions in this pass**: what A assumes about B, and what
  B assumes about A. One approval covers both; there is no reverse row.
- If the row is directional (plans seeded before 2026-09-11 read `A as impacted
  by B` and carry the reverse as its own row), review the direction it names and
  do not add rows for it.
- **Batching.** If other unchecked rows share the context you have **already
  loaded**, you may review them in this same pass, up to four rows in total. Each
  seam is examined properly and reported separately; a batch is several reviews
  done in one sitting, never one review stretched over rows. Do not assume rows
  will batch: the last plan's rows batched in 7 of 66 passes.
- **If you find a real seam listed under `## No seam`, promote it**: add one row
  with SHARED, TRACE and BREAK stated and remove it from that list. Deferred is a
  decision, not a closed door. Conversely, move a row there (with a reason) if it
  turns out to be an internal hop of one behaviour or to share nothing.
- **Router status:** {{select-work.summary}}

Work only the assigned row and any rows you legitimately batch with it.

{{#select-work.escalate}}
> **ESCALATION REQUIRED.** This is at least the fourth consecutive pass on this
> same item, so something here is not converging. Record an `[ESCALATION]` entry
> in `docs/reference/gaps-identified.md` naming precisely what would have to
> become true for this box to be ticked — a decision someone must make, a fixture
> that does not exist, a dependency nothing provides. Then do what you can and
> stop. Do not spend another pass rediscovering the same wall.
{{/select-work.escalate}}

## You fix; someone else approves

A separate approver on a **different model family** runs immediately after you
and is the only agent permitted to tick a box in
`docs/reference/paired-reviews.md`. You must never edit an approval box.

This is not bureaucracy. An agent that certifies its own change has performed no
review, and a mistake it approves stops being visible and compounds into every
later pass. Your recommendation carries weight — but the approver may withhold,
and that is the system working.

You do own `docs/reference/gaps-identified.md`: record what you find there.

## Read the ledger selectively

Do not read `gaps-identified.md` end to end: one ledger reached 1.4 MB and no
longer fit in any context. Read the OPEN items (`grep -n '\[ \]'`) and any section
naming either behaviour of your seam. When you record, write at most ten lines
per entry with `file:symbol`; move an item you close under `## Closed` with a
one-line note.

## Test command(s)

The project's suites, pre-detected — do NOT guess a different toolchain:
`{{record-baseline.test_commands}}`

**Run targeted tests, not whole suites.** Run the tests for the code you
changed — the package, directory or name filter that covers it (`go test
./shared/core/foo/ -run TestBar`, `dotnet test --filter`, a single spec file).
Do not re-run entire suites to prove nothing else broke: after you commit, the
`verify-tests` script re-runs every suite your change touched and diffs it
against the baseline. A whole-suite run from you repeats that work and is the
single largest time cost in a pass.

## Pre-existing failures are not your job

Branch-point baseline: `{{record-baseline.test_baseline}}`

"Done" gates on introducing **no NEW failures vs that baseline**, not on a green
suite. Do not chase pre-existing failures; if you touch one, log it under
`gaps-identified.md`'s "Won't Fix". A script re-runs the affected suites after
you commit and diffs them against that baseline, so your test claims are checked
mechanically — there is nothing to gain from describing a run you did not finish.

## Tests run in the foreground, or they do not run

Run the command(s) synchronously and read the result **within this pass**. Never
end a pass waiting on a background run. A pass spent waiting produces no commit
and scores as churn; two in a row and the loop escalates or stops with the work
half-done.

If a suite is too slow or still going when you are ready to assess: do not wait.
Record it in `gaps-identified.md` (which suite, the exact command, what is
unverified), base your assessment on what completed, and say so. If the missing
evidence is what would have justified approval, recommend no approval.

## RALPH.md is the authority

`./work/RALPH.md` carries the loop protocol for all three roles. **Read the
section for your role first and follow it.** If this summary and RALPH.md ever
disagree, RALPH.md wins, with two exceptions. **Test scope:** where RALPH.md
says to run the test commands, run targeted tests as described above.
**Pushing:** never push, even where RALPH.md says to; the workflow pushes the
pass once the tamper gate has ruled on it.

## Each pass

1. Read `./work/RALPH.md`, your row in `docs/reference/paired-reviews.md`, the
   `### Area` blocks of both behaviours it joins in `docs/reference/focus-areas.md`,
   and the open items in `docs/reference/gaps-identified.md`.
2. If either side of the seam does not build or its targeted tests already fail,
   note it against the baseline before changing anything.
3. Read every file on BOTH sides of the seam and the seam itself, following the
   row's TRACE from entry to observable result.
4. Demonstrate that BREAK cannot happen, in both directions: a test that goes red
   when the seam is broken, or a cited guard at `file:symbol`. Then apply the
   **wired-and-fed** criteria to the seam:
   - `grep -rn` the SHARED symbol, table, topic or flag on both sides — is the
     producer really feeding the consumer in production, not only in tests?
   - Is each side's dependency on the other assembled at the real composition
     root? Name the wiring site or fail it.
   - Does any production call site pass `nil`/empty/hardcoded across the seam?
   - Do any tests pass only because a fake on one side ignores what the other
     side sends?
   - If SHARED is an ordering, what enforces it? A check that runs afterwards
     does not.
5. Make the smallest correct change that advances the seam — strict TDD when
   adding behaviour.
6. Run the targeted tests for what you changed (see Test command(s)).
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back
   to step 5 to squeeze in another fix.

Most seams resolve in one pass; some need several. That is normal — never inflate
a recommendation to end the loop sooner.

## Wrap Up (every pass, without exception)

Nothing defers Wrap Up — not a running test, not an inconclusive result, not an
unfinished fix. If you did anything at all, you reach Step C and commit it.

**Step A** — Is the seam sound in both directions, with BREAK demonstrated
impossible or caught?

**Step B** — Record your **recommendation** per row; tick nothing. One line per
row, exactly one of:

`Recommend: approve {{select-work.target_id}} — <the evidence, both directions>`
`Recommend: no approval for {{select-work.target_id}} — <what is still missing>`

Cite what you verified (`file:symbol`, the grep you ran, the test that fails
when the seam is broken). Record anything newly discovered in
`gaps-identified.md`.

**Step C** — Commit. Your work is valuable regardless of review status:
`git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): {{select-work.target_id}} — <what you changed>"`
Commit only — never push. The workflow pushes the pass once the tamper gate has
ruled on it.

**Step D** — State what this pass advanced, as a single line:

`Progress: <what advanced>`

Name the concrete thing — the fix you landed, the gap you closed, the component
you wired, the NEW failure you removed. If nothing advanced, say so and why.
**This line is the only place your report says what you ACCOMPLISHED**; Step E
reports what REMAINS, and the progress assessor has no tools and sees only your
response.

**Step E** — Check the tracking files: does every single-system review have both
approvals and every paired review its one approval, and are there any unchecked
`[ ]` items in `gaps-identified.md`? Then output exactly one tag as the last line:

- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**
