import type {
  EnrichedInstanceSummary,
  ExecutionContext,
  GraphState,
  GroupGraph,
  GroupPanel,
  GroupPanelTask,
  LiveNodeState,
  LiveNodeStateValue,
} from '../api/types'
import type { WorkflowDefinition } from './types'

/** A queue row with every enriched field the queue components read. */
export function summary(o: Partial<EnrichedInstanceSummary> & { id: string }): EnrichedInstanceSummary {
  return {
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'running',
    priority: 'normal',
    createdAt: '2026-10-16T10:00:00Z',
    updatedAt: '2026-10-16T10:00:00Z',
    runName: `run ${o.id}`,
    retryCount: 0,
    ...o,
  }
}

const step = (id: string, type = 'llm_call', label = id.toUpperCase()) => ({ id, type, label, description: '', config: {} })
const asGraph = (d: WorkflowDefinition) => d as unknown as Record<string, unknown>

/** start -> g (branches; a: a1 -> a2, b: b1) -> done / oops (same shape as P4-T8's view fixture). */
export const fanoutDef: WorkflowDefinition = {
  name: 'fanout',
  description: '',
  author: '',
  nodes: [step('start'), step('g', 'parallel_group', 'Fan out'), step('a1'), step('a2'), step('b1'), step('done'), step('oops')],
  edges: [
    { source: 'start', target: 'g' },
    { source: 'g', target: 'a1', output: 'task', label: 'a' },
    { source: 'g', target: 'b1', output: 'task', label: 'b' },
    { source: 'a1', target: 'a2' },
    { source: 'g', target: 'done', output: 'success' },
    { source: 'g', target: 'oops', output: 'failure' },
  ],
}

export const fanoutLayout: GroupGraph = {
  groups: {
    g: {
      nodeId: 'g',
      config: {
        form: 'branches',
        tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'b1' }],
        completionRule: 'all_succeed',
        unfinishedBehavior: 'cancel_unfinished',
        gatherMode: 'results',
      },
      bodies: { a: ['a1', 'a2'], b: ['b1'] },
      nested: [],
      successTarget: 'done',
      failureTarget: 'oops',
    },
  },
  owner: { a1: { groupNodeId: 'g', taskKey: 'a' }, a2: { groupNodeId: 'g', taskKey: 'a' }, b1: { groupNodeId: 'g', taskKey: 'b' } },
  order: ['g'],
}

/** The child workflow every task of the workflows-form group runs: node id "build" repeats per task. */
export const childWfDef: WorkflowDefinition = {
  name: 'child-wf',
  description: '',
  author: '',
  nodes: [step('build', 'script', 'Build')],
  edges: [],
}

/** start -> g (workflows; x, y, z, q all run child-wf) -> done / oops. */
export const wfGroupDef: WorkflowDefinition = {
  name: 'repeat',
  description: '',
  author: '',
  nodes: [step('start'), step('g', 'parallel_group', 'Fan out'), step('done'), step('oops')],
  edges: [
    { source: 'start', target: 'g' },
    { source: 'g', target: 'done', output: 'success' },
    { source: 'g', target: 'oops', output: 'failure' },
  ],
}

export const wfGroupLayout: GroupGraph = {
  groups: {
    g: {
      nodeId: 'g',
      config: {
        form: 'workflows',
        tasks: ['x', 'y', 'z', 'q'].map((key) => ({ key, workflow: 'child-wf' })),
        completionRule: 'all_succeed',
        unfinishedBehavior: 'wait_for_remaining',
        gatherMode: 'results',
      },
      bodies: {},
      nested: [],
      successTarget: 'done',
      failureTarget: 'oops',
    },
  },
  owner: {},
  order: ['g'],
}

export const live = (instanceId: string, nodeId: string, state: LiveNodeStateValue): LiveNodeState => ({ instanceId, nodeId, state })

