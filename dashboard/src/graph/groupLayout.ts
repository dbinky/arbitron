import type { Node } from '@xyflow/react'
import type { ArrangedGroupElementData } from './arranged/buildArrangedFlow'
import type { GroupGraph, GroupProgress, GroupScope } from '../api/types'
import type { WorkflowEdge, WorkflowNode } from './types'

// Geometry. Presentation nodes share the step-node footprint (200 wide) so the
// existing anchor/centering maths (NODE_WIDTH / 2) holds for them too.
const NODE_WIDTH = 200
const NODE_HEIGHT = 80
export const GROUP_HEADER_HEIGHT = 80
export const GROUP_FOOTER_HEIGHT = 80
export const TASK_CARD_HEIGHT = 96
/** Padding between a group box and its members; larger than loopLayout's 24 so a loop box inside a body stays inside the group box. */
export const GROUP_PAD = 32
/** Vertical gap between header, task-card row and footer in the stored-position layout. */
export const GROUP_GAP_Y = 48
/** Horizontal gap between task cards in the stored-position layout. */
export const TASK_CARD_GAP_X = 24

/** Synthetic presentation id of a group's fan-out header. */
export function groupHeaderId(groupNodeId: string): string {
  return `group:${groupNodeId}:header`
}
/** Synthetic presentation id of a group's gather footer. */
export function groupFooterId(groupNodeId: string): string {
  return `group:${groupNodeId}:footer`
}
/** Synthetic presentation id of a group's container box. */
export function groupBoxId(groupNodeId: string): string {
  return `group:${groupNodeId}:box`
}
/** Synthetic presentation id of a workflows-form task card. */
export function groupTaskCardId(groupNodeId: string, taskKey: string): string {
  return `group:${groupNodeId}:task:${taskKey}`
}

export type GroupPresentationRole = 'header' | 'footer' | 'task'

/** Live overlay of one task (built by P4-T8 from GraphState; absent in static views). */
export interface GroupLiveTask {
  key: string
  /** Live status (GroupTaskLiveStatus). */
  status: string
  /** Status frozen into the decision; '' while undecided. */
  frozenStatus: string
  childInstanceId: string | null
  blocking: boolean
  continuingIndependently: boolean
  cancelRequested: boolean
  redacted: boolean
  launchError?: string
  winner: boolean
  /** Labels of the running/waiting nodes in this task's child run and its descendants. */
  activeNodes: string[]
}

/** Live overlay of one group run, keyed by group node id in useGraphLayout's input. */
export interface GroupLiveSummary {
  /** Instance executing the group node; null when unknown. */
  instanceId: string | null
  groupRunId: string | null
  /** GroupPanelState, or 'not_started' when the group has no run yet. */
  state: string
  progress: GroupProgress | null
  winner?: string
  /** Human text of file-gathering / resolution activity, e.g. "Resolving conflicts (attempt 2)". */
  activity?: string
  /** First waiting reason, if the gather is waiting. */
  waitingReason?: string
  tasks: Record<string, GroupLiveTask>
}

/** Which instance executes a rendered definition node, and how to style it. */
export interface NodeContextInfo {
  /** Owning instance; null when not launched yet, unknown or redacted. */
  instanceId: string | null
  /** The owning task no longer blocks its group (continuing independently). */
  independent: boolean
  /** The owning context is invisible to the caller. */
  hiddenDependency: boolean
}

/** data of groupHeader / groupFooter nodes. */
export interface GroupEndpointData {
  layoutInstanceId?: string
  reviewed?: ArrangedGroupElementData
  definitionNodeId: string
  role: 'header' | 'footer'
  label: string
  form: GroupScope['config']['form']
  completionRule: GroupScope['config']['completionRule']
  unfinishedBehavior: GroupScope['config']['unfinishedBehavior']
  gatherMode: GroupScope['config']['gatherMode']
  taskKeys: string[]
  instanceId: string | null
  live: GroupLiveSummary | null
  [key: string]: unknown
}

