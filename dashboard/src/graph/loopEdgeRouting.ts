import { getBezierPath, Position } from '@xyflow/react'

/**
 * Shared routing logic for loop-back edges. Lives outside the LoopBackEdge
 * component so the layout hook (which picks the side and wires the node handles)
 * and the edge component (which draws the curve) share one implementation.
 */

export const GUTTER_OFFSET = 60

export interface SideRoute {
  side: 'left' | 'right'
  /** Absolute x of the gutter the curve bows out to. */
  gutterX: number
}

/**
 * Choose the gutter side based on the loop body bounding box relative to the
 * edge endpoints. Bow out on the side of the body box that has more room,
 * defaulting to 'right' on a tie. This keeps the curve outside the body box.
 */
export function chooseSide(args: {
  sourceX: number
  targetX: number
  boxX: number
  boxWidth: number
}): SideRoute {
  const center = args.boxX + args.boxWidth / 2
  const endpoints = (args.sourceX + args.targetX) / 2
  // Endpoints left-of or AT center bow out right (default-right on a tie);
  // endpoints right-of center bow out left. Keeps the curve outside the box.
  if (endpoints <= center) {
    return { side: 'right', gutterX: args.boxX + args.boxWidth + GUTTER_OFFSET }
  }
  return { side: 'left', gutterX: args.boxX - GUTTER_OFFSET }
}

/**
 * Map a chosen side to the ArbitronNode side-handle ids a loop-back edge should
 * attach to: it leaves the tail node's source handle on that side and enters the
 * head node's target handle on the same side.
 */
export function loopHandleIds(side: 'left' | 'right'): {
  sourceHandle: string
  targetHandle: string
} {
  return side === 'right'
    ? { sourceHandle: 'loop-s-right', targetHandle: 'loop-t-right' }
    : { sourceHandle: 'loop-s-left', targetHandle: 'loop-t-left' }
}

/**
 * Build the loop-back curve: it leaves the tail node's chosen side, bows out to
 * the gutter, and re-enters the head node on the same side. Both node-ends are
 * horizontal (the side), so the edge reads as a side-to-side return rather than
 * diving through the top-to-bottom forward lanes.
 */
export function buildLoopBackPath(args: {
  sourceX: number
  sourceY: number
  targetX: number
  targetY: number
  side: 'left' | 'right'
  gutterX: number
}): { path: string; labelX: number; labelY: number } {
  const sidePos = args.side === 'right' ? Position.Right : Position.Left
  const midY = (args.sourceY + args.targetY) / 2

  // Segment 1: from the tail's side out to the gutter midpoint.
  const [outPath] = getBezierPath({
    sourceX: args.sourceX,
    sourceY: args.sourceY,
    sourcePosition: sidePos,
    targetX: args.gutterX,
    targetY: midY,
    targetPosition: sidePos,
    curvature: 0.6,
  })
  // Segment 2: from the gutter midpoint back to the head's side.
  const [inPath, labelX, labelY] = getBezierPath({
    sourceX: args.gutterX,
    sourceY: midY,
    sourcePosition: sidePos,
    targetX: args.targetX,
    targetY: args.targetY,
    targetPosition: sidePos,
    curvature: 0.6,
  })

  return { path: `${outPath} ${inPath}`, labelX, labelY }
}
