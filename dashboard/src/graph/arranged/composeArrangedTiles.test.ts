import { renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createHash } from 'node:crypto'
import type { GroupGraph } from '../../api/types'
import type { WorkflowDefinition } from '../types'
import * as fallback from '../useGraphLayout'
import { buildGroupProbeCases } from '../../render/groupProbes'
import { buildArrangedFlow } from './buildArrangedFlow'
import { groupElementID } from './groupValidate'
import { resolveDisplayGeometry } from './validate'
afterEach(() => vi.restoreAllMocks())
import { composeArrangedTiles, type LayoutTile } from './composeArrangedTiles'
import { fixtureView } from './testFixtures'
import v2 from './groupGeometry.v2.json'
import type { Geometry } from './types'

function tile(id: string, index = 2): LayoutTile {
  const f = structuredClone(v2[index]) as unknown as { definition: WorkflowDefinition; groups: GroupGraph; geometry: Geometry }
  f.definition.presentation = { revision: id } as WorkflowDefinition['presentation']
  const probe = buildGroupProbeCases(f.groups).find(p => p.name === 'group:badges')!
  return { instanceId: id, definition: f.definition, groups: f.groups, view: fixtureView({ savedRevision: id, geometry: f.geometry, groups: f.groups }),
    groupLive: probe.groupLive, nodeContexts: probe.nodeContexts, executionStates: Object.fromEntries(f.definition.nodes.map(n => [n.id, 'running'])) }
}
function renameStart(t: LayoutTile, id: string) {
  const definition = t.definition.nodes.find(n => n.id === 'start')!
  const nodeBox = t.view!.geometry!.nodes.find(n => n.id === 'start')!
  definition.id = id; definition.label = nodeBox.text; nodeBox.id = id
  for (const e of t.definition.edges) { if (e.source === 'start') e.source = id; if (e.target === 'start') e.target = id }
  for (const e of t.view!.geometry!.edges) {
    if (e.source === 'start') e.source = id
    if (e.target === 'start') e.target = id
    for (const a of [e.sourceAttach, e.targetAttach]) if (a.nodeId === 'start') { a.nodeId = id; a.elementId = groupElementID(id, 'header') }
  }
}
const digest = (x: unknown) => createHash('sha256').update(JSON.stringify(x)).digest('hex')

// Reverse every translated coordinate, not a composer-produced checksum.
function untranslate(value: unknown, dy: number): unknown {
  if (Array.isArray(value)) return value.map(v => untranslate(v, dy))
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, k === 'y' && typeof v === 'number' ? v - dy : untranslate(v, dy)]))
  return value
}
function absolute(n: { position: { x: number; y: number }; parentId?: string }, nodes: ReturnType<typeof composeArrangedTiles>['nodes']): { x: number; y: number } {
  const p = n.parentId ? absolute(nodes.find(p => p.id === n.parentId)!, nodes) : { x: 0, y: 0 }
  return { x: p.x + n.position.x, y: p.y + n.position.y }
}

