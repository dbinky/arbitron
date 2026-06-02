# Phase 4, Task 1: Worked-Example Native Ralph Loop Workflow Folder

## Goal

Commit a runnable **worked-example workflow** — a 4-node cyclic-edge graph that
expresses the native ralph refinement loop on **existing** engine primitives, plus
its mission/assessment templates and a seed `RALPH.md`/`focus-areas`/`gaps` set for
the instance `{{workspace}}`. The same `workflow.json` must run under **both** the
Claude-subscription and open-weight configurations by repointing profile aliases
only — nodes reference aliases (`editor`, `assessor`), never a vendor (acceptance
#4). This task ships **data, not code**: no Go, no new engine primitive.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-4.md` §1
(the cyclic-edge idiom), §3 (worked-example deliverable), and the spec
`docs/specs/feature-pipeline-replacement-mvp.md` §7 (native ralph loop).

## Dependencies

- **Phase 1** — `agent_call` node kind (`NodeTypeAgentCall`), `llm_call` rebound to
  `ChatProvider`, `model_profile` alias on both kinds (the `model` string was
  removed in favor of `model_profile`). The aliases `editor`/`assessor` reference
  Phase-1 profiles.
- **Phase 2** — connections + profile resolution (so the open-weight alias binding
  can point `editor`/`assessor` at non-Claude connections). Needed only for the
  open-weight *config*, not for the graph itself.
- No engine change. This task is the first item in design §7 "Sequencing" and is a
  prerequisite for Task 2 (loop-mechanics tests reference this graph shape) and
  Task 4 (the acceptance harness runs this exact folder under both configs).

## Background (current state, grounded in real code)

- Example workflow folders live under a workflow directory loaded by
  `adapter/filesystem` (`FileChecker.FileExists(workflowName, relativePath)`,
  `FileReader.ReadFile(workflowName, relativePath)`). The committed test examples are
  laid out as `<workflow-name>/workflow.json` with sibling template files referenced
  by **relative path** — see `adapter/filesystem/testdata/all-node-types/workflow.json`
  (its `llm_call` node references `"templateFile": "prompt.md"`).
- The `workflow.json` schema (fields used here): top-level `name`, `description`,
  `author`, `inputs`, `nodes`, `edges`, optional `defaultErrorBehavior`. Each node
  has `id`, `type`, `label`, optional `entrypoint: true`, and a `config` block.
  Edges have `source`, `target`, and an optional `output` (output port).
- `transform` config: `operations: [{ variable, operation, value }]`; `increment`
  adds the numeric `value` to an existing numeric variable (`executor_transform.go`,
  `applyIncrement`). The variable must already exist and be numeric.
- `conditional` config: `conditions: [{ field, operator, value, output }]` +
  `defaultOutput`; the executor returns the output port of the **first** matching
  condition, else `defaultOutput` (`executor_conditional.go`). Operators include
  `eq`, `gte`, `gt`, etc. There is **no** boolean-OR operator; an OR of two
  conditions is expressed as **two condition entries routing to the same exit port**
  (first-match wins), which is the supported way to encode *(done? OR counter ≥ max)*.
- `{{variable}}` substitution is resolved in template files and in transform `set`
  values via `variable.Resolve`. `{{workspace}}` is the engine-seeded reserved
  variable naming the instance working directory.

## Files to Create (all data — no Go)

Create a new committed workflow folder. Place it where the project keeps runnable
example workflows; if none exists yet, create `examples/workflows/native-ralph-loop/`
and note the path in the README section (Task 5). Mirror the testdata layout
(`<name>/workflow.json` + sibling files).

### Create: `native-ralph-loop/workflow.json`

The 4-node cycle. Nodes reference aliases only; the graph is identical for both
configs. Shape (illustrative — author exact JSON to match the real schema above):

- Input: `max_iterations` (number, required) — the loop bound. Optionally a
  `mission` string input. The iteration counter variable (e.g. `iteration`) is
  seeded via a `transform`-`set` on first entry **or** declared as an input with a
  default of `0` (preferred: an input with `default: 0`, so the counter exists and
  is numeric before the first `increment`).
- Node `editor` — `type: agent_call`, `entrypoint: true`,
  `config: { "model_profile": "editor", "templateFile": "mission.md", ... }`. Edits
  the workspace toward the current focus areas (reads/rewrites `RALPH.md`,
  `focus-areas.md`, `gaps.md` in `{{workspace}}`).
- Node `assessor` — `type: llm_call`,
  `config: { "model_profile": "assessor", "templateFile": "assessment.md",
  "outputSchema": { ...{done, notes}... } }`. Tool-suppressed reasoning; emits
  structured `{ done: boolean, notes: string }`. The `done` field becomes a node
  output variable used by the conditional (e.g. `assessor.done`).
- Node `increment-counter` — `type: transform`,
  `config: { "operations": [ { "variable": "iteration", "operation": "increment",
  "value": 1 } ] }`.
- Node `loop-or-exit` — `type: conditional`, with **two** conditions both routing to
  the `exit` port (encoding the OR) and a default routing back into the loop:
  - `{ "field": "assessor.done", "operator": "eq", "value": true, "output": "exit" }`
  - `{ "field": "iteration", "operator": "gte", "value": "{{max_iterations}}", "output": "exit" }`
  - `"defaultOutput": "loop"`
- Edges (note the **back-edge** is an ordinary edge — the engine has no acyclicity
  check):
  - `editor → assessor`
  - `assessor → increment-counter`
  - `increment-counter → loop-or-exit`
  - `loop-or-exit → editor` with `"output": "loop"` (the **back-edge**)
  - `loop-or-exit → <exit/terminal node>` with `"output": "exit"` (a terminal node
    with no outgoing edges, e.g. a `transform`/`tool_call` summary node, so the walk
    ends cleanly; see note on connectivity below).

Connectivity note: `validateConnectivity` (BFS from the entrypoint,
`domain/workflow/validation.go`) requires every node reachable from the entry. The
exit target must be a real reachable node so the graph validates; the back-edge does
not create an unreachable node. Confirm the example passes `workflow_validate`
(asserted in Task 4's worked-example-validation scenario).

### Create: `native-ralph-loop/mission.md` (the `agent_call` mission template)

The instruction the `editor` agent runs each pass: read `RALPH.md`,
`focus-areas.md`, and `gaps.md` from the workspace; make the smallest correct edit
toward the top focus area; run/inspect the project's tests; update `gaps.md` and
`focus-areas.md` to reflect remaining work. Use `{{variable}}` substitution for the
mission/iteration context (e.g. `{{mission}}`, `{{iteration}}`). The agent operates
in `{{workspace}}` (the `agent_call` WorkDir is seeded from `{{workspace}}`).

### Create: `native-ralph-loop/assessment.md` (the `llm_call` assessment template)

The tool-suppressed reasoning prompt: given the latest test results and the current
`gaps.md`/`focus-areas.md` contents (passed via substitution or summarized by the
prior agent step), decide whether the focus areas are satisfied. Instruct a
**structured** result matching `outputSchema`: `{ "done": <bool>, "notes": <str> }`.
Emphasize: this node has **no tools** and must not claim to edit files (it is the
tool-suppressed chat path — spec §3).

### Create: seed loop documents in the workspace seed set

`RALPH.md`, `focus-areas.md`, `gaps.md` — ordinary files the agent reads and
rewrites each pass (spec §7). Provide them as a committed **seed set** under the
example folder (e.g. `native-ralph-loop/seed/RALPH.md`, `.../focus-areas.md`,
`.../gaps.md`) with a one-line note in the README (Task 5) on how the first pass
populates `{{workspace}}` (either the harness copies the seed set into the instance
workspace before triggering, or the first `agent_call` pass creates them). These are
**not** part of `workflow.json` — they are workspace content.

### Create: two profile-registry bindings (config snippets, not committed secrets)

Provide the two alias bindings exercised by Task 4, as documented config examples
(no API keys in the files):

- **Claude binding** — `editor` → a `claude-cli` **agent** profile (tools enabled,
  no connection / ambient login); `assessor` → a `claude-cli` **chat** profile
  (tool-suppressed, no connection).
- **Open-weight binding** — `editor` → an **opencode** agent profile on an
  open-weight model (connection-backed, key env-injected at launch); `assessor` → an
  Azure AI Foundry or OpenRouter **chat** profile (connection-backed).

Both bindings keep the same aliases; only the profile/connection targets differ.
Place these as example `profiles:` blocks in the README/docs (Task 5) and/or as
test fixtures the harness loads (Task 4) — **never** as committed keys (spec §6;
keys live only in the connection store, supplied via the management surface).

## Testing

This task ships **data**, so it has no unit tests of its own; its correctness is
proven by:

- **Task 2** loop-mechanics tests, which run this graph shape (or a minimal
  equivalent) over the real engine and assert counter/exit/back-edge behavior.
- **Task 4** worked-example-validation scenario: the committed `workflow.json`
  passes `workflow_validate` under both bindings (capability validation, connectivity,
  conditional-edge integrity), and the negative variant (`editor` bound to a
  chat-only profile) **fails** validation (acceptance #6).

Author-time check (manual, recorded in the PR): run `workflow_validate` against the
committed folder under both bindings and confirm zero errors; confirm the
conditional's two exit conditions + default loop edge match the declared output
ports (`validateConditionalEdges`).

## Acceptance Criteria

- A committed `native-ralph-loop/workflow.json` expresses the 4-node cycle
  (`agent_call editor` → `llm_call assessor` → `transform` increment → `conditional`
  *(done OR counter ≥ max → exit, else back-edge to `editor`)*) using **only**
  existing node kinds and a back-edge — **no new engine primitive**.
- Nodes reference aliases `editor`/`assessor` via `model_profile`; the graph is byte-
  identical across the Claude and open-weight configs (only profile/connection
  bindings differ) — satisfying the same-graph requirement of acceptance #4.
- `mission.md` and `assessment.md` templates exist; `assessment.md` emits structured
  `{done, notes}`; the seed `RALPH.md`/`focus-areas.md`/`gaps.md` set is committed
  and documented as `{{workspace}}` content.
- The example passes `workflow_validate` under both bindings; the negative
  chat-profile-on-`editor` variant is reserved for Task 4's #6 scenario.
- No Go code added; example workflows are data per strict hexagonal placement.
