import type { Node } from '@xyflow/react'
import type { ExecutionContext, GraphState, GroupGraph, GroupScope } from '../api/types'
import type { NodeExecutionState, WorkflowDefinition, WorkflowEdge, WorkflowNode } from './types'
import type { GroupLiveSummary, NodeContextInfo } from './groupLayout'
import { buildAggregateView, qualifiedKey, type AggregateView } from './aggregateState'

/**
 * Everything the live renderer draws: the root definition plus, for every
 * visible child workflow whose own graph holds parallel groups, that graph
 * with each node id qualified by the child's instance id (child workflows
 * reuse node ids, so two runs of one workflow — or a child's `start` and the
 * root's — must never alias).
 */
export interface LiveGraph {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  groups: GroupGraph | null
  executionStates: Record<string, NodeExecutionState>
  nodeContexts: Record<string, NodeContextInfo>
  groupLive: Record<string, GroupLiveSummary>
  /** Qualified rendered node id -> the id the server knows it by. Empty without nested graphs. */
  originOf: Record<string, string>
}

const NO_ORIGINS: Record<string, string> = {}

function isDefinition(g: unknown): g is { nodes: WorkflowNode[]; edges: WorkflowEdge[] } {
  const d = g as { nodes?: unknown; edges?: unknown } | null | undefined
  return Array.isArray(d?.nodes) && Array.isArray(d?.edges)
}

function qualifyScope(scope: GroupScope, q: (id: string) => string): GroupScope {
  return {
    ...scope,
    nodeId: q(scope.nodeId),
    config: { ...scope.config, tasks: scope.config.tasks.map((t) => (t.entry ? { ...t, entry: q(t.entry) } : t)) },
    parent: scope.parent ? { ...scope.parent, groupNodeId: q(scope.parent.groupNodeId) } : undefined,
    bodies: Object.fromEntries(Object.entries(scope.bodies).map(([k, ids]) => [k, ids.map(q)])),
    nested: scope.nested.map(q),
    successTarget: scope.successTarget ? q(scope.successTarget) : '',
    failureTarget: scope.failureTarget ? q(scope.failureTarget) : '',
  }
}

/** The visible contexts whose own graph holds groups: the ones the root graph cannot draw. */
function nestedContexts(gs: GraphState): ExecutionContext[] {
  return gs.contexts.filter(
    (c) => c.instanceId !== gs.rootInstanceId && !c.redacted && isDefinition(c.graph) && (c.layout?.order.length ?? 0) > 0,
  )
}

/**
 * Merges every visible child workflow's grouped graph into the root's so one
 * layout draws them together. Each child is resolved against ITS OWN layout
 * (root = the child instance), then re-keyed by the qualified node id.
 */
export function composeLiveGraph(gs: GraphState, rootDef: WorkflowDefinition, aggregate: AggregateView): LiveGraph {
  const rootGroups = gs.layout ?? null
  const base: LiveGraph = {
    nodes: rootDef.nodes,
    edges: rootDef.edges,
    groups: rootGroups,
    executionStates: aggregate.executionStates,
    nodeContexts: aggregate.nodeContexts,
    groupLive: aggregate.groupLive,
    originOf: NO_ORIGINS,
  }
  const children = nestedContexts(gs)
  if (children.length === 0) return base

  const groups: GroupGraph = rootGroups
    ? { groups: { ...rootGroups.groups }, owner: { ...rootGroups.owner }, order: [...rootGroups.order] }
    : { groups: {}, owner: {}, order: [] }
  const out: LiveGraph = {
    nodes: [...rootDef.nodes],
    edges: [...rootDef.edges],
    groups,
    executionStates: { ...aggregate.executionStates },
    nodeContexts: { ...aggregate.nodeContexts },
    groupLive: { ...aggregate.groupLive },
    originOf: {},
  }
  for (const ctx of children) {
    const def = ctx.graph as unknown as WorkflowDefinition
    const layout = ctx.layout as GroupGraph
    const q = (id: string) => qualifiedKey(ctx.instanceId, id)
    const view = buildAggregateView({ ...gs, rootInstanceId: ctx.instanceId, layout }, def)
    for (const n of def.nodes) {
      // Stored positions belong to the child's own canvas; they would pile onto the root's.
      out.nodes.push({ ...n, id: q(n.id), position: undefined })
      out.originOf[q(n.id)] = n.id
      out.executionStates[q(n.id)] = view.executionStates[n.id]
      if (view.nodeContexts[n.id]) out.nodeContexts[q(n.id)] = view.nodeContexts[n.id]
    }
    for (const e of def.edges) out.edges.push({ ...e, source: q(e.source), target: q(e.target) })
    for (const [gid, scope] of Object.entries(layout.groups)) groups.groups[q(gid)] = qualifyScope(scope, q)
    for (const [nid, owner] of Object.entries(layout.owner)) groups.owner[q(nid)] = { ...owner, groupNodeId: q(owner.groupNodeId) }
    for (const gid of layout.order) {
      groups.order.push(q(gid))
      if (view.groupLive[gid]) out.groupLive[q(gid)] = view.groupLive[gid]
    }
  }
  return out
}

/**
 * Gives every rendered node of a nested graph back the id the server knows it
 * by (`definitionNodeId`), so a click selects (instance, original node id).
 */
export function withOriginalIds(rfNodes: Node[], originOf: Record<string, string>): Node[] {
  if (Object.keys(originOf).length === 0) return rfNodes
  return rfNodes.map((n) => {
    const data = (n.data ?? {}) as { definitionNodeId?: unknown }
    const key = typeof data.definitionNodeId === 'string' ? data.definitionNodeId : n.id
    const original = originOf[key]
    return original ? { ...n, data: { ...n.data, definitionNodeId: original } } : n
  })
}
