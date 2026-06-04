# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### ENGINE-001: Unconditional-clear invariant for Retry untested
**Area**: Engine retry resurface (`domain/engine/engine_dismissal_test.go`)
**Fixed**: Iteration 17
**Detail**: `TestEngine_RetryClearsDismissal` only tested the case where the instance WAS dismissed before retry. The `ClearForInstance` call is unconditional (idempotent no-op when no rows exist), but no test verified this. A future "optimization" that adds `if isDismissed { ClearForInstance() }` would violate the always-clear contract without failing any test. Added `TestEngine_RetryCallsClearEvenIfNotDismissed`: trigger → fail → retry WITHOUT dismissing first → assert clearedCount >= 1.

### LIST-002: All-dismissed edge case: empty-array vs null response untested
**Area**: Instance list dismissed-marking (`adapter/http/instance_list_dismissal_test.go`)
**Fixed**: Iteration 16
**Detail**: When all items in the window are dismissed and `includeDismissed=false`, the handler skips every item in its loop and returns the `make([]summaryResponse, 0, ...)` slice (non-nil). JSON serialization of a non-nil empty slice is `[]`, not `null`. Frontend code doing `.map()` or `.length` on the response must not receive `null`. `TestList_AllDismissed_ReturnsEmptyArray` verifies the response is 200 with a non-nil empty JSON array.

### LIST-001: Error-path coverage missing for List dismissed-marking
**Area**: Instance list dismissed-marking (`adapter/http/instance_list_dismissal_test.go`)
**Fixed**: Iteration 15
**Detail**: The List handler has two error returns in its dismissal section: `currentUser.CurrentUser(r)` fails → 500, and `dismissals.DismissedIDs(...)` fails → 500. Neither was tested; the error category was entirely absent for the list endpoint. Added `TestList_DismissalRepoError_500` (errDismissalRepo returns an error) and `TestList_UserResolutionError_500` (errUserRepo returns an error) to cover both paths.

### HAND-003: DismissAll state=completed path untested
**Area**: Dismissal HTTP handlers (`adapter/http/dismissal_handler_test.go`)
**Fixed**: Iteration 14
**Detail**: `TestDismissAll_DismissesEveryListedInCategory` only called `?state=failed`. `TestDismissAll_EmptyCategory_OK` used `state=completed` but with an empty repo. The `state=completed` path through the List+dismiss loop was never exercised with real data. Added `TestDismissAll_CompletedCategory_DismissesAll`: creates two completed instances and one failed, calls `state=completed`, asserts completed ones are dismissed and the failed one is not.

### HAND-002: isDismissibleState StateCompleted arm untested in Dismiss
**Area**: Dismissal HTTP handlers (`adapter/http/dismissal_handler_test.go`)
**Fixed**: Iteration 14
**Detail**: `isDismissibleState` returns true for both `StateCompleted` and `StateFailed`, but `TestDismiss_TerminalInstance_OK` only used `StateFailed`. A regression narrowing the function to only accept `StateFailed` would pass all prior tests. Added `TestDismiss_CompletedInstance_OK` to verify the `StateCompleted` arm of the guard returns 200 and records the dismissal.

