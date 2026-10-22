import type { Node } from '@xyflow/react'
import type {
  ExecutionContext,
  GraphState,
  GroupGraph,
  GroupPanel,
  LiveNodeStateValue,
} from '../api/types'
import type { NodeExecutionState, WorkflowDefinition } from './types'
import type { GroupLiveSummary, GroupLiveTask, NodeContextInfo } from './groupLayout'

/** Instance-qualified node identity: child workflows reuse node ids, so nodeId alone is never a key. */
export function qualifiedKey(instanceId: string, nodeId: string): string {
  return `${instanceId}:${nodeId}`
}

const LIVE_TO_EXEC: Record<LiveNodeStateValue, NodeExecutionState> = {
  pending: 'pending',
  queued: 'queued',
  running: 'running',
  waiting: 'waiting',
  paused: 'paused',
  completed: 'completed',
  failed: 'failed',
  skipped: 'skipped',
  cancelled: 'cancelled',
  unknown: 'unknown',
}

/** Every live node state of every visible context, keyed `${instanceId}:${nodeId}`. */
export function buildAggregateExecutionStates(gs: GraphState): Record<string, NodeExecutionState> {
  const out: Record<string, NodeExecutionState> = {}
  for (const ctx of gs.contexts) {
    for (const n of ctx.nodes) {
      out[qualifiedKey(n.instanceId || ctx.instanceId, n.nodeId)] = LIVE_TO_EXEC[n.state] ?? 'unknown'
    }
  }
  return out
}

/** The panel of the group node `nodeId` executed by `instanceId`. */
export function findGroupPanel(gs: GraphState | null, instanceId: string | null, nodeId: string): GroupPanel | null {
  if (!gs || !instanceId) return null
  return gs.groups.find((p) => p.instanceId === instanceId && p.nodeId === nodeId) ?? null
}

/**
 * The (group node, task) that owns the root context's nodes when the root is a
 * branch-task instance; null for an ordinary run. Read from the nodes the server
 * says the root executes, because the breadcrumb (taskKey) is stripped when the
 * parent is not viewable.
 */
function rootBodyOwner(gs: GraphState, layout: GroupGraph | null): { groupNodeId: string; taskKey: string } | null {
  const rootCtx = gs.contexts.find((c) => c.instanceId === gs.rootInstanceId)
  for (const n of rootCtx?.nodes ?? []) {
    const owner = layout?.owner[n.nodeId]
    if (owner) return owner
  }
  return null
}

/**
 * Which instance executes each definition node of the ROOT graph. Main-path
 * nodes run in the root; a body node runs in the branch instance of its owning
 * (group, task), resolved outermost-first through Phase 1 ownership so nested
 * groups map recursively: root --g1/b--> branch B --g2/x--> branch X.
 * An unlaunched task yields instanceId null; a redacted one hides it.
 */
export function resolveNodeContexts(gs: GraphState, def: WorkflowDefinition, layout: GroupGraph | null): Record<string, NodeContextInfo> {
  const contexts = new Map(gs.contexts.map((c) => [c.instanceId, c]))
  const out: Record<string, NodeContextInfo> = {}
  const root = gs.rootInstanceId
  const ownerChain = (nodeId: string): Array<{ groupNodeId: string; taskKey: string }> => {
    const chain: Array<{ groupNodeId: string; taskKey: string }> = []
    let cur = layout?.owner[nodeId]
    const seen = new Set<string>()
    while (cur && !seen.has(cur.groupNodeId)) {
      seen.add(cur.groupNodeId)
      chain.unshift(cur)
      cur = layout?.owner[cur.groupNodeId]
    }
    return chain
  }
  // A branch-task instance opened as the route root executes only the nodes its
  // own (group, task) owns; the server emits no panel for that parent-owned
  // group, so its step must be treated as already resolved to the root.
  const rootAnchor = rootBodyOwner(gs, layout)
  for (const n of def.nodes) {
    let instanceId: string | null = root
    let independent = false
    let hidden = false
    let chain = ownerChain(n.id)
    if (rootAnchor && chain.length > 0) {
      const at = chain.findIndex((s) => s.groupNodeId === rootAnchor.groupNodeId && s.taskKey === rootAnchor.taskKey)
      if (at < 0) {
        out[n.id] = { instanceId: null, independent: false, hiddenDependency: false }
        continue
      }
      chain = chain.slice(at + 1)
    }
    for (const step of chain) {
      const panel = findGroupPanel(gs, instanceId, step.groupNodeId)
      const task = panel?.tasks.find((t) => t.key === step.taskKey)
      if (!task) {
        instanceId = null
        break
      }
      if (task.redacted) {
        instanceId = null
        hidden = true
        break
      }
      // A decision releases EVERY task (blocking=false); only a task left
      // running on its own after a first-success decision is independent work.
      if (task.continuingIndependently) independent = true
      instanceId = task.childInstanceId ?? null
      if (!instanceId) break
      if (contexts.get(instanceId)?.redacted) {
        instanceId = null
        hidden = true
        break
      }
    }
    out[n.id] = { instanceId, independent, hiddenDependency: hidden }
  }
  return out
}