describe('translation-only independent layout tiles', () => {
  it.each([1, 2, 3, 4])('preserves full local geometry, routes, attachments, text, crossings, and live inputs for form %i', index => {
    const tiles = [tile('root', index), tile('子:"|é', index), tile('子:"|é:2', index)]
    const before = digest(tiles), fallbackSpy = vi.spyOn(fallback, 'computeGraphLayout')
    const scene = composeArrangedTiles(tiles, 'graph-test')
    expect(scene.nodes.length).toBeGreaterThan(0)
    const ids = new Set([...scene.nodes, ...scene.edges].map(n => n.id))
    expect(ids.size).toBe(scene.nodes.length + scene.edges.length)
    for (const [i, t] of tiles.entries()) {
      const geo = t.view!.geometry!, dy = i * (geo.canvas.h + geo.contract.groupRowGapPx)
      const local = buildArrangedFlow(geo, Object.fromEntries(t.definition.nodes.map(n => [n.id, n.type])), t.executionStates!, 'local', new Set(Object.keys(t.executionStates!)), 
        { groups: t.groups, groupLive: t.groupLive, nodeContexts: t.nodeContexts, layoutInstanceId: t.instanceId })
      const nodes = scene.nodes.filter(n => n.data.layoutInstanceId === t.instanceId)
      const edges = scene.edges.filter(e => e.data?.layoutInstanceId === t.instanceId)
      expect(nodes).toHaveLength(local.nodes.length); expect(edges).toHaveLength(local.edges.length)
      for (const [j, n] of nodes.entries()) {
        const data = { ...n.data }; delete data.handles
        const expected = { ...local.nodes[j].data }; delete expected.handles
        expect(untranslate(data, dy)).toEqual(expected)
        expect(absolute(n, scene.nodes)).toEqual({ ...absolute(local.nodes[j], local.nodes), y: absolute(local.nodes[j], local.nodes).y + dy })
        const logical = (n.data.reviewed as { element: { id: string } } | undefined)?.element.id ?? groupElementID((n.data.nodeBox as { id: string }).id, 'header')
        const encoded = btoa(String.fromCodePoint(...new TextEncoder().encode(JSON.stringify(['tile', t.instanceId, logical])))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
        expect(n.id).toBe(`graph-test-${encoded}`)
      }
      for (const [j, e] of edges.entries()) {
        expect(nodes.some(n => n.id === e.source)).toBe(true); expect(nodes.some(n => n.id === e.target)).toBe(true)
        expect(untranslate({ ...e.data, graphInstanceId: 'local' }, dy)).toEqual(local.edges[j].data)
      }
    }
    expect(scene.bounds).toEqual({ x: 0, y: 0, w: tiles[0].view!.geometry!.canvas.w, h: tiles[0].view!.geometry!.canvas.h * 3 + tiles[0].view!.geometry!.contract.groupRowGapPx * 2 })
    expect(digest(tiles)).toBe(before)
    expect(fallbackSpy).not.toHaveBeenCalled(); fallbackSpy.mockRestore()
  })
  it('qualifies reused Unicode/delimiter node IDs without losing their original server identity', () => {
    const tiles = [tile('root'), tile('子:"|é'), tile('子:"|é:2')]
    const id = 'node:"|漢:é'
    for (const t of tiles) renameStart(t, id)
    for (const t of tiles) expect(resolveDisplayGeometry(t.definition, t.view).issues).toEqual([])
    const before = digest(tiles), scene = composeArrangedTiles(tiles, 'graph-utf8')
    const nodes = scene.nodes.filter(n => n.data.definitionNodeId === id)
    expect(nodes).toHaveLength(3); expect(new Set(nodes.map(n => n.id)).size).toBe(3)
    for (const n of nodes) {
      const tuple = JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(n.id.slice('graph-utf8-'.length).replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0))))
      expect(tuple).toEqual(['tile', n.data.layoutInstanceId, groupElementID(id, 'header')])
      expect(scene.edges.some(e => e.source === n.id && e.data!.layoutInstanceId === n.data.layoutInstanceId)).toBe(true)
    }
    expect(digest(tiles)).toBe(before)
  })
  it('keeps an author node ID that resembles a generated group element distinct from that element', () => {
    const root = tile('root')
    renameStart(root, root.view!.geometry!.groupElements![0].id)
    expect(resolveDisplayGeometry(root.definition, root.view).issues).toEqual([])
    const scene = composeArrangedTiles([root], 'graph-collision')
    expect(scene.nodes.length).toBeGreaterThan(0)
    expect(new Set(scene.nodes.map(n => n.id)).size).toBe(scene.nodes.length)
  })
  it('runs fallback only for the missing child, preserving reviewed root and partial retained positions', () => {
    const root = tile('root'), child = tile('child')
    child.view = fixtureView({ savedRevision: 'child', geometry: undefined, retained: { source: 'manifest', positions: { [child.definition.nodes[0].id]: { x: 789, y: 456 } } } })
    const spy = vi.spyOn(fallback, 'computeGraphLayout'), original = digest([root, child])
    const scene = composeArrangedTiles([root, child], 'graph-test')
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0]).toMatchObject({ nodes: child.definition.nodes, groups: child.groups, groupLive: child.groupLive, nodeContexts: child.nodeContexts })
    expect(scene.nodes.filter(n => n.data.layoutInstanceId === 'root').every(n => n.type !== 'arbitronNode')).toBe(true)
    expect(scene.nodes.some(n => n.data.layoutInstanceId === 'child')).toBe(true)
    expect(digest([root, child])).toBe(original); spy.mockRestore()
  })
  it('pure extraction has hook parity without mutating definitions or live maps', () => {
    const t = tile('child'), input = { nodes: t.definition.nodes, edges: t.definition.edges, groups: t.groups, groupLive: t.groupLive, nodeContexts: t.nodeContexts, executionStates: t.executionStates }
    const before = digest(input), pure = fallback.computeGraphLayout(input)
    const { result } = renderHook(() => fallback.useGraphLayout(input))
    expect(pure).toEqual(result.current); expect(digest(input)).toBe(before)
  })
})
