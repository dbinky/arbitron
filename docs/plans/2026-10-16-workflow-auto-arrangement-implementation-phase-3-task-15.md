# Workflow Auto-Arrangement — Phase 3 Task 15: Captured group input, text vocabulary and protocol

> **SPLIT (2026-10-19):** this task now covers **15.1 only**. Stages 15.2–15.6 are their own tasks: **P3T16** (15.2 placement/routing/preflight), **P3T17** (15.3 drawing/capture), **P3T18** (15.4 reviewer/storage), **P3T19** (15.5 runtime/views), **P3T20** (15.6 root/release), executed in that order. The 15.1 acceptance that needs a later stage's producer moved to that stage, so no task waits on a later one. The Exact interfaces section below remains the shared contract for P3T16–P3T20.


**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§6.18–6.20. This is required completion work after P3T1–P3T14, not an optional extension. Earlier v1 examples describe the pre-migration implementation.

**Goal:** Capture immutable group config/ownership, complete text measurement vocabulary and independent per-attempt render/review inputs. P3T16–P3T20 consume these to deliver the same reviewed group geometry in workflow, instance and review surfaces while preserving live inspection/navigation.

**Spec/design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; all three matching design phases, especially their parallel-group alignment sections. React 19/xyflow 12, Go 1.25.5, existing browser/DOM/raster pipeline; no new service or layout dependency.

**Current state:** P3T15 now captures group configs, ownership and measurement inputs. Full
group geometry and drawing remain P3T16–P3T20: the pre-migration path reviews ordinary boxes while `ArrangedGraph.groupFallback` and `ExecutionView.GraphView.liveRenderer` draw a different group layout. A server `passed` verdict therefore does not establish the displayed group's readability. P3T16 replaces the arranger stopgap with complete geometry assertions; P3T17 replaces the
renderer stopgap with actual group DOM assertions. Keep their negative compatibility controls.
P3T20 proves the combined release outcome; it is not a prerequisite of those earlier unit gates.

## Ownership, dependency order and files

Execute P3T15 → P3T16 → P3T17 → P3T18 → P3T19 → P3T20, committing only green slices. Do not recreate P1 layout algorithms, P2 storage/worker, P3 query services or `workflow.AnalyzeGroups`. Each subtask runs failing tests first, observes behavioral RED against a compiling implementation, implements the minimum, runs GREEN and refactors. Existing behavior requires a mutation probe. This docs review changes no completion boxes.

| Step | Files created or modified (tests adjacent unless specified) |
|---|---|
| P3T15 / 15.1 model/input | `domain/layout/{graph,geometry,measure,render,coordinator,reader}.go`, `group_geometry.go`; `dashboard/src/graph/arranged/{types,validate}.ts`, `dashboard/src/render/job.ts`, `groupText.contract.json`; input/Go/TS wire fixtures |
| P3T16 / 15.2 arrange/preflight | new `domain/layout/group_layout.go`, `group_preflight.go`; existing `arrange.go`, `preflight.go`, `refine.go`, `size.go`, `group_size.go`, `coordinator.go`, `contract.go`/`contract.json`; Go/TS validation and structural fixtures; `arrange_group_test.go` |
| P3T17 / 15.3 shared drawing/capture | `dashboard/src/graph/arranged/{buildArrangedFlow,ArrangedGraph,RoutedEdge,routedPath,fonts}.ts(x)`; `dashboard/src/graph/nodes/GroupContainerNode.tsx` and CSS; `dashboard/src/render/{MeasureSurface,RenderSurface,report,job}.ts(x)`; `adapter/renderreview/browser.go`, `domain/layout/{render,group_render}.go`, `src/render/groupProbes.ts`, `groupProbe.contract.json` |
| P3T18 / 15.4 review/storage truth | `adapter/renderreview/reviewer{,_checks,_capture}.go`; `domain/layout/{reader,requester,query}.go`; new genuine browser fixtures in `adapter/renderreview/testdata/readability/`; `domain/layout/layouttest` doubles |
| P3T19 / 15.5 runtime/views | new `dashboard/src/hooks/useInstanceLayoutTiles.ts`; new `dashboard/src/graph/arranged/composeArrangedTiles.ts`; `dashboard/src/graph/useGraphLayout.ts` (pure fallback extraction), `dashboard/src/api/client.ts` (AbortSignal); `ExecutionView.tsx`, `WorkflowReview.tsx`, `useFollowActiveNode.ts`, `CalloutConnector.tsx`, `nestedGraphs.ts`; route/browser tests |
| P3T20 / 15.6 real root/release | existing `cmd/arbitron/layout_groups_e2e_test.go`, `layout_group_surfaces_test.go`; `cmd/arbitron/layout_wiring.go` only if injection needs change; `Makefile`, `.github/workflows/layout.yml`, rebuilt `adapter/dashboard/dist` |

