import { describe, it, expect } from 'vitest'
import type { WorkflowEdge } from './types'
import { detectLoops, backEdgeKey } from './loops'

/** Helper: sort body ids for order-independent assertions. */
const body = (loop: { bodyNodeIds: string[] }) => [...loop.bodyNodeIds].sort()

describe('detectLoops — happy path: linear graph (no loops)', () => {
  it('returns no loops and no back-edges for a straight a→b→c chain', () => {
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ]
    const result = detectLoops(edges)
    expect(result.loops).toEqual([])
    expect(result.backEdgeKeys.size).toBe(0)
  })
})

describe('detectLoops — single loop', () => {
  it('detects one loop with the back-edge as head/tail and the full body', () => {
    // a → b → c → d, with c → b looping back.
    const back: WorkflowEdge = { source: 'c', target: 'b', label: 'tests pass' }
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'c', target: 'd' },
      back,
    ]
    const result = detectLoops(edges)
    expect(result.loops).toHaveLength(1)
    const loop = result.loops[0]
    expect(loop.headId).toBe('b')
    expect(loop.tailId).toBe('c')
    expect(body(loop)).toEqual(['b', 'c'])
    expect(loop.backEdge).toBe(back)
    expect(loop.depth).toBe(0)
    expect(loop.id).toBe('loop-b-c')
    expect(result.backEdgeKeys.has(backEdgeKey('c', 'b'))).toBe(true)
  })
})

describe('detectLoops — edge case: self-loop', () => {
  it('treats a node that edges to itself as a single-node loop', () => {
    const back: WorkflowEdge = { source: 'b', target: 'b' }
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      back,
      { source: 'b', target: 'c' },
    ]
    const result = detectLoops(edges)
    expect(result.loops).toHaveLength(1)
    const loop = result.loops[0]
    expect(loop.headId).toBe('b')
    expect(loop.tailId).toBe('b')
    expect(body(loop)).toEqual(['b'])
    expect(result.backEdgeKeys.has(backEdgeKey('b', 'b'))).toBe(true)
  })
})

describe('detectLoops — boundary: nested loops', () => {
  it('nests inner loop body inside outer and assigns depth', () => {
    // Outer: a → b → c → d → e, back e→b.
    // Inner: c → d, back d→c (inside the outer body).
    const outerBack: WorkflowEdge = { source: 'e', target: 'b', label: 'done' }
    const innerBack: WorkflowEdge = { source: 'd', target: 'c', label: 'refined' }
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'c', target: 'd' },
      { source: 'd', target: 'e' },
      outerBack,
      innerBack,
    ]
    const result = detectLoops(edges)
    expect(result.loops).toHaveLength(2)
    const inner = result.loops.find((l) => l.headId === 'c' && l.tailId === 'd')!
    const outer = result.loops.find((l) => l.headId === 'b' && l.tailId === 'e')!
    expect(body(inner)).toEqual(['c', 'd'])
    expect(body(outer)).toEqual(['b', 'c', 'd', 'e'])
    // Outer strictly contains inner → inner depth 1, outer depth 0.
    expect(inner.depth).toBe(1)
    expect(outer.depth).toBe(0)
  })
})

describe('detectLoops — interaction: multiple loops sharing a head', () => {
  it('emits one loop per back-edge when two tails return to the same head', () => {
    // a → b → c → d; both c→b and d→b loop back to b.
    // l1 body=[b,c], l2 body=[b,c,d]; l1 ⊂ l2 so l1.depth=1, l2.depth=0.
    const back1: WorkflowEdge = { source: 'c', target: 'b' }
    const back2: WorkflowEdge = { source: 'd', target: 'b' }
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'c', target: 'd' },
      back1,
      back2,
    ]
    const result = detectLoops(edges)
    expect(result.loops).toHaveLength(2)
    const l1 = result.loops.find((l) => l.tailId === 'c')!
    const l2 = result.loops.find((l) => l.tailId === 'd')!
    expect(l1.headId).toBe('b')
    expect(l2.headId).toBe('b')
    expect(body(l1)).toEqual(['b', 'c'])
    expect(body(l2)).toEqual(['b', 'c', 'd'])
    // Depth: l1 is strictly contained in l2 → l1.depth=1; l2 is not contained in anything → l2.depth=0.
    expect(l1.depth).toBe(1)
    expect(l2.depth).toBe(0)
  })
})

describe('detectLoops — edge case: sibling loops with disjoint bodies', () => {
  it('assigns depth 0 to both loops when their bodies do not overlap', () => {
    // a → b → c → b (loop 1: head=b, body=[b,c])
    // a → d → e → d (loop 2: head=d, body=[d,e])
    // The two loops are siblings in sequence; neither contains the other.
    const back1: WorkflowEdge = { source: 'c', target: 'b', label: 'retry' }
    const back2: WorkflowEdge = { source: 'e', target: 'd', label: 'refine' }
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      back1,
      { source: 'c', target: 'd' },
      { source: 'd', target: 'e' },
      back2,
    ]
    const result = detectLoops(edges)
    expect(result.loops).toHaveLength(2)
    const lb = result.loops.find((l) => l.headId === 'b')!
    const ld = result.loops.find((l) => l.headId === 'd')!
    expect(body(lb)).toEqual(['b', 'c'])
    expect(body(ld)).toEqual(['d', 'e'])
    expect(lb.depth).toBe(0)
    expect(ld.depth).toBe(0)
    expect(result.backEdgeKeys.has(backEdgeKey('c', 'b'))).toBe(true)
    expect(result.backEdgeKeys.has(backEdgeKey('e', 'd'))).toBe(true)
  })
})

describe('detectLoops — interaction: diamond + loop', () => {
  it('includes both diamond branches in the loop body', () => {
    // a → b; b → c; b → d; c → e; d → e; e → b loops back. Diamond b..e.
    const back: WorkflowEdge = { source: 'e', target: 'b' }
    const edges: WorkflowEdge[] = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'b', target: 'd' },
      { source: 'c', target: 'e' },
      { source: 'd', target: 'e' },
      back,
    ]
    const result = detectLoops(edges)
    expect(result.loops).toHaveLength(1)
    expect(body(result.loops[0])).toEqual(['b', 'c', 'd', 'e'])
  })
})

describe('detectLoops — error/invalid input', () => {
  it('returns empty result for an empty edge list', () => {
    const result = detectLoops([])
    expect(result.loops).toEqual([])
    expect(result.backEdgeKeys.size).toBe(0)
  })

  it('ignores edges referencing nodes with no other connections', () => {
    // Dangling forward edges, no cycle.
    const edges: WorkflowEdge[] = [
      { source: 'x', target: 'y' },
      { source: 'y', target: 'z' },
    ]
    expect(detectLoops(edges).loops).toEqual([])
  })
})

describe('backEdgeKey', () => {
  it('produces a stable, collision-resistant key', () => {
    expect(backEdgeKey('a', 'b')).toBe('a b')
    expect(backEdgeKey('a', 'b')).not.toBe(backEdgeKey('ab', ''))
  })
})
