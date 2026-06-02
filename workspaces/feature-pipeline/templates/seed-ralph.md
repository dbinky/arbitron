# Mission: Seed Ralph Refinement Files

Working **unattended** in the worktree at `./work`, generate the three ralph tracking files at the repo root of the worktree, then commit. The user is unavailable.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`

## Create (in `./work`)
- `RALPH.md` — the refinement charter: the goal of this feature, how to run the project's tests/build, the definition of done (all plan tasks implemented, full test suite green incl. happy/success/failure/error/edge, designs satisfied), and links to the spec/designs/plans.
- `focus-areas.md` — an ordered, prioritized list of the most important remaining work to reach done (failing tests first, then gaps vs. the plans/designs).
- `gaps-identified.md` — concrete known gaps between current code and the spec/designs/plans (missing tests, unimplemented behavior, TODOs).

## Finish
`git -C ./work add RALPH.md focus-areas.md gaps-identified.md && git -C ./work commit -m "chore: seed {{slug}} ralph refinement files"`
Report the top 3 focus areas.