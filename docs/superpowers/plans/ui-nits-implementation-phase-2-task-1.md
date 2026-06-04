# UI Nits Phase 2: Default Zoom + Follow Executing Node — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open the workflow graph at a readable default zoom centered on the most relevant step (active/final/failed/entrypoint) instead of fitting the whole graph, and lay the pure-helper foundation for follow-the-current-step behavior.

**Architecture:** A pure, unit-testable helper `selectAnchorNodeId(instance, workflowDef, executionStates, trace)` resolves which real step node the viewport should center on for any instance/preview state. `GraphView` in `ExecutionView.tsx` drops React Flow's `fitView` and instead centers on that anchor's node center (derived from `positionMap` + `NODE_WIDTH`/`NODE_HEIGHT`) at `DEFAULT_ZOOM = 0.9` on mount, preserving Phase 1's `edgeTypes` and `loopContainer` overlay nodes. Loop-container overlay nodes never appear in `workflowDef.nodes`, so they are naturally excluded from anchor candidates.

**Tech Stack:** React 19, @xyflow/react v12 (useReactFlow/setCenter), TypeScript, Vitest 4 (`npm test` → `vitest run`) + @testing-library/react.

---

## Conventions for this plan

- Test runner: from `dashboard/`, run `npm test` (this is `vitest run`, single pass). To scope to one file, run `npm test -- src/graph/selectAnchorNodeId.test.ts`.
- All paths below are relative to `dashboard/` unless absolute.
- STRICT TDD: for every behavioral unit write a failing test first, run it and confirm the expected failure, write the minimal implementation, run again and confirm pass, then commit.
- Phase 1 dependency: this plan assumes Phase 1 (loop rendering) has either landed or will be rebased under it. The only Phase 1 surface this task touches is the `<ReactFlow>` element in `GraphView`; preserve any `edgeTypes` prop and `loopContainer` nodes Phase 1 added. If Phase 1 has not yet landed when you implement, the `<ReactFlow>` element will not have `edgeTypes` yet — that is fine; just do not remove it if present.

---

### Task 1: Pure `selectAnchorNodeId` helper

Create a pure function that, given the instance, workflow definition, per-node execution states, and the trace, returns the id of the real step node the viewport should center on — or `null` when there is no sensible anchor.

**Files:**

- Create: `src/graph/selectAnchorNodeId.ts`
- Create: `src/graph/selectAnchorNodeId.test.ts`

**Signature (final shape):**

```ts
export function selectAnchorNodeId(
  instance: Instance | null,
  workflowDef: WorkflowDefinition | null,
  executionStates: Record<string, NodeExecutionState>,
  trace: NodeExecution[],
): string | null
```

**Anchor rules (from design "Anchor selection"):**

- Running/waiting instance → `instance.currentNodeId` (the node `executionStates` marks `running`/`waiting`), if that id exists in the graph.
- Failed instance → `instance.failedNodeId` if present and in the graph; else the node marked `failed` in `executionStates`.
- Completed instance → the terminal node: the last `completed` node in trace order (latest `completedAt`/trace position), falling back to a graph sink node (a node with no outgoing forward edge).
- No instance (static preview) → the entrypoint node (`node.entrypoint === true`), falling back to the graph source node (a node with no incoming edge), falling back to the first node.
- Empty graph / no resolvable node → `null`.

#### Step 1.1 — Static preview: entrypoint node (failing test)

- [ ] Add the test file with the first scenario:

