# Workflow Auto-Arrangement — Phase 3 Task 17: One group drawing path, actual measurement and capture

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§6.19–6.20, and [P3T15 geometry/input interfaces](2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-15.md#exact-geometryinput-interfaces-p3t15-declarations-p3t16-validation-p3t17-report-declarations).

**Spec/design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; matching design phases 1–3. Go 1.25.5, React 19/xyflow 12 and the existing browser/DOM/raster pipeline.

**Global constraints:** Arrange every committed authoring save asynchronously; five total attempts by default; preserve execution and every manifest edge occurrence; fence by saved revision/generation; share reviewed geometry across workflow/instance/review surfaces; retain best/fallback and truthful issues/Retry on failure. No manual positioning, opt-out or automatic processing of untouched legacy workflows.

**Order:** P3T15 (captured input) → P3T16 (geometry) → P3T17 (drawing/capture) → P3T18 (review/storage) → P3T19 (live views) → P3T20 (root/release). These replace former P3T15 stages 15.2–15.6. Each task owns its green checkpoint; successor acceptance is not a prerequisite.

**Strict TDD:** For each numbered cycle write the named assertions first, run the focused command and observe behavioral RED against compiling code, implement the minimum, rerun GREEN and refactor. For already-correct behavior remove its production guard/feed temporarily and observe the sink assertion fail, restore it and rerun. Compiler errors, constructor counts and browserless skips do not prove the behavior. Do not check a completion box during this docs review.

**Goal:** Draw P3T16 geometry identically through the shared components and produce actual group DOM/capture evidence and runtime probes for P3T18.

**Files:** `dashboard/src/graph/arranged/{buildArrangedFlow,ArrangedGraph,RoutedEdge,routedPath,fonts}.ts(x)`, `ArrangedNode.tsx`/CSS, `graph/groupLayout.ts`, `graph/nodes/GroupContainerNode.tsx` and CSS; `src/render/{MeasureSurface,RenderSurface,report,job}.ts(x)`, new `src/render/groupProbes.ts`, `arranged/groupProbe.contract.json`; `domain/layout/render.go`, new `group_render.go`; `adapter/renderreview/browser.go`. Tests adjacent, including real browser surface tests in adapter/renderreview. P3T18 owns reviewer changes/genuine success fixtures; P3T19 owns real live callers and tiles.

**Interfaces:** Consume complete P3T16 Geometry and P3T15 WorkflowGroups/measurement vocabulary. Produce the exact ArrangedRuntime/buildArrangedFlow optional-sixth-argument API, `ArrangedGroupElementData`/`RoutedGroupConnectorData`, `RenderedGroupText`/`RenderedGroupElement`/`RenderedGroupConnector`/`RenderedGroupCrossing` and RenderReport group paint fields, GroupProbeCase/buildGroupProbeCases and GroupProbeNames specified in contracts §6.20. Add groupLive/nodeContexts to ArrangedGraphProps now, not first in P3T19: RenderSurface needs those real props in this task. Update all current callers and Go/TS serialization together; ordinary call behavior remains supported.

## 17.1 One actual group drawing path

- [x] **RED:** `ArrangedGraph.groups.test.tsx` and actual ReactFlow DOM tests assert exact nonempty group IDs/boxes/full strings, one real header, lane/body parenting, every original ordinal/path/label/attachment/arrow and synthetic route. Long labels wrap; supplied absolute geometry is converted to parent-relative node positions once. Verify mixed crossings split gaps on either real or synthetic under-routes. Missing/stale ownership, v1/missing geometry and unsupported glyphs expose fallback issues and suppress reviewed mode. A group map omitted from a group definition must fail; replace the former test that accepted ordinary reviewed boxes without ownership.
- [x] **GREEN:** Adapt existing GroupContainerNode/GroupHeaderNode/GroupFooterNode/GroupTaskCardNode via reviewed data and shared bundled fonts. Add the reviewed lane variant and geometry-based real/synthetic handles. Use persisted measured boxes/text rows, the explicit §6.20 baseline fill/outline/contrast policy and fixed OverlayBoxes; remove ellipsis, 9px text, opacity, double borders and arbitrary growth in reviewed mode. Update arrangedNodeTypes/arrangedEdgeTypes and RoutedEdge's distinct connector variant. No dagre or library path recomputation for compatible v2 geometry. Copy NodeContextInfo into the §6.20 ArrangedNodeData ownership/independent/hidden fields, preserving explicit null and original IDs; ordinary fixed-size indicators/accessible descriptions cannot add an unmeasured row. Test selectionFromNode on a branch-owned body node, a redacted null owner and a static node with absent ownership. Empty context forwarding must fail original-owner assertions.
- [x] DOM protocol: `data-arranged-group-element=<logical ID>`, `data-group-text=<Key>`, `data-group-connector=<logical ID>`; header also carries original `data-arranged-node`. Scope selectors with CSS.escape. Namespace rendered/marker IDs by graph-instance plus base64url UTF-8 logical identity; keep real ordinal marker suffixes. Preserve logical data IDs; P3T19 adds tile scoping without changing saved identity.

## 17.2 Actual report, font inputs and deterministic runtime producer

- [x] **RED:** `TestRenderReport_GroupWireFields` and job/report tests round-trip §6.20 exact fields, including false visible/hasArrow values. `collectGroupElements` observes DOM textContent/Range/computed visibility, border/background paint and bounds; `collectGroupConnectors` parses actual M/L subpaths, stroke/dashes and visible marker outlines; `collectGroupCrossings` observes actual separation. Missing/duplicate elements and changed middle segments must fail assertions even when expected geometry is clean. Report collectors never echo candidate values into actual observations.
- [x] **GREEN:** Declare and mirror RenderedGroupText/Element/Connector/Crossing and optional RenderReport arrays exactly once here. BrowserRenderer forwards WorkflowGroups/revision/attempt and returns that actual report without filtering fields. Extend page readiness/coverage to require all real and group text/route IDs. Ordinary reports omit group arrays. Include all static and overlay-vocabulary strings in the same font readiness/coverage checks on MeasureSurface, RenderSurface and ArrangedGraph; removing the only font subset needed by a winner/task label must reject capture.
- [x] **RED→GREEN:** `groupProbes.test.ts` / `TestGroupProbeNames` use the shared golden fixture to distinguish run states from task statuses. Build cases solely from immutable WorkflowGroups, including every state, unfinished/frozen status, all badges, redaction, selected+running and each configured winner (§6.20). RenderSurface forwards each case's groupLive/nodeContexts/selection into the actual ArrangedGraph. Test forwarding by changing a task key/status: actual DOM text and probe findings change; nil maps cannot pass as an exercised runtime case.
- [x] Collect actual group snapshots on each probe/animation phase. Static geometry/routes/marker dimensions must stay fixed; overlay text may change only within its measured rows/allowed vocabulary. Redacted title intentionally changes to hidden dependency, removes identity/links, and fits its reserved title box; do not compare dynamic content to an empty static baseline or require it to be unchanged. StateProbe booleans/issues come from these checks. Restore the static graph, settle fonts/layout, then capture the full overview/readable tiles.

## Wiring and capture sink

MeasureSurface and RenderSurface mount the same adapted components that ArrangedGraph will use in both real routes. Worker/Coordinator already construct independent captured WorkflowGroups and raw node type maps; the installed BrowserRenderer drives render.html without a user opening the dashboard. No optional live service or missing external producer supplies review-state overlays: groupProbes.ts does so here.

- [x] **Happy/success:** `TestBrowserRenderer_GroupGeometryReachesActualCapture` draws branches/workflows/nested v2 geometry with long Unicode labels, observes complete actual group arrays/paint, correct runtime probe names and immutable full-canvas PNGs. This task proves capture; P3T18 supplies the passing rendered review.
- [x] **Failure/error/edge:** delete WorkflowGroups, one connector DOM path/marker, group text, a glyph subset or a required probe case; run the actual browser and assert no ready successful capture, or a false probe with specific actual defect evidence as appropriate. Test cancellation, concurrent captures and ordinary/zero-group reports. No assertion may pass solely because the not-yet-extended reviewer rejects every group.
- [x] Run each focused test RED→GREEN, then `cd dashboard && npx vitest run src/graph/arranged src/render src/graph/nodes && npx tsc --noEmit`; `go test ./domain/layout -count=1`. Run `make ui` then `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview -run 'TestBrowserRenderer_Group|TestSurface.*Group' -count=1 -v`. Add the named tests before running these selectors and verify nonzero execution. Commit after green capture/negative controls; P3T18, not this task, regenerates reviewed-success fixtures.

## Execution record (2026-10-20)

P3T16's previously recorded Unicode-task-key gate remains blocked by unchanged
workflow validation; it was not rerun or silently relaxed. This pass continued
with P3T17's independent drawing/capture slice, preserving and completing the
existing uncommitted work. No reviewer, storage, live caller or release-success
implementation from P3T18–P3T20 is included; no reviewed-success PNG is regenerated.

The installed BrowserRenderer now drives the real render.html → RenderSurface →
ArrangedGraph → registered group components with captured ownership, actual browser
measurements and deterministic nonempty runtime maps. Header/body parenting and
namespaced geometry handles retain absolute persisted paths and original edge
ordinals. Manual paint layers put containment below routes and node/card ink above
them, without selection-driven layer promotion. Shared components draw measured
static text and fixed overlay rows; full static/finite vocabulary reaches the same
bundled-font gate. Capture hit-testing includes actual group paint/text and routes.

Actual DOM collectors return textContent/Range/computed visibility, bounds, border,
background, SVG segments/dashes/marker outlines and mixed separation. Runtime cases
check these observations at each animation phase, including actual fixed row bounds,
changing allowed text, redacted title reservations and restoration of static content
before capture. Go/TS names share the golden fixture, preserve task author order and
match JSON.stringify escaping and UTF-16 group-ID ordering.

Behavioral RED/GREEN evidence:
- Existing drawing, DOM, report, wire and empty-producer RED logs were retained and
  inspected: /tmp/p3t17-{drawing,dom,report,wire,probes}-red.log.
- /tmp/p3t17-initial-ui.log reproduces the empty runtime producer failure.
  /tmp/p3t17-capture-runtime-red.log reproduces absent actual runtime cases and
  group text occlusion; later actual capture exposed containment over edge labels.
  The capture gate is now green for branches, workflows and nested groups, including
  long Unicode labels and workflow titles with a title-only emoji font dependency.
- /tmp/p3t17-row-red.log: moving a frozen-status span 3px within the aggregate
  OverlayBox incorrectly passed probes. Actual reserved-row snapshots now reject it;
  /tmp/p3t17-row-green.log includes its passing negative control.
- /tmp/p3t17-probe-names-red.log and /tmp/p3t17-unicode-names-red.log establish the
  names producer and cross-language Unicode ordering fixes.

Ordered self-review completed:
1. Spec: drawing/report/probe/capture only, existing root/measurement producer reused;
   ordinary calls and omitted ordinary group arrays preserved. No execution/API
   validation change, new runtime flag, manual layout or successor reviewer pass.
2. Anti-green-theater: removing RenderSurface's groupLive feed fails real browser
   runtime text coverage (/tmp/p3t17-runtime-feed-mutation-red.log). Removing only
   nodeContexts fails actual RenderSurface/ReactFlow ownership-indicator DOM
   (/tmp/p3t17-surface-context-mutation-red.log). Dropping the badges case fails the
   exact browser probe-name sink (/tmp/p3t17-required-case-mutation-red.log).
   Dropping original-owner conversion and winner-font vocabulary fails selection,
   explicit-null and fallback assertions (/tmp/p3t17-context-font-mutation-red.log).
   All mutations restored. Browser negatives independently erase ownership/text/
   connector/marker or alter runtime row position/clipping/content/middle segments;
   none relies on the successor reviewer rejecting groups.
3. Quality: shared text-readability checks, typed actual observations, canonical
   reversible IDs, no fabricated connector ordinal in serialized data, and no
   accepted missing-input no-op. The existing viewport test's DOM-only polling could
   return before passive anchor effects; its settled helper now flushes those effects
   before the unchanged exact assertions, rather than changing production timing.

Final verification after restoration/refactoring:
- specified Vitest selector: 27 files / 496 tests pass; npx tsc --noEmit passes
  (/tmp/p3t17-ui-final.log).
- go test ./domain/layout -count=1 passes (12.166s);
  focused report/names/captured-document tests pass under -race
  (/tmp/p3t17-domain-final.log, /tmp/p3t17-wire-race-final.log).
- make ui rebuilds production embedded assets (/tmp/p3t17-ui-build-final.log).
- exact browser-required selector executes four named top-level tests and all their
  subtests, with no skips, passing in 152.505s (/tmp/p3t17-browser-final.log).
  It covers complete actual group arrays/paint/runtime names, restored static text,
  immutable full-canvas PNG hashes/dimensions, title-only missing font, cancellation,
  concurrent isolated captures and ordinary/zero-group omitted report fields.
- gofmt and git diff --check pass. P3T18 owns genuine passing rendered review and
  raster-success fixture regeneration; this checkpoint claims actual capture only.
