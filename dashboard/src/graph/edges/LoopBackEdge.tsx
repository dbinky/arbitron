import { memo } from 'react'
import { BaseEdge, EdgeLabelRenderer, type EdgeProps } from '@xyflow/react'
import type { Loop } from '../loops'
import type { Box } from '../loopLayout'
import { chooseSide, buildLoopBackPath } from '../loopEdgeRouting'

interface LoopBackEdgeData {
  loop: Loop
  box?: Box
  /** Side chosen at layout time; falls back to chooseSide when absent. */
  side?: 'left' | 'right'
  /** Gutter x chosen at layout time; falls back to chooseSide when absent. */
  gutterX?: number
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

  // useGraphLayout normally picks the side and gutter (so the node handles match);
  // standalone renders (e.g. unit tests with no node graph) fall back to chooseSide.
  const fallback = chooseSide({
    sourceX,
    targetX,
    boxX: box.x,
    boxWidth: box.width,
  })
  const side = data?.side ?? fallback.side
  const gutterX = data?.gutterX ?? fallback.gutterX

  const { path, labelX, labelY } = buildLoopBackPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    side,
    gutterX,
  })

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