/** data of groupTaskCard nodes (workflows form). */
export interface GroupTaskCardData {
  layoutInstanceId?: string
  reviewed?: ArrangedGroupElementData
  definitionNodeId: string
  role: 'task'
  taskKey: string
  /** Author order, 0-based. */
  index: number
  workflow: string
  instanceId: string | null
  live: GroupLiveTask | null
  [key: string]: unknown
}

/** data of groupContainer nodes. */
export interface GroupContainerData {
  layoutInstanceId?: string
  reviewed?: ArrangedGroupElementData
  definitionNodeId: string
  label: string
  depth: number
  form: GroupScope['config']['form']
  [key: string]: unknown
}

export interface PresentationNode {
  id: string
  role: GroupPresentationRole
  groupNodeId: string
  taskKey?: string
  width: number
  height: number
}

export interface PresentationEdge {
  id: string
  source: string
  target: string
  /** sink: branch body sink -> footer; fanout: header -> task card; gather: task card -> footer. */
  kind: 'sink' | 'fanout' | 'gather'
  groupNodeId: string
  taskKey: string
}

export interface GroupCluster {
  id: string
  parent?: string
}

/**
 * Everything the layout needs to draw groups, derived ONLY from the Phase 1
 * ownership analysis (GroupGraph) plus the manifest edges — never from a
 * separate reachability interpretation.
 */
export interface GroupLayoutPlan {
  /** Group node ids drawn, in GroupGraph.order. */
  groups: string[]
  scopes: Record<string, GroupScope>
  /** Nesting depth: 0 for a main-path group. */
  depth: Record<string, number>
  /** Definition ids replaced by presentation nodes (the group nodes). */
  hidden: Set<string>
  presentationNodes: PresentationNode[]
  presentationEdges: PresentationEdge[]
  /** dagre compound clusters, parents first. */
  clusters: GroupCluster[]
  /** Rendered node id -> its immediate dagre cluster. */
  clusterOf: Record<string, string>
  /** Task cluster id per "groupNodeId\u0000taskKey" (branches form). */
  taskCluster: Record<string, string>
  /** Immediate rendered members per group box (nested groups by group id). */
  members: Record<string, string[]>
}

function groupClusterId(g: string): string {
  return `cluster-group:${g}`
}
function taskClusterId(g: string, key: string): string {
  return `cluster-group:${g}:task:${key}`
}
function ownerKey(g: string, key: string): string {
  return `${g}\u0000${key}`
}

/** Group node ids of `graph` that are present in `nodes`, in GroupGraph.order. */
function drawnGroups(graph: GroupGraph, nodeIds: Set<string>): string[] {
  return graph.order.filter((g) => nodeIds.has(g) && graph.groups[g] != null)
}

/**
 * Immediate members of (group, task): every node whose IMMEDIATE owner is that
 * pair, in the task's Bodies order first, then any remaining owned node in
 * definition order. Nested groups' body nodes belong to the nested group.
 */
export function immediateMembers(graph: GroupGraph, nodes: WorkflowNode[], groupNodeId: string, taskKey: string): string[] {
  const owned = (id: string) => {
    const o = graph.owner[id]
    return o != null && o.groupNodeId === groupNodeId && o.taskKey === taskKey
  }
  const out: string[] = []
  const seen = new Set<string>()
  for (const id of graph.groups[groupNodeId]?.bodies[taskKey] ?? []) {
    if (owned(id) && !seen.has(id)) {
      seen.add(id)
      out.push(id)
    }
  }
  for (const n of nodes) {
    if (owned(n.id) && !seen.has(n.id)) {
      seen.add(n.id)
      out.push(n.id)
    }
  }
  return out
}

/**
 * Sinks of a branch body: immediate members (not nested groups) with no
 * outgoing manifest edge. A body is closed, so these are where the branch ends.
 * Falls back to the owned task entry when a body has no such node; a skipped
 * or missing entry cannot supply a connector.
 */