## Exact geometry/input interfaces (P3T15 declarations; P3T16 validation; P3T17 report declarations)

Preserve every real node ID and every manifest edge ordinal/source/target/output/label in the existing `Geometry.Nodes` and `Geometry.Edges` arrays. The group node's `NodeBox` is its header, drawn once. Supplemental presentation never becomes a workflow node/edge or enters an instance's execution graph.

```go
// domain/layout/graph.go
// Add Groups *workflow.GroupGraph to Graph (deep copy, nil only when no groups).
// BuildGraph uses ParseGroupConfig for config errors and AnalyzeGroups for ownership.
var ErrInvalidGroups = errors.New("layout: invalid parallel group graph")

// domain/layout/group_geometry.go; all coordinates are absolute canvas coordinates.
type GroupText struct {
    Key string `json:"key"`
    Text string `json:"text"`
    Style TextStyle `json:"style"`
    Box Rect `json:"box"`
    Lines int `json:"lines"`
}
type GroupElement struct {
    ID string `json:"id"`
    GroupNodeID string `json:"groupNodeId"`
    TaskKey string `json:"taskKey,omitempty"`
    Role string `json:"role"` // container | header | footer | task | lane
    Box Rect `json:"box"`
    Texts []GroupText `json:"texts"`
    OverlayBox *Rect `json:"overlayBox,omitempty"` // measured reserved live-status slot
}
type GroupLane struct {
    GroupNodeID string `json:"groupNodeId"`
    TaskKey string `json:"taskKey"`
    Box Rect `json:"box"`
    NodeIDs []string `json:"nodeIds"` // immediate owned real nodes, manifest order
}
type GroupConnector struct {
    ID string `json:"id"`
    GroupNodeID string `json:"groupNodeId"`
    TaskKey string `json:"taskKey"`
    Kind string `json:"kind"` // sink | fanout | gather; presentation only
    SourceElementID string `json:"sourceElementId"`
    TargetElementID string `json:"targetElementId"`
    Points []Point `json:"points"`
    SourceAttach Attachment `json:"sourceAttach"`
    TargetAttach Attachment `json:"targetAttach"`
}
type GroupCrossing struct {
    Point Point `json:"point"`
    Over string `json:"over"`
    Under string `json:"under"`
}
func GroupElementID(groupID, role, taskKey string) string
func GroupTextKey(elementID, field string) string
func ManifestRouteKey(ordinal int) string // "edge:" + decimal ordinal
```

Add to `Geometry`: `GroupElements []GroupElement json:"groupElements,omitempty"`, `GroupLanes []GroupLane json:"groupLanes,omitempty"`, `GroupConnectors []GroupConnector json:"groupConnectors,omitempty"`, `GroupCrossings []GroupCrossing json:"groupCrossings,omitempty"`. Add `Attachment.ElementID string json:"elementId,omitempty"`. In v2 every attachment carries its rendered element ID; NodeID continues to verify the real manifest endpoint (empty for synthetic-only endpoints). Group success/failure routes attach to the footer, task routes leave the header, incoming routes reach the header. All ordinary attachments resolve their real node box.

Element IDs are canonical JSON arrays: for role header with empty task key, `GroupElementID(g,role,k)` = JSON `["node",g]`; for other roles it is JSON `["group",g,role,k]`. Ordinary node element ID = JSON `["node",nodeID]`. Thus header decoration and its real NodeBox identify the same drawn element. Connector ID = JSON `["connector",groupID,kind,taskKey,sourceElementID,targetElementID]`. Task text keys = JSON `["text",elementID,field]`. No delimiter concatenation or user-ID collision. `ManifestRouteKey` addresses real routes; connector IDs address synthetic routes. Existing `Crossings` covers real/real intersections; `GroupCrossings` covers intersections with at least one connector, exactly once, using these route keys. Both use the same deterministic gap treatment and drawing order.

