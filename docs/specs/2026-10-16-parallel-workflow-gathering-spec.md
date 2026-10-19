# Parallel Workflow Gathering — Product Specification

## Purpose

Enable workflow authors to run a fixed set of tasks concurrently, either as parallel branches or as multiple awaited child workflows, and gather their results before continuing.

Both forms offer consistent completion rules and optional gathering of file changes into the parent’s isolated working area. Authors can configure the entire feature through MCP. The workflow UI and queue’s live run view clearly display parallel execution, nested groups, gathers, and all active task states.

## Features

### Parallel groups

- Authors define a fixed set of parallel branches or child workflow tasks.
- Child tasks may use different workflows or reuse one workflow with different inputs.
- Authors specify task order and inputs.
- Groups may nest. Each nested group completes within its enclosing task.
- Each task starts with its own copy of the group’s starting variables and its own isolated working area.
- Tasks can progress concurrently, subject to existing execution limits.

### Completion rules

Each group offers two completion rules:

**All succeed**

- The group succeeds when every task succeeds.
- After a task exhausts its configured error handling, authors choose whether to wait for the remaining tasks or cancel unfinished tasks immediately.
- Either choice leads to the group’s failure path, with available results and failure details.

**First success**

- The first successful task wins.
- Individual failures do not stop other candidates from succeeding.
- The group fails only if every candidate finishes without success.
- Authors choose whether unfinished tasks are cancelled or allowed to finish independently after a winner is selected.

A task’s cancellation does not count as success. Group success and failure are available as distinct onward paths.

### Gathering results

- Gathering presents task outcomes and available results in author-defined task order, regardless of completion order.
- Results identify their originating task and, for first-success groups, the winner.
- When a group continues before every task finishes, its gathered outcome distinguishes completed, unfinished, and cancelled tasks.
- Branch variables are not automatically merged into parent variables. Later workflow steps explicitly reconcile results as needed.
- Each group continues its parent once its completion rule and any requested file gathering are satisfied.
- Later task completions cannot trigger another continuation or change the gathered outcome.

### Gathering file changes

Each group offers **results only** or **results plus file merging**.

- For all-succeed groups, file gathering combines all tasks’ changes after every task succeeds.
- Failed groups expose available results and failure details without automatically merging partial work.
- First-success groups gather only the winner’s changes.
- Gathering attempts automatic conflict resolution.
- Unresolved conflicts pause gathering and open an interactive resolution session, comparable to the brainstorm or dependency-resolution experience.
- Downstream work proceeds only after the conflicts are resolved and gathering succeeds.
- Tasks allowed to finish independently after a first-success decision cannot subsequently alter the parent’s working area.
- Nested groups gather changes into their immediate parent’s working area.

### MCP authoring

Authors can create, inspect, and revise the complete feature through MCP without hand-editing workflow definitions, including:

- Parallel branches and multi-child spawn groups.
- Nested groups.
- Child workflow selection and task-specific inputs.
- Task order.
- Completion rules and unfinished-task behavior.
- Optional file gathering.

Invalid group definitions and incompatible choices receive actionable authoring feedback.

### Workflow and run visibility

- The workflow UI clearly displays fan-out paths, gathers, nested groups, and child workflow relationships.
- The queue’s live run view reflects every concurrently active node across all fanned-out paths.
- Task states distinguish queued, running, waiting, completed, failed, and cancelled work.
- Each gather shows its completion rule, task progress, outstanding dependencies, winner when applicable, and file-gathering or interactive-resolution activity.
- Tasks continuing independently after a first-success decision remain visible and are distinguished from work blocking the parent.
- Completed runs retain task outcomes, gathered results, and merge-resolution outcomes for inspection.

### Existing behavior and cancellation

- Existing single-child awaiting, including passing child results to subsequent steps, remains available.
- Existing fire-and-forget spawning remains available.
- Cancelling a parent stops its unfinished parallel work, including nested tasks and child workflows.

## Outcomes

The feature is successful when:

- An author can configure both forms of parallel execution, including nesting and gathering, entirely through MCP.
- Independent tasks can make progress concurrently within existing execution limits.
- All-succeed groups follow the selected failure behavior and expose available results.
- First-success groups select a winner, apply the selected unfinished-task behavior, and continue only once.
- Gathered results remain in author-defined order without automatically merging variables.
- Optional file gathering brings only eligible changes into the parent’s working area and supports interactive conflict resolution.
- Operators can see all concurrent node states and identify exactly why a gather is waiting.
- Nested execution and parent cancellation behave consistently across both forms of parallel group.

## Non-goals

- Creating tasks dynamically from a runtime list.
- “At least N successes” completion rules.
- Automatically merging branch variables.
- Shared working areas between parallel tasks.
- Automatically merging partial work from failed groups.
- Applying later results or changes from non-winning tasks to an already gathered outcome.
- A visual workflow editor.
- Changes to unrelated workflow capabilities.
