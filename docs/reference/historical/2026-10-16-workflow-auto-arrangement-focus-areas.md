# Focus Areas — Workflow Auto-Arrangement, reviewed parallel groups (branch over `4c785c9`)

Each area is one **behaviour** this branch promises. It is traced from its entry point to
its sink, across every layer in between. The branch is commits `26f8d31..9a02802`:
P3T16 (recursive group placement, routing, preflight), P3T17 (one group drawing path,
measurement and capture), P3T18 (group reviewer, evidence and stored results), P3T19
(runtime inputs, child layouts, route mounts) and P3T20 (composition-root and release
proof). Rows are ordered highest risk first, ranked by consequence.

Review 1 asks: does the path exist and is it fed? Review 2 asks: how does it fail?
The merge decisions behind these four areas are recorded at the end of this file.

**Budget:** 4 single-system areas + 5 paired seams; 2 × 4 + 5 = **13** implied minimum passes ≤ 500 × 0.35 = **175**. No seam is deferred.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | serve revision-bound stored results and retries without losing the best layout | high | [x] | [x] |
| 2 | review actual group ink and runtime states in one captured render job | high | [x] | [x] |
| 3 | display reviewed root and child scenes with truthful status, live state and original ownership | high | [x] | [x] |
| 4 | arrange and refine complete measured group geometry without changing the manifest | med | [x] | [x] |

---

### Area 1 — serve revision-bound stored results and retries without losing the best layout

- **Promise:** P3T18.2 and the P3T15 format transition. Both revision queries return the
  same truthful result. A v2 group result is reviewed only if it is compatible with the
  saved revision. Anything else (v1 history, wrong contract, missing ownership, corrupt
  or missing referenced result, store outage) yields explicit unreviewed fallback or
  attention with actionable Retry. A worse retry keeps the superior best, and
  cancellation or later-attempt errors keep the prior complete geometry. No new SQL
  columns and no separate store.
- **Entry:** `adapter/http/layout_handler.go:LayoutHandler.GetWorkflowLayout` / `GetInstanceLayout` → `domain/layout/query.go:QueryService.WorkflowLayout` / `InstanceLayout` → `domain/layout/reader.go:Reader.View`. The retry entry is `layout_handler.go:RetryWorkflowLayout` / `RetryInstanceLayout` → `query.go:QueryService.retry` → `requester.go:Requester.retry`.
- **Sink:** the `View` JSON (`Geometry`, `Evidence`, `Groups`, `DisplayStatus`, `RetryEligible`, `Provenance`) at `reader.go:Reader.View` (the compatibility gate is at :269). On the write side, the best-result pointer chosen by `domain/layout/worker.go:storedBetter` (:546), plus the `domain/layout/coordinator.go:better` retention inside one job.
- **Files:**
  - changed: `domain/layout/geometry.go` (`FormatV2`, `Validate`, `Attachment.UnmarshalJSON`, `Compatible`, `arrowBounds`), `domain/layout/contract.go` (`ContractVersion = arranged-renderer/2`, eight group contract fields), `domain/layout/group_validation.go` (`validateGroupGeometry`), `domain/layout/group_route.go:groupElementBoxes`, `domain/layout/layouttest/{copy,fakes,store_contract,group_fixtures}.go`
  - changed tests: `domain/layout/reader_group_results_test.go`, `domain/layout/worker_group_results_test.go`, `domain/layout/group_geometry_wire_test.go`, `adapter/filesystem/layout_cache_test.go`, `domain/layout/layouttest/fakes_groups_test.go`, `cmd/arbitron/layout_groups_reviewer_e2e_test.go`, `cmd/arbitron/layout_group_surfaces_test.go` (`TestLayoutGroupSurfaces_IncompatibleStoredGroupsRemainFallback`)
  - unchanged on path: `domain/layout/reader.go`, `domain/layout/query.go`, `domain/layout/requester.go`, `domain/layout/worker.go`, `domain/layout/coordinator.go` (`better`, `fail` paths), `domain/layout/store.go`, `adapter/sqlite/layout_repo.go`, `adapter/filesystem/layout_cache.go`, `adapter/http/layout_handler.go`, `adapter/mcp/tools_layout.go`, `domain/workflow/revision_stamper.go` (`retained`)
- **Folded in:**
  1. Ordinary (non-group) pre-upgrade history. `geometry.go:Validate` now rejects `FormatV1` (:179), so every stored ordinary v1 result becomes incompatible after upgrade. Check that `Reader.View` shows it as needs-attention with Retry, never as reviewed, and that nothing auto-re-arranges an untouched workflow.
  2. `adapter/sqlite/layout_repo.go:DisplayedLayout` (:50) returns an error for an incompatible v1 prior. Check that `domain/workflow/revision_stamper.go:retained` (:126) only logs it and falls back to `Presentation.Retained`, so saving an existing ordinary workflow still succeeds.
  3. `geometry.go:Attachment.UnmarshalJSON`: an absent `nodeId` must reject, and an explicit empty synthetic `nodeId` must decode. Check the SQLite and cache round trips with both.
  4. `layouttest/copy.go`: the memory store's shallow group copy was fixed. Check that stored group arrays cannot alias caller memory (`TestStoreContract` group cases).
  5. Carried gap G3 (`coordinator.go:Run` clone-error returns at :236/:258 skip `best = better(best, cand)`).
- **Review 1 — wiring lens:**
  1. `Reader.View` gates the stored result with `Compatible(result.Geometry, graph)` at `reader.go:269`. `graph` comes from `BuildGraph(wf)` of the authorized revision or snapshot, and `v.Groups = graph.Groups` (:188) comes from the same immutable workflow, not from Store/cache. Grep `v.Groups` and confirm that no current-workflow lookup feeds a historical instance read.
  2. The same `Compatible` gate is consulted at every read and retain site: `reader.go:107` (retained geometry), `reader.go:167` (outage cache), `requester.go:182`, `worker.go:310` (cache publish) and `adapter/sqlite/layout_repo.go:50`. Show each with a test that goes red when the gate is removed.
  3. Retry reaches a real new generation. `RetryWorkflowLayout` → `QueryService.retry` → `Requester.retry` → the Worker re-runs and `storedBetter` keeps the superior best. `TestWorker_GroupRetainedAfterLaterErrorsAndWorseRetry` and `TestReader_GroupStoredTruthAndRetry` must assert the Reader's served generation, not only the Store row.
  4. Group arrays and evidence survive the real stores unchanged. `TestLayoutCache_GroupJSONRoundTrip` and the SQLite store contract compare full group arrays. `TestLayoutGroups_ReviewerAndStoreTruth` (cmd/arbitron) reads them through SQLite, Reader and both authorized queries with the real root (`cmd/arbitron/layout_wiring.go:141,145`).
  5. No test double is installed at the root: `grep -rln layouttest cmd/ adapter/ --include=*.go` must show no non-test file.
