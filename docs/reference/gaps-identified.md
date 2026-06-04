# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_


## Fixed Previously

### DISMISS-002: DismissAll since-filter not tested at handler level
**Area**: Per-user dismissal state model (`adapter/http/dismissal_handler_test.go`)
**Fixed**: Iteration 28
**Detail**: `DismissAll` parses a `since` query parameter and passes it as `filters.Since` to `List`, limiting the scope to the 5-day window the dashboard shows. No existing test exercised this path — all DismissAll tests omit `since`, meaning a regression that dropped `filters.Since` from the handler would silently dismiss items outside the window without failing any test. Added `TestDismissAll_SinceFilterLimitsScope`: creates an "old" instance (before cutoff) and a "recent" instance (after cutoff), calls `?state=failed&since=<cutoff>`, and asserts only the recent item is dismissed.

---

### DISMISS-001: waiting and paused states not tested against dismiss guard at handler and UI layer
**Area**: Per-user dismissal state model (`adapter/http/dismissal_handler_test.go`, `dashboard/src/components/InstanceRow.test.tsx`)
**Fixed**: Iteration 27
**Detail**: The spec explicitly states "Active, waiting (interactive), and cancelled items cannot be dismissed." The handler tests covered `running` (409) and `cancelled` (409), but `waiting` — the spec's other named non-dismissible active state — was untested. `paused` was also non-dismissible but untested. Added `TestDismiss_WaitingInstance_409` and `TestDismiss_PausedInstance_409` to the handler tests. Added `waiting` and `paused` test cases to InstanceRow.test.tsx to pin the `isTerminal = failed || completed` boundary from the frontend side too.

---

### ANCHOR-001: selectAnchorNodeId has no tests for paused or cancelled instance states
**Area**: Domain viewport behavior (`dashboard/src/graph/selectAnchorNodeId.test.ts`)
**Fixed**: Iteration 26
**Detail**: The design doc specifies anchor selection for running/waiting, completed, failed, and static preview. `paused` and `cancelled` are real instance states that fall through to the general fallback (source node), but this behavior was entirely untested. A future "improvement" that made `paused` return `currentNodeId` (like running/waiting) would change centering behavior without failing any test. Added `selectAnchorNodeId — edge case: paused and cancelled (general fallback)` describe block with two tests: (1) paused instance returns the source node (not `currentNodeId`); (2) cancelled instance returns the source node. Both use a 3-node linear graph so the source node (a, no incoming edge) is unambiguous.

### VIEW-003: No integration test that loop containers are excluded from anchor selection
**Area**: Domain viewport behavior (`dashboard/src/routes/GraphView.test.tsx`)
**Fixed**: Iteration 25
**Detail**: The design doc says "loop-container overlay nodes are excluded from anchor candidates — only real step nodes." The exclusion is structural (selectAnchorNodeId searches workflowDef.nodes, containers are never there), but no test verified the end result. Added `loopWorkflowDef` fixture (a→b→c with c→b back-edge) and `centers on the active step node, not the loop container, for a running workflow with a loop`: renders GraphView with `currentNodeId='b'` (a node inside the loop body), verifies the loop container IS present in rfNodes, then asserts setCenter is called with 'b's position at zoom 0.9.

---

### LOOP-005: Nested-loop depth propagation not verified at GraphView integration level
**Area**: Domain loop semantics end-to-end (`dashboard/src/routes/GraphView.loops.test.tsx`)
**Fixed**: Iteration 24
**Detail**: The design spec says "Nested loops are shown as nested containers." The `depth` field drives visual inset and z-index. `useGraphLayout.test.ts` verified two containers for nested loops, but no test checked that inner/outer have the correct `depth` values. A bug hardcoding `depth: 0` on all containers would produce visually flat boxes without failing any test. Added `nestedLoopWorkflow` fixture (outer loop e→b, inner d→c), then two tests: (1) two containers are emitted; (2) the inner container (`data.label === 'Loop until inner done'`) has `depth === 1` and the outer has `depth === 0`. Also cleaned up a duplicate `unlabeledLoopWorkflow` fixture that appeared due to a failed partial edit.

---

