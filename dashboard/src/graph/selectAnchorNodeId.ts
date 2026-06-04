import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from './types'

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
  _executionStates: Record<string, NodeExecutionState>,
  _trace: NodeExecution[],
): string | null {
  if (!workflowDef || workflowDef.nodes.length === 0) return null

  // Static preview: no instance.
  if (!instance) {
    const entry = workflowDef.nodes.find((n) => n.entrypoint === true)
    if (entry) return entry.id
  }

  return null
}
