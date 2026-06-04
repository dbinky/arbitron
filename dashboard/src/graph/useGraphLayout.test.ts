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
})