```ts
// src/graph/selectAnchorNodeId.test.ts
import { describe, it, expect } from 'vitest'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from './types'
import { selectAnchorNodeId } from './selectAnchorNodeId'

// --- fixtures -------------------------------------------------------------

function def(
  nodes: WorkflowDefinition['nodes'],
  edges: WorkflowDefinition['edges'] = [],
): WorkflowDefinition {
  return { name: 'wf', description: '', author: '', nodes, edges }
}

const N = (
  id: string,
  extra: Partial<WorkflowDefinition['nodes'][number]> = {},
): WorkflowDefinition['nodes'][number] => ({
  id,
  type: 'llm_call',
  label: id.toUpperCase(),
  description: '',
  config: {},
  ...extra,
})

const inst = (extra: Partial<Instance>): Instance =>
  ({
    id: 'i1',
    ownerId: 'u1',
    workflowName: 'wf',
    priority: 'normal',
    variables: {},
    originalVariables: {},
    createdAt: '',
    updatedAt: '',
    interactiveActive: false,
    interactiveNodeId: null,
    ...extra,
  }) as Instance

const exec = (
  nodeId: string,
  status: NodeExecution['status'],
  completedAt?: string,
): NodeExecution =>
  ({
    id: `e-${nodeId}`,
    instanceId: 'i1',
    nodeId,
    attempt: 1,
    status,
    startedAt: '2026-01-01T00:00:00Z',
    completedAt,
  }) as NodeExecution

const noStates: Record<string, NodeExecutionState> = {}

// --- static preview -------------------------------------------------------

describe('selectAnchorNodeId — static preview (no instance)', () => {
  it('returns the entrypoint node when one is flagged', () => {
    const d = def(
      [N('a'), N('b', { entrypoint: true }), N('c')],
      [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'c' },
      ],
    )
    expect(selectAnchorNodeId(null, d, noStates, [])).toBe('b')
  })
})
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected FAIL:** module not found / `selectAnchorNodeId is not a function` (file does not exist yet).

#### Step 1.2 — Minimal impl for entrypoint case

- [ ] Create `src/graph/selectAnchorNodeId.ts`:

```ts
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from './types'

/**
 * Pick the id of the real step node the viewport should center on.
 *
 * Returns null when there is no sensible anchor (empty graph). Loop-container
 * overlay nodes are not candidates because they never appear in
 * `workflowDef.nodes` — they are synthesized during layout.
 */
export function selectAnchorNodeId(
  instance: Instance | null,
  workflowDef: WorkflowDefinition | null,
  _executionStates: Record<string, NodeExecutionState>,
  _trace: NodeExecution[],
): string | null {
  if (!workflowDef || workflowDef.nodes.length === 0) return null

  // Static preview: no instance.
  if (!instance) {
    const entry = workflowDef.nodes.find((n) => n.entrypoint === true)
    if (entry) return entry.id
  }

  return null
}
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected PASS:** 1 passing.
- [ ] Commit: `test+feat(ui-nits): selectAnchorNodeId entrypoint anchor for static preview`

#### Step 1.3 — Static preview fallbacks: source node, then first node (failing test)

- [ ] Append to the `static preview` describe block:

```ts
  it('falls back to the source node (no incoming edge) when no entrypoint flag', () => {
    const d = def(
      [N('a'), N('b'), N('c')],
      [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'c' },
      ],
    )
    // 'a' has no incoming edge => it is the source.
    expect(selectAnchorNodeId(null, d, noStates, [])).toBe('a')
  })

  it('falls back to the first node when there is no clear source', () => {
    // A 2-cycle: every node has an incoming edge, so there is no source.
    const d = def(
      [N('x'), N('y')],
      [
        { source: 'x', target: 'y' },
        { source: 'y', target: 'x' },
      ],
    )
    expect(selectAnchorNodeId(null, d, noStates, [])).toBe('x')
  })
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected FAIL:** both new cases return `null`.

#### Step 1.4 — Minimal impl for source / first-node fallbacks

- [ ] Add a source-node helper and wire the fallback chain. Replace the static-preview block and add the helper:

```ts
function sourceNodeId(def: WorkflowDefinition): string | null {
  const hasIncoming = new Set(def.edges.map((e) => e.target))
  const source = def.nodes.find((n) => !hasIncoming.has(n.id))
  return source ? source.id : null
}
```

```ts
  // Static preview: no instance.
  if (!instance) {
    const entry = workflowDef.nodes.find((n) => n.entrypoint === true)
    if (entry) return entry.id
    return sourceNodeId(workflowDef) ?? workflowDef.nodes[0].id
  }
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected PASS:** 3 passing.
- [ ] Commit: `feat(ui-nits): selectAnchorNodeId static-preview fallbacks (source, first node)`

