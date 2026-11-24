import { api } from '../api/client'
import { decodeExecutionStatus } from '../api/runnerDecode'
import { listPoolEvents, listRunners } from '../api/runners'
import type { EnrichedInstanceSummary, ExecutionStatus } from '../api/types'
import { applyLiveEvent, POOL_EVENT_LIMIT, snapshotState, type PoolState } from './poolState'
import { useLiveSnapshot, type LiveSnapshot } from './useLiveSnapshot'

/**
 * Execution rows of the runs the caller may see (the server already filters
 * by visibility), decoded tolerantly, tagged with the owning run when the entry
 * omits it, and deduplicated by durable job id (a run can appear in both the
 * running and waiting reads because they are independent).
 */
export function executionsOf(rows: EnrichedInstanceSummary[]): ExecutionStatus[] {
  const out: ExecutionStatus[] = []
  const seen = new Set<string>()
  for (const row of rows) {
    if (!Array.isArray(row.execution)) continue
    for (const raw of row.execution as unknown[]) {
      const obj = typeof raw === 'object' && raw !== null ? (raw as Record<string, unknown>) : {}
      const runId = typeof obj.runId === 'string' && obj.runId !== '' ? obj.runId : row.id
      const status = decodeExecutionStatus({ ...obj, runId })
      if (!status || seen.has(status.jobId)) continue
      seen.add(status.jobId)
      out.push(status)
    }
  }
  return out
}

/**
 * Four independent authorized reads issued concurrently. This is NOT a
 * transactional snapshot and carries no revision. Running AND waiting runs:
 * pending/recovery work and idle interactive sessions are waiting at the
 * instance level.
 */
export async function loadPool(): Promise<PoolState> {
  const [runners, events, running, waiting] = await Promise.all([
    listRunners(),
    listPoolEvents(POOL_EVENT_LIMIT),
    api.instances.list({ state: 'running' }),
    api.instances.list({ state: 'waiting' }),
  ])
  return snapshotState(runners, events, executionsOf([...running, ...waiting]))
}

export function usePool(): LiveSnapshot<PoolState> {
  return useLiveSnapshot({ enabled: true, subject: 'pool', load: loadPool, apply: applyLiveEvent })
}
