# UI Nits Phase 1, Task 2: Dagre Loop Grouping + Container Overlay Nodes

> Continues `ui-nits-implementation-phase-1-task-1.md`. REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans. Depends on `dashboard/src/graph/loops.ts` from Task 1.

**Goal:** Make `useGraphLayout` (a) omit back-edges from dagre ranking and group each loop body with a dagre compound cluster so the body lays out in one column, (b) emit derived, non-interactive `loopContainer` overlay nodes sized to each loop body's bounding box, and (c) type back-edges as `loopBackEdge`. Add the `LoopContainerNode` component and register it in `nodeTypes`.

**Key constraints (from the design):**
- Container nodes are DERIVED overlays — never a React Flow `parentId`, never persisted, `selectable/focusable/draggable: false`, lower `zIndex` than steps.
- Bounding box is computed from the SAME `positionMap` the step nodes use, so it is correct for both the dagre branch and the stored-positions branch.
- Back-edges are excluded from dagre RANKING ONLY; they are still emitted as edges (typed `loopBackEdge`).
- Label = `Loop until {backEdge.label}` when the back-edge has a non-empty label, else `Loop`.
- The stored-positions branch keeps node positions EXACTLY as today; only container/edge derivation is added.

---

### Task 2: Layout helpers, `LoopContainerNode`, and `useGraphLayout` wiring

**Files:**
- Modify: `dashboard/src/graph/useGraphLayout.ts` (currently 97 lines; dagre branch lines 46–70, edge mapping lines 83–93, return line 95)
- Create: `dashboard/src/graph/loopLayout.ts` (pure geometry helpers — testable without React Flow)
- Create: `dashboard/src/graph/loopLayout.test.ts`
- Create: `dashboard/src/graph/nodes/LoopContainerNode.tsx`
- Create: `dashboard/src/graph/nodes/LoopContainerNode.module.css`
- Modify: `dashboard/src/graph/nodes/nodeTypes.ts` (currently 3 lines)
- Modify: `dashboard/src/graph/useGraphLayout.test.ts` IF it exists; otherwise create it (see step "useGraphLayout tests")

---

#### Part A — Pure container-geometry helpers (`loopLayout.ts`)

Two pure functions, isolated from React Flow so they are unit-testable against a plain `positionMap`:

```ts
import type { Loop } from './loops'

export const CONTAINER_PAD = 24
/** Extra inset per nesting level so nested containers sit inside their parent. */
export const DEPTH_INSET = 10
const NODE_WIDTH = 200
const NODE_HEIGHT = 80

export interface Box { x: number; y: number; width: number; height: number }

export function loopContainerLabel(loop: Loop): string {
  const raw = loop.backEdge.label
  const label = typeof raw === 'string' ? raw.trim() : ''
  return label.length > 0 ? `Loop until ${label}` : 'Loop'
}

/**
 * Bounding box around a loop's body nodes, padded and inset by nesting depth.
 * Returns null when no body node has a known position (nothing to wrap).
 */
export function loopContainerBox(
  loop: Loop,
  positionMap: Record<string, { x: number; y: number }>,
): Box | null
```

