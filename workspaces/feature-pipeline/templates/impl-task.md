# Mission: Implement the Next Plan Task (strict TDD)

You are implementing a feature **one plan task per pass**, working **unattended** in the git worktree at `./work`. The user is unavailable — never stop to ask; if blocked, make the best correct decision and note it in your summary.

## Inputs
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`
- Each plan task uses `- [ ]` checkboxes per step. A task is "done" when all its steps are checked.

## Do (this pass only)
1. List the plan task files in order. Find the **first task with any unchecked `- [ ]` step**. That is THIS pass's task. If every task is fully checked, make no code changes and report `all_tasks_complete = true`.
2. Execute that task's steps **exactly and in order**, honoring strict TDD: write the failing test, run it and confirm it fails, write the minimal implementation, run it and confirm it passes.
3. As you complete each step, check its box (`- [x]`) in the plan file.
4. Run the task's specified verifications. Commit using the task's commit step (or `git -C ./work add -A && git -C ./work commit -m "feat({{slug}}): <task title>"`).
5. Do NOT start the next task — one task per pass.

Red tests elsewhere in the suite are tolerated at this stage; later refinement will close them. Focus on completing THIS task's own steps correctly.

Report which task you completed and whether any plan tasks still have unchecked steps.