# Assess: Draft-Implementation Progress

You are assessing implementation progress. You have no tools — reason only from the information below and emit a structured verdict.

## Context
- The plans are these files, relative to `./work`: {{resolve-inputs.inputs.plan_paths}}. They use `- [ ]` / `- [x]` per step.
- The most recent implementation pass reported the following (look for its trailing `STATUS:` block — `DONE` / `DONE_WITH_CONCERNS` / `BLOCKED`, the remaining-task count, and any concerns/blockers):

{{impl-task.response}}

## Decide
- `all_tasks_complete`: true ONLY if EVERY plan task has all steps checked (`- [x]`) — i.e. no unchecked `- [ ]` step remains anywhere. A `BLOCKED` or `DONE_WITH_CONCERNS` status does NOT make this true; only full step completion does. Never invent completion.
- `tasks_remaining`: your best integer estimate of plan tasks that still have unchecked steps (use the pass's reported remaining count).
- `all_remaining_blocked`: true ONLY if there ARE unchecked steps remaining AND the pass reported `BLOCKED` with **every** remaining task blocked on a producer / dependency that does not exist and is outside its own scope — i.e. no remaining task has any independent, unblocked step left to make progress on. false if all tasks are complete, or if ANY remaining task still has actionable, unblocked steps (a normal in-progress loop). This is the signal that the plan shipped an unfillable dependency; when true the loop stops and escalates rather than spinning to the iteration cap.
- `notes`: one sentence on what remains — and **if the pass reported `BLOCKED` or concerns, name the blocker** (e.g. "task 3 blocked: <reason>; 2 tasks remaining") so it stays visible to the escalation alert, the refinement loop, and the PR. Otherwise "all tasks complete".

Base the completion decision on tasks/steps completion, NOT on whether the whole test suite is green (test-greenness is the ralph loop's job). Distinguish carefully between "still working" (some remaining task has unblocked steps → `all_remaining_blocked` false, keep looping) and "genuinely stuck" (all remaining tasks blocked on non-existent producers → `all_remaining_blocked` true, escalate).

## OUTPUT CONTRACT — read this last, obey it exactly

Emit ONE JSON object and nothing else. No prose, no heading, no code fence, no commentary before or after.

The object MUST contain ALL FOUR keys below, every single time, with no exceptions:

1. `all_tasks_complete` (boolean)
2. `tasks_remaining` (number)
3. `all_remaining_blocked` (boolean)
4. `notes` (string)

**`all_remaining_blocked` is the key that is most often forgotten. Do not forget it.** It is REQUIRED on every response. In the ordinary case — the loop is still chewing through tasks that have actionable steps — its value is simply `false`. `false` is a value: emit the key with `false`, never omit the key. Omitting it fails the run outright; there is no second chance and no opportunity to correct it.

Before you answer, count the keys in your JSON object. If there are not exactly four, add the missing one.

The normal, overwhelmingly common in-progress response looks exactly like this:

{"all_tasks_complete": false, "tasks_remaining": 19, "all_remaining_blocked": false, "notes": "Phase 1 Task 5 complete (composition-root lane binding); 19 tasks remain with actionable steps."}

When every plan task is fully checked:

{"all_tasks_complete": true, "tasks_remaining": 0, "all_remaining_blocked": false, "notes": "all tasks complete"}

When the loop is genuinely stuck on an unfillable dependency:

{"all_tasks_complete": false, "tasks_remaining": 3, "all_remaining_blocked": true, "notes": "task 3 blocked: no producer exists for the scoped SAS credential and it is outside this task's scope; 3 tasks remaining."}
