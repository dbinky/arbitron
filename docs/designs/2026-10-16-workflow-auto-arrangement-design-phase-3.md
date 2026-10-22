# Workflow Auto-Arrangement — Design Phase 3: Surfaces and Shared Rendering

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`
Depends on: Phase 1 geometry/review contract and Phase 2 saved revisions, durable requests,
`layout.Store`, publication fencing and worker. Supplies the shared rendering needed
by the production rendered-review adapter; the three phases are not independently live
until this dependency is satisfied.

## Goal

Show arrangement/review progress, truthful completion or attention/failure, unresolved
issues and Retry. Workflow and instance views use the same revision-scoped best/fallback
layout and renderer. Users can trace long workflows at readable zoom with vertical
navigation, without moving nodes. Supply the actual renderer used to review each candidate.

## Decisions and shared response contract

- Pull status over HTTP/MCP; no new WebSocket channel is required. Both graph views poll
  while their revision is pending/in progress, including completed instances whose layout
  review finishes after execution. Runtime node events are not a layout-completion signal.
- A revision response contains `savedRevision`, `jobGeneration`, state, stage, attempts
  used/limit, findings, failure reason, review evidence/renderer contract, and the displayed
  best geometry or retained fallback with its provenance. `passed` means actual rendered
  review passed; `needs_attention` never displays as complete/passed.
- Both views read the same authoritative revision record via a domain query seam.
  `layout.json` is a cache only. A superseded/failed revision may still have best geometry;
  preserve and return it. Otherwise return its captured fallback, not the latest workflow's
  geometry. “No arrangement” is reserved for pre-feature saves without request metadata.
- Disable manual positioning and remove layout-write APIs. Manifest positions remain
  readable only as retained/legacy fallback; every subsequent authoring save requests
  automatic arrangement with no opt-out.

## Components

### 1. Authorised domain query/retry seam

Define a small `domain/layout` query service using narrow consumer-owned ports for an
accessible manifest/destination, revision history, and retry. Keep `domain/workflow` as the
save-port owner; it must not import `domain/layout` and form a package cycle.

Workflow reads first use `domain/workflow/service.go:Service.Load` visibility rules and
resolve the same private/shared destination. Instance reads first use the existing
`Engine.GetInstance` authorization path and then the saved revision/fallback in the
snapshot. They must not require current workflow visibility or existence for an otherwise
accessible historical run, nor expose an arbitrary revision lookup that bypasses access
checks. Scope/owner from saved destination metadata is not a user-supplied authorization.

Retry must use the same ownership/admin/shared-write policy as `Service.Update`, without
rewriting the manifest or calling Update merely to authorize. Current code has no public
“authorize layout retry and return destination” method; implement a service-owned seam
(QUERY-AUTH dependency), sharing the existing policy. HTTP and MCP must not duplicate or
bypass it. Retry takes the expected saved revision and is fenced against a concurrent save;
a stale request returns conflict. Workflow retry resolves the authorized destination, takes the same MutationCoordinator lock as authoring saves, reloads/re-authorizes under it, then compares the expected token immediately before Requester. Historical instance retry uses its authorized immutable snapshot and never implicitly supersedes another revision job. Allow needs_attention/failed and saved requests whose
enqueue/status service failed; in-progress and valid passed results return conflict. A passed row whose stored result cannot be displayed is downgraded through Requester.InvalidateResult under exact generation fences before retry; Reader stays read-only. Store unavailability
still shows Retry but may return an explicit retry error until service recovers.
For historical instances, expose a separate instance-service-owned retry authorization
seam sharing `domain/engine/engine.go:Engine.authorizeIntervention` policy. An authorised
retry uses only the captured snapshot revision and cannot mutate execution or the current
workflow. This new seam is also QUERY-AUTH work; private helpers are policy references,
not public APIs available to adapters.

### 2. HTTP surfaces

In `adapter/http/workflow_handler.go:WorkflowHandler`, add:

- `GET /workflows/{name}/layout`: current revision status and best/fallback display value.
  Include the revision in the manifest read response too. The client joins manifest and
  layout by that token; if a save races the two reads, refetch instead of mixing geometry
  from one revision with nodes from another.
- `POST /workflows/{name}/layout/retry`: expected revision, shared authorization seam and
  requester; acknowledgement includes revision/generation/status and never waits for review.
- Remove `PUT /workflows/{name}/layout` (`SaveLayout`) and its route registration in
  `adapter/http/server.go:NewRouterFromDeps`.

Extend `adapter/http/instance_handler.go:InstanceHandler.Get` / `toInstanceResponse` with
layout status and best/fallback geometry keyed by the snapshot's saved token. Do not derive
the key from (`Instance.WorkflowID`, content hash): IDs may be nil and a hash is not a save
revision. A legacy snapshot lacking a token uses its own saved positions/dagre fallback.
Add `POST /instances/{id}/layout/retry` for failed/needs-attention historical revisions,
including cancelled/superseded unfinished review and passed records whose stored result is missing/incompatible/unsubstantiated. It uses the instance retry authorization
seam and snapshot token; it never redirects the request to a newer workflow revision.
This keeps the spec's Retry option available for retained incomplete results.

### 3. MCP surfaces

Extend `adapter/mcp/tools_workflow.go:workflowGetHandler` (`workflow_get`) with saved
revision and arrangement status/stage/attempts/findings/failure reason/retry eligibility.
Keep the manifest fields compatible; geometry need not be returned by MCP. Add
`workflow_layout_retry` with expected revision and the same domain retry seam as HTTP;
register it through `RegisterLayoutTools` called by `RegisterAllTools`, and update registry tests. Expose the historical-instance
retry seam in the existing MCP instance-tool family as `instance_layout_retry`. Save responses can acknowledge
pending arrangement immediately, without waiting for the worker.

### 4. Shared renderer and production rendered-review seam

Add a renderer alongside `dashboard/src/graph/useGraphLayout.ts:useGraphLayout`, used by
`WorkflowReview.tsx`, `ExecutionView.tsx`, and a deterministic render surface invoked by
the production RENDER-REVIEW adapter. All three consume the same versioned Phase 1 payload,
node/edge components, CSS and font assets. No production adapter or measurements exist in current source. The selected planned implementation is P3T5 BrowserRenderer (chromedp) plus P3T6 ReadabilityReviewer, consuming actual DOM evidence and raster feature checks. These are mandatory producers, not existing capabilities. It must
work for stdio MCP saves without a user opening the dashboard.

- Use payload node dimensions, wrapped text, padding and measured font contract. Add
  `graph/arranged/ArrangedNode.tsx` and CSS alongside the legacy
  `graph/nodes/ArbitronNode.tsx`: current min-width/ellipsis behavior cannot prove
  readable full labels or enforce the reviewed boxes. Add any required attachment handles
  without changing the manifest graph.
- A new routed edge component in `graph/arranged/` draws persisted paths, arrowheads, label
  boxes and crossing treatment exactly, keyed by manifest edge ordinal. It must not invoke
  xyflow's automatic smooth-step routing or recompute loop returns. Keep loop returns
  visually distinct (existing dashed `#7c8cf5` style is a reference); active-edge animation
  must preserve traceability and arrow direction.
