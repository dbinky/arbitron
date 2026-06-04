# Gaps Identified — Dashboard UX Improvements

Concrete, verified gaps between the current code and the spec/designs/plans.
Each maps to a focus area in `focus-areas.md`. Check an item off only when it is
closed with a test (or a recorded, deliberate decision). Only the user may move
items into Won't Fix.

Verified at seed time: all 10 plan tasks committed; `make build`, `make test`
clean; dashboard 23 files, 205 tests pass. These gaps are about coverage
completeness — **not** missing behavior.

## Open Issues

_No open issues. All identified gaps are closed._

## Closed Issues

### ~~GAP-003: Dismissal HTTP handlers missing error-scenario coverage~~  ✅ CLOSED (MEDIUM)
**Area:** `adapter/http/dismissal_handler_test.go`
**What was wrong:** The dismissal HTTP tests covered the happy path, business-rule
failures (409), and ErrNotFound (404), but three error scenarios were not tested:
- **Invalid instance ID** (non-UUID in the URL) → handler must return 400 BAD_REQUEST.
  `TestDismiss_InvalidID_400` and `TestRestore_InvalidID_400` were missing.
- **Repo write failure** → `Dismiss` and `Restore` return 500 when the repo fails.
  `TestDismiss_RepoError_500` and `TestRestore_RepoError_500` were missing.
- **List failure in DismissAll** → `DismissAll` returns 500 when `instances.List`
  fails. `TestDismissAll_ListError_500` was missing.
**Closed:** added `errDismissalRepo` (always-error dismissal mock, parallel to the
existing `errInstanceRepo`); widened `newDismissalTestServer` to accept
`instance.DismissalRepository` (interface) so error mocks compose without new
helpers; added five tests covering the missing error paths. `go test ./adapter/http/...`
and `make test-all` green.

### ~~GAP-002: GraphView integration coverage missing failed/static centering scenarios~~  ✅ CLOSED (MEDIUM)
**Area:** `dashboard/src/routes/ExecutionView.tsx` (`GraphView`), `dashboard/src/routes/GraphView.test.tsx`
**What was wrong:** `GraphView.test.tsx` covered running and completed instance centering
at the integration level, but not failed or static-preview — those were only covered
at the `selectAnchorNodeId` unit level. Broken wiring in `GraphView` specifically for
those branches would go undetected.
**Closed:** Added two tests to `GraphView.test.tsx` under `'GraphView — default-zoom
centering on mount'`:
- `centers on the failed node for a failed instance` — instance with `state:'failed'`
  + `failedNodeId:'b'` + trace marking 'b' failed; asserts `setCenter` on node 'b'.
- `centers on the entry node for a static workflow preview (no instance)` — `instance={null}`
  with `entrypoint:true` on node 'a'; asserts `setCenter` on node 'a'.
Both pass; full dashboard suite green (208 → 210 tests).

### ~~GAP-001: QueueSection missing dismissed-row dimming coverage~~  ✅ CLOSED (MEDIUM)
**Area:** `dashboard/src/components/QueueSection.test.tsx`
**What was wrong:** `QueueSection.test.tsx` had only two tests — both covering
`[Dismiss All]` button presence/absence. No test verified that an instance with
`dismissed: true` is rendered dimmed with a restore control (not a dismiss control),
or that a non-dismissed terminal instance renders a dismiss control. This left the
`dimmed={inst.dismissed === true}` passthrough in `QueueSection.tsx` uncovered: a
change to that line would not break any test.
**Closed:** added three tests in `describe('QueueSection — dismissed row rendering')`:
- `dismissed: true` instance → only restore control visible, calls `onRestore(id)`.
- `dismissed: false` terminal instance → only dismiss control visible, calls `onDismiss(id)`.
- Active (`running`) instance → no dismiss or restore control (spec: active items
  cannot be dismissed).
All three pass; full dashboard suite green (205 → 208 tests).

## Won't Fix (Out of Scope — spec Non-Goals)

_Only the user may add to or move items in this section. Pre-seeded from the spec's
documented non-goals._

### WF-001: Enterprise workflow-folder system
Spec explicitly deferred. Do not build folders, folder permissions, or security
groups on this branch.

### WF-002: Multi-user identity / authentication (Phase 6)
The `currentUser(r)` seam returns `GetDefaultUser()` today; Phase 6 replaces it.
Do not add auth on this branch.

### WF-003: Persisting loop container geometry
Loop containers are derived, non-interactive overlays computed at render time.
Never persist container positions or use React Flow `parentId`.
