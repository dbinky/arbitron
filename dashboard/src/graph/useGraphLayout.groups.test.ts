import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import type { Edge, Node } from '@xyflow/react'
import type { GroupGraph } from '../api/types'
import type { WorkflowEdge, WorkflowNode } from './types'
import { useGraphLayout } from './useGraphLayout'
import {
  GROUP_PAD,
  groupBoxId,
  groupFooterId,
  groupHeaderId,
  groupTaskCardId,
  toDefinitionPositions,
  type GroupLiveSummary,
  type NodeContextInfo,
} from './groupLayout'
import {
  EMPTY_GRAPH,
  branchEdges,
  branchGraph,
  branchNodes,
  mixedEdges,
  mixedGraph,
  mixedNodes,
  node,
  scope,
} from './groupLayout.fixtures'

type Rect = { x: number; y: number; width: number; height: number }

const W = 200
function rectOf(n: Node | undefined, h = 80): Rect {
  if (!n) throw new Error('node missing')
  if (n.width != null && n.height != null) return { x: n.position.x, y: n.position.y, width: n.width, height: n.height }
  return { x: n.position.x, y: n.position.y, width: W, height: h }
}
function union(rs: Rect[]): Rect {
  const x = Math.min(...rs.map((r) => r.x))
  const y = Math.min(...rs.map((r) => r.y))
  return { x, y, width: Math.max(...rs.map((r) => r.x + r.width)) - x, height: Math.max(...rs.map((r) => r.y + r.height)) - y }
}
function overlaps(a: Rect, b: Rect): boolean {
  return a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height
}
function contains(outer: Rect, inner: Rect): boolean {
  return inner.x >= outer.x && inner.y >= outer.y && inner.x + inner.width <= outer.x + outer.width && inner.y + inner.height <= outer.y + outer.height
}

function layout(nodes: WorkflowNode[], edges: WorkflowEdge[], groups: GroupGraph | null, extra: { groupLive?: Record<string, GroupLiveSummary>; nodeContexts?: Record<string, NodeContextInfo> } = {}) {
  const { result } = renderHook(() => useGraphLayout({ nodes, edges, groups, ...extra }))
  const byId = new Map(result.current.rfNodes.map((n) => [n.id, n]))
  return { ...result.current, byId }
}

const edgeFrom = (edges: Edge[], source: string, target: string) => edges.find((e) => e.source === source && e.target === target)

// ---------------------------------------------------------------------------
// useGraphLayout with groups — branches form, two levels
// ---------------------------------------------------------------------------

