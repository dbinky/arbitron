# Mission: Produce Implementation Plans

You are writing **detailed, code-level implementation plans** for a skilled engineer who has zero context for this codebase and questionable test taste. Work **unattended** in the worktree at `./work`. Do not ask questions.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`

## Output
For each design phase, write one or more **task plan files** at:
`./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-{N}-task-{M}.md`
(N = design phase number, M = task number within the phase).

## Rules (follow exactly)
- **Bite-sized steps (2–5 min each)**, using checkbox syntax `- [ ]`. The canonical TDD cycle per unit of behavior:
  write the failing test → run it, confirm it fails → minimal implementation → run it, confirm it passes → commit.
- **Strict TDD:** include tests for happy-path, success, failure, error, and edge-case scenarios.
- **Exact file paths** always. **Complete code** in every code step (never "add validation" / "handle errors" — show the code). **Exact commands** with expected output.
- DRY, YAGNI, frequent commits.
- Each task file starts with a header: a one-line Goal, 2–3 line Architecture, and Tech Stack line.
- Do not include any "offer execution choice" or sub-skill handoff — execution is handled by the pipeline.

## Finish
`git -C ./work add docs/plans/ && git -C ./work commit -m "docs: write {{slug}} implementation plans"`
Report the number of task files written.