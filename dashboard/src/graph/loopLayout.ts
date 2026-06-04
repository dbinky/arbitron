import type { Loop } from './loops'

export const CONTAINER_PAD = 24
/** Extra inset per nesting level so nested containers sit inside their parent. */
export const DEPTH_INSET = 10
const NODE_WIDTH = 200
const NODE_HEIGHT = 80

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

export function loopContainerLabel(loop: Loop): string {
  const raw = loop.backEdge.label
  const label = typeof raw === 'string' ? raw.trim() : ''
  return label.length > 0 ? `Loop until ${label}` : 'Loop'
}

/**
 * Bounding box around a loop's body nodes, padded and inset by nesting depth.
 * Returns null when no body node has a known position (nothing to wrap).
 */
export function loopContainerBox(
  loop: Loop,
  positionMap: Record<string, { x: number; y: number }>,
): Box | null {
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  let found = false
  for (const id of loop.bodyNodeIds) {
    const p = positionMap[id]
    if (!p) continue
    found = true
    minX = Math.min(minX, p.x)
    minY = Math.min(minY, p.y)
    maxX = Math.max(maxX, p.x + NODE_WIDTH)
    maxY = Math.max(maxY, p.y + NODE_HEIGHT)
  }
  if (!found) return null

  const inset = DEPTH_INSET * loop.depth
  return {
    x: minX - CONTAINER_PAD + inset,
    y: minY - CONTAINER_PAD + inset,
    width: maxX - minX + CONTAINER_PAD * 2 - inset * 2,
    height: maxY - minY + CONTAINER_PAD * 2 - inset * 2,
  }
}
