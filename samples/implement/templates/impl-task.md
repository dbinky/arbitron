# Mission: Implement the Next Plan Task (strict TDD)

You are implementing a feature **one plan task per pass**, working **unattended** in the git worktree at `./work`. The user is unavailable — never stop to ask.

## Inputs
- Plans, in task order (paths are relative to `./work`): {{resolve-inputs.inputs.plan_paths}}
- Each plan task uses `- [ ]` checkboxes per step. A task is "done" when all its steps are checked.

## First: fix what the tamper gate voided
If `{{workspace}}/tamper/must-fix.md` exists, the tamper gate voided the previous pass and undid some or all of its changes, including any boxes it ticked. Read it and address every item **before anything else this pass**: redo that work without deleting, renaming, skipping or loosening tests, and without editing test, mutation or CI config or the branch-point baseline. Do not edit or delete `must-fix.md` itself.

## Select the task (this pass only)
Go through the plan files above in the order given and pick the **first task with any unchecked `- [ ]` step** — that is THIS pass's task. If every task is fully checked, make no code changes and report `STATUS: DONE` with no remaining tasks.

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
Commit using the task's commit step (or `git -C ./work add -A && git -C ./work commit -m "feat({{slug}}): <task title>"`). Commit only; do not push, even if the task's commit step says to. The workflow pushes after the tamper gate has checked this pass. Do NOT start the next task — one task per pass.

Red tests *elsewhere* in the suite are tolerated at this stage; later refinement closes them. Focus on completing THIS task's own steps and its own tests correctly.

## Repairing a conflicting existing test
An existing test (one this pass did not write) may stop a step even though the step is right. For example, its setup omits something the spec, design or contracts now require, or it pins an old signature, count or value that a requirement changed. You may repair that test yourself instead of blocking, but only within these rules:
- **Grounds.** The repair is allowed only when a specific requirement in the spec, design or contracts (or this task's plan text) makes the test's current setup or expectation wrong. "It fails" is not a ground.
- **Never weaken.** The repaired test must still prove what it proved before, now against the required behaviour. Update a fixture, a constructor call, a count or an expected value to the required one. Never delete a test, add a skip, drop an assertion without a replacement that checks the required behaviour, or loosen a timeout, threshold or tolerance to make it pass.
- **A preexisting failure in a test this task does not touch is not a conflict.** Leave it alone; failures elsewhere are tolerated (see Commit).
- **Prove it.** Run the repaired test and the step's own tests; both must pass for the right reason.
- **Record it.** Under the step, add one line per repaired test: `> FIXTURE REPAIR (self-authorized): <TestName> in <file> — <what changed> — because <the requirement, citing the spec, design or contracts section>`. The tamper gate's judge reads this line when it rules on the change; a repair without one is voided.

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