- **Review 2 — failure lens:**
  1. For each of v1, wrong contract, missing geometry, missing ownership, missing or corrupt result, missing evidence, wrong generation and outage, there must be a `Reader.View` test asserting `Available=false`, the specific `FailureReason`, `RetryEligible` and the fallback `Provenance`. Ordinary v1 history needs its own case: `reader_group_results_test.go` covers the group form only. Confirm that `reader_test.go` or add one.
  2. Outage versus corruption: `cachedFallback` (:161) serves cache only on `ErrStoreUnavailable`, and never to hide a corrupt authoritative row. There must be a test where a corrupt row plus a valid cache still shows needs-attention.
  3. A worse retry, a cancelled retry and a later-attempt render error each keep the prior compatible best, with its own findings and evidence (`TestWorker_GroupRetainedAfterLaterErrorsAndWorseRetry`). Gap G3: the two `cloneGroupGraph` error returns in `Coordinator.Run` drop the current candidate. Fix them or show they are unreachable with a test.
  4. Stale generation: an old-generation completion arriving after a retry must not replace the newer pointer (the fenced `Complete`). Cite the test that goes red.
  5. Invalid saved groups (`ErrInvalidGroups`) stay inspectable with a failed status and Retry (`query.go:74`). Malformed basic nodes or edges still reject, and authoring validation is unchanged (`TestQueryService_InvalidSavedGroupsRemainReadableAndRetryable`).
  6. Concurrency: run the reader and worker group tests under `-race`, since the stored group arrays are deep-copied (copy.go).
- **Seed evidence:**
  - `grep -rn "Compatible(" --include=*.go domain adapter | grep -v _test` → `reader.go:107,167,269`, `requester.go:182`, `worker.go:310`, `adapter/sqlite/layout_repo.go:50`.
  - `grep -n "FormatV1" --include=*.go -r . | grep -v _test` → `geometry.go:13` (constant kept for history decoding), `adapter/renderreview/reviewer.go:148`. No production producer emits v1. `route.go:283` and the group arranger emit `FormatV2`.
  - `revision_stamper.go:126-129`: a `DisplayedLayout` error is logged (`read displayed layout fallback`) and falls through to `Presentation.Retained`. The save is not blocked.
  - `coordinator.go:235-238` and `:256-259`: `cloneGroupGraph` error → `return fail(...)` with no `better(best, cand)`. This is the carried gap G3.
  - `grep -rln layouttest cmd adapter domain --include=*.go | grep -v _test.go` → only `domain/layout/layouttest/*` itself. Root wiring is `cmd/arbitron/layout_wiring.go:64` (`NewBrowserRenderer`), `:76` (`NewReadabilityReviewer`), `:141` (`NewCoordinator`), `:145` (`NewWorker`).

### Area 2 — review actual group ink and runtime states in one captured render job

- **Promise:** P3T17.1/17.2 (one group drawing path, actual report) and P3T18.1 (reviewer
  integrity, actual ink). The installed BrowserRenderer draws v2 group geometry through
  the same components the dashboard uses. It reports actual DOM text, bounds, paint, SVG
  segments, markers and crossing gaps, and the reviewer then requires full coverage
  and real pixels. A clean report or a present PNG alone is not evidence.

  P3T17.2 (deterministic runtime producer) and P3T18.1
  (`TestReadabilityReviewer_GroupRuntimeProbes`). With no live instance, the render
  surface builds runtime cases solely from the immutable captured WorkflowGroups, renders
  each through the real ArrangedGraph and snapshots every animation phase. The reviewer
  requires exactly `GroupProbeNames(req.WorkflowGroups)`. Static geometry stays fixed.
  Overlay text changes only within its measured rows and vocabulary, and a redacted
  title shows "hidden dependency" without identity or links.
