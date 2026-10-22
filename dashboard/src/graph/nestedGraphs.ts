import type { ExecutionContext, GraphState } from '../api/types'
import type { WorkflowEdge, WorkflowNode } from './types'

function isDefinition(g: unknown): g is { nodes: WorkflowNode[]; edges: WorkflowEdge[] } {
  const d = g as { nodes?: unknown; edges?: unknown } | null | undefined
  return Array.isArray(d?.nodes) && Array.isArray(d?.edges)
}

/** The visible contexts whose own graph holds groups: the ones the root graph cannot draw. */
export function nestedContexts(gs: GraphState): ExecutionContext[] {
  return gs.contexts.filter(
    (c) => c.instanceId !== gs.rootInstanceId && (c.kind === 'workflow' || c.kind === 'spawn') &&
      !c.redacted && isDefinition(c.graph) && c.graph.nodes.length > 0 && (c.layout?.order.length ?? 0) > 0,
  )
}
