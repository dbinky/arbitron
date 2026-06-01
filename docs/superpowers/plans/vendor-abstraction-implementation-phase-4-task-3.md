# Phase 4, Task 3 (OPTIONAL): Advisory Interactive-in-Loop Lint

> **OPTIONAL.** This task is explicitly optional in design §2 and §7 (item 3:
> "Optional interactive-in-loop advisory lint") and design §6 ("if built"). It is an
> **advisory** warning, **never** a hard gate — cyclic graphs stay fully allowed. If
> descoped, the constraint is enforced only by documentation (Task 5). Build this
> only if the warning's value justifies the surface; otherwise skip it and note the
> skip in the PR.

## Goal

Add a **validation lint** that warns when an `interactive` node sits on a cycle
(i.e. it is reachable from itself via a back-edge). Rationale: the interactive
executor short-circuits on a prior-succeeded `(instance, node)` execution and would
return its first answer on every subsequent pass (`executor_interactive.go:65`–`68`),
so an interactive node in a loop body is almost always an authoring mistake (spec §7,
design §2). The lint is **advisory severity** — it surfaces a warning but does **not**
fail `workflow_validate`, honoring the design's "cyclic graphs allowed" decision.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-4.md` §2
(constraints) and §6 (Interactive-in-loop lint bullet); spec §7 (interactive must not
appear in a loop body).

## Dependencies

- **Phase 1** node kinds (`NodeTypeInteractive` already exists). No Phase 2/3
  dependency.
- Third item in design §7 "Sequencing"; independent of Tasks 1/2/4 (those do not rely
  on the lint existing). Ordered before Task 4 only so the lint, if built, can have a
  harness scenario; if descoped, Task 4 drops that scenario.

## Background (current state, grounded in real code)

- Validation is `domain/workflow/validation.go`: `Validate(wf *Workflow, checker
  FileChecker, ...) []ValidationError` returns a slice of `ValidationError{Code,
  Message, NodeID, EdgeIdx}`. **All** current validation errors are hard failures —
  there is currently **no severity field**. A pure-advisory result needs either a
  severity dimension or a separate return channel (see Design below).
- Reachability machinery already exists: `validateConnectivity`
  (`validation.go:196`–`241`) builds an adjacency list from edges and runs a **BFS**
  from the entry node, tracking a `visited` map. The cycle/back-edge detection here
  reuses exactly this adjacency+BFS pattern, generalized to start from each
  interactive node.
- `interactive` is untouched by the vendor-abstraction phases; it keeps its
  short-circuit semantics, which is *why* it must not loop.

## Design

A node "sits on a cycle" iff it is reachable from itself: starting a BFS/DFS from the
node's successors, the node itself is re-reached. Detect per interactive node:

1. Build the adjacency list from `wf.Edges` (same construction as
   `validateConnectivity`).
2. For each node `n` where `n.Type == NodeTypeInteractive`: BFS from `n`'s successors;
   if `n.ID` is re-reached, `n` is on a cycle → emit an advisory finding.

### Advisory severity (choose the lower-surface option)

Two acceptable shapes — pick the one matching the repo's existing validation
plumbing; **do not** make this a hard failure:

- **Preferred (additive, non-breaking):** add an optional `Severity string` field to
  `ValidationError` (`"error"` default when empty for back-compat; `"warning"` for
  this lint), and have the `workflow_validate` surface report warnings separately from
  errors **without** failing validation. Callers that treat any returned entry as
  fatal must be updated to gate on `Severity == "error"` (or empty). Audit call sites
  of `Validate(` (search the repo) and ensure warnings do not block a run.
- **Alternative:** return advisories on a separate channel — e.g. a dedicated
  `LintWorkflow(wf) []ValidationError` (all warnings) invoked alongside `Validate`,
  whose results are surfaced informationally and never gate execution. This avoids
  touching `ValidationError`/all call sites at the cost of a second entry point.

Either way: the lint **must not** appear in the error list that fails
`workflow_validate`. Cyclic graphs without interactive nodes produce **no** finding.

### Files to Create/Modify (domain only — strict hex)

- **Modify: `domain/workflow/validation.go`** — add `lintInteractiveInLoop(wf
  *Workflow) []ValidationError` (advisory `Code: "INTERACTIVE_IN_LOOP"`, severity
  warning, `NodeID` set to the offending interactive node). Reuse the
  `validateConnectivity` adjacency-build helper (extract a shared `buildAdjacency(wf)`
  if not already present). Wire it into the chosen surface (severity field or separate
  lint entry) so it is **non-gating**.
- All new code stays in `domain/workflow` (domain validation), imports stdlib only —
  consistent with the existing file. No adapter import.

## Testing (strict TDD — all five scenario categories, per design §6 lint bullet)

- **Happy path — interactive on a cycle is flagged.** A graph with an `interactive`
  node having a back-edge into a predecessor that returns to it yields exactly one
  `INTERACTIVE_IN_LOOP` advisory on that node.
- **Success — linear interactive not flagged.** An `interactive` node on a purely
  linear (acyclic) path yields **no** finding.
- **Failure — advisory does not gate.** A workflow that triggers the lint still
  **passes** `workflow_validate` (the run is allowed); assert the offending finding is
  present as a **warning** and is **absent** from the hard-error set that would fail
  validation. This is the load-bearing "advisory, not a hard gate" assertion.
- **Error — self-loop / multi-node cycle.** An interactive node with a direct
  self-edge, and one on a longer multi-node cycle, are both flagged (BFS re-reaches the
  origin). A cycle that does **not** include the interactive node (interactive sits
  off the cycle on a linear spur) is **not** flagged.
- **Edge case — no interactive nodes / no cycles.** A cyclic graph with no interactive
  node, and an acyclic graph with an interactive node, both produce zero advisories;
  multiple interactive nodes on the same cycle each produce their own finding.

Tests live in `domain/workflow` and use in-memory `Workflow` fixtures (no adapter,
no resolver needed for this lint).

## Acceptance Criteria (if built)

- An `interactive` node on a cycle produces an `INTERACTIVE_IN_LOOP` **advisory**
  (warning severity), detected via back-edge reachability reusing the
  `validateConnectivity` BFS pattern.
- The advisory **never** fails `workflow_validate`; cyclic graphs remain allowed
  (design §2). A linear interactive node and a cycle without an interactive node both
  produce no finding.
- All new code is in `domain/workflow` (domain validation), stdlib-only; existing
  validation and all `Validate(` callers continue to pass (warnings do not gate).
- `go test ./domain/workflow/...` passes.

## If descoped

Record the skip in the PR. The "no interactive in a loop body" constraint is then
enforced solely by the Task 5 documentation and the Task 4 harness (which simply does
not place an interactive node in the worked-example loop). No code is added.
