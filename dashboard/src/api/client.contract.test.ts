import { describe, it, expect, vi, afterEach } from 'vitest'
import { api } from './client'
import type { GraphState } from './types'
// Go-serialized engine.GraphState, kept byte-identical to the Go encoder by
// domain/engine/graph_state_wire_test.go (regenerate with
// UPDATE_GRAPH_STATE_WIRE=1 go test ./domain/engine -run WireFixture).
import wire from './fixtures/graph-state.wire.json'

const ROOT = '00000000-0000-0000-0000-000000000001'
const BRANCH = '00000000-0000-0000-0000-00000000000a'
const CHILD = '00000000-0000-0000-0000-00000000000c'
const RUN = '00000000-0000-0000-0000-0000000000d1'

describe('api.instances.graphState against the Go-serialized wire fixture', () => {
  afterEach(() => vi.restoreAllMocks())

  async function fetchFixture(): Promise<GraphState> {
    globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => structuredClone(wire) }) as Response) as typeof fetch
    return api.instances.graphState(ROOT)
  }

  it('keeps the fields the live views read: identity, states, ownership', async () => {
    const gs = await fetchFixture()

    expect(gs.rootInstanceId).toBe(ROOT)
    expect(gs.active).toBe(true)
    expect(gs.warnings).toEqual(['one descendant unreadable'])
    const branch = gs.contexts.find((c) => c.instanceId === BRANCH)!
    expect(branch).toMatchObject({ kind: 'branch', parentInstanceId: ROOT, groupRunId: RUN, taskKey: 'a', state: 'running', blocking: true })
    expect(branch.nodes).toEqual([{ instanceId: BRANCH, nodeId: 'a1', state: 'running', attempt: 2 }])
    expect(gs.layout!.owner.a1).toEqual({ groupNodeId: 'g', taskKey: 'a' })
    expect(gs.layout!.order).toEqual(['g'])
  })

  it("carries a workflow task's own nested-group layout through normalization", async () => {
    const gs = await fetchFixture()

    const child = gs.contexts.find((c) => c.instanceId === CHILD)!
    expect(child).toMatchObject({ kind: 'workflow', workflowName: 'sub', blocking: false })
    expect(child.layout!.order).toEqual(['k'])
    expect(child.layout!.groups.k.config.tasks[0]).toMatchObject({ key: 'w', workflow: 'sub', inputs: { x: '1' } })
    expect(child.layout!.groups.k.bodies).toEqual({})
  })

  it('keeps the panel, frozen decision and gather history the group views read', async () => {
    const gs = await fetchFixture()

    const panel = gs.groups[0]
    expect(panel).toMatchObject({
      groupRunId: RUN, instanceId: ROOT, nodeId: 'g', form: 'branches', state: 'resolving_conflicts',
      completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results_and_files', winner: 'a',
    })
    expect(panel.waitingReasons).toEqual(['waiting_for_tasks:a', 'cancelling:a'])
    expect(panel.progress).toEqual({ total: 1, completed: 1, failed: 0, cancelled: 0, active: 0, notStarted: 0 })
    expect(panel.tasks[0]).toMatchObject({
      key: 'a', childInstanceId: BRANCH, liveStatus: 'completed', frozenStatus: 'unfinished',
      blocking: false, continuingIndependently: true, cancelRequested: true, launchError: 'boom', redacted: false,
    })
    expect(panel.decision).toMatchObject({ outcome: 'success', winnerKey: 'a', reason: 'task a completed' })
    expect(panel.decision!.tasks[0]).toMatchObject({ key: 'a', childInstanceId: BRANCH, status: 'completed' })
    expect(panel.gather).toMatchObject({ mode: 'results_and_files', state: 'resolving', mergeIndex: 1 })
    expect(panel.gather!.tasks[0]).toEqual({ key: 'a', merged: false, reason: 'conflict', conflictPaths: ['f.txt'] })
    expect(panel.gather!.attempts[0]).toMatchObject({ taskKey: 'a', attempt: 2, outcome: 'resolved', conflictPaths: ['f.txt'] })
    const session = { groupRunId: RUN, mergeIndex: 1, attempt: 2, instanceId: ROOT, nodeId: 'g' }
    expect(panel.gather!.activeSession).toEqual(session)
    expect(panel.activeSession).toEqual(session)
  })

  it('marks the hidden descendant redacted with no identity', async () => {
    const gs = await fetchFixture()

    const hidden = gs.contexts.find((c) => c.redacted)!
    expect(hidden.state).toBe('unknown')
    expect(hidden.nodes).toEqual([])
    expect(hidden.parentInstanceId).toBeUndefined()
  })
})
