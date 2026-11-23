import { memo } from 'react'
import { EdgeLabelRenderer } from '@xyflow/react'
import type { Edge, EdgeProps } from '@xyflow/react'
import type { EdgeRoute, GroupConnector, Point, RendererContract } from './types'
import { renderedID } from './GeometryHandles'
import { buildRoutedPath } from './routedPath'
import styles from './RoutedEdge.module.css'

export interface RoutedGroupConnectorData {
  connector: GroupConnector
  gaps: Point[]
  overs: Point[]
  contract: RendererContract
  graphInstanceId: string
  active: boolean
  layoutInstanceId?: string
  [key: string]: unknown
}
export interface RoutedEdgeData {
  moveBadge?: string
  layoutInstanceId?: string
  route: EdgeRoute
  gaps: Point[]
  /** Crossings this route passes over; dashed routes restart their dash there. */
  overs: Point[]
  contract: RendererContract
  active: boolean
  graphInstanceId: string
  [key: string]: unknown
}

/** Class name the measure surface (P3T4) applies to measure edge labels exactly as they render. */
export const edgeLabelClassName = styles.edgeLabel

/** Text colours for edge labels (contrast-checked against the #1a1a2e label background). */
const FORWARD_LABEL_COLOR = '#e0e0e0'
const LOOP_LABEL_COLOR = '#9aa6ff'

type RoutedEdgeType = Edge<RoutedEdgeData | RoutedGroupConnectorData, 'routedEdge'>

/** First dash length of a CSS dash pattern such as "6 4"; 0 when undashed. */
function firstDash(pattern: string): number {
  const first = Number.parseFloat(pattern.trim().split(/[\s,]+/)[0] ?? '')
  return Number.isFinite(first) && first > 0 ? first : 0
}

/**
 * Draws a persisted route exactly. xyflow's sourceX/sourceY/targetX/targetY are
 * deliberately ignored: the reviewed geometry is the only source of the path.
 */
function RoutedEdge({ data, style }: EdgeProps<RoutedEdgeType>) {
  if (!data) return null
  const { gaps, overs, contract, active, graphInstanceId, layoutInstanceId } = data
  const connector = 'connector' in data ? (data as RoutedGroupConnectorData).connector : null
  const route = connector ? null : (data as RoutedEdgeData).route
  if (!graphInstanceId) throw new Error('graphInstanceId is required')
  const isLoop = route != null && route.kind !== 'forward'
  const color = isLoop ? contract.loopReturnColor : contract.forwardColor
  const pattern = connector ? contract.groupConnectorDash : isLoop ? contract.loopReturnDash : ''
  const dash = pattern ? { onPx: firstDash(pattern), overs } : undefined
  const d = buildRoutedPath((connector ?? route)!.points, gaps, contract.crossingGapPx, dash, { radiusPx: contract.cornerRadiusPx, overs, arrowPx: contract.arrowPx })
  const markerId = layoutInstanceId
    ? renderedID(graphInstanceId, JSON.stringify(['tile', layoutInstanceId, connector ? `arrow:${connector.id}` : `arrow:edge:${route!.ordinal}`]))
    : connector ? `${renderedID(graphInstanceId, connector.id)}-arranged-connector-arrow` : `${graphInstanceId}-arranged-arrow-${route!.ordinal}`
  const a = contract.arrowPx

  return (
    <>
      <defs>
        <marker
          id={markerId}
          markerWidth={a}
          markerHeight={a}
          viewBox={`0 0 ${a} ${a}`}
          refX={a}
          refY={a / 2}
          orient="auto"
          markerUnits="userSpaceOnUse"
        >
          <path d={`M 0,0 L ${a},${a / 2} L 0,${a} Z`} fill={color} />
        </marker>
      </defs>
      <path
        data-edge-path={route?.ordinal}
        data-group-connector={connector?.id}
        data-layout-instance={layoutInstanceId}
        data-edge-kind={route?.kind}
        d={d}
        markerEnd={`url(#${markerId})`}
        style={{
          fill: 'none',
          stroke: color,
          strokeWidth: contract.strokePx,
          strokeDasharray: pattern || undefined,
        }}
      />
      {active && (
        <path
          className={styles.activeOverlay}
          data-edge-overlay={route?.ordinal}
          data-layout-instance={layoutInstanceId}
          d={d}
          style={{ strokeWidth: Math.max(1, contract.strokePx / 2) }}
        />
      )}
      {route?.text && (
        <EdgeLabelRenderer>
          <div
            data-edge-label={route.ordinal}
            data-layout-instance={layoutInstanceId}
            className={styles.edgeLabel}
            style={{
              zIndex: style?.zIndex,
              transform: `translate(${route.text.box.x}px, ${route.text.box.y}px)`,
              width: route.text.box.w,
              height: route.text.box.h,
              padding: `${contract.edgeLabelPaddingY}px ${contract.edgeLabelPaddingX}px`,
              lineHeight: contract.lineHeight,
              color: isLoop ? LOOP_LABEL_COLOR : FORWARD_LABEL_COLOR,
            }}
          >
            {route.text.text}
          </div>
        </EdgeLabelRenderer>
      )}
      {route && typeof data.moveBadge === 'string' && route.points.length > 0 && (
        <EdgeLabelRenderer>
          <div data-move-badge={route.ordinal} data-layout-instance={layoutInstanceId}
            className={styles.moveBadge}
            style={{ zIndex: style?.zIndex,
              transform: `translate(-50%, -100%) translate(${route.points[route.points.length - 1].x}px, ${route.points[route.points.length - 1].y}px)`,
            }}>
            {data.moveBadge}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

export default memo(RoutedEdge)
