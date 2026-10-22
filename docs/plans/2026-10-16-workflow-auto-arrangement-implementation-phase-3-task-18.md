# Workflow Auto-Arrangement — Phase 3 Task 18: Group reviewer, evidence and authoritative stored results

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§6.19–6.20, and [P3T15 geometry/input interfaces](2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-15.md#exact-geometryinput-interfaces-p3t15-declarations-p3t16-validation-p3t17-report-declarations).

**Spec/design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; matching design phases 1–3. Go 1.25.5, React 19/xyflow 12 and the existing browser/DOM/raster pipeline.

**Global constraints:** Arrange every committed authoring save asynchronously; five total attempts by default; preserve execution and every manifest edge occurrence; fence by saved revision/generation; share reviewed geometry across workflow/instance/review surfaces; retain best/fallback and truthful issues/Retry on failure. No manual positioning, opt-out or automatic processing of untouched legacy workflows.

**Order:** P3T15 (captured input) → P3T16 (geometry) → P3T17 (drawing/capture) → P3T18 (review/storage) → P3T19 (live views) → P3T20 (root/release). These replace former P3T15 stages 15.2–15.6. Each task owns its green checkpoint; successor acceptance is not a prerequisite.

**Strict TDD:** For each numbered cycle write the named assertions first, run the focused command and observe behavioral RED against compiling code, implement the minimum, rerun GREEN and refactor. For already-correct behavior remove its production guard/feed temporarily and observe the sink assertion fail, restore it and rerun. Compiler errors, constructor counts and browserless skips do not prove the behavior. Do not check a completion box during this docs review.

**Goal:** Require actual rendered readability of every v2 group element/connector before a pass, persist it unchanged and expose the same truthful result/fallback from both revision queries.

**Files:** `adapter/renderreview/reviewer{,_checks,_capture}.go` and group reviewer tests; genuine immutable fixtures under `adapter/renderreview/testdata/readability/`; `domain/layout/{reader,requester,query}.go` and tests only where v2 compatibility needs extension; `domain/layout/layouttest` doubles; extend existing SQLite/cache JSON round-trip tests. No new SQL group columns or separate store.

**Interfaces:** Consume P3T17 RenderReport group paint/text/route/crossing fields and `layout.GroupProbeNames(req.WorkflowGroups)`, P3T16 complete Geometry and P3T15 captured ownership. Review(ctx, ReviewRequest) and HasRenderedEvidence(Geometry,*Review) retain their existing signatures. Evidence binds the whole v2 digest/full contract and artifacts; do not invent report fields inside Evidence. The reviewer examines the captured report/probe set, and its truthful findings/Rendered/Evidence are what Reader/Requester validate later.

## 18.1 Reviewer integrity, actual ink and state coverage

- [x] **RED:** `TestReadabilityReviewer_GroupsRequireCompleteActualEvidence` uses genuine P3T17 captures for branches/workflows/nested groups. Compare independent WorkflowGroups ownership and exact candidate digest, all element/text/route IDs, actual attachment/arrow/crossing data, paint (including §6.20 exact baseline fills/outline), containment/clearance and full tile union. Zero border or transparent card fill must not turn required paint checks into no-ops. The group header appears once in real-node coverage plus its supplements; parent containment is not a collision with its own children.
- [x] **GREEN:** Extend existing integrity/DOM/raster checks to every group element, including empty-interior container/lane outlines, complete text glyph regions, synthetic dashed corridors, markers and mixed gaps at existing tolerances. Use P3T16 CrossingCount for total rendered crossing advisories, so synthetic crossings do not disappear from candidate scoring. Map connector defects to owning GroupNodeID/connector ID, never a fabricated manifest ordinal. A clean report or present PNG is insufficient evidence.
- [x] **RED→GREEN:** `TestReadabilityReviewer_GroupRasterMutationsBlock` erases only container/lane border pixels, a footer label, card background, synthetic middle segment, marker or crossing gap; update valid hashes and keep the clean DOM report. Each must produce a blocking finding. Missing/unreadable/hash-mismatched PNG is ErrEvidenceUnavailable. Use complete local tile regions; report metadata alone cannot pass the mutation.
- [x] `TestReadabilityReviewer_GroupRuntimeProbes` requires the exact GroupProbeNames set for the captured graph. Omit one, return false flags, clip only a live row, obscure only a winner, or disclose a redacted dependency: actual browser/report controls must block. Include run gathering/resolving_conflicts/done and task unfinished, not just generic node states. Compare dynamic text to the measured case vocabulary/rows; static baseline text/paths remain exact. Ordinary geometry requires no group cases.

## 18.2 Storage, read truth and retry

- [x] **RED:** extend Reader/Requester and existing SQLite/cache round trips with nonempty group arrays, full digest/contract, exact revision/generations and genuine review evidence. Missing ownership/group geometry, v1 history, corrupt/missing referenced result, outage and wrong contract yield explicit unreviewed fallback/attention and actionable retry, never complete with absent compatible geometry. A worse retry retains a superior compatible best and its own findings/evidence; cancellation/later-attempt errors retain prior complete geometry.
- [x] **GREEN:** Reuse existing JSON persistence and fenced invalidation/retry. View.Groups must still derive from the same authorized immutable workflow/snapshot independent of Store/cache availability. P3T15 already implements that input and the narrow ErrInvalidGroups query exemption; characterize it rather than duplicating ownership logic. Invalid saved groups remain inspectable with failed status/Retry; malformed basic nodes/edges still reject; authoring/execution validation remains intact.
- [x] Update argument-honoring unit doubles with complete nonempty v2 report/evidence for the actual supplied ownership/geometry, requiring and copying the new inputs. Missing/wrong group input rejects; no test fake is installed at initDomain. Regenerate genuine browser success fixtures through P3T17's built production surface; never bless hand-painted rectangles as success.

## Root, data and verification

The installed initDomain renderer/reviewer are unchanged constructors: saved input → real BrowserRenderer/RenderSurface report+PNG → extended Reviewer → Worker → SQLite. Add `TestLayoutGroups_ReviewerAndStoreTruth` through that real root, reading nonempty group arrays/digest/evidence from SQLite, Reader and authorized workflow/snapshot queries; removing actual group report transport/review checking must fail an independent missing-ink/input control. This checkpoint proves server group review/storage, while P3T20 adds both real route/live subtree acceptance.

- [x] Run focused RED/GREEN tests and negative controls, then `make ui`; `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview -run 'TestReadabilityReviewer.*Group' -count=1 -v`; `go test ./domain/layout/... ./adapter/sqlite ./adapter/filesystem -run 'Test.*Group|Test.*Retained|Test.*Retry|Test.*Layout' -count=1`. Verify every new named test executes.
- [x] Run affected reviewer/Reader/Store tests under `-race` and the new real-root reviewer/store sink with browser required. Recheck manifest bytes and previous presentation after errors. Commit only after all applicable controls are green.

## Execution record (2026-10-20)

One-task pass: P3T18. P3T16's recorded Unicode task-key validation blocker is
unchanged; it was skipped rather than re-failed. No P3T19 live callers/tiles or
P3T20 release implementation is included.

The installed reviewer now consumes the actual group report and captured ownership,
requires complete element/text/connector/crossing and runtime-case coverage, checks
baseline paint, attachments, arrows, containment, clearance, mixed path gaps and
actual tile ink, and issues total CrossingCount advisories. Synthetic findings retain
real group owners and logical connector/text identities, never invented ordinals.
The existing root constructors, JSON stores, fences, Reader and authorized queries
remain in use. ReviewRequest.ValidateGeometry is a review-boundary helper, not a new
ownership producer: presentation nodes do not carry the workflow entrypoint needed
for re-running AnalyzeGroups. It validates geometry against independently captured
ownership and checks that bodies, parents, nested order and continuations agree.

Real built-surface branches/workflows/nested/loop captures and their production
review evidence are immutable fixtures. Affected ordinary fixtures were migrated
to v2/ElementID and regenerated by the actual browser, retaining the v1 negative
control and deliberately merged-port scenes. Unit doubles now require/copy group
inputs and bind complete reports to the supplied geometry; no fake is installed at
initDomain. A previously shallow group copy in the memory test store is fixed.
SQLite and filesystem cache group round trips, Reader/Requester failure/retry truth,
cancellation checkpoints, worse-retry retention and immutable authoring bytes are
characterized without adding SQL columns or duplicating query ownership analysis.

Behavioral RED/GREEN evidence (all production mutations restored):
- /tmp/p3t18-evidence-red.log: captured group identity and runtime coverage fail
  before implementation. /tmp/p3t18-captured-map-red.log: wrong parent/nested order/
  continuation were accepted before the review-boundary consistency checks.
- /tmp/p3t18-raster-red.log: clean DOM plus valid hashes accepted erased group
  outline/text/fill/connector/marker/gap ink. /tmp/p3t18-real-mixed-gap-red.log proves
  the real-under/synthetic-over gap criterion independently of connector crossings.
- /tmp/p3t18-probe-red.log: omitting required group cases passed when the coverage
  guard was disabled. /tmp/p3t18-runtime-owner-red.log exposed missing owners on
  actual group text/connector runtime findings. Real browser row clipping, winner
  contrast loss and redacted-identity disclosure now block review at the sink.
- /tmp/p3t18-reader-red.log and /tmp/p3t18-retain-red.log: disabling compatibility or
  best-result retention exposes incompatible geometry or replaces generation 1
  with a worse retry. /tmp/p3t18-storage-red.log exposes aliased stored group text.
- /tmp/p3t18-doubles-red.log and /tmp/p3t18-boxed-counterfeit-red.log: group doubles
  reject all groups initially; ordinary-box counterfeit headers later exposed the
  missing ownership guard. Complete positive and missing/wrong-input cases pass now.
- /tmp/p3t18-root-transport-red.log: removing BrowserRenderer group report transport
  fails the SQLite/authorized-query success sink. /tmp/p3t18-root-review-red.log:
  removing group raster review incorrectly publishes a pass after erasing ONLY a
  group outline, while real-node ink and the clean DOM report remain unchanged.
- /tmp/p3t18-contacts-red.log proves actual body/lane containment and supplemental/
  branch glyph collision findings, rather than merely a generic mismatch.

Ordered self-review completed before commit:
1. Spec: no extra runtime flags, ownership service, store, execution-policy change or
   live-view implementation. Full contract/digest/artifacts are bound without adding
   report fields to Evidence. Existing input/outage/invalid-history logic is reused.
2. Anti-green-theater: actual Service saves feed the installed browser and reviewer,
   then SQLite raw JSON, Reader and both authorized immutable revision queries.
   Missing group transport and missing ink independently fail those durable sinks.
   Argument-honoring doubles are used only in unit tests, not root acceptance.
3. Quality: shared real/synthetic route-gap and pixel-corridor helpers, deterministic
   logical IDs/order, unique crossing owners, deep copies of all group arrays,
   explicit baseline-paint constants, no missing-input no-op or fabricated ordinal.
   gofmt and git diff --check pass; task tests rerun after fixes.

Known capture concern, not a relaxed pass: P3T17's original long wrapped node label
has a browser Range extending beyond its text box. The unchanged 1.5px reviewer
containment tolerance rejects it. Passing genuine fixtures use fully contained
multiline Unicode labels; TestReadabilityReviewer_GroupActualWrapOverflowFailsClosed
preserves the original capture case and proves it is not blessed. Removing only the
text containment guard fails that test (/tmp/p3t18-wrap-containment-red.log). A future
renderer/Range correction must retain the readability gate; none is guessed here.

Final verification:
- make ui rebuilt the embedded production surface (/tmp/p3t18-ui-build.log).
- Exact browser-required reviewer selector executes eight top-level tests, no skips:
  /tmp/p3t18-reviewer-final.log (57.441s), and -race final (128.478s).
- Specified domain/layout/... + SQLite + filesystem selector passes after all fixes
  (/tmp/p3t18-storage-final.log); affected tests pass -race
  (/tmp/p3t18-storage-race-final.log). Full domain/layout/... also passes.
- Browser-required real-root reviewer/store positive and missing-report/ink controls
  pass under -race (71.935s, /tmp/p3t18-root-race-final.log), comparing full SQLite
  geometry/review JSON and both authorized queries with unchanged manifest bytes.
  Earlier expanded root -race also passes the browser-metric and reordered immutable
  revision Retry controls (152.411s, /tmp/p3t18-root-race.log).