#### Step 1.5 — Running instance: active node (failing test)

- [ ] Add a new describe block:

```ts
describe('selectAnchorNodeId — running instance', () => {
  it('returns currentNodeId when the instance is running', () => {
    const d = def(
      [N('a'), N('b'), N('c')],
      [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'c' },
      ],
    )
    const states: Record<string, NodeExecutionState> = {
      a: 'completed',
      b: 'running',
      c: 'pending',
    }
    const i = inst({ state: 'running', currentNodeId: 'b' })
    expect(selectAnchorNodeId(i, d, states, [exec('a', 'succeeded')])).toBe('b')
  })

  it('returns currentNodeId when the instance is waiting (interactive)', () => {
    const d = def([N('a'), N('b')], [{ source: 'a', target: 'b' }])
    const states: Record<string, NodeExecutionState> = { a: 'completed', b: 'waiting' }
    const i = inst({ state: 'waiting', currentNodeId: 'b' })
    expect(selectAnchorNodeId(i, d, states, [exec('a', 'succeeded')])).toBe('b')
  })

  it('ignores a currentNodeId that is not in the graph (fallback continues)', () => {
    const d = def([N('a'), N('b')], [{ source: 'a', target: 'b' }])
    const i = inst({ state: 'running', currentNodeId: 'ghost' })
    // No completed nodes, not failed: falls through to source node 'a'.
    expect(selectAnchorNodeId(i, d, {}, [])).toBe('a')
  })
})
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected FAIL:** running cases return `null` (no running branch yet).

#### Step 1.6 — Minimal impl for running/waiting + general fallback

- [ ] Add a node-id set and the running branch, plus the shared final fallback (source → first node). Insert after the static-preview block and add a helper at top:

```ts
function nodeIdSet(def: WorkflowDefinition): Set<string> {
  return new Set(def.nodes.map((n) => n.id))
}
```

```ts
  const ids = nodeIdSet(workflowDef)

  // Running / waiting: the active node.
  if (instance.state === 'running' || instance.state === 'waiting') {
    if (instance.currentNodeId && ids.has(instance.currentNodeId)) {
      return instance.currentNodeId
    }
  }

  // General fallback for any instance with no specific anchor.
  return sourceNodeId(workflowDef) ?? workflowDef.nodes[0].id
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected PASS:** 6 passing.
- [ ] Commit: `feat(ui-nits): selectAnchorNodeId running/waiting active-node anchor`

#### Step 1.7 — Failed instance: failed node (failing test)

- [ ] Add describe block:

```ts
describe('selectAnchorNodeId — failed instance', () => {
  it('returns failedNodeId when present and in the graph', () => {
    const d = def([N('a'), N('b'), N('c')], [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ])
    const states: Record<string, NodeExecutionState> = {
      a: 'completed',
      b: 'failed',
      c: 'pending',
    }
    const i = inst({ state: 'failed', failedNodeId: 'b' })
    expect(
      selectAnchorNodeId(i, d, states, [exec('a', 'succeeded'), exec('b', 'failed')]),
    ).toBe('b')
  })

  it('falls back to the node marked failed in executionStates when failedNodeId is missing', () => {
    const d = def([N('a'), N('b')], [{ source: 'a', target: 'b' }])
    const states: Record<string, NodeExecutionState> = { a: 'completed', b: 'failed' }
    const i = inst({ state: 'failed' }) // no failedNodeId
    expect(selectAnchorNodeId(i, d, states, [])).toBe('b')
  })
})
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected FAIL:** failed cases return source-node fallback `'a'` instead of `'b'`.

#### Step 1.8 — Minimal impl for failed branch

- [ ] Insert the failed branch after the running branch (before the general fallback):

```ts
  // Failed: the failed node.
  if (instance.state === 'failed') {
    if (instance.failedNodeId && ids.has(instance.failedNodeId)) {
      return instance.failedNodeId
    }
    const markedFailed = workflowDef.nodes.find(
      (n) => _executionStates[n.id] === 'failed',
    )
    if (markedFailed) return markedFailed.id
  }