- **Entry:** `domain/layout/coordinator.go:Coordinator.Run` → `adapter/renderreview/browser.go:BrowserRenderer.Render` → `dashboard/src/render/RenderSurface.tsx:runRenderJob`. That single job collects the static baseline, mounts `groupProbes.ts:buildGroupProbeCases`, compares each phase with the baseline via `groupReport.ts`, restores the graph and produces the final report and PNGs.
- **Sink:** `adapter/renderreview/reviewer.go:ReadabilityReviewer.Review` combines `checkCoverage`, `checkGroupCapture`, `checkGroups` and `reviewer_checks.go:checkStates` into one `Review{Rendered, Findings, Evidence}`. `coordinator.go:Coordinator.Run` uses that verdict and all findings for `cand.Score` and `cand.Passed`; a probe has no separately accepted result.
- **Files:**
  - changed Go: `adapter/renderreview/reviewer.go` (`checkIdentity`), `reviewer_groups.go` (`checkGroupCoverage`, `checkGroups`, `groupFill`), `reviewer_groups_capture.go` (`checkGroupCapture`, `outlineInk`, `groupFillInk`, `routeInk`, `gapInkClear`), `reviewer_checks.go` (`expectedRouteSegments`, the `checkCrossings` advisory), `reviewer_capture.go`, `domain/layout/render.go` (`RenderedGroup*`, `RenderReport` group arrays), `domain/layout/review_geometry.go` (`ReviewRequest.ValidateGeometry`)
  - changed TS: `dashboard/src/render/RenderSurface.tsx`, `src/render/report.ts` (`collectGroupElements`, `collectGroupConnectors`, `collectGroupCrossings`), `src/render/job.ts`, `src/graph/arranged/{ArrangedGraph,ArrangedNode,ReviewedGroupElement,GeometryHandles,RoutedEdge}.tsx`, `buildArrangedFlow.ts`, `groupVocabulary.ts` (`geometryTexts`), `src/graph/nodes/GroupContainerNode.tsx`, `src/graph/groupLayout.ts`, `dashboard/render.html`, rebuilt `adapter/dashboard/dist/*`
  - changed tests: `adapter/renderreview/reviewer_group_{evidence,raster,contacts}_test.go`, `reviewer_groups_test.go`, `surface_groups_test.go`, `reviewer_browser_test.go`, `domain/layout/group_render_test.go` (`TestRenderReport_GroupWireFields`), `domain/layout/review_geometry_test.go`, `dashboard/src/render/report.groups.test.ts`, `src/graph/arranged/ArrangedGraph.groups{,.dom}.test.tsx`, `cmd/arbitron/layout_groups_e2e_test.go` (`TestLayoutGroups_RealRootSavedRevisionReachesReviewedSink`), genuine fixtures under `adapter/renderreview/testdata/readability/group-*`
  - unchanged on path: `adapter/renderreview/tiles.go`, `adapter/renderreview/assets.go`, `domain/layout/coordinator.go` (`reviewFindingsProblem` :437, `evidenceProblem`), `domain/layout/coordinator.go:HasRenderedEvidence`, `Makefile` (`test-render`), `.github/workflows/layout.yml`

  - changed: `dashboard/src/render/groupProbes.ts`, `src/render/groupReport.ts`, `src/render/RenderSurface.tsx`, `src/graph/arranged/groupProbe.contract.json`, `src/graph/arranged/groupVocabulary.ts` (`GROUP_RUN_STATES`, `GROUP_TASK_STATES`, `groupOverlayItems`), `src/graph/arranged/ReviewedGroupElement.tsx` (`GroupOverlay`, redacted title :25), `domain/layout/group_render.go`, `adapter/renderreview/reviewer.go`, `reviewer_checks.go` (`checkStates`)
  - changed tests: `dashboard/src/render/groupProbes.test.ts`, `src/render/job.groups.test.ts`, `domain/layout/group_render_test.go` (`TestGroupProbeNames`), `adapter/renderreview/reviewer_group_probes_test.go` (`TestReadabilityReviewer_GroupRuntimeProbes`, `...RuntimeProbeOwnership`, `...RuntimeProbesActualBrowser`)
  - unchanged on path: `dashboard/src/graph/groupLayout.ts` (`GroupLiveSummary`, `NodeContextInfo` types), `dashboard/src/render/report.ts` (`stabilityIssues`, `readabilityIssues`), `RenderSurface.tsx:animationPhases`, `domain/layout/group_geometry.go:groupTextItems` (the measured vocabulary)
- **Folded in:**
  1. `Makefile:test-render` (:65) stays unfiltered over `./adapter/renderreview/ ./cmd/arbitron/` with `ARBITRON_REQUIRE_BROWSER=1`, and `.github/workflows/layout.yml:42` runs `make test-all`. Check that both new P3T20 test names run there with no skip.
  2. `adapter/dashboard/dist/*` must equal a fresh `make ui` build. A stale embedded bundle would let Go tests review an old surface.
  3. `GroupContainerNode.tsx` / `RoutedEdge.tsx` reviewed variants: no ellipsis, 9px text, opacity or double border in reviewed mode (P3T17.1 GREEN).
  4. Font readiness covers the full static and overlay vocabulary (`groupVocabulary.ts:geometryTexts`, `RenderSurface.tsx:93`). Removing the only font subset a winner or task label needs must reject capture (`TestBrowserRenderer_GroupTitleFontAndCancellation`).
  5. The ordinary v2 migration in the reviewer: ordinary reports omit group arrays (`render.go` `omitempty`, `RenderSurface.tsx:104`), and the regenerated ordinary fixtures keep the v1 negative control (`TestBrowserRenderer_GroupConcurrentCaptureAndOrdinaryReport`).
  6. `group_render.go:24-27` sorts group IDs by UTF-16 to match JS default sort, while `groupProbes.ts:65` uses `[...groups.order].sort()`. Check astral-plane IDs in `TestGroupProbeNames` against `groupProbe.contract.json`.
  7. `groupProbe.contract.json`: the shared golden fixture distinguishes run states from task statuses.
- **Review 1 — wiring lens:**
  1. Captured ownership is fed. `coordinator.go:235` and `:256` clone `g.Groups` into both `RenderRequest.WorkflowGroups` and `ReviewRequest.WorkflowGroups`. `browser.go:renderJobDocument` marshals the whole request (`json:"workflowGroups"`) into the page. `TestRun_WorkflowGroupsAreCapturedAndIndependentlyFed` and the P3T20 mutation (`/tmp/p3t20-root-feed-red.log`) must fail when either one is nil.
  2. The report is actual, not echoed. `RenderSurface.tsx:104` calls `collectGroupElements/Connectors/Crossings(geo, ctx)`. Read `report.ts:296-400` and confirm that every observed field (textContent/Range, computed paint, SVG `M/L` segments, dash, marker outline, gap) comes from the DOM, and none is copied from `geo`.
  3. The reviewer consults every group check on the live path: `reviewer.go:69-91` → `checkGroupCoverage`, `checkGroupCapture`, `checkGroups`, plus `checkIdentity`'s `ValidateGeometry` (:155). Each needs a test that goes red when it is removed (`TestReadabilityReviewer_GroupsRequireCompleteActualEvidence`, `TestReadabilityReviewer_GroupRasterMutationsBlock`).
  4. The happy path reaches the sink through the real root. `TestLayoutGroups_RealRootSavedRevisionReachesReviewedSink` must reach `StatePassed` within five attempts through `initDomain` for branches, workflows and nested groups, assert the SQLite review evidence and hash the PNGs. Check that it runs under `make test-render` with no skip.
  5. Reviewer group findings are accepted by `coordinator.go:reviewFindingsProblem` (:437). They must name real group node IDs (no element ID in `NodeIDs`, no fabricated ordinal in `EdgeOrdinals`), otherwise every group finding becomes a render mismatch with `Rendered=false`. Cite the test.
  6. The cases are fed by the real captured map: `RenderSurface.tsx:138` uses `job.workflowGroups` from the Go `RenderRequest.WorkflowGroups` (the captured-ownership wiring check above). `job.ts:groupsForJob` rejects absent ownership before group capture. Separately remove a probe while preserving valid captured ownership and require `reviewer.go:checkCoverage` to block (`TestReadabilityReviewer_GroupRuntimeProbes`); test these distinct failure boundaries.
  7. Each case's `groupLive` / `nodeContexts` / `selectedNodeIds` reach the actual ArrangedGraph DOM. Removing the `groupLive` feed fails real browser runtime text coverage (`/tmp/p3t17-runtime-feed-mutation-red.log`), and dropping `nodeContexts` fails the ownership indicator DOM (`RenderSurface.tsx:RenderSurface` → `buildArrangedFlow.ts:buildArrangedFlow`).
  8. Name parity: `GroupProbeNames` (Go) and `buildGroupProbeCases` (TS) produce the identical ordered list for branches, workflows and nested groups, including the per-task winner names (`TestGroupProbeNames`, `groupProbes.test.ts`, shared golden).
  9. The reviewer requires the exact set: `reviewer.go:65-68` appends `GroupProbeNames` only when identity has not already blocked. Omitting one case must block (`/tmp/p3t18-probe-red.log`).
  10. After probing, the static graph is restored and fonts settled before capture (`RenderSurface.tsx` `finally` → `mount({})` → `ensureFonts`). The captured PNG must show static text, not the last probe.