export function bodySinks(
  graph: GroupGraph,
  nodes: WorkflowNode[],
  edges: WorkflowEdge[],
  groupNodeId: string,
  taskKey: string,
): string[] {
  const nodeIds = new Set(nodes.map((n) => n.id))
  const hasOutgoing = new Set(edges.filter((e) => nodeIds.has(e.target)).map((e) => e.source))
  const members = immediateMembers(graph, nodes, groupNodeId, taskKey).filter((id) => nodeIds.has(id))
  const sinks = members.filter((id) => graph.groups[id] == null && !hasOutgoing.has(id))
  if (sinks.length > 0) return sinks
  const entry = graph.groups[groupNodeId]?.config.tasks.find((t) => t.key === taskKey)?.entry
  return entry && members.includes(entry) ? [entry] : []
}

/** Rendered id an edge endpoint maps to: a group node becomes its header (incoming) or footer (outgoing non-task). */
export function renderedSource(graph: GroupGraph | null, e: WorkflowEdge): string {
  if (!graph || graph.groups[e.source] == null) return e.source
  return e.output === 'task' ? groupHeaderId(e.source) : groupFooterId(e.source)
}
export function renderedTarget(graph: GroupGraph | null, e: WorkflowEdge): string {
  if (!graph || graph.groups[e.target] == null) return e.target
  return groupHeaderId(e.target)
}

/** Rendered id of a definition node that is a member of a body (a nested group renders via its footer). */
function renderedMemberSink(graph: GroupGraph, id: string): string {
  return graph.groups[id] != null ? groupFooterId(id) : id
}

export function groupContainerLabel(scope: GroupScope): string {
  const form = scope.config.form === 'workflows' ? 'Parallel workflows' : 'Parallel branches'
  const rule = scope.config.completionRule === 'first_success' ? 'first success' : 'all succeed'
  return `${form} · ${rule}`
}

/** Builds the presentation plan for every drawn group. */
export function planGroupLayout(nodes: WorkflowNode[], edges: WorkflowEdge[], graph: GroupGraph): GroupLayoutPlan {
  const nodeIds = new Set(nodes.map((n) => n.id))
  const groups = drawnGroups(graph, nodeIds)
  const drawn = new Set(groups)
  const scopes: Record<string, GroupScope> = {}
  for (const g of groups) {
    const scope = graph.groups[g]
    const seen = new Set<string>()
    // AnalyzeGroups keeps raw config even when a task cannot seed a body.
    // Draw the first keyed task only, and require branch entries to belong to
    // that task; skipped/shared/missing entries cannot supply gather sinks.
    const tasks = scope.config.tasks.filter((task) => {
      if (!task.key || seen.has(task.key)) return false
      seen.add(task.key)
      if (scope.config.form === 'workflows') return true
      const owner = task.entry ? graph.owner[task.entry] : undefined
      return !!task.entry && nodeIds.has(task.entry)
        && owner?.groupNodeId === g && owner.taskKey === task.key
    })
    scopes[g] = { ...scope, config: { ...scope.config, tasks } }
  }

  const depth: Record<string, number> = {}
  const depthOf = (g: string, guard = 0): number => {
    if (depth[g] != null) return depth[g]
    const parent = scopes[g]?.parent?.groupNodeId
    const d = parent && drawn.has(parent) && guard < 64 ? depthOf(parent, guard + 1) + 1 : 0
    depth[g] = d
    return d
  }
  for (const g of groups) depthOf(g)

  const presentationNodes: PresentationNode[] = []
  const presentationEdges: PresentationEdge[] = []
  const clusters: GroupCluster[] = []
  const clusterOf: Record<string, string> = {}
  const taskCluster: Record<string, string> = {}
  const members: Record<string, string[]> = {}

  // Parents before children so dagre sees every parent cluster first.
  const byDepth = [...groups].sort((a, b) => depth[a] - depth[b])
  for (const g of byDepth) {
    const scope = scopes[g]
    const parent = scope.parent && drawn.has(scope.parent.groupNodeId)
      ? taskCluster[ownerKey(scope.parent.groupNodeId, scope.parent.taskKey)]
      : undefined
    const cluster = groupClusterId(g)
    clusters.push(parent ? { id: cluster, parent } : { id: cluster })

    const header = groupHeaderId(g)
    const footer = groupFooterId(g)
    presentationNodes.push({ id: header, role: 'header', groupNodeId: g, width: NODE_WIDTH, height: GROUP_HEADER_HEIGHT })
    presentationNodes.push({ id: footer, role: 'footer', groupNodeId: g, width: NODE_WIDTH, height: GROUP_FOOTER_HEIGHT })
    clusterOf[header] = cluster
    clusterOf[footer] = cluster
    const boxMembers = [header, footer]

    if (scope.config.form === 'workflows') {
      for (const task of scope.config.tasks) {
        const card = groupTaskCardId(g, task.key)
        presentationNodes.push({ id: card, role: 'task', groupNodeId: g, taskKey: task.key, width: NODE_WIDTH, height: TASK_CARD_HEIGHT })
        clusterOf[card] = cluster
        boxMembers.push(card)
        presentationEdges.push({ id: `group:${g}:fanout:${task.key}`, source: header, target: card, kind: 'fanout', groupNodeId: g, taskKey: task.key })
        presentationEdges.push({ id: `group:${g}:gather:${task.key}`, source: card, target: footer, kind: 'gather', groupNodeId: g, taskKey: task.key })
      }
    } else {
      for (const task of scope.config.tasks) {
        const body = immediateMembers(graph, nodes, g, task.key).filter((id) => nodeIds.has(id))
        if (body.length > 0) {
          const tc = taskClusterId(g, task.key)
          clusters.push({ id: tc, parent: cluster })
          taskCluster[ownerKey(g, task.key)] = tc
          for (const id of body) {
            if (graph.groups[id] == null) clusterOf[id] = tc
          }
        }
        boxMembers.push(...body)
        for (const sink of bodySinks(graph, nodes, edges, g, task.key)) {
          const source = renderedMemberSink(graph, sink)
          presentationEdges.push({ id: `group:${g}:sink:${task.key}:${sink}`, source, target: footer, kind: 'sink', groupNodeId: g, taskKey: task.key })
        }
      }
    }
    members[g] = boxMembers
  }

  return {
    groups,
    scopes,
    depth,
    hidden: new Set(groups),
    presentationNodes,
    presentationEdges,
    clusters,
    clusterOf,
    taskCluster,
    members,
  }
}

