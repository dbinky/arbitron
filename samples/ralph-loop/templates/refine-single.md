# Mission: Single-System Refinement Pass

Working **unattended** in the worktree at `./work`. The user is unavailable —
make the calls yourself.

If `{{workspace}}/tamper/must-fix.md` exists, address it before anything else this pass.

## Your assignment this pass

- **Work item:** `{{select-work.target_id}}` — {{select-work.target_label}}
- **Which review:** `{{select-work.next_kind}}`
  - `single_1` → you are performing **Review 1, the WIRING lens**: does the path
    exist and is it fed? Composition root, real producer, happy and success paths
    reaching the sink.
  - `single_2` → you are performing **Review 2, the FAILURE lens**: how does it
    fail? Nil and empty inputs, timeouts, partial failure and rollback,
    concurrency, ordering. Review 1 already passed on the wiring lens; you are on
    a different model family asking a **different question** — that is the entire
    reason two approvals exist. Re-checking the wiring wastes the pass.
- **Router status:** {{select-work.summary}}

Work **only** this area. Do not sweep others; the loop cannot tell progress from
churn if you do.

{{#select-work.escalate}}
> **ESCALATION REQUIRED.** This is at least the fourth consecutive pass on this
> same item, so something here is not converging. Record an `[ESCALATION]` entry
> in `docs/reference/gaps-identified.md` naming precisely what would have to
> become true for this box to be ticked — a decision someone must make, a fixture
> that does not exist, a dependency nothing provides. Then do what you can and
> stop. Do not spend another pass rediscovering the same wall.
{{/select-work.escalate}}

## The area is a behaviour, and its block tells you where it lives

`docs/reference/focus-areas.md` carries a `### Area N — <name>` block under the
table for your row: **Promise**, **Entry**, **Sink**, **Files**, **Folded in**, the
numbered checks for **your lens**, and **Seed evidence**. Load every file in
**Files**, entry to sink, and work your lens's checks in order. The other lens's
checks are not your job this pass.

If the plan has no block for this area (plans seeded before 2026-09-11 have only
the table row), derive entry, sink and files yourself from the row and the code
before you start, and apply the wired-and-fed criteria below through your lens.

## You fix; someone else approves

A separate approver on a **different model family** runs immediately after you
and is the only agent permitted to tick a box in
`docs/reference/focus-areas.md`. You must never edit an approval box.

This is not bureaucracy. An agent that certifies its own change has performed no
review, and a mistake it approves stops being visible and compounds into every
later pass. Your recommendation carries weight — but the approver may withhold,
and that is the system working.

You do own `docs/reference/gaps-identified.md`: record what you find there.

## Read the ledger selectively

`docs/reference/gaps-identified.md` grows every pass. Do **not** read it end to
end: one ledger reached 1.4 MB and no longer fit in any context. Read the OPEN
items (`grep -n '\[ \]' docs/reference/gaps-identified.md`) and any section that
names your area, and nothing else. When you record, write at most ten lines per
entry with `file:symbol`; move an item you close under `## Closed` with a one-line
note rather than leaving its history in the open list.

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

1. Read `./work/RALPH.md`, your row and its `### Area N` block in
   `docs/reference/focus-areas.md`, and the open items in `docs/reference/gaps-identified.md`.
2. If the area's code does not build or its targeted tests already fail, note it
   against the baseline before changing anything.
3. Read every file in the area's **Files** list, entry to sink.
4. Work your lens's numbered checks with real commands. The **wired-and-fed**
   criteria behind them:
   - `grep -rn <Symbol>` each new exported field / collaborator / config — is it
     constructed or populated **outside `*_test.go`**? Only ever set in tests → the area fails.
   - Is each new component assembled into the real composition root so the live
     path reaches it? Name the wiring site or fail it.
   - Does any production call site pass `nil`/empty/hardcoded where a real
     producer should feed it?
   - Do any tests pass only because a fake ignores a load-bearing argument, or an
     "integration" test asserts an intermediate hop instead of the sink?
   - All five scenario categories covered (happy, success, failure, error, edge),
     and would at least one test **fail** if its production input were nil/empty?
   Cover the area's **Folded in** items too; they are part of this review.
5. Make the smallest correct change that advances the area — strict TDD when
   adding behaviour.
6. Run the targeted tests for what you changed (see Test command(s)).
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back
   to step 5 to squeeze in another fix.

A finding that belongs to the **other** lens is recorded as a gap for that review,
not fixed now and not a reason to recommend against this one. Most areas need
more than one pass. That is normal — never inflate a recommendation to end the
loop sooner.

## Wrap Up (every pass, without exception)

Nothing defers Wrap Up — not a running test, not an inconclusive result, not an
unfinished fix. If you did anything at all, you reach Step C and commit it.

**Step A** — Did the area meet every check of *your* lens?

**Step B** — Record your **recommendation**; tick nothing. One line, exactly one of:

`Recommend: approve {{select-work.target_id}} — <the evidence>`
`Recommend: no approval for {{select-work.target_id}} — <what is still missing>`

Cite what you verified (`file:symbol`, the grep you ran, the test that fails
without the production input). Record anything newly discovered in
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
