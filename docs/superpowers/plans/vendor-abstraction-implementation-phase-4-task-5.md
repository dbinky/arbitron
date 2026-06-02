# Phase 4, Task 5: Documentation — Native Ralph Loop + Migration Note

## Goal

Document the native ralph loop idiom and its constraints, and record the migration
note that the external `ralph-o-matic` server and the standalone feature-pipeline
skill are superseded. This is the final deliverable of the phase (design §7 item 5,
§5 Documentation Deliverables). **Markdown only — no code.**

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-4.md` §5
(Documentation Deliverables) and §2 (Constraints); spec §7 (native ralph loop), §9
(accepted no-runaway-guard risk).

## Dependencies

- **Task 1** — the worked example to point at as the canonical illustration.
- Conceptually depends on Tasks 2–4 having confirmed the behavior the docs describe,
  so the documentation does not over-claim. Ordered last alongside completion.

## Background (current state)

- The repo has a top-level `README.md`. The design calls for a "Native Ralph Loop"
  section in "the project docs/README." Add the section to `README.md` (or a dedicated
  `docs/` page linked from `README.md` if the README is kept terse — match the repo's
  existing docs convention).
- The external orchestration this supersedes is referenced throughout the project's
  skills (e.g. `ralph-o-matic`, the standalone feature-pipeline skill). The migration
  note documents the supersession; it does **not** delete those skills (that is out of
  scope here).

## Files to Create/Modify (docs only)

### Modify: `README.md` (add a "Native Ralph Loop" section) — or a linked `docs/` page

Write a section covering:

1. **The idiom.** The cyclic-edge pattern over existing node kinds — no new engine
   primitive:
   - `agent_call` (edit toward focus areas) → `llm_call` (assess: run/inspect tests,
     remaining gaps; emits structured `{done, notes}`) → `transform` (increment an
     iteration counter held in instance variables) → `conditional` *(done? OR
     counter ≥ max)* routing back along a **back-edge** to the `agent_call`, or out to
     the exit.
   - The counter is a plain instance variable; it persists across passes and is
     visible in the execution trace.
   - `RALPH.md`, `focus-areas`, and `gaps` are ordinary files in the instance
     `{{workspace}}` that the agent reads and rewrites each pass. No external server.
   - Point at the Task 1 worked example (`native-ralph-loop/`) as the canonical,
     runnable illustration, including how the seed `RALPH.md`/`focus-areas`/`gaps`
     set is placed in `{{workspace}}` for the first pass.
   - Note the same graph runs under both the Claude and open-weight configs by
     repointing the `editor`/`assessor` profile aliases only (acceptance #4).

2. **Constraints (call out clearly):**
   - **No `interactive` node in a loop body.** The interactive executor short-circuits
     on a prior-succeeded execution and would replay its first answer every pass; loop
     bodies use only `agent_call` / `llm_call` / `transform` / `conditional` /
     `tool_call`. (If Task 3 shipped, mention the advisory `INTERACTIVE_IN_LOOP`
     lint and that it is advisory-only — cyclic graphs remain allowed.)
   - **No engine runaway guard (accepted MVP risk).** Termination depends solely on
     the author's `conditional`. A malformed loop can run indefinitely; this is a
     documented, accepted MVP risk. Note the planned future backstop (an engine-level
     cap on max node-executions and/or wall-clock per instance) as out of scope
     (spec §9).

### Modify: migration note (in `README.md` and/or the relevant skills' docs)

Record that:
- The external `ralph-o-matic` server is **superseded** by the in-engine native loop:
  Arbitron itself executes the ralph refinement cycle; no external server dependency.
- The **standalone feature-pipeline skill** is superseded by the native loop plus the
  (separately-specced) native feature-pipeline workflow — the latter is the named
  acceptance test for this layer and is authored in its own spec, not here.
- Be precise about scope: this phase delivers the **enabling loop idiom + worked
  example + docs**, not the full feature-pipeline workflow content (spec §1, §9
  non-goals).

## Testing

Documentation only — no automated tests. Verification:
- Cross-check every claim against the design (§1, §2, §5) and spec (§7, §9) so the
  docs do not over-claim (e.g. do **not** state there is a runaway guard).
- Confirm the worked-example path and node/alias names referenced in the docs match
  the actual Task 1 committed folder.
- Confirm internal links resolve (README → docs page / worked-example folder).

## Acceptance Criteria

- A "Native Ralph Loop" section exists in the project docs/README describing the
  cyclic-edge idiom, the counter-in-instance-variables + trace visibility, the
  `{{workspace}}` `RALPH.md`/`focus-areas`/`gaps` files, and the same-graph/both-config
  property — pointing at the Task 1 worked example.
- The two constraints are documented clearly: **no interactive in a loop body** and
  **no runaway guard (accepted MVP risk)**, with the planned future backstop noted as
  out of scope.
- A migration note records that the external `ralph-o-matic` server and the standalone
  feature-pipeline skill are superseded by the native loop + the separately-specced
  native feature-pipeline workflow.
- No code changes; documentation matches the as-built worked example and the design.
