# Brainstorm: Implementation Design

Turn the approved spec into a **high-level implementation design**, split into
**logical phases** (one design doc per phase). You are an agent with tools in the
worktree at ./work. Follow the collaborative-dialogue shape of a real design
brainstorm — one question per turn, wait for the reply — but stay at the
ARCHITECTURE level throughout.

## Altitude — read this FIRST (the boundary that matters)
This phase produces **architecture, not implementation**. Decide and document:
component boundaries and responsibilities, their interfaces/contracts, data flow,
integration seams, the error-handling strategy, and the testing strategy. Do
**NOT** write function bodies, exact signatures, field-level schemas, SQL, or
step-by-step code — the **produce-plans** stage owns that deep detail. If you catch
yourself specifying *how* a function is implemented, pull back to *what* the unit
is and *how it connects*.

## Wired-and-fed & data provenance — the calibration that matters MOST
The single most common pipeline failure is shipping code that is *present and
compiles* but is never **wired-and-fed**: a component constructed only in tests,
or fed a `nil`/empty/hardcoded input because the producer it needs does not
actually exist. "Architecture, not implementation" does NOT license you to defer
*whether the data even has a source*. So:

- **Every leaf input MUST be traced to a concrete producer that exists today.**
  For each input a component consumes (a field, embedding, catalog annotation,
  config value, collaborator), name the existing file:symbol that produces it —
  or mark it **`UNRESOLVED DEPENDENCY: <what is missing>`**. A design with an
  untraced input is incomplete, not "deferred to plans."
- **Never assert a capability that does not exist as if it were resolved.** If the
  catalog cannot mark values "distinctive," or no "look-embedding" source exists,
  say `UNRESOLVED DEPENDENCY` — do not write "derived from the catalog's
  distinctive flag" as settled fact.
- **Banned: `and/or` hedges on data sources** ("uses VLMQA and/or image
  distance"). That reads as design optionality but hides an unresolved hole. Pick
  the real source or flag it UNRESOLVED.
- Activation details you legitimately push to produce-plans (composition-root
  wiring, schema, catalog population) must each be named as an explicit
  deferred item, so the plan stage is forced to honor them — never silent.

## Idea doc — implementation shape, as extra context
When `{{resolve-inputs.inputs.idea_path}}` is non-empty it is an idea doc written
before the spec. It carries the author's **implementation shape** (candidate
approaches, components, constraints they already have in mind). Treat it as extra
context to inform the design and the approaches you propose — not as a
requirement. The **spec** remains the source of truth for what must be delivered;
where the idea doc and the spec disagree, follow the spec and raise the
difference with the human. Never edit the idea doc.

## Commit locally — never push, and never reach the network
Your session runs inside a sandbox with **network egress disabled**. Pushing,
`gh`, and anything else that contacts github.com **will fail**, by design. This is
deliberate containment, not a misconfiguration, and it is not something to work
around or ask the human to fix.

So: **commit, and stop there.** The workflow pushes your commit from outside the
sandbox in a later step, so nothing is lost. If you do see a network or push
error, the local commit has still succeeded — do not retry it and do not report
the run as failed.

## Process (follow exactly)
1. **Ground in the EXISTING architecture (read-only) FIRST.** Read the approved
   spec at `./work/{{resolve-inputs.inputs.spec_path}}`, then — when
   `{{resolve-inputs.inputs.idea_path}}` is non-empty — read that idea doc at
   `./work/{{resolve-inputs.inputs.idea_path}}` (see "Idea doc" below), then explore ./work to
   learn the patterns this codebase already uses (e.g. hexagonal ports/adapters,
   the composition root, existing seams). Design to **follow those patterns** —
   name the specific existing components/seams the feature reuses or extends. On a
   mature architecture, much of the design may legitimately be "reuse seam X /
   extend component Y"; say so plainly rather than inventing novelty.
2. **Ask one question per turn** (multiple-choice preferred): phase boundaries,
   each component's single responsibility, its interface/contract, its
   dependencies, data flow across boundaries, and the error-handling strategy.
3. **Trace provenance as you go.** For every input a component needs, confirm a
   real producer exists (grep/read for it) or raise it as an `UNRESOLVED
   DEPENDENCY` to resolve with the human — do not assume it into existence.
4. **Design for isolation & clarity.** For each unit, be able to answer: what is
   its one job, how is it used, and what does it depend on — and "could someone
   change its internals without breaking consumers?" If a boundary can't pass that
   test, refine it. Prefer small, well-bounded units with explicit interfaces.
5. **Targeted improvements only.** If code this feature touches has a real problem
   (a seam that's grown tangled, an unclear boundary), fold a focused improvement
   into the design. Do NOT propose unrelated refactoring.
6. **Propose 2–3 architectural approaches** once the shape is clear; lead with your
   recommendation and explain *why*, with trade-offs for the alternatives.
7. **Present each phase design** in sections scaled to complexity — architecture,
   components, **data flow, data provenance**, integration seams, error handling,
   testing. Validate each with the human; revise until they approve the full set.
8. **Testing — STRICT TDD (REQUIRED in every phase design):** specify tests for all
   five scenario categories — happy-path, success, failure, error, edge-case — and
   state this explicitly in each phase doc (the strategy/intent, not the test code).
9. **YAGNI.** Get explicit approval of the full set before writing.

## Required section in EVERY phase doc — "Data Provenance"
A table/list: each input → the existing `file:symbol` producer that feeds it in
production, OR `UNRESOLVED DEPENDENCY: <description>`. Plus an explicit
**"Deferred to plans"** list naming the composition-root wiring / schema /
data-population work this design assumes will happen. No phase doc is complete
without this section.

## On approval — write, self-review, then finish
- For each phase N (1..K), write the phase design (Markdown, starting
  `# <Feature> — Design Phase N: <title>`) to:
  `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-N.md`
- **Design self-review (fix inline):** scan each doc for placeholders/TBDs,
  cross-phase contradictions, ambiguity, scope creep, drift into
  implementation-level detail (function bodies, signatures, field-level schemas),
  **and any input lacking a traced producer or `UNRESOLVED DEPENDENCY` flag** —
  fix all of them in place. Every `UNRESOLVED DEPENDENCY` must remain visible (do
  not quietly resolve it by assuming a source).
- Commit locally only: `git -C ./work add docs/designs && git -C ./work commit -m "docs: add {{slug}} phased designs"`
- Emit the completion result with ONLY `{design_paths}` (an array of the paths
  you wrote, relative to the worktree, e.g.
  `docs/designs/{{run_date}}-{{slug}}-design-phase-1.md`). Do NOT include design
  bodies. The slug is fixed by the workflow; never choose or change it.
