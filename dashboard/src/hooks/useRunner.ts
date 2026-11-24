import { useCallback } from 'react'
import { decodeExecutionStatus, type LiveEvent } from '../api/runnerDecode'
import { getRunner, listPoolEvents } from '../api/runners'
import type { ExecutionStatus, RunnerDetail } from '../api/types'
import { POOL_EVENT_LIMIT, snapshotState, withLoggedEvent, type PoolState } from './poolState'
import { useLiveSnapshot, type Applied, type LiveSnapshot } from './useLiveSnapshot'

export interface RunnerState {
  /** The authoritative detail response; never patched from events. */
  detail: RunnerDetail
  /** snapshotState([detail], this runner's logged events, visible assignment execution rows). */
  pool: PoolState
}

/** Visible (server-authorized) assignments as execution rows; hidden runs are already redacted to runId null. */
function assignmentExecutions(d: RunnerDetail): ExecutionStatus[] {
  const out: ExecutionStatus[] = []
  for (const a of d.assignments) {
    if (a.hidden || !a.runId) continue
    const s = decodeExecutionStatus({ runId: a.runId, nodeId: a.nodeId, jobId: a.jobId, state: a.state, runnerId: d.id, sourceId: a.sourceId })
    if (s) out.push(s)
  }
  return out
}

function visibleRuns(s: RunnerState): Set<string> {
  return new Set(s.detail.assignments.flatMap((a) => (a.runId ? [a.runId] : [])))
}

/**
 * Invalidations scoped to this runner: its own pool events, source-only
 * events that name none of the runners or one of its sources (conservative),
 * and execution/node/pool events for runs assigned to it. The detail is never
 * patched; only this runner's events join the display log.
 */
export function applyRunnerEvent(s: RunnerState, live: LiveEvent, runnerId: string): Applied<RunnerState> {
  switch (live.kind) {
    case 'pool': {
      const ev = live.event
      const mine = ev.runnerId === runnerId
      const relevant =
        mine ||
        (ev.runnerId === '' && (ev.sourceId === '' || ev.sourceId.startsWith(`${runnerId}/`))) ||
        (ev.runId !== null && visibleRuns(s).has(ev.runId))
      if (!relevant) return { state: s, refetch: false }
      const pool = mine ? withLoggedEvent(s.pool, ev) : s.pool
      return { state: pool === s.pool ? s : { ...s, pool }, refetch: true }
    }
    case 'execution':
      return { state: s, refetch: live.status.runnerId === runnerId || visibleRuns(s).has(live.status.runId) }
    case 'node_done':
      return { state: s, refetch: visibleRuns(s).has(live.instanceId) }
    default:
      return { state: s, refetch: false }
  }
}

export function useRunner(id: string | undefined): LiveSnapshot<RunnerState> {
  const load = useCallback(async (): Promise<RunnerState> => {
    if (!id) throw new Error('no runner id')
    const [detail, events] = await Promise.all([getRunner(id), listPoolEvents(POOL_EVENT_LIMIT)])
    return { detail, pool: snapshotState([detail], events.filter((e) => e.runnerId === id), assignmentExecutions(detail)) }
  }, [id])
  const apply = useCallback((s: RunnerState, live: LiveEvent) => applyRunnerEvent(s, live, id ?? ''), [id])
  return useLiveSnapshot({ enabled: Boolean(id), subject: id ?? '', load, apply })
}
