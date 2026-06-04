# UI Nits Phase 1, Task 3: LoopBackEdge Component + GraphView Wiring

> Continues `ui-nits-implementation-phase-1-task-2.md`. REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans. Depends on `loops.ts`, `loopLayout.ts`, and the `useGraphLayout` changes (back-edges typed `loopBackEdge` with `data.loop`) from Tasks 1–2.

**Goal:** Render the loop return edge as a visually distinct, dashed, accent-colored curve routed up a side gutter (biased to the side of the body bounding box with more free space) instead of slicing straight up through the steps between tail and head. Wire the `loopBackEdge` edge type into `GraphView` via React Flow `edgeTypes`, and verify end-to-end through the real layout.

**Key constraints (from the design):**
- `LoopBackEdge` is a React Flow custom edge component receiving `EdgeProps`. It reuses React Flow path helpers (`getBezierPath`) and derives the gutter side from the loop body bounding box carried on `data.loop` + the source/target geometry.
- It must remain visually distinct from forward edges: dashed stroke, accent color, routed to the side — never a straight vertical line through the body.
- `edgeTypes` is a stable module-level object passed to `<ReactFlow>` (like `nodeTypes`), to avoid React Flow re-creation warnings.

---

### Task 3: `LoopBackEdge` edge component + `edgeTypes` wiring

**Files:**
- Create: `dashboard/src/graph/edges/LoopBackEdge.tsx`
- Create: `dashboard/src/graph/edges/LoopBackEdge.test.tsx`
- Create: `dashboard/src/graph/edges/edgeTypes.ts`
- Modify: `dashboard/src/routes/ExecutionView.tsx` (import + pass `edgeTypes` to `<ReactFlow>`; the `<ReactFlow ...>` element is at lines 102–112, `nodeTypes` is already passed on line 105)

---

#### Part A — Side-routing geometry helper (pure, testable)

A pure function decides which horizontal side the return edge should bow out to, and how far. It lives inside `LoopBackEdge.tsx` but is exported for unit testing.

```ts
export interface SideRoute {
  side: 'left' | 'right'
  /** Absolute x of the gutter the curve bows out to. */
  gutterX: number
}

/**
 * Choose the gutter side based on the loop body bounding box relative to the
 * edge endpoints. Bow out on the side of the body box that has more room,
 * i.e. away from the box center, defaulting to 'right' on a tie.
 */
export function chooseSide(args: {
  sourceX: number
  targetX: number
  boxX: number
  boxWidth: number
}): SideRoute
```

Rule: compute the box's right edge (`boxX + boxWidth`) and left edge (`boxX`). The gutter is placed `GUTTER_OFFSET` outside whichever vertical side is closer to the endpoints' average x is *farther* from — concretely: if the endpoints sit left-of-center of the box, route right (`gutterX = boxX + boxWidth + GUTTER_OFFSET`, `side: 'right'`); otherwise route left (`gutterX = boxX - GUTTER_OFFSET`, `side: 'left'`). This keeps the curve outside the body box.

- [ ] **Write the failing test** `dashboard/src/graph/edges/LoopBackEdge.test.tsx`:

