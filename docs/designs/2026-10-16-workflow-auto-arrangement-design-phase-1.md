# Workflow Auto-Arrangement — Design Phase 1: Layout Core and Review Contract

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`

## Goal

Make execution paths traceable without moving nodes: continuous downward progression,
branches beside the flow, distinct loop returns, readable labels and individually
traceable connections. Readability takes priority over compactness. This phase defines
geometry, deterministic arrangement and refinement, and the contract for reviewing every
candidate's actual rendered result. Phases 2 and 3 store and display that same geometry.

## Decisions and shared vocabulary

- Pure Go arrangement and geometric preflight live in `domain/layout`; domain code imports
  no concrete adapters. The attempt coordinator calls a rendered-review port, so the
  coordinator itself is not I/O-free.
- **Geometric preflight is not visual review.** A production renderer must draw each
  candidate with the dashboard's actual node/edge components, CSS and fonts. A readability
  reviewer examines the rendered result and returns findings. A promise that the browser
  will obey geometry is insufficient evidence. These producers do not currently exist:
  see the RENDER-REVIEW task mapping below.
- **Saved revision** means a server-issued token for one committed authoring save,
  including a save with identical content or changed positions. It is not a content hash.
  Phase 2 owns revision stamping and its propagation into instance snapshots.
- **Attempt** means arrange → render → review. Default budget is five total attempts,
  not five retries after the initial attempt. A manual retry starts a new job generation
  for the same saved revision; old generations cannot publish over it.
- **Candidate** is one geometry value. **Best available layout** is the structurally valid
  candidate with the best review score, or retained existing presentation if none exists.
  Only a candidate whose rendered review passed may have verdict `passed`.

## Architecture and components

```
immutable saved manifest + savedRevision + render contract + maxAttempts + context
    → arrange candidate → geometric preflight → render and readability review
              ↑                                      │
              └──── findings-driven refinement ──────┘
    → result: best geometry, review evidence, verdict, findings, attempts, failure
