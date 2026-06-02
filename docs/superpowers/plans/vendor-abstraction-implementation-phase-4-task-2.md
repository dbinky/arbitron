# Phase 4, Task 2: Loop-Mechanics Tests Over Existing Engine Primitives

## Goal

Prove the native ralph loop runs on the engine's **existing** cyclic-edge behavior —
**no new engine code**. Add verification/confirmation tests that a cyclic graph: (a)
increments a counter across passes, (b) exits on `done`, (c) exits on
`counter ≥ max`, (d) revisits the agent node along the back-edge, and (e) persists
the counter in instance variables where it is visible in the execution trace. These
are *characterization* tests of behavior the design declares already-present
(design §1 "Why no new primitive", spec §7). Write the tests **first**; only if a
test exposes a real gap (a missing capability the loop needs) is engine code touched
— and that would be a flagged deviation, not an expected outcome.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-4.md` §1,
§2, §6 (Loop mechanics bullet); spec §7.

## Dependencies

- **Task 1** — the worked-example graph shape (this task may use a minimal in-test
  graph that mirrors it: `agent_call`/`llm_call` replaced by **fakes** so the test is
  hermetic, plus the real `transform` and `conditional` executors).
- **Phase 1** — `NodeTypeAgentCall` and the rebound `llm_call`; the fake providers
  stand in for `ChatProvider`/`AgentProvider`.
- Second item in design §7 "Sequencing". No dependency on Phase 2/3 (uses fakes, not
  real vendors).

## Background (current state, grounded in real code — cite when writing)

The graph walker already supports cycles. Confirm against `domain/engine/engine.go`:

- The walk is `for currentNodeID != "" { ... }` (`engine.go:308`), advancing via
  `findNextNode(currentNodeID, outputPort, adj)` (`engine.go:735`–`761`). It follows
  edges until none remain. **There is no acyclicity check and no unique
  `(instance, node)` constraint** — a back-edge is an ordinary edge in the adjacency
  map built by `parseWorkflowGraph` (`engine.go:710`–`716`).
- `transform` **re-executes on revisit**: `TransformExecutor.Execute`
  (`executor_transform.go:22`) is stateless and applies its operations against the
  live `*variable.VariableStore` every time the node is reached; `applyIncrement`
  (`executor_transform.go:68`) reads the current numeric value and writes
  `current + delta`. Re-entry therefore increments again.
- `conditional` **re-evaluates on revisit**: `ConditionalExecutor.Execute`
  (`executor_conditional.go:23`) reads the live store each pass and returns the first
  matching condition's output port, else `defaultOutput`. The OR of *(done? OR
  counter ≥ max)* is two conditions routing to the `exit` port (first-match wins).
- Counter persistence + trace visibility: after each node, `handleNodeSuccess`
  (`engine.go:486`) calls `vars.SetNodeOutput` and `e.instances.UpdateVariables(ctx,
  instanceID, vars.Snapshot())` (`engine.go:521`), so the counter lives in instance
  `Variables`. Per-node `VariableSnapshot` records are written **before** each
  execution (`engine.go:344`–`352`) capturing `vars.Snapshot()` keyed by node — this
  is the trace surface where the counter's per-pass progression is visible.
- `interactive` short-circuit (the reason interactive is banned in loops — relevant
  to Task 3, asserted-against here only as a negative-control note): on a prior
  succeeded `(instance, node)` execution it returns the stored output instead of
  re-running (`executor_interactive.go:65`–`68`). Loop bodies must avoid it.

## Files to Create/Modify

### Create: `domain/engine/loop_mechanics_test.go` (or extend the existing engine test suite)

A focused, hermetic test file driving the **real** `Engine.run` walk (or the
existing engine test harness used elsewhere in `domain/engine`) over a cyclic graph
built from: real `TransformExecutor` + real `ConditionalExecutor`, and **fake**
`agent_call`/`llm_call` executors whose output is scripted per pass (so a test can
make `assessor.done` flip to `true` on the Nth pass). Use the repositories/eventBus
fakes already used by the engine tests. The counter variable is seeded numeric
(input default `0`) so `increment` succeeds.

If the engine package already has a test scaffold (in-memory repos, fake executors,
a `runToCompletion` helper), reuse it rather than building a new one; match the
established engine-test style.

## Testing (strict TDD — all five scenario categories, per design §6 Loop mechanics)

These are confirmation tests of existing behavior; each maps to a design §6 bullet.

- **Happy path — multi-pass then exit on `done`.** Script the fake `assessor` to
  return `{done:false}` for two passes then `{done:true}`. Assert: the `editor` node
  executed 3 times, the loop exits via the `exit` port on pass 3, the instance
  completes, and `iteration == 3` in the final instance `Variables`.
- **Success — exit on `counter ≥ max`.** Script `assessor` to always return
  `{done:false}`; set `max_iterations = 4`. Assert the loop exits via the
  `counter ≥ max` condition (not `done`) after exactly 4 passes, `iteration == 4`,
  and the instance completes (does **not** run forever within the test bound).
- **Failure — counter not seeded / non-numeric.** Remove the `iteration` seed (or set
  it non-numeric) so `applyIncrement` errors (`"variable %q not found"` /
  `"non-numeric"`, `executor_transform.go:70`,`76`). Assert the transform node fails
  and the instance halts via the default `halt` error behavior (the loop does not
  silently spin) — documenting that a malformed counter surfaces as a node error, not
  an infinite loop.
- **Error — back-edge revisits the agent node (re-execution proof).** Assert that
  across passes the `editor` (agent) node is entered more than once and a **new**
  `NodeExecution` record is created per pass (no unique `(instance,node)` constraint),
  and a per-pass `VariableSnapshot` is written for each revisit — i.e. the back-edge
  genuinely re-enters the node rather than being treated as already-done. (Contrast:
  an `interactive` node would short-circuit; this scenario proves `agent_call`/
  `transform`/`conditional` do not.)
- **Edge case — counter persisted in instance variables AND visible in trace
  (acceptance #5).** After the run, assert `instance.Variables["iteration"]` equals
  the final count, and that the ordered `VariableSnapshot` trace shows the counter
  progressing `0 → 1 → 2 → …` across the recorded passes (read via the snapshot
  repository keyed by node/time). This is the explicit acceptance-#5 assertion at the
  engine level; Task 4 re-asserts it end-to-end.

Additional documented-risk note (no auto-kill): include a test that a no-exit loop
(`conditional` whose `defaultOutput` always loops and conditions never fire) keeps
producing node executions, bounded in-test by a max-pass guard in the **fake**
executor (not the engine) — confirming the engine adds **no** runaway guard
(accepted MVP risk, spec §9 / design §2). The test must self-terminate via the fake,
asserting the engine itself did not stop the loop.

## Constraints

- **No new engine primitive.** If every scenario passes against the existing engine,
  add **zero** engine code — the deliverable is the test file. Any required engine
  change is a discovered gap: stop, surface it, and treat it as a separate flagged
  change (it would contradict design §1).
- Strict hexagonal: tests live in `domain/engine`; providers are fakes; no adapter
  imports.

## Acceptance Criteria

- A loop-mechanics test suite drives the real cyclic walk and passes against the
  **existing** engine with no new engine code.
- Counter increments across passes; exits on `done`; exits on `counter ≥ max`;
  back-edge re-executes the agent node (new `NodeExecution` per pass); counter
  persisted in instance `Variables` and visible in the `VariableSnapshot` trace
  (acceptance #5 at engine level).
- A no-exit loop is shown to run unbounded by the engine (self-terminated by the
  fake), confirming no runaway guard.
- `go test ./domain/engine/...` passes; existing engine tests still pass.
