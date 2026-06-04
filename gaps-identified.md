# Gaps Identified — Dashboard UX Improvements

Concrete, verified gaps between the current code and the spec/designs/plans.
Each maps to a focus area in `focus-areas.md`. Check an item off only when it is
closed with a test (or a recorded, deliberate decision). Only the user may move
items into Won't Fix.

Verified at seed time: all 10 plan tasks committed; `make build`, `make test`
clean; dashboard 23 files, 205 tests pass. These gaps are about coverage
completeness — **not** missing behavior.

## Open Issues

### GAP-002: GraphView integration coverage missing failed/static centering scenarios
**Area:** `dashboard/src/routes/ExecutionView.tsx` (`GraphView`), `dashboard/src/routes/GraphView.test.tsx`
**Focus area:** 2
**What is wrong:** `GraphView.test.tsx` covers two initial-centering scenarios at the
integration level: a running instance (centers on active node) and a completed
instance (centers on final node). Two cases from the Phase 2 spec are covered only
at the `selectAnchorNodeId` unit level:
- **Failed instance** → centers on the failed node (spec requirement).
- **Static workflow preview** (no instance) → centers on the entry node (spec requirement).
If `GraphView`'s wiring of `selectAnchorNodeId` were broken specifically for those
branches, no integration test would catch it.
**Fix:** Add two tests to `GraphView.test.tsx`:
1. Renders with a failed instance + a node marked `failed` in `executionStates` and
   asserts `setCenter` is called on the failed node's position.
2. Renders with `instance={null}` (no instance / static preview) and asserts
   `setCenter` is called on the entry/source node's position.

## Closed Issues

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
