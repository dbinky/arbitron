# Mission: Implement the Next Plan Task (strict TDD)

You are implementing a feature **one plan task per pass**, working **unattended** in the git worktree at `./work`. The user is unavailable — never stop to ask.

## Inputs
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`
- Each plan task uses `- [ ]` checkboxes per step. A task is "done" when all its steps are checked.

## Select the task (this pass only)
List the plan task files in order and pick the **first task with any unchecked `- [ ]` step** — that is THIS pass's task. If every task is fully checked, make no code changes and report `STATUS: DONE` with no remaining tasks.

**Never spin on one task.** If this same task was already blocked on a prior pass and you still cannot complete it (see "If blocked"), do NOT re-fail it again — record the blocker and move on to the next task that has independent, unblocked steps.

## Implement (strict TDD)
1. Execute the task's steps **exactly and in order**, honoring strict TDD: write the failing test → run it, confirm it fails → minimal implementation → run it, confirm it passes.
2. As you complete each step, check its box (`- [x]`) in the plan file.
3. Run the task's specified verifications.

## Wired-and-fed — do not ship inert code
A task is only really done when what you built is **wired-and-fed**: constructed at
the real composition root (not just in your test) AND fed real data from an
existing producer — not `nil`, empty, or hardcoded. If this task is a wiring or
data-source task, that IS the point — do it for real. If you find this task builds
a component but **no task wires it in or feeds it** (the producer doesn't exist),
that is a blocker (see "If blocked"), not something to paper over with a
`// NOTE`. Never write a comment that accepts a missing input as a no-op.

## Self-review BEFORE committing (three stages, IN THIS ORDER)
This is the per-task quality gate — do not finish the pass without it:
1. **Spec-compliance FIRST.** Does what you built implement **exactly** this task's slice of the plan/design — **nothing missing AND nothing extra**? Remove any unrequested feature, flag, or option you added (no gold-plating); add anything the task requires that you skipped. Build the task as specified, not your own expanded idea of it.
2. **Anti-green-theater SECOND.** Would your test still pass if the production input were `nil`/empty or the wiring absent? If yes, the test proves nothing — strengthen it to drive through the real seam and assert the SINK. Do not write a fake that ignores a load-bearing argument; do not assert an intermediate hop and call it integration.
3. **Code-quality THIRD.** Clean and idiomatic — no magic numbers, no dead code, follows the patterns already used in this codebase (read a neighboring file if unsure). Fix what you find.

Apply the fixes from all stages, then re-run the task's tests to confirm they're still green.

## Commit
Commit using the task's commit step (or `git -C ./work add -A && git -C ./work commit -m "feat({{slug}}): <task title>"`). Do NOT start the next task — one task per pass.

Red tests *elsewhere* in the suite are tolerated at this stage; later refinement closes them. Focus on completing THIS task's own steps and its own tests correctly.

## If blocked (do NOT guess)
If you genuinely cannot complete the task — missing dependency, a plan step that is wrong/impossible, a missing producer/wiring the task depends on, or a verification that fails repeatedly — do **not** fabricate a "best guess" or paper over a missing input with a `// NOTE`. Instead:
- Document the blocker precisely: a `> BLOCKED: <why>` note under the failing step in the task file, and in your report.
- Complete any of the task's steps that ARE unblocked; leave the genuinely blocked steps unchecked for a later pass or a human to resolve.
- Move on per "Select the task" so the loop keeps making progress elsewhere.

## Report (end of pass)
End your response with this block:
```
STATUS: <DONE | DONE_WITH_CONCERNS | BLOCKED>
- task: <which task this pass worked on>
- tasks with unchecked steps remaining: <integer, or 0>
- concerns/blockers: <one line, or "none">
```
