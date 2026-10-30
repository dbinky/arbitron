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

A dependency is **UNRESOLVED** if a plan task consumes a type / field / collaborator /
data source that no task produces and no existing `file:symbol` provides — including any
design `UNRESOLVED DEPENDENCY` you could not resolve into a task, and any `// NOTE` that
papers over a missing producer. Emit `UNRESOLVED-DEPENDENCIES: 0` ONLY when the graph is
fully closed. **Do not hide a hole to make the count zero** — an unresolved dependency
caught here is cheap; one that reaches implementation livelocks the whole run.
