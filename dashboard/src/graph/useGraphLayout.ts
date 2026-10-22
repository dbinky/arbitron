import { useMemo } from 'react'
import dagre from 'dagre'
import type { Node, Edge } from '@xyflow/react'
import { MarkerType } from '@xyflow/react'
import type { GroupGraph } from '../api/types'
import type { WorkflowNode, WorkflowEdge, NodeExecutionState } from './types'
import type { ArbitronNodeData } from './nodes/ArbitronNode'
import { detectLoops, type Loop } from './loops'
import { loopContainerBox, loopContainerLabel } from './loopLayout'
import { chooseLoopSide } from './loopEdgeRouting'
import {
  groupContainerBoxes,
  groupContainerLabel,
  groupBoxId,
  orderTaskCards,
  ownerCluster,
  placeGroupsFromStoredPositions,
  planGroupLayout,
  renderedSource,
  renderedTarget,
  type GroupContainerData,
  type GroupEndpointData,
  type GroupLayoutPlan,
  type GroupLiveSummary,
  type GroupTaskCardData,
  type NodeContextInfo,
} from './groupLayout'

export const NODE_WIDTH = 200
export const NODE_HEIGHT = 80

// Stable empty-states reference. Used as the default so that callers which omit
// executionStates (e.g. the static WorkflowReview view) don't pass a fresh `{}`
// each render, which would be a new dependency every time and defeat the useMemo
// below — recomputing the layout on every render and making rfNodes/rfEdges
// referentially unstable. That instability drives WorkflowReview's
// setNodes(rfNodes) sync-effect into an infinite loop (React error #185) on the
// first node selection. Must never be mutated.
const EMPTY_EXECUTION_STATES: Record<string, NodeExecutionState> = {}
// Same reasoning for the group overlays (P4-T7): stable empty defaults.
const EMPTY_GROUP_LIVE: Record<string, GroupLiveSummary> = {}
const EMPTY_NODE_CONTEXTS: Record<string, NodeContextInfo> = {}

export interface UseGraphLayoutInput {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  executionStates?: Record<string, NodeExecutionState>
  /**
   * Phase 1 ownership analysis of this graph. When it names at least one group
   * present in `nodes`, each group renders as a container box with a fan-out
   * header and a gather footer (plus task cards for the workflows form).
   */
  groups?: GroupGraph | null
  /** Live group overlays keyed by group node id (absent in static views). */
  groupLive?: Record<string, GroupLiveSummary>
  /** Owning-instance info keyed by definition node id (absent in static views). */
  nodeContexts?: Record<string, NodeContextInfo>
  /** Complete finite fallback positions; otherwise manifest positions/dagre apply. */
  positions?: Record<string, { x: number; y: number }>
}

export interface UseGraphLayoutOutput {
  rfNodes: Node[]
  rfEdges: Edge[]
}

function hasXY(p: { x: number; y: number } | null | undefined): p is { x: number; y: number } {
  return p != null && Number.isFinite(p.x) && Number.isFinite(p.y)
}

const PRESENTATION_EDGE_STYLE = { stroke: '#5b6b8c', strokeDasharray: '4 4' }

export function useGraphLayout(input: UseGraphLayoutInput): UseGraphLayoutOutput {
  const { nodes, edges, executionStates, groups, groupLive, nodeContexts, positions } = input
  return useMemo(() => computeGraphLayout(input), [nodes, edges, executionStates, groups, groupLive, nodeContexts, positions])
}

