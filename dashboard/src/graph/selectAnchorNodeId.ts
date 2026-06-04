import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from './types'

function sourceNodeId(def: WorkflowDefinition): string | null {
  const hasIncoming = new Set(def.edges.map((e) => e.target))
  const source = def.nodes.find((n) => !hasIncoming.has(n.id))
  return source ? source.id : null
}

function nodeIdSet(def: WorkflowDefinition): Set<string> {
  return new Set(def.nodes.map((n) => n.id))
}

/**
 * Pick the id of the real step node the viewport should center on.
 *
 * Returns null when there is no sensible anchor (empty graph). Loop-container
 * overlay nodes are not candidates because they never appear in
 * `workflowDef.nodes` — they are synthesized during layout.
 */
export function selectAnchorNodeId(
  instance: Instance | null,
  workflowDef: WorkflowDefinition | null,
  executionStates: Record<string, NodeExecutionState>,
  _trace: NodeExecution[],
): string | null {
  if (!workflowDef || workflowDef.nodes.length === 0) return null

  // Static preview: no instance.
  if (!instance) {
    const entry = workflowDef.nodes.find((n) => n.entrypoint === true)
    if (entry) return entry.id
    return sourceNodeId(workflowDef) ?? workflowDef.nodes[0].id
  }

  const ids = nodeIdSet(workflowDef)

  // Running / waiting: the active node.
  if (instance.state === 'running' || instance.state === 'waiting') {
    if (instance.currentNodeId && ids.has(instance.currentNodeId)) {
      return instance.currentNodeId
    }
  }

  // Failed: the failed node.
  if (instance.state === 'failed') {
    if (instance.failedNodeId && ids.has(instance.failedNodeId)) {
      return instance.failedNodeId
    }
    const markedFailed = workflowDef.nodes.find(
      (n) => executionStates[n.id] === 'failed',
    )
    if (markedFailed) return markedFailed.id
  }

  // General fallback for any instance with no specific anchor.
  return sourceNodeId(workflowDef) ?? workflowDef.nodes[0].id
}