- Draw loop-container decorations only if included in reviewed geometry. Runtime/selection
  styles, including running glows, cannot resize nodes or obscure labels/endpoints. Exercise
  those styles in rendered review/acceptance fixtures.
- Render measurement reports must include actual text/box bounds and ready font/version
  information. Wait for fonts and layout to settle before capture. Capture an overview and
  overlapping tiles at practical zoom across the entire canvas; return artifacts and
  measurements tied to candidate/revision/renderer version for the review adapter.
- Version mismatch, incomplete edge/node coverage or invalid geometry fails validation;
  return an explicit display/review issue and use the shared captured fallback. A payload
  compatibility error must not leave a “passed” badge while displaying an unreviewed layout.

The concrete font producer is P3T2 fonts.ts: bundled Inter, Noto Sans SC 5.3.1 and
Noto Emoji 5.3.2, exact shared stack/contract and build-time fontkit cmap/file-hash/sequence
coverage. Measure and render explicitly load the required bundled text ranges/weights;
unsupported glyphs/sequences fail visibly with fallback and Retry. System fallback or a
loaded Inter face alone never establishes parity. P3T3 imports xyflow global CSS in the
shared renderer itself, so a fresh render.html tab matches both interactive routes. It invokes
the same bundled-font readiness/coverage producer before drawing reviewed geometry, guards
revision/geometry/font-asset epochs and emits onDisplayIssues to P3T12/P3T13's panel.
Loading/failed fonts cannot coexist with a “Layout reviewed” badge. Browser measurements
retain fractional content dimensions; domain sizing alone rounds node/label boxes.