/** Projects the qualified states onto the rendered (definition-id keyed) graph. */
export function projectExecutionStates(
  qualified: Record<string, NodeExecutionState>,
  nodeContexts: Record<string, NodeContextInfo>,
  def: WorkflowDefinition,
): Record<string, NodeExecutionState> {
  const out: Record<string, NodeExecutionState> = {}
  for (const n of def.nodes) {
    const c = nodeContexts[n.id]
    if (c?.hiddenDependency) {
      out[n.id] = 'unknown'
      continue
    }
    if (!c?.instanceId) {
      out[n.id] = 'pending'
      continue
    }
    out[n.id] = qualified[qualifiedKey(c.instanceId, n.id)] ?? 'pending'
  }
  return out
}

function nodesOf(graph: Record<string, unknown> | undefined): Array<{ id: string; label?: string }> {
  const raw = graph?.nodes
  return Array.isArray(raw) ? (raw as Array<{ id: string; label?: string }>) : []
}

/** Labels of the running/waiting nodes of `instanceId` and all of its visible descendants. */
export function activeNodeLabels(gs: GraphState, instanceId: string, rootDef: WorkflowDefinition): string[] {
  const byId = new Map(gs.contexts.map((c) => [c.instanceId, c]))
  const graphOf = (ctx: ExecutionContext): Array<{ id: string; label?: string }> => {
    let cur: ExecutionContext | undefined = ctx
    const seen = new Set<string>()
    while (cur && !seen.has(cur.instanceId)) {
      seen.add(cur.instanceId)
      if (cur.graph) return nodesOf(cur.graph)
      cur = cur.parentInstanceId ? byId.get(cur.parentInstanceId) : undefined
    }
    return rootDef.nodes
  }
  const inSubtree = (ctx: ExecutionContext): boolean => {
    let cur: ExecutionContext | undefined = ctx
    const seen = new Set<string>()
    while (cur && !seen.has(cur.instanceId)) {
      seen.add(cur.instanceId)
      if (cur.instanceId === instanceId) return true
      cur = cur.parentInstanceId ? byId.get(cur.parentInstanceId) : undefined
    }
    return false
  }
  const labels: string[] = []
  for (const ctx of gs.contexts) {
    if (ctx.redacted || !inSubtree(ctx)) continue
    const nodes = graphOf(ctx)
    for (const n of ctx.nodes) {
      if (n.state !== 'running' && n.state !== 'waiting') continue
      labels.push(nodes.find((d) => d.id === n.nodeId)?.label || n.nodeId)
    }
  }
  return labels
}

/**
 * Label of `nodeId` as executed by `instanceId`: the nearest snapshot at or
 * above that instance names it (a child workflow has its own graph; a branch
 * shares its parent's), else the root definition, else the bare id.
 */
export function nodeLabelIn(gs: GraphState | null, rootDef: WorkflowDefinition | null, instanceId: string | null, nodeId: string): string {
  const byId = new Map((gs?.contexts ?? []).map((c) => [c.instanceId, c]))
  const seen = new Set<string>()
  let cur = instanceId ? byId.get(instanceId) : undefined
  while (cur && !seen.has(cur.instanceId)) {
    seen.add(cur.instanceId)
    if (cur.graph) return nodesOf(cur.graph).find((d) => d.id === nodeId)?.label || nodeId
    cur = cur.parentInstanceId ? byId.get(cur.parentInstanceId) : undefined
  }
  return rootDef?.nodes.find((d) => d.id === nodeId)?.label || nodeId
}