```

- [ ] Rename the unused `_executionStates` parameter to `executionStates` (it is now used). Update the signature line and the `markedFailed` lookup accordingly.
- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected PASS:** 8 passing.
- [ ] Commit: `feat(ui-nits): selectAnchorNodeId failed-node anchor`

#### Step 1.9 — Completed instance: terminal node (failing test)

- [ ] Add describe block:

```ts
describe('selectAnchorNodeId — completed instance', () => {
  it('returns the last completed node by trace order', () => {
    const d = def([N('a'), N('b'), N('c')], [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ])
    const states: Record<string, NodeExecutionState> = {
      a: 'completed',
      b: 'completed',
      c: 'completed',
    }
    const i = inst({ state: 'completed' })
    const t = [
      exec('a', 'succeeded', '2026-01-01T00:00:01Z'),
      exec('b', 'succeeded', '2026-01-01T00:00:02Z'),
      exec('c', 'succeeded', '2026-01-01T00:00:03Z'),
    ]
    expect(selectAnchorNodeId(i, d, states, t)).toBe('c')
  })

  it('falls back to a graph sink node when the trace has no completed nodes', () => {
    const d = def([N('a'), N('b'), N('c')], [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ])
    const i = inst({ state: 'completed' })
    // 'c' has no outgoing edge => sink.
    expect(selectAnchorNodeId(i, d, {}, [])).toBe('c')
  })
})
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected FAIL:** completed cases return source-node fallback `'a'`.

#### Step 1.10 — Minimal impl for completed branch + sink helper

- [ ] Add a sink helper:

```ts
function sinkNodeId(def: WorkflowDefinition): string | null {
  const hasOutgoing = new Set(def.edges.map((e) => e.source))
  // Last node with no outgoing edge (terminal-most in declaration order).
  let sink: string | null = null
  for (const n of def.nodes) {
    if (!hasOutgoing.has(n.id)) sink = n.id
  }
  return sink
}
```

- [ ] Insert the completed branch after the failed branch (before the general fallback):

```ts
  // Completed: last completed node in trace order, else a sink node.
  if (instance.state === 'completed') {
    const completed = trace
      .filter((e) => e.status === 'succeeded' && ids.has(e.nodeId))
      .sort((a, b) => (a.completedAt ?? '').localeCompare(b.completedAt ?? ''))
    if (completed.length > 0) return completed[completed.length - 1].nodeId
    const sink = sinkNodeId(workflowDef)
    if (sink) return sink
  }
```

- [ ] Rename the unused `_trace` parameter to `trace` and update the signature.
- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected PASS:** 10 passing.
- [ ] Commit: `feat(ui-nits): selectAnchorNodeId completed terminal-node anchor`

#### Step 1.11 — Empty / degenerate graph (failing test)

- [ ] Add describe block:

```ts
describe('selectAnchorNodeId — empty / degenerate', () => {
  it('returns null when workflowDef is null', () => {
    expect(selectAnchorNodeId(null, null, {}, [])).toBeNull()
  })

  it('returns null when the graph has no nodes', () => {
    expect(selectAnchorNodeId(null, def([]), {}, [])).toBeNull()
  })

  it('returns null for a running instance on an empty graph', () => {
    const i = inst({ state: 'running', currentNodeId: 'a' })
    expect(selectAnchorNodeId(i, def([]), {}, [])).toBeNull()
  })
})
```