P1T9 fills RenderRequest.WorkflowNodeTypes directly from immutable Job.Manifest.Nodes[].Type.
P3T5 forwards that map plus revision/attempt; P3T4 never reverses TypeCaption to invent types.
P3T4 collects actual DOM textContent, Range glyph regions/visibility, transformed full M/L
subpaths, computed stroke/dashes and visible marker outlines. P3T6 compares all of them and
checks every route corridor, glyph region, arrow and crossing-gap pixel across complete
overlapping tiles. Coloured node rectangles, clean endpoints or PNG presence alone cannot
pass. P3T5 creates immutable unique capture directories, preserving referenced evidence.
Runtime probes cover path/label/crossing/marker stability, selected+running combinations
and animation extrema before deterministic baseline capture. SVG marker IDs include a
stable unique graph-instance namespace as well as ordinal to support concurrent graphs.

This is the cross-phase producer consumed by Phase 1's rendered-review port and wired in
Phase 2. Fake port tests do not establish production visual readability.

### 5. Dashboard navigation, status and refresh

Extend `dashboard/src/api/client.ts` and graph types with revision/status/geometry fields;
remove `workflows.saveLayout`. The common renderer consumes the revision-scoped best or
retained fallback. Only legacy/no-geometry fallback uses `useGraphLayout`, with all
`needsAutoSave` writes removed. Extend the fallback conversion to preserve known
revision-scoped coordinates when only some nodes have them, placing missing nodes
deterministically and routing the actual saved edges. The existing all-or-nothing
`useGraphLayout` behavior currently discards partial positions; it is not already a
producer of this retention behavior (new work under LAYOUT-CORE/SAVE-REVISION).
No arbitrary old `layout.json` can be treated as current.

Use a readable initial zoom (existing `graph/constants.ts:DEFAULT_ZOOM = 0.9`), centred on
an entry/start node, with vertical scrolling/panning through long graphs. Remove the
unconditional fit-all `fitView` in `WorkflowReview.tsx`; fit-all can remain an explicit
navigation control. Do not shrink feature-pipeline to fit one screen. Preserve node
selection, callouts and follow-active-node behavior; update `useFollowActiveNode` inputs and remember terminal anchors for explicit Recenter without terminal auto-pan,
and `ExecutionView.tsx` viewport/callout calculations that currently use fixed
`NODE_WIDTH`/`NODE_HEIGHT` to use payload dimensions.

Both views show pending, arranging, rendering/reviewing, complete, needs attention or
failed; list findings and failure reason. Superseded is job lifecycle state, not a review
pass: unfinished superseded work displays needs attention with its retained best/fallback.
Retry is available for eligible current revisions with workflow write access and for
historical revisions through the instance authorization seam. Poll revision status while work is
active, even if execution is terminal or idle. Retry acknowledgement starts polling again.
Use request/key epochs and revision/job/result generations to ignore out-of-order responses, including route A→B→A. RetryAck installs a minimum generation before refetch so stale terminal reads cannot stop polling. In-band available=false follows error backoff while retaining last compatible same-revision geometry. Network
or status-storage errors keep last geometry, show an availability error and Retry/reload,
and back off without silently reporting no arrangement. Refetch current workflow metadata
on focus/save invalidation so external HTTP/MCP saves are visible after terminal polling
stops. Geometry and status for a revision must update together in both views.

### 6. Remove manual positioning and build

Delete both `saveLayout` calls, drag handlers and auto-save bookkeeping in
`WorkflowReview.tsx`; disable `nodesDraggable` in both views. Delete
`domain/workflow/service.go:Service.SaveNodePositions`,
`domain/workflow/ports.go:Repository.SaveNodePositions`, the filesystem implementation,
and HTTP handler/route. Preserve position parsing for legacy fallback. No node creation,
editing or layout opt-out is introduced. `make ui` refreshes embedded dashboard assets
after source changes.

## Data flow

- Workflow: authorised manifest+revision → same-revision query → best/fallback renderer;
  poll active work, display truthful terminal verdict/findings, retry with expected token.
- Instance: authorised instance → snapshot revision+fallback → same query/result → shared
  renderer; poll layout independently of execution WebSocket updates; retry its exact historical revision.
