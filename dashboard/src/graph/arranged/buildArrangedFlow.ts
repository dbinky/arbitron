import type { Edge, EdgeTypes, Node, NodeTypes } from '@xyflow/react'
import type { GroupGraph, GroupScope } from '../../api/types'
import type { GroupLiveSummary, GroupLiveTask, NodeContextInfo } from '../groupLayout'
import type { NodeExecutionState } from '../types'
import type { Attachment, Geometry, GroupElement, NodeBox, Point, Rect, RendererContract } from './types'
import ArrangedNode, { type ArrangedNodeData } from './ArrangedNode'
import GroupContainerNode, { GroupHeaderNode, GroupFooterNode, GroupTaskCardNode, GroupLaneNode } from '../nodes/GroupContainerNode'
import RoutedEdge, { type RoutedEdgeData, type RoutedGroupConnectorData } from './RoutedEdge'
import { groupElementID } from './groupValidate'
import { renderedID, type GeometryHandle } from './GeometryHandles'

export interface ArrangedRuntime {
  onOverlayOverflow?: () => void
  groups: GroupGraph | null
  groupLive?: Record<string, GroupLiveSummary>
  nodeContexts?: Record<string, NodeContextInfo>
  layoutInstanceId?: string
}
export interface ArrangedGroupElementData {
  onOverlayOverflow?: () => void
  element: GroupElement
  contract: RendererContract
  group: GroupScope
  nodeBox?: NodeBox
  live: GroupLiveSummary | null
  taskLive: GroupLiveTask | null
  context: NodeContextInfo | null
  definitionNodeId: string
  layoutInstanceId?: string
  [key: string]: unknown
}
export const arrangedNodeTypes: NodeTypes = { arrangedNode: ArrangedNode, groupContainer: GroupContainerNode, groupHeader: GroupHeaderNode, groupFooter: GroupFooterNode, groupTaskCard: GroupTaskCardNode, groupLane: GroupLaneNode }
export const arrangedEdgeTypes: EdgeTypes = { routedEdge: RoutedEdge }

