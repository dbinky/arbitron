import type { WorkflowEdge } from './types'

export interface Loop {
  id: string
  headId: string
  tailId: string
  bodyNodeIds: string[]
  backEdge: WorkflowEdge
  depth: number
}

export interface LoopDetection {
  loops: Loop[]
  backEdgeKeys: Set<string>
}

export function backEdgeKey(source: string, target: string): string {
  return `${source} ${target}`
}

/** Forward adjacency, preserving edge order; includes self-edges. */
function buildAdjacency(edges: WorkflowEdge[]): Map<string, string[]> {
  const adj = new Map<string, string[]>()
  for (const e of edges) {
    if (!adj.has(e.source)) adj.set(e.source, [])
    adj.get(e.source)!.push(e.target)
    if (!adj.has(e.target)) adj.set(e.target, [])
  }
  return adj
}

/** Reverse adjacency for ancestor queries. */
function buildReverse(edges: WorkflowEdge[]): Map<string, string[]> {
  const rev = new Map<string, string[]>()
  for (const e of edges) {
    if (!rev.has(e.target)) rev.set(e.target, [])
    rev.get(e.target)!.push(e.source)
    if (!rev.has(e.source)) rev.set(e.source, [])
  }
  return rev
}

/** Iterative DFS classifying back-edges via an on-stack set. */
function findBackEdges(edges: WorkflowEdge[]): WorkflowEdge[] {
  const adj = buildAdjacency(edges)
  // Map each (source,target) pair to the edge object so we can recover labels.
  const edgeByPair = new Map<string, WorkflowEdge>()
  for (const e of edges) edgeByPair.set(backEdgeKey(e.source, e.target), e)

  const visited = new Set<string>()
  const onStack = new Set<string>()
  const backEdges: WorkflowEdge[] = []
  const seenBack = new Set<string>()

  // Roots in source order for deterministic traversal.
  const roots: string[] = []
  for (const e of edges) if (!roots.includes(e.source)) roots.push(e.source)
  for (const n of adj.keys()) if (!roots.includes(n)) roots.push(n)

  for (const root of roots) {
    if (visited.has(root)) continue
    // Explicit stack of (node, childIndex).
    const stack: Array<{ node: string; i: number }> = [{ node: root, i: 0 }]
    visited.add(root)
    onStack.add(root)
    while (stack.length > 0) {
      const frame = stack[stack.length - 1]
      const children = adj.get(frame.node) ?? []
      if (frame.i < children.length) {
        const child = children[frame.i]
        frame.i++
        const key = backEdgeKey(frame.node, child)
        if (frame.node === child || onStack.has(child)) {
          if (!seenBack.has(key)) {
            seenBack.add(key)
            backEdges.push(edgeByPair.get(key)!)
          }
        } else if (!visited.has(child)) {
          visited.add(child)
          onStack.add(child)
          stack.push({ node: child, i: 0 })
        }
      } else {
        onStack.delete(frame.node)
        stack.pop()
      }
    }
  }
  return backEdges
}

/** Forward-reachable set from `start` (inclusive). */
function reachable(start: string, adj: Map<string, string[]>): Set<string> {
  const seen = new Set<string>([start])
  const queue = [start]
  while (queue.length > 0) {
    const n = queue.shift()!
    for (const next of adj.get(n) ?? []) {
      if (!seen.has(next)) {
        seen.add(next)
        queue.push(next)
      }
    }
  }
  return seen
}

export function detectLoops(edges: WorkflowEdge[]): LoopDetection {
  const backEdges = findBackEdges(edges)
  const backEdgeKeys = new Set<string>(
    backEdges.map((e) => backEdgeKey(e.source, e.target)),
  )
  if (backEdges.length === 0) {
    return { loops: [], backEdgeKeys }
  }

  // Body paths follow FORWARD edges only: a back-edge is the path that CLOSES a
  // cycle, not a path through the body. Including back-edges here would let one
  // loop's body leak into another (e.g. via a shared head), so we strip every
  // detected back-edge before computing descendants/ancestors.
  const forwardEdges = edges.filter(
    (e) => !backEdgeKeys.has(backEdgeKey(e.source, e.target)),
  )
  const adj = buildAdjacency(forwardEdges)
  const rev = buildReverse(forwardEdges)

  const loops: Loop[] = backEdges.map((be) => {
    const head = be.target
    const tail = be.source
    let bodyNodeIds: string[]
    if (head === tail) {
      bodyNodeIds = [head]
    } else {
      const descendants = reachable(head, adj)
      const ancestors = reachable(tail, rev)
      const set = new Set<string>([head, tail])
      for (const n of descendants) if (ancestors.has(n)) set.add(n)
      bodyNodeIds = [...set]
    }
    return {
      id: `loop-${head}-${tail}`,
      headId: head,
      tailId: tail,
      bodyNodeIds,
      backEdge: be,
      depth: 0,
    }
  })

  // depth = number of OTHER loops strictly containing this body.
  for (const loop of loops) {
    const mine = new Set(loop.bodyNodeIds)
    let depth = 0
    for (const other of loops) {
      if (other === loop) continue
      const theirs = new Set(other.bodyNodeIds)
      if (theirs.size > mine.size && [...mine].every((n) => theirs.has(n))) {
        depth++
      }
    }
    loop.depth = depth
  }

  // Deterministic order: smaller bodies first, then head, then tail.
  loops.sort(
    (a, b) =>
      a.bodyNodeIds.length - b.bodyNodeIds.length ||
      a.headId.localeCompare(b.headId) ||
      a.tailId.localeCompare(b.tailId),
  )

  return { loops, backEdgeKeys }
}