Add `View.Groups *workflow.GroupGraph json:"groups,omitempty"` and TS `LayoutView.groups?: GroupGraph` for revision-bound ownership. Reader.View produces this from BuildGraph of the SAME authorized saved workflow/snapshot used for SavedRevision, even when the Store row/result is pending or missing; never from a separate moving group-layout lookup. Invalid groups omit it and expose the failed/attention reason. It is derived response data, not a second Store column. TS mirrors every field/tag above, including optional arrays/elementId; P3T15 owns the input/wire declarations; P3T16 owns complete positive-v2 validation and wire fixture migration. P3T17 owns the DOM/report schema in contracts §6.20; P3T18 owns genuine captured review fixtures. `GroupGraph` is the existing API type, not a second ownership model.

Extend `validateGeometry(def,geo,contract,groups?: GroupGraph|null)` to require complete matching ownership for a group definition; `resolveDisplayGeometry(def,view)` passes the joined view.groups. All normal view callers receive that map from Reader; RenderSurface puts job.workflowGroups into its synthetic LayoutView.groups. Validate config/task order against the manifest and lane/parent memberships against the authoritative map; do not implement a second ownership reachability analysis in TS. Missing, stale or wrong ownership produces display issues and suppresses reviewed mode. Ordinary definitions need no map.

**Format transition:** produce `FormatV2 = "arbitron.layout/v2"` and `ContractVersion = "arranged-renderer/2"` for all new candidates. Keep the v1 constant for decoding history only. Add exact contract fields `groupPaddingPx:32`, `groupTaskGapPx:24`, `groupRowGapPx:48`, `groupMinWidth:200`, `groupHeaderMinHeight:80`, `groupFooterMinHeight:80`, `groupTaskMinHeight:96`, `groupConnectorDash:"3 3"`. Go field names are PascalCase equivalents with identical JSON tags. All other typography/stroke/zoom values stay as in v1. Full-contract equality and geometry digests include these values and group arrays. P3T16 updates Go↔JSON↔TS contract/version and structural wire fixtures atomically; P3T17 updates the actual group surface/report; P3T18 regenerates genuine screenshot fixtures only after that producer exists. No earlier task waits on a later screenshot/review producer. Reader rejects v1 as current reviewed geometry under v2, retaining captured coordinates with an explicit compatibility issue; Requester invalidates/retries it through the existing fenced seam. Never silently bless v1 group boxes, auto-retry untouched workflows, or bump an authoring revision during this migration.

## 15.1 Input, typography and page protocol

- [x] **RED tests:** `TestBuildGraph_GroupInputIsCaptured`, `TestGroupGeometry_WireAndValidation` and TS `groupGeometry.contract.test.ts` use branches, workflows and nested forms. Assert exact config/task order/ownership and clone isolation; ordinary graphs have no group decorations. Nil/empty/missing/mismatched ownership, invalid config, group-on-cycle and body escape fail with `ErrInvalidGroups`, never an ordinary-box success. Mutation removing group input must fail. Invalid saved legacy input remains readable via fallback and visibly fails arranging; existing authoring validation policy stays intact. *(15.1 scope complete; its remaining positive-v2 portions moved to P3T16/P3T17 on 2026-10-19.)*
  - [x] Input-capture portion: branches/workflows/nested configs, author task order, immediate ownership, nested input clone isolation, ordinary nil input, typed config/topology failures, and actual-root invalid-history SQLite/read/Retry sinks. Removing capture fails the new unit and real-worker browser-metric/SQLite assertions.
  - [x] Wire-shape/ownership-response portion: Go/TS GroupText/GroupElement/GroupLane/GroupConnector/GroupCrossing, optional Geometry arrays and Attachment.ElementID; shared exact-field/Unicode wire fixtures, lossless decoding and full-digest sensitivity. Reader feeds View.Groups from the same authorized input for legacy/missing/pending/outage reads; actual-root authorized workflow and frozen-instance HTTP sinks retain different revision-bound configs/order after a later save. Invalid groups remain readable with explicit failure and no ownership; ordinary graphs omit it.
  - [x] Captured-input validation portion: Arrange rejects nil/empty/missing/edited ownership, task/config order, nested parent/body/owner mismatches and extra ordinary-graph ownership with ErrInvalidGroups and no candidate. Geometry.Validate/Compatible/Preflight reject the same stale input; validation preserves ErrInvalidGeometry for existing stored-value callers. Independent exact copies and captured-before-later-save inputs remain accepted. This is input integrity, not positive v2 geometry acceptance.
  - (moved 2026-10-19 to P3T16, the stage that produces it) complete positive v2 ownership/geometry validation in `TestGroupGeometry_WireAndValidation` and TS, contract fields/version/format migration and corresponding structural wire fixtures; genuine group screenshots belong to P3T18. Current wire tests establish serialization/digest binding and reject undrawable additions in v1, not valid reviewed group geometry. The moved acceptance belongs to P3T16, not to this completed input slice.
