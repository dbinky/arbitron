# Workflow Auto-Arrangement — Phase 3 Task 3: Shared renderer `ArrangedGraph` + fallback conversion

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Execution discipline:** Expected outputs below are predictions, never recorded test results.
Where an initial run cannot compile/import a new symbol, add only its declaration and a
minimal compiling non-passing body, then rerun and observe the named behavioral assertion
fail before implementing behavior. Keep each subtask green before committing; do not commit
empty success stubs or knowingly failing tests. Test count examples are illustrative: record
the actual command/output at execution. Each data path needs an assertion at its consumer
and an absent/nil/empty/wrong-input negative control; constructor/call-count checks alone
cannot establish that the graph, status, evidence or retry was fed correctly.

**Alignment requirements:** Read contracts §6 (6.5/6.6) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Build the one graph renderer that WorkflowReview, ExecutionView and the render surface all share. It draws validated reviewed geometry exactly. Otherwise it falls back to revision-scoped retained positions, then manifest positions, then dagre. It never drags, never auto-saves, and never auto-fits.

**Architecture:**
- `buildArrangedFlow` turns a validated `Geometry` into xyflow nodes/edges. Positions, sizes, routes and gaps come from the payload. Node type and runtime state come from the caller.
- `fallbackPositions` merges retained and manifest positions per node and places missing nodes deterministically. It replaces `useGraphLayout`'s all-or-nothing rule through a new `positions` input. `useGraphLayout` drops `needsAutoSave`.
- `ArrangedGraph` calls `resolveDisplayGeometry` (P3T1) and runs its hooks unconditionally. It renders arranged or fallback mode inside `<div data-arranged-canvas>`, centres once per displayed layout at `DEFAULT_ZOOM`, and offers an explicit "Fit all" control. `fixedViewport` gives the render surface a static, chrome-free canvas.

**Tech Stack:** TypeScript 6 (strict, noUnusedLocals/Parameters, relative imports only), React 19, @xyflow/react 12, Vitest 4 + jsdom + Testing Library, CSS modules, dagre (fallback only).

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` · Design: `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` (§4 shared renderer, §5 navigation/status/refresh, §6 remove manual positioning) · Contracts: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md` §3 Dashboard TS + §5 Addenda 5–7

**Depends on:**
- P3T1: `types.ts`, `validate.ts` (`resolveDisplayGeometry`, `RENDERER_CONTRACT`), `testFixtures.ts`.
- P3T2: `ArrangedNode`, `RoutedEdge`, `fonts.ts`.

**Decision recorded here:** removing `needsAutoSave` from `useGraphLayout` breaks `WorkflowReview.tsx`. **This task (P3T3) deletes WorkflowReview's `needsAutoSave` autosave effect.** P3T12 then deletes the remaining manual-positioning code (drag handler, `collectPositions`, `saveLayout`, the local ReactFlow).

**Interfaces (consumed):**
```ts
// P3T1
export interface LayoutView { savedRevision: string; resultGeneration: number; geometry?: Geometry; retained?: RetainedLayout; /* … */ }
export interface RetainedLayout { source: 'layout' | 'manifest' | 'none'; positions?: Record<string, { x: number; y: number }>; /* … */ }
export function resolveDisplayGeometry(def: WorkflowDefinition, view: LayoutView | null): { geometry: Geometry | null; issues: string[] }
export function fixtureWorkflow(): WorkflowDefinition; export function fixtureGeometry(): Geometry; export function fixtureView(o?: Partial<LayoutView>): LayoutView
// P3T2
export default memo(ArrangedNode); export interface ArrangedNodeData { nodeBox: NodeBox; nodeType: string; executionState: NodeExecutionState; contract: RendererContract; [k: string]: unknown }
export default memo(RoutedEdge);   export interface RoutedEdgeData { route: EdgeRoute; gaps: Point[]; contract: RendererContract; active: boolean; graphInstanceId: string; [k: string]: unknown }
// existing
export const NODE_WIDTH = 200; export const NODE_HEIGHT = 80          // graph/useGraphLayout.ts
export const DEFAULT_ZOOM = 0.9                                        // graph/constants.ts
export const nodeTypes /* arbitronNode, loopContainer */; export const edgeTypes /* loopBackEdge */
```

**Interfaces (produced; contracts §3; ★ = addition to contracts):**
```ts
// buildArrangedFlow.ts
export function buildArrangedFlow(geo: Geometry, nodeTypesById: Record<string, string>, executionStates: Record<string, NodeExecutionState>, graphInstanceId: string, activeSources?: Set<string>): { nodes: Node<ArrangedNodeData>[]; edges: Edge<RoutedEdgeData>[] }
export const arrangedNodeTypes: NodeTypes  // { arrangedNode: ArrangedNode }
export const arrangedEdgeTypes: EdgeTypes  // { routedEdge: RoutedEdge }
// fallbackLayout.ts
export function fallbackPositions(nodes: WorkflowNode[], retained: Record<string, {x:number;y:number}> | undefined): Record<string, {x:number;y:number}> | null
export const FALLBACK_STACK_PITCH = 120   // ★ top-to-top pitch of stacked unpositioned nodes
// useGraphLayout.ts — input gains positions?: Record<string,{x:number;y:number}>; output is { rfNodes, rfEdges } (needsAutoSave removed)
// ArrangedGraph.tsx (must be rendered inside <ReactFlowProvider>)
export interface ArrangedGraphProps {
  workflowDef: WorkflowDefinition
  view: LayoutView | null
  executionStates?: Record<string, NodeExecutionState>
  selectedNodeId: string | null
  selectedNodeIds?: ReadonlySet<string>
  onNodeClick: (nodeId: string) => void
  anchorNodeId: string | null
  onMoveStart?: (event: unknown) => void
  onDisplayIssues?: (issues: readonly string[]) => void
  onAnchorPosition?: (center: { x: number; y: number } | null) => void   // ★ feeds ExecutionView's follow hook (P3T13)
  fixedViewport?: { x: number; y: number; zoom: number }
  children?: React.ReactNode
}
export default function ArrangedGraph(props: ArrangedGraphProps): JSX.Element
export function nodeCenter(view: LayoutView | null, rfNodes: Node[], nodeId: string): { x: number; y: number } | null
// DOM: <div data-arranged-canvas="true" data-layout-mode="arranged"|"fallback"> (★ data-layout-mode)
```

---

### Task 3.1: `fallbackPositions` — revision-scoped retained positions with deterministic placement

**Files:**
- Create: `dashboard/src/graph/arranged/fallbackLayout.ts`
- Test: `dashboard/src/graph/arranged/fallbackLayout.test.ts`

Rule:
- Each node gets its retained position if it has one, otherwise its manifest position.
- Nodes with neither are stacked at the minimum placed x, in manifest order. The first stacked top is `lowestBottom + (120 - NODE_HEIGHT)` with `lowestBottom = max(y + NODE_HEIGHT)`. Each following one is +120.
- If no node has a position, return `null` so the caller uses dagre.

- [x] **Step 1: Write the failing test**

