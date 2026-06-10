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

describe('useGraphLayout — error/invalid: empty nodes list', () => {
  it('returns empty arrays and needsAutoSave=false when nodes is empty', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes: [], edges: [] }))
    expect(result.current.rfNodes).toEqual([])
    expect(result.current.rfEdges).toEqual([])
    expect(result.current.needsAutoSave).toBe(false)
  })
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

  // Design constraint: loop containers are derived overlays, NOT React Flow parent nodes.
  // Step nodes must never have a parentId pointing to a container. Using parentId would
  // make ReactFlow treat the container as a parent scope and break the overlay approach.
  it('step nodes in the loop body have no parentId', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const stepNodes = result.current.rfNodes.filter((n) => n.type === 'arbitronNode')
    for (const n of stepNodes) {
      expect((n as { parentId?: string }).parentId).toBeUndefined()
    }
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

  // Seam test (detection→geometry): verifies that detectLoops produces bodyNodeIds that
  // loopContainerBox uses to compute the correct box from the known node positions.
  // b=(100,0), c=(100,200). NODE_WIDTH=200, NODE_HEIGHT=80, CONTAINER_PAD=24.
  // Expected: minX=100, minY=0, maxX=300, maxY=280 → box (76, -24, 224, 328).
  it('container position and size reflect the bounding box of body node positions', () => {
    const nodes: WorkflowNode[] = [
      { ...node('b'), position: { x: 100, y: 0 } },
      { ...node('c'), position: { x: 100, y: 200 } },
    ]
    const edges: WorkflowEdge[] = [{ source: 'b', target: 'c' }, { source: 'c', target: 'b' }]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    type BoxNode = { type?: string; position?: { x: number; y: number }; width?: number; height?: number }
    const container = result.current.rfNodes.find((n) => n.type === 'loopContainer') as BoxNode | undefined
    expect(container).toBeDefined()
    // x = minX - PAD = 100 - 24 = 76
    expect(container!.position!.x).toBe(76)
    // y = minY - PAD = 0 - 24 = -24
    expect(container!.position!.y).toBe(-24)
    // width = (maxX - minX) + 2*PAD = (300-100) + 48 = 248
    expect(container!.width).toBe(248)
    // height = (maxY - minY) + 2*PAD = (280-0) + 48 = 328
    expect(container!.height).toBe(328)
  })
})

