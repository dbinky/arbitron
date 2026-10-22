import { describe, it, expect } from 'vitest'
import { buildArrangedFlow, arrangedEdgeTypes, arrangedNodeTypes } from './buildArrangedFlow'
import { fixtureGeometry } from './testFixtures'
import ArrangedNode from './ArrangedNode'
import RoutedEdge from './RoutedEdge'

const types = { a: 'script', b: 'llm_call', c: 'conditional' }

describe('buildArrangedFlow', () => {
  it('feeds payload positions, sizes, text, types and runtime states into registered nodes', () => {
    const geo = fixtureGeometry()
    const { nodes } = buildArrangedFlow(geo, types, { b: 'running' }, 'test-graph')
    expect(nodes.map(n => n.id)).toEqual(['a', 'b', 'c'])
    for (const [i, n] of nodes.entries()) {
      const nb = geo.nodes[i]
      expect(n.type).toBe('arrangedNode')
      expect(n.position).toEqual({ x: nb.box.x, y: nb.box.y })
      expect([n.width, n.height]).toEqual([nb.box.w, nb.box.h])
      expect(n.draggable).toBe(false)
      expect(n.connectable).toBe(false)
      expect(n.selectable).toBe(true)
      expect(n.data.nodeBox).toBe(nb)
      expect(n.data.contract).toBe(geo.contract)
      expect(n.data.nodeType).toBe(types[n.id as keyof typeof types])
    }
    expect(nodes.map(n => n.data.executionState)).toEqual(['pending', 'running', 'pending'])
    expect(arrangedNodeTypes.arrangedNode).toBe(ArrangedNode)
    expect(arrangedEdgeTypes.routedEdge).toBe(RoutedEdge)
  })
  it('rejects absent, empty and mismatching types and graph namespaces', () => {
    for (const bad of [{}, { ...types, a: '' }, { ...types, a: 'tool_call' }]) {
      expect(() => buildArrangedFlow(fixtureGeometry(), bad, {}, 'test')).toThrow(/node type/)
    }
    for (const id of ['', undefined]) {
      expect(() => buildArrangedFlow(fixtureGeometry(), types, {}, id as string)).toThrow(/graphInstanceId/)
    }
  })
  it('preserves ordinal routes, namespace, under-edge gaps, over crossings and source-only activity', () => {
    const geo = fixtureGeometry()
    geo.crossings = [
      { point: { x: 140, y: 222 }, over: 2, under: 1 },
      { point: { x: 135, y: 116 }, over: 1, under: 0 },
      { point: { x: 140, y: 230 }, over: 0, under: 1 },
    ]
    const { edges } = buildArrangedFlow(geo, types, {}, 'test-graph', new Set(['b']))
    expect(edges.map(e => e.id)).toEqual(['routed-0', 'routed-1', 'routed-2'])
    for (const [i, e] of edges.entries()) {
      expect(e.type).toBe('routedEdge')
      expect([e.source, e.target]).toEqual([geo.edges[i].source, geo.edges[i].target])
      expect(e.selectable).toBe(false)
      expect(e.focusable).toBe(false)
      expect(e.data!.route).toBe(geo.edges[i])
      expect(e.data!.graphInstanceId).toBe('test-graph')
      expect(e.data!.contract).toBe(geo.contract)
    }
    expect(edges.map(e => e.data!.gaps)).toEqual([
      [{ x: 135, y: 116 }], [{ x: 140, y: 222 }, { x: 140, y: 230 }], [],
    ])
    expect(edges.map(e => e.data!.overs)).toEqual([
      [{ x: 140, y: 230 }], [{ x: 135, y: 116 }], [{ x: 140, y: 222 }],
    ])
    expect(edges.map(e => e.data!.active)).toEqual([false, true, false])
    expect(buildArrangedFlow(geo, types, {}, 'test').edges.every(e => !e.data!.active)).toBe(true)
  })
  it('does not collapse identical parallel occurrences', () => {
    const geo = fixtureGeometry()
    geo.edges.push({ ...structuredClone(geo.edges[0]), ordinal: 3 })
    const { edges } = buildArrangedFlow(geo, types, {}, 'parallel')
    expect(edges.map(e => e.id)).toEqual(['routed-0', 'routed-1', 'routed-2', 'routed-3'])
    expect(edges[3].data!.route).toBe(geo.edges[3])
    expect(edges[3].data!.graphInstanceId).toBe('parallel')
  })
})
