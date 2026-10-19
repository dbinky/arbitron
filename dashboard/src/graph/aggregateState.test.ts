import { describe, it, expect } from 'vitest'
import type { ExecutionContext, GraphState, GroupPanel, GroupPanelTask } from '../api/types'
import type { WorkflowDefinition } from './types'
import {
  activeNodeLabels,
  buildAggregateExecutionStates,
  buildAggregateView,
  buildGroupLive,
  findGroupPanel,
  gatherActivity,
  projectExecutionStates,
  qualifiedKey,
  resolveNodeContexts,
  selectionFromNode,
} from './aggregateState'
import { branchEdges, branchGraph, branchNodes, mixedEdges, mixedGraph, mixedNodes } from './groupLayout.fixtures'

const branchDef: WorkflowDefinition = { name: 'wf', description: '', author: '', nodes: branchNodes, edges: branchEdges }
const mixedDef: WorkflowDefinition = { name: 'wf', description: '', author: '', nodes: mixedNodes, edges: mixedEdges }

function ctx(instanceId: string, over: Partial<ExecutionContext> = {}): ExecutionContext {
  return { instanceId, kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [], ...over }
}
function task(key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask {
  return {
    key, kind: 'branch', origin: key, childInstanceId: null, liveStatus: 'running', frozenStatus: '',
    blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, ...over,
  }
}
function panel(instanceId: string, nodeId: string, tasks: GroupPanelTask[], over: Partial<GroupPanel> = {}): GroupPanel {
  return {
    groupRunId: `run-${nodeId}`, instanceId, nodeId, form: 'branches', completionRule: 'all_succeed',
    unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running', tasks,
    progress: { total: tasks.length, completed: 0, failed: 0, cancelled: 0, active: tasks.length, notStarted: 0 },
    waitingReasons: [], ...over,
  }
}
const live = (instanceId: string, nodeId: string, state: ExecutionContext['nodes'][number]['state']) => ({ instanceId, nodeId, state })

/**
 * Running two-level branches aggregate:
 *   root waits at g1; branch A (br-a) runs a2; branch B (br-b) passed nested g2
 *   (x won in br-x; y continues independently in br-y running y2) and runs b2.
 */
function branchState(): GraphState {
  return {
    rootInstanceId: 'root',
    version: 'v1',
    active: true,
    layout: branchGraph,
    contexts: [
      ctx('root', { kind: 'root', state: 'waiting', nodes: [live('root', 'start', 'completed'), live('root', 'g1', 'waiting')] }),
      ctx('br-a', { parentInstanceId: 'root', groupRunId: 'run-g1', taskKey: 'a', nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'running')] }),
      ctx('br-b', { parentInstanceId: 'root', groupRunId: 'run-g1', taskKey: 'b', nodes: [live('br-b', 'g2', 'completed'), live('br-b', 'b2', 'running')] }),
      ctx('br-x', { parentInstanceId: 'br-b', groupRunId: 'run-g2', taskKey: 'x', state: 'completed', blocking: false, nodes: [live('br-x', 'x1', 'completed')] }),
      ctx('br-y', { parentInstanceId: 'br-b', groupRunId: 'run-g2', taskKey: 'y', blocking: false, nodes: [live('br-y', 'y1', 'completed'), live('br-y', 'y2', 'running')] }),
    ],
    groups: [
      panel('root', 'g1', [task('a', { childInstanceId: 'br-a' }), task('b', { childInstanceId: 'br-b' })]),
      panel('br-b', 'g2', [
        task('x', { childInstanceId: 'br-x', liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
        task('y', { childInstanceId: 'br-y', liveStatus: 'running', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
      ], { completionRule: 'first_success', unfinishedBehavior: 'finish_independently', state: 'done', winner: 'x' }),
    ],
    warnings: [],
  }
}

describe('qualified node identity', () => {
  it('keys every live node by instance AND node id', () => {
    expect(qualifiedKey('br-a', 'a2')).toBe('br-a:a2')
    const states = buildAggregateExecutionStates(branchState())
    expect(states['br-a:a2']).toBe('running')
    expect(states['br-y:y2']).toBe('running')
    expect(states['root:g1']).toBe('waiting')
  })

  // Two workflow tasks running the SAME child workflow reuse node ids; a map
  // keyed by node id alone would collapse them into one.
  it('keeps duplicate node ids of repeated child workflows apart', () => {
    const gs: GraphState = {
      rootInstanceId: 'root', version: 'v', active: true, groups: [], warnings: [],
      contexts: [
        ctx('c1', { kind: 'workflow', nodes: [live('c1', 'build', 'running')] }),
        ctx('c2', { kind: 'workflow', nodes: [live('c2', 'build', 'failed')] }),
      ],
    }
    const states = buildAggregateExecutionStates(gs)
    expect(states['c1:build']).toBe('running')
    expect(states['c2:build']).toBe('failed')
  })

  it('maps aggregate-only states (queued, paused, cancelled, unknown) through', () => {
    const gs: GraphState = {
      rootInstanceId: 'r', version: 'v', active: true, groups: [], warnings: [],
      contexts: [ctx('r', { kind: 'root', nodes: [live('r', 'q', 'queued'), live('r', 'p', 'paused'), live('r', 'c', 'cancelled'), live('r', 'u', 'unknown')] })],
    }
    expect(buildAggregateExecutionStates(gs)).toEqual({ 'r:q': 'queued', 'r:p': 'paused', 'r:c': 'cancelled', 'r:u': 'unknown' })
  })
})

describe('resolveNodeContexts — owning instance via Phase 1 ownership', () => {
  it('maps main-path nodes to the root and body nodes to their branch, recursively through nested groups', () => {
    const c = resolveNodeContexts(branchState(), branchDef, branchGraph)
    expect(c.start.instanceId).toBe('root')
    expect(c.done.instanceId).toBe('root')
    expect(c.g1.instanceId).toBe('root')
    expect(c.a2.instanceId).toBe('br-a')
    expect(c.g2.instanceId).toBe('br-b')
    expect(c.b2.instanceId).toBe('br-b')
    expect(c.x1.instanceId).toBe('br-x')
    expect(c.y2.instanceId).toBe('br-y')
  })

  it('marks only work continuing independently as independent', () => {
    const c = resolveNodeContexts(branchState(), branchDef, branchGraph)
    expect(c.y2.independent).toBe(true)
    expect(c.x1.independent).toBe(false)
    expect(c.a2.independent).toBe(false)
  })

  it('an unlaunched task leaves its body without an owning instance (pending)', () => {
    const gs = branchState()
    gs.groups[0] = panel('root', 'g1', [task('a', { childInstanceId: 'br-a' }), task('b', { childInstanceId: null, liveStatus: 'queued' })])
    const c = resolveNodeContexts(gs, branchDef, branchGraph)
    expect(c.b2.instanceId).toBeNull()
    expect(c.x1.instanceId).toBeNull()
    expect(projectExecutionStates(buildAggregateExecutionStates(gs), c, branchDef).b2).toBe('pending')
  })

  it('a group that has not started leaves its bodies pending', () => {
    const gs = branchState()
    gs.groups = []
    const view = buildAggregateView(gs, branchDef)
    expect(view.nodeContexts.a2.instanceId).toBeNull()
    expect(view.executionStates.a2).toBe('pending')
    expect(view.groupLive).toEqual({})
  })

  it('a redacted task or context hides the body as a hidden dependency with unknown state', () => {
    const gs = branchState()
    gs.groups[0].tasks[0] = task('a', { childInstanceId: null, redacted: true, liveStatus: 'unknown' })
    gs.contexts[3] = ctx('br-x', { redacted: true, nodes: [] })
    const view = buildAggregateView(gs, branchDef)
    expect(view.nodeContexts.a2).toEqual({ instanceId: null, independent: false, hiddenDependency: true })
    expect(view.executionStates.a2).toBe('unknown')
    expect(view.nodeContexts.x1.hiddenDependency).toBe(true)
    expect(view.executionStates.x1).toBe('unknown')
  })
})

describe('resolveNodeContexts — a branch-task instance opened as the route root', () => {
  // Opening br-b directly (hierarchy row, task card "open run"): the server's root context
  // holds only br-b's own body nodes and emits no panel for the parent-owned group g1.
  function branchRootState(): GraphState {
    const gs = branchState()
    return {
      ...gs,
      rootInstanceId: 'br-b',
      contexts: [
        ctx('br-b', { kind: 'root', parentInstanceId: 'root', groupRunId: 'run-g1', taskKey: 'b', nodes: [live('br-b', 'g2', 'completed'), live('br-b', 'b2', 'running')] }),
        gs.contexts[3],
        gs.contexts[4],
      ],
      groups: [gs.groups[1]],
    }
  }

  it('owns its own body nodes and still resolves nested groups beneath it', () => {
    const c = resolveNodeContexts(branchRootState(), branchDef, branchGraph)
    expect(c.g2.instanceId).toBe('br-b')
    expect(c.b2.instanceId).toBe('br-b')
    expect(c.x1.instanceId).toBe('br-x')
    expect(c.y2.instanceId).toBe('br-y')
    expect(c.y2.independent).toBe(true)
  })

  it('does not claim a sibling branch\'s body', () => {
    const c = resolveNodeContexts(branchRootState(), branchDef, branchGraph)
    expect(c.a2.instanceId).toBeNull()
  })

  it('renders the live states of the branch body instead of pending', () => {
    const view = buildAggregateView(branchRootState(), branchDef)
    expect(view.executionStates.b2).toBe('running')
    expect(view.executionStates.g2).toBe('completed')
    expect(view.executionStates.y2).toBe('running')
    expect(view.executionStates.x1).toBe('completed')
    expect(view.executionStates.a2).toBe('pending')
    expect(view.groupLive.g2.winner).toBe('x')
  })
})

describe('projectExecutionStates — every concurrent path is live', () => {
  it('shows BOTH running branches and the nested independent task as running', () => {
    const view = buildAggregateView(branchState(), branchDef)
    expect(view.executionStates.a2).toBe('running')
    expect(view.executionStates.b2).toBe('running')
    expect(view.executionStates.y2).toBe('running')
    expect(view.executionStates.x1).toBe('completed')
    expect(view.executionStates.done).toBe('pending')
    expect(view.executionStates.g1).toBe('waiting')
  })
})

describe('buildGroupLive', () => {
  it('summarizes each group for its header/footer and marks winner and independent task', () => {
    const gs = branchState()
    const view = buildAggregateView(gs, branchDef)
    expect(view.groupLive.g1.instanceId).toBe('root')
    expect(view.groupLive.g1.groupRunId).toBe('run-g1')
    expect(view.groupLive.g2.winner).toBe('x')
    expect(view.groupLive.g2.tasks.x.winner).toBe(true)
    expect(view.groupLive.g2.tasks.y.continuingIndependently).toBe(true)
    expect(view.groupLive.g2.tasks.y.frozenStatus).toBe('unfinished')
    expect(view.groupLive.g2.tasks.y.activeNodes).toEqual(['Y2'])
    // Task b's subtree includes the nested branches: b2 and y2 are both active.
    expect(view.groupLive.g1.tasks.b.activeNodes.sort()).toEqual(['B2', 'Y2'])
  })

  it('workflow task cards list their own child run active nodes (labels from the child graph), never a sibling\'s', () => {
    const gs: GraphState = {
      rootInstanceId: 'root', version: 'v', active: true, layout: mixedGraph, warnings: [],
      contexts: [
        ctx('root', { kind: 'root', state: 'waiting', nodes: [live('root', 'g1', 'waiting')] }),
        ctx('br-b', { parentInstanceId: 'root', taskKey: 'b', nodes: [live('br-b', 'w1', 'waiting')] }),
        ctx('c-t1', { kind: 'workflow', parentInstanceId: 'br-b', taskKey: 't1', workflowName: 'child-a', graph: { nodes: [{ id: 'build', label: 'Build A' }] }, nodes: [live('c-t1', 'build', 'running')] }),
        ctx('c-t3', { kind: 'workflow', parentInstanceId: 'br-b', taskKey: 't3', workflowName: 'child-a', graph: { nodes: [{ id: 'build', label: 'Build A' }] }, nodes: [live('c-t3', 'build', 'completed')] }),
        ctx('gc', { kind: 'spawn', parentInstanceId: 'c-t3', nodes: [live('gc', 'lint', 'waiting')], graph: { nodes: [{ id: 'lint', label: 'Lint' }] } }),
      ],
      groups: [
        panel('root', 'g1', [task('a'), task('b', { childInstanceId: 'br-b' })]),
        panel('br-b', 'w1', [
          task('t1', { kind: 'workflow', origin: 'child-a', childInstanceId: 'c-t1' }),
          task('t2', { kind: 'workflow', origin: 'child-b', childInstanceId: null, liveStatus: 'queued' }),
          task('t3', { kind: 'workflow', origin: 'child-a', childInstanceId: 'c-t3' }),
        ], { form: 'workflows' }),
      ],
    }
    const view = buildAggregateView(gs, mixedDef)
    expect(view.groupLive.w1.instanceId).toBe('br-b')
    expect(view.groupLive.w1.tasks.t1.activeNodes).toEqual(['Build A'])
    expect(view.groupLive.w1.tasks.t2.activeNodes).toEqual([])
    expect(view.groupLive.w1.tasks.t2.status).toBe('queued')
    // Descendants of a task's child run count as that task's active work.
    expect(view.groupLive.w1.tasks.t3.activeNodes).toEqual(['Lint'])
  })

  it('withholds a redacted task\'s child id', () => {
    const gs = branchState()
    gs.groups[0].tasks[1] = task('b', { childInstanceId: 'br-b', redacted: true })
    const liveMap = buildGroupLive(gs, resolveNodeContexts(gs, branchDef, branchGraph), branchGraph, branchDef)
    expect(liveMap.g1.tasks.b.childInstanceId).toBeNull()
    expect(liveMap.g1.tasks.b.activeNodes).toEqual([])
  })
})

describe('activeNodeLabels', () => {
  it('ignores redacted contexts', () => {
    const gs = branchState()
    gs.contexts[4] = { ...gs.contexts[4], redacted: true }
    expect(activeNodeLabels(gs, 'br-b', branchDef)).toEqual(['B2'])
  })
})

describe('gatherActivity', () => {
  const base = panel('root', 'g', [], { gatherMode: 'results_and_files' })
  it('describes gathering progress', () => {
    expect(gatherActivity({ ...base, state: 'gathering' })).toBe('Gathering files')
    expect(gatherActivity({
      ...base, state: 'gathering',
      gather: { mode: 'results_and_files', state: 'merging', mergeIndex: 1, tasks: [{ key: 'a', merged: true, reason: 'clean' }, { key: 'b', merged: false, reason: 'pending' }], attempts: [] },
    })).toBe('Gathering files (1/2 merged)')
  })
  it('describes an active resolution attempt', () => {
    expect(gatherActivity({
      ...base, state: 'resolving_conflicts',
      gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 1, attempt: 2, instanceId: 'root', nodeId: 'g' } },
    })).toBe('Resolving conflicts (attempt 2)')
  })
  it('describes a failed or rolled-back gather and is silent otherwise', () => {
    expect(gatherActivity({ ...base, state: 'done', gather: { mode: 'results_and_files', state: 'failed', mergeIndex: 0, tasks: [], attempts: [], error: 'git unavailable' } })).toBe('File gathering failed: git unavailable')
    expect(gatherActivity({ ...base, state: 'done', gather: { mode: 'results_and_files', state: 'rolled_back', mergeIndex: 0, tasks: [], attempts: [] } })).toBe('File gathering rolled back')
    expect(gatherActivity({ ...base, state: 'running' })).toBeUndefined()
  })
  it('surfaces a rollback-failed suspension, not ordinary gathering progress', () => {
    // A failed rollback leaves the run in `gathering` (group_gatherer.go:abort)
    // with tasks already merged; the footer must not read as progress.
    const suspended: GroupPanel = {
      ...base, state: 'gathering', waitingReasons: ['gathering', 'rollback_failed'],
      gather: {
        mode: 'results_and_files', state: 'failed', mergeIndex: 1, rollbackStatus: 'rollback_failed',
        tasks: [{ key: 'a', merged: true, reason: 'clean' }, { key: 'b', merged: false, reason: 'pending' }], attempts: [],
        error: 'merge failed; rollback failed: disk full',
      },
    }
    expect(gatherActivity(suspended)).toBe('Rollback failed: merge failed; rollback failed: disk full')
    expect(gatherActivity({ ...suspended, gather: { ...suspended.gather!, error: undefined } })).toBe('Rollback failed')
  })
})

describe('findGroupPanel / selectionFromNode', () => {
  it('finds a panel by (instance, node) only', () => {
    const gs = branchState()
    expect(findGroupPanel(gs, 'br-b', 'g2')?.groupRunId).toBe('run-g2')
    expect(findGroupPanel(gs, 'root', 'g2')).toBeNull()
    expect(findGroupPanel(gs, null, 'g2')).toBeNull()
  })

  it('header, footer and task cards select their group; step nodes select (instance, node)', () => {
    expect(selectionFromNode({ id: 'group:g1:header', type: 'groupHeader', data: { definitionNodeId: 'g1', instanceId: 'root' } }, 'root'))
      .toEqual({ kind: 'group', rfId: 'group:g1:header', nodeId: 'g1', instanceId: 'root' })
    expect(selectionFromNode({ id: 'group:g2:footer', type: 'groupFooter', data: { definitionNodeId: 'g2', instanceId: 'br-b' } }, 'root'))
      .toEqual({ kind: 'group', rfId: 'group:g2:footer', nodeId: 'g2', instanceId: 'br-b' })
    expect(selectionFromNode({ id: 'a2', type: 'arbitronNode', data: { instanceId: 'br-a' } }, 'root'))
      .toEqual({ kind: 'node', rfId: 'a2', nodeId: 'a2', instanceId: 'br-a' })
  })

  it('falls back to the route instance only when the node carries no owner info', () => {
    expect(selectionFromNode({ id: 'a', type: 'arbitronNode', data: { label: 'A' } }, 'root').instanceId).toBe('root')
    expect(selectionFromNode({ id: 'x1', type: 'arbitronNode', data: { instanceId: null } }, 'root').instanceId).toBeNull()
  })
})
