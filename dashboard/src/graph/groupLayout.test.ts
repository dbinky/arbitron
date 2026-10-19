import { describe, it, expect } from 'vitest'
import wireFixtures from './testdata/group-layout-wire.json'
import { normalizeGroupGraph } from '../api/client'
import type { GroupGraph } from '../api/types'
import type { Node } from '@xyflow/react'
import {
  bodySinks,
  definitionNodeIdOf,
  groupBoxId,
  groupContainerLabel,
  groupFooterId,
  groupHeaderId,
  groupTaskCardId,
  immediateMembers,
  planGroupLayout,
  toDefinitionPositions,
} from './groupLayout'
import { branchEdges, branchGraph, branchNodes, mixedEdges, mixedGraph, mixedNodes, node } from './groupLayout.fixtures'
import type { WorkflowEdge } from './types'

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

describe('group presentation ids', () => {
  it('derives stable synthetic ids from the definition node id', () => {
    expect(groupHeaderId('g1')).toBe('group:g1:header')
    expect(groupFooterId('g1')).toBe('group:g1:footer')
    expect(groupBoxId('g1')).toBe('group:g1:box')
    expect(groupTaskCardId('w1', 't2')).toBe('group:w1:task:t2')
  })

  it('maps a rendered node back to its definition node id', () => {
    expect(definitionNodeIdOf({ id: 'group:g1:footer', data: { definitionNodeId: 'g1' } })).toBe('g1')
    expect(definitionNodeIdOf({ id: 'a1', data: { label: 'A1' } })).toBe('a1')
  })

  it('labels a container by form and rule', () => {
    expect(groupContainerLabel(branchGraph.groups.g1)).toBe('Parallel branches · all succeed')
    expect(groupContainerLabel(mixedGraph.groups.w1)).toBe('Parallel workflows · first success')
  })
})

describe('immediateMembers / bodySinks — ownership comes from GroupGraph.owner', () => {
  it('lists only IMMEDIATE members: a nested group is a member, its body nodes are not', () => {
    expect(immediateMembers(branchGraph, branchNodes, 'g1', 'b')).toEqual(['g2', 'b2', 'b3'])
    expect(immediateMembers(branchGraph, branchNodes, 'g2', 'y')).toEqual(['y1', 'y2'])
  })

  it('finds the sinks of each body (no outgoing edge, nested groups excluded)', () => {
    expect(bodySinks(branchGraph, branchNodes, branchEdges, 'g1', 'a')).toEqual(['a2'])
    expect(bodySinks(branchGraph, branchNodes, branchEdges, 'g1', 'b')).toEqual(['b2', 'b3'])
    expect(bodySinks(branchGraph, branchNodes, branchEdges, 'g2', 'x')).toEqual(['x1'])
  })

  it('falls back to the task entry when a body has no sink', () => {
    const cyclic: WorkflowEdge[] = [...branchEdges, { source: 'x1', target: 'x1' }]
    expect(bodySinks(branchGraph, branchNodes, cyclic, 'g2', 'x')).toEqual(['x1'])
  })

  it('plans header/footer per group and sink connectors only for branches', () => {
    const plan = planGroupLayout(mixedNodes, mixedEdges, mixedGraph)
    expect(plan.groups).toEqual(['g1', 'w1'])
    expect(plan.depth).toEqual({ g1: 0, w1: 1 })
    expect(plan.presentationEdges.filter((e) => e.kind === 'sink').map((e) => e.groupNodeId)).toEqual(['g1', 'g1', 'g1'])
    expect(plan.presentationEdges.filter((e) => e.kind === 'fanout').map((e) => e.taskKey)).toEqual(['t1', 't2', 't3'])
  })

  it('ignores groups that are not in the node list (stale layout) without throwing', () => {
    const plan = planGroupLayout([node('a')], [], branchGraph)
    expect(plan.groups).toEqual([])
    expect(plan.presentationNodes).toEqual([])
  })
})

describe('toDefinitionPositions', () => {
  it('saves a header as its group node and drops footers, cards and boxes', () => {
    const rf = [
      { id: 'a1', type: 'arbitronNode', position: { x: 1, y: 2 }, data: {} },
      { id: groupHeaderId('w1'), type: 'groupHeader', position: { x: 10, y: 20 }, data: { definitionNodeId: 'w1' } },
      { id: groupFooterId('w1'), type: 'groupFooter', position: { x: 10, y: 400 }, data: { definitionNodeId: 'w1' } },
      { id: groupTaskCardId('w1', 't1'), type: 'groupTaskCard', position: { x: 0, y: 200 }, data: { definitionNodeId: 'w1' } },
      { id: groupBoxId('w1'), type: 'groupContainer', position: { x: -30, y: -30 }, data: { definitionNodeId: 'w1' } },
    ] as Node[]
    expect(toDefinitionPositions(rf)).toEqual({ a1: { x: 1, y: 2 }, w1: { x: 10, y: 20 } })
  })

  it('passes loop containers and step nodes through unchanged (pre-existing behaviour)', () => {
    const rf = [
      { id: 'a', type: 'arbitronNode', position: { x: 1, y: 2 }, data: {} },
      { id: 'loop-container-x', type: 'loopContainer', position: { x: 3, y: 4 }, data: {} },
    ] as Node[]
    expect(toDefinitionPositions(rf)).toEqual({ a: { x: 1, y: 2 }, 'loop-container-x': { x: 3, y: 4 } })
  })
})

// The HTTP test pins this shared JSON to AnalyzeGroups. This equality catches
// transitive body lists that the real producer never emits.
it('branch fixture matches the producer immediate-owner shape', () => {
  const wire = wireFixtures.find((fixture) => fixture.name === 'nested-branches')!
  expect(branchGraph).toEqual(normalizeGroupGraph(wire.layout as unknown as GroupGraph))
})
