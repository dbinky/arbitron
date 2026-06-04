import { useMemo } from 'react'
import dagre from 'dagre'
import type { Node, Edge } from '@xyflow/react'
import { MarkerType } from '@xyflow/react'
import type { WorkflowNode, WorkflowEdge, NodeExecutionState } from './types'
import type { ArbitronNodeData } from './nodes/ArbitronNode'
import { detectLoops, type Loop } from './loops'
import { loopContainerBox, loopContainerLabel } from './loopLayout'

export const NODE_WIDTH = 200
export const NODE_HEIGHT = 80

interface UseGraphLayoutInput {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  executionStates?: Record<string, NodeExecutionState>
}

interface UseGraphLayoutOutput {
  rfNodes: Node[]
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

    const detection = detectLoops(edges)

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
      const g = new dagre.graphlib.Graph({ compound: true })
      g.setDefaultEdgeLabel(() => ({}))
      g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })

      for (const n of nodes) {
        g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
      }

      // Compound clusters: group each loop body so dagre keeps it in one column.
      // Innermost loop (largest depth) wins for nodes shared across loops.
      const clusterByNode: Record<string, { clusterId: string; depth: number }> = {}
      for (const loop of detection.loops) {
        const clusterId = `cluster-${loop.id}`
        g.setNode(clusterId, {})
        for (const id of loop.bodyNodeIds) {
          const existing = clusterByNode[id]
          if (!existing || loop.depth > existing.depth) {
            clusterByNode[id] = { clusterId, depth: loop.depth }
          }
        }
      }
      for (const [nodeId, { clusterId }] of Object.entries(clusterByNode)) {
        g.setParent(nodeId, clusterId)
      }

      // Seed only forward edges so back-edges do not distort rank assignment.
      for (const e of edges) {
        if (detection.backEdgeKeys.has(`${e.source} ${e.target}`)) continue
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

    const containerNodes: Node<{ label: string; depth: number }>[] = []
    for (const loop of detection.loops) {
      const box = loopContainerBox(loop, positionMap)
      if (!box) continue
      containerNodes.push({
        id: `loop-container-${loop.id}`,
        type: 'loopContainer',
        position: { x: box.x, y: box.y },
        width: box.width,
        height: box.height,
        data: { label: loopContainerLabel(loop), depth: loop.depth },
        selectable: false,
        focusable: false,
        draggable: false,
        // Below step nodes; deeper loops sit slightly above shallower ones.
        zIndex: -10 + loop.depth,
      })
    }

    const loopByBackEdge = new Map<string, Loop>()
    for (const loop of detection.loops) {
      loopByBackEdge.set(`${loop.tailId} ${loop.headId}`, loop)
    }

    const boxByLoopId = new Map<string, ReturnType<typeof loopContainerBox>>()
    for (const loop of detection.loops) {
      boxByLoopId.set(loop.id, loopContainerBox(loop, positionMap))
    }

    const rfEdges: Edge[] = edges.map((e, i) => {
      const key = `${e.source} ${e.target}`
      const loop = loopByBackEdge.get(key)
      const isBack = detection.backEdgeKeys.has(key)
      return {
        id: `e-${e.source}-${e.target}-${i}`,
        source: e.source,
        target: e.target,
        label: e.label ?? e.output ?? undefined,
        type: isBack ? 'loopBackEdge' : 'smoothstep',
        animated: executionStates[e.source] === 'running',
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
        style: { stroke: isBack ? '#7c8cf5' : '#444466' },
        labelStyle: { fill: '#888888', fontSize: 11 },
        data: loop ? { loop, box: boxByLoopId.get(loop.id) ?? undefined } : undefined,
        zIndex: isBack ? 5 : undefined,
      }
    })

    return { rfNodes: [...containerNodes, ...rfNodes], rfEdges, needsAutoSave }
  }, [nodes, edges, executionStates])
}