/** The task cluster that owns a definition node (for parenting a loop cluster inside a branch body). */
export function ownerCluster(plan: GroupLayoutPlan, graph: GroupGraph, nodeId: string): string | undefined {
  const o = graph.owner[nodeId]
  if (!o) return undefined
  return plan.taskCluster[ownerKey(o.groupNodeId, o.taskKey)]
}

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

function sizeOf(plan: GroupLayoutPlan, id: string): { width: number; height: number } {
  const p = plan.presentationNodes.find((n) => n.id === id)
  return p ? { width: p.width, height: p.height } : { width: NODE_WIDTH, height: NODE_HEIGHT }
}

function boundsOf(rects: Box[]): Box | null {
  if (rects.length === 0) return null
  const minX = Math.min(...rects.map((r) => r.x))
  const minY = Math.min(...rects.map((r) => r.y))
  const maxX = Math.max(...rects.map((r) => r.x + r.width))
  const maxY = Math.max(...rects.map((r) => r.y + r.height))
  return { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
}

function memberRect(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>, boxes: Record<string, Box>, id: string): Box | null {
  if (plan.scopes[id] != null) return boxes[id] ?? null
  const p = positions[id]
  if (!p) return null
  const s = sizeOf(plan, id)
  return { x: p.x, y: p.y, width: s.width, height: s.height }
}

/**
 * Stored-position layout: the header takes the group node's saved position;
 * workflow task cards form an author-ordered row under it; the footer sits
 * under the cards (workflows) or under the lowest body member (branches).
 * Innermost groups first, so an enclosing footer clears nested boxes.
 */
export function placeGroupsFromStoredPositions(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>): void {
  const boxes: Record<string, Box> = {}
  const innermostFirst = [...plan.groups].sort((a, b) => plan.depth[b] - plan.depth[a])
  for (const g of innermostFirst) {
    const scope = plan.scopes[g]
    const head = positions[g] ?? { x: 0, y: 0 }
    positions[groupHeaderId(g)] = { x: head.x, y: head.y }
    let bottom = head.y + GROUP_HEADER_HEIGHT
    if (scope.config.form === 'workflows') {
      const n = scope.config.tasks.length
      const rowWidth = n * NODE_WIDTH + Math.max(0, n - 1) * TASK_CARD_GAP_X
      const startX = head.x + NODE_WIDTH / 2 - rowWidth / 2
      const y = head.y + GROUP_HEADER_HEIGHT + GROUP_GAP_Y
      scope.config.tasks.forEach((task, i) => {
        positions[groupTaskCardId(g, task.key)] = { x: startX + i * (NODE_WIDTH + TASK_CARD_GAP_X), y }
      })
      if (n > 0) bottom = y + TASK_CARD_HEIGHT
    } else {
      for (const id of plan.members[g]) {
        if (id === groupHeaderId(g) || id === groupFooterId(g)) continue
        const r = memberRect(plan, positions, boxes, id)
        if (r) bottom = Math.max(bottom, r.y + r.height)
      }
    }
    positions[groupFooterId(g)] = { x: head.x, y: bottom + GROUP_GAP_Y }
    const box = computeGroupBox(plan, positions, boxes, g)
    if (box) boxes[g] = box
  }
}

function computeGroupBox(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>, boxes: Record<string, Box>, g: string): Box | null {
  const rects: Box[] = []
  for (const id of plan.members[g]) {
    const r = memberRect(plan, positions, boxes, id)
    if (r) rects.push(r)
  }
  const b = boundsOf(rects)
  if (!b) return null
  return { x: b.x - GROUP_PAD, y: b.y - GROUP_PAD, width: b.width + GROUP_PAD * 2, height: b.height + GROUP_PAD * 2 }
}

/** Container boxes for every drawn group, innermost first so an enclosing box wraps its nested boxes. */
export function groupContainerBoxes(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>): Record<string, Box> {
  const boxes: Record<string, Box> = {}
  const innermostFirst = [...plan.groups].sort((a, b) => plan.depth[b] - plan.depth[a])
  for (const g of innermostFirst) {
    const box = computeGroupBox(plan, positions, boxes, g)
    if (box) boxes[g] = box
  }
  return boxes
}

/**
 * After dagre, re-assign workflow task-card x positions in author order (dagre
 * may permute nodes within a rank to reduce crossings; task order is
 * author-defined and must read left to right).
 */
export function orderTaskCards(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>): void {
  for (const g of plan.groups) {
    const scope = plan.scopes[g]
    if (scope.config.form !== 'workflows') continue
    const ids = scope.config.tasks.map((t) => groupTaskCardId(g, t.key)).filter((id) => positions[id] != null)
    const xs = ids.map((id) => positions[id].x).sort((a, b) => a - b)
    ids.forEach((id, i) => {
      positions[id] = { x: xs[i], y: positions[id].y }
    })
  }
}

/**
 * Maps laid-out React Flow nodes back to DEFINITION positions for
 * saveLayout: a group header saves as its group node; footers, task cards and
 * container boxes are derived presentation and are never persisted.
 */
export function toDefinitionPositions(rfNodes: Node[]): Record<string, { x: number; y: number }> {
  const out: Record<string, { x: number; y: number }> = {}
  for (const n of rfNodes) {
    if (n.type === 'groupFooter' || n.type === 'groupTaskCard' || n.type === 'groupContainer') continue
    if (n.type === 'groupHeader') {
      const def = (n.data as { definitionNodeId?: unknown }).definitionNodeId
      if (typeof def === 'string') out[def] = { x: n.position.x, y: n.position.y }
      continue
    }
    out[n.id] = { x: n.position.x, y: n.position.y }
  }
  return out
}

/** The definition node a rendered React Flow node stands for. */
export function definitionNodeIdOf(n: Pick<Node, 'id' | 'data'>): string {
  const def = (n.data as { definitionNodeId?: unknown } | undefined)?.definitionNodeId
  return typeof def === 'string' ? def : n.id
}
