import { useMemo } from 'react'
import dagre from 'dagre'
import type { Node, Edge } from '@xyflow/react'
import { MarkerType } from '@xyflow/react'
import type { WorkflowNode, WorkflowEdge, NodeExecutionState } from './types'
import type { ArbitronNodeData } from './nodes/ArbitronNode'

const NODE_WIDTH = 200
const NODE_HEIGHT = 80

interface UseGraphLayoutInput {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  executionStates?: Record<string, NodeExecutionState>
}

interface UseGraphLayoutOutput {
  rfNodes: Node<ArbitronNodeData>[]
  rfEdges: Edge[]
  needsAutoSave: boolean
}

export function useGraphLayout({
  nodes,
  edges,
  executionStates = {},
}: UseGraphLayoutInput): UseGraphLayoutOutput {
  return useMemo(() => {
    if (nodes.length === 0) {
      return { rfNodes: [], rfEdges: [], needsAutoSave: false }
    }

    // All-or-nothing rule: use stored positions only when every node has one.
    const allHavePositions = nodes.every(
      (n) => n.position != null && n.position.x != null && n.position.y != null,
    )

    let needsAutoSave = false
    let positionMap: Record<string, { x: number; y: number }>

    if (allHavePositions) {
      positionMap = {}
      for (const n of nodes) {
        positionMap[n.id] = { x: n.position!.x, y: n.position!.y }
      }
    } else {
      // Run dagre for the entire graph.
      needsAutoSave = true
      const g = new dagre.graphlib.Graph()
      g.setDefaultEdgeLabel(() => ({}))
      g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })

      for (const n of nodes) {
        g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
      }
      for (const e of edges) {
        g.setEdge(e.source, e.target)
      }

      dagre.layout(g)

      positionMap = {}
      for (const n of nodes) {
        const dagreNode = g.node(n.id)
        positionMap[n.id] = {
          x: dagreNode.x - NODE_WIDTH / 2,
          y: dagreNode.y - NODE_HEIGHT / 2,
        }
      }
    }

    const rfNodes: Node<ArbitronNodeData>[] = nodes.map((n) => ({
      id: n.id,
      type: 'arbitronNode',
      position: positionMap[n.id],
      data: {
        label: n.label || n.id,
        nodeType: n.type,
        executionState: executionStates[n.id] ?? 'pending',
      },
    }))

    const rfEdges: Edge[] = edges.map((e, i) => ({
      id: `e-${e.source}-${e.target}-${i}`,
      source: e.source,
      target: e.target,
      label: e.label ?? e.output ?? undefined,
      type: 'smoothstep',
      animated: executionStates[e.source] === 'running',
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
      style: { stroke: '#444466' },
      labelStyle: { fill: '#888888', fontSize: 11 },
    }))

    return { rfNodes, rfEdges, needsAutoSave }
  }, [nodes, edges, executionStates])
}