- **Review 2 — failure lens:**
  1. Missing or mismatched ownership: nil `WorkflowGroups` with a `parallel group` node, a wrong parent, the wrong nested order or a wrong continuation must block in `checkIdentity` / `ValidateGeometry` (`TestReviewRequest_GroupHeaderCannotUseOrdinaryBoxes`, `/tmp/p3t18-captured-map-red.log`).
  2. Ink, not metadata: erase only a container or lane border, a footer label, a card fill, a synthetic middle segment, a marker or a crossing gap, recompute valid hashes and keep the DOM report clean. Each must block (`TestReadabilityReviewer_GroupRasterMutationsBlock`). Zero border or transparent fill must not turn a paint check into a no-op (`reviewer_groups.go:groupFill`).
  3. Evidence errors: a missing, unreadable or hash-mismatched PNG is `ErrEvidenceUnavailable`, never a pass. A canvas over `maxViewportPx` / `maxTiles` (`browser.go:354-357`, `validateRenderRequest`) fails the rendering stage explicitly and retains the unreviewed candidate.
  4. Gap G1: a long auto-wrapped body label fails containment in the captured one-attempt case (`TestReadabilityReviewer_GroupActualWrapOverflowFailsClosed`). Decide whether refinement converges through `Coordinator.Run`, and pin it with a test (see paired row 2).
  5. Cancellation and concurrency: cancelled render or review contexts return an error, and concurrent captures stay isolated (`TestBrowserRenderer_GroupConcurrentCaptureAndOrdinaryReport`, `TestCoordinator_GroupCancellation`). Run the renderreview group tests under `-race` with the browser required.
  6. A false `nodeBoxesStable` / `labelsReadable` flag in any group case blocks review with the case name and real group owner in the finding (`TestReadabilityReviewer_GroupRuntimeProbeOwnership`).
  7. Clip only a live row, obscure only a winner, move a frozen-status span 3px inside the aggregate OverlayBox, or disclose a redacted dependency: each must block in the real browser (`/tmp/p3t17-row-red.log`, `TestReadabilityReviewer_GroupRuntimeProbesActualBrowser`).
  8. Dynamic text is compared with the measured case vocabulary and rows. It must not be compared with an empty static baseline, and the redacted title must not be required to stay unchanged (`groupReport.ts:groupProbeIssues`).
  9. Ordinary geometry requires no group cases, so `GroupProbeNames(nil)` and empty `Order` return nil (`group_render.go:12-14`).
  10. Animation phases: snapshots are taken on each phase (`RenderSurface.tsx:animationPhases`), so a transient overflow during a CSS transition is caught, not only the settled frame.
- **Seed evidence:**
  - `grep -n "WorkflowGroups" domain/layout/coordinator.go` → `:235` render, `:256` review. `adapter/renderreview/reviewer.go:67` (`GroupProbeNames(req.WorkflowGroups)`), `:138-150` (`groupsMatch`, v1 group block).
  - `grep -n "collectGroup" dashboard/src/render/RenderSurface.tsx` → `:104`, guarded by `geo.groupElements?.length`. A group geometry with empty `groupElements` emits no group arrays, so `checkGroupCoverage` must still demand them from the geometry.
  - `adapter/renderreview/browser.go:419 validateRenderRequest` does not check `WorkflowGroups`. A nil map reaches the page, where `job.ts:groupsForJob` rejects it; independently supplied review requests are also rejected by `reviewer.go:checkIdentity`. Do not claim a successful no-probe capture for missing ownership.
  - `grep -n "test-render" Makefile` → `:47` (`test-all` dependency), `:65-66` (unfiltered packages). `.github/workflows/layout.yml:42` → `make test-all`.
  - `adapter/renderreview/reviewer_group_fixture_test.go:TestGenerateGroupReadabilityFixtures` is skipped unless `ARBITRON_UPDATE_RENDER_FIXTURES=1`, so normal runs do not regenerate fixtures. Its success labels contain explicit `\n` breaks, while `surface_groups_test.go:24` uses one long line that fails review (G1).

  - `grep -rn "buildGroupProbeCases" dashboard/src --include=*.ts* | grep -v test` → `render/RenderSurface.tsx:15,138`, the only production caller.
  - `grep -rn "GroupProbeNames" --include=*.go adapter domain | grep -v _test` → `adapter/renderreview/reviewer.go:67`, the only production consumer.
  - `groupProbes.ts:21-27`: every case seeds `state: 'running'` and all tasks `running`, then overrides one dimension. The badges case sets winner, continuingIndependently and cancelRequested at once, with frozen `unfinished` (:49-53).
  - `ReviewedGroupElement.tsx:80`: a live key with no measured alternative renders `unsupported group overlay: <key>`, an unmeasured row. Probes cover only the enumerated vocabulary (see paired row 3).
  - Critic trace: `RenderSurface.tsx:runRenderJob` uses the same `collect()` for baseline and probe snapshots, and returns only after `finally` restores the static graph. `reviewer.go:ReadabilityReviewer.Review` gates both raster and state checks on common identity/coverage and returns one verdict. These cannot be judged as independent fed paths.

