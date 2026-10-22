import { describe, expect, it } from 'vitest'
import type { GroupGraph } from '../api/types'
import { fixtureGeometry } from '../graph/arranged/testFixtures'
import { mixedGraph } from '../graph/groupLayout.fixtures'
import { buildSurfaceModel } from './RenderSurface'
import { parseJob, RUNTIME_STATES, workflowForJob } from './job'
import type { RenderJob } from './job'

function groupJob(): RenderJob {
  const geometry = fixtureGeometry()
  geometry.nodes[0].typeCaption = 'parallel group'
  geometry.edges = [
    { ...geometry.edges[0], output: 'success', text: { text: 'success', lines: 1, box: { x: 150, y: 100, w: 60, h: 21 } } },
    { ordinal: 1, source: 'a', target: 'c', output: 'failure', kind: 'forward',
      points: [{ x: 170, y: 92 }, { x: 170, y: 116 }, { x: 320, y: 116 }, { x: 320, y: 240 }, { x: 130, y: 240 }, { x: 130, y: 260 }],
      sourceAttach: { nodeId: 'a', side: 'bottom', point: { x: 170, y: 92 } }, targetAttach: { nodeId: 'c', side: 'top', point: { x: 130, y: 260 } },
      text: { text: 'failure', lines: 1, box: { x: 250, y: 160, w: 60, h: 21 } } },
  ]
  geometry.crossings = []
  const groups: GroupGraph = {
    groups: { a: { ...structuredClone(mixedGraph.groups.g1), nodeId: 'a', parent: undefined, nested: [], bodies: {}, successTarget: 'b', failureTarget: 'c', config: {
      form: 'workflows', completionRule: 'first_success', unfinishedBehavior: 'finish_independently', gatherMode: 'results',
      tasks: [{ key: 'second', workflow: '同じ子', inputs: { nested: ['保存'] } }, { key: 'first', workflow: '同じ子' }],
    } } }, owner: {}, order: ['a'],
  }
  return { mode: 'render', revision: 'saved-group', attempt: 1, geometry, workflowNodeTypes: { a: 'parallel_group', b: 'llm_call', c: 'conditional' }, workflowGroups: groups, states: [...RUNTIME_STATES] }
}

describe('captured group page input', () => {
  it('feeds saved config/task order and ownership into the actual surface model', () => {
    const job = groupJob()
    const { workflowDef, view } = buildSurfaceModel(job)
    expect(workflowDef.nodes[0].config.group).toEqual(job.workflowGroups!.groups.a.config)
    expect(workflowDef.nodes[1].config).toEqual({})
    expect(view.groups).toEqual(job.workflowGroups)
    const config = workflowDef.nodes[0].config.group as GroupGraph['groups'][string]['config']
    config.tasks[0].inputs!.nested = ['changed']
    expect(job.workflowGroups!.groups.a.config.tasks[0].inputs!.nested).toEqual(['保存'])
  })
  it.each(['constructor', '__proto__', '節点 ✅'])('preserves opaque group/node id %s without prototype lookups', id => {
    const job = groupJob(), scope = job.workflowGroups!.groups.a
    job.geometry.nodes[0].id = id
    job.workflowNodeTypes = { ...job.workflowNodeTypes, [id]: 'parallel_group' }
    delete job.workflowNodeTypes.a
    for (const edge of job.geometry.edges) edge.source = id
    scope.nodeId = id
    job.workflowGroups = { groups: Object.fromEntries([[id, scope]]), owner: {}, order: [id] }
    expect(workflowForJob(job).nodes[0].config.group).toEqual(scope.config)
  })
  it('does not attach inherited group config to an ordinary node', () => {
    const job = groupJob()
    job.geometry.nodes[1].id = 'constructor'
    job.workflowNodeTypes = { ...job.workflowNodeTypes, constructor: 'llm_call' }
    delete job.workflowNodeTypes.b
    job.geometry.edges[0].target = 'constructor'
    job.workflowGroups!.groups.a.successTarget = 'constructor'
    expect(workflowForJob(job).nodes[1].config).toStrictEqual({})
  })
  it.each([undefined, null, {}, { groups: {}, owner: {}, order: [] }])('rejects absent/empty group input %j before capture', workflowGroups => {
    expect(() => parseJob({ ...groupJob(), workflowGroups })).toThrow(/group ownership/)
  })
  it('rejects node/type/order/config mismatches, not just presence of a map', () => {
    const mutations: ((g: GroupGraph) => void)[] = [
      g => { g.order = [] },
      g => { g.order.push('a') },
      g => { g.groups.a.nodeId = 'wrong' },
      g => { g.groups.b = structuredClone(g.groups.a) },
      g => { g.groups.a.config.tasks[0].workflow = '' },
      g => { g.groups.a.config.tasks[0].key = 'first' },
      g => { g.groups.a.config.form = 'invalid' as 'branches' },
      g => { g.owner.b = { groupNodeId: 'missing', taskKey: 'first' } },
      g => { g.groups.a.parent = { groupNodeId: 'a', taskKey: 'first' } },
      g => { g.groups.a.nested = ['missing'] },
      g => { g.groups.a.bodies.first = ['b'] },
      g => { g.groups.a.successTarget = 'missing' },
    ]
    for (const mutate of mutations) {
      const job = groupJob()
      mutate(job.workflowGroups!)
      expect(() => workflowForJob(job)).toThrow(/group ownership/)
    }
    const job = groupJob()
    job.workflowNodeTypes.a = 'transform'
    expect(() => workflowForJob(job)).toThrow(/group ownership/)
  })
  it('does not capture ordinary boxes as reviewed v1 or v2 groups even with valid ownership', () => {
    for(const format of ['arbitron.layout/v1','arbitron.layout/v2']){
      const job=groupJob();job.geometry.format=format
      expect(() => parseJob(job)).toThrow(/group geometry/)
    }
  })
})
