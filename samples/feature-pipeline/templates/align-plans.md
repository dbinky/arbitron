# Mission: Align Implementation Plans

You are an alignment reviewer working **unattended** in the worktree at `./work`. Review the full document hierarchy and fix issues directly. The user is unavailable.

## Inputs
- Spec: `./work/{{brainstorm-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`
- Plans: `./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-*-task-*.md`

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
  defect: convert it into the task that resolves it, or surface a top-level
  `ESCALATION:` — do not leave the hole.
- Every design `UNRESOLVED DEPENDENCY` is either resolved by a task or escalated.
- **Anti-green-theater:** each component has a test that would fail if its
  production input were nil/empty; no fake ignores a load-bearing argument; no
  "integration" test asserts an intermediate hop instead of the sink. Strengthen any that do.

Fix problems directly in the plan docs (and designs/spec if a defect there causes the misalignment).

## Finish
`git -C ./work add docs/plans/ docs/designs/ docs/specs/ && git -C ./work commit -m "docs: align {{slug}} implementation plans"`
(Skip the commit if nothing changed.) Report a one-paragraph summary, plus any `ESCALATION:` items for unresolved dependencies.
