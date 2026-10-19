import { describe, it, expect } from 'vitest'
import { activeNodesOf, buildHierarchyMarkers, EMPTY_DEFINITION, rootDefinitionOf } from './liveSummary'
import {
  fanoutAggregate,
  fanoutContexts,
  fanoutDef,
  HIDDEN_ID,
  independentAggregate,
  live,
  repeatedChildAggregate,
  wfGroupDef,
} from './liveSummary.fixtures'

describe('rootDefinitionOf', () => {
  it("returns the selected run's own snapshot from its root context", () => {
    expect(rootDefinitionOf(fanoutAggregate())?.nodes.map((n) => n.id)).toEqual(fanoutDef.nodes.map((n) => n.id))
  })

  // Error: an unreadable snapshot must not be drawn as an empty-but-valid graph.
  it('is null without an aggregate, without a root context, or with an unreadable snapshot', () => {
    const [root, ...rest] = fanoutContexts()
    expect(rootDefinitionOf(null)).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ rootInstanceId: 'other' }))).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest] }))).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ contexts: [{ ...root, graph: { nodes: 'x' } }, ...rest] }))).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ contexts: [{ ...root, redacted: true }, ...rest] }))).toBeNull()
  })
})

describe('activeNodesOf', () => {
  // Happy path: every concurrently active node, across both fanned-out paths.
  it('lists EVERY running/waiting node of the root and both branches, instance-qualified', () => {
    const out = activeNodesOf(fanoutAggregate(), fanoutDef)
    expect(out.map((a) => [a.key, a.label, a.state, a.taskKey])).toEqual([
      ['root:g', 'Fan out', 'waiting', null],
      ['br-a:a2', 'A2', 'running', 'a'],
      ['br-b:b1', 'B1', 'running', 'b'],
    ])
  })

  // Edge: duplicate node ids across repeated child workflows; paused tasks; redaction.
  it('keeps repeated node ids apart, includes paused work, and never lists a redacted context', () => {
    const out = activeNodesOf(repeatedChildAggregate(), wfGroupDef)
    expect(out.map((a) => [a.key, a.label, a.state, a.taskKey])).toEqual([
      ['p-wf:g', 'Fan out', 'waiting', null],
      ['w1:build', 'Build', 'running', 'x'],
      ['w2:build', 'Build', 'waiting', 'y'],
      ['w3:build', 'Build', 'paused', 'z'],
    ])
    expect(out.some((a) => a.instanceId === HIDDEN_ID)).toBe(false)
  })

  it('marks work continuing independently after its group was decided as independent', () => {
    expect(activeNodesOf(independentAggregate(), fanoutDef)).toEqual([
      { key: 'c-ind:a2', instanceId: 'c-ind', nodeId: 'a2', label: 'A2', state: 'running', taskKey: 'a', independent: true },
    ])
  })

  it('falls back to the node id when no snapshot names the node', () => {
    const [root, ...rest] = fanoutContexts()
    const out = activeNodesOf(fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest] }), EMPTY_DEFINITION)
    expect(out.map((a) => a.label)).toEqual(['g', 'a2', 'b1'])
  })

  it('an inactive aggregate has no active nodes', () => {
    expect(activeNodesOf(fanoutAggregate({ contexts: fanoutContexts('completed').map((c) => ({ ...c, nodes: [] })) }), fanoutDef)).toEqual([])
  })
})