- [x] Add group text items to `MeasureRequestFor` using `GroupTextKey`. Branch lane decorations contain `task <author index + 1> · <task key>` so identical task-port labels still identify each branch. Header supplements contain `fan-out`, task count and form; primary header text/type still come from NodeBox. Container caption matches `groupContainerLabel`'s form/rule wording. Footer contains full rule, unfinished behavior and gather mode text using `groupText.ts`'s wording. Workflows-form cards contain author index/key and configured child workflow name. Use StyleNodeLabel for titles, StyleNodeType for type/status captions, StyleEdgeLabel for metadata. Extra keyed measurement items cover every allowed live status/badge phrase, redaction text and winner+configured-task-key value; sizing reserves the maximum combined block in OverlayBox on footer/task elements. These alternatives are measurement inputs, not baseline static texts to draw simultaneously. `group_geometry.go:groupTextItems(g Graph, c RendererContract) []TextItem` owns deterministic measurement text; 15.1 creates `dashboard/src/graph/arranged/groupText.contract.json` golden text cases read by Go and TS tests, including all enum values and Unicode. No backend caption reversal, guessed glyph widths or missing-text defaults. *(15.1 scope complete; its remaining positive-v2 portions moved to P3T16/P3T17 on 2026-10-19.)*
  - [x] Measurement-input portion: canonical element/text/route IDs, deterministic static and finite live text vocabulary, shared Go/TS golden wording (all config enums and Unicode), and actual Worker → Coordinator → installed BrowserRenderer measurement results from captured saved input.
  - [x] Header sizing prerequisite: Arrange reserves browser-measured fan-out/form/task-count supplements together with the original header label/type; width/height grow from the actual blocks, including nested headers. Missing/corrupt supplements fail arrangement and preflight checks their complete text clearance. The real worker's retained SQLite candidate and Reader geometry assert the measured reservation; bypassing production sizing fails that sink.
  - (moved 2026-10-19 to P3T16, the stage that produces it) measured combined-block sizing and persisted footer/task OverlayBox reservation; no completed group layout is claimed by the input tests.
- [x] Add `WorkflowGroups *workflow.GroupGraph json:"workflowGroups,omitempty"` to both RenderRequest and ReviewRequest. Keep the four-argument `NewRenderRequest` API; Coordinator fills each request with an independent clone from its captured `BuildGraph(job.Manifest).Groups` before Render/Review. BrowserRenderer forwards it to the render job, RenderSurface uses it to populate synthetic definition configs/ownership, and validates exact agreement with geometry and node types before capture. Nil is legal only for a no-group manifest. Change `render.go`, `job.ts`, BrowserRenderer and all argument-honoring doubles together. Error/no-ink/missing-map tests must fail without that input at the real root. *(15.1 scope complete; its remaining positive-v2 portions moved to P3T16/P3T17 on 2026-10-19.)*
  - [x] Protocol/input portion: independent captured Render/Review clones (including subsequent attempts), Go→browser document forwarding, synthetic saved configs and LayoutView.groups, config/type/order/body/parent/continuation consistency checks, and real-root SQLite/authorized-HTTP/no-PNG sinks. Dropping Coordinator.WorkflowGroups fails all three real-root form tests. Reviewer and shared doubles reject ignored/extra ownership and ordinary-box v1 group evidence.
  - (moved 2026-10-19 to P3T17, the stage that produces it) positive v2 geometry/ownership agreement and successful full group DOM/capture acceptance. The moved acceptance belongs to P3T17, not to this completed input slice.
- [x] GREEN commands: `go test ./domain/layout -run 'TestBuildGraph|TestGroupGeometry|TestMeasureRequest' -count=1`; `cd dashboard && npx vitest run src/graph/arranged src/render/job.test.ts && npx tsc --noEmit`. Run behavioral RED first; do not count missing-symbol failures alone. *(15.1 scope complete; its remaining positive-v2 portions moved to P3T16/P3T17 on 2026-10-19.)*
  - [x] Header sizing prerequisite verifications: specified commands pass, full domain/layout passes under -race, and browser-required actual-root group/ownership tests pass under -race. These are not full v2 acceptance.


## Historical execution evidence (before the P3T15–P3T20 split)