describe('useGraphLayout — branches groups nested two levels', () => {
  it('replaces each group node with a header and a footer mapped back to the definition node', () => {
    const { rfNodes, byId } = layout(branchNodes, branchEdges, branchGraph)
    expect(byId.has('g1')).toBe(false)
    expect(byId.has('g2')).toBe(false)
    for (const g of ['g1', 'g2']) {
      expect(byId.get(groupHeaderId(g))?.type).toBe('groupHeader')
      expect(byId.get(groupFooterId(g))?.type).toBe('groupFooter')
      expect((byId.get(groupHeaderId(g))!.data as { definitionNodeId: string }).definitionNodeId).toBe(g)
      expect((byId.get(groupFooterId(g))!.data as { definitionNodeId: string }).definitionNodeId).toBe(g)
    }
    // Every non-group definition node is still a step node.
    expect(rfNodes.filter((n) => n.type === 'arbitronNode').map((n) => n.id).sort()).toEqual(
      ['a1', 'a2', 'b2', 'b3', 'done', 'oops', 'start', 'x1', 'y1', 'y2'],
    )
  })

  it('routes task edges out of the header and success/failure out of the footer', () => {
    const { rfEdges } = layout(branchNodes, branchEdges, branchGraph)
    expect(edgeFrom(rfEdges, 'start', groupHeaderId('g1'))).toBeDefined()
    expect(edgeFrom(rfEdges, groupHeaderId('g1'), 'a1')?.label).toBe('a')
    expect(edgeFrom(rfEdges, groupHeaderId('g1'), groupHeaderId('g2'))?.label).toBe('b')
    expect(edgeFrom(rfEdges, groupFooterId('g1'), 'done')?.label).toBe('success')
    expect(edgeFrom(rfEdges, groupFooterId('g1'), 'oops')?.label).toBe('failure')
    expect(edgeFrom(rfEdges, groupFooterId('g2'), 'b2')?.label).toBe('success')
    expect(rfEdges.some((e) => e.source === 'g1' || e.target === 'g1')).toBe(false)
  })

  it('adds presentation-only sink connectors from every branch body sink into its footer', () => {
    const { rfEdges } = layout(branchNodes, branchEdges, branchGraph)
    const sinks = rfEdges.filter((e) => (e.data as { kind?: string } | undefined)?.kind === 'sink')
    expect(sinks.map((e) => `${e.source}->${e.target}`).sort()).toEqual(
      [
        `a2->${groupFooterId('g1')}`,
        `b2->${groupFooterId('g1')}`,
        `b3->${groupFooterId('g1')}`,
        `x1->${groupFooterId('g2')}`,
        `y2->${groupFooterId('g2')}`,
      ].sort(),
    )
    for (const e of sinks) {
      expect((e.data as { presentationOnly?: boolean }).presentationOnly).toBe(true)
      expect(e.selectable).toBe(false)
      expect((e.style as { strokeDasharray?: string }).strokeDasharray).toBe('4 4')
    }
  })

  it('never adds a connector to the manifest edges it was given', () => {
    const edges = branchEdges.map((e) => ({ ...e }))
    const snapshot = JSON.parse(JSON.stringify(edges))
    layout(branchNodes, edges, branchGraph)
    expect(edges).toEqual(snapshot)
  })

  it('draws a container box per group, the nested one inside the outer one and above it', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const outer = byId.get(groupBoxId('g1'))!
    const inner = byId.get(groupBoxId('g2'))!
    expect(outer.type).toBe('groupContainer')
    expect(outer.selectable).toBe(false)
    expect(outer.draggable).toBe(false)
    expect(contains(rectOf(outer), rectOf(inner))).toBe(true)
    expect(inner.zIndex!).toBeGreaterThan(outer.zIndex!)
    expect((outer.data as { label: string }).label).toBe('Parallel branches · all succeed')
  })

  it('keeps every body inside its own box with the header on top and the footer at the bottom', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const box = rectOf(byId.get(groupBoxId('g1')))
    for (const id of ['a1', 'a2', 'b2', 'b3', groupHeaderId('g1'), groupFooterId('g1')]) {
      expect(contains(box, rectOf(byId.get(id)))).toBe(true)
    }
    const header = byId.get(groupHeaderId('g1'))!.position.y
    const footer = byId.get(groupFooterId('g1'))!.position.y
    for (const id of ['a1', 'a2', 'b2', 'b3', 'x1', 'y1', 'y2']) {
      expect(byId.get(id)!.position.y).toBeGreaterThan(header)
      expect(byId.get(id)!.position.y).toBeLessThan(footer)
    }
    const inner = rectOf(byId.get(groupBoxId('g2')))
    for (const id of ['x1', 'y1', 'y2']) expect(contains(inner, rectOf(byId.get(id)))).toBe(true)
    // Nodes outside the group stay outside its box.
    for (const id of ['start', 'done', 'oops']) expect(overlaps(box, rectOf(byId.get(id)))).toBe(false)
  })

  it('lays out sibling task bodies without overlap at both levels', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const bodyA = union(['a1', 'a2'].map((id) => rectOf(byId.get(id))))
    const bodyB = union([rectOf(byId.get(groupBoxId('g2'))), ...['b2', 'b3'].map((id) => rectOf(byId.get(id)))])
    expect(overlaps(bodyA, bodyB)).toBe(false)
    const bodyX = rectOf(byId.get('x1'))
    const bodyY = union(['y1', 'y2'].map((id) => rectOf(byId.get(id))))
    expect(overlaps(bodyX, bodyY)).toBe(false)
  })

  it('pads the box by GROUP_PAD around its members', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const box = rectOf(byId.get(groupBoxId('g2')))
    const members = union([groupHeaderId('g2'), groupFooterId('g2'), 'x1', 'y1', 'y2'].map((id) => rectOf(byId.get(id))))
    expect(box.x).toBe(members.x - GROUP_PAD)
    expect(box.y).toBe(members.y - GROUP_PAD)
    expect(box.width).toBe(members.width + GROUP_PAD * 2)
    expect(box.height).toBe(members.height + GROUP_PAD * 2)
  })
})

// ---------------------------------------------------------------------------
// useGraphLayout with groups — workflows form nested in branches
// ---------------------------------------------------------------------------

