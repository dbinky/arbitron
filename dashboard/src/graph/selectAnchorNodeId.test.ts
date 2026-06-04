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

describe('selectAnchorNodeId — running instance', () => {
  it('returns currentNodeId when the instance is running', () => {
    const d = def(
      [N('a'), N('b'), N('c')],
      [
        { source: 'a', target: 'b' },
        { source: 'b', target: 'c' },
      ],
    )
    const states: Record<string, NodeExecutionState> = {
      a: 'completed',
      b: 'running',
      c: 'pending',
    }
    const i = inst({ state: 'running', currentNodeId: 'b' })
    expect(selectAnchorNodeId(i, d, states, [exec('a', 'succeeded')])).toBe('b')
  })

  it('returns currentNodeId when the instance is waiting (interactive)', () => {
    const d = def([N('a'), N('b')], [{ source: 'a', target: 'b' }])
    const states: Record<string, NodeExecutionState> = { a: 'completed', b: 'waiting' }
    const i = inst({ state: 'waiting', currentNodeId: 'b' })
    expect(selectAnchorNodeId(i, d, states, [exec('a', 'succeeded')])).toBe('b')
  })

  it('ignores a currentNodeId that is not in the graph (fallback continues)', () => {
    const d = def([N('a'), N('b')], [{ source: 'a', target: 'b' }])
    const i = inst({ state: 'running', currentNodeId: 'ghost' })
    // No completed nodes, not failed: falls through to source node 'a'.
    expect(selectAnchorNodeId(i, d, {}, [])).toBe('a')
  })
})

describe('selectAnchorNodeId — failed instance', () => {
  it('returns failedNodeId when present and in the graph', () => {
    const d = def([N('a'), N('b'), N('c')], [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ])
    const states: Record<string, NodeExecutionState> = {
      a: 'completed',
      b: 'failed',
      c: 'pending',
    }
    const i = inst({ state: 'failed', failedNodeId: 'b' })
    expect(
      selectAnchorNodeId(i, d, states, [exec('a', 'succeeded'), exec('b', 'failed')]),
    ).toBe('b')
  })

  it('falls back to the node marked failed in executionStates when failedNodeId is missing', () => {
    const d = def([N('a'), N('b')], [{ source: 'a', target: 'b' }])
    const states: Record<string, NodeExecutionState> = { a: 'completed', b: 'failed' }
    const i = inst({ state: 'failed' }) // no failedNodeId
    expect(selectAnchorNodeId(i, d, states, [])).toBe('b')
  })
})

describe('selectAnchorNodeId — completed instance', () => {
  it('returns the last completed node by trace order', () => {
    const d = def([N('a'), N('b'), N('c')], [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ])
    const states: Record<string, NodeExecutionState> = {
      a: 'completed',
      b: 'completed',
      c: 'completed',
    }
    const i = inst({ state: 'completed' })
    const t = [
      exec('a', 'succeeded', '2026-01-01T00:00:01Z'),
      exec('b', 'succeeded', '2026-01-01T00:00:02Z'),
      exec('c', 'succeeded', '2026-01-01T00:00:03Z'),
    ]
    expect(selectAnchorNodeId(i, d, states, t)).toBe('c')
  })

  it('falls back to a graph sink node when the trace has no completed nodes', () => {
    const d = def([N('a'), N('b'), N('c')], [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
    ])
    const i = inst({ state: 'completed' })
    // 'c' has no outgoing edge => sink.
    expect(selectAnchorNodeId(i, d, {}, [])).toBe('c')
  })
})

describe('selectAnchorNodeId — empty / degenerate', () => {
  it('returns null when workflowDef is null', () => {
    expect(selectAnchorNodeId(null, null, {}, [])).toBeNull()
  })

  it('returns null when the graph has no nodes', () => {
    expect(selectAnchorNodeId(null, def([]), {}, [])).toBeNull()
  })

  it('returns null for a running instance on an empty graph', () => {
    const i = inst({ state: 'running', currentNodeId: 'a' })
    expect(selectAnchorNodeId(i, def([]), {}, [])).toBeNull()
  })
})

// The design doc only specifies anchor rules for running/waiting, completed, failed,
// and static preview. paused and cancelled fall through to the general fallback
// (source node). These tests pin that behavior explicitly so any future "improvement"
// that adds a specific rule for these states is a deliberate, visible change.
describe('selectAnchorNodeId — edge case: paused and cancelled (general fallback)', () => {
  const d = def(
    [N('a'), N('b'), N('c')],
    [{ source: 'a', target: 'b' }, { source: 'b', target: 'c' }],
  )

  it('paused instance falls back to the source node, not currentNodeId', () => {
    // Design doc lists running/waiting/completed/failed/preview — paused is not
    // a named case, so currentNodeId is intentionally ignored and the source node
    // ('a', the only node with no incoming edge) is returned.
    const i = inst({ state: 'paused', currentNodeId: 'b' })
    expect(selectAnchorNodeId(i, d, {}, [])).toBe('a')
  })

  it('cancelled instance falls back to the source node', () => {
    const i = inst({ state: 'cancelled', currentNodeId: 'b' })
    expect(selectAnchorNodeId(i, d, {}, [])).toBe('a')
  })
})