### HAND-001: Cancelled-instance dismiss guard untested
**Area**: Dismissal HTTP handlers (`adapter/http/dismissal_handler_test.go`)
**Fixed**: Iteration 13
**Detail**: `isDismissibleState` has an explicit comment: "narrower than stream_handler's isTerminalState, which also includes cancelled; only completed/failed items can be dismissed." The existing 409 test (`TestDismiss_NonTerminal_409`) only used `StateRunning`, so a change that widened the guard to also allow `StateCancelled` (which IS terminal in the streaming handler's sense) would pass all tests. Added `TestDismiss_CancelledInstance_409` using `StateCancelled` to pin this intentional design boundary.

### REPO-002: ClearForInstance isolation untested
**Area**: Dismissal persistence (`adapter/sqlite/dismissal_repo.go`)
**Fixed**: Iteration 12
**Detail**: `TestDismissalRepo_ClearForInstanceAcrossUsers` verified clearing one instance removes it across all users, but never checked that OTHER instances' dismissals survive the call. A bug where the DELETE used `WHERE user_id = ?` instead of `WHERE instance_id = ?` would wipe all dismissals for a user but still pass the existing test. Added `TestDismissalRepo_ClearForInstanceDoesNotAffectOtherInstances`: dismiss two instances, clear the first, assert the second is still dismissed.

### REPO-001: DismissedIDs multi-instance iteration untested
**Area**: Dismissal persistence (`adapter/sqlite/dismissal_repo.go`)
**Fixed**: Iteration 11
**Detail**: Every existing test for `DismissedIDs` used 0 or 1 dismissed entries. The `rows.Next()` loop was never exercised with multiple entries, so a bug in that loop (early exit, missed rows) would not have been caught. Added `TestDismissalRepo_DismissedIDsMultipleInstances` which dismisses 3 instances for one user and asserts all 3 appear in the returned map.

### FOLLOW-002: Recenter-after-advance-while-paused behavior untested
**Area**: Follow-active-node + Recenter control (`dashboard/src/graph/useFollowActiveNode.ts`)
**Fixed**: Iteration 10
**Detail**: The implementation always updates `prevAnchorRef` even when `following=false`, so clicking Recenter after execution advances while paused jumps to the *current* anchor (where the node is now) rather than the stale pause-time position. The existing recenter test only covered the static case (single anchor, no intermediate advance). Added `useFollowActiveNode — recenter after advance-while-paused` to verify: animate to (100,200) → pause → advance to (300,400) while paused (no animation) → recenter calls setCenter with (300,400), not (100,200).

### FOLLOW-001: Null-anchor edge case untested
**Area**: Follow-active-node + Recenter control (`dashboard/src/graph/useFollowActiveNode.ts`)
**Fixed**: Iteration 9
**Detail**: The design doc lists "Empty graph / no anchor: skip centering (no-op); nothing throws" as an explicit edge case. The implementation guards with `if (!anchor) return` in the effect and `if (prevAnchorRef.current)` in recenter, but no test verified this behavior. Added `useFollowActiveNode — edge case: null anchor (empty graph)` with two tests: (1) null anchor from the start → no setCenter called; (2) recenter when anchor was never recorded → following re-engages but setCenter is not called.

### VIEW-002: Ghost-currentNodeId fallback untested at integration level
**Area**: Anchor selection + default zoom (`dashboard/src/routes/ExecutionView.tsx`)
**Fixed**: Iteration 8
**Detail**: `selectAnchorNodeId.test.ts` covers the case where `currentNodeId` is not in the graph (ghost ID, e.g. deleted node mid-run) returning the source node fallback. But `GraphView.test.tsx` had no integration test exercising this full chain: ghost ID → `selectAnchorNodeId` fallback → `anchorPos` resolved → `setCenter` called on source node. Added "falls back to centering on the source node when currentNodeId is not in the graph" to the `default-zoom centering on mount` describe block.

### VIEW-001: One-shot mount-centering guard untested
**Area**: Anchor selection + default zoom (`dashboard/src/routes/ExecutionView.tsx`)
**Fixed**: Iteration 7
**Detail**: The `didInitialCenter` ref prevents `setCenter` from firing a second time when the component re-renders (e.g. on each live WebSocket event). The existing mount-centering tests verified `setCenter` was called once on mount but never asserted it stayed at exactly 1 after a parent re-render. Added `GraphView — mount centering one-shot guard` describe block with a Harness that triggers an unrelated re-render and verifies `setCenterSpy` is still called only once total.

### EDGE-002: chooseSide never tested with asymmetric sourceX/targetX
**Area**: Loop back-edge rendering (`dashboard/src/graph/edges/LoopBackEdge.tsx`)
**Fixed**: Iteration 6
**Detail**: All three existing `chooseSide` tests used `sourceX === targetX`, meaning the midpoint averaging formula `(sourceX + targetX) / 2` was never exercised with unequal x values. Diamond loops in production can have tail and head nodes at different horizontal positions. Added a test covering: tie-at-center with asymmetric inputs (routes right), midpoint-right-of-center (routes left), midpoint-left-of-center (routes right).

### EDGE-001: LoopBackEdge fallback-box path untested
**Area**: Loop back-edge rendering (`dashboard/src/graph/edges/LoopBackEdge.tsx`)
**Fixed**: Iteration 5
**Detail**: The component has a fallback box derived from edge endpoints when `data` is absent or `data.box` is undefined. This path executes in production when `loopContainerBox` returns null (all body nodes unpositioned). Both cases were untested — the "error/failure" scenario category was missing. Added a `LoopBackEdge — fallback: endpoint-derived box when box is absent` describe block with two tests.

### LAYOUT-002: useGraphLayout self-loop integration untested
**Area**: Loop geometry + container overlays (`dashboard/src/graph/useGraphLayout.ts`)
**Fixed**: Iteration 4
**Detail**: The design doc explicitly lists "self-loop (node edges to itself): treated as a single-node loop; container wraps that node" as an edge case, and `loopContainerBox` had a standalone self-loop unit test, but the integration path through `useGraphLayout` (self-edge → loopBackEdge type, single-node container emitted, correct label) was entirely untested. Added a `useGraphLayout — edge case: self-loop` describe block covering this path. Marks focus-area #2 Pass 2 complete.

### LAYOUT-001: useGraphLayout empty-nodes early-return path untested
**Area**: Loop geometry + container overlays (`dashboard/src/graph/useGraphLayout.ts`)
**Fixed**: Iteration 3
**Detail**: The hook early-returns `{ rfNodes: [], rfEdges: [], needsAutoSave: false }` when `nodes.length === 0` but no test exercised that path — the "error/invalid input" category was absent for the hook. Added a `useGraphLayout — error/invalid: empty nodes list` describe block covering this branch.

### LOOP-003: No three-level nesting test — depth=2 was unverified
**Area**: Loop detection (`dashboard/src/graph/loops.ts`)
**Fixed**: Iteration 2
**Detail**: The deepest depth tested was 1 (Pass 1 added the shared-head case with l1.depth=1). A three-level arrangement (outer loop contains middle loop contains a self-loop) would produce depths 0/1/2, but no test exercised depth=2. Added a "three-level nesting" describe block that also checks backEdge reference identity for all three loops (ensuring labels are preserved through the detection pipeline).

### LOOP-002: Missing depth assertions in "multiple loops sharing a head" test + no sibling-loops coverage
**Area**: Loop detection (`dashboard/src/graph/loops.ts`)
**Fixed**: Iteration 1
**Detail**: The "multiple loops sharing a head" test verified that `body(l1) ⊂ body(l2)` but never asserted `depth`. Since depth drives container nesting in the renderer, an incorrect depth value would cause visual mis-rendering without any failing tests. Added `expect(l1.depth).toBe(1)` and `expect(l2.depth).toBe(0)` to that test. Also added a new `describe` block for genuinely disjoint sibling loops (two sequential non-overlapping loops) to verify both get `depth === 0` — this topology was entirely untested._

<!--
Example of a fixed issue entry:

### LOOP-001: Nested loop body leaked across sibling loops
**Area**: Loop detection (`dashboard/src/graph/loops.ts`)
**Fixed**: Iteration 3
**Detail**: Body reachability traversed back-edges, so a nested loop's body absorbed nodes from a sibling loop via the cycle-closing edge. Stripped detected back-edges before building body adjacency. Added 2 tests: nested loops, two loops sharing a head.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### FOLDER-W01: Enterprise workflow-folder system with group permissions
**Area**: Spec out-of-scope section
**Detail**: Folders that organize workflows and restrict run/view by security group depend on the merged Phase 6 identity model and a group concept Phase 6 does not introduce. Deferred to a post–Phase-6 spec; do not implement here.
-->