The following dated records preserve prior observations; their former parent-task and
blocker descriptions do not override the current ownership table or contracts §6.20.

### Input/typography execution slice (not v2 acceptance)

Selection: enumerated all plan files in numeric phase/task order. P3T7 is the first
unchecked task; its two remaining race/release boxes already record repeated stage-entry,
group-state and package-deadline failures. That same blocked gate was not spun again.
P3T15 has independent input work. Only this task's input/typography slice is implemented;
all original parent checkboxes remain unchecked. The nested checks above record the
completed portions rather than falsely closing the format/render/live migration.

Production path: BuildGraph now calls the existing ParseGroupConfig/AnalyzeGroups and
captures independent configs/ownership. MeasureRequestFor appends the group's deterministic
text vocabulary, which the existing live coordinator feeds to the installed browser.
Invalid historical groups fail at arranging, with no candidate, while QueryService still
exposes their stored failure/fallback and same-revision Retry. Basic malformed nodes/edges
are not exempted. Current authoring continues rejecting invalid groups. The old ordinary-box
stopgap assertions are preserved; their previously invalid missing-config/task-port fixture
now uses a valid branches config and the actual shared task port, keeping its labels.

Strict TDD evidence: /tmp/p3t15-input-red.log records compiling nil-capture and missing-error
failures; /tmp/p3t15-legacy-red.log records the authorized read incorrectly hiding the
historical failure before the narrow ErrInvalidGroups exemption. Measurement assertions fail
before group items/IDs exist (/tmp/p3t15-measure-red.log). The canonical-ID test also catches
Go's HTML/line-separator escaping versus JSON.stringify before correction. Existing TS text
characterization uses a production-caption mutation, which fails two golden cases
(/tmp/p3t15-text-mutation.log). Removing BuildGraph's group capture fails both actual-root
worker tests: zero measured group items and the SQLite rendering-versus-arranging failure
stage/reason (/tmp/p3t15-worker-input-red.log). All mutations are restored.

Ordered self-review before commit:
1. Spec: this is capture/measurement groundwork only, not reviewed group geometry. No v2
   result, OverlayBox, WorkflowGroups forwarding, group DOM/raster acceptance or descendant
   tile completion is claimed. Those original steps remain open. No flags, alternate layout
   service, authoring-policy bypass or passing group-review fake is introduced.
2. Anti-green-theater: the root measurement test observes the live worker's actual browser
   request/output via an argument-preserving delegate (no direct test-origin Measure call).
   It consumes SQLite's captured input and asserts actual nonempty, task-key-sensitive glyph
   metrics. The invalid-history test asserts durable failure, authorized Reader/Retry and
   byte-identical saved input. Absent group capture makes these sinks fail.
3. Quality: retained existing workflow analysis and query authorization seams, preserved
   stopgap expectations, strengthened manifest clone tests to exact bytes, kept canonical
   IDs collision-free across Unicode/HTML/line separators, and ran gofmt/diff checks.

After review: `go test ./domain/layout -run 'TestBuildGraph|TestGroupGeometry|TestMeasureRequest'
-count=1` passes; the entire domain/layout suite passes (4.040s). Browser-required focused
root/input/query tests pass under -race (domain/layout 1.817s; cmd/arbitron 4.376s;
/tmp/p3t15-focused-race.log). `npx vitest run src/graph/arranged src/render/job.test.ts`
passes 15 files/368 tests; `npx tsc --noEmit` passes. `make ui` succeeds with no embedded
asset diff. The first complete dashboard run fails the unchanged ArrangedGraph recenter test
(expected 550,210; observed previous center 140,175); the second complete run passes all
91 files/1384 tests without source/assertion changes (/tmp/p3t15-dashboard{,-repeat}.log).
This intermittent broad-suite issue is recorded, not suppressed or called two green runs.
The task's final release commands and complete v2 group acceptance remain unexecuted/open.

## Wire-shape/ownership-response execution slice (partial task)

Continued P3T15, the only task file with unchecked steps at this pass's selection.
Completed only the newly checked nested wire/ownership portion of 15.1. The format
and contract remain v1 until the atomic producer/drawing/reviewer migration; adding
serializable group fields does not make them reviewed. Go and TS reject group-array
additions and rendered-element attachments in v1 rather than silently ignoring ink
that this renderer cannot draw. No positive v2 validation, group placement, overlay
reservation, page ownership forwarding, drawing/capture, raster acceptance or live
revision-tile completion is claimed. Those parent steps remain unchecked. There is
no newly discovered external dependency/blocker; the task is still incomplete.