- [ ] Run: `npm test -- src/graph/selectAnchorNodeId.test.ts`
- [ ] **Expected:** PASS (the early `return null` on empty graph already covers these). If any FAIL, fix by ensuring the empty-graph guard runs before all branches.
- [ ] Commit: `test(ui-nits): selectAnchorNodeId empty/degenerate graph guards`

#### Step 1.12 — Full helper review

- [ ] Read the completed `src/graph/selectAnchorNodeId.ts` and confirm the branch order is: empty-graph guard → static preview (no instance) → running/waiting → failed → completed → general fallback (source → first node).
- [ ] Run the whole suite: `npm test`
- [ ] **Expected PASS:** all dashboard tests green (the new file plus existing suites).

---

### Task 2: Replace `fitView` with default-zoom centering on mount

Switch `GraphView`'s `<ReactFlow>` from `fitView` to centering on the anchor node at `DEFAULT_ZOOM = 0.9`, using `useReactFlow().setCenter` and node-center math from `positionMap` + the node-size constants. Preserve Phase 1's `edgeTypes`/`loopContainer` handling and keep `minZoom={0.3} maxZoom={2}`.

**Files:**

- Modify: `src/graph/useGraphLayout.ts` — export `NODE_WIDTH` and `NODE_HEIGHT` (currently module-private at lines 8–9) so centering math can reuse them.
- Modify: `src/routes/ExecutionView.tsx` — `GraphView` (lines 66–118): remove `fitView`/`fitViewOptions` (lines 107–108), add anchor resolution + mount-centering effect.
- Modify: `src/routes/GraphView.test.tsx` — extend the `@xyflow/react` mock (lines 21–42) to expose `setCenter` and assert it is called.

#### Step 2.1 — Export node-size constants (refactor, no behavior change)

- [ ] In `src/graph/useGraphLayout.ts`, change the constant declarations (lines 8–9) from:

```ts
const NODE_WIDTH = 200
const NODE_HEIGHT = 80
```

to:

```ts
export const NODE_WIDTH = 200
export const NODE_HEIGHT = 80
```

- [ ] Run: `npm test`
- [ ] **Expected PASS:** unchanged — all existing tests still green (pure export addition).
- [ ] Commit: `refactor(ui-nits): export NODE_WIDTH/NODE_HEIGHT from useGraphLayout`

#### Step 2.2 — Failing test: GraphView centers on the active node at DEFAULT_ZOOM on mount

The existing `GraphView.test.tsx` mocks `@xyflow/react`. Extend that mock so the `ReactFlow` mock exposes the `nodes` it received and so `useReactFlow` returns a spyable `setCenter`. Then add a test asserting `setCenter` is called once on mount with the active node's center coordinates and `{ zoom: 0.9 }`.

- [ ] In `src/routes/GraphView.test.tsx`, replace the mock factory (lines 21–42) with one that records nodes and provides `setCenter`:

```ts
const renderCount = { value: 0 }
const setCenterSpy = vi.fn()
let lastReactFlowProps: { nodes: Array<{ id: string; position?: { x: number; y: number } }> } | null = null

vi.mock('@xyflow/react', () => ({
  ReactFlow: (props: {
    nodes: Array<{ id: string; position?: { x: number; y: number } }>
  }) => {
    renderCount.value += 1
    lastReactFlowProps = props
    return (
      <div>
        {props.nodes.map((n) => (
          <div key={n.id} data-testid={`node-${n.id}`} />
        ))}
      </div>
    )
  },
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  useNodes: () => [],
  useReactFlow: () => ({
    flowToScreenPosition: (p: unknown) => p,
    setCenter: setCenterSpy,
  }),
}))
```

- [ ] Update `beforeEach` (lines 66–68) to also reset the spy and capture:

```ts
beforeEach(() => {
  renderCount.value = 0
  setCenterSpy.mockClear()
  lastReactFlowProps = null
})
```