```tsx
import { render } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'

// React Flow's BaseEdge renders an SVG <path>; in jsdom we render it directly,
// but the component must be wrapped in an <svg> for the path to be valid DOM.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    // BaseEdge in isolation needs no provider; passthrough to a plain <path>.
    BaseEdge: ({ path, style }: { path: string; style?: object }) => (
      <path data-testid="edge-path" d={path} style={style} />
    ),
    EdgeLabelRenderer: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  }
})

import LoopBackEdge, { chooseSide, GUTTER_OFFSET } from './LoopBackEdge'

const baseProps = {
  id: 'e-c-b-3',
  source: 'c',
  target: 'b',
  sourceX: 200,
  sourceY: 400,
  targetX: 200,
  targetY: 100,
  sourcePosition: 'bottom',
  targetPosition: 'top',
  data: {
    loop: {
      id: 'loop-b-c',
      headId: 'b',
      tailId: 'c',
      bodyNodeIds: ['b', 'c'],
      backEdge: { source: 'c', target: 'b', label: 'tests pass' },
      depth: 0,
    },
    box: { x: 100, y: 80, width: 200, height: 360 },
  },
} as unknown as Parameters<typeof LoopBackEdge>[0]

describe('chooseSide', () => {
  it('routes right when endpoints are left of the box center', () => {
    const r = chooseSide({ sourceX: 120, targetX: 120, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('right')
    expect(r.gutterX).toBe(100 + 200 + GUTTER_OFFSET)
  })

  it('routes left when endpoints are right of the box center', () => {
    const r = chooseSide({ sourceX: 280, targetX: 280, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('left')
    expect(r.gutterX).toBe(100 - GUTTER_OFFSET)
  })

  it('defaults to right on a centered tie', () => {
    const r = chooseSide({ sourceX: 200, targetX: 200, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('right')
  })
})

describe('LoopBackEdge — rendering', () => {
  it('renders an SVG path (the routed return curve)', () => {
    const { getByTestId } = render(
      <svg>
        <LoopBackEdge {...baseProps} />
      </svg>,
    )
    const path = getByTestId('edge-path')
    expect(path).toBeInTheDocument()
    // The path string is non-empty and contains curve commands.
    expect(path.getAttribute('d')!.length).toBeGreaterThan(0)
  })

  it('applies a dashed accent stroke distinct from forward edges', () => {
    const { getByTestId } = render(
      <svg>
        <LoopBackEdge {...baseProps} />
      </svg>,
    )
    const style = getByTestId('edge-path').getAttribute('style') ?? ''
    expect(style).toMatch(/dash/i) // stroke-dasharray present
    expect(style.toLowerCase()).toContain('#7c8cf5'.toLowerCase())
  })

  it('renders the loop label when the back-edge has one', () => {
    const { getByText } = render(
      <svg>
        <LoopBackEdge {...baseProps} />
      </svg>,
    )
    expect(getByText('tests pass')).toBeInTheDocument()
  })

  it('renders without a label when the back-edge has none', () => {
    const noLabel = {
      ...baseProps,
      data: {
        ...(baseProps as { data: { loop: { backEdge: object } } }).data,
        loop: {
          ...(baseProps as { data: { loop: object } }).data.loop,
          backEdge: { source: 'c', target: 'b' },
        },
      },
    } as typeof baseProps
    const { queryByText } = render(
      <svg>
        <LoopBackEdge {...noLabel} />
      </svg>,
    )
    expect(queryByText('tests pass')).not.toBeInTheDocument()
  })
})
```

- [ ] **Run, expect FAIL** (module missing):

```
npm test -- src/graph/edges/LoopBackEdge.test.tsx
```
Expected: `Cannot find module './LoopBackEdge'`.

- [ ] **Write** `dashboard/src/graph/edges/LoopBackEdge.tsx`. It bows the curve out to a side gutter using two bezier segments built with `getBezierPath`, joining at the gutter midpoint. The `box` is read from `data` (the layout passes `data.loop`; the box is recomputed here from the loop body is not available — so the layout must also pass `box`; see the wiring note below). To keep this component self-contained and testable, it reads `data.box` when present and otherwise falls back to a straight side offset around the endpoints.

