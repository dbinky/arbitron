import type { WorkflowNode } from '../types'
import { NODE_HEIGHT } from '../useGraphLayout'

type XY = { x: number; y: number }

/** Top-to-top pitch for unpositioned fallback nodes. */
export const FALLBACK_STACK_PITCH = 120

function isXY(p: XY | null | undefined): p is XY {
  return p != null && Number.isFinite(p.x) && Number.isFinite(p.y)
}

/** Retained → manifest → deterministic stack. Null asks the caller to use dagre. */
export function fallbackPositions(
  nodes: WorkflowNode[],
  retained: Record<string, XY> | undefined,
): Record<string, XY> | null {
  const out: Record<string, XY> = Object.create(null)
  const missing: WorkflowNode[] = []
  for (const n of nodes) {
    const r = retained?.[n.id]
    if (isXY(r)) out[n.id] = { ...r }
    else if (isXY(n.position)) out[n.id] = { ...n.position }
    else missing.push(n)
  }
  const placed = Object.values(out)
  if (!placed.length) return null
  const minX = Math.min(...placed.map(p => p.x))
  let y = Math.max(...placed.map(p => p.y + NODE_HEIGHT)) + FALLBACK_STACK_PITCH - NODE_HEIGHT
  for (const n of missing) {
    out[n.id] = { x: minX, y }
    y += FALLBACK_STACK_PITCH
  }
  return out
}