### Area 3 — display reviewed root and child scenes with truthful status, live state and original ownership

- **Promise:** P3T19.1 and the tile-placement half of P3T19.2. Each visible workflow or
  spawn child with its own grouped snapshot gets its own reviewed layout from the
  authorized instance endpoint. It is polled under a bounded scheduler, cancellation
  really aborts the fetch, and old responses or retries cannot settle a new epoch. The
  tile is translated below the preceding extents with `GroupRowGapPx`, and a failing
  child never replaces the root or another child. Redacted or empty contexts make no
  fetch and show no identity.

  P3T19.2/19.3 and P3T20.2. WorkflowReview draws joined `LayoutView.groups`
  statically, and the instance route draws the same reviewed geometry with real
  `buildAggregateView` live maps. Runtime styling changes no reviewed geometry. Long
  activity, launch errors, waiting reasons and active lists stay in GroupPanel and
  callouts without graph growth. Selection of a branch-owned body keeps its original
  owner. Follow and recenter and the callout endpoints use actual rendered dimensions and
  qualified IDs. Nil, v1 or wrong ownership stays an explicit fallback, never a reviewed
  badge.
- **Entry:** `dashboard/src/routes/ExecutionView.tsx:GraphView` joins `nestedGraphs.ts:nestedContexts` / `useInstanceLayoutTiles.ts:useInstanceLayoutTiles` with `aggregateState.ts:buildAggregateView` into the same root/child `LayoutTile[]`. `WorkflowReview.tsx:ReviewPage` is the static revision entry, with ownership from its joined `LayoutView`.
- **Sink:** `ArrangedGraph.tsx:ArrangedGraph` → `composeArrangedTiles.ts:composeArrangedTiles` → `buildArrangedFlow.ts:buildArrangedFlow` produces the one ReactFlow scene, including live overlays, qualified selection and actual bounds used for follow/recenter and callouts. `ExecutionView.tsx:GraphView` also publishes each child's `LayoutStatusPanel` and binds its GET/abort/retry actions to that same child identity.
- **Files:**
  - changed: `dashboard/src/hooks/useInstanceLayoutTiles.ts`, `dashboard/src/api/client.ts`, `dashboard/src/graph/arranged/composeArrangedTiles.ts`, `dashboard/src/graph/arranged/ArrangedGraph.tsx` (tiles prop, per-tile `resolveDisplayGeometry` :121, font epochs), `dashboard/src/routes/ExecutionView.tsx` (tile assembly :141-152, status rail :260-270), `dashboard/src/routes/ExecutionView.module.css`, `dashboard/src/graph/nestedGraphs.ts` (`nestedContexts`), `dashboard/src/graph/useGraphLayout.ts` (exported `computeGraphLayout` for fallback tiles)
  - changed tests: `dashboard/src/hooks/useInstanceLayoutTiles.test.ts`, `src/api/client.cancellation.test.ts`, `src/graph/arranged/composeArrangedTiles.test.ts`, `src/routes/ExecutionView.groups.test.tsx`, `src/routes/ExecutionView.layout.test.tsx`, `cmd/arbitron/layout_group_surfaces_test.go` (`TestLayoutGroupSurfaces_RealGroupedChildTilesAndIndependentOutcome`)
  - unchanged on path: `dashboard/src/hooks/useLayoutStatus.ts` (`POLL_INTERVAL_MS`, `MAX_BACKOFF_MS`), `dashboard/src/graph/arranged/LayoutStatusPanel.tsx`, `dashboard/src/graph/arranged/validate.ts:resolveDisplayGeometry`, `dashboard/src/hooks/useGraphState.ts`, `domain/engine/graph_state.go:GraphStateReader` (redaction of contexts), `adapter/http/layout_handler.go:GetInstanceLayout` / `RetryInstanceLayout`

  - changed: `dashboard/src/routes/WorkflowReview.tsx`, `src/routes/ExecutionView.tsx`, `src/graph/arranged/ArrangedGraph.tsx`, `buildArrangedFlow.ts`, `ReviewedGroupElement.tsx`, `RoutedEdge.tsx`, `GeometryHandles.tsx`, `src/graph/arranged/validate.ts` + `groupValidate.ts` (`groupGeometryIssues` → reviewed vs fallback decision), `src/graph/aggregateState.ts` (`buildGroupLive` :229), `src/components/GroupPanel.tsx`, `src/graph/CalloutConnector.tsx`, `src/graph/nodes/{ArbitronNode,GroupContainerNode}.tsx`, `src/graph/groupLayout.ts`
  - changed tests: `dashboard/src/routes/WorkflowReview.test.tsx`, `ExecutionView.groups.test.tsx`, `ExecutionView.layout.test.tsx`, `GraphView.test.tsx`, `GraphView.loops.test.tsx`, `QueueView.livepane.test.tsx`, `src/graph/CalloutConnector.arranged.test.tsx`, `src/graph/arranged/ArrangedGraph.groups.test.tsx`, `cmd/arbitron/layout_group_surfaces_test.go` (`TestLayoutGroupSurfaces_WorkflowAndInstanceParity`, `...ResolutionCalloutAnchorsReviewedFooter`, `...CompletedInstanceReceivesLateReview`, `...RevisionBoundOwnershipBeforeGeometry`, `...ReaderOwnershipMatchesRenderedRevision`), `cmd/arbitron/layout_surfaces_e2e_test.go` (inspectors), `cmd/arbitron/layout_font_recovery_test.go`
  - unchanged on path: `dashboard/src/graph/arranged/LayoutStatusPanel.tsx`, `src/hooks/useLayoutStatus.ts`, `src/hooks/useFollowActiveNode.ts`, `src/hooks/useGraphState.ts`, `domain/engine/graph_state_groups.go` (panel and task live status producer), `domain/layout/reader.go` (`View.Groups`)