- Review: worker's immutable candidate → shared renderer measurements/capture → actual
  readability reviewer → findings/refinement; displayed geometry is the reviewed value.

## Data Provenance

| Leaf input | Existing producer or explicit unresolved dependency |
|---|---|
| Workflow graph and read visibility | `domain/workflow/service.go:Service.Load`; `domain/workflow/workflow.go:Workflow`, `Node`, `Edge` |
| Instance snapshot and access check | `domain/instance/instance.go:Instance.WorkflowSnapshot`; `domain/engine/engine.go:Engine.TriggerWithDepartment`, `Engine.GetInstance`, `Engine.authorizeIntervention`; `adapter/http/instance_handler.go:InstanceHandler.Get` |
| Current/private/shared write-policy reference | `domain/workflow/service.go:Service.Update`, `Service.adminSharedWrite`, `Service.translateWrite`; these are not an existing retry-authorization API |
| Reusable authorised query/retry and destination resolution | **PLANNED DEPENDENCY QUERY-AUTH (P3T8–P3T10):** Phase 3 must implement query ports/service and a workflow-service-owned write-authorization seam, including instance visibility/intervention authorization and expected-revision checks. |
| Saved revision, exact saved graph and immutable retained fallback | **PLANNED DEPENDENCY SAVE-REVISION (P2T2–P2T4/P2T7, commit protocol per SAVE-COMMIT-PROOF resolution / P2T7 Task 7.5):** Phase 2 presentation metadata/snapshot propagation; current `Instance.WorkflowID` and content hashes do not provide this. |
| Revision status, attempts, stage, findings, review evidence and best geometry | **PLANNED DEPENDENCY LAYOUT-STORE (P2T5/P2T6/P2T9):** Phase 2 `layout.Store` and adapter; `layout.json` is only the PUBLICATION-CAS cache, not a second source of truth. |
| Retry scheduling/generation and progress | **PLANNED DEPENDENCY ASYNC-PIPELINE (P2T8 Tasks 8.1–8.7/P2T9, commit protocol per SAVE-COMMIT-PROOF resolution / P2T7 Task 7.5):** Phase 2 requester/worker. |
| Geometry, edge ordinals, size/typography/render-version contract | **PLANNED DEPENDENCY LAYOUT-CORE (P1T1–P1T9):** Phase 1 model and algorithms; clients mirror this shared contract. |
| Reviewed boxes/fonts, rendered artifacts and visual findings | **PLANNED DEPENDENCY RENDER-REVIEW (P3T2–P3T7):** Phase 3 supplies the shared renderer/measurement surface; plans must select/build the production capture/readability adapter and font/runtime dependencies. Current system fonts and xyflow rendering do not themselves produce review evidence. |
| Retained/legacy positions and fallback routing | `domain/workflow/workflow.go:Node.Position`; `dashboard/src/graph/useGraphLayout.ts:useGraphLayout` (stored positions or dagre); captured per-revision fallback is new SAVE-REVISION work |
| Display text, node type, runtime state and active-edge animation | `dashboard/src/graph/useGraphLayout.ts:useGraphLayout` (`n.label || n.id`, `n.type`, `executionStates[e.source]`); `dashboard/src/routes/ExecutionView.tsx:buildExecutionStates`, consumed by `GraphView`; `dashboard/src/graph/nodes/ArbitronNode.tsx:ArbitronNode` |
| Existing navigation/style references | `dashboard/src/graph/constants.ts:DEFAULT_ZOOM`; `dashboard/src/graph/useFollowActiveNode.ts:useFollowActiveNode`; `dashboard/src/graph/edges/LoopBackEdge.tsx:LoopBackEdge`; `dashboard/src/styles/theme.css` |

The implementation contracts now select HTTP/MCP shapes, polling backoff, query port
signatures, render runtime and component conversion. Their §6 maps these dependencies
to producer/wiring tasks and records the SAVE-COMMIT-PROOF resolution (contracts §6.2).

## Error handling

No request metadata on a pre-feature revision means legacy fallback, with no automatic
write or arrangement request. Missing rows for a saved requested revision mean pending
recovery or an explicit service error, never a legacy/no-arrangement success. Storage,
cache, network, version and renderer errors retain best/fallback and expose the issue.
Unauthorised reads/retries follow existing concealment rules; stale-revision retry is a
conflict. Neither retries nor fallback rendering modify workflow execution data.

