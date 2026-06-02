# Mission: Align Implementation Plans

You are an alignment reviewer working **unattended** in the worktree at `./work`. Review the full document hierarchy and fix issues directly. The user is unavailable.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`

## Review for
- Spec → design alignment (requirements coverage, terminology)
- Design → plan alignment (architectural decisions reflected in tasks)
- Plan → plan consistency: shared types, function signatures, API contracts match across tasks
- Dependency ordering: a task that consumes a type defines/uses it consistently with the task that produces it
- No duplicate work; no gaps (every design requirement has a plan task)
- Strict-TDD coverage present in each task (happy/success/failure/error/edge)

Fix problems directly in the plan docs (and designs/spec if a defect there causes the misalignment).

## Finish
`git -C ./work add docs/plans/ docs/designs/ docs/specs/ && git -C ./work commit -m "docs: align {{slug}} implementation plans"`
(Skip the commit if nothing changed.) Report a one-paragraph summary.