```ts
import { describe, it, expect } from 'vitest'
import { fallbackPositions, FALLBACK_STACK_PITCH } from './fallbackLayout'
import { NODE_HEIGHT } from '../useGraphLayout'
import type { WorkflowNode } from '../types'

const node = (id: string, position?: { x: number; y: number }): WorkflowNode => ({
  id,
  type: 'script',
  label: id,
  description: '',
  config: {},
  ...(position ? { position } : {}),
})

describe('fallbackPositions', () => {
  it('returns null when no node has a retained or manifest position (caller uses dagre)', () => {
    expect(fallbackPositions([node('a'), node('b')], undefined)).toBeNull()
    expect(fallbackPositions([node('a')], {})).toBeNull()
  })

  it('returns null for an empty workflow', () => {
    expect(fallbackPositions([], { a: { x: 1, y: 2 } })).toBeNull()
  })

  it('uses revision-scoped retained positions for every known node', () => {
    const out = fallbackPositions([node('a'), node('b')], { a: { x: 10, y: 20 }, b: { x: 10, y: 200 } })
    expect(out).toEqual({ a: { x: 10, y: 20 }, b: { x: 10, y: 200 } })
  })

  it('prefers the retained position over the manifest position', () => {
    const out = fallbackPositions([node('a', { x: 500, y: 500 })], { a: { x: 1, y: 2 } })
    expect(out).toEqual({ a: { x: 1, y: 2 } })
  })

  it('falls back to the manifest position when a node has no retained one', () => {
    const out = fallbackPositions([node('a'), node('b', { x: 70, y: 300 })], { a: { x: 10, y: 20 } })
    expect(out).toEqual({ a: { x: 10, y: 20 }, b: { x: 70, y: 300 } })
  })

  it('ignores retained entries for ids that are not in the workflow', () => {
    const out = fallbackPositions([node('a')], { a: { x: 0, y: 0 }, ghost: { x: 9, y: 9 } })
    expect(out).toEqual({ a: { x: 0, y: 0 } })
  })

  it('stacks unpositioned nodes below the lowest placed node, at the minimum x, in manifest order', () => {
    const out = fallbackPositions(
      [node('a'), node('new1'), node('b'), node('new2')],
      { a: { x: 40, y: 0 }, b: { x: 300, y: 400 } },
    )!
    const firstY = 400 + NODE_HEIGHT + (FALLBACK_STACK_PITCH - NODE_HEIGHT)
    expect(out.new1).toEqual({ x: 40, y: firstY })
    expect(out.new2).toEqual({ x: 40, y: firstY + FALLBACK_STACK_PITCH })
    // Placed nodes are untouched.
    expect(out.a).toEqual({ x: 40, y: 0 })
    expect(out.b).toEqual({ x: 300, y: 400 })
  })

  it('never overlaps a stacked node with the lowest placed node box', () => {
    const out = fallbackPositions([node('a'), node('m')], { a: { x: 0, y: 100 } })!
    expect(out.m.y).toBeGreaterThanOrEqual(100 + NODE_HEIGHT)
  })

  it('treats non-finite coordinates as missing', () => {
    const out = fallbackPositions([node('a'), node('b')], { a: { x: 0, y: 0 }, b: { x: Number.NaN, y: 5 } })!
    expect(out.b).toEqual({ x: 0, y: NODE_HEIGHT + (FALLBACK_STACK_PITCH - NODE_HEIGHT) })
  })

  it('is deterministic for the same input', () => {
    const nodes = [node('a'), node('x'), node('y')]
    const retained = { a: { x: 5, y: 5 } }
    expect(fallbackPositions(nodes, retained)).toEqual(fallbackPositions(nodes, retained))
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

```bash
cd dashboard && npx vitest run src/graph/arranged/fallbackLayout.test.ts
```
Expected:
```text
Error: Failed to resolve import "./fallbackLayout" from "src/graph/arranged/fallbackLayout.test.ts". Does the file exist?
```

- [x] **Step 3: Implement**

```ts
import type { WorkflowNode } from '../types'
import { NODE_HEIGHT } from '../useGraphLayout'

type XY = { x: number; y: number }

/** Vertical pitch (px, top-to-top) between nodes stacked below the placed ones. */
export const FALLBACK_STACK_PITCH = 120

function isXY(p: { x?: unknown; y?: unknown } | null | undefined): p is XY {
  return p != null && typeof p.x === 'number' && Number.isFinite(p.x) && typeof p.y === 'number' && Number.isFinite(p.y)
}

/**
 * Complete top-left position map for the legacy/no-geometry fallback renderer.
 * Precedence per node: revision-scoped retained position → manifest position.
 * Nodes with neither are stacked deterministically (manifest order) at the
 * minimum placed x, starting one pitch below the lowest placed node's top and
 * never overlapping its bottom (y + NODE_HEIGHT). Returns null when no node has
 * any position, so the caller lays the whole graph out with dagre.
 */
export function fallbackPositions(
  nodes: WorkflowNode[],
  retained: Record<string, { x: number; y: number }> | undefined,
): Record<string, { x: number; y: number }> | null {
  const out: Record<string, XY> = {}
  const missing: WorkflowNode[] = []
  for (const n of nodes) {
    const r = retained?.[n.id]
    if (isXY(r)) out[n.id] = { x: r.x, y: r.y }
    else if (isXY(n.position)) out[n.id] = { x: n.position.x, y: n.position.y }
    else missing.push(n)
  }
  const placed = Object.values(out)
  if (placed.length === 0) return null
  if (missing.length === 0) return out

  const minX = Math.min(...placed.map((p) => p.x))
  const lowestBottom = Math.max(...placed.map((p) => p.y + NODE_HEIGHT))
  let y = lowestBottom + (FALLBACK_STACK_PITCH - NODE_HEIGHT)
  for (const n of missing) {
    out[n.id] = { x: minX, y }
    y += FALLBACK_STACK_PITCH
  }
  return out
}
```

- [x] **Step 4: Run it and confirm it passes**

```bash
cd dashboard && npx vitest run src/graph/arranged/fallbackLayout.test.ts
```
Expected:
```text
      Tests  10 passed (10)
```

- [x] **Step 5: Commit**

```bash
git add dashboard/src/graph/arranged/fallbackLayout.ts dashboard/src/graph/arranged/fallbackLayout.test.ts && git commit -m "feat(dashboard): add revision-scoped fallback positions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3.2: `useGraphLayout` takes a `positions` map; `needsAutoSave` is removed everywhere

**Files:**
- Modify: `dashboard/src/graph/useGraphLayout.ts:23-62` (input/output types, position selection) and `:188-189` (return + memo deps)
- Modify: `dashboard/src/graph/useGraphLayout.test.ts:14-21`, `:95-107` (drop `needsAutoSave` assertions), append a new `describe`
- Modify: `dashboard/src/routes/WorkflowReview.tsx:46` and delete `:60-68` (the `needsAutoSave` autosave effect)

- [x] **Step 1: Update the test.** Apply this diff to `dashboard/src/graph/useGraphLayout.test.ts`:

```diff
--- a/dashboard/src/graph/useGraphLayout.test.ts
+++ b/dashboard/src/graph/useGraphLayout.test.ts
@@ -12,11 +12,10 @@
 })
 
 describe('useGraphLayout — error/invalid: empty nodes list', () => {
-  it('returns empty arrays and needsAutoSave=false when nodes is empty', () => {
+  it('returns empty arrays when nodes is empty', () => {
     const { result } = renderHook(() => useGraphLayout({ nodes: [], edges: [] }))
     expect(result.current.rfNodes).toEqual([])
     expect(result.current.rfEdges).toEqual([])
-    expect(result.current.needsAutoSave).toBe(false)
   })
 })
 
@@ -93,14 +92,13 @@
 })
 
 describe('useGraphLayout — stored positions branch', () => {
-  it('derives a container without changing node positions or needsAutoSave', () => {
+  it('derives a container without changing node positions', () => {
     const nodes: WorkflowNode[] = [
       { ...node('b'), position: { x: 100, y: 0 } },
       { ...node('c'), position: { x: 100, y: 200 } },
     ]
     const edges: WorkflowEdge[] = [{ source: 'b', target: 'c', label: 'again' }, { source: 'c', target: 'b' }]
     const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
-    expect(result.current.needsAutoSave).toBe(false)
     const b = result.current.rfNodes.find((n) => n.id === 'b')!
     expect(b.position).toEqual({ x: 100, y: 0 })
     expect(result.current.rfNodes.some((n) => n.type === 'loopContainer')).toBe(true)
@@ -300,3 +298,46 @@
     expect(result.current.rfEdges).toBe(firstEdges)
   })
 })
+
+describe('useGraphLayout — positions input (fallback conversion)', () => {
+  const edges: WorkflowEdge[] = [
+    { source: 'a', target: 'b' },
+    { source: 'b', target: 'c' },
+  ]
+
+  it('uses a complete positions map instead of dagre when manifest positions are partial', () => {
+    // Only 'a' has a manifest position: the all-or-nothing rule alone would discard it and run dagre.
+    const nodes: WorkflowNode[] = [{ ...node('a'), position: { x: 5, y: 5 } }, node('b'), node('c')]
+    const positions = { a: { x: 5, y: 5 }, b: { x: 5, y: 125 }, c: { x: 5, y: 245 } }
+    const { result } = renderHook(() => useGraphLayout({ nodes, edges, positions }))
+    const pos = Object.fromEntries(result.current.rfNodes.map((n) => [n.id, n.position]))
+    expect(pos).toEqual(positions)
+  })
+
+  it('prefers the positions map over complete manifest positions', () => {
+    const nodes: WorkflowNode[] = [
+      { ...node('a'), position: { x: 0, y: 0 } },
+      { ...node('b'), position: { x: 0, y: 100 } },
+      { ...node('c'), position: { x: 0, y: 200 } },
+    ]
+    const positions = { a: { x: 900, y: 0 }, b: { x: 900, y: 100 }, c: { x: 900, y: 200 } }
+    const { result } = renderHook(() => useGraphLayout({ nodes, edges, positions }))
+    expect(result.current.rfNodes.find((n) => n.id === 'b')!.position).toEqual({ x: 900, y: 100 })
+  })
+
+  it('ignores an incomplete positions map and falls back to dagre', () => {
+    const nodes = [node('a'), node('b'), node('c')]
+    const { result: withPartial } = renderHook(() =>
+      useGraphLayout({ nodes, edges, positions: { a: { x: 7777, y: 7777 } } }),
+    )
+    const { result: dagreOnly } = renderHook(() => useGraphLayout({ nodes, edges }))
+    expect(withPartial.current.rfNodes.find((n) => n.id === 'a')!.position).toEqual(
+      dagreOnly.current.rfNodes.find((n) => n.id === 'a')!.position,
+    )
+  })
+
+  it('no longer reports needsAutoSave (fallback layouts are display-only)', () => {
+    const { result } = renderHook(() => useGraphLayout({ nodes: [node('a')], edges: [] }))
+    expect('needsAutoSave' in result.current).toBe(false)
+  })
+})
```