export function computeGraphLayout({
  nodes,
  edges,
  executionStates = EMPTY_EXECUTION_STATES,
  groups = null,
  groupLive = EMPTY_GROUP_LIVE,
  nodeContexts = EMPTY_NODE_CONTEXTS,
  positions,
}: UseGraphLayoutInput): UseGraphLayoutOutput {
    if (nodes.length === 0) {
      return { rfNodes: [], rfEdges: [] }
    }

    const plan: GroupLayoutPlan | null =
      groups && groups.order.length > 0 ? planGroupLayout(nodes, edges, groups) : null
    const activePlan = plan && plan.groups.length > 0 ? plan : null
    const graph = activePlan ? groups : null
    const isHidden = (id: string) => activePlan?.hidden.has(id) === true
    const renderedIds = new Set([
      ...nodes.filter((n) => !isHidden(n.id)).map((n) => n.id),
      ...(activePlan?.presentationNodes.map((n) => n.id) ?? []),
    ])
    // Malformed definitions can contain edges to absent nodes. Do not let
    // dagre invent phantom endpoints or send dangling edges to React Flow.
    const layoutEdges = activePlan
      ? edges.filter((e) => renderedIds.has(renderedSource(graph, e)) && renderedIds.has(renderedTarget(graph, e)))
      : edges
    const detection = detectLoops(layoutEdges)

    // All-or-nothing rule: use supplied fallback positions, else manifest
    // positions, only when every node has a finite one.
    const positionsComplete = positions != null && nodes.every((n) => hasXY(positions[n.id]))
    const allHavePositions = nodes.every((n) => hasXY(n.position))
    let positionMap: Record<string, { x: number; y: number }>

    if (positionsComplete) {
      positionMap = {}
      for (const n of nodes) positionMap[n.id] = { ...positions[n.id] }
      if (activePlan) placeGroupsFromStoredPositions(activePlan, positionMap)
    } else if (allHavePositions) {
      positionMap = {}
      for (const n of nodes) {
        positionMap[n.id] = { x: n.position!.x, y: n.position!.y }
      }
      if (activePlan) placeGroupsFromStoredPositions(activePlan, positionMap)
    } else {
      // Display-only dagre layout; never persisted.
      const g = new dagre.graphlib.Graph({ compound: true })
      g.setDefaultEdgeLabel(() => ({}))
      g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })

      for (const n of nodes) {
        if (isHidden(n.id)) continue
        g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
      }

      if (activePlan) {
        for (const p of activePlan.presentationNodes) {
          g.setNode(p.id, { width: p.width, height: p.height })
        }
        // Group and task clusters (parents first), then each member's cluster.
        for (const c of activePlan.clusters) {
          g.setNode(c.id, {})
        }
        for (const c of activePlan.clusters) {
          if (c.parent) g.setParent(c.id, c.parent)
        }
        for (const [nodeId, clusterId] of Object.entries(activePlan.clusterOf)) {
          g.setParent(nodeId, clusterId)
        }
      }

      // Compound clusters: group each loop body so dagre keeps it in one column.
      // Innermost loop (largest depth) wins for nodes shared across loops.
      const clusterByNode: Record<string, { clusterId: string; depth: number }> = {}
      for (const loop of detection.loops) {
        const clusterId = `cluster-${loop.id}`
        g.setNode(clusterId, {})
        // A loop inside a branch body stays inside that body's task cluster.
        if (activePlan && graph) {
          const parent = ownerCluster(activePlan, graph, loop.headId)
          if (parent) g.setParent(clusterId, parent)
        }
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
      for (const e of layoutEdges) {
        if (detection.backEdgeKeys.has(`${e.source} ${e.target}`)) continue
        g.setEdge(renderedSource(graph, e), renderedTarget(graph, e))
      }
      if (activePlan) {
        for (const pe of activePlan.presentationEdges) {
          g.setEdge(pe.source, pe.target, { weight: 0 })
        }
      }

      dagre.layout(g)

      positionMap = {}
      for (const n of nodes) {
        if (isHidden(n.id)) continue
        const dagreNode = g.node(n.id)
        positionMap[n.id] = {
          x: dagreNode.x - NODE_WIDTH / 2,
          y: dagreNode.y - NODE_HEIGHT / 2,
        }
      }
      if (activePlan) {
        for (const p of activePlan.presentationNodes) {
          const dn = g.node(p.id)
          positionMap[p.id] = { x: dn.x - p.width / 2, y: dn.y - p.height / 2 }
        }
        orderTaskCards(activePlan, positionMap)
      }
    }

    const contextData = (id: string): Record<string, unknown> => {
      const c = nodeContexts[id]
      if (!c) return {}
      return { instanceId: c.instanceId, independent: c.independent, hiddenDependency: c.hiddenDependency }
    }

    const rfNodes: Node<ArbitronNodeData>[] = nodes
      .filter((n) => !isHidden(n.id))
      .map((n) => ({
        id: n.id,
        type: 'arbitronNode',
        position: positionMap[n.id],
        data: {
          label: n.label || n.id,
          nodeType: n.type,
          executionState: executionStates[n.id] ?? 'pending',
          ...contextData(n.id),
        },
      }))

    const groupNodes: Node[] = []
    const groupContainers: Node<GroupContainerData>[] = []
    if (activePlan) {
      const byId = new Map(nodes.map((n) => [n.id, n]))
      const boxes = groupContainerBoxes(activePlan, positionMap)
      for (const gid of activePlan.groups) {
        const scope = activePlan.scopes[gid]
        const def = byId.get(gid)
        const live = groupLive[gid] ?? null
        const instanceId = nodeContexts[gid]?.instanceId ?? live?.instanceId ?? null
        const box = boxes[gid]
        if (box) {
          groupContainers.push({
            id: groupBoxId(gid),
            type: 'groupContainer',
            position: { x: box.x, y: box.y },
            width: box.width,
            height: box.height,
            data: { definitionNodeId: gid, label: groupContainerLabel(scope), depth: activePlan.depth[gid], form: scope.config.form },
            selectable: false,
            focusable: false,
            draggable: false,
            // Below loop containers (-10 + depth) and step nodes; nested groups above enclosing ones.
            zIndex: -20 + activePlan.depth[gid],
          })
        }
        const endpoint = (role: 'header' | 'footer'): GroupEndpointData => ({
          definitionNodeId: gid,
          role,
          label: def?.label || gid,
          form: scope.config.form,
          completionRule: scope.config.completionRule,
          unfinishedBehavior: scope.config.unfinishedBehavior,
          gatherMode: scope.config.gatherMode,
          taskKeys: scope.config.tasks.map((t) => t.key),
          instanceId,
          live,
        })
        for (const p of activePlan.presentationNodes) {
          if (p.groupNodeId !== gid) continue
          if (p.role === 'task') {
            const task = scope.config.tasks.find((t) => t.key === p.taskKey)
            const data: GroupTaskCardData = {
              definitionNodeId: gid,
              role: 'task',
              taskKey: p.taskKey ?? '',
              index: scope.config.tasks.findIndex((t) => t.key === p.taskKey),
              workflow: task?.workflow ?? '',
              instanceId,
              live: (p.taskKey && live?.tasks[p.taskKey]) || null,
            }
            groupNodes.push({ id: p.id, type: 'groupTaskCard', position: positionMap[p.id], data })
          } else {
            groupNodes.push({
              id: p.id,
              type: p.role === 'header' ? 'groupHeader' : 'groupFooter',
              position: positionMap[p.id],
              data: endpoint(p.role),
            })
          }
        }
      }
    }

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

    const rfEdges: Edge[] = layoutEdges.map((e, i) => {
      const key = `${e.source} ${e.target}`
      const loop = loopByBackEdge.get(key)
      const isBack = detection.backEdgeKeys.has(key)

      // For a back-edge, pick the side to route on from where the loop body nodes
      // actually sit (they often bulge to one side; route on the open side so the
      // edge doesn't cut through them). Positions are known here.
      let data: Record<string, unknown> | undefined = loop
        ? { loop, box: boxByLoopId.get(loop.id) ?? undefined }
        : undefined
      if (isBack && loop) {
        const spineX = (positionMap[loop.tailId]?.x ?? 0) + NODE_WIDTH / 2
        const bodyLefts: number[] = []
        const bodyRights: number[] = []
        for (const id of loop.bodyNodeIds) {
          const px = positionMap[id]?.x
          if (px == null) continue
          bodyLefts.push(px)
          bodyRights.push(px + NODE_WIDTH)
        }
        if (bodyLefts.length > 0) {
          const side = chooseLoopSide({ spineX, bodyLefts, bodyRights })
          data = { loop, box: boxByLoopId.get(loop.id) ?? undefined, side }
        }
      }

      return {
        id: `e-${e.source}-${e.target}-${i}`,
        // Group endpoints: task edges leave the header; success/failure leave
        // the footer; edges into a group enter its header.
        source: renderedSource(graph, e),
        target: renderedTarget(graph, e),
        label: e.label ?? e.output ?? undefined,
        type: isBack ? 'loopBackEdge' : 'smoothstep',
        animated: executionStates[e.source] === 'running',
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
        style: { stroke: isBack ? '#7c8cf5' : '#444466' },
        labelStyle: { fill: '#888888', fontSize: 11 },
        data,
        zIndex: isBack ? 5 : undefined,
      }
    })

    // Presentation-only connectors (sink -> gather, header -> card -> gather).
    // They exist only in the rendered edge list — never in the manifest, which
    // would break closed bodies or run branches in the parent cursor.
    if (activePlan) {
      for (const pe of activePlan.presentationEdges) {
        rfEdges.push({
          id: pe.id,
          source: pe.source,
          target: pe.target,
          type: 'smoothstep',
          animated: false,
          selectable: false,
          focusable: false,
          markerEnd: { type: MarkerType.ArrowClosed, width: 12, height: 12 },
          style: PRESENTATION_EDGE_STYLE,
          data: { presentationOnly: true, kind: pe.kind, groupNodeId: pe.groupNodeId, taskKey: pe.taskKey },
        })
      }
    }

    return {
      rfNodes: [...groupContainers, ...containerNodes, ...rfNodes, ...groupNodes],
      rfEdges,
    }
}