```

### 1. Geometry and renderer contract

A versioned serialisable value contains node boxes, complete displayed node text, routed
edge paths, source/target boundary attachments, arrowheads, branch-label boxes, loop-return
channels, any visible loop-container boxes/labels, and canvas extent. Include the renderer
contract version and typography, wrapping, padding, stroke, marker and crossing treatment
needed to reproduce what was reviewed. Runtime state/selection may change styling, never
geometry or label placement.

Use each edge's **ordinal in the immutable manifest edge slice** as its identity, with
`Source`, `Target`, `Output` and `Label` as verification data. `workflow.Edge` has no ID and
no enforced uniqueness of `(Source, Target, Output)`; identical parallel edges must not
collapse. Every manifest node has one box and every edge occurrence has one route.

Displayed node text is `Node.Label` when non-empty, otherwise `Node.ID`, plus the type
caption from `Node.Type`. For branch text preserve the current JSON/UI rule:
`edge.label ?? edge.output`, with omitted/empty Go fields following their JSON `omitempty`
semantics. Preflight and rendering must use the same normalised text. Long labels must
wrap or grow boxes rather than silently ellipsize. Layout cannot change workflow behavior,
conditions, node configuration, edge ordering or connections.

### 2. Arranger

Layered top-to-bottom ranking excludes back-edges using the DFS/back-edge semantics in
`dashboard/src/graph/loops.ts:detectLoops`. Port its interpretation, not its pair-keyed
edge identity: classify and route every parallel edge occurrence. Support self-loops,
nested/intersecting loops, disconnected components and multiple entrypoints. Loop bodies
may be clustered; every visible cluster decoration is part of the geometry and review.
Sequential flow never wraps into columns. Forward destinations are below sources;
branches sit beside the flow, and shortcuts/rejoins get traceable routes. Orthogonal
routing avoids unrelated node boxes, separates parallel lanes, and reserves side channels
for loop returns. `dashboard/src/graph/loopEdgeRouting.ts:chooseLoopSide` is a reference
for side choice, not a source of already-computed Go routes.

The selected construction reserves source-label, source-fanout, main-track and target-fanin
bands per rank channel. Conflicting endpoint columns receive unique transit columns;
precedence cycles are never resolved by dropping constraints. Branch text sits beside its
route with explicit clearance, and each labelled self loop has its own measured lane strip.
Evaluate complete routed candidates for every bounded crossing-search prefix, choosing
blocking-contact count, then proper crossing count, then deterministic prefix order. A
remaining advisory crossing is one that survived that bounded routed search.

Inputs are the immutable nodes/edges, renderer measurements/contract, and an explicit
options value (rank/node spacing, gutter width, lane separation and crossing-search
budget). Defaults and refinement increments are new Phase 1 policy values, not catalog
metadata. The arranger never mutates the manifest or preserves manual coordinates as an
arrangement constraint.

### 3. Geometric preflight and rendered readability reviewer

Findings carry a criterion, severity, implicated node IDs/edge ordinals, and evidence.
Both stages check the spec's acceptance criteria:

- Node overlap; routes through unrelated nodes; node/branch/container text collisions,
  clipping or occlusion; unreadable text at the practical viewing zoom — **blocking**.
- Merged/overlapping lines concealing destinations; ambiguous attachments or arrow
  directions; crossings indistinguishable from junctions — **blocking**. Use a visible
  crossing treatment where necessary; a crossing at a bend is not merely advisory.
- Indistinct branches/rejoins/shortcuts/loop returns or upward forward progression —
  **blocking**, including issues visible in the rendering despite clean box geometry.
- Unnecessary crossings that the bounded routing search can remove — refine before
  passing. Remaining unavoidable, visibly distinguishable crossings are advisory;
  neither zero crossings nor global optimality is promised.

Artifact metadata must bind the exact candidate/contract, include a full overview and
cover the entire zero-origin canvas with readable-scale tiles; one tiny tile is insufficient.
Domain checks the evidence envelope; the production reviewer opens and checks actual PNGs.

A pass requires zero blocking findings **and successful rendered review of that exact
candidate with the matching renderer contract**. Missing render evidence, unavailable
review, or reviewer error never implies a pass. Review the whole graph in overlapping
readable-scale tiles plus an overview, so long workflows are not reviewed only as tiny
fit-to-screen images. A practical zoom starts from the existing `DEFAULT_ZOOM = 0.9`;
font/contrast/clearance thresholds and visual-review mechanics remain explicit planning
work under RENDER-REVIEW, not assumed constants supplied by the domain today.

### 4. Refinement and bounded attempt coordinator

Findings drive targeted spacing, lane, label, loop-gutter and routing-search changes.
Prefer candidates with verified complete rendered evidence over unreviewed candidates;
within the same evidence class, rank lexicographically by blocking issue severity/count, then advisory score
(including crossing count), then deterministic attempt order. A layout with unresolved
blocking issues must not beat a passing one because it is compact.

Call arrange → preflight → render/review for each produced candidate, including candidates
that fail preflight; never call an unrendered candidate visually reviewed. Stop on a pass,
the configured budget, cancellation or proven inability to make a different candidate.
Early exhaustion returns `needs_attention` with the reason and unresolved findings.
Count attempts once at arrangement start; a render/review failure does not reset or evade
the budget. Return attempts used, best geometry if any, findings/review evidence for that
geometry, and a separate failure/cancellation indication. Renderer/reviewer exceptions
produce `failed`; retain best-so-far and state clearly if it remains unreviewed. The worker
must not turn that result into `passed`.

## Data flow and phase output

1. Phase 2 supplies one captured saved manifest, its revision, configured budget, context
   and the renderer/reviewer implementation. It does not reload a moving manifest mid-job.
2. Build the raw ID→Node.Type map from the captured manifest (never reverse a caption);
   obtain measurements for the full text/styles, arrange, preflight, render and review.
   Each Measure/Render/Review call has its own bounded stage context. Refine from findings
   while budget remains; text rounding slack grows node and branch label boxes.
3. Return the best candidate and its own evidence/findings, never another attempt's
   verdict. Phase 2 persists the value; Phase 3 renders it without recomputing paths.
4. Phase 3's shared renderer is also required by production visual review. Phase 1 can be
   unit-tested with a fake port; the production pipeline is not complete until this
   cross-phase dependency is implemented and verified.

## Data Provenance

Every new producer below is a planning dependency, not an existing capability. These
markers must become implementation tasks or explicit escalations; do not remove them
merely because a proposed symbol has been named.

| Leaf input | Existing producer or explicit unresolved dependency |
|---|---|
| Node ID, label, type and all behavior to preserve | `domain/workflow/workflow.go:Node`, `NodeConfig`, `Workflow`; loaded by `domain/workflow/ports.go:Repository.Load` |
| Edge occurrences, endpoints, output and label | `domain/workflow/workflow.go:Workflow.Edges`, `Edge`; slice index supplies occurrence identity |
| Existing saved coordinates (fallback only, Phase 2) | `domain/workflow/workflow.go:Node.Position` |
| Snapshot of manifest content | `domain/instance/instance.go:Instance.WorkflowSnapshot`; JSON marshal/unmarshal in `domain/engine/engine.go:Engine.TriggerWithDepartment` |
| Loop classification and side-choice reference | `dashboard/src/graph/loops.ts:detectLoops`; `dashboard/src/graph/loopEdgeRouting.ts:chooseLoopSide` (Go implementation is new) |
| Current displayed node text/style and edge text | `dashboard/src/graph/useGraphLayout.ts:useGraphLayout`; `dashboard/src/graph/nodes/ArbitronNode.tsx:ArbitronNode`; `dashboard/src/graph/nodes/ArbitronNode.module.css`; these currently use truncation and do not supply measured font metrics |
| Current fonts and practical zoom reference | `dashboard/src/styles/theme.css` (`--font-family`, system-font stack); `dashboard/src/graph/constants.ts:DEFAULT_ZOOM`; neither supplies portable measured glyph widths |
| Actual glyph/text measurements, raw node-type styles, renderer version, render artifacts, whole-graph visual findings and readability thresholds | **PLANNED DEPENDENCY RENDER-REVIEW (P3T2–P3T7):** no production candidate renderer, measured typography producer or rendered readability reviewer exists in `domain/layout`, `go.mod` or `dashboard/package.json`. Phase 1 defines the port, Phase 3 supplies the shared renderer; plans must select/build the render/review adapter and its runtime/font dependencies. Do not substitute guessed Go font constants or geometric checks for rendered review. |
| Core geometry/options/refinement policy/result values | **PLANNED DEPENDENCY LAYOUT-CORE (P1T1–P1T9):** `domain/layout` does not exist. Phase 1 must implement the models, defaults, edge identity, algorithms, invariants and attempt coordinator described here. |
| Saved revision and immutable job input | **PLANNED DEPENDENCY SAVE-REVISION (P2T2–P2T4/P2T7, commit protocol per SAVE-COMMIT-PROOF resolution / P2T7 Task 7.5):** Phase 2 must add server-stamped revision metadata and captured requests; no current `Workflow` revision field exists. |
| Attempt limit | **PLANNED DEPENDENCY LAYOUT-CONFIG (P2T1/P2T9):** Phase 2 must add `LayoutConfig.MaxAttempts`; `domain/config/config.go:Config` and `adapter/config/loader.go:Load` exist but have no layout setting. |
| Cancellation/deadline | **PLANNED DEPENDENCY ASYNC-PIPELINE (P2T8 Tasks 8.1–8.7/P2T9, commit protocol per SAVE-COMMIT-PROOF resolution / P2T7 Task 7.5):** Phase 2 worker supplies the job context; `context.Context` alone is not an existing layout worker. |

### Deferred to plans

Exact field names/serialization, geometric algorithms, refinement weights and review
adapter selection are planning tasks within the contracts above. No deferred decision may
waive actual rendered review or change the revision and edge-identity contracts.

## Error handling

Empty graphs, dangling endpoints, duplicate node IDs and non-finite geometry return typed
errors without panics. Invalid manifests remain saved; arrangement failure is reported.
Cancellation and later-attempt errors retain the best complete geometry/evidence already
available. A review error preserves its failure reason and cannot produce a pass.

## Testing strategy (STRICT TDD — required)

For each behavior write a failing test first, run and observe the expected failure,
implement the minimum change, then refactor with tests green. Port fakes test orchestration;
production renderer/reviewer integration and visual acceptance cannot be replaced by fakes.

- **Happy path:** linear graph is one downward column; `samples/pr-review/workflow.json`
  receives actual rendered review and passes at readable zoom.
- **Success:** `samples/feature-pipeline/workflow.json` (66 nodes / 79 edges) passes within
  five total attempts with continuous downward progression and readable branches, rejoins,
  shortcuts and returns; `samples/ralph-loop/workflow.json` has distinct loop channels.
  Use rendered fixtures at practical zoom and inspect the full graph, not just snapshots
  of the model. Production visual acceptance completes with Phase 3.
- **Failure:** injected visual-only clipping, ambiguous crossings and concealed destinations
  prevent a pass even when geometric checks pass; budget exhaustion returns the best
  candidate's findings, `needs_attention`, and a retryable result.
- **Error:** invalid graph, measurement/render/review exception and cancellation preserve
  best-so-far where available, report the failing stage, and never synthesize a pass.
- **Edge cases:** single node, self-loop, nested/overlapping loops, multiple entrypoints,
  disconnected components, identical parallel edges, empty/fallback labels, long Unicode
  labels, early exhaustion and budget one; five attempts means exactly five at most.
- **Invariants:** every node and edge occurrence is represented once; immutable manifest
  bytes/behavior are unchanged; fixed manifest/options/measurements give deterministic
  geometry; findings and evidence always belong to the returned candidate.

## Implementation alignment and resolved commit provenance

The implementation contracts §6 map each original dependency to producer and wiring tasks.
`layout.Store` is the revision repository port. Production rendering uses bundled Inter,
Noto Sans SC and Noto Emoji, shipped glyph coverage/hash checks, the shared browser
renderer, actual DOM reports and deterministic raster checks over complete readable-scale
tiles (P3T2–P3T7). Coloured boxes or PNG presence alone cannot establish visual review.
P2T8 owns lifecycle/repeated recovery; P3T13 owns independent instance polling and variable
boxes; P3T7/P3T14 jointly own the production surface-parity acceptance gate.

**RESOLVED SAVE-COMMIT-PROOF (human decision 2026-10-17):** two-phase journal receipts
inside the destination mutation lock, with exact registry publication proof for registry
operations. The atomic publication transaction commits the prepared saved revision and
stable workflow ID together; an older publication row or a legacy NULL token is never
proof of a new publish. Shared rename atomically changes registry identity and publication
name/proof. Private manifest changes commit by atomic rename. All copies, publishes and
seeds stamp the destination manifest in staging before visibility.

P2T7's `WorkflowRepo` implements the workflow-owned `TreeCommitter` port with fsynced
backup/intent state. A failed overwrite/publication restores every prior tree byte before
abandoning the new prepared request; failed rollback retains its intent and blocks another
mutation. Recovery resolves tree/relocation intents before reads/seeding, compares exact
publication revision plus stable ID, rolls a committed operation forward to a receipt,
and restores/quarantines an uncommitted one. I/O/registry uncertainty preserves undecided
evidence for later polls. Mutation-coordinated Load sees only final committed or restored
content, not staging tokens or a rename gap.

Producer: `domain/workflow/commit_source.go:CommitSource` over the durable filesystem
journal, TreeCommitter and revision-capable publication store (P2T7 Task 7.5 and P2T6).
P2T9 injects the real journal/tree/publication/registry collaborators as
WorkerConfig.Committed and StamperDeps.Settler; P2T8 Worker.Recover consumes proven
committed immutable requests. Binding signatures, crash/rollback tests and exact proof
rules are in implementation contracts §§6.2 and 6.10. Every committed historical revision
remains eligible; current-revision/generation CAS prevents stale publication (§6.12).

## Current-source and parallel-group alignment (2026-10-18)

Implementation contracts §§6.18–6.20 reconcile current producers and the mandatory
P3T15–P3T20 completion chain. The initial Data Provenance table describes the design baseline,
not today's source availability. Existing active-rename/seed-ack/read-settlement/retry-lock
producers still require their named root/crash tests; old execution notes cannot waive them.

Parallel groups are part of universal every-save arrangement. P3T15 captures configs
and workflow.AnalyzeGroups ownership in BuildGraph; P3T16 produces recursively placed,
measured header/footer/container/task/lane geometry and real plus synthetic routes. Invalid
group topology yields explicit failure with retained fallback, never a passed ordinary-box
substitute. All group text and presentation connectors participate in preflight, actual
DOM/raster review, candidate scoring and full geometry digest. Preserve every real node
and edge occurrence and do not change execution graphs. Finite zero margin/slack and
integral zero search budget are valid; other spacing remains positive.

P3T16 migrates the shared format to arbitron.layout/v2 and renderer to arranged-renderer/2,
including structural Go/JSON/TS fixtures; P3T17 migrates the actual report surface and
P3T18 produces genuine screenshot fixtures. Earlier v1 examples are pre-migration contracts.
No untouched workflow is automatically reprocessed. Only a complete v2 group result can
be displayed as reviewed; old/missing group geometry stays explicitly unreviewed fallback.

## Split-task geometry and refinement contract (2026-10-19)

P3T16 changes both ordinary and group attachments to v2 ElementID resolution while
preserving each real NodeID/edge ordinal. It sizes fixed live-status rows from P3T15's
actual measured vocabulary, reserving the complete simultaneous block rather than one
badge. Group run states and task statuses are distinct inputs. Real and synthetic routes
share obstacle/crossing search and contribute to both reviewed and unreviewed candidate
scores; a connector crossing cannot vanish from best-result selection. Containment
outlines may enclose/cross their own contents; unrelated lane/node/text contacts remain
blocking. P3T17's drawing and P3T18's raster review include actual container/lane paint.

Contracts §§6.19–6.20 assign the exact signatures, fields and tests. P3T16's structural
wire/contract migration precedes P3T17's actual group report and P3T18's genuine captured
review fixtures. Each producer's own stopgap tests change when it lands; full release
proof belongs to P3T20 and is never an earlier task's green-checkpoint prerequisite.
