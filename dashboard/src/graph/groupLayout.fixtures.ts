/**
 * Shared group-layout fixtures (Phase 1 GroupGraph shapes) used by the layout,
 * ExecutionView and WorkflowReview tests. Not imported by application code.
 */
import type { GroupGraph, GroupScope } from '../api/types'
import type { WorkflowEdge, WorkflowNode } from './types'

export const node = (id: string, type = 'llm_call', position?: { x: number; y: number }): WorkflowNode => ({
  id,
  type,
  label: id.toUpperCase(),
  description: '',
  config: {},
  ...(position ? { position } : {}),
})

export function scope(over: Partial<GroupScope> & Pick<GroupScope, 'nodeId' | 'config'>): GroupScope {
  return { bodies: {}, nested: [], successTarget: '', failureTarget: '', ...over }
}

/*
 * Two-level BRANCHES nesting:
 *   start -> g1
 *   g1 task a: a1 -> a2
 *   g1 task b: g2 (nested branches group), whose success -> b2, failure -> b3
 *     g2 task x: x1
 *     g2 task y: y1 -> y2
 *   g1 success -> done, failure -> oops
 */
export const branchNodes: WorkflowNode[] = [
  node('start'),
  node('g1', 'parallel_group'),
  node('a1'),
  node('a2'),
  node('g2', 'parallel_group'),
  node('x1'),
  node('y1'),
  node('y2'),
  node('b2'),
  node('b3'),
  node('done'),
  node('oops'),
]
export const branchEdges: WorkflowEdge[] = [
  { source: 'start', target: 'g1' },
  { source: 'g1', target: 'a1', output: 'task', label: 'a' },
  { source: 'g1', target: 'g2', output: 'task', label: 'b' },
  { source: 'a1', target: 'a2' },
  { source: 'g2', target: 'x1', output: 'task', label: 'x' },
  { source: 'g2', target: 'y1', output: 'task', label: 'y' },
  { source: 'y1', target: 'y2' },
  { source: 'g2', target: 'b2', output: 'success' },
  { source: 'g2', target: 'b3', output: 'failure' },
  { source: 'g1', target: 'done', output: 'success' },
  { source: 'g1', target: 'oops', output: 'failure' },
]
export const branchGraph: GroupGraph = {
  groups: {
    g1: scope({
      nodeId: 'g1',
      config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'g2' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' },
      bodies: { a: ['a1', 'a2'], b: ['g2', 'b2', 'b3'] },
      nested: ['g2'],
      successTarget: 'done',
      failureTarget: 'oops',
    }),
    g2: scope({
      nodeId: 'g2',
      config: { form: 'branches', tasks: [{ key: 'x', entry: 'x1' }, { key: 'y', entry: 'y1' }], completionRule: 'first_success', unfinishedBehavior: 'finish_independently', gatherMode: 'results' },
      parent: { groupNodeId: 'g1', taskKey: 'b' },
      bodies: { x: ['x1'], y: ['y1', 'y2'] },
      successTarget: 'b2',
      failureTarget: 'b3',
    }),
  },
  owner: {
    a1: { groupNodeId: 'g1', taskKey: 'a' },
    a2: { groupNodeId: 'g1', taskKey: 'a' },
    g2: { groupNodeId: 'g1', taskKey: 'b' },
    b2: { groupNodeId: 'g1', taskKey: 'b' },
    b3: { groupNodeId: 'g1', taskKey: 'b' },
    x1: { groupNodeId: 'g2', taskKey: 'x' },
    y1: { groupNodeId: 'g2', taskKey: 'y' },
    y2: { groupNodeId: 'g2', taskKey: 'y' },
  },
  order: ['g1', 'g2'],
}

/*
 * Two-level MIXED nesting (both forms):
 *   start -> g1 (branches)
 *   g1 task a: a1
 *   g1 task b: w1 (WORKFLOWS group: t1 child-a, t2 child-b, t3 child-a) success -> b2, failure -> b3
 *   g1 success -> done, failure -> oops
 */
export const mixedNodes: WorkflowNode[] = [
  node('start'),
  node('g1', 'parallel_group'),
  node('a1'),
  node('w1', 'parallel_group'),
  node('b2'),
  node('b3'),
  node('done'),
  node('oops'),
]
export const mixedEdges: WorkflowEdge[] = [
  { source: 'start', target: 'g1' },
  { source: 'g1', target: 'a1', output: 'task', label: 'a' },
  { source: 'g1', target: 'w1', output: 'task', label: 'b' },
  { source: 'w1', target: 'b2', output: 'success' },
  { source: 'w1', target: 'b3', output: 'failure' },
  { source: 'g1', target: 'done', output: 'success' },
  { source: 'g1', target: 'oops', output: 'failure' },
]
export const mixedGraph: GroupGraph = {
  groups: {
    g1: scope({
      nodeId: 'g1',
      config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'w1' }], completionRule: 'all_succeed', unfinishedBehavior: 'wait_for_remaining', gatherMode: 'results' },
      bodies: { a: ['a1'], b: ['w1', 'b2', 'b3'] },
      nested: ['w1'],
      successTarget: 'done',
      failureTarget: 'oops',
    }),
    w1: scope({
      nodeId: 'w1',
      config: {
        form: 'workflows',
        tasks: [{ key: 't1', workflow: 'child-a' }, { key: 't2', workflow: 'child-b' }, { key: 't3', workflow: 'child-a' }],
        completionRule: 'first_success',
        unfinishedBehavior: 'cancel_unfinished',
        gatherMode: 'results',
      },
      parent: { groupNodeId: 'g1', taskKey: 'b' },
      successTarget: 'b2',
      failureTarget: 'b3',
    }),
  },
  owner: {
    a1: { groupNodeId: 'g1', taskKey: 'a' },
    w1: { groupNodeId: 'g1', taskKey: 'b' },
    b2: { groupNodeId: 'g1', taskKey: 'b' },
    b3: { groupNodeId: 'g1', taskKey: 'b' },
  },
  order: ['g1', 'w1'],
}

export const EMPTY_GRAPH: GroupGraph = { groups: {}, owner: {}, order: [] }
