import { describe, it, expect } from 'vitest'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from './types'
import { selectAnchorNodeId } from './selectAnchorNodeId'

// --- fixtures -------------------------------------------------------------

function def(
  nodes: WorkflowDefinition['nodes'],
  edges: WorkflowDefinition['edges'] = [],
): WorkflowDefinition {
  return { name: 'wf', description: '', author: '', nodes, edges }
}

const N = (
  id: string,
  extra: Partial<WorkflowDefinition['nodes'][number]> = {},
): WorkflowDefinition['nodes'][number] => ({
  id,
  type: 'llm_call',
  label: id.toUpperCase(),
  description: '',
  config: {},
  ...extra,
})

const inst = (extra: Partial<Instance>): Instance =>
  ({
    id: 'i1',
    ownerId: 'u1',
    workflowName: 'wf',
    priority: 'normal',
    variables: {},
    originalVariables: {},
    createdAt: '',
    updatedAt: '',
    interactiveActive: false,
    interactiveNodeId: null,
    ...extra,
  }) as Instance

const exec = (
  nodeId: string,
  status: NodeExecution['status'],
  completedAt?: string,
): NodeExecution =>
  ({
    id: `e-${nodeId}`,
    instanceId: 'i1',
    nodeId,
    attempt: 1,
    status,
    startedAt: '2026-01-01T00:00:00Z',
    completedAt,
  }) as NodeExecution

const noStates: Record<string, NodeExecutionState> = {}

// --- static preview -------------------------------------------------------

describe('selectAnchorNodeId — static preview (no instance)', () => {
  it('returns the entrypoint node when one is flagged', () => {
    const d = def(
      [N('a'), N('b', { entrypoint: true }), N('c')],
      [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'c' },
      ],
    )
    expect(selectAnchorNodeId(null, d, noStates, [])).toBe('b')
  })

  it('falls back to the source node (no incoming edge) when no entrypoint flag', () => {
    const d = def(
      [N('a'), N('b'), N('c')],
      [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'c' },
      ],
    )
    // 'a' has no incoming edge => it is the source.
    expect(selectAnchorNodeId(null, d, noStates, [])).toBe('a')
  })

  it('falls back to the first node when there is no clear source', () => {
    // A 2-cycle: every node has an incoming edge, so there is no source.
    const d = def(
      [N('x'), N('y')],
      [
        { source: 'x', target: 'y' },
        { source: 'y', target: 'x' },
      ],
    )
    expect(selectAnchorNodeId(null, d, noStates, [])).toBe('x')
  })
})