/** Human text of a group's file-gathering / resolution activity, if any. */
export function gatherActivity(panel: GroupPanel): string | undefined {
  const g = panel.gather
  // A failed rollback suspends the run in `gathering`; it is not progress.
  if (g?.rollbackStatus === 'rollback_failed') return g.error ? `Rollback failed: ${g.error}` : 'Rollback failed'
  if (panel.state === 'resolving_conflicts') {
    return g?.activeSession ? `Resolving conflicts (attempt ${g.activeSession.attempt})` : 'Resolving conflicts'
  }
  if (panel.state === 'gathering') {
    if (g && g.tasks.length > 0) {
      const merged = g.tasks.filter((t) => t.merged).length
      return `Gathering files (${merged}/${g.tasks.length} merged)`
    }
    return 'Gathering files'
  }
  if (g?.state === 'failed') return g.error ? `File gathering failed: ${g.error}` : 'File gathering failed'
  if (g?.state === 'rolled_back') return 'File gathering rolled back'
  return undefined
}

/** Live overlays for every group node of the root layout, keyed by group node id. */
export function buildGroupLive(
  gs: GraphState,
  nodeContexts: Record<string, NodeContextInfo>,
  layout: GroupGraph | null,
  rootDef: WorkflowDefinition,
): Record<string, GroupLiveSummary> {
  const out: Record<string, GroupLiveSummary> = {}
  for (const gid of layout?.order ?? []) {
    const instanceId = nodeContexts[gid]?.instanceId ?? null
    const panel = findGroupPanel(gs, instanceId, gid)
    if (!panel) continue
    const tasks: Record<string, GroupLiveTask> = {}
    for (const t of panel.tasks) {
      const child = t.redacted ? null : t.childInstanceId ?? null
      tasks[t.key] = {
        key: t.key,
        status: t.liveStatus,
        frozenStatus: t.frozenStatus,
        childInstanceId: child,
        blocking: t.blocking,
        continuingIndependently: t.continuingIndependently,
        cancelRequested: t.cancelRequested,
        redacted: t.redacted,
        launchError: t.launchError || undefined,
        winner: panel.winner === t.key,
        activeNodes: child ? activeNodeLabels(gs, child, rootDef) : [],
      }
    }
    out[gid] = {
      instanceId: panel.instanceId,
      groupRunId: panel.groupRunId,
      state: panel.state,
      progress: panel.progress,
      winner: panel.winner || undefined,
      activity: gatherActivity(panel),
      waitingReason: panel.waitingReasons[0],
      tasks,
    }
  }
  return out
}

/** Everything GraphView needs from an aggregate, derived once per GraphState version. */
export interface AggregateView {
  qualifiedStates: Record<string, NodeExecutionState>
  executionStates: Record<string, NodeExecutionState>
  nodeContexts: Record<string, NodeContextInfo>
  groupLive: Record<string, GroupLiveSummary>
}

export function buildAggregateView(gs: GraphState, def: WorkflowDefinition): AggregateView {
  const layout = gs.layout ?? null
  const qualifiedStates = buildAggregateExecutionStates(gs)
  const nodeContexts = resolveNodeContexts(gs, def, layout)
  return {
    qualifiedStates,
    executionStates: projectExecutionStates(qualifiedStates, nodeContexts, def),
    nodeContexts,
    groupLive: buildGroupLive(gs, nodeContexts, layout, def),
  }
}

/** What a click on the graph selected: a step node (with its owning instance) or a group. */
export type GraphSelection =
  | { kind: 'node'; rfId: string; nodeId: string; instanceId: string | null }
  | { kind: 'group'; rfId: string; nodeId: string; instanceId: string | null }

const GROUP_TYPES = new Set(['groupHeader', 'groupFooter', 'groupTaskCard', 'groupLane'])

/**
 * Selection of a clicked React Flow node. Header, footer and task cards select
 * their GROUP (same panel); a step node selects (owning instance, node). A
 * node without owning-instance info (static/legacy graph) belongs to
 * `fallbackInstanceId`; an explicit null (unlaunched/redacted owner) stays null.
 */
export function selectionFromNode(n: Pick<Node, 'id' | 'type' | 'data'>, fallbackInstanceId: string | null): GraphSelection {
  const data = (n.data ?? {}) as { definitionNodeId?: unknown; instanceId?: unknown }
  const instanceId =
    'instanceId' in data ? (typeof data.instanceId === 'string' ? data.instanceId : null) : fallbackInstanceId
  if (n.type && GROUP_TYPES.has(n.type) && typeof data.definitionNodeId === 'string') {
    return { kind: 'group', rfId: n.id, nodeId: data.definitionNodeId, instanceId }
  }
  // A node of a child workflow's graph is rendered under a qualified id but selected by the server's id.
  const nodeId = typeof data.definitionNodeId === 'string' ? data.definitionNodeId : n.id
  return { kind: 'node', rfId: n.id, nodeId, instanceId }
}