- **Folded in:**
  1. `client.ts:226` `getLayout(id, signal?)` forwards `signal` through `request<T>(...,{signal})`. Check `client.cancellation.test.ts` with a pending fetch.
  2. `nestedGraphs.ts:46 nestedContexts` gained `kind === 'workflow' || 'spawn'` and `graph.nodes.length > 0`. Check that a branch context, a redacted context and an empty graph each produce no tile and no GET.
  3. `ExecutionView.module.css`: status-rail titles, findings and Retry sit outside graph ink.
  4. Dead code G2: `nestedGraphs.ts:composeLiveGraph` (:58) and `withOriginalIds` (:111) lost their last caller when the tiles replaced the merged relayout.
  5. `WorkflowReview.tsx:311` `staticGroups = joinedView?.groups ?? groupLayout.graph` and `:366` hiding "Group layout unavailable" when joined groups exist. Check that static inspection works when the current group-analysis endpoint fails.
  6. `GroupPanel.tsx` / `CalloutConnector.tsx`: long details stay outside graph ink, and the callout anchors the reviewed footer (`TestLayoutGroupSurfaces_ResolutionCalloutAnchorsReviewedFooter`).
  7. Client font failure uses the existing "Reload reviewed fonts" with zero Retry POSTs and an unchanged durable result (`cmd/arbitron/layout_font_recovery_test.go`).