```tsx
import { memo } from 'react'
import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  Position,
  type EdgeProps,
} from '@xyflow/react'
import type { Loop } from '../loops'
import type { Box } from '../loopLayout'

export const GUTTER_OFFSET = 60

export interface SideRoute {
  side: 'left' | 'right'
  gutterX: number
}

export function chooseSide(args: {
  sourceX: number
  targetX: number
  boxX: number
  boxWidth: number
}): SideRoute {
  const center = args.boxX + args.boxWidth / 2
  const endpoints = (args.sourceX + args.targetX) / 2
  if (endpoints < center) {
    return { side: 'right', gutterX: args.boxX + args.boxWidth + GUTTER_OFFSET }
  }
  return { side: 'left', gutterX: args.boxX - GUTTER_OFFSET }
}

interface LoopBackEdgeData {
  loop: Loop
  box?: Box
  [key: string]: unknown
}

function LoopBackEdge(props: EdgeProps) {
  const {
    id,
    sourceX,
    sourceY,
    targetX,
    targetY,
    markerEnd,
    style,
  } = props
  const data = props.data as LoopBackEdgeData | undefined

  // Fall back to a box derived from the endpoints when none is supplied.
  const box: Box =
    data?.box ?? {
      x: Math.min(sourceX, targetX) - 20,
      y: Math.min(sourceY, targetY),
      width: Math.abs(sourceX - targetX) + 40,
      height: Math.abs(sourceY - targetY),
    }

  const { gutterX, side } = chooseSide({
    sourceX,
    targetX,
    boxX: box.x,
    boxWidth: box.width,
  })

  const sidePos = side === 'right' ? Position.Right : Position.Left
  const midY = (sourceY + targetY) / 2

  // Segment 1: from the tail (source, bottom) out to the gutter midpoint.
  const [outPath] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition: Position.Bottom,
    targetX: gutterX,
    targetY: midY,
    targetPosition: sidePos,
    curvature: 0.6,
  })
  // Segment 2: from the gutter midpoint back to the head (target, top).
  const [inPath, labelX, labelY] = getBezierPath({
    sourceX: gutterX,
    sourceY: midY,
    sourcePosition: sidePos,
    targetX,
    targetY,
    targetPosition: Position.Top,
    curvature: 0.6,
  })

  const path = `${outPath} ${inPath}`
  const label =
    typeof data?.loop.backEdge.label === 'string'
      ? data.loop.backEdge.label.trim()
      : ''

  const edgeStyle: React.CSSProperties = {
    stroke: '#7c8cf5',
    strokeWidth: 2,
    strokeDasharray: '6 4',
    fill: 'none',
    ...style,
  }

  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} style={edgeStyle} />
      {label.length > 0 && (
        <EdgeLabelRenderer>
          <div
            style={{
              position: 'absolute',
              transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
              background: 'var(--bg-base, #14142b)',
              color: '#7c8cf5',
              fontSize: 11,
              padding: '0 4px',
              pointerEvents: 'none',
            }}
          >
            {label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

export default memo(LoopBackEdge)
```

- [ ] **Run, expect PASS:**

```
npm test -- src/graph/edges/LoopBackEdge.test.tsx
```
Expected: `chooseSide` and `LoopBackEdge — rendering` describe blocks all green.

- [ ] **Commit:**

