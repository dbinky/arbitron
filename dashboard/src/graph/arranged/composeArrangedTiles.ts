import type { Edge, Node } from '@xyflow/react'
import type { GroupGraph } from '../../api/types'
import type { GroupLiveSummary, NodeContextInfo } from '../groupLayout'
import type { NodeExecutionState, WorkflowDefinition } from '../types'
import type { Geometry, LayoutView, Point, Rect } from './types'
import { buildArrangedFlow } from './buildArrangedFlow'
import { renderedID } from './GeometryHandles'
import { fallbackPositions } from './fallbackLayout'
import { RENDERER_CONTRACT, resolveDisplayGeometry } from './validate'
import { computeGraphLayout, NODE_HEIGHT, NODE_WIDTH } from '../useGraphLayout'
import { definitionNodeIdOf } from '../groupLayout'
import { groupElementID } from './groupValidate'

export interface LayoutTile {
  onOverlayOverflow?: () => void
  instanceId: string; definition: WorkflowDefinition; view: LayoutView | null
  groups: GroupGraph | null
  executionStates?: Record<string, NodeExecutionState>
  groupLive?: Record<string, GroupLiveSummary>
  nodeContexts?: Record<string, NodeContextInfo>
}
export function tileSceneID(graphInstanceId: string, instanceId: string, logicalID: string): string {
  return renderedID(graphInstanceId, JSON.stringify(['tile', instanceId, logicalID]))
}

/** Translate a disposable drawing copy; the local reviewed geometry is never changed. */
function translated(geo: Geometry, dy: number): Geometry {
  const point = (p: Point): Point => ({ ...p, y: p.y + dy })
  const rect = (r: Rect): Rect => ({ ...r, y: r.y + dy })
  const attachment = (a: Geometry['edges'][number]['sourceAttach']) => ({ ...a, point: point(a.point) })
  return { ...geo, canvas: rect(geo.canvas),
    nodes: geo.nodes.map(n => ({ ...n, box: rect(n.box) })),
    edges: geo.edges.map(e => ({ ...e, points: e.points.map(point), sourceAttach: attachment(e.sourceAttach), targetAttach: attachment(e.targetAttach), ...(e.text ? { text: { ...e.text, box: rect(e.text.box) } } : {}) })),
    crossings: geo.crossings.map(c => ({ ...c, point: point(c.point) })),
    groupElements: geo.groupElements?.map(e => ({ ...e, box: rect(e.box), texts: e.texts.map(t => ({ ...t, box: rect(t.box) })), ...(e.overlayBox ? { overlayBox: rect(e.overlayBox) } : {}) })),
    groupLanes: geo.groupLanes?.map(l => ({ ...l, box: rect(l.box) })),
    groupConnectors: geo.groupConnectors?.map(c => ({ ...c, points: c.points.map(point), sourceAttach: attachment(c.sourceAttach), targetAttach: attachment(c.targetAttach) })),
    groupCrossings: geo.groupCrossings?.map(c => ({ ...c, point: point(c.point) })),
  }
}

export function composeArrangedTiles(tiles: readonly LayoutTile[], graphInstanceId: string): { nodes: Node[]; edges: Edge[]; bounds: Rect } {
  if (!graphInstanceId) throw new Error('graphInstanceId is required')
  const nodes: Node[] = [], edges: Edge[] = []
  let bottom = 0, right = 0
  for (const [index, tile] of tiles.entries()) {
    const geo = resolveDisplayGeometry(tile.definition, tile.view).geometry
    const dy = index === 0 ? 0 : bottom + RENDERER_CONTRACT.groupRowGapPx
    let flow: { nodes: Node[]; edges: Edge[] }, extent: Rect
    if (geo) {
      flow = buildArrangedFlow(translated(geo, dy), Object.fromEntries(tile.definition.nodes.map(n => [n.id, n.type])), tile.executionStates ?? {}, graphInstanceId,
        new Set(Object.keys(tile.executionStates ?? {}).filter(id => tile.executionStates?.[id] === 'running')),
        { groups: tile.view?.groups ?? null, groupLive: tile.groupLive, nodeContexts: tile.nodeContexts, layoutInstanceId: tile.instanceId, onOverlayOverflow: tile.onOverlayOverflow })
      extent = { ...geo.canvas, y: geo.canvas.y + dy }
    } else {
      const retained = tile.view?.savedRevision === (tile.definition.presentation?.revision ?? '') ? tile.view?.retained?.positions : undefined
      const fallback = computeGraphLayout({ nodes: tile.definition.nodes, edges: tile.definition.edges, positions: fallbackPositions(tile.definition.nodes, retained) ?? undefined,
        groups: tile.groups, executionStates: tile.executionStates, groupLive: tile.groupLive, nodeContexts: tile.nodeContexts })
      flow = { nodes: fallback.rfNodes.map(n => ({ ...n, position: { ...n.position, y: n.position.y + dy }, data: { ...n.data, definitionNodeId: definitionNodeIdOf(n), layoutInstanceId: tile.instanceId } })),
        edges: fallback.rfEdges.map(e => ({ ...e, data: { ...e.data, layoutInstanceId: tile.instanceId, ...(e.data?.box ? { box: { ...e.data.box as Rect, y: (e.data.box as Rect).y + dy } } : {}) } })) }
      extent = { x: 0, y: 0, w: Math.max(0, ...flow.nodes.map(n => n.position.x + (n.width ?? NODE_WIDTH))), h: Math.max(dy, ...flow.nodes.map(n => n.position.y + (n.height ?? NODE_HEIGHT))) }
    }
    const ids = new Map(flow.nodes.map(n => {
      const logical = (n.data.reviewed as { element: { id: string } } | undefined)?.element.id ?? (n.data.nodeBox ? groupElementID((n.data.nodeBox as { id: string }).id, 'header') : n.id)
      return [n.id, tileSceneID(graphInstanceId, tile.instanceId, logical)]
    }))
    nodes.push(...flow.nodes.map(n => ({ ...n, id: ids.get(n.id)!, parentId: n.parentId ? ids.get(n.parentId) : undefined })))
    edges.push(...flow.edges.map(e => ({ ...e, id: tileSceneID(graphInstanceId, tile.instanceId, (e.data?.connector as { id: string } | undefined)?.id ?? (e.data?.route ? `edge:${(e.data.route as { ordinal: number }).ordinal}` : e.id)), source: ids.get(e.source)!, target: ids.get(e.target)! })))
    bottom = Math.max(bottom, extent.y + extent.h)
    right = Math.max(right, extent.x + extent.w)
  }
  return { nodes, edges, bounds: { x: 0, y: 0, w: right, h: bottom } }
}