## Testing strategy (STRICT TDD — required)

For every behavior write the failing test, observe the intended failure, implement and
refactor. Use Go httptest/MCP tests, Vitest component/hook tests, and real-browser
render/review acceptance; jsdom cannot supply production font/render evidence.

- **Happy path:** current-revision GET returns passed status and geometry; both views and
  the review surface draw identical nodes, labels, paths and attachments from one fixture.
- **Success:** real rendered pr-review and feature-pipeline satisfy every spec criterion
  at practical zoom with continuous downward navigation; show stage/attempt progress;
  HTTP and MCP retry yield a new generation and both views refresh to its terminal result.
- **Failure:** exhausted review shows needs attention, findings and best layout plus Retry;
  failed render/review/enqueue retains best/fallback and reason; ambiguous crossings,
  clipped Unicode labels and runtime styling that obscures endpoints fail visual review.
- **Error:** unauthorized read/retry, storage outage, corrupt/missing cache, invalid payload,
  renderer-version mismatch and polling failures expose errors without losing the graph
  or falsely displaying a pass; stale-revision retry conflicts.
- **Edge cases:** instance opened while review is pending updates even after execution
  completes; superseded/failed history uses retained geometry; legacy nil-ID/tokenless
  snapshots use their own fallback without writes; workflow save/read races and stale
  network/retry responses cannot mix revisions/generations; deletion/current visibility
  change does not block an authorised historical instance from reading/retrying its layout;
  historical retry never changes the current workflow's layout or execution.
- **Regression:** dragging/layout PUT/save calls are removed; fallback does not auto-save;
  long labels wrap, large graphs start at readable zoom rather than fit-all; variable node
  dimensions preserve callouts/follow; loop styling and edge animation preserve endpoints;
  polling stops at terminal state/unmount and restarts on retry or current-revision refresh.
- Run `npx tsc --noEmit` and `npx vitest run` in `dashboard/`, affected Go tests, and
  `make ui` for implementation.

## Implementation alignment and resolved commit provenance

The implementation contracts §6 map every original dependency marker to producer and
wiring tasks. `layout.Store` is the selected name for the revision repository port.
The selected render/review implementation is the bundled Inter/Noto stack + shared browser renderer,
actual DOM reports and deterministic raster checks over complete readable-scale tiles
(P3T2–P3T7). Presence of PNGs or coloured node boxes alone is insufficient visual review.
Worker lifecycle/repeated recovery is explicitly P2T8 Tasks 8.5–8.7; instance polling and
variable-size callouts are P3T13 Tasks 13.3–13.4. Production surface parity is P3T14 Task 14.3.

**RESOLVED DEPENDENCY SAVE-COMMIT-PROOF (2026-10-17):** Use the two-phase journal
with durable receipts inside destination mutation locks. P2T7 CommitSource/RevisionJournal
recover only proven committed input. Registry-backed shared saves/seeds/publications use
RevisionPublicationStore.CommitPublication with exact new Revision + matching WorkflowID;
an older publication record or mere folder presence is not evidence. TreeCommitter keeps
a durable complete prior-tree backup and restores it on publication failure; undecided
rollback/registry errors preserve intents and block further conflicting mutation. Rename
uses atomic RenamePublication for registry identity plus publication revision. Relocation
resolves stable WorkflowID through Registry.ResolveName/Resolve under both endpoint locks;
registry-authoritative recovery handles both-folders-present windows, rather than treating
source-folder existence as a commit decision. P2T9 injects real publications, registry,
mutation coordinator and tree committer into CommitSource before seeding; WorkerConfig.Committed
and StamperDeps.Settler share it. Binding signatures, commit windows, settle rules and
kill/restart tests are implementation contracts §§6.2, 6.10 and 6.12.

## Phase 3 completion gate and known producer dependencies

P3T7 makes make test-render (ARBITRON_REQUIRE_BROWSER=1, rebuilt embedded assets) a required
part of make test-all and CI; P3T14 extends it with actual authorized HTTP → WorkflowReview
and ExecutionView browser parity. Every box/text, ordinal/path/label/attachment/arrow and
review digest must match the persisted revision/generation, including completed-instance
late review, retry, cache loss, external save and restart. Unit fakes, nil geometry/status
or browserless skipped tests cannot establish this acceptance. A missing runtime browser
is an explicit retryable job failure; a missing render.html is a boot/build failure.

