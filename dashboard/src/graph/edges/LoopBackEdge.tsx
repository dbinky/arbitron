import { memo } from 'react'
import { BaseEdge, EdgeLabelRenderer, type EdgeProps } from '@xyflow/react'
import type { Loop } from '../loops'
import type { Box } from '../loopLayout'
import { chooseSide, loopBackGeometry, buildLoopBackPath } from '../loopEdgeRouting'
import { NODE_WIDTH, NODE_HEIGHT } from '../useGraphLayout'

interface LoopBackEdgeData {
  loop: Loop
  box?: Box
  /** Side chosen at layout time from the body-node positions. */
  side?: 'left' | 'right'
  [key: string]: unknown
}

function LoopBackEdge(props: EdgeProps) {
  const { id, sourceX, sourceY, targetX, targetY, markerEnd, style } = props
  const data = props.data as LoopBackEdgeData | undefined

  // React Flow hands us the default handle coords: source = tail bottom-center,
  // target = head top-center (sourceX/targetX are therefore the node centers).
  // Fall back to an endpoint-derived box when the loop body box is absent.
  const box: Box =
    data?.box ?? {
      x: Math.min(sourceX, targetX) - 20,
      y: Math.min(sourceY, targetY),
      width: Math.abs(sourceX - targetX) + 40,
      height: Math.abs(sourceY - targetY),
    }

  // Prefer the side chosen at layout time from the body-node positions; fall back
  // to the box heuristic for standalone renders (e.g. unit tests with no layout).
  const side =
    data?.side ??
    chooseSide({ sourceX, targetX, boxX: box.x, boxWidth: box.width }).side

  const geometry = loopBackGeometry({
    sourceX,
    sourceY,
    targetX,
    targetY,
    side,
    nodeWidth: NODE_WIDTH,
    nodeHeight: NODE_HEIGHT,
  })
  const { path, labelX, labelY } = buildLoopBackPath(geometry)

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