- **Review 1 — wiring lens:**
  1. The production caller feeds real contexts. `ExecutionView.tsx:140` derives `childContexts` from `graphState` (GraphStateReader's authorized snapshots) in `contexts` order, `:141` passes them to `useInstanceLayoutTiles`, and `:149-150` joins each to `childLayouts.views.get(c.instanceId)`. A root/route feed set to empty must fail the child-tile DOM test (`/tmp/p3t19-view-mutation-red.log`).
  2. `useInstanceLayoutTiles.ts:92` uses the real `api.instances.getLayout(e.id, signal)`, and `:94` rejects a `savedRevision` that differs from the child snapshot's revision. Grep for any production call site that passes a hardcoded or empty revision.
  3. Retry is wired per tile. `ExecutionView.tsx:268` calls `childLayouts.retry(c.instanceId)` → `retryCurrent` (:119) → `api.instances.retryLayout(id, displayed.savedRevision)`. It installs `e.minimum = ack.jobGeneration` (:129) before refetching, and touches no other tile or the root.
  4. Tiles reach the scene. `ArrangedGraph.tsx:157` calls `composeArrangedTiles(tiles, graphInstanceId)`. The child origin is below the preceding full extents plus `GroupRowGapPx`, and the root origin is zero (`composeArrangedTiles.test.ts`).
  5. End to end: `TestLayoutGroupSurfaces_RealGroupedChildTilesAndIndependentOutcome` (browser-required) asserts two independently polled child endpoints, distinct revisions and generations, and uniform translation through the real router.
  6. Live maps are fed by the real producer: `ExecutionView.tsx:126` builds `aggregate` from `graphState`, and `:145`/`:277` pass `aggregate.groupLive` / `aggregate.nodeContexts`. `aggregateState.ts:282-287` builds them from GraphState panels. The same maps passed as empty must fail the visible-status assertions (`/tmp/p3t19-live-mutation-red.log`, `/tmp/p3t20-live-red.log`).
  7. `buildArrangedFlow.ts:93-94` copies `groupLive[element.groupNodeId]`, `taskLive` and `nodeContexts[...]` into `ArrangedGroupElementData`. `:71` copies NodeContextInfo onto body nodes (ownership, independent, hidden), preserving explicit null and original IDs. `selectionFromNode` on a branch-owned body returns its original owner (`/tmp/p3t19-owner-mutation-red.log`).
  8. WorkflowReview draws from the joined revision: `groups={staticGroups}` and `view` come from the same `LayoutView`, with no current-workflow lookup for a historical revision.
  9. Parity end to end: `TestLayoutGroupSurfaces_WorkflowAndInstanceParity` compares every local node and group box, text, real ordinal, path, label, attachment, arrow, synthetic path and gap, revision, generation, digest and `data-layout-mode=arranged` across both embedded routes, and a completed instance updates when review finishes later.
  10. Follow and recenter in `ExecutionView.tsx:GraphView` use `activeBounds` and the real `useFollowActiveNode` viewport across all tiles' actual dimensions (`/tmp/p3t19-follow-mutation-red.log`). A mocked no-op `setCenter` does not establish this.
- **Review 2 — failure lens:**
  1. Bounded concurrency: queue more than four children and assert at most four GETs in flight (`MAX_CHILD_GETS` :9, `pump` :73). Removing a child mid-GET must release its slot (`reconcile` :149 `abort` + `release`).
  2. Epochs: an A→B→A context change, a late GET and a late retry acknowledgement must not settle the new entry (`live(e)`, `sequence` guards :93, :115). An abort is not an error and must not restart a removed context.
  3. Generation floor: after `RetryAck`, a stale terminal read with `jobGeneration < minimum` keeps polling (:108) instead of stopping. A regressing `resultGeneration` is ignored.
  4. In-band unavailable (:96-106) keeps only the same child's compatible geometry and backs off (`fail` :57-63). It must never borrow root geometry or another tile's geometry, and must mark the view unreviewed.
  5. Redaction: a redacted context (filtered at `nestedContexts`) makes no GET, and no child instance ID, link or request reaches the DOM. P3T20's org-session revocation case asserts this through the real page. Cite it.
  6. A fallback tile alone calls `computeGraphLayout` with its own partial retained positions. A compatible tile never does, and a missing child layout cannot run dagre over the root (`composeArrangedTiles.test.ts`).
  7. Nil, v1 or wrong ownership must yield an explicit fallback with no reviewed badge (`validate.ts:246` `groupGeometryIssues`, `TestLayoutGroupSurfaces_IncompatibleStoredGroupsRemainFallback`). A reviewed root status must not hide an unreviewed child.
  8. Unsupported live values: a status, frozen status, progress count above `len(tasks)` or winner outside the measured alternatives renders `unsupported group overlay` (`ReviewedGroupElement.tsx:80`). Show that this cannot occur for any `GroupRunState`, `GroupTaskStatus` or `GroupTaskLiveStatus` the engine emits (`domain/instance/group_run.go:52-58, 99-107`, `domain/engine/graph_state_groups.go:266-279`), or that it is reported (paired row 3).
  9. Row heights: `GroupOverlay` measures alternatives at runtime with `useLayoutEffect` (`ReviewedGroupElement.tsx:45-53`), and `heights[row] ?? 0` before measurement. A first paint with zero-height rows, or a client font that differs from the server's, must not overflow the fixed OverlayBox. Cite a test.
  10. Redaction in the DOM: a redacted task shows "hidden dependency" (:25), `childInstanceId` is null (`aggregateState.ts:242`), and no link or identity renders. Selecting during a pending retry keeps the original selection.
  11. Timing: route tests depend on settled ReactFlow and fonts. The P3T19 and P3T20 records report 5-second route timeouts under load, so check that no assertion was weakened and that waits observe the sink.
- **Seed evidence:**
  - `grep -rn "useInstanceLayoutTiles" dashboard/src --include=*.tsx | grep -v test` → `routes/ExecutionView.tsx:15,141`, the only production caller.
  - `grep -rn "composeLiveGraph\|withOriginalIds" dashboard/src` → definitions only (`nestedGraphs.ts:58`, `:111`): no production or test caller. Before the branch, `ExecutionView.tsx:29,140` imported it (`git show 4c785c9:dashboard/src/routes/ExecutionView.tsx`). Gap G2.
  - `useInstanceLayoutTiles.ts:164`: unmount aborts and releases every entry. `:146-158` `reconcile` is incremental (only changed ids are removed or added), so adding one child does not restart the others.
  - `ExecutionView.tsx:146`: with no child contexts and no compatible root geometry, `tiles` is `undefined` and the legacy path runs. With root geometry, the root always goes through `composeArrangedTiles`.

  - `grep -rn "groupLive\|nodeContexts" dashboard/src --include=*.tsx | grep -v test` → `routes/ExecutionView.tsx:145,151,277`, `graph/arranged/ArrangedGraph.tsx:42-43,72,162,180`. No production call site passes a literal `{}`.
  - `WorkflowReview.tsx:311,333,366,405`: the joined `view.groups` takes precedence over the client `groupLayout.graph`.
  - `aggregateState.ts:240-262`: `buildGroupLive` maps panel `liveStatus` and `frozenStatus` verbatim, and `winner: panel.winner === t.key`. The vocabulary (`group_geometry.go:193`, `groupVocabulary.ts:6-7`) lists the same eight run states and ten task statuses as `domain/instance/group_run.go`.
  - `ArrangedGraph.tsx:78,121` and `composeArrangedTiles.ts:46` all decide reviewed vs fallback via `resolveDisplayGeometry` (`validate.ts:360`).
  - Critic trace: `ExecutionView.tsx:GraphView` puts `childLayouts.views`, `local.groupLive` and `local.nodeContexts` into each tile; with tiles present, `ArrangedGraph.tsx:ArrangedGraph` disables the direct `arrangedFlow` path and uses `composeArrangedTiles`. Selection and follow consume that scene's qualified nodes. Polling/placement and live-route review therefore require the same functions.

### Area 4 — arrange and refine complete measured group geometry without changing the manifest

- **Promise:** P3T16. Innermost groups are laid out as atomic rectangles. Task lanes go
  left to right in author order, the header sits above all lanes, the footer below their
  complete extents, and continuations below the footer, with no sequential column wrap.
  Real and synthetic routes share measured obstacles, and mixed crossings count in the
  score. Preflight checks ownership, nesting, coverage, ports, clearance and
  reservations, and a blocking finding drives a changed candidate through `Refine`.
  Ordinary workflows keep the ordinary arranger, now emitting v2 with header
  `ElementID`s.
- **Entry:** `domain/layout/coordinator.go:Coordinator.Run` (:142) → `c.renderer.Measure` (:194, the real `BrowserRenderer.Measure` with `groupTextItems` from `measure.go:72`) → `domain/layout/arrange.go:Arrange` → dispatch to `group_layout.go:arrangeGroups` (:34-36) → `preflight.go:Preflight` → `group_preflight.go:preflightGroups`.
- **Sink:** `Candidate{Geometry, Preflight, Score}` (`coordinator.go:225-226`, `:288`), retained by the Worker in SQLite (`worker.go:214 StoredResult`) and read back by `Reader`, plus the next attempt's `Options` from `refine.go:Refine`.
- **Files:**
  - changed: `domain/layout/group_layout.go` (`arrangeGroups`, `groupPlacer.region/group/element`, `expectedGroupConnectors`, `translateGroupGeometry`), `group_route.go` (`routeGroupGeometry`, `searchGroupPath`, `findGroupCrossings`, `groupRouteCost`), `group_blocks.go` (`measureGroupBlocks`), `group_preflight.go` (`preflightGroups`, `groupContacts`), `group_validation.go`, `group_geometry.go` (`groupConnectorID`, `CrossingCount`, `groupTextItems`), `arrange.go`, `preflight.go`, `route.go` (FormatV2 and header ElementIDs for ordinary graphs), `geometry.go` (`arrowBounds`), `coordinator.go` (`ScoreOf(..., CrossingCount)` at :288, :336), `contract.go`; dashboard wire mirrors `src/graph/arranged/{types,validate,groupValidate}.ts`, `contract.json`, `groupText.contract.json`, `groupGeometry.v2.json`
  - changed tests: `domain/layout/arrange_group_test.go`, `coordinator_groups_test.go`, `group_geometry_wire_test.go`, `group_size_test.go`, `group_measure_test.go`, `geometry_test.go`, `dashboard/src/graph/arranged/groupGeometry.contract.test.ts`, `validate.test.ts`, `cmd/arbitron/layout_groups_e2e_test.go` (`TestLayoutGroups_RealBrowserMeasuresCapturedGroupText`)
  - unchanged on path: `domain/layout/refine.go`, `domain/layout/measure.go`, `domain/layout/group_size.go`, `domain/layout/size.go`, `domain/layout/graph.go:BuildGraph`, `domain/layout/group_geometry.go:cloneGroupGraph`, `domain/workflow/group.go` (`ParseGroupConfig`, `groupTaskKeyRE` :173), `adapter/renderreview/browser.go:Measure`, `dashboard/src/render/MeasureSurface.tsx`
- **Folded in:**
  1. `group_geometry.go:193` adds `not_started` to the footer run-state vocabulary, and `:211` adds `redactedTitle` as a node-label-style measurement for workflow cards. Check that both appear in `groupText.contract.json` and that sizing reserves them.
  2. `arrange.go:142-146` / `geometry.go:arrowBounds` refactor: canvas normalization still includes the full arrow triangle for ordinary routes (`geometry_test.go`).
  3. The Unicode task-key sizing box blocked in P3T16 (Won't Fix W1): check that `TestGroupSizing_ReservesCombinedLiveRows` covers long valid ASCII keys plus Unicode display strings, as its execution record claims.
- **Review 1 — wiring lens:**
  1. Dispatch is fed by real ownership: `arrange.go:34` dispatches only when `g.Groups != nil && len(g.Groups.Order) > 0`, and `g.Groups` comes from `BuildGraph` / AnalyzeGroups of the captured manifest. Removing dispatch must fail the persisted geometry sink (`/tmp/p3t16-dispatch-{unit,root}-red.log`).
  2. Measurements are real: `coordinator.go:194` calls the installed `BrowserRenderer.Measure` with `groupTextItems`, and `group_blocks.go:measureGroupBlocks` errors on missing, zero or nonfinite metrics instead of defaulting. `TestLayoutGroups_RealBrowserMeasuresCapturedGroupText` asserts the measured combined-row height in SQLite (104px, not a constant).
  3. Scoring sees all crossings: `CrossingCount` (`group_geometry.go:86`) is used at `coordinator.go:288` (reviewed), `:336` (unreviewed), `group_route.go:499` (route cost) and `reviewer_checks.go:492` (advisory). `TestCoordinator_GroupCrossingsAffectBestScore` checks the produced score with one attempt; `TestGroupCrossings_CandidateRanking` checks `better` with hand-built candidates. Neither proves persisted cross-generation selection; paired row 5 traces that boundary.
  4. Preflight is consulted: `preflight.go:31-44` runs `preflightGroups` plus the readable/text/header/label checks for any graph with Groups. A blocking group finding must produce a *changed* group candidate through `Refine`. The effective gaps at `group_layout.go:185-187` read `o.NodeSpacing`, `o.RankSpacing` and `o.NodeTextSlack`. Assert the changed geometry, not the changed options.
  5. Go↔TS contract parity: `TestGroupGeometry_SharedV2Fixtures` and `groupGeometry.contract.test.ts` load the same `groupGeometry.v2.json`, and `contract.json` equals `DefaultContract()`.
- **Review 2 — failure lens:**
  1. Removing a footer or card, mis-owning a body, routing through a sibling card, omitting a connector or mixed crossing, or detaching a label must each fail the specific validation or preflight sink (`TestPreflightGroups_RejectsIncompleteAndCollidingGeometry`).
  2. Edge shapes: linear→group→rejoin, duplicate child workflow names with distinct keys, nested branches and workflows, a loop inside a lane, disjoint groups, a closed loop without a sink (no invented exit) and identical real parallel edges (`TestArrangeGroups_DownwardAndComplete`, `TestArrangeGroups_AuthorOrderNeverWraps`).
  3. Purity: the original manifest JSON and `g.Groups` are byte-equal after Arrange and after a failed attempt (`TestArrangeGroups_DownwardAndComplete` asserts unchanged manifest JSON). Check that `translateGroupGeometry` never mutates captured input maps.
  4. Search bounds: `searchGroupPath` (`group_route.go:367`) must terminate with an error, not hang, on a blocked corridor, and that error must surface as an explicit `StageArranging` failure. Cite or add a test.
  5. Cancellation: cancelled Coordinator entry and between-stage contexts stop the job (`TestCoordinator_GroupCancellation`). Arrange keeps its context-free signature.
  6. Ordinary graphs: no group arrays, v2 format, header `ElementID`s at `route.go:378-379`, and ordinary `Preflight` unchanged (pr-review and feature-pipeline acceptance still pass).
- **Seed evidence:**
  - `domain/layout/arrange.go:34-36`: the group dispatch. `preflight.go:31`: the `g.Groups != nil` branch returns early after `preflightGroups`, so the ordinary `checkNodeOverlap` / `checkRoutesAvoidNodes` never run for group graphs. Group overlap and route checks must therefore live entirely in `preflightGroups` / `groupContacts`. Verify that coverage is equivalent.
  - `grep -rn "CrossingCount" --include=*.go domain adapter | grep -v _test` → `coordinator.go:288,336`, `group_route.go:499`, `reviewer_checks.go:492-493`, `layouttest/store_contract.go:53,70`.
  - `refine.go:33-68` responds to NodeOverlap, EdgeThroughNode, TextCollision, TextClipped, MergedLines, AmbiguousAttachment, AmbiguousCrossing, IndistinctLoop and IndistinctBranch. `preflightGroups` also emits `CriterionUpwardProgression`, `CriterionArrowDirection` and `CriterionCoverage`, which `Refine` ignores, so those findings end in `no_refinement`. Confirm that this is intended and explicit.
  - `domain/workflow/group.go:173 groupTaskKeyRE = ^[A-Za-z0-9_-]+$` rejects Unicode task keys (W1).

---

## Merge decisions

- **Storage, Reader, Requester, cache and retry → Area 1.** Keep the authorized read/retry entry and persisted-best sink together; migration, decoding and store-copy fixtures are named checks on this path.
- **Static drawing/capture/review + runtime probes → Area 2.** Merge-test clauses: judging either as wired-and-fed requires the other, the same functions are reviewed, and probes have no independent accepted sink. `RenderSurface.tsx:runRenderJob` compares probes with its static collector and restores that graph before capture; `reviewer.go:ReadabilityReviewer.Review` makes one coverage-gated decision. The seed's separate snapshot and PNG checks remain in both lenses.
- **Child polling/placement + live route drawing → Area 3.** Merge-test clauses: both require `ExecutionView.tsx:GraphView`, `ArrangedGraph.tsx:ArrangedGraph` and the same composed scene. Tile construction joins geometry and live maps, and qualified selection/follow consumes its output. The seed's proposed entry split is inside one `GraphView` call, not independent route entries.
- **Arrangement + preflight + scoring → Area 4.** Keep recursive placement, measurements, route search and refinement together; preflight has no independent entry. Structural validation and wire fixtures are folded into the producing behaviour. The persisted retry score ordering is paired row 5.
- **Release proof stays folded in.** P3T20 tests are evidence at each sink, and the Makefile/CI gate stays in Area 2.
- The remaining boundaries have distinct entries and sinks: authorized stored reads/retries (Area 1), headless browser review (Area 2), user route scenes (Area 3), and measured geometry/refinement (Area 4). Shared DTOs and drawing helpers do not justify merging these distinct entry paths.