Exact new symbols, required signatures, ownership and tests live in implementation
contracts §§6.5–6.7 and §§6.13–6.14. All remain planned: P1T8/P1T9 RenderRequest/map and
coordinator, P2T5 Requester/Reader/Store/helper ports, P2T7 commit/locks, P2T9 construction,
P3T2 fonts.ts/coverage, P3T3 shared CSS/renderer, P3T4 actual report/page protocol,
P3T5 immutable captures, P3T6 reviewer, P3T7/14 production acceptance. No unresolved
decision authorizes an empty success stub or a guessed leaf input.

P2T7 external reads settle durable mutations under their destination lock through the
P2T9-injected read settler. Unresolved peer-crash relocation returns ErrMutationPending
until a two-endpoint recovery sweep decides it. P3T9/P3T10 expose the fixed safe recovery
reason as HTTP503 UNAVAILABLE / MCP tool error; P3T14 verifies a surviving peer never
displays a killed writer's uncommitted tree and refreshes after real settlement.

## Current-source and parallel-group alignment (2026-10-18)

Implementation contracts §§6.18–6.20 replace stale producer gaps with named source
APIs/tasks and retain unverified release gates. Retry locks caller-private AND shared
resolution candidates before reauthorizing scope/token. The actual root wires settled
raw reserved-file reads and a local-only stdio identity; P2T9.8 supplies stderr console
logging and unfiltered subprocess protocol/SQLite-sink tests; preserve those guards. Client-only font failure offers
Reload reviewed fonts without POSTing a valid passed result; server render/font failure
retains arrangement Retry. Zero margin/slack and zero integral search budget match Go.

P3T15–P3T20 are mandatory: all visible group headers/footers/containers/cards/lanes and synthetic
connectors must come from the same measured, persisted v2 geometry in both user views and
render review. Shared group components use contract fonts/wrapping/stroke, measured live
status slots and no runtime box growth; unbounded live details remain in GroupPanel/callouts.
The format migrates to arbitron.layout/v2 and arranged-renderer/2 with full Go/JSON/TS/report
parity; old/missing group geometry visibly falls back and cannot show a reviewed badge.

Replace composeLiveGraph's reviewed-mode relayout with non-overlapping vertical revision
tiles in the existing single scene. The root and visible child snapshots keep their own
local geometry/revision/digest; only display translation changes. Branch contexts overlay
parent nodes. Existing GraphStateReader supplies authorized snapshots/ownership/live data;
P3T19's useInstanceLayoutTiles polls the existing instance layout query independently of
execution. Per-tile errors/status remain visible and selection/follow uses qualified IDs
and actual element bounds. No merged scene is claimed as one reviewed revision.

P3T15, P3T16, P3T17, P3T18, P3T19 and P3T20 own captured input/types, geometry,
actual drawing/capture, raster review/storage, runtime mounts and root/browser release
proof respectively, in that order. Nil group/live/child-layout inputs and erased
synthetic-route pixels must fail real sink tests. P3T7/P3T14 sample/Docker/full-suite gates
remain necessary; no passing unit fixture or browserless skip establishes completion.

## Split-task rendering and live input closure (2026-10-19)

P3T17 owns the optional-sixth-argument buildArrangedFlow runtime contract and live props
on ArrangedGraph before RenderSurface consumes them. Its groupProbes.ts creates review
overlays from the captured config/task vocabulary, with a shared Go/TS probe-name
contract distinguishing group run states from task statuses. Actual DOM observations
set probe results; P3T18 requires every case and checks real group text/outline/background/
connector ink. Neither empty maps nor a clean expected report establish that evidence.

P3T19 owns abortable instance layout GETs, a bounded child poll scheduler, pure fallback
extraction and translation-only scene composition with a supplied stable graph namespace.
The existing root hook polls root; branch contexts project live state onto their parent;
visible grouped workflow/spawn snapshots use their own authorized endpoint/revision.
Keep layout-tile identity separate from the branch instance used for selection. Place
variable titles/status/Retry panels in a rail outside reviewed graph ink. Child errors
remain visible and cannot relayout the reviewed root. P3T20 drives the existing real
HTTP/MCP roots and both embedded routes, preserving the unfiltered make test-render gate.
Exact APIs, field producers and negative controls are implementation contracts §6.20.