describe('useGraphLayout — workflows group nested inside a branches group', () => {
  it('renders ordered task cards inside the workflows box, linked header -> card -> footer', () => {
    const { byId, rfEdges } = layout(mixedNodes, mixedEdges, mixedGraph)
    const cards = ['t1', 't2', 't3'].map((k) => byId.get(groupTaskCardId('w1', k))!)
    for (const c of cards) expect(c.type).toBe('groupTaskCard')
    expect(cards.map((c) => (c.data as { workflow: string }).workflow)).toEqual(['child-a', 'child-b', 'child-a'])
    expect(cards.map((c) => (c.data as { index: number }).index)).toEqual([0, 1, 2])
    // Author order reads left to right.
    expect(cards[0].position.x).toBeLessThan(cards[1].position.x)
    expect(cards[1].position.x).toBeLessThan(cards[2].position.x)
    const box = rectOf(byId.get(groupBoxId('w1')))
    for (const c of cards) expect(contains(box, rectOf(c, 96))).toBe(true)
    for (const k of ['t1', 't2', 't3']) {
      const fan = edgeFrom(rfEdges, groupHeaderId('w1'), groupTaskCardId('w1', k))!
      const gather = edgeFrom(rfEdges, groupTaskCardId('w1', k), groupFooterId('w1'))!
      expect((fan.data as { presentationOnly: boolean }).presentationOnly).toBe(true)
      expect((gather.data as { presentationOnly: boolean }).presentationOnly).toBe(true)
    }
  })

  it('nests the workflows box inside the branches box and wires its ports from the footer', () => {
    const { byId, rfEdges } = layout(mixedNodes, mixedEdges, mixedGraph)
    expect(contains(rectOf(byId.get(groupBoxId('g1'))), rectOf(byId.get(groupBoxId('w1'))))).toBe(true)
    expect(edgeFrom(rfEdges, groupHeaderId('g1'), groupHeaderId('w1'))?.label).toBe('b')
    expect(edgeFrom(rfEdges, groupFooterId('w1'), 'b2')?.label).toBe('success')
    // A workflows group has no body sinks of its own.
    expect(rfEdges.some((e) => (e.data as { kind?: string } | undefined)?.kind === 'sink' && e.target === groupFooterId('w1'))).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Stored positions
// ---------------------------------------------------------------------------

describe('useGraphLayout — groups with stored positions', () => {
  const positioned = mixedNodes.map((n, i) => ({ ...n, position: { x: 0, y: i * 200 } }))
  const at = (id: string, x: number, y: number) => positioned.find((n) => n.id === id)!.position = { x, y }
  at('start', 0, 0)
  at('g1', 0, 150)
  at('a1', -300, 330)
  at('w1', 300, 330)
  at('b2', 200, 800)
  at('b3', 450, 800)
  at('done', 0, 1100)
  at('oops', 300, 1100)

  it('keeps saved positions and puts the header at the group node', () => {
    const { byId } = layout(positioned, mixedEdges, mixedGraph)
    expect(byId.get('a1')!.position).toEqual({ x: -300, y: 330 })
    expect(byId.get(groupHeaderId('g1'))!.position).toEqual({ x: 0, y: 150 })
    expect(byId.get(groupHeaderId('w1'))!.position).toEqual({ x: 300, y: 330 })
  })

  it('places task cards in author order under the header and footers below their content', () => {
    const { byId } = layout(positioned, mixedEdges, mixedGraph)
    const xs = ['t1', 't2', 't3'].map((k) => byId.get(groupTaskCardId('w1', k))!.position.x)
    expect(xs[0]).toBeLessThan(xs[1])
    expect(xs[1]).toBeLessThan(xs[2])
    const cardBottom = byId.get(groupTaskCardId('w1', 't1'))!.position.y + 96
    expect(byId.get(groupFooterId('w1'))!.position.y).toBeGreaterThan(cardBottom)
    // The outer footer clears the lowest member of its body (b2/b3 at y=800).
    expect(byId.get(groupFooterId('g1'))!.position.y).toBeGreaterThan(880)
    expect(contains(rectOf(byId.get(groupBoxId('g1'))), rectOf(byId.get(groupBoxId('w1'))))).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Live overlays and owning-instance stamping
// ---------------------------------------------------------------------------

describe('useGraphLayout — live overlays', () => {
  const live: Record<string, GroupLiveSummary> = {
    w1: {
      instanceId: 'branch-b',
      groupRunId: 'run-w1',
      state: 'running',
      progress: { total: 3, completed: 1, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
      tasks: {
        t2: { key: 't2', status: 'running', frozenStatus: '', childInstanceId: 'child-t2', blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, winner: false, activeNodes: ['Build'] },
      },
    },
  }
  const contexts: Record<string, NodeContextInfo> = {
    a1: { instanceId: 'branch-a', independent: true, hiddenDependency: false },
    w1: { instanceId: 'branch-b', independent: false, hiddenDependency: false },
  }

  it('hands each header/footer its group summary and each card its task overlay', () => {
    const { byId } = layout(mixedNodes, mixedEdges, mixedGraph, { groupLive: live, nodeContexts: contexts })
    const footer = byId.get(groupFooterId('w1'))!.data as { live: GroupLiveSummary | null; instanceId: string | null }
    expect(footer.live?.groupRunId).toBe('run-w1')
    expect(footer.instanceId).toBe('branch-b')
    const card = byId.get(groupTaskCardId('w1', 't2'))!.data as { live: { childInstanceId: string } | null }
    expect(card.live?.childInstanceId).toBe('child-t2')
    expect((byId.get(groupTaskCardId('w1', 't1'))!.data as { live: unknown }).live).toBeNull()
    expect((byId.get(groupHeaderId('g1'))!.data as { live: unknown }).live).toBeNull()
  })

  it('stamps the owning instance and independence onto step nodes', () => {
    const { byId } = layout(mixedNodes, mixedEdges, mixedGraph, { groupLive: live, nodeContexts: contexts })
    expect(byId.get('a1')!.data).toMatchObject({ instanceId: 'branch-a', independent: true, hiddenDependency: false })
    expect(byId.get('start')!.data).not.toHaveProperty('instanceId')
  })
})

// ---------------------------------------------------------------------------
// Regression: graphs without groups and existing loop layouts are unaffected
// ---------------------------------------------------------------------------

describe('useGraphLayout — existing loop layouts unaffected', () => {
  const loopNodes = [node('a'), node('b'), node('c'), node('d')]
  const loopEdges: WorkflowEdge[] = [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'd' },
    { source: 'c', target: 'b', label: 'tests pass' },
  ]

  it('produces identical nodes and edges for a loop graph with an empty or null group layout', () => {
    const plain = layout(loopNodes, loopEdges, null)
    const empty = layout(loopNodes, loopEdges, EMPTY_GRAPH)
    const unrelated = layout(loopNodes, loopEdges, branchGraph) // groups not in this graph
    expect(empty.rfNodes).toEqual(plain.rfNodes)
    expect(empty.rfEdges).toEqual(plain.rfEdges)
    expect(unrelated.rfNodes).toEqual(plain.rfNodes)
    expect(unrelated.rfEdges).toEqual(plain.rfEdges)
    expect(plain.rfNodes.some((n) => n.type?.startsWith('group'))).toBe(false)
  })

  it('keeps a loop inside a branch body inside that group box', () => {
    const nodes = [node('start'), node('g', 'parallel_group'), node('l1'), node('l2'), node('l3'), node('k1'), node('done'), node('oops')]
    const edges: WorkflowEdge[] = [
      { source: 'start', target: 'g' },
      { source: 'g', target: 'l1', output: 'task', label: 'loop' },
      { source: 'g', target: 'k1', output: 'task', label: 'plain' },
      { source: 'l1', target: 'l2' },
      { source: 'l2', target: 'l1', label: 'again' },
      { source: 'l2', target: 'l3' },
      { source: 'g', target: 'done', output: 'success' },
      { source: 'g', target: 'oops', output: 'failure' },
    ]
    const graph: GroupGraph = {
      groups: {
        g: scope({
          nodeId: 'g',
          config: { form: 'branches', tasks: [{ key: 'loop', entry: 'l1' }, { key: 'plain', entry: 'k1' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' },
          bodies: { loop: ['l1', 'l2', 'l3'], plain: ['k1'] },
          successTarget: 'done',
          failureTarget: 'oops',
        }),
      },
      owner: { l1: { groupNodeId: 'g', taskKey: 'loop' }, l2: { groupNodeId: 'g', taskKey: 'loop' }, l3: { groupNodeId: 'g', taskKey: 'loop' }, k1: { groupNodeId: 'g', taskKey: 'plain' } },
      order: ['g'],
    }
    const { byId, rfEdges } = layout(nodes, edges, graph)
    const loopBox = [...byId.values()].find((n) => n.type === 'loopContainer')!
    expect(loopBox).toBeDefined()
    expect(contains(rectOf(byId.get(groupBoxId('g'))), rectOf(loopBox))).toBe(true)
    expect(rfEdges.find((e) => e.source === 'l2' && e.target === 'l1')?.type).toBe('loopBackEdge')
    expect(loopBox.zIndex!).toBeGreaterThan(byId.get(groupBoxId('g'))!.zIndex!)
  })

  it('keeps rfNodes/rfEdges referentially stable across re-renders with groups', () => {
    const { result, rerender } = renderHook(() => useGraphLayout({ nodes: branchNodes, edges: branchEdges, groups: branchGraph }))
    const first = result.current
    rerender()
    expect(result.current.rfNodes).toBe(first.rfNodes)
    expect(result.current.rfEdges).toBe(first.rfEdges)
  })
})

// ---------------------------------------------------------------------------
// toDefinitionPositions over a real layout (saveLayout mapping)
// ---------------------------------------------------------------------------

describe('toDefinitionPositions over a laid-out group graph', () => {
  it('saves a header as its group node and drops footers, cards and boxes', () => {
    const { rfNodes } = layout(mixedNodes, mixedEdges, mixedGraph)
    const out = toDefinitionPositions(rfNodes)
    expect(Object.keys(out).sort()).toEqual(['a1', 'b2', 'b3', 'done', 'g1', 'oops', 'start', 'w1'])
    const header = rfNodes.find((n) => n.id === groupHeaderId('w1'))!
    expect(out.w1).toEqual(header.position)
  })

})
