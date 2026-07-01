# Assess: Draft-Implementation Progress

You are assessing implementation progress. You have no tools — reason only from the information below and emit a structured verdict.

## Context
- Plans live at `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`, using `- [ ]` / `- [x]` per step.
- The most recent implementation pass reported the following (look for its trailing `STATUS:` block — `DONE` / `DONE_WITH_CONCERNS` / `BLOCKED`, the remaining-task count, and any concerns/blockers):

{{impl-task.response}}

## Decide
- `all_tasks_complete`: true ONLY if EVERY plan task has all steps checked (`- [x]`) — i.e. no unchecked `- [ ]` step remains anywhere. A `BLOCKED` or `DONE_WITH_CONCERNS` status does NOT make this true; only full step completion does. Never invent completion.
- `tasks_remaining`: your best integer estimate of plan tasks that still have unchecked steps (use the pass's reported remaining count).
- `notes`: one sentence on what remains — and **if the pass reported `BLOCKED` or concerns, name the blocker** (e.g. "task 3 blocked: <reason>; 2 tasks remaining") so it stays visible to the refinement loop and the PR. Otherwise "all tasks complete".

Base the completion decision on tasks/steps completion, NOT on whether the whole test suite is green (test-greenness is the ralph loop's job).