- [ ] **Write the failing test** `dashboard/src/graph/loopLayout.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import type { Loop } from './loops'
import {
  loopContainerLabel,
  loopContainerBox,
  CONTAINER_PAD,
  DEPTH_INSET,
} from './loopLayout'

const makeLoop = (over: Partial<Loop>): Loop => ({
  id: 'loop-b-c',
  headId: 'b',
  tailId: 'c',
  bodyNodeIds: ['b', 'c'],
  backEdge: { source: 'c', target: 'b' },
  depth: 0,
  ...over,
})

describe('loopContainerLabel — happy path & boundary', () => {
  it('prefixes a non-empty back-edge label with "Loop until "', () => {
    expect(
      loopContainerLabel(makeLoop({ backEdge: { source: 'c', target: 'b', label: 'tests pass' } })),
    ).toBe('Loop until tests pass')
  })

  it('falls back to "Loop" when the label is missing', () => {
    expect(loopContainerLabel(makeLoop({ backEdge: { source: 'c', target: 'b' } }))).toBe('Loop')
  })

  it('falls back to "Loop" when the label is whitespace-only', () => {
    expect(
      loopContainerLabel(makeLoop({ backEdge: { source: 'c', target: 'b', label: '   ' } })),
    ).toBe('Loop')
  })
})

describe('loopContainerBox — happy path', () => {
  it('wraps the body nodes with padding, accounting for node size', () => {
    // b at (100,0), c at (100,160). NODE_WIDTH=200, NODE_HEIGHT=80.
    const positionMap = { b: { x: 100, y: 0 }, c: { x: 100, y: 160 } }
    const box = loopContainerBox(makeLoop({}), positionMap)!
    // minX=100, minY=0, maxX=100+200=300, maxY=160+80=240.
    expect(box.x).toBe(100 - CONTAINER_PAD)
    expect(box.y).toBe(0 - CONTAINER_PAD)
    expect(box.width).toBe(300 - 100 + CONTAINER_PAD * 2)
    expect(box.height).toBe(240 - 0 + CONTAINER_PAD * 2)
  })
})

describe('loopContainerBox — boundary: nesting inset', () => {
  it('insets deeper loops so nested containers sit inside their parent', () => {
    const positionMap = { b: { x: 100, y: 0 }, c: { x: 100, y: 160 } }
    const shallow = loopContainerBox(makeLoop({ depth: 0 }), positionMap)!
    const deep = loopContainerBox(makeLoop({ depth: 2 }), positionMap)!
    expect(deep.x).toBe(shallow.x + DEPTH_INSET * 2)
    expect(deep.y).toBe(shallow.y + DEPTH_INSET * 2)
    expect(deep.width).toBe(shallow.width - DEPTH_INSET * 2 * 2)
    expect(deep.height).toBe(shallow.height - DEPTH_INSET * 2 * 2)
  })
})

describe('loopContainerBox — edge case: self-loop (single node)', () => {
  it('wraps a single body node', () => {
    const positionMap = { b: { x: 50, y: 50 } }
    const box = loopContainerBox(
      makeLoop({ id: 'loop-b-b', headId: 'b', tailId: 'b', bodyNodeIds: ['b'] }),
      positionMap,
    )!
    expect(box.x).toBe(50 - CONTAINER_PAD)
    expect(box.width).toBe(200 + CONTAINER_PAD * 2)
  })
})

describe('loopContainerBox — error/invalid: no known positions', () => {
  it('returns null when none of the body nodes are positioned', () => {
    expect(loopContainerBox(makeLoop({}), {})).toBeNull()
  })

  it('ignores body ids absent from the positionMap', () => {
    const box = loopContainerBox(
      makeLoop({ bodyNodeIds: ['b', 'ghost'] }),
      { b: { x: 0, y: 0 } },
    )!
    expect(box.x).toBe(0 - CONTAINER_PAD)
    expect(box.width).toBe(200 + CONTAINER_PAD * 2)
  })
})
```

- [ ] **Run, expect FAIL** (module missing):

```
npm test -- src/graph/loopLayout.test.ts
```
Expected: `Cannot find module './loopLayout'` / failed to load url. 0 passing.

- [ ] **Write** `dashboard/src/graph/loopLayout.ts`:

```ts
import type { Loop } from './loops'

export const CONTAINER_PAD = 24
export const DEPTH_INSET = 10
const NODE_WIDTH = 200
const NODE_HEIGHT = 80

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

export function loopContainerLabel(loop: Loop): string {
  const raw = loop.backEdge.label
  const label = typeof raw === 'string' ? raw.trim() : ''
  return label.length > 0 ? `Loop until ${label}` : 'Loop'
}

export function loopContainerBox(
  loop: Loop,
  positionMap: Record<string, { x: number; y: number }>,
): Box | null {
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  let found = false
  for (const id of loop.bodyNodeIds) {
    const p = positionMap[id]
    if (!p) continue
    found = true
    minX = Math.min(minX, p.x)
    minY = Math.min(minY, p.y)
    maxX = Math.max(maxX, p.x + NODE_WIDTH)
    maxY = Math.max(maxY, p.y + NODE_HEIGHT)
  }
  if (!found) return null

  const inset = DEPTH_INSET * loop.depth
  return {
    x: minX - CONTAINER_PAD + inset,
    y: minY - CONTAINER_PAD + inset,
    width: maxX - minX + CONTAINER_PAD * 2 - inset * 2,
    height: maxY - minY + CONTAINER_PAD * 2 - inset * 2,
  }
}
```