### LOOP-004: GraphView integration test verifies node type but not container label
**Area**: Domain loop semantics end-to-end (`dashboard/src/routes/GraphView.loops.test.tsx`)
**Fixed**: Iteration 23
**Detail**: `GraphView.loops.test.tsx` checked that a loop workflow emits a `loopContainer` node and a `loopBackEdge` edge, but never checked the container node's `data.label` content. A bug in the label-propagation chain (e.g., `loopContainerLabel` not called in `useGraphLayout`) would pass all integration tests. Added two tests: (1) `container label reflects the back-edge label from the workflow definition` — verifies `data.label === 'Loop until tests pass'` for a workflow with `label: 'tests pass'` on the back-edge; (2) `container falls back to "Loop" when the back-edge carries no label` — verifies `data.label === 'Loop'` for an unlabeled back-edge. Also added `unlabeledLoopWorkflow` fixture (same topology, back-edge without a label) to the test file.

---

### UI-003: Edge-case coverage missing for dismissal UI — completed dismissAll + cancelled guard
**Area**: Queue dismissal UI (`dashboard/src/routes/QueueView.test.tsx`, `dashboard/src/components/InstanceRow.test.tsx`)
**Fixed**: Iteration 22
**Detail**: Two edge cases were untested. (1) `handleDismissAll` in QueueView was only tested for `state='failed'` (Failed section button, index 0). The Completed section's button (index 1) calls `handleDismissAll('completed')` — a distinct code path. Added `handleDismissAll calls dismissAll with state=completed for the Completed section` which clicks `dismissAllBtns[1]` and asserts `dismissAll` is called with `'completed'`. (2) `InstanceRow` tests covered `running` (non-terminal) but not `cancelled`. The `isTerminal` check is `state === 'failed' || state === 'completed'` — deliberately narrower than the engine's `isTerminalState` (which includes `cancelled`). Added `shows no dismiss/restore control on cancelled rows` to pin this design boundary at the UI layer.

---

### UI-002: QueueView dismiss handlers had no error handling; error paths untested
**Area**: Queue dismissal UI (`dashboard/src/routes/QueueView.tsx`, `QueueView.test.tsx`)
**Fixed**: Iteration 21
**Detail**: `handleDismiss`, `handleRestore`, and `handleDismissAll` were plain `async` functions with no try/catch. API failures caused unhandled promise rejections — the item stayed visible with no refetch but vitest reported three unhandled rejections. The fetchAll error state (renders "Failed to load instances" + Retry prompt) was also completely untested. Fixed by: (1) wrapping all three handlers in try/catch (best-effort — item stays visible on failure), (2) adding error-path tests for all three handlers verifying stable view + no spurious refetch after a rejected API call, and (3) adding a test that fetchAll failure renders the error state and that clicking Retry re-triggers the load.

---

### UI-001: QueueView dismiss-handler API calls untested; wrong role-query selector
**Area**: Queue dismissal UI (`dashboard/src/routes/QueueView.test.tsx`)
**Fixed**: Iteration 19
**Detail**: The original mock only included `api.instances.list`. All three dismiss-side handlers (`handleDismiss`, `handleRestore`, `handleDismissAll`) called the real API in tests, making them impossible to verify. Extended the mock with `vi.hoisted()` to add `dismiss`, `restore`, and `dismissAll` vi.fn()s, then added `TestHandleDismissAll_CallsAPIAndRefetches`: verifies `dismissAll('failed', ISOdate)` is called and `list` is called again after. Root cause of the initial "0 calls" failure: the `getAllByRole('button', { name: /dismiss all/i })` regex matched the section's `div[role="button"]` header (its accessible name is the concatenation of all children, including the "Dismiss All" button text), so index 0 was the HEADER div, not the button — clicking it collapsed the section instead of invoking `onDismissAll`. Fixed by using the exact string `'Dismiss All'` for the role query, which matches only the `<button>` whose accessible name IS exactly "Dismiss All".

### ENGINE-002: Multi-user dismissal clearing untested in engine
**Area**: Engine retry resurface (`domain/engine/engine_dismissal_test.go`)
**Fixed**: Iteration 18
**Detail**: The design doc says Retry clears dismissals "for everyone who had dismissed it." `TestEngine_RetryClearsDismissal` only tested with a single user. A bug where the engine called `Restore(user, id)` per-user (e.g. for the acting user only) rather than `ClearForInstance(id)` would pass the single-user test but violate the all-users contract. Added `TestEngine_RetryClearsDismissalForAllUsers`: two distinct users dismiss the same failed instance, retry is called, both users' dismissals are verified to be cleared.

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
