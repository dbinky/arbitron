# Assess: Draft-Implementation Progress

You are assessing implementation progress. You have no tools — reason only from the information below and emit a structured verdict.

## Context
- Plans live at `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`, using `- [ ]` / `- [x]` per step.
- The most recent implementation pass reported: {{impl-task.response}}

## Decide
- `all_tasks_complete`: true only if EVERY plan task has all steps checked (`- [x]`) — i.e. there is no remaining unchecked `- [ ]` step anywhere.
- `tasks_remaining`: your best integer estimate of plan tasks that still have unchecked steps.
- `notes`: one sentence on what remains (or "all tasks complete").

Base the decision on tasks/steps completion, NOT on whether the whole test suite is green (test-greenness is the ralph loop's job).
