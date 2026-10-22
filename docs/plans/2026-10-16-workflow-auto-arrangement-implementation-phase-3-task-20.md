# Workflow Auto-Arrangement — Phase 3 Task 20: Group composition-root and release proof

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§6.19–6.20, and [P3T15 geometry/input interfaces](2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-15.md#exact-geometryinput-interfaces-p3t15-declarations-p3t16-validation-p3t17-report-declarations).

**Spec/design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; matching design phases 1–3. Go 1.25.5, React 19/xyflow 12 and the existing browser/DOM/raster pipeline.

**Global constraints:** Arrange every committed authoring save asynchronously; five total attempts by default; preserve execution and every manifest edge occurrence; fence by saved revision/generation; share reviewed geometry across workflow/instance/review surfaces; retain best/fallback and truthful issues/Retry on failure. No manual positioning, opt-out or automatic processing of untouched legacy workflows.

**Order:** P3T15 (captured input) → P3T16 (geometry) → P3T17 (drawing/capture) → P3T18 (review/storage) → P3T19 (live views) → P3T20 (root/release). These replace former P3T15 stages 15.2–15.6. Each task owns its green checkpoint; successor acceptance is not a prerequisite.

**Strict TDD:** For each numbered cycle write the named assertions first, run the focused command and observe behavioral RED against compiling code, implement the minimum, rerun GREEN and refactor. For already-correct behavior remove its production guard/feed temporarily and observe the sink assertion fail, restore it and rerun. Compiler errors, constructor counts and browserless skips do not prove the behavior. Do not check a completion box during this docs review.

**Goal:** Prove the complete saved group input → production measured/reviewed result → both user views/live subtree outcome through existing real composition roots and required CI gates.

**Files:** extend existing `cmd/arbitron/layout_groups_e2e_test.go`, `layout_group_surfaces_test.go`; extend current root/browser helpers instead of creating another stack. Modify layout_wiring.go only if a real dependency injection changes. Preserve Makefile/.github/workflows/layout.yml required gates and rebuild adapter/dashboard/dist.

**Interfaces:** Consume P3T15 captured Groups/WorkflowGroups, P3T16 complete v2 geometry, P3T17 actual DOM/report/probe producers, P3T18 Reviewer/Reader persistence truth and P3T19 real live route/tile hooks. Existing initDomain constructs Service/Stamper/Requester/SQLite/Worker/Coordinator/BrowserRenderer/Reviewer/QueryService/GraphStateReader.

**Existing harness producers:** `cmd/arbitron/layout_surfaces_e2e_test.go:surfaceSnapshot` / `surfaceQueueGate` persist a real trigger snapshot before releasing execution; `surfaceServer`, `surfaceBrowser`, `surfaceView` and `surfaceAssertGeometry` drive the actual router/embedded page. Extend their inspectors for group/tile fields rather than replacing HTTP/DOM. `cmd/arbitron/group_provenance_e2e_test.go:newGroupProcessGates`, `groupProcessGates.reached/release` and `gateOrderedManifest` supply bounded real-script admission controls for the live subtree; author the fixture through Service first, then trigger with the real authorized Engine. `cmd/arbitron/layout_stdio_protocol_test.go:buildLayoutStdioBinary` / `startLayoutStdio` / `layoutStdioProcess.call` supply the unfiltered subprocess MCP path. These helpers delegate actual producers; no snapshot/GraphState/result is fabricated. Local stdio principal, clean JSON-RPC transport and raw-read settlement are existing P2T9 producers, not optional fixtures.

## 20.1 Real authoring and reviewed revision sinks

- [x] **RED:** add `TestLayoutGroups_RealRootSavedRevisionReachesReviewedSink`. Through initDomain's actual Service, authorized HTTP authoring and unfiltered local stdio MCP, save branches/workflows/nested manifests, including group upsert/delete/full update. Trigger a pending snapshot with the existing surfaceSnapshot queue boundary; saved input/behavior remains exact. Assert nonempty group config/task order/ownership, distinct saved revisions, configured budget five and exact SQLite captured bytes, not only a constructor or row count.
- [x] **GREEN:** Let the actual Worker→Coordinator→BrowserRenderer→Reviewer complete. Require StatePassed within five attempts, full geometry/digest, every real ordinal/verification field and complete group element/text/connector/attachment/crossing data. Open/hash immutable overview and all readable tiles. Stop the worker and remove cache; authorized workflow and frozen-instance reads still return that exact persisted result/generation. No graph/renderer/reviewer/store success fake is allowed.
- [x] Failure/error/edge controls: missing WorkflowGroups, one erased synthetic corridor or container/lane paint with valid hashes, failed font/browser asset, exhausted review, retry retaining best, stale generation and a later authoring save. Explicit failures retain saved input/best/fallback and truthful actionable Retry; they never count as the successful acceptance fixture. Ordinary v2 samples and a legacy untouched workflow retain their original behavior.

## 20.2 Actual embedded workflow/instance and child scenes

- [x] **RED:** add `TestLayoutGroupSurfaces_WorkflowAndInstanceParity`. Mount both embedded routes through the real authorized router with the above result, wait for actual fonts, then compare every local node/group box/text, real ordinal/path/label/attachment/arrow, synthetic path/gap, revision/generation/digest and data-layout-mode=arranged. Runtime styling changes no reviewed geometry. A completed instance must update when review finishes later.
- [x] Create a real authorized subtree with two grouped child snapshots using existing safe script/process gates, not fabricated GraphState/API data. Assert each independently polled child endpoint/revision/generation/digest and uniformly translated paths; inverse-translated root geometry matches WorkflowReview. Exercise independent losing tasks, redaction, original branch/child selection, group inspection, all-active follow/recenter and callouts. Titles/findings/Retry controls in the status rail do not obscure graph paths. Remove a hidden context and assert no ID/link/request leaks.
- [x] Repeat after child/root retry, status outage/cache deletion, restart and an external later save. Per-tile errors/generation floors cannot replace another snapshot, stop active child polling or hide a failed child behind a reviewed root. Server font failure uses arrangement Retry; client font failure uses existing Reload reviewed fonts without a POST/generation change. Preserve explicit nil/v1/incompatible group fallback negatives.
- [x] In isolated restored mutations, drop each load-bearing feed at its real site: Coordinator.WorkflowGroups, BrowserRenderer group report, stored group arrays, GraphView.groupLive/nodeContexts and child layout client data. Each corresponding actual SQLite/PNG/DOM/selection sink must fail. Count/call-only assertions and fake argument-ignoring wrappers are insufficient. Do not check earlier tasks on their behalf.

## 20.3 Release integration and verification

P3T16/P3T17 already replace their owned ordinary-box/standard-layout stopgaps with stronger producer/DOM tests. Check those tests remain effective here; do not retain an inert group fallback to satisfy obsolete success assertions and do not postpone their replacement until this release task.

- [x] Keep `make test-render` unfiltered over all adapter/renderreview and cmd/arbitron tests (current source already does so); add these normal *_test.go files without build tags and confirm both new test names run with ARBITRON_REQUIRE_BROWSER=1 and rebuilt embedded assets. Do not narrow it to new prefixes. `.github/workflows/layout.yml` still invokes make test-all, including real browser and font portability prerequisites. Add a build-gate assertion if needed to prevent future omission; avoid redundant CI roots/jobs.
- [x] Run focused new root/surface tests RED→GREEN before broad gates: `make ui`; `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron -run '^TestLayoutGroups_|^TestLayoutGroupSurfaces_' -count=1 -v -timeout 90m`. Observe the named sink failure before repair (or a targeted feed mutation for existing behavior), restore and confirm nonzero test execution/no skips.
- [x] Run affected group/renderer/root suites under `-race`, then `go test ./...`; `cd dashboard && npx tsc --noEmit && npx vitest run`; `make test-render`; `make test-all`; `git diff --check`. Group additions must not remove P3T7/P3T14 pr-review/feature-pipeline/ralph-loop acceptance. Missing browser/runtime or failed readability leaves release incomplete; never raise the budget, shrink fixtures, weaken the reviewer or report a skipped/fake result as success. Commit implementation only after applicable gates pass.

**Review focus:** each new field has a production producer and sink in the ordered chain. Missing input/ink/live/child-layout controls are mandatory. No merged scene is persisted or claimed reviewed as one saved revision.

## Execution record (2026-10-20)

P3T16's previously recorded Unicode task-key validation conflict remains unchanged;
this pass selected the next independent task, P3T20. No authoring policy, attempt
budget, reviewer criterion, production constructor or CI selection was changed.

The new saved-revision test covers all nine form/transport combinations and four
commits each (create, group upsert, group delete, full update). Actual Service,
authorized HTTP and shipped, race-built stdio MCP feed captured bytes into SQLite,
real browser measurement/capture/review and exact persisted Reader results. A real
queued Engine snapshot freezes the still-pending first revision. Immutable PNG
hashes, every real occurrence/attachment, captured config/order, five-attempt
budget and cache-independent current/frozen reads are asserted at their sinks.

The embedded-route inspectors now include actual group/text/overlay bounds and
paint, complete real/synthetic dashed paths and mixed gaps. The shared typography
fixture supplies a real loop and mixed crossings. Two distinct grouped child
revisions execute bounded shell processes through the real Engine. The tests
assert captured tile order/uniform translation, original branch-owner variables
and group selection, independent losing tasks, all-active follow/recenter, and
an outside-the-graph status rail. Actual result/cache loss, HTTP status outage,
child/root UI Retry, later authoring and root restart preserve each frozen tile.
A real org session plus stored ownership revocation exercises redaction: no child
ID, link or request reaches the page. A real file conflict opens the existing
resolution-session producer; its callout anchors the reviewed footer. Only the
existing deterministic CLI transcript fixture is reused there; no GraphState,
layout, renderer, reviewer, store or successful HTTP result is fabricated.

Separate controls erase container/lane/corridor PNG pixels with recomputed valid
hashes. A missing corridor exhausts exactly five actual attempts, retains the
unreviewed best during Retry and rejects old-generation completion. Server font
measurement/capture outages use real arrangement Retry; client font loss uses
Reload reviewed fonts with zero Retry POSTs and an unchanged durable result.
Actual nil/v1/missing-ownership SQLite corruption keeps both routes explicitly
unreviewed/fallback and actionable. Completed execution also receives a later
real review without navigating or fabricating a new snapshot.

Behavioral RED evidence (all mutations restored):
- `/tmp/p3t20-root-feed-red.log`: removing Coordinator.WorkflowGroups makes the
  new named saved-revision test fail its passed SQLite sink with the exact
  missing-ownership rendering failure.
- `/tmp/p3t20-browser-report-red.log`: dropping the actual BrowserRenderer group
  report fails the retained/reviewed result sink (coverage, needs_attention).
- `/tmp/p3t20-storage-arrays-red.log`: dropping group arrays at SQLite insertion
  fails the authorized Reader's complete-result sink.
- `/tmp/p3t20-context-red.log`, `p3t20-live-red.log`, `p3t20-child-feed-red.log`:
  removing nodeContexts, groupLive and child client layouts separately fails the
  actual independent-body indicator, winner/non-blocking rows and tile geometry.
- `/tmp/p3t20-controls-raster-red.log`: removing group raster checks wrongly
  passes erased lane/corridor ink; the durable negative-control sink fails.

Focused browser-required root/surface suite passed with no skips (821.509s;
`/tmp/p3t20-focused.log`). Additional strengthened mixed-route, overlay, original
selection, follow, callout and nil/v1 controls also passed their focused runs.

Release verification follow-through (2026-10-20):
- Group producer/browser tests passed under `-race` (domain/layout 22.106s;
  adapter/renderreview 289.076s); the complete
  browser-required group root/surface selection passed under `-race` (1016.275s).
  The final live-child controls also passed under `-race` (110.807s).
- The first unfiltered render gate exposed one stale ordinary v2 fixture:
  DashedLoopCrossings/over appended an edge without attachment ElementIDs.
  `/tmp/p3t20-loop-fixture-red.log` reproduces the actual browser rejection;
  supplying the two canonical header IDs restores both over/under crossings
  without changing a route, threshold or assertion. The full renderreview package
  then passed under `-race` (551.434s).
- The same initial gate caught a test shutdown race at nested/stdio's cache
  deletion control: terminal SQLite completion precedes best-effort cache
  publication. Waiting for publication only AFTER stopping the subprocess could
  cancel its producer. The test now waits BEFORE shutdown, with the original
  one-second deadline, then deletes the real cache and asserts both immutable
  Reader results. The exact nested/stdio case passed under `-race` (74.100s;
  `/tmp/p3t20-cache-order-green.log`). No production lifecycle was changed.
- Initial broad Go and dashboard runs had an unrelated Git process-start timing
  failure and route-test timing failures. The two dashboard files passed on
  focused rerun (58 tests), then the entire dashboard passed (98 files/1460 tests).
  The later unfiltered `make test-all` passed Go (including adapter/git), TypeScript,
  all 1460 dashboard tests, and its unchanged full `make test-render` prerequisite:
  adapter/renderreview 379.317s; cmd/arbitron 1734.346s. Real dashboard-browser tests
  and Docker font portability also passed (76 installed faces versus zero), with
  no browser skips. `/tmp/p3t20-test-all.log` and
  `/tmp/p3t20-verification-summary.log` record exit 0 for the full gate.
- `make ui` rebuilt the embedded assets; they match the checked-in production
  build. `git diff --check` passes. Required Makefile/CI selection and existing
  pr-review/feature-pipeline/ralph-loop acceptance remain unfiltered and unchanged.

Ordered self-review completed before commit:
1. Spec compliance: this is root/release proof only. No new production flag,
   constructor, service, attempt budget or reviewer policy; all nine real
   authoring form/transport combinations, retained evidence, both embedded
   routes and live child histories are covered. The ordinary fixture repair
   supplies required v2 ownership rather than removing its crossing acceptance.
2. Anti-green-theater: tests assert captured SQLite bytes, actual immutable PNG
   hashes, authorized Reader results and independently scoped real DOM paths,
   boxes, rows and original selection. The recorded independent production-feed
   mutations fail those sinks; no success fake ignores a load-bearing input.
   Cache deletion now occurs after its real producer has published, not after
   cancellation. Nil/v1/wrong-ownership and erased-ink negatives remain intact.
3. Code quality: shared inspectors extend existing HTTP/browser/process helpers;
   scoped scene comparisons uniformly translate copied geometry without mutating
   persisted inputs. Temporary mutations are restored, Go is formatted, and no
   dead wiring or accepted missing-input no-op is added. After the fixes, the
   affected browser/race tests and full release gate are green.

P3T20 is complete. P3T16's independently blocked Unicode task-key assertion remains
unchecked; this release proof does not change execution task-key validation or
claim that blocked requirement passed.
