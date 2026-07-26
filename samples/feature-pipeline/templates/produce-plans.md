# Mission: Produce Implementation Plans

You are writing **detailed, code-level implementation plans** for a skilled engineer who has zero context for this codebase and questionable test taste. Work **unattended** in the worktree at `./work`. Do not ask questions.

## Inputs
- Spec: `./work/{{spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`

## Wired-and-fed — the bar EVERY plan must meet
A component counts as planned ONLY if the plan makes it **wired-and-fed**:
constructed at the real composition root (not just in a test) AND fed real data
from a producer that exists (or is built by another task) — not `nil`, empty, or
hardcoded. "Build the component + isolated tests pass" is HALF a feature. Carry
the design's `UNRESOLVED DEPENDENCY` and "Deferred to plans" items in as real
work, not prose.

## Step 1 — Map the file structure FIRST (before writing any task)
Before defining tasks, map out which files each design phase will create or modify
and what each one is responsible for — this is where decomposition gets locked in.
- One clear responsibility per file; design units with clear boundaries and explicit
  interfaces. Prefer smaller, focused files over large ones that do too much.
- Files that change together live together — split by responsibility, not by layer.
- In this existing codebase, **follow established patterns**; don't unilaterally
  restructure. (If a file you must modify has grown unwieldy, a focused split is OK.)
- **Identify the composition root(s)** (where this codebase constructs/injects real
  collaborators — e.g. server wiring, DI setup) and **the producer for every input**
  each new component consumes. These are tasks, not afterthoughts (see Step 2a).
This file map drives the task breakdown below — each task should produce a
self-contained change that makes sense on its own.

## Step 2 — Output
For each design phase, write one or more **task plan files** at:
`./work/docs/plans/{{run_date}}-{{slug}}-implementation-phase-{N}-task-{M}.md`
(N = design phase number, M = task number within the phase).

Each task file starts with a header: a one-line **Goal**, a 2–3 line **Architecture**,
and a **Tech Stack** line. (Do NOT add an execution/sub-skill handoff — execution is
the pipeline's job.)

### Per-task structure
Lead each task with an explicit **Files** block, then the bite-sized steps:
```
### Task N: <component name>
**Files:**
- Create: `exact/path/to/file.go`
- Modify: `exact/path/to/existing.go:123-145`
- Test:   `exact/path/to/file_test.go`

- [ ] Step 1: Write the failing test   <full test code>
- [ ] Step 2: Run it, confirm it fails  <exact command + expected output>
- [ ] Step 3: Minimal implementation    <full code>
- [ ] Step 4: Run it, confirm it passes <exact command + expected output>
- [ ] Step 5: Commit                    <exact git command>
```

## Step 2a — Wiring & data-source tasks are MANDATORY (not optional prose)
For **every** new component, collaborator, or field you plan, you MUST also plan:
1. **A composition-root wiring task** — the exact edit to the real construction
   site (server/DI wiring) that injects it into the live path, with a test that
   exercises it **through** that site (not by constructing it directly in the test).
2. **A data-source task** — the producer that populates each input it consumes,
   with the exact file:symbol that writes the value in production.

Hard rules:
- **An unfilled dependency is a TASK or an ESCALATION — NEVER a `// NOTE`.** Writing
  `// NOTE: X is not available, only Y runs` in code is a PLAN FAILURE. If a design
  left an `UNRESOLVED DEPENDENCY`, either plan the task that resolves it, or emit a
  top-level `ESCALATION: <what's missing>` in the plan output — do not encode the
  hole as an accepted no-op.
- A component task may not be the *terminal* task for that component: it must be
  followed (in some phase) by its wiring task and its data-source task. The
  composition-root file (e.g. the server wiring) must appear in at least one task.

## Step 2b — Tests must exercise the REAL wiring (anti-green-theater)
The point of the tests is to fail when the feature is inert. So:
- **At least one test per component must fail if its production input is `nil`/empty
  or its wiring is absent** (a provenance probe — drive it through the composition
  root, assert the real effect at the SINK).
- **Fakes must honor load-bearing arguments.** A fake whose method ignores the
  argument that decides behavior (e.g. `Similarity(_ , _ PoseBucket)`) makes the
  test unfailable — forbidden.
- **"Integration" tests must assert the SINK, not an intermediate hop.** "the plan
  reached the proposer" is not integration; "a look modifier reached the rendered
  prompt" is.

## Rules (follow exactly)
- **Bite-sized steps (2–5 min each)**, using checkbox syntax `- [ ]`. The canonical
  TDD cycle per unit of behavior: write the failing test → run it, confirm it fails →
  minimal implementation → run it, confirm it passes → commit.
- **Strict TDD:** include tests for happy-path, success, failure, error, and edge-case scenarios.
- **Exact file paths** always. **Complete code** in every code step. **Exact commands** with expected output.
- DRY, YAGNI, frequent commits.

## No placeholders (these are PLAN FAILURES — never write them)
- "TBD" / "TODO" / "implement later" / "fill in details".
- "Add appropriate error handling" / "add validation" / "handle edge cases" — show the actual code instead.
- "Write tests for the above" without the actual test code.
- **"Similar to Task N" — REPEAT the code.** The impl loop does one task per pass and may read tasks out of order; a task must stand alone.
- Steps that describe *what* to do without showing *how* (code steps require code blocks).
- **References to types, functions, or methods not defined in any task.**
- **`// NOTE: <X is unavailable>` standing in for a missing producer/wiring** — make it a task or an ESCALATION.

## Step 3 — Self-review (after writing all task files; fix inline, no need to re-review)
1. **Spec coverage:** skim each spec/design requirement — can you point to a task that implements it? Add a task for any gap.
2. **Wired-and-fed coverage:** for each new component, point to its composition-root wiring task AND its data-source task. If either is missing, add it (or emit an ESCALATION). Confirm no `// NOTE` encodes a missing dependency.
3. **Anti-green-theater:** confirm each component has a test that would fail if its production input were nil/empty; confirm no fake ignores a load-bearing arg; confirm "integration" tests assert the sink.
4. **Placeholder scan:** search your plans for any of the "No placeholders" red flags above; fix them.
5. **Type/name consistency:** do the types, function signatures, and property names used in later tasks match what earlier tasks define? (e.g. `clearLayers()` in Task 3 vs `clearFullLayers()` in Task 7 is a bug.) Reconcile them.

## Finish
`git -C ./work add docs/plans/ && git -C ./work commit -m "docs: write {{slug}} implementation plans" && git -C ./work push --quiet -u origin HEAD`
Report the number of task files written, and any `ESCALATION:` lines for unresolved dependencies.
