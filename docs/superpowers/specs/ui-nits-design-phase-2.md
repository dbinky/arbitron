# UI Nits — Design Phase 2: Default Zoom + Follow Executing Node

Implements spec section 2 ("Readable default zoom with follow-the-current-step")
from `docs/specs/ui-nits-spec.md`. Frontend only — no backend or API changes.

## Goal

Open the workflow graph at a readable zoom (not fitted-all-the-way-out) centered
on the most relevant step, and — while an instance runs — smoothly keep the
executing step centered, without taking control away from the user.

## Current state

- `ExecutionView.tsx` `GraphView` renders `<ReactFlow fitView
  fitViewOptions={{ padding: 0.2 }} minZoom={0.3} maxZoom={2} ...>`. `fitView`
  zooms to fit the entire graph, shrinking step titles on non-trivial workflows.
- `GraphView` already computes `executionStates` (per-node
  pending/running/waiting/completed/failed/skipped) and `rfNodes` with positions.
  It is rendered inside a `ReactFlowProvider`, so `useReactFlow()` is available.
- There is no concept of centering on, or following, a node.

## Integration with Phase 1

Phase 1 and this phase both modify the same `<ReactFlow>` element in
`GraphView` (`ExecutionView.tsx`). They compose, but the implementer must apply
this phase **on top of** Phase 1's changes, not in place of them:

- Phase 1 adds `edgeTypes` (for `loopBackEdge`) and `loopContainer` nodes to the
  graph; this phase must preserve them.
- This phase removes `fitView`/`fitViewOptions` and adds the follow hook; it must
  not drop Phase 1's `edgeTypes`/container handling.
- Anchor/centering math reuses the `positionMap` and node-size constants from
  `useGraphLayout`; loop-container overlay nodes are **excluded** from anchor
  selection (only real step nodes are candidate anchors).

## Anchor selection

Define the "anchor" node the view should center on, given instance + states:

- **Running** instance → the active node (`instance.currentNodeId`, which
  `executionStates` marks `running`/`waiting`).
- **Completed** instance → the terminal node (the node with no outgoing forward
  edge that was executed last; in practice the last `completed` node in trace
  order, falling back to a sink node of the graph).
- **Failed** instance → the failed node (`instance.failedNodeId` / the node
  marked `failed`).
- **No instance (static workflow preview)** → the entrypoint node
  (`node.entrypoint === true`, falling back to the graph's source node).

Implement as a pure helper `selectAnchorNodeId(instance, workflowDef,
executionStates, trace)` so it is unit-testable.

## Default zoom

- Remove `fitView` / `fitViewOptions`. Keep `minZoom={0.3} maxZoom={2}`.
- On first render (once `rfNodes` and the anchor exist), call
  `setCenter(anchor.x + NODE_WIDTH/2, anchor.y + NODE_HEIGHT/2,
  { zoom: DEFAULT_ZOOM })` where `DEFAULT_ZOOM = 0.9` (within existing bounds;
  ~2–3× the apparent size of the old fit-all view on typical graphs).
- Centering uses node center coordinates derived from `positionMap` + the node
  size constants already defined in `useGraphLayout`.

## Follow behavior

New hook `dashboard/src/graph/useFollowActiveNode.ts`, used inside `GraphView`
(inside the provider):

- Inputs: the `ReactFlow` instance (`useReactFlow()`), the current anchor node id
  and position, and whether the instance is running.
- State: `following: boolean` (default `true`).
- **On anchor change while `following`:** call `setCenter(x, y, { zoom:
  currentZoom, duration: 400 })` to smoothly animate to the new active node,
  preserving the user's current zoom (only the default-zoom set on mount forces
  `DEFAULT_ZOOM`).
- **Pause on manual interaction:** subscribe to `onMoveStart`. React Flow passes
  the triggering event; **programmatic** moves (our `setCenter`) carry a `null`
  event, while user pan/zoom carries a real event. When the event is non-null,
  set `following = false`.
- **Recenter / Follow control:** a small button (in the graph toolbar area near
  React Flow `<Controls>`, or the breadcrumb bar). Clicking it sets
  `following = true` and immediately `setCenter`s on the current anchor at
  `DEFAULT_ZOOM`. Its label/icon reflects state ("Follow" when paused,
  active/highlighted when following).
- For non-running graphs (completed/failed/static), there is no advancing anchor,
  so follow is effectively a one-shot center on mount plus the manual Recenter.

## Components & boundaries

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `selectAnchorNodeId` | Pick the node to center on | instance, def, states, trace |
| `useFollowActiveNode` | Center on mount, animate on advance, pause/resume | `useReactFlow`, anchor |
| Recenter control | Toggle/Re-engage follow | hook state |
| `GraphView` | Wire the above into ReactFlow | existing layout |

## Edge cases

- **Empty graph / no anchor:** skip centering (no-op); nothing throws.
- **Anchor node has no position yet** (layout not computed): defer centering
  until positions exist (the effect depends on the resolved position).
- **Rapid node transitions** (fast workflows): `setCenter` animations replace one
  another; use the latest anchor. Debounce is unnecessary because React Flow
  cancels in-flight transitions, but coalesce on the memoized anchor id.
- **User zoom then new node arrives:** `following` is already `false`, so the
  view does not jump; the Recenter button is the way back.
- **Reduced-motion:** if `prefers-reduced-motion`, use `duration: 0` (jump
  instead of animate).

## Testing

- Unit-test `selectAnchorNodeId` across running/completed/failed/static cases,
  including fallbacks (no entrypoint flag, missing currentNodeId).
- Hook test (with a mocked `useReactFlow`): centers on mount; animates on anchor
  change while following; stops animating after a user-initiated `onMoveStart`;
  re-engages and centers on Recenter.
- Manual check in the dashboard: open a running instance and confirm it follows;
  pan away and confirm it stops; click Recenter and confirm it resumes; open a
  completed and a failed instance and confirm correct initial centering.

## Out of scope

Loop rendering (Phase 1) and queue/dismiss behavior (Phase 3).
