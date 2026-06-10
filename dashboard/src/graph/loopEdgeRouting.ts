/**
 * Routing logic for loop-back edges. Kept out of the LoopBackEdge component so
 * the geometry is pure and unit-testable.
 *
 * Approach: floating geometry. The nodes carry ONLY their default top/bottom
 * handles (so forward edges route cleanly bottom→top). A loop-back edge receives
 * those default handle coords from React Flow — source = the tail's bottom-center,
 * target = the head's top-center — and derives its own SIDE attachment points and
 * the channel it bows out to. No extra node handles, so forward edges are never
 * pulled to a side.
 */

export const GUTTER_OFFSET = 60

export interface SideRoute {
  side: 'left' | 'right'
  /** Absolute x of the gutter the curve bows out to. */
  gutterX: number
}

/**
 * Choose the side based on the loop body bounding box relative to the edge
 * endpoints. Bow out on the side of the body box that has more room, defaulting
 * to 'right' on a tie. Keeps the curve outside the body box.
 */
export function chooseSide(args: {
  sourceX: number
  targetX: number
  boxX: number
  boxWidth: number
}): SideRoute {
  const center = args.boxX + args.boxWidth / 2
  const endpoints = (args.sourceX + args.targetX) / 2
  if (endpoints <= center) {
    return { side: 'right', gutterX: args.boxX + args.boxWidth + GUTTER_OFFSET }
  }
  return { side: 'left', gutterX: args.boxX - GUTTER_OFFSET }
}

/**
 * Choose which side a loop-back edge should route on by looking at where the
 * loop's body nodes actually sit. The edge connects two spine nodes (tail/head);
 * the intermediate body nodes typically bulge to one side. Route on the side the
 * body reaches LESS into (the open side) so the edge doesn't cut through them.
 * Ties default to right. This is more reliable than the loop-body bounding box,
 * which is widened by the very nodes we need to avoid.
 */
export function chooseLoopSide(args: {
  spineX: number
  bodyLefts: number[]
  bodyRights: number[]
}): 'left' | 'right' {
  const leftReach = args.spineX - Math.min(...args.bodyLefts)
  const rightReach = Math.max(...args.bodyRights) - args.spineX
  return rightReach <= leftReach ? 'right' : 'left'
}

export interface LoopBackGeometry {
  /** Tail (source) attachment: vertical center of the chosen side. */
  tailX: number
  tailY: number
  /** Head (target) attachment: vertical center of the chosen side. */
  headX: number
  headY: number
  /** x of the channel the curve bows out to, clear of the node column. */
  channelX: number
}

/**
 * Derive the side attachment points and bow channel from React Flow's default
 * handle coords. `sourceX/sourceY` is the tail's bottom-center; `targetX/targetY`
 * is the head's top-center. Both attachment points sit at the vertical center of
 * the chosen side, and the channel sits one gutter beyond the outer side.
 */
export function loopBackGeometry(args: {
  sourceX: number
  sourceY: number
  targetX: number
  targetY: number
  side: 'left' | 'right'
  nodeWidth: number
  nodeHeight: number
}): LoopBackGeometry {
  const dir = args.side === 'right' ? 1 : -1
  const tailX = args.sourceX + dir * (args.nodeWidth / 2)
  const tailY = args.sourceY - args.nodeHeight / 2 // bottom-center → side-center
  const headX = args.targetX + dir * (args.nodeWidth / 2)
  const headY = args.targetY + args.nodeHeight / 2 // top-center → side-center
  const outerX = args.side === 'right' ? Math.max(tailX, headX) : Math.min(tailX, headX)
  const channelX = outerX + dir * GUTTER_OFFSET
  return { tailX, tailY, headX, headY, channelX }
}

/**
 * Build the out-and-back return curve: it leaves the tail's side horizontally,
 * bows out through the channel (clear of the node column), and re-enters the
 * head on the same side. A single cubic whose control points sit out at the
 * channel — so it visibly "goes out" rather than running flush up the column.
 */
export function buildLoopBackPath(g: LoopBackGeometry): {
  path: string
  labelX: number
  labelY: number
} {
  const path = `M ${g.tailX},${g.tailY} C ${g.channelX},${g.tailY} ${g.channelX},${g.headY} ${g.headX},${g.headY}`
  return { path, labelX: g.channelX, labelY: (g.tailY + g.headY) / 2 }
}
