import { useCallback, useEffect, useState } from 'react'
import { useNodes, useReactFlow, useStoreApi } from '@xyflow/react'
import styles from './CalloutConnector.module.css'

interface CalloutConnectorProps {
  /** The graph node ID to draw the connector from. */
  nodeId: string
  /** Whether the connector should be visible. */
  visible: boolean
  /** Original execution owner, not the scene tile's layout instance. */
  instanceId?: string | null
}

interface ConnectorGeometry {
  /** X of the source circle center (right edge of node, viewport coords). */
  sourceX: number
  /** Y of the source circle center (vertical center of node, viewport coords). */
  sourceY: number
  /** X of the target arrowhead tip (left edge of panel, viewport coords). */
  targetX: number
  /** Y of the target arrowhead tip (same Y as source). */
  targetY: number
}

/**
 * SVG overlay that draws a horizontal connector line from the right edge of an
 * interactive workflow node to the left border of the side panel.
 *
 * - Source end: filled orange circle (r=4)
 * - Target end: triangle arrowhead pointing right
 * - Only rendered when `visible` is true (i.e. an interactive session is active)
 */
export default function CalloutConnector({ nodeId, visible, instanceId }: CalloutConnectorProps) {
  const nodes = useNodes()
  const store = useStoreApi()
  const { flowToScreenPosition } = useReactFlow()
  const [geometry, setGeometry] = useState<ConnectorGeometry | null>(null)

  const computeGeometry = useCallback(() => {
    const candidates = nodes.filter(n => n.data.definitionNodeId === nodeId && n.data.instanceId === instanceId)
    const node = (instanceId !== undefined
      ? candidates.find(n => n.type === 'groupFooter') ?? candidates.find(n => n.type === 'arrangedNode' || n.type === 'arbitronNode')
      : null) ?? nodes.find(n => n.id === nodeId && (instanceId === undefined || !('instanceId' in n.data) || n.data.instanceId === instanceId))
    if (!node) {
      setGeometry(null)
      return
    }

    // Anchor the source to the node's RENDERED geometry. The node's DOM rect
    // already reflects the live viewport transform (pan/zoom) AND its true
    // content-sized width, so the orange dot lands on the actual right edge.
    // The flow-coordinate math below is only a fallback for the brief window
    // before the element is mounted/measured: relying on it directly let the
    // dot fall to position.x + a 200px width guess, which is mid-node for any
    // node wider than 200 (e.g. long interactive labels).
    let sourceX: number
    let sourceY: number
    const graphRoot = store.getState().domNode
    const nodeEl = graphRoot?.querySelector<HTMLElement>(
      `.react-flow__node[data-id="${CSS.escape(node.id)}"]`,
    )
    if (nodeEl) {
      const rect = nodeEl.getBoundingClientRect()
      sourceX = rect.right
      sourceY = rect.top + rect.height / 2
    } else {
      const nodeWidth = node.measured?.width ?? node.width ?? 200
      const nodeHeight = node.measured?.height ?? node.height ?? 80
      let x = node.position.x, y = node.position.y, parentId = node.parentId
      while (parentId) {
        const parent = nodes.find(n => n.id === parentId)
        if (!parent) break
        x += parent.position.x; y += parent.position.y; parentId = parent.parentId
      }
      const flowX = x + nodeWidth
      const flowY = y + nodeHeight / 2
      const screenPos = flowToScreenPosition({ x: flowX, y: flowY })
      sourceX = screenPos.x
      sourceY = screenPos.y
    }

    // Target: left edge of the panel divider.
    // The ResizablePanel divider is the boundary. We find it by looking for the
    // divider element, or fall back to the right panel's left edge.
    const divider = graphRoot?.closest('[data-execution-view]')?.querySelector<HTMLElement>('[class*="divider"]')
    let targetX: number
    if (divider) {
      const dividerRect = divider.getBoundingClientRect()
      targetX = dividerRect.left
    } else {
      // Fallback: use the right edge of the viewport
      targetX = window.innerWidth - 380
    }

    setGeometry({
      sourceX,
      sourceY,
      targetX,
      targetY: sourceY,
    })
  }, [nodes, nodeId, instanceId, flowToScreenPosition, store])

  // Recompute on node changes and on viewport transforms.
  // We use an interval for viewport changes since ReactFlow doesn't expose a
  // viewport-change callback that works well with SVG overlays.
  useEffect(() => {
    if (!visible) {
      setGeometry(null)
      return
    }

    computeGeometry()

    // Re-compute periodically to track zoom/pan changes smoothly
    const interval = setInterval(computeGeometry, 100)
    // Also re-compute on resize
    window.addEventListener('resize', computeGeometry)

    return () => {
      clearInterval(interval)
      window.removeEventListener('resize', computeGeometry)
    }
  }, [visible, computeGeometry])

  if (!visible || !geometry) return null

  // Don't render if the source is to the right of the target (node not visible)
  if (geometry.sourceX >= geometry.targetX - 20) return null

  const arrowSize = 8

  return (
    <svg className={styles.overlay}>
      <defs>
        <marker
          id="callout-arrowhead"
          markerWidth={arrowSize}
          markerHeight={arrowSize}
          refX={arrowSize}
          refY={arrowSize / 2}
          orient="auto"
        >
          <polygon
            points={`0,0 ${arrowSize},${arrowSize / 2} 0,${arrowSize}`}
            fill="var(--state-waiting)"
            opacity={0.7}
          />
        </marker>
      </defs>

      {/* Horizontal connector line */}
      <line
        x1={geometry.sourceX}
        y1={geometry.sourceY}
        x2={geometry.targetX - arrowSize}
        y2={geometry.targetY}
        stroke="var(--state-waiting)"
        strokeWidth={2}
        opacity={0.7}
        markerEnd="url(#callout-arrowhead)"
      />

      {/* Source: filled circle at node right edge */}
      <circle
        cx={geometry.sourceX}
        cy={geometry.sourceY}
        r={4}
        fill="var(--state-waiting)"
        opacity={0.7}
      />
    </svg>
  )
}
