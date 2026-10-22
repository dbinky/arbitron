# Workflow Auto-Arrangement — Phase 3 Task 16: Recursive group placement, full routing and preflight

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§6.19–6.20, and [P3T15 geometry/input interfaces](2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-15.md#exact-geometryinput-interfaces-p3t15-declarations-p3t16-validation-p3t17-report-declarations).

**Spec/design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; matching design phases 1–3. Go 1.25.5, React 19/xyflow 12 and the existing browser/DOM/raster pipeline.

**Global constraints:** Arrange every committed authoring save asynchronously; five total attempts by default; preserve execution and every manifest edge occurrence; fence by saved revision/generation; share reviewed geometry across workflow/instance/review surfaces; retain best/fallback and truthful issues/Retry on failure. No manual positioning, opt-out or automatic processing of untouched legacy workflows.

**Order:** P3T15 (captured input) → P3T16 (geometry) → P3T17 (drawing/capture) → P3T18 (review/storage) → P3T19 (live views) → P3T20 (root/release). These replace former P3T15 stages 15.2–15.6. Each task owns its green checkpoint; successor acceptance is not a prerequisite.

**Strict TDD:** For each numbered cycle write the named assertions first, run the focused command and observe behavioral RED against compiling code, implement the minimum, rerun GREEN and refactor. For already-correct behavior remove its production guard/feed temporarily and observe the sink assertion fail, restore it and rerun. Compiler errors, constructor counts and browserless skips do not prove the behavior. Do not check a completion box during this docs review.

**Goal:** Produce complete measured v2 group geometry, including live reservations and all real/synthetic routes, without changing the saved execution graph.

**Files:** create `domain/layout/group_layout.go`, `group_preflight.go`; modify `group_geometry.go`, `group_size.go`, `arrange.go`, `geometry.go`, `preflight.go`, `refine.go`, `size.go`, `coordinator.go`, `contract.go`; tests `arrange_group_test.go`, `group_geometry_wire_test.go`, `group_size_test.go`; dashboard `arranged/{types,validate,testFixtures}.ts`, `contract.json`, `groupText.contract.json` and validation/contract tests. Update only affected consumer fixtures/expectations, not successor implementations.

**Interfaces:** Consume P3T15 Graph.Groups, GroupElement/Lane/Connector/Crossing, captured measurement items and existing ordinary rank/order/route helpers. Produce `arrangeGroups(g Graph, m Measurements, c RendererContract, o Options) (Geometry,error)` in group_layout.go, `preflightGroups(g Graph, geo Geometry, m Measurements) []Finding` in group_preflight.go plus `CrossingCount(geo Geometry) int` and the private groupConnectorID helper in contracts §6.20. Arrange dispatches only for nonempty validated Groups. Geometry.Validate/Compatible and TS validateGeometry use the same v2 coverage/attachment rules.

## 16.1 Format, structural validation and measured reservations

- [x] **RED:** Extend `TestGroupGeometry_WireAndValidation`, `TestGroupGeometry_WireRoundTrip` and `groupGeometry.contract.test.ts` with valid branches/workflows/nested geometry and ordinary v2 geometry. Assert exact v2 format, full Go↔JSON↔TS contract equality, every real ordinal and ElementID, all group arrays, canonical Unicode IDs and full-digest sensitivity. Drop each footer/lane/card/connector/ownership field or change config/task order: reject it; v1 group boxes and missing ownership never qualify as reviewed. Ordinary group arrays remain empty.
- [x] **RED:** `TestGroupSizing_ReservesCombinedLiveRows` measures the complete §6.20 rows: long valid ASCII task keys (`[A-Za-z0-9_-]+`) and their winner rows, with Unicode display strings (workflow titles, branch node labels, task-port labels), each count 0..N, all simultaneous badges and frozen status, footer not_started and redacted card title. Missing/zero/nonfinite metrics fail sizing, input maps remain unchanged. Compare row sums and actual measurement values, not the sizing function's own output. One large badge cannot stand in for the whole visible block.
  > AMENDED (2026-10-21, product owner): task keys are identity and stay ASCII-only. The validation in `domain/workflow/group.go` (`groupTaskKeyRE`, `ParseGroupConfig`) is unchanged. Unicode is exercised through display strings instead. The test uses a 400-character valid key, a Unicode workflow title on every workflows-form card, and Unicode branch node labels and task-port labels. It also asserts every progress count 0..N per kind, a winner row per task, the redacted title, and rejection of empty, NaN and +Inf metrics. The two earlier BLOCKED notes about Unicode keys are resolved by this amendment and removed.
  > Evidence (2026-10-21): the GREEN sizing was already in place, so the strengthened test passes. Its sensitivity was checked by mutation, with each mutation reverted afterwards. Dropping the `not started` progress rows from `groupTextItems` fails with "count 0 of not started", and replacing the measured workflow title fails with "the Unicode workflow title is measured verbatim".
- [x] **GREEN:** Migrate FormatV2/ContractVersion, exact group contract fields from P3T15, ordinary and group attachment ElementIDs, Go/TS structural wire fixtures and validation together. Implement combined measured OverlayBox sizing from groupTextItems; extend its vocabulary/golden cases only where needed (§6.20). Do not regenerate group PNGs before P3T17/P3T18 supply the drawing/reviewer.
- [x] Replace obsolete ordinary-box arranger expectations with complete positive geometry assertions here. Update client negative compatibility fixtures impacted by v2 here; positive actual DOM belongs to P3T17. Do not postpone necessary fixture changes to P3T20 or weaken nil/v1 fallback controls.

## 16.2 Recursive placement, routing and findings-driven refinement

- [x] **RED:** `TestArrangeGroups_DownwardAndComplete` covers linear→group→rejoin, duplicate child workflow names with distinct task keys/order, nested branches/workflows, loop inside a lane, disjoint groups, long labels and identical real parallel edges. Assert no unrelated overlaps/contacts, full text/overlay containment, deterministic complete routes/crossings and unchanged original manifest JSON.
- [x] **GREEN:** Lay out innermost groups as atomic rectangles in the parent lane; author-order task lanes go left-to-right, header above all lanes, footer below their complete text/node/loop/route extents, continuations below footer. Produce one container/header/footer per group; one lane per task; branches have a measured lane-caption element, workflows have a measured task card. Header NodeBox draws once. Container caption is inside its measured box; lane caption/overlay rows have explicit clearance from body nodes. No sequential column wrap or live-state-dependent sizing.
- [x] Derive workflows header→card→footer and branches immediate-sink→footer connectors from captured AnalyzeGroups ownership/continuation edges, treating nested groups at their footer. A closed loop without a sink has no invented exit. Every real task edge remains real; synthetic edges remain presentation only. Route both classes together; containment outlines may surround/cross their own contents (§6.20), while unrelated lane interiors/text/node/card boxes are obstacles. Score complete routed candidates, including mixed crossings, with the existing bounded search.
- [x] **RED→GREEN:** `TestCoordinator_GroupCrossingsAffectBestScore` and direct candidate-ranking tests distinguish geometries with equal real crossings but different connector/mixed crossings. Produce CrossingCount and pass it in both reviewed and unreviewed Coordinator ScoreOf paths; retained SQLite Candidate.Score must include the total. A render failure may retain an unreviewed candidate here, but must not zero out its mixed crossing cost. P3T18 extends the reviewer advisory count using the same helper.
- [x] **RED→GREEN:** `TestPreflightGroups_RejectsIncompleteAndCollidingGeometry` removes a footer/card, mis-owns a body, routes through a sibling card, omits a connector/mixed crossing and detaches a label. Each fails the specific validation/preflight sink. A zero/negative/absent group metric is an error, not a default. Test cancelled Coordinator entry/between-stage contexts; Arrange itself retains its existing context-free signature.
- [x] Preflight checks exact ownership, nesting, order, text/route coverage, ports, clearance, live reservations and canvas. Findings use existing criteria and real GroupNodeID, with element/connector IDs in Evidence and no fabricated ordinals. Effective task gap is GroupTaskGapPx + max(0,o.NodeSpacing-DefaultOptions().NodeSpacing), row gap is GroupRowGapPx + max(0,o.RankSpacing-DefaultOptions().RankSpacing), padding is GroupPaddingPx + o.NodeTextSlack. A blocking finding must drive an actual changed group candidate through Refine; test the changed geometry.

## Wiring, data and verification

Existing initDomain → Worker → Coordinator → Arrange supplies captured Graph.Groups and real BrowserRenderer measurements; no new root constructor is needed. Add a root measurement/retained-result test by extending `cmd/arbitron/layout_groups_e2e_test.go:TestLayoutGroups_RealBrowserMeasuresCapturedGroupText`: save a nonempty group through Service, let the real coordinator consume browser measurements and assert complete v2 group boxes/OverlayBoxes in its retained SQLite candidate and Reader. At this intermediate checkpoint review may explicitly fail until P3T17/P3T18; assert the exact known rendering/reviewing failure plus nonempty geometry, never accept any arbitrary terminal state. Missing Groups, group measurements or Arrange dispatch must fail the geometry sink. P3T20 later requires passed review.

- [x] Run RED/GREEN for each cycle: `go test ./domain/layout -run 'TestGroupGeometry|TestGroupSizing|TestArrangeGroups|TestPreflightGroups' -count=1`; `cd dashboard && npx vitest run src/graph/arranged && npx tsc --noEmit`.
- [x] Run `go test ./domain/layout -count=1` and the new focused tests under `-race`; rebuild with `make ui` before the root browser-metric test, run it with `ARBITRON_REQUIRE_BROWSER=1`. Remove group dispatch and a measurement lookup separately to prove the persisted geometry sink fails; restore, rerun and commit only the green producer slice.

## Execution record (2026-10-19)

One-task pass: P3T16. The Unicode-key sizing assertion above remains blocked on
an explicit plan/domain-validation decision; no authoring/execution validation
was relaxed. All other producer steps are implemented and verified. The sizing
test exercises long valid ASCII keys, Unicode workflow titles, every configured
count/status/winner alternative, heterogeneous measured row heights, simultaneous
badges, frozen status, redacted title, immutable inputs and missing/zero/nonfinite
metrics. No Unicode task-key GREEN is claimed.

The existing initDomain → Worker → Coordinator → Arrange path now produces v2
geometry from captured ownership and actual browser measurements. Recursive
atomic regions reuse ordinary rank/order/placement and channel fan ordering;
real and synthetic routes share measured obstacles, bounded complete-candidate
ranking and mixed crossing annotations. Overlay reservations are stacked measured
rows. Redacted task titles have a separate node-label-style measurement keyed by
`GroupTextKey(taskElementID,"redactedTitle")`, rather than reusing badge typography.
Go/TS validation and shared five-form wire fixtures include all rendered endpoint
IDs. Explicit empty synthetic NodeID differs from missing wire ownership.

Behavioral RED/GREEN evidence:
- `/tmp/p3t16-format-sizing-red.log`: v1 output and absent actual group blocks.
- `/tmp/p3t16-ts-v2-red.log`: full Go/TS contract migration mismatch.
- `/tmp/p3t16-placement-red.log`, `p3t16-preflight-ports-red.log`,
  `p3t16-arrow-red.log`, `p3t16-self-red.log`: actual clearance, port, marker-canvas
  and measured self-loop failures before the corresponding fixes.
- `/tmp/p3t16-score-red.log`: reviewed/unreviewed scores omit mixed crossings and
  retain the wrong candidate. Both paths now use CrossingCount; the real root
  asserts a nonzero mixed cost in SQLite and the exact geometry in Reader.
- `/tmp/p3t16-empty-owner-red.log`: a missing synthetic nodeId was indistinguishable
  from explicit empty ownership before the wire decoder guard.
- `/tmp/p3t16-dispatch-{unit,root}-red.log`: removing Arrange dispatch loses complete
  group geometry at both sinks. `/tmp/p3t16-metric-{unit,root}-red.log`: replacing
  actual overlay metrics with constants fails the persisted combined-row height
  assertion (104px expected, 22px observed). Mutations restored.
- `/tmp/p3t16-consumer-fixtures-red.log`: ordinary client font/capture fixtures still
  expect v1. Only affected fixture contracts/negative controls were migrated;
  the real sink assertions and argument-checking font doubles remain intact.

Ordered self-review completed before commit:
1. Spec: no successor drawing/report/reviewer/live-view implementation, PNG fixture
   regeneration, new runtime flag, root constructor or authoring-policy change.
   Only the impossible Unicode-key gate remains open. Ordinary v1/nil/wrong-owner
   rejection controls are preserved, not replaced by passing ordinary group boxes.
2. Anti-green-theater: actual Service saves feed installed BrowserRenderer metrics
   into retained SQLite boxes, exact aggregate row dimensions and nonzero mixed
   score, then Reader's identical digest. Independent dispatch/metric mutations
   fail those sinks. The intermediate checkpoint asserts exactly needs_attention,
   no_refinement, and the existing review identity failure caused by reconstruction
   without captured ownership. It does not accept arbitrary terminal failure or
   claim successful group drawing/review. P3T17/P3T18 own those successors.
3. Quality: deterministic canonical identities and route ordering, lexicographic
   contacts/crossings/length costs rather than magic penalties, ordinary channel
   fan ordering, measured right-side self-loop ports, shared arrow bounds and no
   fabricated connector ordinals in findings. Removed unused helpers/parameters;
   gofmt, TypeScript checking and diff whitespace checks pass.

Final verifications: specified focused Go tests pass; full domain/layout passes
(11.545s); focused tests including Coordinator scoring/cancellation pass under
-race (22.790s). Arranged Vitest passes 14 files/349 tests; affected render/route/
callout fixtures pass 12 files/201 tests; tsc passes. make ui rebuilt embedded
assets. Browser-required root measurement and all three root ownership forms pass
under -race (33.322s; `/tmp/p3t16-root-budget-green.log`). Their old five-second
terminal helper was insufficient after these tests began doing actual captures
(two observed deadline failures, not production verdict failures). These two tests
now share the existing bounded 30-second measurement/capture test budget; all exact
saved-result, score, ownership, verdict and failure-evidence assertions are retained.
Full release/screenshot-success gates remain owned by P3T17–P3T20.
