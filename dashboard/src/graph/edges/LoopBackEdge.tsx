import { memo } from 'react'
import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  Position,
  type EdgeProps,
} from '@xyflow/react'
import type { Loop } from '../loops'
import type { Box } from '../loopLayout'

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

interface LoopBackEdgeData {
  loop: Loop
  box?: Box
  [key: string]: unknown
}

function LoopBackEdge(props: EdgeProps) {
  const { id, sourceX, sourceY, targetX, targetY, markerEnd, style } = props
  const data = props.data as LoopBackEdgeData | undefined

  // Fall back to a box derived from the endpoints when none is supplied.
  const box: Box =
    data?.box ?? {
      x: Math.min(sourceX, targetX) - 20,
      y: Math.min(sourceY, targetY),
      width: Math.abs(sourceX - targetX) + 40,
      height: Math.abs(sourceY - targetY),
    }

  const { gutterX, side } = chooseSide({
    sourceX,
    targetX,
    boxX: box.x,
    boxWidth: box.width,
  })

  const sidePos = side === 'right' ? Position.Right : Position.Left
  const midY = (sourceY + targetY) / 2

  // Segment 1: from the tail (source, bottom) out to the gutter midpoint.
  const [outPath] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition: Position.Bottom,
    targetX: gutterX,
    targetY: midY,
    targetPosition: sidePos,
    curvature: 0.6,
  })
  // Segment 2: from the gutter midpoint back to the head (target, top).
  const [inPath, labelX, labelY] = getBezierPath({
    sourceX: gutterX,
    sourceY: midY,
    sourcePosition: sidePos,
    targetX,
    targetY,
    targetPosition: Position.Top,
    curvature: 0.6,
  })

  const path = `${outPath} ${inPath}`
  const label =
    typeof data?.loop.backEdge.label === 'string'
      ? data.loop.backEdge.label.trim()
      : ''

  const edgeStyle: React.CSSProperties = {
    stroke: '#7c8cf5',
    strokeWidth: 2,
    strokeDasharray: '6 4',
    fill: 'none',
    ...style,
  }

  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} style={edgeStyle} />
      {label.length > 0 && (
        <EdgeLabelRenderer>
          <div
            style={{
              position: 'absolute',
              transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
              background: 'var(--bg-base, #14142b)',
              color: '#7c8cf5',
              fontSize: 11,
              padding: '0 4px',
              pointerEvents: 'none',
            }}
          >
            {label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

export default memo(LoopBackEdge)
