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

## Finish
`git -C ./work add RALPH.md focus-areas.md gaps-identified.md && git -C ./work commit -m "chore: seed {{slug}} ralph refinement files" && git -C ./work push --quiet -u origin HEAD`
Report the top 3 focus areas.
