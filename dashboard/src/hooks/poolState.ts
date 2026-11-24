import type { LiveEvent } from '../api/runnerDecode'
import type { ExecutionStatus, PoolEvent, RunnerView } from '../api/types'
import type { Applied } from './useLiveSnapshot'

/** Recent pool events kept in view; also the REST `limit` the hooks request. */
export const POOL_EVENT_LIMIT = 50

/**
 * One authoritative read of the pool. The runners, event log and execution
 * rows come from independent REST reads with no shared revision, so nothing
 * here claims they are a transactional snapshot.
 */
export interface PoolState {
  runners: RunnerView[]
  /** Display log only, newest first. Never proof that `runners` reflects an event. */
  events: PoolEvent[]
  executions: ExecutionStatus[]
}

function dedupeEvents(events: PoolEvent[]): PoolEvent[] {
  const seen = new Set<string>()
  const out: PoolEvent[] = []
  for (const e of events) {
    if (seen.has(e.id)) continue
    seen.add(e.id)
    out.push(e)
    if (out.length === POOL_EVENT_LIMIT) break
  }
  return out
}

export function snapshotState(runners: RunnerView[], events: PoolEvent[], executions: ExecutionStatus[]): PoolState {
  return { runners, events: dedupeEvents(events), executions }
}

/** Pure helper for already-authoritative rows. MUST NOT be fed unversioned system events. */
export function upsertExecution(list: ExecutionStatus[], status: ExecutionStatus): ExecutionStatus[] {
  const idx = list.findIndex((s) => s.jobId === status.jobId)
  if (idx === -1) return [...list, status]
  const next = [...list]
  next[idx] = status
  return next
}

/** Pure helper for already-authoritative rows. MUST NOT be fed unversioned system events. */
export function pruneExecutions(list: ExecutionStatus[], runId: string, nodeId: string): ExecutionStatus[] {
  const next = list.filter((s) => !(s.runId === runId && s.nodeId === nodeId))
  return next.length === list.length ? list : next
}

/** Adds a live pool event to the display log (deduplicated by id); data rows are untouched. */
export function withLoggedEvent(state: PoolState, ev: PoolEvent): PoolState {
  if (state.events.some((e) => e.id === ev.id)) return state
  return { ...state, events: [ev, ...state.events].slice(0, POOL_EVENT_LIMIT) }
}

/**
 * Events are invalidations. Every pool/source/job/workspace/session event and
 * every execution.status / node completion asks for an authoritative REST
 * reload; none patches health, liveness, counts, telemetry or executions.
 * Event `at` is never used as a cursor or watermark.
 */
export function applyLiveEvent(state: PoolState, live: LiveEvent): Applied<PoolState> {
  switch (live.kind) {
    case 'pool':
      return { state: withLoggedEvent(state, live.event), refetch: true }
    case 'execution':
    case 'node_done':
      return { state, refetch: true }
    default:
      return { state, refetch: false }
  }
}