- [x] **Step 2: Run it and confirm it fails**

```bash
cd dashboard && npx vitest run src/graph/useGraphLayout.test.ts
```
Expected:
```text
× uses a complete positions map instead of dagre when manifest positions are partial
× prefers the positions map over complete manifest positions
× no longer reports needsAutoSave (fallback layouts are display-only)
      Tests  3 failed | 20 passed (23)
```

- [x] **Step 3: Implement.** Apply to `dashboard/src/graph/useGraphLayout.ts`:

```diff
--- a/dashboard/src/graph/useGraphLayout.ts
+++ b/dashboard/src/graph/useGraphLayout.ts
@@ -24,42 +24,59 @@
   nodes: WorkflowNode[]
   edges: WorkflowEdge[]
   executionStates?: Record<string, NodeExecutionState>
+  /**
+   * Complete top-left position map (from arranged/fallbackLayout.ts
+   * fallbackPositions). When it has a finite entry for every node it is used
+   * as-is; otherwise the manifest all-or-nothing rule / dagre applies.
+   */
+  positions?: Record<string, { x: number; y: number }>
 }
 
 interface UseGraphLayoutOutput {
   rfNodes: Node[]
   rfEdges: Edge[]
-  needsAutoSave: boolean
+}
+
+function hasXY(p: { x?: unknown; y?: unknown } | null | undefined): p is { x: number; y: number } {
+  return p != null && typeof p.x === 'number' && Number.isFinite(p.x) && typeof p.y === 'number' && Number.isFinite(p.y)
 }
 
 export function useGraphLayout({
   nodes,
   edges,
   executionStates = EMPTY_EXECUTION_STATES,
+  positions,
 }: UseGraphLayoutInput): UseGraphLayoutOutput {
   return useMemo(() => {
     if (nodes.length === 0) {
-      return { rfNodes: [], rfEdges: [], needsAutoSave: false }
+      return { rfNodes: [], rfEdges: [] }
     }
 
     const detection = detectLoops(edges)
+
+    // A complete caller-supplied map (revision-scoped retained + manifest +
+    // deterministic placement) wins over the manifest all-or-nothing rule.
+    const positionsComplete = positions != null && nodes.every((n) => hasXY(positions[n.id]))
 
     // All-or-nothing rule: use stored positions only when every node has one.
     const allHavePositions = nodes.every(
       (n) => hasXY(n.position),
     )
 
-    let needsAutoSave = false
     let positionMap: Record<string, { x: number; y: number }>
 
-    if (allHavePositions) {
+    if (positionsComplete) {
       positionMap = {}
       for (const n of nodes) {
+        positionMap[n.id] = { x: positions![n.id].x, y: positions![n.id].y }
+      }
+    } else if (allHavePositions) {
+      positionMap = {}
+      for (const n of nodes) {
         positionMap[n.id] = { x: n.position!.x, y: n.position!.y }
       }
     } else {
-      // Run dagre for the entire graph.
-      needsAutoSave = true
+      // Run dagre for the entire graph. Display-only: nothing is ever saved.
       const g = new dagre.graphlib.Graph({ compound: true })
       g.setDefaultEdgeLabel(() => ({}))
       g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })
@@ -185,6 +202,6 @@
       }
     })
 
-    return { rfNodes: [...containerNodes, ...rfNodes], rfEdges, needsAutoSave }
-  }, [nodes, edges, executionStates])
-}
+    return { rfNodes: [...containerNodes, ...rfNodes], rfEdges }
+  }, [nodes, edges, executionStates, positions])
+}
```

Remove the only production consumer of `needsAutoSave`, in `dashboard/src/routes/WorkflowReview.tsx`. Fallback layouts are display-only and must never be written back.

```diff
--- a/dashboard/src/routes/WorkflowReview.tsx
+++ b/dashboard/src/routes/WorkflowReview.tsx
@@ -43,7 +43,7 @@
   selectedNodeId: string | null
   workflowName: string
 }) {
-  const { rfNodes, rfEdges, needsAutoSave } = useGraphLayout({
+  const { rfNodes, rfEdges } = useGraphLayout({
     nodes: workflowDef.nodes,
     edges: workflowDef.edges,
   })
@@ -56,16 +56,6 @@
   useEffect(() => {
     setNodes(rfNodes)
   }, [rfNodes])
-
-  // Auto-save on first layout computation if positions were dagre-generated.
-  const autoSaved = useRef(false)
-  useEffect(() => {
-    if (needsAutoSave && !autoSaved.current && rfNodes.length > 0) {
-      autoSaved.current = true
-      const positions = collectPositions(rfNodes)
-      api.workflows.saveLayout(workflowName, positions).catch(console.error)
-    }
-  }, [needsAutoSave, rfNodes, workflowName])
 
   const onNodeDragStop: OnNodeDrag<Node> = useCallback(
     (_event, node) => {
```

- [x] **Step 4: Run it and confirm it passes; confirm nothing else reads `needsAutoSave`**

```bash
cd dashboard && npx vitest run src/graph/useGraphLayout.test.ts src/routes && npx tsc --noEmit && grep -rn needsAutoSave src | grep -v useGraphLayout.test.ts
```
Expected:
```text
      Tests  … passed  (useGraphLayout.test.ts: 23 passed)
(tsc exits 0; grep prints nothing)
```

- [x] **Step 5: Commit**

```bash
git add dashboard/src/graph/useGraphLayout.ts dashboard/src/graph/useGraphLayout.test.ts dashboard/src/routes/WorkflowReview.tsx && git commit -m "feat(dashboard): feed fallback positions to useGraphLayout and drop needsAutoSave" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3.3: `buildArrangedFlow`

**Files:**
- Create: `dashboard/src/graph/arranged/buildArrangedFlow.ts`
- Test: `dashboard/src/graph/arranged/buildArrangedFlow.test.ts`

- [x] **Step 1: Write the failing test**

```ts
import { describe, it, expect } from 'vitest'
import { buildArrangedFlow, arrangedEdgeTypes, arrangedNodeTypes } from './buildArrangedFlow'
import { fixtureGeometry } from './testFixtures'
import ArrangedNode from './ArrangedNode'
import RoutedEdge from './RoutedEdge'

