# Mission: Align Implementation Plans

You are an alignment reviewer working **unattended** in the worktree at `./work`. Review the full document hierarchy and fix issues directly. The user is unavailable.

## Inputs
- Spec: `./work/{{resolve-inputs.inputs.spec_path}}`
- Designs (paths relative to `./work`, as a JSON array): `{{resolve-inputs.inputs.design_paths}}`
- Plans: `./work/{{plan_dir}}/{{run_date}}-phase-*-task-*.md`

## Review for
- **Spirit of the spec, not just surface conformance** — do the plans, taken together, actually implement what the spec INTENDS (its purpose/outcomes) and the design decisions, not merely restate them? Flag plans that are internally consistent but have drifted from the spec's intent.
- Spec → design alignment (requirements coverage, terminology)
- Design → plan alignment (architectural decisions reflected in tasks)
- Plan → plan consistency: shared types, function signatures, API contracts match across tasks
- Dependency ordering: a task that consumes a type defines/uses it consistently with the task that produces it
- No duplicate work; no gaps (every design requirement has a plan task)
- Strict-TDD coverage present in each task (happy/success/failure/error/edge)

## Wired-and-fed — verify the plans don't ship inert code
This is the highest-value alignment check. For **every** new component/collaborator/field:
- **It has a composition-root wiring task** (constructed/injected at the real
  server/DI site, with a test that drives it through that site) — add the task if missing.
- **It has a data-source task** naming the producer that populates each input in
  production — add it if missing.
- **No `// NOTE` encodes a missing producer/wiring as an accepted no-op.** If a plan
  task contains `// NOTE: X not available, only Y runs` (or similar), that is a
  defect: convert it into the task that resolves it, or surface it as unresolved
  (below) — do not leave the hole.
- Every design `UNRESOLVED DEPENDENCY` is either resolved by a task or surfaced as
  unresolved (below).
- **Anti-green-theater:** each component has a test that would fail if its
  production input were nil/empty; no fake ignores a load-bearing argument; no
  "integration" test asserts an intermediate hop instead of the sink. Strengthen any that do.

## Work no implementation agent can do — surface it, never pass it through
The implementation loop is unattended agents in a sandbox. They cannot do, and
will stall for hours on (lunaroute, 2026-11), any task step that needs:
- **operator observation or sign-off**, a manual check, or a person watching a
  real system;
- **live vendor evidence**, a live acceptance run against a real provider, or a
  **fixture captured from production / the real world** that is not already in
  the repo;
- **credentials, accounts or consoles** an agent cannot have.

Also check every task that changes existing behaviour against the **tests
already in the repo**: grep for the tests that assert the current behaviour.
If a plan requirement **contradicts a retained test** and the plan does not
explicitly authorize changing or removing that test (naming it), the
implementer can satisfy only one of them and will block.

Do not silently delete or rewrite these into something easier. Report each one
in the escalation block below, tagged `HUMAN-ONLY` or `TEST-CONFLICT`, so a
person decides: drop it, re-scope it into something an agent can verify (e.g. a
recorded fixture committed to the repo), move it to a non-checkbox
"Operator follow-up (not for implementation)" section, or authorize the test change.

## Keep the checkbox TDD steps — never replace them with prose
The implementation loop finds work ONLY by unticked `- [ ]` boxes: a plan
without them is skipped as finished, and every task that consumes what it
builds then blocks (remote-runners, 2026-11-23: alignment rewrote full TDD plans
into prose and 47 tasks blocked). So:
- Edit plans **in place**, keeping every task's `- [ ]` steps. Never collapse
  step bodies into a summary or prose description, however long the plan is.
- Any task or plan you add gets full checkbox TDD steps in the same structure
  as its siblings (failing test, run it, implement, run it, commit).
- A script checks every plan for checkbox steps right after you finish; a plan
  without them stops the stage.

Fix problems directly in the plan docs (and designs/spec if a defect there causes the misalignment).

## Commit
`git -C ./work add docs/plans/ docs/designs/ docs/specs/ && git -C ./work commit -m "docs: align {{slug}} implementation plans"`
(Skip the commit if nothing changed.) Commit only — never push; the workflow
pushes from outside the sandbox.

## Finish — REQUIRED machine-readable escalation block
Report a one-paragraph summary. Then, as the **LAST thing** in your output, emit a
machine-readable dependency-provenance block that the planning gate parses — **ALWAYS**,
even when clean:

    UNRESOLVED-DEPENDENCIES: <count>

If the count is greater than 0, follow it with one line per unresolved dependency:

    - <doc path> — <consumer task/component> needs <missing producer file:symbol or field>: <the design decision a human must make>
    - HUMAN-ONLY: <doc path> — <task/step> needs <operator observation | live vendor evidence | captured production fixture | credentials>: <the decision a human must make>
    - TEST-CONFLICT: <doc path> — <task> requires <behaviour>, but retained test <file:TestName> asserts <other behaviour>: <which wins?>

A dependency is **UNRESOLVED** if a plan task consumes a type / field / collaborator /
data source that no task produces and no existing `file:symbol` provides — including any
design `UNRESOLVED DEPENDENCY` you could not resolve into a task, and any `// NOTE` that
papers over a missing producer. Every `HUMAN-ONLY` and `TEST-CONFLICT` item counts too.
Emit `UNRESOLVED-DEPENDENCIES: 0` ONLY when the graph is
fully closed. **Do not hide a hole to make the count zero** — an unresolved dependency
caught here is cheap; one that reaches implementation livelocks the whole run.
