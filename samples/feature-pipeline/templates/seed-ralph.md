# Mission: Seed Ralph Refinement Files

Working **unattended** in the worktree at `./work`, generate the three ralph tracking files at the repo root of the worktree, then commit. The user is unavailable.

## Inputs
- Spec: `./work/{{spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`
- **Test command(s)** (use these exact commands — already detected for this repo): `{{record-baseline.test_commands}}`
- **Branch-point test baseline** (failures that existed BEFORE this feature): `{{record-baseline.test_baseline}}`

## Create (in `./work`)
- `RALPH.md` — the refinement charter: the goal of this feature, the exact test/build command(s) to run (from the test command(s) above — do NOT invent or guess a command), the definition of done (below), and links to the spec/designs/plans.
- `focus-areas.md` — an ordered, prioritized list of the most important remaining work to reach done (newly-introduced test failures first, then **whole-pipeline wiring/provenance gaps**, then gaps vs. the plans/designs).
- `gaps-identified.md` — concrete known gaps between current code and the spec/designs/plans (missing tests, unimplemented behavior, TODOs, **and every inert/no-op path**).

## Seed focus-areas.md with WHOLE-PIPELINE provenance checks (not just per-component)
The recurring failure mode is code that is present and green in isolation but
**never wired-and-fed** in production. Seed these as concrete, named focus items
(do the greps now and cite real findings, not generic reminders):
- For **every** new exported field / collaborator / config the feature added:
  `grep -rn <Symbol>` the whole repo — is it **constructed/populated outside
  `*_test.go`**? If it's only ever set in tests, that's a top-priority gap.
- For **every** new component: is it assembled into the real composition root
  (server/DI wiring) so the live path reaches it? Name the wiring site or flag it.
- Does any production call site pass `nil`/empty/hardcoded where a real producer
  should feed it? (e.g. a scorer injected `nil`, an embedding set left empty, a
  bucket hardcoded.) List each.
- Do any tests pass only because a **fake ignores a load-bearing argument**, or
  because an "integration" test asserts an intermediate hop instead of the sink?

## Definition of done — gate on NEW failures AND on wired-and-fed
The repo may already be red on arrival. Pre-existing failures are NOT this
feature's job and MUST NOT block the loop. Write RALPH.md's done-condition and
checklist so that "done" means ALL of:
- Every plan task is genuinely implemented — **wired-and-fed, not merely present**:
  constructed at the real composition root AND fed real data from a producer that
  exists in the code (NOT `nil`/empty/hardcoded, NOT only set in tests).
- The feature's behaviors have meaningful coverage across all five scenario
  categories (happy / success / failure / error / edge), and **at least one test
  per component would fail if its production input were nil/empty** (green
  over-mocked tests do NOT count as done).
- The Phase 1–3 designs are satisfied in spirit.
- **No NEW test failures** beyond the recorded branch-point baseline above —
  i.e. the current set of failing tests is a subset of the baseline.
- **No new build/compile break.** A suite that built at the baseline must still build.

Make the RALPH.md checklist include BOTH:
- "No new test failures vs the recorded baseline (pre-existing failures excluded), and nothing that built at baseline is now broken."
- "Every new component is wired-and-fed: constructed at the real composition root and fed a real producer (verified by grep, not by a green isolated test)."

Do NOT write a checklist box that requires the entire suite to be green. And do
NOT let `gaps-identified.md` record an inert/no-op path (e.g. "empty set treated
as maximally varied — acceptable") as *acceptable*: an unfed production path is a
GAP to fix or an explicit escalation, never a tolerated no-op.

## Loop protocol — RALPH.md MUST contain this section VERBATIM

This is the part that makes the loop a loop. **Transcribe the block below into
RALPH.md exactly as written — do not paraphrase it, summarise it, renumber it, or
invent your own.** Substitute only the two bracketed placeholders.

Without it the refinement agent has no rule for what *ends* a pass: it will
improvise a protocol that says what to do and never says when to stop, sprawl
across several focus areas in a single pass, and leave the loop unable to tell
progress from churn.

---8<--- copy from here ---8<---

## Constraints

- **Do NOT use sub-agents.** Modify and create code by hand, one component at a time.
- **Read before you write.** Before modifying any file, read it. Before claiming something is fine, verify it.
- **One focus area per iteration. Fix the most important issue, then stop.**
- **Do NOT invent new functionality.** Log perceived gaps in `gaps-identified.md`. If something is clearly beyond scope, put it in `Won't Fix`.
- **Test coverage matters.** Every public function in the focus area should have tests covering happy path, success, failure, error, and edge cases.

## Iteration Structure

1. **Read tracking** — Read `focus-areas.md`. Pick the next incomplete focus area (lowest number first).
2. **Read the code** — Read every file in the focus area. Understand patterns, contracts, edge cases.
3. **Cross-reference** — Compare against the spec and phase designs. Add issues to `gaps-identified.md`.
4. **Fix the most important issue** — ONE issue per iteration. Fix it well across all affected files.
5. **Write/verify tests** — Cover happy path, success, failure, error, and edge cases. Strict TDD when adding behaviour.
6. **Run tests** — Run `[TEST COMMANDS]`. Compare failures against the recorded baseline; fix NEW failures only.
7. **Assess the checklist** — Evaluate the checklist, then proceed to Wrap Up regardless of checklist state. **Do not go back to step 4.**

Most focus areas need multiple passes to complete. This is normal and expected —
do not try to finish everything in one pass, and do not mark an area complete to
make the loop end sooner.

## Wrap Up

**Step A** — Did the focus area pass every checklist item?

**Step B** — If it passed: mark the focus area complete in `focus-areas.md`. If it did not: do **NOT** update tracking.

**Step C** — Commit and push. Your work is valuable regardless of checklist status:
`git -C ./work add -A && git -C ./work commit -m "refine([SLUG]): <what you changed>" && git -C ./work push --quiet -u origin HEAD`

**Step D** — Check both files:
1. Are ALL focus areas in `focus-areas.md` marked complete?
2. Are there ANY unchecked `[ ]` items in `gaps-identified.md`?

Then output exactly one tag:
- All complete AND no open issues: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

---8<--- copy to here ---8<---

Substitutions: `[TEST COMMANDS]` → the exact commands from the Inputs above.
`[SLUG]` → `{{slug}}`. Everything else is copied unchanged — the
`<promise>` tags especially, because the workflow's refine gate reads them
literally to decide whether to run another pass.

## Finish
`git -C ./work add RALPH.md focus-areas.md gaps-identified.md && git -C ./work commit -m "chore: seed {{slug}} ralph refinement files" && git -C ./work push --quiet -u origin HEAD`
Report the top 3 focus areas, and confirm RALPH.md contains the verbatim Loop
protocol (Constraints, Iteration Structure, Wrap Up A–D, and both promise tags).