const types = { a: 'script', b: 'llm_call', c: 'conditional' }

describe('buildArrangedFlow — nodes', () => {
  it('places every node at its payload box with the payload size and is not draggable', () => {
    const geo = fixtureGeometry()
    const { nodes } = buildArrangedFlow(geo, types, {}, 'test-graph')
    expect(nodes.map((n) => n.id)).toEqual(['a', 'b', 'c'])
    const b = nodes[1]
    expect(b.type).toBe('arrangedNode')
    expect(b.position).toEqual({ x: 40, y: 140 })
    expect(b.width).toBe(200)
    expect(b.height).toBe(70)
    expect(b.draggable).toBe(false)
    expect(b.selectable).toBe(true)
    expect(b.data.nodeBox).toBe(geo.nodes[1])
    expect(b.data.contract).toBe(geo.contract)
  })

  it('carries node type and execution state, defaulting the state to pending', () => {
    const { nodes } = buildArrangedFlow(fixtureGeometry(), types, { b: 'running' }, 'test-graph')
    expect(nodes[1].data.nodeType).toBe('llm_call')
    expect(nodes[1].data.executionState).toBe('running')
    expect(nodes[0].data.executionState).toBe('pending')
  })

  it('rejects absent manifest node types instead of inventing empty types', () => {
    expect(() => buildArrangedFlow(fixtureGeometry(), {}, {}, 'test-graph')).toThrow(/node type/)
  })
})

describe('buildArrangedFlow — edges', () => {
  it('keys each routed edge by manifest ordinal and carries its route', () => {
    const geo = fixtureGeometry()
    const { edges } = buildArrangedFlow(geo, types, {}, 'test-graph')
    expect(edges.map((e) => e.id)).toEqual(['routed-0', 'routed-1', 'routed-2'])
    expect(edges[2].type).toBe('routedEdge')
    expect(edges[2].source).toBe('c')
    expect(edges[2].target).toBe('b')
    expect(edges[2].data!.route).toBe(geo.edges[2])
    expect(edges[2].selectable).toBe(false)
  })

  it('gives each edge only the crossing points where it is the under edge', () => {
    const geo = fixtureGeometry()
    geo.crossings = [
      { point: { x: 140, y: 222 }, over: 2, under: 1 },
      { point: { x: 135, y: 116 }, over: 1, under: 0 },
      { point: { x: 140, y: 230 }, over: 0, under: 1 },
    ]
    const { edges } = buildArrangedFlow(geo, types, {}, 'test-graph')
    expect(edges[0].data!.gaps).toEqual([{ x: 135, y: 116 }])
    expect(edges[1].data!.gaps).toEqual([{ x: 140, y: 222 }, { x: 140, y: 230 }])
    expect(edges[2].data!.gaps).toEqual([])
  })

  it('marks edges active only when their source is in activeSources', () => {
    const { edges } = buildArrangedFlow(fixtureGeometry(), types, {}, 'test-graph', new Set(['b']))
    expect(edges.map((e) => e.data!.active)).toEqual([false, true, false])
  })

  it('treats a missing activeSources as no active edges', () => {
    const { edges } = buildArrangedFlow(fixtureGeometry(), types, {}, 'test-graph')
    expect(edges.every((e) => e.data!.active === false)).toBe(true)
  })
})