/** Convert validated geometry without recomputing any persisted position or route. */
export function buildArrangedFlow(
  geo: Geometry, nodeTypesById: Record<string, string>, executionStates: Record<string, NodeExecutionState>,
  graphInstanceId: string, activeSources: Set<string> = new Set(), runtime?: ArrangedRuntime,
): { nodes: Node[]; edges: Edge[] } {
  if (!graphInstanceId) throw new Error('graphInstanceId is required')
  for (const n of geo.nodes) {
    const type = nodeTypesById[n.id]
    if (!type || type.replace(/_/g, ' ') !== n.typeCaption) throw new Error(`node type mismatch for ${n.id}`)
  }
  const groups = runtime?.groups
  if (geo.nodes.some(n => nodeTypesById[n.id] === 'parallel_group') && !groups?.order.length) throw new Error('group geometry: missing ownership')
  const elements = new Map((geo.groupElements ?? []).map(e => [e.id, e]))
  const ordinaryID = (id: string) => groupElementID(id, 'header')
  const nodeID = (id: string) => groups ? renderedID(graphInstanceId, id) : geo.nodes.find(n => ordinaryID(n.id) === id)?.id ?? id
  const handles = new Map<string, GeometryHandle[]>()
  const gaps = new Map<string, Point[]>(), overs = new Map<string, Point[]>()
  for (const c of [...geo.crossings.map(c => ({ ...c, over: `edge:${c.over}`, under: `edge:${c.under}` })), ...(geo.groupCrossings ?? [])]) {
    gaps.set(c.under, [...(gaps.get(c.under) ?? []), c.point])
    overs.set(c.over, [...(overs.get(c.over) ?? []), c.point])
  }
  const edgeEnds = (key: string, source: Attachment, target: Attachment) => {
    for (const [type, attachment] of [['source', source], ['target', target]] as const) {
      const element = attachment.elementId ?? ordinaryID(attachment.nodeId)
      handles.set(element, [...(handles.get(element) ?? []), { id: `${key}:${type}`, type, attachment }])
    }
    return { source: nodeID(source.elementId ?? ordinaryID(source.nodeId)), target: nodeID(target.elementId ?? ordinaryID(target.nodeId)), sourceHandle: `${key}:source`, targetHandle: `${key}:target` }
  }
  const edges: Edge[] = geo.edges.map(route => ({
    id: `routed-${route.ordinal}`, ...edgeEnds(`edge:${route.ordinal}`, route.sourceAttach, route.targetAttach),
    type: 'routedEdge', selectable: false, focusable: false,
    data: { route, gaps: gaps.get(`edge:${route.ordinal}`) ?? [], overs: overs.get(`edge:${route.ordinal}`) ?? [], contract: geo.contract, active: activeSources.has(route.source), graphInstanceId, layoutInstanceId: runtime?.layoutInstanceId } satisfies RoutedEdgeData,
  }))
  for (const connector of geo.groupConnectors ?? []) edges.push({
    id: renderedID(graphInstanceId, connector.id), ...edgeEnds(connector.id, connector.sourceAttach, connector.targetAttach), type: 'routedEdge', selectable: false, focusable: false,
    data: { connector, gaps: gaps.get(connector.id) ?? [], overs: overs.get(connector.id) ?? [], contract: geo.contract, active: false, graphInstanceId, layoutInstanceId: runtime?.layoutInstanceId } satisfies RoutedGroupConnectorData,
  })
  const contextData = (id: string) => {
    const context = runtime?.nodeContexts?.[id]
    return { definitionNodeId: id, ...(context ? { ...context } : {}), ...(runtime?.layoutInstanceId ? { layoutInstanceId: runtime.layoutInstanceId } : {}) }
  }
  const parentOf = (element: GroupElement): string | undefined => {
    if (element.role !== 'container') return groupElementID(element.groupNodeId, 'container')
    const owner = groups?.owner[element.groupNodeId]
    return owner ? groupElementID(owner.groupNodeId, 'lane', owner.taskKey) : undefined
  }
  const relative = (box: Rect, parent?: string) => {
    const origin = parent ? elements.get(parent)?.box : undefined
    if (parent && !origin) throw new Error(`missing parent ${parent}`)
    return { x: box.x - (origin?.x ?? 0), y: box.y - (origin?.y ?? 0) }
  }
  const nodes: Node[] = geo.nodes.filter(n => !groups?.groups[n.id]).map(nodeBox => {
    const owner = groups?.owner[nodeBox.id], parent = owner ? groupElementID(owner.groupNodeId, 'lane', owner.taskKey) : undefined
    return { id: nodeID(ordinaryID(nodeBox.id)), parentId: parent ? nodeID(parent) : undefined, type: 'arrangedNode', position: relative(nodeBox.box, parent), width: nodeBox.box.w, height: nodeBox.box.h, draggable: false, connectable: false, selectable: true,
      data: { nodeBox, nodeType: nodeTypesById[nodeBox.id], executionState: executionStates[nodeBox.id] ?? 'pending', contract: geo.contract, handles: handles.get(ordinaryID(nodeBox.id)) ?? [], ...contextData(nodeBox.id) } satisfies ArrangedNodeData }
  })
  const variants: Record<string, string> = { container: 'groupContainer', header: 'groupHeader', footer: 'groupFooter', lane: 'groupLane', task: 'groupTaskCard' }
  for (const element of elements.values()) {
    const group = groups?.groups[element.groupNodeId]
    if (!group) throw new Error(`missing group ${element.groupNodeId}`)
    const live = runtime?.groupLive?.[element.groupNodeId] ?? null, parent = parentOf(element)
    const reviewed: ArrangedGroupElementData = { onOverlayOverflow: runtime?.onOverlayOverflow, element, group, contract: geo.contract, live, taskLive: live?.tasks[element.taskKey ?? ''] ?? null, context: runtime?.nodeContexts?.[element.groupNodeId] ?? null, definitionNodeId: element.groupNodeId, layoutInstanceId: runtime?.layoutInstanceId, ...(element.role === 'header' ? { nodeBox: geo.nodes.find(n => n.id === element.groupNodeId) } : {}), handles: handles.get(element.id) ?? [] }
    nodes.push({ id: nodeID(element.id), parentId: parent ? nodeID(parent) : undefined, type: variants[element.role], position: relative(element.box, parent), width: element.box.w, height: element.box.h, draggable: false, connectable: false, selectable: element.role !== 'container', data: { ...contextData(element.groupNodeId), instanceId: reviewed.context ? reviewed.context.instanceId : live?.instanceId ?? null, reviewed } })
  }
  // ReactFlow requires parents before their children, including nested groups.
  const depth = (n: Node): number => n.parentId ? 1 + depth(nodes.find(p => p.id === n.parentId)!) : 0
  nodes.sort((a, b) => depth(a) - depth(b))
  if (elements.size) {
    // Containment paint is behind every route, which is behind node/card ink.
    const routeLayer = Math.max(...nodes.map(depth)) + 1
    for (const n of nodes) n.zIndex = n.type === 'groupContainer' || n.type === 'groupLane' ? depth(n) : routeLayer + 1
    for (const e of edges) { e.zIndex = routeLayer; e.style = { zIndex: routeLayer } }
  }
  return { nodes, edges }
}