export function task(key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask {
  return {
    key,
    kind: 'branch',
    origin: key === 'a' ? 'a1' : 'b1',
    childInstanceId: `br-${key}`,
    liveStatus: 'running',
    frozenStatus: '',
    blocking: true,
    continuingIndependently: false,
    cancelRequested: false,
    redacted: false,
    ...over,
  }
}

export function panel(over: Partial<GroupPanel> = {}): GroupPanel {
  return {
    groupRunId: 'run-g',
    instanceId: 'root',
    nodeId: 'g',
    form: 'branches',
    completionRule: 'all_succeed',
    unfinishedBehavior: 'cancel_unfinished',
    gatherMode: 'results',
    state: 'running',
    tasks: [task('a'), task('b')],
    progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
    waitingReasons: ['Waiting for tasks a, b'],
    ...over,
  }
}

/** Root waits at g; branch a runs a2; branch b's b1 is `bState`. */
export function fanoutContexts(bState: 'running' | 'completed' = 'running'): ExecutionContext[] {
  return [
    { instanceId: 'root', kind: 'root', state: 'waiting', blocking: true, redacted: false, graph: asGraph(fanoutDef), nodes: [live('root', 'start', 'completed'), live('root', 'g', 'waiting')] },
    { instanceId: 'br-a', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'a', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'running')] },
    { instanceId: 'br-b', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'b', kind: 'branch', state: bState, blocking: true, redacted: false, nodes: [live('br-b', 'b1', bState)] },
  ]
}

export function fanoutAggregate(over: Partial<GraphState> = {}): GraphState {
  return { rootInstanceId: 'root', version: 'v1', active: true, layout: fanoutLayout, warnings: [], contexts: fanoutContexts(), groups: [panel()], ...over }
}

/**
 * p-done: a COMPLETED first_success parent. Winner b (c-win) finished; the
 * non-winner a (c-ind) keeps running under finish_independently.
 */