describe('arranged type maps', () => {
  it('registers the arrangedNode and routedEdge components', () => {
    expect(arrangedNodeTypes.arrangedNode).toBe(ArrangedNode)
    expect(arrangedEdgeTypes.routedEdge).toBe(RoutedEdge)
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

```bash
cd dashboard && npx vitest run src/graph/arranged/buildArrangedFlow.test.ts
```
Expected:
```text
Error: Failed to resolve import "./buildArrangedFlow" from "src/graph/arranged/buildArrangedFlow.test.ts". Does the file exist?
```

- [x] **Step 3: Implement**

```ts
import type { Edge, EdgeTypes, Node, NodeTypes } from '@xyflow/react'
import type { NodeExecutionState } from '../types'
import type { Geometry, Point } from './types'
import ArrangedNode, { type ArrangedNodeData } from './ArrangedNode'
import RoutedEdge, { type RoutedEdgeData } from './RoutedEdge'

export const arrangedNodeTypes: NodeTypes = { arrangedNode: ArrangedNode }
export const arrangedEdgeTypes: EdgeTypes = { routedEdge: RoutedEdge }

/**
 * Convert a validated geometry payload into xyflow nodes/edges. Every position,
 * size, route, label box and crossing gap comes from the payload; only the node
 * type (for the accent colour) and runtime state come from the caller.
 */
export function buildArrangedFlow(
  geo: Geometry,
  nodeTypesById: Record<string, string>,
  executionStates: Record<string, NodeExecutionState>,
  graphInstanceId: string,
  activeSources: Set<string> = new Set(),
): { nodes: Node<ArrangedNodeData>[]; edges: Edge<RoutedEdgeData>[] } {
  if (!graphInstanceId) throw new Error('graphInstanceId is required')
  for (const n of geo.nodes) {
    const type = nodeTypesById[n.id]
    if (!type || type.replace(/_/g, ' ') !== n.typeCaption) throw new Error(`node type mismatch for ${n.id}`)
  }
  const gapsByUnder = new Map<number, Point[]>()
  for (const c of geo.crossings) {
    const list = gapsByUnder.get(c.under) ?? []
    list.push(c.point)
    gapsByUnder.set(c.under, list)
  }

  const nodes: Node<ArrangedNodeData>[] = geo.nodes.map((nb) => ({
    id: nb.id,
    type: 'arrangedNode',
    position: { x: nb.box.x, y: nb.box.y },
    width: nb.box.w,
    height: nb.box.h,
    draggable: false,
    connectable: false,
    selectable: true,
    data: {
      nodeBox: nb,
      nodeType: nodeTypesById[nb.id] ?? '',
      executionState: executionStates[nb.id] ?? 'pending',
      contract: geo.contract,
    },
  }))

  const edges: Edge<RoutedEdgeData>[] = geo.edges.map((route) => ({
    id: `routed-${route.ordinal}`,
    source: route.source,
    target: route.target,
    type: 'routedEdge',
    selectable: false,
    focusable: false,
    data: {
      route,
      gaps: gapsByUnder.get(route.ordinal) ?? [],
      contract: geo.contract,
      active: activeSources.has(route.source),
      graphInstanceId,
    },
  }))

  return { nodes, edges }
}
```

- [x] **Step 4: Run it and confirm it passes**

```bash
cd dashboard && npx vitest run src/graph/arranged/buildArrangedFlow.test.ts && npx tsc --noEmit
```
Expected:
```text
      Tests  8 passed (8)
(tsc exits 0)
```

- [x] **Step 5: Commit**

```bash
git add dashboard/src/graph/arranged/buildArrangedFlow.ts dashboard/src/graph/arranged/buildArrangedFlow.test.ts && git commit -m "feat(dashboard): convert reviewed geometry into React Flow nodes and edges" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3.4: `ArrangedGraph` + `nodeCenter`

**Files:**
- Create: `dashboard/src/graph/arranged/ArrangedGraph.tsx`
- Create: `dashboard/src/graph/arranged/ArrangedGraph.module.css`
- Test: `dashboard/src/graph/arranged/ArrangedGraph.test.tsx` (React Flow mocked: props/behaviour)
- Test: `dashboard/src/graph/arranged/ArrangedGraph.dom.test.tsx` (real React Flow: DOM contract and type wiring)

Behaviour:
- **Mode selection:** arranged mode applies only when `resolveDisplayGeometry(workflowDef, view).geometry` is non-null. Otherwise fallback mode: `fallbackPositions(nodes, view?.retained?.positions)` → `useGraphLayout({ positions })`, which uses the legacy ArbitronNode/LoopBackEdge components.
- **Hooks:** all hooks run unconditionally. The fallback hook is fed stable empty arrays while arranged.
- **ReactFlow props:** `nodesDraggable={false}`, `nodesConnectable={false}`, `edgesFocusable={false}`, **no `fitView` prop**, `minZoom` 0.2, `maxZoom` 2.
- **Centring:** `setCenter(anchorCentre, { zoom: DEFAULT_ZOOM })` runs once per displayed layout. The key is `fallback` or `arranged:<savedRevision>:<resultGeneration>`, so the arranged result arriving after a fallback re-centres once, and unrelated re-renders never re-centre.
- **Fit all:** an explicit button labelled "Fit whole workflow" calls `fitView({ padding: 0.1 })`.
- **`fixedViewport`:** passes `defaultViewport` and disables pan and zoom. It renders no Background/Controls/Fit button and never centres.
- **Provider:** the component calls `useReactFlow()`, so it must be inside a `<ReactFlowProvider>`. It does not create one, so a parent's `useFollowActiveNode` (P3T13) shares the same instance.

- [x] **Step 1: Write the failing behaviour test** `dashboard/src/graph/arranged/ArrangedGraph.test.tsx`. Provenance probes: "moves the nodes when the view geometry moves", plus the four fallback cases (no geometry, null view, empty geometry, version mismatch).

Create the behavior suite with the real ArrangedGraph and an isolated leaf ReactFlow
props recorder. Its font port fake must validate the exact contract and complete actual
label/type/branch text, resolve only supported fixtures, and expose deferred/rejected font
loads. It is unit-only; real fonts/DOM remain covered by P3T5/P3T14.

Use async tests and await font readiness before arranged assertions (including viewport
call counts). Required cases, each observed RED before implementation:

- Valid same-revision payload draws exact positions/sizes/routes and state at the leaf;
  fixtureGeometryWithBoxes moves routes/attachments/canvas too, and changes sink positions.
- Missing/null/empty/incompatible/foreign geometry falls back with same-revision retained
  coordinates and no writes. Invalid font contract, missing manifest types, missing coverage
  asset, unsupported text and failed font load never draw reviewed nodes or claim reviewed.
- Deferred font load emits onDisplayIssues('Loading reviewed fonts') and loading placeholder (no premature fallback centring); resolve it and assert
  exact arranged DOM plus cleared issue. Reject it and assert fallback plus panel issue.
  Change revision/digest during load and resolve the older promise; it must not activate or
  clear a newer revision's error. Remove ensureBundledTextFonts invocation and require the
  missing-font negative to fail. Production routes consume the callback (P3T12/P3T13).
- Exact registered node/edge components, ordinal-specific gaps and running-source overlays
  reach the leaf. Selected IDs/click callbacks/children remain wired. Two graphs have
  independent marker namespaces and neither loses arrows when the first unmounts.
- nodesDraggable/nodesConnectable/edgesFocusable false, no fitView/drag handlers, readable
  DEFAULT_ZOOM once per displayed revision/layout; no repeated recenter on unrelated render.
  Fallback→ready arranged centres once; revision/legacy workflow navigation resets key.
- Explicit Fit whole workflow calls fitView; fixedViewport pins translation/zoom, disables
  interaction/chrome and avoids centring. nodeCenter uses actual payload size, then sized
  xyflow fallback, then legacy constants, and null for unknown IDs.

Keep the existing nodeCenter pure tests and all fallback/selection/navigation regressions.
Do not make the component's font readiness optional in production to preserve old synchronous
test assumptions. The DOM test below also awaits ready fonts before reading arranged nodes.

- [x] **Step 2: Write the failing DOM-contract test** `dashboard/src/graph/arranged/ArrangedGraph.dom.test.tsx`. It uses real React Flow and fails if the arranged node/edge types are not wired into ReactFlow or the DOM contract is not emitted.

```tsx
// Real @xyflow/react (no module mock): proves ArrangedGraph wires the arranged
// node/edge types into React Flow and that the DOM contract consumed by the
// render surface (P3T4) actually appears in the document. jsdom lacks layout,
// so ResizeObserver, DOMMatrixReadOnly, offsetWidth/offsetHeight and getBBox are stubbed
// just enough for React Flow to measure nodes and mount edges.
import { render, act, waitFor } from '@testing-library/react'
import { describe, it, expect, beforeAll, afterAll, vi } from 'vitest'
import { ReactFlowProvider } from '@xyflow/react'
import ArrangedGraph from './ArrangedGraph'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from './testFixtures'
import type { LayoutView, RendererContract } from './types'

// Unit font boundary only: validate text/contract and resolve fixture fonts; real font
// asset/coverage loading is exercised in P3T5/P3T14 fresh-browser tests.
vi.mock('./fonts', () => ({ ensureBundledTextFonts: async (_doc: Document, texts: readonly string[], contract: RendererContract) => {
  if (!texts.length || contract.version !== 'arranged-renderer/1') throw new Error('invalid font request')
  return { fontsReady: true, fontFamilyResolved: true }
} }))

const saved: { ro?: unknown; dm?: unknown; ow?: PropertyDescriptor; oh?: PropertyDescriptor; bbox?: unknown } = {}

beforeAll(() => {
  const g = globalThis as unknown as { ResizeObserver?: unknown; DOMMatrixReadOnly?: unknown }
  saved.ro = g.ResizeObserver
  saved.dm = g.DOMMatrixReadOnly
  saved.ow = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth')
  saved.oh = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight')
  saved.bbox = (SVGElement.prototype as unknown as { getBBox?: unknown }).getBBox

  class StubResizeObserver {
    private cb: ResizeObserverCallback
    constructor(cb: ResizeObserverCallback) {
      this.cb = cb
    }
    observe(el: Element) {
      setTimeout(() => this.cb([{ target: el } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver), 0)
    }
    unobserve() {}
    disconnect() {}
  }
  class StubDOMMatrix {
    m22: number
    constructor(transform?: string) {
      const m = /scale\(([\d.]+)\)/.exec(transform ?? '')
      this.m22 = m ? Number(m[1]) : 1
    }
  }
  g.ResizeObserver = StubResizeObserver
  // Legacy smoothstep edge labels (fallback mode) measure themselves with getBBox.
  ;(SVGElement.prototype as unknown as { getBBox: () => DOMRect }).getBBox = () =>
    ({ x: 0, y: 0, width: 10, height: 10 }) as DOMRect
  g.DOMMatrixReadOnly = StubDOMMatrix
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
    configurable: true,
    get() {
      return parseFloat((this as HTMLElement).style.width) || 10
    },
  })
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
    configurable: true,
    get() {
      return parseFloat((this as HTMLElement).style.height) || 10
    },
  })
})

afterAll(() => {
  const g = globalThis as unknown as { ResizeObserver?: unknown; DOMMatrixReadOnly?: unknown }
  g.ResizeObserver = saved.ro
  g.DOMMatrixReadOnly = saved.dm
  ;(SVGElement.prototype as unknown as { getBBox?: unknown }).getBBox = saved.bbox
  if (saved.ow) Object.defineProperty(HTMLElement.prototype, 'offsetWidth', saved.ow)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).offsetWidth
  if (saved.oh) Object.defineProperty(HTMLElement.prototype, 'offsetHeight', saved.oh)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).offsetHeight
})