describe('buildHierarchyMarkers', () => {
  it('annotates each branch child with its group, task key, blocking flag and active nodes', () => {
    const m = buildHierarchyMarkers(fanoutAggregate(), 'root')
    expect(Object.keys(m)).toEqual(['br-a', 'br-b'])
    expect(m['br-a']).toEqual({
      kind: 'branch',
      groupNodeId: 'g',
      groupLabel: 'Fan out',
      taskKey: 'a',
      blocking: true,
      independent: false,
      activeSummary: 'running: A2',
    })
    expect(m['br-b'].activeSummary).toBe('running: B1')
  })

  // Success: finish_independently work is flagged non-blocking; finished work has no live summary.
  it('flags finish_independently work as non-blocking; a terminal child has no active summary', () => {
    const m = buildHierarchyMarkers(independentAggregate(), 'p-done')
    expect(m['c-ind']).toMatchObject({ kind: 'branch', taskKey: 'a', blocking: false, independent: true, activeSummary: 'running: A2' })
    expect(m['c-win']).toMatchObject({ taskKey: 'b', independent: false, activeSummary: null })
  })

  it('keeps repeated child workflows apart, summarises paused work, and never keys a redacted child', () => {
    const m = buildHierarchyMarkers(repeatedChildAggregate(), 'p-wf')
    expect(Object.keys(m).sort()).toEqual(['w1', 'w2', 'w3'])
    expect(m.w1).toMatchObject({ kind: 'workflow', groupNodeId: 'g', groupLabel: 'Fan out', taskKey: 'x', activeSummary: 'running: Build' })
    expect(m.w2.activeSummary).toBe('waiting: Build')
    expect(m.w3.activeSummary).toBe('paused')
  })

  it("only DIRECT children get markers; a grandchild's activity rolls up into its parent's summary", () => {
    const gs = fanoutAggregate({
      contexts: [
        ...fanoutContexts(),
        { instanceId: 'gc', parentInstanceId: 'br-a', kind: 'spawn', state: 'running', blocking: false, redacted: false, nodes: [live('gc', 'x', 'running')] },
      ],
    })
    const m = buildHierarchyMarkers(gs, 'root')
    expect(m.gc).toBeUndefined()
    expect(m['br-a'].activeSummary).toBe('running: A2, x')
  })

  // Failure (paired:22): a redacted descendant's activity must never mark a
  // visible row as owning active work — not via its own row, not rolled up.
  it("a redacted task's running work neither keys a row nor rolls up into a visible completed child", () => {
    const gs = fanoutAggregate({
      contexts: [
        ...fanoutContexts('completed').map((c) =>
          c.instanceId === 'br-a' ? { ...c, state: 'completed' as const, nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'completed')] } : c,
        ),
        { instanceId: 'hidden-1', parentInstanceId: 'root', kind: 'branch', state: 'running', blocking: false, redacted: true, nodes: [live('hidden-1', 'A1', 'running')] },
        { instanceId: 'hidden-2', parentInstanceId: 'br-a', kind: 'spawn', state: 'running', blocking: false, redacted: true, nodes: [live('hidden-2', 'x', 'running')] },
      ],
    })
    const m = buildHierarchyMarkers(gs, 'root')
    expect(Object.keys(m).sort()).toEqual(['br-a', 'br-b'])
    expect(m['br-a'].activeSummary).toBeNull()
    expect(m['br-b'].activeSummary).toBeNull()

    // A RUNNING visible child: the hidden grandchild's node never joins its summary.
    const running = buildHierarchyMarkers(
      fanoutAggregate({
        contexts: [
          ...fanoutContexts(),
          { instanceId: 'hidden-2', parentInstanceId: 'br-a', kind: 'spawn', state: 'running', blocking: false, redacted: true, nodes: [live('hidden-2', 'secret', 'running')] },
        ],
      }),
      'root',
    )
    expect(running['br-a'].activeSummary).toBe('running: A2')
  })

  it('a plain spawned child gets its state summary but no group or task', () => {
    const gs = fanoutAggregate({
      contexts: [
        ...fanoutContexts(),
        { instanceId: 'sp', parentInstanceId: 'root', kind: 'spawn', workflowName: 'side', state: 'queued', blocking: false, redacted: false, nodes: [] },
      ],
    })
    expect(buildHierarchyMarkers(gs, 'root').sp).toEqual({
      kind: 'spawn',
      groupNodeId: null,
      groupLabel: null,
      taskKey: null,
      blocking: false,
      independent: false,
      activeSummary: 'queued',
    })
  })

  // Error: an unreadable parent snapshot degrades to the group node id, never a guessed label.
  it('falls back to the group node id when the parent snapshot is unreadable', () => {
    const [root, ...rest] = fanoutContexts()
    expect(buildHierarchyMarkers(fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest] }), 'root')['br-a'].groupLabel).toBe('g')
  })
})
