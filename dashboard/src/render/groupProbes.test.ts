import { describe, expect, it } from 'vitest'
import * as producer from './groupProbes'
import fixture from '../graph/arranged/groupGeometry.v2.json'
import golden from '../graph/arranged/groupProbe.contract.json'
import type { GroupGraph } from '../api/types'

describe('captured group runtime producer', () => {
  it('uses the golden names, real task keys, statuses and nested ownership without mutation', () => {
    const groups = structuredClone(fixture[3].groups) as unknown as GroupGraph
    const before = structuredClone(groups)
    const cases = producer.buildGroupProbeCases(groups)
    expect(cases.map(c => c.name)).toEqual(golden)
    expect(groups).toEqual(before)
    for (const c of cases) {
      expect(Object.keys(c.groupLive)).toEqual(groups.order)
      expect(Object.keys(c.nodeContexts).sort()).toEqual([...new Set([...groups.order, ...Object.keys(groups.owner)])].sort())
      for (const gid of groups.order) expect(Object.keys(c.groupLive[gid].tasks)).toEqual(groups.groups[gid].config.tasks.map(t => t.key))
    }
    expect(cases.find(c => c.name === 'group:run:launching')!.groupLive.fan.state).toBe('launching')
    expect(cases.find(c => c.name === 'group:task:unfinished')!.groupLive.fan.tasks.second).toMatchObject({ status: 'unfinished', frozenStatus: 'unfinished' })
    expect(cases.find(c => c.name === 'group:badges')!.groupLive.fan.tasks.second).toMatchObject({ winner: true, continuingIndependently: true, cancelRequested: true })
    expect(cases.find(c => c.name === 'group:redacted')!.groupLive.a.tasks.first).toMatchObject({ redacted: true, childInstanceId: null })
    expect(cases.find(c => c.name === 'group:redacted')!.nodeContexts.a).toEqual({ instanceId: null, independent: false, hiddenDependency: true })
    expect(cases.find(c => c.name === 'group:selected-running')!.selectedNodeIds.size).toBeGreaterThan(0)
    groups.groups.fan.config.tasks[0].key = 'changed-task'
    const changed = producer.buildGroupProbeCases(groups)
    expect(changed[changed.length - 2].name).toBe(JSON.stringify(['group-probe', 'winner', 'fan', 'changed-task']))
    expect(changed[0].groupLive.fan.tasks).toHaveProperty('changed-task')
    expect(changed[0].groupLive.fan.tasks).not.toHaveProperty('second')
  })

  it('orders winner cases by UTF-16 code units, as Go GroupProbeNames does for astral IDs', () => {
    const order = ['\ue000', '🧪', '中<&\u2028']
    const groups = { order, owner: {}, groups: Object.fromEntries(order.map(id => [id, { config: { tasks: [{ key: 'valid' }] } }])) } as unknown as GroupGraph
    const names = producer.buildGroupProbeCases(groups).map(c => c.name)
    expect(names.slice(-3)).toEqual(['中<&\u2028', '🧪', '\ue000'].map(id => JSON.stringify(['group-probe', 'winner', id, 'valid'])))
  })
})