Strict TDD: the compiling wire test first loses every new array/elementId on JSON
round trip (/tmp/p3t15-wire-reader-red.log). After declarations, both Go and TS
behaviorally fail on incorrectly accepting undrawn group data as v1 reviewed
geometry (/tmp/p3t15-wire-validation-red.log, /tmp/p3t15-wire-ts-red.log), then pass.
The actual-root three-form test saves through Service, freezes the real engine
snapshot before execution, saves changed config/task order, and asserts both
independently authorized HTTP layout bodies. Removing Reader's Groups feed fails
every form at that sink (/tmp/p3t15-ownership-root-red.log); restored input passes.
The direct Reader matrix also covers legacy/missing/pending/outage and nested
clone isolation. Invalid input never exports an ownership map or reviewed result.

Ordered self-review before commit:
1. Spec: exact declared group shapes/tags, existing GroupGraph ownership, shared
   wire/mirror tests and same-input Reader feed; no SQL column, alternate ownership
   analysis, format blessing, authoring revision change or release-complete claim.
2. Anti-green-theater: the HTTP expected maps come independently from AnalyzeGroups
   of actual saved inputs, not from the implementation under test. Nil ownership
   fails; later saves cannot replace snapshot config/order. Wire fixtures establish
   serialization/digest binding only, not producer/browser acceptance.
3. Quality: converted the affected unkeyed Attachment test literals, reused the
   captured Graph for result compatibility, and preserved active-row polling while
   exposing invalid-group failure. RED tests catch both an impossible legacy Retry
   without a revision and prematurely stopping pending-row polling; fixes pass.

After review: full domain/layout passes (8.536s); browser-required affected input,
Reader/query and real-root suites pass under -race (domain/layout 1.646s,
cmd/arbitron 7.015s). The specified arranged/job Vitest selection passes 15 files /
371 tests and tsc passes. The full dashboard suite passes 91 files / 1388 tests.
make ui rebuilds the embedded assets; focused HTTP/query and BrowserRenderer/reviewer
suites pass. The first concurrent tsc invocation collided with make ui's npm ci
replacing node_modules; the subsequent serial full dashboard verification is green,
not a hidden source fix. Final logs: /tmp/p3t15-wire-reviewed-{race,ts}.log,
/tmp/p3t15-wire-dashboard.log, /tmp/p3t15-wire-{http,adapter}-final.log.
The complete task/release gates remain open, not blocked on an invented producer.

## Captured page-protocol execution slice (partial task)

P3T15 is now the only task with unchecked steps. Continued its 15.1 declarations/
protocol work; no v2 format, measured placement, group drawing/report/raster acceptance
or live descendant tile completion is claimed. The newly checked nested protocol box
records only completed input work. The actual positive capture dependency is recorded
under its remaining box above; it is work owned by 15.2–15.4, not a missing external
service. No parent or release checkbox is closed by a v1 failure test.

Strict TDD evidence:
- Compiling Coordinator tests first receive nil WorkflowGroups for all three forms
  (`/tmp/p3t15-protocol-red.log`). Render and Review now receive independent deep
  copies of the captured BuildGraph map, including typed nested inputs, immediate
  bodies, nested parent links and author task order. Mutating the original manifest,
  the renderer's copy and the reviewer's copy cannot affect subsequent requests or
  the next refinement attempt. The protocol reviewer produces no passing evidence.
- Browser job serialization first drops the map
  (`/tmp/p3t15-browser-protocol-red.log`). The page-model tests first lack configs and
  ownership and accept malformed maps (`/tmp/p3t15-page-protocol-red.log`). Browser
  job encoding now embeds RenderRequest rather than maintaining a second field list;
  the synthetic definition and LayoutView receive cloned saved configs/ownership.
  Structural checks compare the captured map/ports/types/order/membership, without a
  second reachability analysis or client layout.
- Before BrowserRenderer forwards the map, every actual-root form fails with the
  page's missing-ownership error (`/tmp/p3t15-protocol-root-red.log`). With forwarding,
  each reaches the distinct v1 incompatibility failure, persisted in SQLite and the
  authorized HTTP response, with no screenshot published and unchanged saved bytes.
  Explicitly deleting Coordinator's map reproduces all three sink failures
  (`/tmp/p3t15-protocol-root-mutation.log`); restored. This proves input propagation
  and truthful failure, NOT successful reviewed group geometry.
