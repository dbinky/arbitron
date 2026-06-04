# UI Nits — Design Phase 1: Loop Container Rendering

Implements spec section 1 ("Clearer loop rendering") from
`docs/specs/ui-nits-spec.md`. Frontend only — no backend or API changes.

## Goal

Render loops as self-contained, repeating sections: loop-body steps grouped in a
labeled container and aligned in one column, with the return edge drawn as a
distinct curve routed to the side instead of slicing across the steps between.

## Current state

- `dashboard/src/graph/useGraphLayout.ts` runs **dagre** (`rankdir: 'TB'`,
  `nodesep: 60`, `ranksep: 80`) when nodes lack stored positions, otherwise uses
  stored absolute positions under an **all-or-nothing rule** (stored positions
  are used only when *every* node has one). Computed positions are auto-saved via
  the `/api/v1/workflows/{name}/layout` endpoint.
- Every edge is emitted as a `smoothstep` React Flow edge with an arrow marker.
  Back-edges (target is an earlier node) get no special treatment, so they route
  straight into the target's top handle and cross intervening nodes.
- `nodeTypes` registers a single `arbitronNode`. The graph renders in
  `ExecutionView.tsx`'s `GraphView` inside a `ReactFlowProvider`.

## Key constraint

React Flow parent/child grouping (`parentId`) re-bases child coordinates to be
relative to the parent. That would break the existing **absolute** position model
and the `/layout` persistence. Therefore the loop container must be a
**derived, non-interactive overlay** computed from node positions at render
time, never a real parent node and never persisted.

## Approach

### 1. Loop detection (derived, render-time)

Add a pure helper (e.g. `dashboard/src/graph/loops.ts`):

- Build adjacency from `edges`. Run a DFS to classify edges; an edge
  `(source → target)` is a **back-edge** when `target` is an ancestor of
  `source` on the DFS stack (i.e. it closes a cycle).
- For each back-edge, the **loop head** is its target and the **loop tail** is
  its source. The **loop body** is the set of nodes reachable from the head
  while still able to reach the tail (the nodes on cycle paths between head and
  tail). Compute this as: nodes that are both descendants of the head and
  ancestors of the tail, plus head and tail themselves.
- Produce a list of `Loop { id, headId, tailId, bodyNodeIds: string[],
  backEdge: WorkflowEdge, depth }`. Nesting `depth` is the count of other loops
  whose body fully contains this loop's body.

This helper is unit-testable in isolation against synthetic node/edge sets.

### 2. Layout adjustments in `useGraphLayout`

Only affects the dagre branch (no stored positions); stored-position graphs are
left exactly as-is and the container is derived from whatever positions exist.

- When seeding dagre edges, **omit back-edges** from `g.setEdge(...)` so they do
  not distort rank assignment — this keeps the loop body in one straight column.
- Use dagre **compound graph** support to keep each loop body grouped: create a
  synthetic cluster node per loop and `g.setParent(bodyNodeId, clusterId)` for
  body members (innermost loop wins for nodes in nested loops). The cluster is
  used only to influence dagre grouping; its geometry is discarded.
- Back-edges are still emitted as React Flow edges (see step 4) — they are only
  excluded from dagre *ranking*, not from rendering.

### 3. Container overlay nodes

- Add a new node type `loopContainer` (component
  `dashboard/src/graph/nodes/LoopContainerNode.tsx`) registered in
  `nodeTypes.ts`. It is non-interactive (`selectable: false`,
  `focusable: false`, `draggable: false`) and rendered with a lower `zIndex`
  than step nodes so steps sit on top.
- For each detected loop, compute the bounding box of its body nodes' positions
  (using the same `positionMap` the step nodes use, so it is correct for both
  dagre and stored positions), pad it, and emit a `loopContainer` node at that
  box with `data: { label, depth }`.
- Label text: `Loop until {backEdge.label}` when the back-edge has a non-empty
  label, otherwise `Loop`. (`WorkflowEdge.label` already exists; no schema
  change.)
- Nested loops produce nested containers; deeper `depth` gets a slightly inset
  box and higher `zIndex` among containers (still below step nodes).

### 4. Distinct back-edge rendering

- Add a custom edge type `loopBackEdge` (component
  `dashboard/src/graph/edges/LoopBackEdge.tsx`), registered via React Flow
  `edgeTypes` on the `ReactFlow` element in `GraphView`.
- In `useGraphLayout`, emit back-edges with `type: 'loopBackEdge'`; emit all
  other edges as today (`smoothstep`).
- `LoopBackEdge` routes the edge up a side gutter (a curved path biased to the
  left or right of the body bounding box rather than through it) with a dashed,
  accent-colored stroke distinct from forward edges. Reuse React Flow path
  helpers for the curve; choose the side with more free space based on the
  container box.

## Components & boundaries

| Unit | Responsibility | Depends on |
| --- | --- | --- |
| `loops.ts` | Pure loop/back-edge/body detection | edges only |
| `useGraphLayout.ts` | Positions, container overlay nodes, edge typing | `loops.ts`, dagre |
| `LoopContainerNode.tsx` | Render labeled, non-interactive box | node data |
| `LoopBackEdge.tsx` | Render routed, styled return edge | React Flow edge props |

## Edge cases

- **No loops:** `loops.ts` returns empty; layout and rendering behave exactly as
  today.
- **Self-loop** (node edges to itself): treated as a single-node loop; container
  wraps that node; back-edge curves out and back.
- **Multiple back-edges to the same head / multiple loops:** each produces its
  own container and back-edge; overlapping bodies nest by `depth`.
- **Irregular / overlapping bodies that don't nest cleanly:** containers are
  still drawn from bounding boxes; they may visually overlap but never change
  node positions or interactivity.
- **Stored positions:** containers/edges derive from stored positions; if a
  user has dragged nodes, the container simply follows the resulting box.

## Testing

- Unit-test `loops.ts`: linear graph (no loops), single loop, nested loops,
  self-loop, multiple loops sharing a head, diamond + loop combination.
- Unit-test the container bounding-box derivation given a positionMap.
- Component/render test: a workflow with a loop renders one `loopContainer` node
  with the expected label and the back-edge typed `loopBackEdge`; a loopless
  workflow renders neither.
- Visual sanity check in the running dashboard against a real looping workflow
  (e.g. an existing refinement-loop workflow).

## Out of scope

Zoom/follow behavior (Phase 2) and any persistence of container geometry.