describe('useGraphLayout — container zIndex', () => {
  // zIndex = -10 + depth. A sign flip would put inner containers BELOW outer ones,
  // reversing the layering so outer containers visually obscure inner containers.
  it('single loop container has zIndex = -10 (depth 0)', () => {
    const nodes = [node('a'), node('b'), node('c')]
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'c', target: 'b' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const container = result.current.rfNodes.find((n) => n.type === 'loopContainer')!
    expect(container.zIndex).toBe(-10)
  })

  it('inner container (depth 1) has a higher zIndex than outer (depth 0)', () => {
    const nodes = [node('a'), node('b'), node('c'), node('d'), node('e')]
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'c', target: 'd' },
      { source: 'd', target: 'e' },
      { source: 'e', target: 'b', label: 'outer' },
      { source: 'd', target: 'c', label: 'inner' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    type ZNode = { type?: string; zIndex?: number; data?: { label?: string } }
    const containers = result.current.rfNodes.filter((n) => n.type === 'loopContainer') as ZNode[]
    const outer = containers.find((c) => c.data?.label === 'Loop until outer')!
    const inner = containers.find((c) => c.data?.label === 'Loop until inner')!
    expect(outer.zIndex).toBe(-10) // depth 0
    expect(inner.zIndex).toBe(-9)  // depth 1: inner is above outer
    expect(inner.zIndex!).toBeGreaterThan(outer.zIndex!)
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

describe('useGraphLayout — edge case: self-loop (node edges to itself)', () => {
  it('wraps the self-looping node in a container and types the self-edge loopBackEdge', () => {
    // spec: "self-loop — treated as a single-node loop; container wraps that node"
    const nodes = [node('a'), node('b'), node('c')]
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'b', label: 'retry' },
      { source: 'b', target: 'c' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const containers = result.current.rfNodes.filter((n) => n.type === 'loopContainer')
    expect(containers).toHaveLength(1)
    expect((containers[0].data as { label: string }).label).toBe('Loop until retry')
    const selfEdge = result.current.rfEdges.find((e) => e.source === 'b' && e.target === 'b')!
    expect(selfEdge.type).toBe('loopBackEdge')
    // Forward edges are unaffected.
    const fwd = result.current.rfEdges.find((e) => e.source === 'a' && e.target === 'b')!
    expect(fwd.type).toBe('smoothstep')
  })
})

describe('useGraphLayout — back-edge carries the container box', () => {
  it('includes a box on the loopBackEdge data', () => {
    const nodes: WorkflowNode[] = [
      { ...node('b'), position: { x: 100, y: 0 } },
      { ...node('c'), position: { x: 100, y: 200 } },
    ]
    const edges: WorkflowEdge[] = [
      { source: 'b', target: 'c' },
      { source: 'c', target: 'b', label: 'again' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const back = result.current.rfEdges.find((e) => e.source === 'c' && e.target === 'b')!
    const box = (back.data as { box?: { width: number } }).box
    expect(box).toBeDefined()
    expect(box!.width).toBeGreaterThan(0)
  })

  // Seam: the back-edge's data.box must be the SAME bounding box as the container node's
  // position+size. Both are computed from loopContainerBox(loop, positionMap), but called
  // separately. If either call used a different padding or positionMap, the edge would
  // route around the wrong area while the container is displayed correctly (silent mismatch).
  it('back-edge data.box matches the container node position and size', () => {
    const nodes: WorkflowNode[] = [
      { ...node('b'), position: { x: 100, y: 0 } },
      { ...node('c'), position: { x: 100, y: 200 } },
    ]
    const edges: WorkflowEdge[] = [
      { source: 'b', target: 'c' },
      { source: 'c', target: 'b' },
    ]
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    type BoxNode = { type?: string; position?: { x: number; y: number }; width?: number; height?: number }
    type BoxEdge = { data?: { box?: { x: number; y: number; width: number; height: number } } }
    const container = result.current.rfNodes.find((n) => n.type === 'loopContainer') as BoxNode
    const back = result.current.rfEdges.find((e) => e.source === 'c' && e.target === 'b') as BoxEdge
    const box = back?.data?.box
    expect(box).toBeDefined()
    expect(box!.x).toBe(container.position!.x)
    expect(box!.y).toBe(container.position!.y)
    expect(box!.width).toBe(container.width)
    expect(box!.height).toBe(container.height)
  })
})

describe('useGraphLayout — loop-back edge routing data', () => {
  const nodes = [node('a'), node('b'), node('c'), node('d')]
  const edges: WorkflowEdge[] = [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'd' },
    { source: 'c', target: 'b', label: 'tests pass' },
  ]

  it('tags the back-edge with its loop body box and leaves no explicit handles (the edge routes itself)', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const back = result.current.rfEdges.find((e) => e.type === 'loopBackEdge')!
    expect(back).toBeDefined()
    // Routing is computed inside LoopBackEdge from the default handle coords, so
    // no side-handle is wired here (which would otherwise capture forward edges).
    expect(back.sourceHandle).toBeUndefined()
    expect(back.targetHandle).toBeUndefined()
    expect((back.data as { box?: unknown }).box).toBeDefined()
  })

  it('leaves forward edges on the default top/bottom handles', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes, edges }))
    const fwd = result.current.rfEdges.find((e) => e.type === 'smoothstep')!
    expect(fwd.sourceHandle).toBeUndefined()
    expect(fwd.targetHandle).toBeUndefined()
  })
})

describe('useGraphLayout — referential stability (regression: WorkflowReview blank-screen loop)', () => {
  // When executionStates is omitted, the memo MUST return the same rfNodes/rfEdges
  // references across re-renders. A fresh `{}` default each call defeats the useMemo,
  // so WorkflowReview's `setNodes(rfNodes)` sync-effect chases a new array every
  // render and loops forever (React error #185, "Maximum update depth exceeded") the
  // instant a node is selected — blanking the whole app.
  it('keeps rfNodes/rfEdges referentially stable across re-renders when executionStates is omitted', () => {
    const nodes = [node('a'), node('b'), node('c')]
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ]
    const { result, rerender } = renderHook(() => useGraphLayout({ nodes, edges }))
    const firstNodes = result.current.rfNodes
    const firstEdges = result.current.rfEdges

    rerender()

    expect(result.current.rfNodes).toBe(firstNodes)
    expect(result.current.rfEdges).toBe(firstEdges)
  })
})