- The production reviewer initially marks ordinary v1 group boxes Rendered=true and
  ignores an extra ownership map (`/tmp/p3t15-review-protocol-red.log`). It now emits
  blocking identity/compatibility findings before reading pixels. Shared layouttest
  doubles likewise reject unsupported/extra group input rather than ignoring it
  (`/tmp/p3t15-doubles-protocol-red.log`). Existing ordinary rendering stays green.

Ordered self-review before final verification:
1. Spec: kept the four-argument constructor and existing GroupGraph producer; no
   alternate ownership service, SQL field, caption-to-type reconstruction, flags,
   authoring revision bump or false v2 compatibility. V1 group capture is explicitly
   rejected instead of accepting absent group ink. All successful v2/full-task gates
   stay open; ordinary-box/standard-layout stopgap tests are preserved.
2. Anti-green-theater: real initDomain Service→Store→Worker→Coordinator→installed
   BrowserRenderer→built embedded page→SQLite→authorized HTTP assertions distinguish
   missing input from complete input with unsupported geometry. The missing-map
   mutation fails those sinks. Unit protocol doubles compare every captured field
   and never manufacture a passing rendered group review. No claim is made that
   these negative tests replace the required positive ink/child/live acceptance.
3. Quality: reused ParseGroupConfig for independent typed input copies and embedded
   the request in the browser document to avoid field drift. Additional behavioral
   REDs expose inherited-property lookups for opaque `constructor`/`__proto__` IDs
   and an unintended group property on an ordinary node
   (`/tmp/p3t15-protocol-opaque-ids-red.log`). Map-based ownership lookup and the raw
   node type fix them; Unicode IDs/config text remain intact. gofmt/vet/diff checks
   pass. No unrelated test timing or source was changed.

After review/fixes: full domain/layout and layouttest pass; the affected input,
protocol, Reader/root and reviewer suites pass under browser-required -race (root
31.804s; `/tmp/p3t15-protocol-final-race.log`). The entire browser-required
adapter/renderreview package passes (217.907s), dashboard embedding tests pass,
make ui rebuilds the render asset, and TypeScript passes. Focused arranged/render
Vitest: 17 files / 391 tests pass. Two full dashboard runs each pass 91/92 files and
1398/1399 tests, failing different unchanged tests: QueueView's 101st-task 5s timeout,
then GraphView's asynchronous recenter spy (zero calls at its immediate assertion).
Both files pass together in isolation (37 tests). Logs:
`/tmp/p3t15-protocol-final-dashboard{,-repeat}.log`,
`/tmp/p3t15-protocol-unrelated-isolated.log`. These broad flakes are recorded, not
suppressed or reported as full-suite success. The complete Go/test-render/test-all
release gates are not executed/closed for this partial migration.

## Captured-input validation execution slice (partial task)

Selection enumerated all 33 task files in numeric phase/task order; P3T15 is the
only task with unchecked steps. The previously recorded positive-capture blocker
was not retried. This pass completed another independent 15.1 prerequisite: nil,
empty, edited or wrong-revision ownership/configs cannot silently reach Arrange's
ordinary-box candidate sink or pass Geometry.Validate/Compatible/Preflight. The
new nested checkbox records only this input-integrity portion. No parent step,
v2 placement/validation, OverlayBox, drawing, raster review or release gate is
claimed complete. The existing 15.2–15.4 producer/drawing/reviewer dependency under
the positive-capture step remains unresolved; no other unchecked task file exists.

BuildGraph retains the existing AnalyzeGroups result privately and exports an
independent clone of that same GroupGraph. The normal Worker → Coordinator →
BuildGraph → Arrange path and stored-value validation consume the comparison;
there is no new ownership model, graph reachability algorithm, SQL column,
composition-root option or authoring-policy change. Exact detached copies remain
legal; later edits to the original manifest (including nested child inputs) cannot
change the captured graph. Invalid geometry callers retain ErrInvalidGeometry
while also receiving the specific ErrInvalidGroups cause.

Strict TDD:
- `/tmp/p3t15-group-input-boundary-red.log`: compiling tests demonstrate ordinary
  candidates returned for absent/edited group input, config/task order changes,
  malformed scopes, nested body/parent/owner mismatches and extra ordinary-graph
  ownership. Each now returns ErrInvalidGroups and zero Geometry.
- `/tmp/p3t15-group-geometry-input-red.log`: existing candidate validation accepts
  missing/stale ownership before the new guard; the tests now assert typed errors,
  incompatible display input and a single blocking preflight coverage finding.
