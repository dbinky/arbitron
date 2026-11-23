import type { Edge, Node } from '@xyflow/react'
import { describe, expect, it } from 'vitest'
import type { HomeMove, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from './types'
import { applyRunnerMarks, applyRunnerMarksByOwner, buildRunnerMarks, fnv1a, moveBadgeText, runnerColor, RUNNER_PALETTE, SERVER_SIDE_COLOR } from './runnerMarks'

const def: WorkflowDefinition = {
  name: 'wf', description: '', author: '',
  nodes: ['build', 'gate'].map(id => ({ id, type: id === 'gate' ? 'conditional' : 'agent_call', label: id, description: '', config: {} })),
  edges: [],
}
const execution = (instanceId: string, attempt: number, runnerId: string): NodeExecution => ({
  id: `${instanceId}-${attempt}`, instanceId, nodeId: 'build', attempt, status: 'succeeded', startedAt: '2026-11-20T10:00:00Z', runnerId,
  placementHistory: [{ jobId: `${instanceId}-job-${attempt}`, runnerId, reason: 'placed', outcome: 'completed', generation: attempt, at: '2026-11-20T10:00:00Z' }],
})
const move = (job: string | null, generation: number): HomeMove => ({ from: 'builtin', to: 'mac-b', reason: 'exhausted', bytes: 42, generation,
  at: '2026-11-20T10:00:00Z', targetJobId: job, nodeId: 'build', invocationId: 'invocation', operationId: 'node', fromGeneration: generation - 1, toGeneration: generation,
})

describe('buildRunnerMarks', () => {
  it('uses only the owner trace, latest attempt and server-side definition types', () => {
    const marks = buildRunnerMarks('root', def, [execution('root', 2, 'mac-b'), execution('root', 1, 'builtin'), execution('child', 99, 'secret')], undefined)
    expect(marks.nodes.build).toEqual({ runnerId: 'mac-b', color: runnerColor('mac-b'), label: 'mac-b' })
    expect(marks.nodes.gate).toEqual({ runnerId: null, color: SERVER_SIDE_COLOR, label: 'server' })
    expect(buildRunnerMarks('root', null, [], undefined)).toEqual({ instanceId: 'root', nodes: {}, moveInto: {} })
    expect(buildRunnerMarks('root', def, [], undefined).nodes.build).toBeUndefined()
  })

  it('retains ordered exact-job move history across retries, never time-matches drain moves', () => {
    const first = move('root-job-1', 2), second = move('root-job-2', 3)
    const marks = buildRunnerMarks('root', def, [execution('root', 1, 'builtin'), execution('root', 2, 'mac-b')], [second, move(null, 4), move('child-job-1', 5), first])
    expect(marks.moveInto).toEqual({ build: [first, second] })
    expect(marks.nodes.build.moved).toEqual(second)
    expect(moveBadgeText(first)).toBe('workspace moved builtin → mac-b')
    expect(buildRunnerMarks('root', def, [execution('root', 1, 'builtin')], [{ ...first, nodeId: 'other' }]).moveInto).toEqual({})
  })
})

describe('applyRunnerMarksByOwner', () => {
  it('never falls back to root for an unowned tile and clears withdrawn marks', () => {
    const root = buildRunnerMarks('root', def, [execution('root', 1, 'builtin')], undefined)
    const node: Node = { id: 'tile:build', type: 'arrangedNode', position: { x: 0, y: 0 }, data: { definitionNodeId: 'build' } }
    expect(applyRunnerMarksByOwner([node], [], { root }, 'root').nodes[0].data.runnerMark).toBeUndefined()
    const hidden = { ...node, id: 'build', data: { ...node.data, instanceId: null } }
    expect(applyRunnerMarksByOwner([hidden], [], { root }, 'root').nodes[0].data.runnerMark).toBeUndefined()
    const stale = { ...node, data: { ...node.data, instanceId: 'child', runnerMark: root.nodes.build } }
    expect(applyRunnerMarksByOwner([stale], [], { root }, 'root').nodes[0].data.runnerMark).toBeUndefined()
  })

  it('uses execution ownership rather than the enclosing layout tile for branch nodes', () => {
    const root = buildRunnerMarks('root', def, [execution('root', 1, 'builtin')], undefined)
    const branch = buildRunnerMarks('branch', def, [execution('branch', 1, 'mac-b')], undefined)
    const node: Node = { id: 'tile:build', type: 'arrangedNode', position: { x: 0, y: 0 }, data: { definitionNodeId: 'build', layoutInstanceId: 'root', instanceId: 'branch' } }
    expect(applyRunnerMarksByOwner([node], [], { root, branch }, 'root').nodes[0].data.runnerMark).toMatchObject({ runnerId: 'mac-b' })
  })

  it('joins by rendered owner plus definition node, preserving all move badges and unrelated nodes', () => {
    const moves = [move('root-job-1', 2), move('root-job-1', 3)]
    const root = buildRunnerMarks('root', def, [execution('root', 1, 'builtin')], moves)
    const child = buildRunnerMarks('child', def, [execution('child', 1, 'mac-c')], undefined)
    const nodes: Node[] = [
      { id: 'build', type: 'arbitronNode', position: { x: 0, y: 0 }, data: {} },
      { id: 'child:build', type: 'arrangedNode', position: { x: 0, y: 0 }, data: { definitionNodeId: 'build', layoutInstanceId: 'child' } },
      { id: 'hidden:build', type: 'arrangedNode', position: { x: 0, y: 0 }, data: { definitionNodeId: 'build', instanceId: 'hidden' } },
      { id: 'header', type: 'groupHeader', position: { x: 0, y: 0 }, data: { definitionNodeId: 'build' } },
    ]
    const edges: Edge[] = [
      { id: 'r', source: 'gate', target: 'build', type: 'routedEdge', data: { route: {} } },
      { id: 'f', source: 'gate', target: 'build', type: 'smoothstep', label: 'yes' },
      { id: 'c', source: 'gate', target: 'child:build' },
      { id: 'loop', source: 'build', target: 'build', type: 'loopBackEdge' },
      { id: 'arranged-loop', source: 'build', target: 'build', type: 'routedEdge', data: { route: { kind: 'loop_return' } } },
    ]
    const out = applyRunnerMarksByOwner(nodes, edges, { root, child }, 'root')
    expect(out.nodes[0].data.runnerMark).toMatchObject({ runnerId: 'builtin' })
    expect(out.nodes[1].data.runnerMark).toMatchObject({ runnerId: 'mac-c' })
    expect(out.nodes[2]).toBe(nodes[2])
    expect(out.nodes[3]).toBe(nodes[3])
    expect(out.edges[0].data).toMatchObject({ route: {}, moves, moveBadge: 'workspace moved builtin → mac-b · workspace moved builtin → mac-b' })
    expect(out.edges[0].label).toBeUndefined()
    expect(out.edges[1].label).toBe('yes · workspace moved builtin → mac-b · workspace moved builtin → mac-b')
    expect(out.edges[2]).toBe(edges[2])
    expect(out.edges[3]).toBe(edges[3])
    expect(out.edges[4]).toBe(edges[4])
    expect(nodes[0].data.runnerMark).toBeUndefined()
    expect(applyRunnerMarks(nodes, edges, undefined)).toEqual({ nodes, edges })
    expect(applyRunnerMarks(nodes, edges, undefined).nodes).toBe(nodes)
    expect(applyRunnerMarks(nodes, edges, root).nodes[1]).toBe(nodes[1])
  })
})

describe('runner colours', () => {
  it('uses stable FNV-1a colours distinct from server grey', () => {
    expect(fnv1a('')).toBe(2166136261)
    expect(fnv1a('hello')).toBe(1335831723)
    expect(runnerColor('mac-a')).toBe(runnerColor('mac-a'))
    expect(RUNNER_PALETTE).toHaveLength(8)
    expect(RUNNER_PALETTE).toContain(runnerColor('builtin'))
    expect(RUNNER_PALETTE).not.toContain(SERVER_SIDE_COLOR)
  })
})