async function mount(view: LayoutView | null) {
  const utils = render(
    <div style={{ width: 800, height: 600 }}>
      <ReactFlowProvider>
        <ArrangedGraph
          workflowDef={fixtureWorkflow()}
          view={view}
          selectedNodeId={null}
          onNodeClick={() => {}}
          anchorNodeId="a"
        />
      </ReactFlowProvider>
    </div>,
  )
  // Let the stubbed ResizeObserver callbacks run so React Flow measures nodes and mounts edges.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 20))
  })
  if (view?.geometry) await waitFor(() => expect(utils.container.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
  return utils
}

describe('ArrangedGraph DOM contract (real React Flow)', () => {
  it('wraps React Flow in data-arranged-canvas and renders arranged nodes at their payload boxes', async () => {
    const { container } = await mount(fixtureView())
    const canvas = container.querySelector('[data-arranged-canvas="true"]')!
    expect(canvas.querySelector('.react-flow')).not.toBeNull()
    const nodes = [...canvas.querySelectorAll('[data-arranged-node]')] as HTMLElement[]
    expect(nodes.map((n) => n.getAttribute('data-arranged-node'))).toEqual(['a', 'b', 'c'])
    const b = canvas.querySelector('[data-arranged-node="b"]') as HTMLElement
    expect(b.style.width).toBe('200px')
    expect(b.style.height).toBe('70px')
    const wrapper = b.closest('.react-flow__node') as HTMLElement
    expect(wrapper.style.transform).toBe('translate(40px,140px)')
    expect(wrapper.classList.contains('draggable')).toBe(false)
    expect(b.querySelector('[data-node-text="label"]')!.textContent).toBe('Draft a very long label that must wrap')
    expect(b.querySelector('[data-node-text="type"]')!.textContent).toBe('llm call')
  })

  it('renders one routed path per manifest edge with its own arrow marker', async () => {
    const { container } = await mount(fixtureView())
    const geo = fixtureGeometry()
    for (const route of geo.edges) {
      const path = container.querySelector(`path[data-edge-path="${route.ordinal}"]`) as SVGPathElement
      expect(path).not.toBeNull()
      expect(path.getAttribute('data-edge-kind')).toBe(route.kind)
      const ref = path.getAttribute('marker-end')!
      expect(ref).toMatch(new RegExp(`-arranged-arrow-${route.ordinal}\\)$`))
      const markerId = ref.slice(5, -1)
      const group = path.closest('g')!
      expect(group.querySelector(`[id="${markerId}"]`)).not.toBeNull()
    }
    expect(container.querySelector('path[data-edge-path="1"]')!.getAttribute('d')).toBe(
      'M 140,210 L 140,235 L 130,235 L 130,260',
    )
  })

  it('renders edge labels at their payload boxes', async () => {
    await mount(fixtureView())
    const yes = document.querySelector('[data-edge-label="1"]') as HTMLElement
    const again = document.querySelector('[data-edge-label="2"]') as HTMLElement
    expect(yes.textContent).toBe('yes')
    expect(yes.style.transform).toBe('translate(146px, 222px)')
    expect(again.textContent).toBe('again')
  })

  it('renders the legacy fallback (no arranged DOM) when there is no geometry', async () => {
    const { container } = await mount(null)
    expect(container.querySelector('[data-arranged-node]')).toBeNull()
    expect(container.querySelector('[data-edge-path]')).toBeNull()
    expect(container.querySelectorAll('.react-flow__node-arbitronNode')).toHaveLength(3)
    expect(container.querySelector('[data-arranged-canvas="true"]')!.getAttribute('data-layout-mode')).toBe('fallback')
  })
})
```

- [x] **Step 3: Run both and confirm they fail**

```bash
cd dashboard && npx vitest run src/graph/arranged/ArrangedGraph.test.tsx src/graph/arranged/ArrangedGraph.dom.test.tsx
```
Expected: after import/declaration setup, observe the intended data-feed, font-failure, mode and viewport behavioral failures. Import failures alone are insufficient RED evidence.

- [x] **Step 4: Implement the stylesheet** `dashboard/src/graph/arranged/ArrangedGraph.module.css`:

```css
.canvas {
  position: relative;
  width: 100%;
  height: 100%;
}

.fitButton {
  position: absolute;
  top: 12px;
  right: 12px;
  z-index: 5;
  padding: 6px 12px;
  font-size: 12px;
  color: #ccccdd;
  background: #1a1a2e;
  border: 1px solid #444466;
  border-radius: 4px;
  cursor: pointer;
}

.fitButton:hover {
  border-color: #6666aa;
}
```

- [x] **Step 5: Implement the component** `dashboard/src/graph/arranged/ArrangedGraph.tsx`:

```tsx
import { useEffect, useMemo, useRef, useId, useState } from 'react'
import type { ReactNode } from 'react'
import { ReactFlow, Background, Controls, useReactFlow } from '@xyflow/react'
import type { Edge, Node } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { ensureBundledTextFonts } from './fonts'
import fontCoverage from './fontCoverage.json'
import type { NodeExecutionState, WorkflowDefinition, WorkflowEdge, WorkflowNode } from '../types'
import type { LayoutView } from './types'
import { resolveDisplayGeometry } from './validate'
import { buildArrangedFlow, arrangedEdgeTypes, arrangedNodeTypes } from './buildArrangedFlow'
import { fallbackPositions } from './fallbackLayout'
import { useGraphLayout, NODE_WIDTH, NODE_HEIGHT } from '../useGraphLayout'
import { nodeTypes as legacyNodeTypes } from '../nodes/nodeTypes'
import { edgeTypes as legacyEdgeTypes } from '../edges/edgeTypes'
import { DEFAULT_ZOOM } from '../constants'
import styles from './ArrangedGraph.module.css'

export interface ArrangedGraphProps {
  workflowDef: WorkflowDefinition
  /** Geometry is drawn only when view.geometry validates; otherwise retained positions → dagre fallback. */
  view: LayoutView | null
  executionStates?: Record<string, NodeExecutionState>
  selectedNodeId: string | null
  /** Render-surface state probe: marks many nodes selected. */
  selectedNodeIds?: ReadonlySet<string>
  onNodeClick: (nodeId: string) => void
  /** Initial centre at DEFAULT_ZOOM (0.9); ignored when fixedViewport is set. */
  anchorNodeId: string | null
  onMoveStart?: (event: unknown) => void
  /** Reports the anchor node's centre (flow coordinates, payload dimensions) whenever it changes. */
  onDisplayIssues?: (issues: readonly string[]) => void
  onAnchorPosition?: (center: { x: number; y: number } | null) => void
  /** Render surface: no pan/zoom, no Controls/Background/Fit button, no centring. */
  fixedViewport?: { x: number; y: number; zoom: number }
  /** Rendered inside <ReactFlow> (e.g. the recenter button). */
  children?: ReactNode
}

// Stable empties so the memoised fallback layout is not recomputed every render.
const EMPTY_STATES: Record<string, NodeExecutionState> = {}
const NO_NODES: WorkflowNode[] = []
const NO_EDGES: WorkflowEdge[] = []

/**
 * Centre of a node in flow coordinates. Uses the payload box when `view`
 * carries the geometry being displayed (callers pass null otherwise), else the
 * rendered xyflow node's position and size (NODE_WIDTH/NODE_HEIGHT when the
 * node has no explicit size, i.e. legacy fallback nodes).
 */
export function nodeCenter(
  view: LayoutView | null,
  rfNodes: Node[],
  nodeId: string,
): { x: number; y: number } | null {
  const box = view?.geometry?.nodes?.find((n) => n.id === nodeId)?.box
  if (box) return { x: box.x + box.w / 2, y: box.y + box.h / 2 }
  const n = rfNodes.find((r) => r.id === nodeId)
  if (!n) return null
  return {
    x: n.position.x + (n.width ?? NODE_WIDTH) / 2,
    y: n.position.y + (n.height ?? NODE_HEIGHT) / 2,
  }
}

/**
 * The one renderer shared by WorkflowReview, ExecutionView and the render
 * surface. Must be rendered inside a <ReactFlowProvider>.
 */
export default function ArrangedGraph({
  workflowDef,
  view,
  executionStates = EMPTY_STATES,
  selectedNodeId,
  selectedNodeIds,
  onNodeClick,
  anchorNodeId,
  onMoveStart,
  onAnchorPosition,
  onDisplayIssues,
  fixedViewport,
  children,
}: ArrangedGraphProps) {
  const reactFlow = useReactFlow()
  const graphInstanceId = `graph-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`

  const { geometry } = useMemo(() => resolveDisplayGeometry(workflowDef, view), [workflowDef, view])
  const fontKey = JSON.stringify([view?.savedRevision, geometry, fontCoverage.fingerprint])
  const [fontState, setFontState] = useState<{key: string; ready: boolean; issues: readonly string[]} | null>(null)
  useEffect(() => {
    let cancelled = false
    if (!geometry) return
    const texts = [...geometry.nodes.flatMap(n => [n.text, n.typeCaption]), ...geometry.edges.flatMap(e => e.text ? [e.text.text] : [])]
    ensureBundledTextFonts(document, texts, geometry.contract).then(result => {
      if (!cancelled) setFontState({ key: fontKey, ready: result.fontsReady && result.fontFamilyResolved, issues: result.fontsReady && result.fontFamilyResolved ? [] : ['Reviewed fonts could not be loaded'] })
    }).catch(error => {
      if (!cancelled) setFontState({ key: fontKey, ready: false, issues: [String(error)] })
    })
    return () => { cancelled = true }
  }, [fontKey])
  const fontsReady = fontState?.key === fontKey && fontState.ready
  const fontIssues = geometry && !fontsReady ? (fontState?.key === fontKey ? fontState.issues : ['Loading reviewed fonts']) : []
  const fontIssueKey = JSON.stringify(fontIssues)
  useEffect(() => { onDisplayIssues?.(fontIssues) }, [fontIssueKey, onDisplayIssues])
  const awaitingFonts = geometry != null && fontState?.key !== fontKey
  const arranged = geometry != null && fontsReady

  const nodeTypesById = useMemo(
    () => Object.fromEntries(workflowDef.nodes.map((n) => [n.id, n.type])),
    [workflowDef.nodes],
  )
  const activeSources = useMemo(
    () => new Set(Object.keys(executionStates).filter((id) => executionStates[id] === 'running')),
    [executionStates],
  )
  const arrangedFlow = useMemo(
    () => (geometry && arranged ? buildArrangedFlow(geometry, nodeTypesById, executionStates, graphInstanceId, activeSources) : null),
    [geometry, arranged, nodeTypesById, executionStates, graphInstanceId, activeSources],
  )

  // Fallback (hooks run unconditionally; fed empty input while arranged).
  const sameRevision = view?.savedRevision === (workflowDef.presentation?.revision ?? '')
  const retainedPositions = sameRevision ? view?.retained?.positions : undefined
  const positions = useMemo(
    () => (arranged ? null : fallbackPositions(workflowDef.nodes, retainedPositions)),
    [arranged, workflowDef.nodes, retainedPositions],
  )
  const legacy = useGraphLayout({
    nodes: arranged ? NO_NODES : workflowDef.nodes,
    edges: arranged ? NO_EDGES : workflowDef.edges,
    executionStates,
    positions: positions ?? undefined,
  })

  const baseNodes: Node[] = arrangedFlow ? arrangedFlow.nodes : legacy.rfNodes
  const edges: Edge[] = arrangedFlow ? arrangedFlow.edges : legacy.rfEdges

  const nodes = useMemo(
    () =>
      baseNodes.map((n) => {
        const selected = n.id === selectedNodeId || (selectedNodeIds?.has(n.id) ?? false)
        return n.selected === selected ? n : { ...n, selected }
      }),
    [baseNodes, selectedNodeId, selectedNodeIds],
  )

  const anchor = useMemo(
    () => (!awaitingFonts && anchorNodeId ? nodeCenter(arranged ? view : null, nodes, anchorNodeId) : null),
    [anchorNodeId, arranged, awaitingFonts, view, nodes],
  )
  const anchorX = anchor?.x
  const anchorY = anchor?.y

  useEffect(() => {
    onAnchorPosition?.(anchorX != null && anchorY != null ? { x: anchorX, y: anchorY } : null)
  }, [anchorX, anchorY, onAnchorPosition])

  // Centre once per displayed layout (fallback, or each arranged result), at
  // the readable default zoom. Unrelated re-renders never re-centre.
  const layoutKey = arranged && view ? `arranged:${view.savedRevision}:${view.resultGeneration}` : `fallback:${workflowDef.presentation?.revision ?? ''}:${workflowDef.name}`
  const centredFor = useRef<string | null>(null)
  useEffect(() => {
    if (fixedViewport) return
    if (anchorX == null || anchorY == null) return
    if (centredFor.current === layoutKey) return
    centredFor.current = layoutKey
    reactFlow.setCenter(anchorX, anchorY, { zoom: DEFAULT_ZOOM })
  }, [anchorX, anchorY, layoutKey, fixedViewport, reactFlow])

  const fixed = fixedViewport != null

  if (awaitingFonts) return <div data-arranged-canvas="true" data-layout-mode="loading" className={styles.canvas}>Loading reviewed fonts…</div>
  return (
    <div
      data-arranged-canvas="true"
      data-layout-mode={arranged ? 'arranged' : 'fallback'}
      className={styles.canvas}
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={arranged ? arrangedNodeTypes : legacyNodeTypes}
        edgeTypes={arranged ? arrangedEdgeTypes : legacyEdgeTypes}
        onNodeClick={(_event, node) => onNodeClick(node.id)}
        onMoveStart={onMoveStart ? (event) => onMoveStart(event) : undefined}
        nodesDraggable={false}
        nodesConnectable={false}
        edgesFocusable={false}
        minZoom={0.2}
        maxZoom={2}
        defaultViewport={fixedViewport}
        panOnDrag={!fixed}
        zoomOnScroll={!fixed}
        zoomOnPinch={!fixed}
        zoomOnDoubleClick={!fixed}
        proOptions={{ hideAttribution: true }}
      >
        {!fixed && <Background color="#2a2a4a" gap={20} size={1} />}
        {!fixed && <Controls showInteractive={false} />}
        {!fixed && (
          <button
            type="button"
            className={styles.fitButton}
            aria-label="Fit whole workflow"
            title="Fit whole workflow"
            onClick={() => reactFlow.fitView({ padding: 0.1 })}
          >
            Fit all
          </button>
        )}
        {children}
      </ReactFlow>
    </div>
  )
}
```

- [x] **Step 6: Run them, the full dashboard suite, and the type check**

> VERIFIED on production revisit (2026-10-17): The previously missing dependencies now exist. Actual initDomain authoring → worker → BrowserRenderer/RenderSurface/ArrangedGraph → ReadabilityReviewer → SQLite → authorised HTTP → useLayoutStatus → WorkflowReview/ExecutionView reaches complete geometry/text/path/marker/label DOM sinks. Real script execution feeds runtime state; removal of one used bundled font subset reaches retained fallback and the actual status panel. Independently removing the graph input from either user route fails its arranged-DOM assertion; removing geometry only from RenderSurface's ArrangedGraph input fails the persisted passed-state assertion with an explicit DOM-coverage rendering failure. Restoring all three inputs and rebuilding embedded assets passes the root probes. The arranged suite (298 tests), full dashboard (1,027 tests), TypeScript and required Chrome component probe pass. No missing production input remains for this task; separate release/protocol gates in other task files are not waived.

```bash
cd dashboard && npx vitest run src/graph/arranged && npx vitest run && npx tsc --noEmit
```
Expected:
```text
 Test Files  8 passed (8)        (src/graph/arranged; ArrangedGraph.test.tsx 23 passed, ArrangedGraph.dom.test.tsx 4 passed)
 Test Files  … passed           (full suite, 0 failed)
(tsc exits 0)
```

- [x] **Step 7: Commit**

```bash
git add dashboard/src/graph/arranged/ArrangedGraph.tsx dashboard/src/graph/arranged/ArrangedGraph.module.css dashboard/src/graph/arranged/ArrangedGraph.test.tsx dashboard/src/graph/arranged/ArrangedGraph.dom.test.tsx && git commit -m "feat(dashboard): add shared ArrangedGraph renderer with retained fallback" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Wiring (composition roots that mount `ArrangedGraph`):**
  - `dashboard/src/routes/WorkflowReview.tsx` `ReviewGraph` (P3T12; `WorkflowReview.test.tsx` drives it through the route).
  - `dashboard/src/routes/ExecutionView.tsx` `GraphView` (P3T13; `ExecutionView.layout.test.tsx` drives it through the default export).
  - `dashboard/src/render/RenderSurface.tsx` (P3T4, with `fixedViewport` + `selectedNodeIds`). The Go `renderreview.BrowserRenderer` (P3T5) loads it to produce review evidence.
  - `useGraphLayout`'s new `positions` input is fed only by `ArrangedGraph`, through `fallbackPositions`.
- **Data source:**
  - `view` is `useLayoutStatus(...).view` (P3T11) ← `api.workflows.getLayout` / `api.instances.getLayout` (P3T1) ← HTTP `LayoutHandler` (P3T9) ← `layout.QueryService` (P3T8) ← `layout.Reader.View` (P2T5) over the `layout.Store` row and result written by `layout.Worker` (P2T8), with geometry from `layout.Arrange`/`Coordinator.Run` (P1T6/P1T9).
  - `view.retained.positions` is captured at save time by `workflow.RevisionStamper.Stamp` (P2T2) and stored with the request row (P2T5/P2T6).
  - `workflowDef` is `api.workflows.get` (WorkflowReview) or the instance `workflowSnapshot` (ExecutionView).
  - `executionStates` comes from `ExecutionView.tsx:buildExecutionStates`.
- **Provenance probes:**
  - `ArrangedGraph.test.tsx` "moves the nodes when the view geometry moves (fails if ArrangedGraph ignored view)", plus the empty/invalid-geometry fallback cases.
  - `ArrangedGraph.dom.test.tsx` asserts real `[data-arranged-node]`/`path[data-edge-path]`/`marker#<graphInstanceId>-arranged-arrow-N`/`[data-edge-label]` in the document.

**Shared CSS producer:** ArrangedGraph imports @xyflow/react/dist/style.css itself.
RenderSurface must not depend on a route having imported that stylesheet earlier; P3T5
fresh-tab surface test fails when shared CSS is absent.

**Occurrence and fallback checks:** BuildArrangedFlow tests assert each route/ordinal and
namespace reaches RoutedEdgeData, including identical parallel occurrences. A foreign view
with only retained positions cannot move this manifest's fallback. Include two different
legacy workflow names with no token so centring resets on navigation. Retained/legacy
fallback may remain imperfect/unreviewed; it never produces evidence or a reviewed badge.

## Execution evidence

- Selected P3T3 after inspecting the ordered task inventory: preceding unchecked tasks retain documented contract/producer/acceptance blockers. This pass does not retry or waive those gates.
- 3.1 RED: missing module, then 5 behavioral failures with a compiling null-return stub. GREEN: retained → manifest precedence, finite coordinates, manifest-order stacking, input immutability and dagre selection. A later code-quality RED exposed legal `__proto__` node IDs disappearing from the position map; a prototype-free output fixes it. Seven focused tests pass.
- 3.2 RED: complete supplied coordinates were discarded and `needsAutoSave` remained present (2 failing tests). GREEN: 24 hook tests; route suites and TypeScript pass. The actual WorkflowReview route loads an unpositioned manifest, draws/selects nodes through the real hook and performs no automatic layout write. Manual drag code remains deliberately owned by P3T12.
- 3.3 RED: missing module, then 4 behavioral failures with compiling empty-output/type-map stubs. GREEN: exact payload boxes/routes, manifest types, states, ordinal-only crossing gaps, namespaces, source activity and parallel occurrences. Used ES2020-compatible underscore replacement instead of the example's unsupported String.replaceAll.
- 3.4 RED: missing component, then 25 behavioral failures against a TypeScript-compiling empty component/null-centre stub. GREEN: validated geometry, same-revision fallback, deferred/rejected/unsupported fonts with complete text+contract validation, stale success/error fencing, issues, selection/click/children/follow callbacks, readable once-per-layout centring, explicit fit and pinned chrome-free viewports. The real React Flow DOM suite asserts wrappers, all node text/boxes, persisted paths, labels, active overlay, actual interactive viewport transform, and unique two-graph markers surviving first-graph unmount. Structural fixture boxes are not claimed to fit real measured text.
- Self-review, in order: (1) checked exact task/design/contracts ownership; added no flag, alternate schema, automatic save, fake production input or early P3T12/P3T13 integration. Added the missing route-level no-auto-write sink and fixed-canvas/unknown-anchor/stale-error cases. (2) Removing the real font invocation caused 8 expected failures; a constant graph namespace failed the two-graph DOM sink; bypassing revision fencing and omitting centring caused 6 failures including the actual viewport sink; replacing payload positions/routes with zero/empty data caused 5 failures; dropping font cancellation and restoring automatic route writes caused 4 failures. All mutations were restored. (3) checked neighboring React/TypeScript patterns, retained unconditional hooks/stable empty inputs, formatted production JSX, and fixed prototype-key position loss via RED → GREEN.
- Final verification: focused arranged/hook/route suite 283 tests pass; full dashboard 60 files / 803 tests pass; `npx tsc --noEmit`, real Chrome `npm run test:arranged-browser` (1 pass, no skip), `make ui`, `go test ./adapter/dashboard` and `git diff --check` pass. Vite reports its existing large-chunk warning. Embedded assets reflect the autosave deletion; the future shared renderer is not falsely mounted with nil status.
- Commit steps are included together using the mission's single-task commit alternative. At initial implementation production completion remained blocked under 3.4 Step 6; the production revisit below closes that gate.

## Production revisit evidence (2026-10-17)

- Selected only P3T3 after skipping preceding recorded protocol, policy and portability blockers. Its sole remaining step was verification; no new component behavior or unrelated feature was required. P3T4/P3T8/P3T9/P3T11/P3T12/P3T13 now provide the previously missing producers and mounts.
- Strict characterization RED: temporarily replaced WorkflowReview's `view={joinedView}` with null, rebuilt the actual embedded SPA and ran `TestLayoutSurfaces_ComponentRuntimeStylesPreserveReviewedGeometry`. Its workflow arranged-DOM assertion failed (`/tmp/p3t3-root-red.log`). Restored it, then independently nullified ExecutionView's layout input while retaining a compiling reference: workflow geometry still matched, but the instance arranged-DOM assertion failed (`/tmp/p3t3-execution-red.log`). Restored it, then removed only geometry from RenderSurface's ArrangedGraph input: the real worker persisted failed rather than passed with `render surface: exact DOM coverage did not become ready` (`/tmp/p3t3-render-red.log`). These are behavioral failures after successful builds, not missing-symbol evidence. Every mutation is restored; final git diff contains no production or generated-asset change.
- Ordered self-review: **(1) Spec compliance:** checked the exact shared-renderer/fallback/viewport/font interfaces and all three assigned composition roots. This pass closes only the deferred verification step; no retry policy, layout algorithm, timeout, option or sibling-task checkbox changed. **(2) Anti-green-theater:** the three independently removed inputs fail actual DOM or SQLite sinks. Restored root tests use saved manifests, real browser measurement/render/review, authorised router/client, bundled assets, runtime script execution and complete persisted geometry comparisons, not synthetic status or graph components. **(3) Code quality:** inspected ArrangedGraph and neighboring route/render/surface tests; existing idiomatic code required no changes. Preserved unrelated untracked `docs/visuals/` and restored all temporary assets with `make ui`.
- Final GREEN after restoration/review: `GOMAXPROCS=2 ARBITRON_REQUIRE_BROWSER=1 go test -p 1 ./cmd/arbitron -run '^TestLayoutSurfaces_(ComponentRuntimeStylesPreserveReviewedGeometry|MissingBundledSubsetRetainsFallback|PRReviewWorkflowAndInstanceUseReviewedGeometry)$' -count=1 -timeout=4m -v` passed all three tests (30.573s). They verify full reviewed geometry through both user routes, the actual shared render page and font-error callback/panel fallback.
- Final prescribed gates: `GOMAXPROCS=2 npx vitest run src/graph/arranged --maxWorkers=1` **10 files / 298 tests passed** (21.79s); `GOMAXPROCS=2 npx vitest run --maxWorkers=1` **70 files / 1,027 tests passed** (150.55s); `npx tsc --noEmit` passed. Serial worker scheduling changes no assertion or timeout. An initial two-worker run exceeded its outer 240s bound under host contention; the full serial run passed twice without weakening tests.
- Additional GREEN: `ARBITRON_REQUIRE_BROWSER=1 npm run test:arranged-browser` **1 passed, 0 skipped** (12.21s); `make ui`; `GOMAXPROCS=2 go test -p 1 ./adapter/dashboard`; `git diff --check`. Existing Node localstorage/Vite chunk-size warnings remain nonfatal. All P3T3 steps are now checked; earlier task blockers and complex-sample release gates retain their own unchecked status.