- [ ] Add a new describe block at the end of the file:

```ts
describe('GraphView — default-zoom centering on mount', () => {
  it('centers on the active node at DEFAULT_ZOOM=0.9 on mount', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )

    // Active node is 'a' (instance.currentNodeId === 'a', state 'running').
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    const expectedX = aNode.position!.x + 200 / 2 // NODE_WIDTH / 2
    const expectedY = aNode.position!.y + 80 / 2 // NODE_HEIGHT / 2

    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    expect(setCenterSpy).toHaveBeenCalledWith(
      expectedX,
      expectedY,
      expect.objectContaining({ zoom: 0.9 }),
    )
  })

  it('does not call setCenter when there is no anchor (empty graph)', () => {
    const emptyDef = { ...workflowDef, nodes: [], edges: [] }
    render(
      <GraphView
        instance={instance}
        workflowDef={emptyDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(setCenterSpy).not.toHaveBeenCalled()
  })
})
```

- [ ] Run: `npm test -- src/routes/GraphView.test.tsx`
- [ ] **Expected FAIL:** `setCenterSpy` not called (GraphView still uses `fitView` and never calls `setCenter`).

#### Step 2.3 — Minimal impl: anchor + mount-centering effect in GraphView

- [ ] In `src/routes/ExecutionView.tsx`, update imports:
  - Add `useRef` to the `react` import (line 1): `import { useState, useEffect, useCallback, useMemo, useRef } from 'react'`.
  - Add `useReactFlow` to the `@xyflow/react` import (line 3): `import { ReactFlowProvider, ReactFlow, Background, Controls, useReactFlow } from '@xyflow/react'`.
  - Add to the layout import (line 9): `import { useGraphLayout, NODE_WIDTH, NODE_HEIGHT } from '../graph/useGraphLayout'`.
  - Add: `import { selectAnchorNodeId } from '../graph/selectAnchorNodeId'`.

- [ ] Add the default-zoom constant near the top of the file (after the imports, before `buildExecutionStates`):

```ts
const DEFAULT_ZOOM = 0.9
```

- [ ] Inside `GraphView`, after `nodesWithSelection` is computed (after line 98) and before the `return`, add anchor resolution and the mount-centering effect:

```ts
  const reactFlow = useReactFlow()

  // The real step node the viewport should center on for this instance state.
  const anchorId = useMemo(
    () => selectAnchorNodeId(instance, workflowDef, executionStates, trace),
    [instance, workflowDef, executionStates, trace],
  )

  // Resolve the anchor's node-center coordinates from the laid-out rfNodes.
  // Loop-container overlay nodes are excluded automatically: anchorId only
  // ever references a real workflowDef node id.
  const anchorPos = useMemo(() => {
    if (!anchorId) return null
    const node = rfNodes.find((n) => n.id === anchorId)
    if (!node) return null
    return {
      x: node.position.x + NODE_WIDTH / 2,
      y: node.position.y + NODE_HEIGHT / 2,
    }
  }, [anchorId, rfNodes])

  // Center once, on first mount, at the readable default zoom. Guarded by a ref
  // so live WebSocket re-renders (which re-run this component) do not re-center
  // and fight the user. Follow-on-advance behavior is added in task-2 of the
  // task-2 plan via useFollowActiveNode; this effect is the one-shot mount center.
  const didInitialCenter = useRef(false)
  useEffect(() => {
    if (didInitialCenter.current) return
    if (!anchorPos) return
    reactFlow.setCenter(anchorPos.x, anchorPos.y, { zoom: DEFAULT_ZOOM })
    didInitialCenter.current = true
  }, [anchorPos, reactFlow])
```

- [ ] Update the `<ReactFlow>` element (lines 102–115): remove the `fitView` and `fitViewOptions` props (lines 107–108). Keep `minZoom={0.3}` and `maxZoom={2}`. Preserve any Phase 1 props (`edgeTypes`, etc.) if present. Result:

```tsx
      <ReactFlow
        nodes={nodesWithSelection}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        onNodeClick={(_event, node) => onNodeClick(node.id)}
        minZoom={0.3}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
        <Background color="#2a2a4a" gap={20} size={1} />
        <Controls showInteractive={false} />
      </ReactFlow>
```

- [ ] Run: `npm test -- src/routes/GraphView.test.tsx`
- [ ] **Expected PASS:** both new centering tests pass; the existing no-remount tests still pass.

> Note on the mock: the existing `useReactFlow` mock returned only `flowToScreenPosition`. Step 2.2 already extended it to include `setCenter`. Because the mock's `setCenter` is a stable `vi.fn` referenced via a module-level `useReactFlow` factory, the `reactFlow` object identity is recreated each render — the mount-center effect is guarded by `didInitialCenter` so it still only fires once. The "called once" assertion verifies this.

#### Step 2.4 — Failing test: completed instance centers on the final node

- [ ] Add to the `default-zoom centering on mount` describe block:

```ts
  it('centers on the final node for a completed instance', () => {
    const completedInstance = {
      ...instance,
      state: 'completed',
      currentNodeId: undefined,
    } as unknown as Instance
    const completedTrace: NodeExecution[] = [
      {
        id: 'e-a',
        instanceId: 'i',
        nodeId: 'a',
        attempt: 1,
        status: 'succeeded',
        startedAt: '2026-01-01T00:00:00Z',
        completedAt: '2026-01-01T00:00:01Z',
      } as NodeExecution,
      {
        id: 'e-b',
        instanceId: 'i',
        nodeId: 'b',
        attempt: 1,
        status: 'succeeded',
        startedAt: '2026-01-01T00:00:02Z',
        completedAt: '2026-01-01T00:00:03Z',
      } as NodeExecution,
    ]
    render(
      <GraphView
        instance={completedInstance}
        workflowDef={workflowDef}
        trace={completedTrace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(setCenterSpy).toHaveBeenCalledWith(
      bNode.position!.x + 100,
      bNode.position!.y + 40,
      expect.objectContaining({ zoom: 0.9 }),
    )
  })
```

- [ ] Run: `npm test -- src/routes/GraphView.test.tsx`
- [ ] **Expected:** PASS (the helper + effect already handle completed). This is a regression guard confirming the end-to-end wiring picks the right anchor for a non-running state. If it FAILs, debug with superpowers:systematic-debugging — likely the anchor memo deps or the `rfNodes.find` lookup.
- [ ] Commit: `feat(ui-nits): default-zoom centering on anchor node, drop fitView`

#### Step 2.5 — Full suite + typecheck

- [ ] Run: `npm test`
- [ ] **Expected PASS:** all dashboard tests green.
- [ ] Run: `npm run build` (runs `tsc && vite build`) — confirms no TypeScript errors from the new imports/exports.
- [ ] **Expected:** build succeeds. If `tsc` errors on an unused Phase-1 prop or missing type, resolve before continuing.
- [ ] Commit any build-fix follow-ups separately if needed.

---

## Done criteria for this plan file

- `selectAnchorNodeId` exists with passing unit tests for all five scenario categories: running (active node), completed (final node), failed (failed node), static preview (entrypoint), and fallbacks (no entrypoint flag, missing currentNodeId, sink fallback, empty graph).
- `GraphView` no longer uses `fitView`; it centers on the anchor at `DEFAULT_ZOOM = 0.9` on mount with `minZoom 0.3` / `maxZoom 2` preserved, and `NODE_WIDTH`/`NODE_HEIGHT` are exported and reused.
- Phase 1's `edgeTypes`/`loopContainer` nodes on `<ReactFlow>` are preserved (not removed).
- The follow-on-advance behavior and Recenter control are implemented in `ui-nits-implementation-phase-2-task-2.md`.
