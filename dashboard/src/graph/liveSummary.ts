import type { ExecutionContext, ExecutionContextKind, GraphState, LiveNodeStateValue } from '../api/types'
import type { WorkflowDefinition } from './types'
import { activeNodeLabels, qualifiedKey } from './aggregateState'

/** A definition with no nodes: the label fallback when a run's snapshot is unreadable. */
export const EMPTY_DEFINITION: WorkflowDefinition = { name: '', description: '', author: '', nodes: [], edges: [] }

/** Node states that count as active work (queued and paused work is still unfinished). */
const ACTIVE_NODE_STATES = new Set<LiveNodeStateValue>(['queued', 'running', 'waiting', 'paused'])

/** Run states whose hierarchy row carries an active-state summary. */
const ACTIVE_RUN_STATES = new Set<string>(['queued', 'running', 'waiting', 'paused'])

/** One currently active node of an aggregate, identified by (instanceId, nodeId). */
export interface ActiveNodeRef {
  /** qualifiedKey(instanceId, nodeId): repeated child workflows reuse node ids, so nodeId alone is never a key. */
  key: string
  instanceId: string
  nodeId: string
  /** Label from the owning context's snapshot (walking up to the nearest ancestor that has one), else the node id. */
  label: string
  state: LiveNodeStateValue
  /** Task key of the group task the owning context runs; null for the selected run itself or a plain spawn. */
  taskKey: string | null
  /** The owning task continues after its group was decided (finish_independently). */
  independent: boolean
}

/** Branch / task / active-state annotation of one CHILD row in the queue hierarchy. */
export interface HierarchyMarker {
  /** branch | workflow (group tasks) | spawn (plain spawned child). */
  kind: ExecutionContextKind
  /** The parent's parallel_group node that launched this child; null for a plain spawn. */
  groupNodeId: string | null
  /** That group node's label in the parent's snapshot (falls back to its id). */
  groupLabel: string | null
  taskKey: string | null
  /** False once a group decision released the task (or for a plain spawn). */
  blocking: boolean
  /** Continues after its group was decided (finish_independently): non-blocking work. */
  independent: boolean
  /** "<state>: <active node labels of the child and its descendants>", or just the state; null when terminal. */
  activeSummary: string | null
}

type NodeRef = { id: string; label?: string }

function isDefinition(g: unknown): g is { nodes: NodeRef[]; edges: unknown[] } {
  const d = g as { nodes?: unknown; edges?: unknown } | null | undefined
  return Array.isArray(d?.nodes) && Array.isArray(d?.edges)
}

/**
 * The selected run's own workflow snapshot: the graph of its (non-redacted)
 * root context. Null when absent or unreadable — callers must not draw an
 * empty-but-valid graph for a snapshot they could not read.
 */
export function rootDefinitionOf(gs: GraphState | null): WorkflowDefinition | null {
  if (!gs) return null
  const root = gs.contexts.find((c) => c.instanceId === gs.rootInstanceId && !c.redacted)
  if (!root || !isDefinition(root.graph)) return null
  return { ...EMPTY_DEFINITION, ...(root.graph as unknown as WorkflowDefinition) }
}

/** Child instance ids whose task continues independently after its group decision. */
function independentChildren(gs: GraphState): Set<string> {
  const out = new Set<string>()
  for (const p of gs.groups) {
    for (const t of p.tasks) {
      if (t.continuingIndependently && !t.redacted && t.childInstanceId) out.add(t.childInstanceId)
    }
  }
  return out
}

/**
 * Every queued, running, waiting or paused node of every VISIBLE context of the
 * aggregate, in context order. Keys are instance-qualified so the same node id
 * in two child runs of one workflow stays two entries. Redacted contexts are
 * skipped entirely (no id, no node, no label).
 */
export function activeNodesOf(gs: GraphState, rootDef: WorkflowDefinition): ActiveNodeRef[] {
  const byId = new Map(gs.contexts.filter((c) => !c.redacted).map((c) => [c.instanceId, c]))
  const independent = independentChildren(gs)
  const nodesFor = (ctx: ExecutionContext): NodeRef[] => {
    let cur: ExecutionContext | undefined = ctx
    const seen = new Set<string>()
    while (cur && !seen.has(cur.instanceId)) {
      seen.add(cur.instanceId)
      if (cur.graph) return isDefinition(cur.graph) ? cur.graph.nodes : []
      cur = cur.parentInstanceId ? byId.get(cur.parentInstanceId) : undefined
    }
    return rootDef.nodes
  }
  const out: ActiveNodeRef[] = []
  for (const ctx of gs.contexts) {
    if (ctx.redacted) continue
    const nodes = nodesFor(ctx)
    for (const n of ctx.nodes) {
      if (!ACTIVE_NODE_STATES.has(n.state)) continue
      out.push({
        key: qualifiedKey(ctx.instanceId, n.nodeId),
        instanceId: ctx.instanceId,
        nodeId: n.nodeId,
        label: nodes.find((d) => d.id === n.nodeId)?.label || n.nodeId,
        state: n.state,
        taskKey: ctx.taskKey || null,
        independent: independent.has(ctx.instanceId),
      })
    }
  }
  return out
}

/**
 * Markers for the DIRECT children of `parentId`, keyed by child instance id,
 * from the parent's authorized aggregate (`GET /instances/{parentId}/graph-state`).
 * Redacted children are never keyed: the list endpoint never returns them
 * either, and their placeholder id must not surface. A child's active summary
 * includes its visible descendants' running/waiting nodes (child relationships
 * roll up), resolved per instance so repeated node ids never cross.
 */
export function buildHierarchyMarkers(gs: GraphState, parentId: string): Record<string, HierarchyMarker> {
  const def = rootDefinitionOf(gs) ?? EMPTY_DEFINITION
  const panels = new Map(gs.groups.map((p) => [p.groupRunId, p]))
  const out: Record<string, HierarchyMarker> = {}
  for (const ctx of gs.contexts) {
    if (ctx.redacted || ctx.parentInstanceId !== parentId || ctx.instanceId === parentId) continue
    const panel = ctx.groupRunId ? panels.get(ctx.groupRunId) : undefined
    const panelTask = panel?.tasks.find((t) => t.childInstanceId === ctx.instanceId)
    const groupNodeId = panel?.nodeId ?? null
    let activeSummary: string | null = null
    if (ACTIVE_RUN_STATES.has(ctx.state)) {
      const labels = activeNodeLabels(gs, ctx.instanceId, def)
      activeSummary = labels.length > 0 ? `${ctx.state}: ${labels.join(', ')}` : ctx.state
    }
    out[ctx.instanceId] = {
      kind: ctx.kind,
      groupNodeId,
      groupLabel: groupNodeId ? def.nodes.find((n) => n.id === groupNodeId)?.label || groupNodeId : null,
      taskKey: ctx.taskKey || null,
      blocking: ctx.blocking,
      independent: panelTask?.continuingIndependently === true,
      activeSummary,
    }
  }
  return out
}
