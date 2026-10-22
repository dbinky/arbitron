import type { GroupGraph } from '../api/types'
import type { GroupLiveSummary, NodeContextInfo } from '../graph/groupLayout'
import { GROUP_RUN_STATES, GROUP_TASK_STATES } from '../graph/arranged/groupVocabulary'

export interface GroupProbeCase {
  name: string
  groupLive: Record<string, GroupLiveSummary>
  nodeContexts: Record<string, NodeContextInfo>
  selectedNodeIds: ReadonlySet<string>
}

/** Deterministic review overlays from captured ownership, never a live instance lookup. */
export function buildGroupProbeCases(groups: GroupGraph): GroupProbeCase[] {
  if (!groups.order.length) return []
  const make = (name: string): GroupProbeCase => {
    const groupLive: Record<string, GroupLiveSummary> = {}
    const nodeContexts: Record<string, NodeContextInfo> = {}
    for (const gid of groups.order) {
      const tasks = groups.groups[gid].config.tasks
      groupLive[gid] = {
        instanceId: `probe:${gid}`, groupRunId: `probe:${gid}`, state: 'running',
        progress: { total: tasks.length, completed: 0, active: tasks.length, notStarted: 0, failed: 0, cancelled: 0 },
        tasks: Object.fromEntries(tasks.map(t => [t.key, { key: t.key, status: 'running', frozenStatus: '',
          childInstanceId: `probe:${gid}:${t.key}`, blocking: true, continuingIndependently: false,
          cancelRequested: false, redacted: false, winner: false, activeNodes: [] }])),
      }
    }
    for (const id of new Set([...groups.order, ...Object.keys(groups.owner)])) {
      const owner = groups.owner[id]
      nodeContexts[id] = { instanceId: owner ? `probe:${owner.groupNodeId}:${owner.taskKey}` : `probe:${id}`, independent: false, hiddenDependency: false }
    }
    return { name, groupLive, nodeContexts, selectedNodeIds: new Set() }
  }
  const cases: GroupProbeCase[] = []
  for (const state of GROUP_RUN_STATES) {
    const c = make(`group:run:${state}`)
    for (const live of Object.values(c.groupLive)) live.state = state
    cases.push(c)
  }
  for (const status of GROUP_TASK_STATES) {
    const c = make(`group:task:${status}`)
    for (const live of Object.values(c.groupLive)) for (const task of Object.values(live.tasks)) {
      task.status = status
      task.frozenStatus = status === 'unknown' ? '' : status
    }
    cases.push(c)
  }
  const badges = make('group:badges')
  for (const live of Object.values(badges.groupLive)) for (const task of Object.values(live.tasks)) {
    task.winner = task.continuingIndependently = task.cancelRequested = true
    task.frozenStatus = 'unfinished'
  }
  for (const context of Object.values(badges.nodeContexts)) context.independent = true
  cases.push(badges)
  const redacted = make('group:redacted')
  for (const live of Object.values(redacted.groupLive)) for (const task of Object.values(live.tasks)) {
    task.redacted = true
    task.childInstanceId = null
  }
  for (const id of Object.keys(redacted.nodeContexts)) redacted.nodeContexts[id] = { instanceId: null, independent: false, hiddenDependency: true }
  cases.push(redacted)
  const selected = make('group:selected-running')
  selected.selectedNodeIds = new Set(Object.keys(selected.nodeContexts))
  cases.push(selected)
  for (const gid of [...groups.order].sort()) for (const task of groups.groups[gid].config.tasks) {
    const c = make(JSON.stringify(['group-probe', 'winner', gid, task.key]))
    c.groupLive[gid].winner = task.key
    c.groupLive[gid].tasks[task.key].winner = true
    cases.push(c)
  }
  return cases
}