export function independentAggregate(): GraphState {
  return {
    rootInstanceId: 'p-done',
    version: 'd1',
    active: true,
    layout: fanoutLayout,
    warnings: [],
    contexts: [
      { instanceId: 'p-done', kind: 'root', state: 'completed', blocking: false, redacted: false, graph: asGraph(fanoutDef), nodes: [live('p-done', 'start', 'completed'), live('p-done', 'g', 'completed'), live('p-done', 'done', 'completed')] },
      { instanceId: 'c-ind', parentInstanceId: 'p-done', groupRunId: 'run-g', taskKey: 'a', kind: 'branch', state: 'running', blocking: false, redacted: false, nodes: [live('c-ind', 'a1', 'completed'), live('c-ind', 'a2', 'running')] },
      { instanceId: 'c-win', parentInstanceId: 'p-done', groupRunId: 'run-g', taskKey: 'b', kind: 'branch', state: 'completed', blocking: false, redacted: false, nodes: [live('c-win', 'b1', 'completed')] },
    ],
    groups: [
      panel({
        instanceId: 'p-done',
        completionRule: 'first_success',
        unfinishedBehavior: 'finish_independently',
        state: 'done',
        winner: 'b',
        waitingReasons: [],
        tasks: [
          task('a', { childInstanceId: 'c-ind', liveStatus: 'running', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
          task('b', { childInstanceId: 'c-win', liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
        ],
        progress: { total: 2, completed: 1, failed: 0, cancelled: 0, active: 1, notStarted: 0 },
      }),
    ],
  }
}

/** The id Go encodes for a redacted context (uuid.Nil): it must never surface. */
export const HIDDEN_ID = '00000000-0000-0000-0000-000000000000'

const wtask = (key: string, child: string, liveStatus: GroupPanelTask['liveStatus']): GroupPanelTask =>
  task(key, { kind: 'workflow', origin: 'child-wf', childInstanceId: child, liveStatus })

/**
 * p-wf waits at a workflows-form group whose tasks x/y/z run the SAME child
 * workflow (each child reuses node id "build"): x running, y waiting, z
 * paused; q is a child the caller may not see (redacted).
 */
export function repeatedChildAggregate(): GraphState {
  const child = (id: string, key: string, state: 'running' | 'waiting' | 'paused'): ExecutionContext => ({
    instanceId: id,
    parentInstanceId: 'p-wf',
    groupRunId: 'run-w',
    taskKey: key,
    kind: 'workflow',
    workflowName: 'child-wf',
    state,
    blocking: true,
    redacted: false,
    graph: asGraph(childWfDef),
    nodes: [live(id, 'build', state)],
  })
  return {
    rootInstanceId: 'p-wf',
    version: 'w1',
    active: true,
    layout: wfGroupLayout,
    warnings: [],
    contexts: [
      { instanceId: 'p-wf', kind: 'root', state: 'waiting', blocking: true, redacted: false, graph: asGraph(wfGroupDef), nodes: [live('p-wf', 'start', 'completed'), live('p-wf', 'g', 'waiting')] },
      child('w1', 'x', 'running'),
      child('w2', 'y', 'waiting'),
      child('w3', 'z', 'paused'),
      { instanceId: HIDDEN_ID, parentInstanceId: 'p-wf', groupRunId: 'run-w', taskKey: 'q', kind: 'workflow', state: 'unknown', blocking: true, redacted: true, nodes: [] },
    ],
    groups: [
      panel({
        groupRunId: 'run-w',
        instanceId: 'p-wf',
        form: 'workflows',
        unfinishedBehavior: 'wait_for_remaining',
        tasks: [
          wtask('x', 'w1', 'running'),
          wtask('y', 'w2', 'waiting'),
          wtask('z', 'w3', 'paused'),
          task('q', { kind: 'workflow', origin: 'child-wf', childInstanceId: null, liveStatus: 'unknown', redacted: true }),
        ],
        progress: { total: 4, completed: 0, failed: 0, cancelled: 0, active: 3, notStarted: 0 },
        waitingReasons: ['Waiting for tasks x, y, z and a hidden dependency'],
      }),
    ],
  }
}

/**
 * p-nest runs one workflows-form task (x) whose child workflow `wf-x` is the
 * fan-out graph: its group `g` (same id as the root's) runs branches a and b.
 * Only the aggregate can draw wf-x's group, from the context's own graph and layout.
 */
export function nestedChildGroupAggregate(): GraphState {
  const root = { ...wfGroupLayout.groups.g, config: { ...wfGroupLayout.groups.g.config, tasks: [{ key: 'x', workflow: 'fanout' }] } }
  return {
    rootInstanceId: 'p-nest',
    version: 'n1',
    active: true,
    layout: { ...wfGroupLayout, groups: { g: root } },
    warnings: [],
    contexts: [
      { instanceId: 'p-nest', kind: 'root', state: 'waiting', blocking: true, redacted: false, graph: asGraph(wfGroupDef), nodes: [live('p-nest', 'start', 'completed'), live('p-nest', 'g', 'waiting')] },
      { instanceId: 'wf-x', parentInstanceId: 'p-nest', groupRunId: 'run-w', taskKey: 'x', kind: 'workflow', workflowName: 'fanout', state: 'waiting', blocking: true, redacted: false, graph: asGraph(fanoutDef), layout: fanoutLayout, nodes: [live('wf-x', 'start', 'completed'), live('wf-x', 'g', 'waiting')] },
      { instanceId: 'br-xa', parentInstanceId: 'wf-x', groupRunId: 'run-n', taskKey: 'a', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-xa', 'a1', 'completed'), live('br-xa', 'a2', 'running')] },
      { instanceId: 'br-xb', parentInstanceId: 'wf-x', groupRunId: 'run-n', taskKey: 'b', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-xb', 'b1', 'running')] },
    ],
    groups: [
      panel({ instanceId: 'p-nest', form: 'workflows', tasks: [task('x', { kind: 'workflow', origin: 'fanout', childInstanceId: 'wf-x' })], progress: { total: 1, completed: 0, failed: 0, cancelled: 0, active: 1, notStarted: 0 }, waitingReasons: ['Waiting for task x'] }),
      panel({ groupRunId: 'run-n', instanceId: 'wf-x', tasks: [task('a', { childInstanceId: 'br-xa' }), task('b', { childInstanceId: 'br-xb' })] }),
    ],
  }
}
