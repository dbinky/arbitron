# Mission: Restore Checkbox TDD Steps to Stepless Plans

You are repairing implementation plans **unattended** in the worktree at `./work`.
Do not ask questions. **Never launch background work** (no background subagents,
no `run_in_background`): once your turn ends nothing collects their results.

## Why you are here
A script just read this run's committed plans,
`./work/{{plan_dir}}/{{run_date}}-phase-*-task-*.md`, and found at least one plan
with **no checkbox steps** (no `- [ ]` / `- [x]` line outside a code fence), or a
plan file that is not committed. The implementation loop finds work ONLY by
unticked `- [ ]` boxes: a stepless plan is skipped as if finished, and every task
that consumes what it builds then blocks (remote-runners, 2026-11-23: alignment
rewrote full TDD plans into prose and 47 tasks blocked). If the plans are still
stepless after you finish, the stage stops.

## Find the broken plans
For each plan file above, count checkbox lines outside fenced code blocks. Any
plan with none is stepless. Also run `git -C ./work status --short {{plan_dir}}`:
an uncommitted plan file must be committed.

## Restore the steps — do not invent a different plan
1. Read the plan's history: `git -C ./work log --oneline -- <plan>` and
   `git -C ./work show <commit>:<plan>`. The version written by produce-plans
   (`docs: write ... implementation plans`) has the full TDD steps; a later
   alignment commit usually replaced them with prose.
2. Rebuild the plan so it keeps **every alignment fix** (renamed types, added
   wiring or data-source tasks, corrected contracts, resolved decisions) AND has
   full checkbox TDD steps for every task, in the structure its sibling plans use:
   ```
   - [ ] Step 1: Write the failing test   <full test code>
   - [ ] Step 2: Run it, confirm it fails  <exact command + expected output>
   - [ ] Step 3: Minimal implementation    <full code>
   - [ ] Step 4: Run it, confirm it passes <exact command + expected output>
   - [ ] Step 5: Commit                    <exact git command>
   ```
   Where alignment changed a task, rewrite its steps to match the aligned
   contract. A task that exists only as prose (no earlier stepped version) gets
   its steps written from scratch, with complete code and exact commands.
3. Keep work no implementation agent can do (operator observation, manual
   checks, live vendor evidence, captured production fixtures) OUT of the
   checkbox steps: leave it under an "Operator follow-up (not for
   implementation)" heading as plain bullets, or flag it in your report.
4. Do not touch plans that already have their steps, or anything outside
   `./work/{{plan_dir}}/`.

## Commit
`git -C ./work add {{plan_dir}}/ && git -C ./work commit -m "docs: restore {{slug}} plan checkbox steps"`
Commit only — never push; the workflow pushes from outside the sandbox.
Report which plans you restored and how many checkbox steps each now has.
