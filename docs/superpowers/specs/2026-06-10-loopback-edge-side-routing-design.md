# Loop-back edge side routing — design

**Date:** 2026-06-10
**Status:** Implemented (revised — see Revision note)
**Scope:** Dashboard graph rendering only. No backend/API changes.

## Revision (2026-06-10): switched B → A (floating geometry)

The first implementation used **Approach B (explicit side handles)**. Adding four
extra handles per node broke *forward* edges: once a node has multiple source/
target handles, React Flow stops defaulting an unhandled edge to bottom/top and
pulls it to a side handle — so every forward edge hooked out the node side. We
reverted to **Approach A (floating geometry)**:

- Nodes keep ONLY their default top/bottom handles → forward edges are untouched.
- `LoopBackEdge` derives its own side attachment points from the default handle
  coords + node dimensions and draws an out-and-back arc that bows into a side
  channel (so it visibly leaves the node, rather than running flush up the column).
- Handle dots are hidden via CSS (read-only viewer; they were visual noise).

Also fixed a latent bug found here: **`WorkflowReview` never passed `edgeTypes`**
to its `<ReactFlow>`, so the custom `LoopBackEdge` was never used in that view —
loop-back edges silently fell back to the default bezier. `ExecutionView` already
wired it; `WorkflowReview` now does too.

The component descriptions below are the original Approach-B plan, kept for
history; the shipped code follows Approach A as described in this note.

## Problem

Workflows render top-to-bottom. Forward edges connect bottom-center of one node
to top-center of the next — correct and unchanged.

Loop-back edges (the back-edge of a loop, tail → earlier head) currently also
**attach at the source's bottom and the target's top**, then bow sideways to a
gutter in the middle. This reads as the loop edge diving out of the bottom of the
tail and into the top of the head, crossing the forward-flow lanes.

**Desired:** a loop-back edge should leave the **side** of the tail node and enter
the **side** of the head node it returns to — using the side with the most room so
it routes cleanly outside the loop body.

## Decision summary

- **Side heuristic:** unchanged. Reuse the existing `chooseSide` — pick the side of
  the loop-body box with more horizontal room (default right on a tie). The whole
  back-edge uses ONE consistent side: exit the tail's chosen side, enter the head's
  same side.
- **Attachment mechanism:** explicit ReactFlow side handles (Approach B), not
  floating geometry. More idiomatic; ReactFlow computes the exact side-handle
  coordinates and positions.

## Components

### 1. `graph/nodes/ArbitronNode.tsx` (+ `ArbitronNode.module.css`)

Add four side handles as connection anchors, keeping the existing
Top (`target`) / Bottom (`source`) handles for forward edges untouched:

| id            | type   | position |
|---------------|--------|----------|
| `loop-s-left` | source | Left     |
| `loop-s-right`| source | Right    |
| `loop-t-left` | target | Left     |
| `loop-t-right`| target | Right    |

Styled invisible — this is a read-only viewer, the handles are pure anchors:
`.loopHandle { opacity: 0; pointer-events: none; }`. Node appearance is unchanged.

### 2. `graph/loopEdgeRouting.ts` (new — single source of truth)

Extract `chooseSide`, `GUTTER_OFFSET`, and the `SideRoute` type out of
`LoopBackEdge.tsx` so the layout hook and the edge component share one
implementation. Add:

```ts
loopHandleIds(side: 'left' | 'right'): { sourceHandle: string; targetHandle: string }
// 'right' -> { sourceHandle: 'loop-s-right', targetHandle: 'loop-t-right' }
// 'left'  -> { sourceHandle: 'loop-s-left',  targetHandle: 'loop-t-left'  }
```

### 3. `graph/useGraphLayout.ts`

For each back-edge, decide the side at layout time (positions are known here):

- source/target node-center X from `positionMap` (`+ NODE_WIDTH / 2`)
- loop box from the existing `boxByLoopId`
- `const { side, gutterX } = chooseSide({ sourceX, targetX, boxX, boxWidth })`
- set `sourceHandle` / `targetHandle` via `loopHandleIds(side)`
- pass `data: { loop, box, side, gutterX }`

Forward (`smoothstep`) edges and the rest of layout are untouched. `rfNodes`/
`rfEdges` referential stability (see the blank-screen fix) is preserved — the
side is derived from already-memoized inputs.

### 4. `graph/edges/LoopBackEdge.tsx`

With side handles set, ReactFlow provides `sourceX/Y` and `targetX/Y` already at
the side-handle points (each side's vertical center) and `sourcePosition` /
`targetPosition` equal to the chosen side. The component:

- uses `props.sourcePosition` / `props.targetPosition` for the curve ends
- bows out through `data.gutterX` (exit → gutter → entry), horizontal at both node
  ends; the arrowhead enters the head node's side
- keeps a fallback: if `data.side` is absent (standalone edge unit tests with no
  node graph), compute side via the shared `chooseSide` and derive endpoints from
  the props, as today

## Data flow

```
useGraphLayout (layout-time)
  detect back-edge -> chooseSide(positions, box) -> { side, gutterX }
  edge.sourceHandle/targetHandle = loopHandleIds(side)
  edge.data = { loop, box, side, gutterX }
        |
        v
ReactFlow  -> resolves side handles -> sourceX/Y, targetX/Y, source/targetPosition
        |
        v
LoopBackEdge -> path: side-exit -> gutter -> side-entry (horizontal ends)
```

## Testing (TDD)

- `loopEdgeRouting.test.ts` — moved `chooseSide` cases (left/right/tie→right) +
  `loopHandleIds` mapping.
- `useGraphLayout.test.ts` — a back-edge carries `sourceHandle`/`targetHandle` for
  a single consistent side and `data.side` matches; forward edges carry neither.
- `LoopBackEdge.test.tsx` — endpoints attach horizontally on the chosen side
  (path starts/ends at the side handle position, not bottom/top).
- `ArbitronNode` — renders the four invisible side handles with the expected ids.
- Manual: headless-Chrome repro on `feature-pipeline` and `pr-review` (both have
  loops) confirms loop edges leave/enter node sides with no overlap and no errors.

## Non-goals

- No change to forward-edge routing, the gutter heuristic, or node appearance.
- Not making handles interactive (read-only viewer).
- No backend/schema changes.
