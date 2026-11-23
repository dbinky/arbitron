import type { EnrichedInstanceSummary, ExecutionState, ExecutionStatus, MoveProgress } from '../api/types'

const STATE_ORDER: Record<ExecutionState, number> = {
  running: 0,
  moving_workspace: 1,
  recovering: 2,
  waiting_capacity: 3,
  queued_slot: 4,
}

/** Active operations first, with stable node ordering for parallel branches. */
export function orderedStatuses(list: ExecutionStatus[] | undefined): ExecutionStatus[] {
  return [...(list ?? [])].sort((a, b) => STATE_ORDER[a.state] - STATE_ORDER[b.state] || a.nodeId.localeCompare(b.nodeId))
}

export interface ActiveAssignment {
  jobId: string
  nodeId: string
  runnerId: string | null
  state: ExecutionState
}

export interface RunnerSummary {
  active: ActiveAssignment[]
  home: string | null
  pin: string | null
}

export function runnerSummary(list: ExecutionStatus[] | undefined): RunnerSummary {
  const ordered = orderedStatuses(list)
  return {
    active: ordered.map((s) => ({ jobId: s.jobId, nodeId: s.nodeId, runnerId: s.runnerId ?? null, state: s.state })),
    home: ordered.find((s) => s.homeRunnerId)?.homeRunnerId ?? null,
    pin: ordered.find((s) => s.sessionPinRunnerId)?.sessionPinRunnerId ?? null,
  }
}

export function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KiB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MiB`
  return `${(n / 1024 ** 3).toFixed(1)} GiB`
}

export function moveProgressText(p: MoveProgress | undefined): string {
  if (!p || p.bytesTotal <= 0) return 'progress unknown'
  const done = Math.min(p.bytesDone, p.bytesTotal)
  return `${Math.floor((done / p.bytesTotal) * 100)}% (${humanBytes(p.bytesDone)} of ${humanBytes(p.bytesTotal)})`
}

/** Sort/accessor text uses the operation runner, not the home or session pin. */
export function runnerCellText(row: EnrichedInstanceSummary): string {
  return orderedStatuses(row.execution).find((s) => s.runnerId)?.runnerId ?? '—'
}
