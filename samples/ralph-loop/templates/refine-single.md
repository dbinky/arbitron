# Mission: Single-System Refinement Pass

Working **unattended** in the worktree at `./work`. The user is unavailable —
make the calls yourself.

## Your assignment this pass

- **Work item:** `{{select-work.target_id}}` — {{select-work.target_label}}
- **Which review:** `{{select-work.next_kind}}`
  - `single_1` → you are performing **Review 1**, the first independent look.
  - `single_2` → you are performing **Review 2**. Review 1 already passed. Your
    job is explicitly to find **what the first reviewer missed**, on a different
    model from the one that did Review 1 — that difference is the entire reason
    two approvals exist. Agreeing by default wastes the pass.
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

## You fix; someone else approves

A separate approver on a **different model family** runs immediately after you
and is the only agent permitted to tick a box in
`docs/reference/focus-areas.md`. You must never edit an approval box.

This is not bureaucracy. An agent that certifies its own change has performed no
review, and a mistake it approves stops being visible and compounds into every
later pass. Your recommendation carries weight — but the approver may withhold,
and that is the system working.

You do own `docs/reference/gaps-identified.md`: record what you find there.

## Test command(s)

Use these exact, pre-detected command(s) — do NOT guess a different one:
`{{record-baseline.test_commands}}`

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
disagree, RALPH.md wins.

## Each pass

1. Read `./work/RALPH.md`, `docs/reference/focus-areas.md`, `docs/reference/gaps-identified.md`.
2. Run the test command(s) and compare failures against the baseline.
3. Read every file the assigned area covers.
4. Apply the **wired-and-fed** criteria, with real commands:
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
5. Make the smallest correct change that advances the area — strict TDD when
   adding behaviour.
6. Re-run the affected tests.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back
   to step 5 to squeeze in another fix.

Most areas need several passes. That is normal — never inflate a recommendation
to end the loop sooner.

## Wrap Up (every pass, without exception)

Nothing defers Wrap Up — not a running test, not an inconclusive result, not an
unfinished fix. If you did anything at all, you reach Step C and commit it.

**Step A** — Did the area meet every approval criterion for *your* review?

**Step B** — Record your **recommendation**; tick nothing. One line, exactly one of:

`Recommend: approve {{select-work.target_id}} — <the evidence>`
`Recommend: no approval for {{select-work.target_id}} — <what is still missing>`

Cite what you verified (`file:symbol`, the grep you ran, the test that fails
without the production input). Record anything newly discovered in
`gaps-identified.md`.

**Step C** — Commit and push. Your work is valuable regardless of review status:
`git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): {{select-work.target_id}} — <what you changed>" && git -C ./work push --quiet -u origin HEAD`

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
