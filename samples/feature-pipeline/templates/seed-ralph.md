# Mission: Seed Ralph Refinement Files

Working **unattended** in the worktree at `./work`, generate the three ralph tracking files at the repo root of the worktree, then commit. The user is unavailable.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`
- **Test command(s)** (use these exact commands — already detected for this repo): `{{record-baseline.test_commands}}`
- **Branch-point test baseline** (failures that existed BEFORE this feature): `{{record-baseline.test_baseline}}`

## Create (in `./work`)
- `RALPH.md` — the refinement charter: the goal of this feature, the exact test/build command(s) to run (from the test command(s) above — do NOT invent or guess a command), the definition of done (below), and links to the spec/designs/plans.
- `focus-areas.md` — an ordered, prioritized list of the most important remaining work to reach done (newly-introduced test failures first, then gaps vs. the plans/designs).
- `gaps-identified.md` — concrete known gaps between current code and the spec/designs/plans (missing tests, unimplemented behavior, TODOs).

## Definition of done — gate on NEW failures, not a green suite
The repo may already be red on arrival. Pre-existing failures are NOT this
feature's job and MUST NOT block the loop. Write RALPH.md's done-condition and
checklist so that "done" means ALL of:
- Every plan task is genuinely implemented (not merely present).
- The feature's behaviors have meaningful coverage across all five scenario
  categories (happy / success / failure / error / edge).
- The Phase 1–3 designs are satisfied in spirit.
- **No NEW test failures** beyond the recorded branch-point baseline above —
  i.e. the current set of failing tests is a subset of the baseline. A test that
  was already failing at the baseline does NOT block.
- **No new build/compile break.** A suite that built at the baseline must still
  build. (A *build* regression always blocks, even though pre-existing *test*
  failures do not.)

Make the RALPH.md checklist's test box read like:
"No new test failures vs the recorded baseline (pre-existing failures excluded), and nothing that built at baseline is now broken."
Do NOT write a checklist box that requires the entire suite to be green — that is
the bug this pipeline was changed to avoid.

## Finish
`git -C ./work add RALPH.md focus-areas.md gaps-identified.md && git -C ./work commit -m "chore: seed {{slug}} ralph refinement files"`
Report the top 3 focus areas.
