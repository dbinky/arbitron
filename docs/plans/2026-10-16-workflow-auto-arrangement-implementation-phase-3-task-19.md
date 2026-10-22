# Workflow Auto-Arrangement — Phase 3 Task 19: Group runtime inputs, child layouts and route mounts

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§6.19–6.20, and [P3T15 geometry/input interfaces](2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-15.md#exact-geometryinput-interfaces-p3t15-declarations-p3t16-validation-p3t17-report-declarations).

**Spec/design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; matching design phases 1–3. Go 1.25.5, React 19/xyflow 12 and the existing browser/DOM/raster pipeline.

**Global constraints:** Arrange every committed authoring save asynchronously; five total attempts by default; preserve execution and every manifest edge occurrence; fence by saved revision/generation; share reviewed geometry across workflow/instance/review surfaces; retain best/fallback and truthful issues/Retry on failure. No manual positioning, opt-out or automatic processing of untouched legacy workflows.

**Order:** P3T15 (captured input) → P3T16 (geometry) → P3T17 (drawing/capture) → P3T18 (review/storage) → P3T19 (live views) → P3T20 (root/release). These replace former P3T15 stages 15.2–15.6. Each task owns its green checkpoint; successor acceptance is not a prerequisite.

**Strict TDD:** For each numbered cycle write the named assertions first, run the focused command and observe behavioral RED against compiling code, implement the minimum, rerun GREEN and refactor. For already-correct behavior remove its production guard/feed temporarily and observe the sink assertion fail, restore it and rerun. Compiler errors, constructor counts and browserless skips do not prove the behavior. Do not check a completion box during this docs review.

**Goal:** Preserve reviewed root/child geometry while feeding live group state, independent child layout polling and original selection/follow/callouts through the real routes.

**Files:** new `dashboard/src/hooks/useInstanceLayoutTiles.ts` and `graph/arranged/composeArrangedTiles.ts`, adjacent tests; `api/client.ts` and cancellation test; `graph/useGraphLayout.ts`, `ArrangedGraph.tsx`, `ExecutionView.tsx`, `WorkflowReview.tsx`, `useFollowActiveNode.ts`, `CalloutConnector.tsx`, `nestedGraphs.ts`; actual route/ReactFlow tests. P3T17 already owns the live-renderer props and buildArrangedFlow signature; consume them rather than declaring a second API.

**Sources:** GraphState.layout/ExecutionContext.layout are GroupGraph ownership from `domain/engine/graph_state.go:GraphStateReader`, not arrangement. Frozen config/task text comes only from each saved snapshot. `aggregateState.ts:buildAggregateView` supplies GroupLiveSummary/NodeContextInfo/executionStates; each child's arrangement comes from its existing authorized instance endpoint. WorkflowReview static ownership comes from joined LayoutView.groups. No current-workflow lookup authorizes a historical child.

## 19.1 Independent child layout polling

Produce this hook in useInstanceLayoutTiles.ts:

```ts
export function useInstanceLayoutTiles(contexts: readonly ExecutionContext[]): {
  views: ReadonlyMap<string,LayoutView>; errors: ReadonlyMap<string,string>
  refresh: (instanceId:string)=>void
  retry: (instanceId:string)=>Promise<void>
  retrying: ReadonlySet<string>; retryErrors: ReadonlyMap<string,string>
}
```

- [x] **RED:** hook tests drive two grouped child snapshots pending→terminal while execution is idle/completed; distinct revisions/generations and unchanged root data. Queue more than four children, assert at most four actual child GETs in flight; old A→B→A responses/retries cannot settle a new context epoch. In-band unavailable preserves only the same child's compatible geometry and backs off. RetryAck installs a minimum generation before refetch, so stale terminal reads cannot stop polling.
- [x] **GREEN:** Reuse P3T11 polling/backoff/epoch rules in a bounded scheduler (no dynamic hook calls in a loop). Caller selects visible workflow/spawn child contexts with their own grouped snapshot in GraphState.contexts order; root retains its existing hook, branches overlay parent nodes, and redacted/empty contexts make no child fetch. Join each response to that snapshot's saved revision, including explicit legacy fallback. Export the existing UseGraphLayoutInput/Output types and pure `computeGraphLayout` memo body for the next cycle.
- [x] Implement optional AbortSignal on `api.instances.getLayout(id,signal)` and pass it via existing request<T>(...,{signal}). Hook aborts removed/hidden/unmounted GETs, releases queue capacity and ignores late reads/retries with per-instance guards; cancellation is not an error that restarts a removed context. Do not claim to abort requests while only dropping their results. Test actual request helper signal forwarding with a pending fetch. Retry calls the existing retryLayout(id,expectedRevision), publishes per-tile busy/error state and never changes another tile/root.

## 19.2 Translation-only scene composition

Consume the exact LayoutTile and two-argument `composeArrangedTiles(tiles,graphInstanceId)` signatures in contracts §6.20; declare/export them in composeArrangedTiles.ts. Add `tiles?: readonly LayoutTile[]` to ArrangedGraphProps here. ArrangedGraph supplies its stable graph namespace, so the pure composer never fabricates a random/useId value.

- [x] **RED:** composer tests preserve each local GeometryDigest and all original node/group/edge/connector/text/attachment/crossing data after inverse translation; root origin is zero and children are disjoint below preceding full extents with GroupRowGapPx. Two children reuse every node/ordinal/task key, including Unicode/delimiter IDs; namespaced IDs/markers still resolve within their own tile. Inputs remain byte/value unchanged. Missing child layout renders its own fallback and cannot run dagre over the root or replace its view.
- [x] **GREEN:** Call P3T17 buildArrangedFlow for each compatible tile with joined view.groups, actual groupLive/nodeContexts/executionStates and layoutInstanceId. Qualify ReactFlow/marker identities per §6.20; translate all painted coordinates once. Keep local logical data IDs and data-layout-instance for assertions. Preserve actual executing branch instance/definitionNodeId for selectionFromNode, distinct from the scene's owning layout instance. Root and child remain separate reviewed revisions; persist/report no synthetic combined geometry.
- [x] Fallback tiles alone use extracted computeGraphLayout with fallbackPositions plus their own frozen ownership/live data. Characterize pure-extraction parity, partial retained positions and no writes. Compatible tiles never invoke it. Put per-tile titles/status panels/Retry controls in the separate navigation/status rail; arbitrary findings/titles cannot overlap reviewed graph ink or change its local geometry.

## 19.3 Real route mounts and live sinks

- [x] **RED:** actual WorkflowReview and ExecutionView/GraphView tests mount the shared renderer and real hooks. Assert frozen static geometry with no execution; queued/running/waiting/paused/unfinished tasks, winner, independent losing task, cancel request and redaction; task links/group inspection preserve original selection. Long activity/launch errors/waiting reasons/active lists remain fully available in GroupPanel/callouts without graph growth. The same live maps passed as empty must fail the visible-status assertions.
- [x] **GREEN:** Wire GraphView root/child snapshots plus per-context buildAggregateView maps into LayoutTiles and ArrangedGraph; static WorkflowReview needs no live maps. Replace composeLiveGraph's arranged-mode merged relayout. Remove unconditional groupFallback only for validated v2; nil/v1/wrong ownership remains explicit fallback and never a reviewed badge. Show each tile's actual hook view/error/retry state in LayoutStatusPanel; reviewed root status cannot hide an unreviewed child.
- [x] Test all-active follow/recenter and callout endpoints using actual rendered dimensions/qualified IDs across two tiles, plus child removal while GET/retry is pending, selection during retry and redacted contexts producing no identity/link/fetch. No mocked no-op setCenter or component-call-only assertion establishes this behavior. Browser release acceptance without graph/API fakes is P3T20.

## Verification and production wiring

All new components mount at existing WorkflowReview and ExecutionView roots, reached through initDomain-authorized HTTP layout and GraphStateReader routes. API data is the real saved-revision producer; the composer is presentation-only. A root/route feed set to nil must fail nonempty DOM geometry/live/selection assertions.

- [x] For each cycle observe RED, then GREEN: `cd dashboard && npx vitest run src/hooks/useInstanceLayoutTiles.test.ts src/graph/arranged/composeArrangedTiles.test.ts src/routes/WorkflowReview.test.tsx src/routes/ExecutionView.layout.test.tsx src/routes/GraphView.test.tsx src/graph/CalloutConnector.test.tsx && npx tsc --noEmit`. Add adjacent named tests before running and inspect the executed cases.
- [x] Run full dashboard tests and `make ui`. Temporarily omit groupLive, tile view and AbortSignal forwarding separately; visible status/geometry/cancellation sinks must fail. Restore, rerun and commit. P3T20 verifies the actual embedded HTTP→route/live subtree chain with real browser evidence.

## Execution record

One-task pass: P3T19. P3T16's previously recorded Unicode task-key blocker
remains unchanged: the real workflow parser permits only `[A-Za-z0-9_-]+` keys.
No authoring/execution identity validation was relaxed; that blocked step was
skipped in favor of this independent live-view slice. P3T20 is not started here.

Production wiring is GraphStateReader's authorized saved context snapshots →
GraphView → per-context buildAggregateView and useInstanceLayoutTiles → the
existing authorized instance layout client → ArrangedGraph → composeArrangedTiles
→ shared reviewed node/path components. The root retains useLayoutStatus.
WorkflowReview uses joined LayoutView.groups for static inspection as well as
reviewed drawing, without requiring the current group-analysis endpoint to succeed.
No new Go root, current-workflow authorization lookup, merged stored geometry,
authoring option or manual positioning path was added.

The bounded child scheduler carries actual AbortSignals through request/fetch,
releases removed requests' slots, fences late GET/retry epochs and generations,
retains only compatible same-snapshot outage geometry, and isolates busy/error
state. Children remain in captured context order. Scene identities encode canonical
UTF-8 tuples, including ordinary node element identities so author IDs resembling
synthetic elements cannot alias them. Every reviewed coordinate is translated on a
disposable copy; local input/digests stay unchanged. Only fallback tiles call the
extracted legacy layout function, with their own partial retained positions and
live ownership. Titles/status/Retry remain outside graph ink. Child fonts have
independent keyed loads/status; one failing child cannot invalidate another tile.

Behavioral RED/GREEN evidence (all mutations restored):
- `/tmp/p3t19-poll-red.log`, `p3t19-abort-red.log`, `p3t19-compose-red.log`:
  missing independent polling, bounded scheduling, cancellation and scene geometry.
  Hook fixtures use actual nonempty v2 grouped definitions/ownership/geometry.
- `p3t19-route-red.log`, `p3t19-static-red.log`, `p3t19-panel-red.log`,
  `p3t19-callout-red.log`: actual route DOM lacks reviewed groups/live rows,
  frozen group inspection, full active-node details and qualified child callouts.
- `p3t19-poll-refinement-red.log`, `p3t19-truth-red.log`,
  `p3t19-fallback-dom-red.log`, `p3t19-id-collision-red.log`,
  `p3t19-lane-red.log`, `p3t19-paint-layer-red.log`: stale retry acknowledgements,
  incompatible retained geometry, false child font verdicts, absent explicit
  fallback scope, canonical ID collision, lane inspection and routes raised above
  child card ink while the root alone is fallback.
- `p3t19-live-mutation-red.log`: removing actual groupLive feeds fails visible
  runtime rows, not a component-call assertion. `p3t19-view-mutation-red.log`:
  removing child hook views loses nonempty reviewed child DOM geometry.
  `p3t19-abort-mutation-red.log`: omitting request-helper signal forwarding fails
  pending-fetch cancellation and removed-request capacity assertions.
- `p3t19-owner-mutation-red.log`: dropping actual nodeContexts fails the original
  branch-owner trace endpoint sink. `p3t19-follow-mutation-red.log`: following only
  root work fails the real ReactFlow viewport against all three tiles' dimensions.
  Automatic follow/recenter use the real viewport with reduced-motion enabled;
  linked child callouts use actual mounted footer dimensions/qualified identities.

Ordered self-review completed:
1. Spec compliance: this slice only; root/child polling, frozen joins, presentation-
   only translation, independent fallbacks/status/fonts, original selection,
   all-active follow, live details/redaction and cancellation are wired at the
   existing routes. Nil/v1/wrong ownership never receives a reviewed badge.
2. Anti-green-theater: actual API-client/hook/route/ReactFlow DOM, route paths,
   markers, viewport, callout endpoints, group inspection and branch trace reads
   are asserted. Independent restored missing-feed probes fail those sinks.
   Epoch races include GET/retry removal and selection while retry is pending.
3. Quality: reuse the existing pure legacy layout body and shared renderer;
   graph-local reversible IDs, stable node/edge registrations, actual measured
   reviewed extents, independent font epochs, no dynamic hooks, no no-op input
   comments or success fakes. Updated only affected callback/qualified-ID fixtures.

Final gates: exact prescribed focused command passes 6 files / 130 tests; tsc
passes. Full dashboard passes 98 files / 1460 tests with `--maxWorkers=2`; make ui
rebuilds adapter/dashboard/dist; git diff --check passes. Default full-suite
concurrency encountered a five-second route timeout and cascading overlapping-act
failures under simultaneous host Go/browser load (plus one unrelated queue timeout).
The complete suite was rerun with bounded workers, not filtered, with no raised
timeouts or weakened assertions. The actual browser/server release gate remains
P3T20's responsibility; jsdom font/layout boundaries here are not claimed as that
release proof.