- [ ] **Run, expect PASS:**

```
npm test -- src/graph/loopLayout.test.ts
```
Expected: all tests green.

- [ ] **Commit:**

```
git add dashboard/src/graph/loopLayout.ts dashboard/src/graph/loopLayout.test.ts
git commit -m "feat(ui-nits): pure loop container geometry helpers [phase1 task2]

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

#### Part B — `LoopContainerNode` component

A non-interactive labeled box. Its `data` carries `label` and `depth`; width/height come from React Flow node `width`/`height` (set in the overlay node, see Part D), so the component fills its node box.

- [ ] **Write the failing test** `dashboard/src/graph/nodes/LoopContainerNode.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import LoopContainerNode from './LoopContainerNode'

// React Flow node components receive NodeProps; we only read data here.
const renderNode = (data: { label: string; depth: number }) =>
  render(
    // @ts-expect-error — partial NodeProps is sufficient for this unit test.
    <LoopContainerNode data={data} />,
  )

describe('LoopContainerNode', () => {
  it('renders the provided label text', () => {
    renderNode({ label: 'Loop until tests pass', depth: 0 })
    expect(screen.getByText('Loop until tests pass')).toBeInTheDocument()
  })

  it('renders the bare "Loop" label when that is provided', () => {
    renderNode({ label: 'Loop', depth: 1 })
    expect(screen.getByText('Loop')).toBeInTheDocument()
  })
})
```

- [ ] **Run, expect FAIL:**

```
npm test -- src/graph/nodes/LoopContainerNode.test.tsx
```
Expected: `Cannot find module './LoopContainerNode'`.

- [ ] **Write** `dashboard/src/graph/nodes/LoopContainerNode.module.css`:

```css
.container {
  width: 100%;
  height: 100%;
  box-sizing: border-box;
  border: 1px dashed var(--border-subtle, #444466);
  border-radius: var(--radius-md, 8px);
  background: rgba(124, 140, 245, 0.05);
  pointer-events: none;
}

.label {
  position: absolute;
  top: -10px;
  left: 12px;
  padding: 0 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
  color: var(--accent, #7c8cf5);
  background: var(--bg-base, #14142b);
  pointer-events: none;
}
```

- [ ] **Write** `dashboard/src/graph/nodes/LoopContainerNode.tsx`:

```tsx
import { memo } from 'react'
import type { NodeProps, Node } from '@xyflow/react'
import styles from './LoopContainerNode.module.css'

export interface LoopContainerData {
  label: string
  depth: number
  [key: string]: unknown
}

type LoopContainerNodeType = Node<LoopContainerData, 'loopContainer'>

function LoopContainerNode({ data }: NodeProps<LoopContainerNodeType>) {
  return (
    <div className={styles.container}>
      <span className={styles.label}>{data.label}</span>
    </div>
  )
}

export default memo(LoopContainerNode)
```

- [ ] **Run, expect PASS:**

```
npm test -- src/graph/nodes/LoopContainerNode.test.tsx
```
Expected: both tests green.

- [ ] **Register the type** — modify `dashboard/src/graph/nodes/nodeTypes.ts` from:

```ts
import ArbitronNode from './ArbitronNode'

export const nodeTypes = { arbitronNode: ArbitronNode }
```

to:

```ts
import ArbitronNode from './ArbitronNode'
import LoopContainerNode from './LoopContainerNode'

export const nodeTypes = {
  arbitronNode: ArbitronNode,
  loopContainer: LoopContainerNode,
}
```

- [ ] **Type-check, expect no errors:**

```
npx tsc --noEmit
```

- [ ] **Commit:**

```
git add dashboard/src/graph/nodes/LoopContainerNode.tsx dashboard/src/graph/nodes/LoopContainerNode.module.css dashboard/src/graph/nodes/LoopContainerNode.test.tsx dashboard/src/graph/nodes/nodeTypes.ts
git commit -m "feat(ui-nits): LoopContainerNode overlay component [phase1 task2]

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

#### Part C — `useGraphLayout` integration tests (write first)

The hook returns `{ rfNodes, rfEdges, needsAutoSave }`. After this task it must additionally:
1. Emit a `loopContainer` node per detected loop (id `loop-container-${loop.id}`), with `width`/`height`/`position` from `loopContainerBox`, `data: { label, depth }`, `selectable/focusable/draggable: false`, `zIndex` below step nodes, and ordered BEFORE step nodes in `rfNodes` so steps paint on top.
2. Type back-edges as `loopBackEdge` (keep `smoothstep` for all others), passing `data: { loop }` so Task 3's edge component can route around the body.
3. Omit back-edges from `g.setEdge(...)` in the dagre branch, and group each loop body under a dagre compound cluster (innermost loop wins for shared nodes).

Note on the React Flow `Node` type: `focusable` and `zIndex` are valid `Node` fields; `width`/`height` set the node box. The overlay nodes use `type: 'loopContainer'`.

- [ ] **Create/append** `dashboard/src/graph/useGraphLayout.test.ts` with these tests:

```ts
import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useGraphLayout } from './useGraphLayout'
import type { WorkflowNode, WorkflowEdge } from './types'

const node = (id: string): WorkflowNode => ({
  id,
  type: 'llm_call',
  label: id.toUpperCase(),
  description: '',
  config: {},
})

describe('useGraphLayout — no loops', () => {
  it('emits no loopContainer nodes and no loopBackEdge edges', () => {
    const nodes = [node('a'), node('b'), node('c')]
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    expect(result.current.rfNodes.some((n) => n.type === 'loopContainer')).toBe(false)
    expect(result.current.rfEdges.every((e) => e.type === 'smoothstep')).toBe(true)
    // Every step node is still present.
    expect(result.current.rfNodes.filter((n) => n.type === 'arbitronNode')).toHaveLength(3)
  })
})

describe('useGraphLayout — single loop (dagre branch, no stored positions)', () => {
  const nodes = [node('a'), node('b'), node('c'), node('d')]
  const edges: WorkflowEdge[] = [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'd' },
    { source: 'c', target: 'b', label: 'tests pass' },
  ]

  it('emits exactly one loopContainer node with the expected label', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const containers = result.current.rfNodes.filter((n) => n.type === 'loopContainer')
    expect(containers).toHaveLength(1)
    expect((containers[0].data as { label: string }).label).toBe('Loop until tests pass')
  })

  it('orders container nodes before step nodes so steps paint on top', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const firstStepIdx = result.current.rfNodes.findIndex((n) => n.type === 'arbitronNode')
    const containerIdx = result.current.rfNodes.findIndex((n) => n.type === 'loopContainer')
    expect(containerIdx).toBeLessThan(firstStepIdx)
  })

  it('marks the container non-interactive', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const c = result.current.rfNodes.find((n) => n.type === 'loopContainer')!
    expect(c.selectable).toBe(false)
    expect(c.draggable).toBe(false)
    expect(c.focusable).toBe(false)
  })

  it('types the back-edge as loopBackEdge and keeps forward edges smoothstep', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const back = result.current.rfEdges.find((e) => e.source === 'c' && e.target === 'b')!
    expect(back.type).toBe('loopBackEdge')
    const fwd = result.current.rfEdges.find((e) => e.source === 'a' && e.target === 'b')!
    expect(fwd.type).toBe('smoothstep')
  })

  it('passes the loop onto the back-edge data for routing', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const back = result.current.rfEdges.find((e) => e.source === 'c' && e.target === 'b')!
    expect((back.data as { loop?: { headId: string } } | undefined)?.loop?.headId).toBe('b')
  })
})

describe('useGraphLayout — stored positions branch', () => {
  it('derives a container without changing node positions or needsAutoSave', () => {
    const nodes: WorkflowNode[] = [
      { ...node('b'), position: { x: 100, y: 0 } },
      { ...node('c'), position: { x: 100, y: 200 } },
    ]
    const edges: WorkflowEdge[] = [{ source: 'b', target: 'c', label: 'again' }, { source: 'c', target: 'b' }]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    expect(result.current.needsAutoSave).toBe(false)
    const b = result.current.rfNodes.find((n) => n.id === 'b')!
    expect(b.position).toEqual({ x: 100, y: 0 })
    expect(result.current.rfNodes.some((n) => n.type === 'loopContainer')).toBe(true)
  })
})

describe('useGraphLayout — nested loops', () => {
  it('emits one container per loop', () => {
    const nodes = [node('a'), node('b'), node('c'), node('d'), node('e')]
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'c', target: 'd' },
      { source: 'd', target: 'e' },
      { source: 'e', target: 'b' },
      { source: 'd', target: 'c' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    expect(result.current.rfNodes.filter((n) => n.type === 'loopContainer')).toHaveLength(2)
  })
})
```

- [ ] **Run, expect FAIL** (current hook emits no containers / no `loopBackEdge`):

```
npm test -- src/graph/useGraphLayout.test.ts
```
Expected: the "single loop", "stored positions", and "nested loops" describe blocks fail (e.g. `expected [] to have length 1`). The "no loops" block may already pass.

---

#### Part D — Implement `useGraphLayout` changes

- [ ] **Add imports** at the top of `dashboard/src/graph/useGraphLayout.ts` (after the existing `import type { ArbitronNodeData }` line):

```ts
import { detectLoops, type Loop } from './loops'
import { loopContainerBox, loopContainerLabel } from './loopLayout'
```

- [ ] **Replace the dagre edge-seeding loop and add compound grouping.** Change the dagre branch (currently lines 49–58):

```ts
      const g = new dagre.graphlib.Graph()
      g.setDefaultEdgeLabel(() => ({}))
      g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })

      for (const n of nodes) {
        g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
      }
      for (const e of edges) {
        g.setEdge(e.source, e.target)
      }
```

to (the `compound: true` graph option enables `setParent`; back-edges are skipped from ranking; innermost loop — largest `depth` — wins as the parent cluster for shared nodes):

```ts
      const g = new dagre.graphlib.Graph({ compound: true })
      g.setDefaultEdgeLabel(() => ({}))
      g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })

      for (const n of nodes) {
        g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
      }

      // Compound clusters: group each loop body so dagre keeps it in one column.
      // Innermost loop (largest depth) wins for nodes shared across loops.
      const clusterByNode: Record<string, { clusterId: string; depth: number }> = {}
      for (const loop of detection.loops) {
        const clusterId = `cluster-${loop.id}`
        g.setNode(clusterId, {})
        for (const id of loop.bodyNodeIds) {
          const existing = clusterByNode[id]
          if (!existing || loop.depth > existing.depth) {
            clusterByNode[id] = { clusterId, depth: loop.depth }
          }
        }
      }
      for (const [nodeId, { clusterId }] of Object.entries(clusterByNode)) {
        g.setParent(nodeId, clusterId)
      }

      // Seed only forward edges so back-edges do not distort rank assignment.
      for (const e of edges) {
        if (detection.backEdgeKeys.has(`${e.source} ${e.target}`)) continue
        g.setEdge(e.source, e.target)
      }
```

- [ ] **Compute `detection` once, before the branch.** Insert immediately after the `if (nodes.length === 0) { ... }` guard (currently ends line 31) and before the `allHavePositions` computation:

```ts
    const detection = detectLoops(edges)
```

- [ ] **Build container overlay nodes** after `positionMap` is finalized (i.e. after the `if (allHavePositions) {...} else {...}` block, currently ending line 70, before the `const rfNodes` mapping on line 72). Insert:

```ts
    const containerNodes: Node<{ label: string; depth: number }>[] = []
    for (const loop of detection.loops) {
      const box = loopContainerBox(loop, positionMap)
      if (!box) continue
      containerNodes.push({
        id: `loop-container-${loop.id}`,
        type: 'loopContainer',
        position: { x: box.x, y: box.y },
        width: box.width,
        height: box.height,
        data: { label: loopContainerLabel(loop), depth: loop.depth },
        selectable: false,
        focusable: false,
        draggable: false,
        // Below step nodes; deeper loops sit slightly above shallower ones.
        zIndex: -10 + loop.depth,
      })
    }
```

- [ ] **Type the back-edges** — replace the edge mapping (currently lines 83–93):

```ts
    const rfEdges: Edge[] = edges.map((e, i) => ({
      id: `e-${e.source}-${e.target}-${i}`,
      source: e.source,
      target: e.target,
      label: e.label ?? e.output ?? undefined,
      type: 'smoothstep',
      animated: executionStates[e.source] === 'running',
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
      style: { stroke: '#444466' },
      labelStyle: { fill: '#888888', fontSize: 11 },
    }))
```

with:

```ts
    const loopByBackEdge = new Map<string, Loop>()
    for (const loop of detection.loops) {
      loopByBackEdge.set(`${loop.tailId} ${loop.headId}`, loop)
    }

    const rfEdges: Edge[] = edges.map((e, i) => {
      const key = `${e.source} ${e.target}`
      const loop = loopByBackEdge.get(key)
      const isBack = detection.backEdgeKeys.has(key)
      return {
        id: `e-${e.source}-${e.target}-${i}`,
        source: e.source,
        target: e.target,
        label: e.label ?? e.output ?? undefined,
        type: isBack ? 'loopBackEdge' : 'smoothstep',
        animated: executionStates[e.source] === 'running',
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
        style: { stroke: isBack ? '#7c8cf5' : '#444466' },
        labelStyle: { fill: '#888888', fontSize: 11 },
        data: loop ? { loop } : undefined,
        zIndex: isBack ? 5 : undefined,
      }
    })
```

- [ ] **Return containers before step nodes** — change the return (currently line 95):

```ts
    return { rfNodes, rfEdges, needsAutoSave }
```

to:

```ts
    return { rfNodes: [...containerNodes, ...rfNodes], rfEdges, needsAutoSave }
```

Note: the `rfNodes` type annotation is `Node<ArbitronNodeData>[]`; the combined array mixes container nodes. Widen the function's `UseGraphLayoutOutput.rfNodes` type to `Node[]` (import `Node` is already imported). Update the interface (currently line 18):

```ts
  rfNodes: Node[]
```

and remove the now-too-narrow generic on the local `rfNodes` declaration if `tsc` complains, leaving `const rfNodes: Node<ArbitronNodeData>[] = nodes.map(...)` as-is (it is a subtype of `Node`, so spreading into `Node[]` is fine).

- [ ] **Run the hook tests, expect PASS:**

```
npm test -- src/graph/useGraphLayout.test.ts
```
Expected: all describe blocks green.

- [ ] **Run the full graph suite to confirm no regression** (the existing `GraphView.test.tsx` mocks `@xyflow/react`; `loopContainer` nodes flow through its mock as plain nodes, so it stays green):

```
npm test -- src/graph src/routes/GraphView.test.tsx
```
Expected: all green, including the pre-existing flicker-guard tests.

- [ ] **Type-check:**

```
npx tsc --noEmit
```
Expected: no errors.

- [ ] **Commit:**

```
git add dashboard/src/graph/useGraphLayout.ts dashboard/src/graph/useGraphLayout.test.ts
git commit -m "feat(ui-nits): dagre loop grouping + derived container overlays [phase1 task2]

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Next task

**Task 3** (`ui-nits-implementation-phase-1-task-3.md`): the `LoopBackEdge` custom edge component that routes the return edge up a side gutter with a dashed accent curve, wiring `edgeTypes` into `GraphView`, and an end-to-end render test through the real (unmocked) layout.