- `/tmp/p3t15-guard-validation-mutation.log`: bypassing the comparison reproduces
  both failures. `/tmp/p3t15-guard-root-input-mutation.log`: removing the exported
  map after real BuildGraph capture changes SQLite's terminal failure stage from
  rendering to arranging in all three actual-root forms. These are sink failures,
  not constructor-call assertions. All mutations are restored. This proves the
  input guard is wired, NOT successful reviewed group geometry.

Ordered self-review before final verification:
1. Spec: restricted to the explicit nil/empty/mismatched input requirement. Kept
   all ordinary-box stopgap tests, current format/contract and incomplete v2 gates;
   added no layout feature, flag, dependency, retry policy or passing-review fake.
2. Anti-green-theater: tests call real Arrange, Validate, Compatible and Preflight
   and assert rejection at their sinks, not an intermediate comparison helper.
   Missing input and guard removal are behavioral failures; browser-required root
   mutation reaches the actual Worker/SQLite sink. Positive exact-copy and
   subsequent-save cases prevent an always-reject implementation.
3. Quality: reused the existing group clone and typed errors, kept authority in
   the same existing GroupGraph type, added descriptive subtests and ran gofmt.

After review: specified BuildGraph/GroupGeometry/MeasureRequest tests plus the new
Arrange tests pass; full domain/layout and layouttest pass. Full domain/layout
passes under -race (22.718s). Browser-required affected actual-root/authorized
ownership suites pass under -race (10.237s), including all three forms. Specified
arranged/job Vitest selection passes 15 files / 371 tests; tsc passes. Logs:
`/tmp/p3t15-guard-{domain-race,root-race,ts}.log`. No dashboard source changed;
real browser tests use the existing built production surface. Full-task
Go/test-render/test-all release gates remain open, not reported as green.

## Measured header sizing execution slice (partial task)

P3T15 remains the only task with unchecked steps. The previously blocked positive
capture gate was not retried as an implementation strategy. Completed the independent
header-sizing prerequisite of 15.1: original label/type and measured fan-out/metadata
blocks now determine the real NodeBox size, including nested groups. Existing port
reservations, routing, original text and manifest bytes are preserved. Preflight checks
all these blocks against the actual header, reporting missing metrics or clipping.
The v2 footer/task placement, combined live OverlayBoxes, drawing/review and descendant
work remain unchecked; the existing missing-producer dependency is recorded above.

Behavioral REDs: `/tmp/p3t15-header-red.log` shows ignored measured width/height and
missing/corrupt metrics accepted by Arrange. `/tmp/p3t15-header-root-red.log` shows the
actual browser/worker/SQLite sink persisting a 60px header when its measured four-block
text needs 95px. `/tmp/p3t15-header-preflight-red.log` shows missing and oversized nested
supplements escaping preflight. All pass after implementation. Bypassing the production
nodeSizes reservation reproduces the exact 60-versus-95 SQLite failure
(`/tmp/p3t15-header-root-mutation.log`); mutation restored.

Ordered self-review:
1. Spec: scoped to measured header reservation and its clearance check, with no new
   options, format blessing, ordinary-box review acceptance or false group-layout claim.
   The original stopgap tests remain. Footer/task/live reservations stay incomplete.
2. Anti-green-theater: the actual installed browser receives the captured saved input;
   its real metrics reach the persisted candidate and Reader sink. Bypassing sizing
   fails that sink. Unit tests also require size sensitivity, complete primary text,
   immutable manifests, real routes and positive preflight before corrupting metrics.
3. Quality: shared the existing canonical header text producer between requests, sizing
   and preflight; reused metric validation, contract padding and existing rounding,
   node/label size types and finding criteria. No guessed glyph sizes or missing-metric
   defaults. gofmt and diff checks pass.

After review, specified BuildGraph/GroupGeometry/MeasureRequest commands plus new
header tests pass; domain/layout and layouttest pass. Full domain/layout -race passes
(23.551s); browser-required real-root group/ownership -race passes (10.792s). Specified
arranged/job Vitest selection passes 15 files / 371 tests and tsc passes. Logs:
`/tmp/p3t15-header-domain{,-race}.log`, `/tmp/p3t15-header-root-race.log`, and
`/tmp/p3t15-header-root-green.log`. No dashboard source or embedded assets changed.
Full v2 and 15.6 release gates remain open; this task is not complete.