```
git add dashboard/src/graph/edges/LoopBackEdge.tsx dashboard/src/graph/edges/LoopBackEdge.test.tsx
git commit -m "feat(ui-nits): LoopBackEdge side-routed return edge component [phase1 task3]

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

#### Part B — Pass the box onto the back-edge data (small layout follow-up)

`LoopBackEdge` routes best when it knows the body box. Extend the back-edge `data` in `useGraphLayout` to carry the computed `box` alongside `loop`.

- [ ] **Write the failing assertion** — append to `dashboard/src/graph/useGraphLayout.test.ts`:

```ts
describe('useGraphLayout — back-edge carries the container box', () => {
  it('includes a box on the loopBackEdge data', () => {
    const nodes = [
      { id: 'b', type: 'llm_call', label: 'B', description: '', config: {}, position: { x: 100, y: 0 } },
      { id: 'c', type: 'llm_call', label: 'C', description: '', config: {}, position: { x: 100, y: 200 } },
    ]
    const edges = [
      { source: 'b', target: 'c' },
      { source: 'c', target: 'b', label: 'again' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const back = result.current.rfEdges.find((e) => e.source === 'c' && e.target === 'b')!
    const box = (back.data as { box?: { width: number } }).box
    expect(box).toBeDefined()
    expect(box!.width).toBeGreaterThan(0)
  })
})
```

(`renderHook` is already imported in this test file from Task 2.)

- [ ] **Run, expect FAIL:**

```
npm test -- src/graph/useGraphLayout.test.ts
```
Expected: the new `back-edge carries the container box` test fails (`box` undefined).

- [ ] **Update `useGraphLayout.ts`.** In the back-edge mapping built in Task 2, the `data` field currently is `loop ? { loop } : undefined`. Build a per-loop box lookup and attach it. Right after the `loopByBackEdge` map is built, add:

```ts
    const boxByLoopId = new Map<string, ReturnType<typeof loopContainerBox>>()
    for (const loop of detection.loops) {
      boxByLoopId.set(loop.id, loopContainerBox(loop, positionMap))
    }
```

Then change the edge `data` line from:

```ts
        data: loop ? { loop } : undefined,
```

to:

```ts
        data: loop ? { loop, box: boxByLoopId.get(loop.id) ?? undefined } : undefined,
```

- [ ] **Run, expect PASS:**

```
npm test -- src/graph/useGraphLayout.test.ts
```
Expected: all green including the new box assertion.

- [ ] **Commit:**

```
git add dashboard/src/graph/useGraphLayout.ts dashboard/src/graph/useGraphLayout.test.ts
git commit -m "feat(ui-nits): pass loop body box onto back-edge data [phase1 task3]

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

#### Part C — Register `edgeTypes` and wire into `GraphView`

> **Cross-phase note:** Phase 1 KEEPS `fitView`/`fitViewOptions` on `<ReactFlow>` (zoom behavior is out of scope here). Phase 2 task-1 later REMOVES `fitView`/`fitViewOptions` and builds default-zoom centering ON TOP of the `edgeTypes={edgeTypes}` prop you add below — so the `edgeTypes` import + prop must remain. Do not remove them. Phase 2 also extends the `useReactFlow` mock in `GraphView.loops.test.tsx` to add `setCenter`/`getZoom`; that update belongs to Phase 2, not here.

- [ ] **Write** `dashboard/src/graph/edges/edgeTypes.ts` (stable module-level object, mirroring `nodeTypes.ts`):

```ts
import LoopBackEdge from './LoopBackEdge'

export const edgeTypes = { loopBackEdge: LoopBackEdge }
```

- [ ] **Write the failing integration test** by extending the EXISTING, real-layout path. The current `dashboard/src/routes/GraphView.test.tsx` mocks `@xyflow/react` and asserts node DOM identity. Add a NEW test file `dashboard/src/routes/GraphView.loops.test.tsx` that captures the props passed to a mocked `ReactFlow` and asserts a loop workflow yields a `loopContainer` node and a `loopBackEdge` edge end-to-end through the real `useGraphLayout`:

```tsx
import { render } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

const captured: { nodes?: Array<{ type?: string }>; edges?: Array<{ type?: string }> } = {}

vi.mock('@xyflow/react', () => ({
  ReactFlow: ({ nodes, edges }: { nodes: Array<{ type?: string }>; edges: Array<{ type?: string }> }) => {
    captured.nodes = nodes
    captured.edges = edges
    return <div data-testid="rf" />
  },
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  useNodes: () => [],
  useReactFlow: () => ({ flowToScreenPosition: (p: unknown) => p }),
}))

import { GraphView } from './ExecutionView'

const loopWorkflow: WorkflowDefinition = {
  name: 'wf',
  description: '',
  author: '',
  nodes: [
    { id: 'a', type: 'llm_call', label: 'A', description: '', config: {} },
    { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
    { id: 'c', type: 'llm_call', label: 'C', description: '', config: {} },
  ],
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'b', label: 'tests pass' },
  ],
}

const loopless: WorkflowDefinition = {
  ...loopWorkflow,
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
  ],
}

const renderView = (def: WorkflowDefinition) =>
  render(
    <GraphView
      instance={null as unknown as Instance}
      workflowDef={def}
      trace={[] as NodeExecution[]}
      onNodeClick={() => {}}
      selectedNodeId={null}
    />,
  )

describe('GraphView — loop rendering integration', () => {
  it('renders a loopContainer node and a loopBackEdge for a looping workflow', () => {
    renderView(loopWorkflow)
    expect(captured.nodes!.some((n) => n.type === 'loopContainer')).toBe(true)
    expect(captured.edges!.some((e) => e.type === 'loopBackEdge')).toBe(true)
  })

  it('renders neither for a loopless workflow', () => {
    renderView(loopless)
    expect(captured.nodes!.some((n) => n.type === 'loopContainer')).toBe(false)
    expect(captured.edges!.every((e) => e.type === 'smoothstep')).toBe(true)
  })
})
```

- [ ] **Run, expect PASS already for the layout assertions** (Tasks 1–2 made `useGraphLayout` emit these). Run it:

```
npm test -- src/routes/GraphView.loops.test.tsx
```
Expected: both tests green. (This test exercises the integration; it does not require the `edgeTypes` wiring yet — that wiring is what makes the real, unmocked `<ReactFlow>` actually render `LoopBackEdge`.)

- [ ] **Wire `edgeTypes` into `GraphView`** — modify `dashboard/src/routes/ExecutionView.tsx`. Add the import alongside the existing `nodeTypes` import (currently line 10):

```ts
import { edgeTypes } from '../graph/edges/edgeTypes'
```

Then pass it to the `<ReactFlow>` element. The element currently reads (lines 102–112):

```tsx
      <ReactFlow
        nodes={nodesWithSelection}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        onNodeClick={(_event, node) => onNodeClick(node.id)}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.3}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
```

Add the `edgeTypes` prop after `nodeTypes`:

```tsx
      <ReactFlow
        nodes={nodesWithSelection}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodeClick={(_event, node) => onNodeClick(node.id)}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.3}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
```

- [ ] **Run the existing GraphView flicker-guard test to confirm no regression** (its `@xyflow/react` mock ignores `edgeTypes`, so it stays green):

```
npm test -- src/routes/GraphView.test.tsx
```
Expected: both pre-existing tests green.

- [ ] **Type-check:**

```
npx tsc --noEmit
```
Expected: no errors.

- [ ] **Run the FULL suite** to confirm the whole feature is green end-to-end:

```
npm test
```
Expected: all test files pass, including `loops`, `loopLayout`, `LoopContainerNode`, `LoopBackEdge`, `useGraphLayout`, `GraphView`, and `GraphView.loops`.

- [ ] **Commit:**

```
git add dashboard/src/graph/edges/edgeTypes.ts dashboard/src/routes/GraphView.loops.test.tsx dashboard/src/routes/ExecutionView.tsx
git commit -m "feat(ui-nits): wire loopBackEdge edgeTypes into GraphView [phase1 task3]

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Manual verification (post-implementation, optional but recommended)

Per the design's "Visual sanity check": rebuild and run the dashboard against a real looping workflow (e.g. an existing refinement-loop workflow) and confirm:
- Loop body steps are wrapped in a labeled container reading `Loop until {label}` (or `Loop`).
- The return edge is dashed/accent-colored and bows out to the side rather than crossing the steps between tail and head.
- A loopless workflow renders exactly as before (no container, all `smoothstep` edges).
- Nested loops render as nested containers.

Build/run: from the repo root, rebuild the server binary (stale binary uses Postgres) and `./arbitron serve`, then open an instance of a looping workflow in the dashboard.
