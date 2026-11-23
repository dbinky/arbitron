import type { ExecutionStatus, OutcomeClass, PoolEventCause, RunnerSource, RunnerView } from '../api/types'

export type SourceStatus = 'healthy' | 'pressure' | 'exhausted' | 'logged_out' | 'probation' | 'unknown' | 'unavailable'

export const STATUS_TEXT: Record<SourceStatus, string> = {
  unavailable: 'Unavailable',
  healthy: 'healthy',
  pressure: 'busy · pressure',
  exhausted: 'exhausted',
  logged_out: 'logged out',
  probation: 'probation',
  unknown: 'status unknown',
}

export const OUTCOME_TEXT: Record<OutcomeClass, string> = {
  '': 'no outcome',
  exhausted: 'subscription exhausted',
  logged_out: 'logged out',
  transient: 'temporary congestion',
  runner_lost: 'runner lost',
  other: 'other failure',
  fenced: 'rolled back to the last committed checkpoint',
}

/** Durable disclosure (spec "Recovery and session continuity"): retries can repeat external effects. */
export const RECOVERY_DISCLOSURE =
  'Interrupted work is repeated automatically and may repeat external effects (for example a push or a comment); exactly-once effects are not guaranteed.'

const CREDENTIAL_LABELS: Record<string, string> = {
  anthropic_subscription: 'Anthropic subscription',
  openai_subscription: 'OpenAI subscription',
  lunaroute: 'LunaRoute',
  server_connection: 'Server connection',
  local_harness: 'Local harness',
  none: 'Process steps',
}
/** Credential kinds whose ModelGroup string carries the connection ref. */
const REF_KINDS = new Set(['lunaroute', 'server_connection', 'local_harness'])

export function credentialLabel(kind: string): string {
  return CREDENTIAL_LABELS[kind] ?? kind
}

export function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** Mirrors runner.ModelGroup.String (contracts §6). */
export function groupKey(kind: string, ref: string, family: string): string {
  if (kind === 'none') return 'none'
  return REF_KINDS.has(kind) && ref ? `${kind}:${ref}:${family}` : `${kind}:${family}`
}

function groupLabel(kind: string, ref: string, family: string): string {
  if (kind === 'none') return 'Process steps (no credential)'
  const refText = REF_KINDS.has(kind) && ref ? ` (${ref})` : ''
  return `${credentialLabel(kind)}${refText} · ${family}`
}

/**
 * One status per source from server fields only. Logged-out and exhausted are
 * independent: a restored login leaves an exhausted source exhausted. LunaRoute
 * (or any provider) congestion is pressure, never exhaustion.
 */
export function sourceStatus(s: RunnerSource): SourceStatus {
  if (s.unavailableReason) return 'unavailable'
  const h = s.health
  if (h.auth === 'logged_out') return 'logged_out'
  if (h.capacity === 'exhausted') return 'exhausted'
  if (h.capacity === 'probation') return 'probation'
  if (h.capacity === 'unknown' || h.auth === 'unknown') return 'unknown'
  if (h.congested) return 'pressure'
  return 'healthy'
}

export function runnerAcceptsWork(r: RunnerView): boolean {
  return r.liveness === 'online' && !r.cordoned && !r.draining && r.compat.state !== 'incompatible'
}

export interface GroupMember {
  runnerId: string
  runnerName: string
  source: RunnerSource
  status: SourceStatus
  eligible: boolean
}
export interface SpreadEntry {
  runnerId: string
  runnerName: string
  inFlight: number
}
export interface ModelGroupCard {
  key: string
  label: string
  members: GroupMember[]
  healthy: number
  total: number
  inFlight: number
  queued: number
  nextReset: string | null
  pressure: boolean
  spread: SpreadEntry[]
}

const IN_FLIGHT = new Set(['running', 'moving_workspace'])
const QUEUED = new Set(['queued_slot', 'waiting_capacity', 'recovering'])

export function buildModelGroups(runners: RunnerView[], executions: ExecutionStatus[]): ModelGroupCard[] {
  const cards = new Map<string, ModelGroupCard>()
  const names = new Map(runners.map((r) => [r.id, r.name]))
  for (const r of runners) {
    const eligible = runnerAcceptsWork(r)
    for (const s of r.sources) {
      const families = s.kind === 'none' ? [''] : s.families.length > 0 ? s.families : ['unknown family']
      for (const family of families) {
        const key = groupKey(s.kind, s.credentialRef, family)
        let card = cards.get(key)
        if (!card) {
          card = { key, label: groupLabel(s.kind, s.credentialRef, family), members: [], healthy: 0, total: 0, inFlight: 0, queued: 0, nextReset: null, pressure: false, spread: [] }
          cards.set(key, card)
        }
        const status = sourceStatus(s)
        card.members.push({ runnerId: r.id, runnerName: r.name, source: s, status, eligible })
        card.total += 1
        if (eligible && (status === 'healthy' || status === 'pressure')) card.healthy += 1
        if (status === 'pressure') card.pressure = true
        const reset = s.health.resetAt
        if (status === 'exhausted' && reset && (card.nextReset === null || Date.parse(reset) < Date.parse(card.nextReset))) {
          card.nextReset = reset
        }
        if (!card.spread.some((e) => e.runnerId === r.id)) card.spread.push({ runnerId: r.id, runnerName: r.name, inFlight: 0 })
      }
    }
  }
  for (const e of executions) {
    const card = e.group ? cards.get(e.group) : undefined
    if (!card) continue
    if (IN_FLIGHT.has(e.state)) {
      card.inFlight += 1
      if (e.runnerId) {
        let entry = card.spread.find((s) => s.runnerId === e.runnerId)
        if (!entry) {
          entry = { runnerId: e.runnerId, runnerName: names.get(e.runnerId) ?? e.runnerId, inFlight: 0 }
          card.spread.push(entry)
        }
        entry.inFlight += 1
      }
    } else if (QUEUED.has(e.state)) {
      card.queued += 1
    }
  }
  return [...cards.values()].sort((a, b) => a.key.localeCompare(b.key))
}

export type AlertKind = 'logged_out' | 'incompatible' | 'chat_only' | 'runner_down' | 'drain_blocked' | 'waiting_capacity' | 'recovering'
export interface PoolAlert {
  id: string
  kind: AlertKind
  severity: 'error' | 'warning'
  title: string
  detail: string
  /** Visible run to link; null when none or hidden (the server never sends hidden ids). */
  runId: string | null
  /** For capacity waits: the next known reset (absolute), rendered as a ticking countdown. */
  resetAt?: string | null
}

const DOWN_TEXT: Record<string, string> = {
  suspect: 'No contact for about 30 seconds; it receives no new work.',
  lost: 'Lost contact; its work is being recovered on other runners.',
  offline: 'Disconnected.',
}

export function buildAlerts(runners: RunnerView[], executions: ExecutionStatus[]): PoolAlert[] {
  const out: PoolAlert[] = []
  for (const r of runners) {
    for (const s of r.sources) {
      if (s.health.auth !== 'logged_out') continue
      out.push({
        id: `logged_out:${s.id}`,
        kind: 'logged_out',
        severity: 'error',
        title: `${r.name}: ${credentialLabel(s.kind)} is logged out`,
        detail: 'Log in again on that machine; the source returns to service automatically once login is restored.',
        runId: null,
      })
    }
  }
  for (const r of runners) {
    if (r.compat.state === 'incompatible') {
      out.push({ id: `incompatible:${r.id}`, kind: 'incompatible', severity: 'error', title: `${r.name} is incompatible`, detail: `${r.compat.reason || 'No explanation reported'}. It receives no new work until resolved.`, runId: null })
    } else if (r.compat.state === 'chat_only') {
      out.push({ id: `chat_only:${r.id}`, kind: 'chat_only', severity: 'warning', title: `${r.name} runs chat steps only`, detail: r.compat.reason || 'No explanation reported', runId: null })
    }
  }
  for (const r of runners) {
    if (r.liveness === 'online') continue
    out.push({ id: `down:${r.id}`, kind: 'runner_down', severity: 'error', title: `${r.name} is ${r.liveness}`, detail: DOWN_TEXT[r.liveness] ?? '', runId: null })
  }
  for (const r of runners) {
    if (!r.draining || !r.drain || r.drain.complete) continue
    r.drain.blocked.forEach((b, i) => {
      out.push({ id: `drain:${r.id}:${i}`, kind: 'drain_blocked', severity: 'error', title: `Drain of ${r.name} is blocked`, detail: `${b.kind}: ${b.reason}${b.group ? ` (${b.group})` : ''}`, runId: b.runId })
    })
  }
  for (const e of executions) {
    if (e.state === 'waiting_capacity') {
      out.push({
        id: `wait:${e.jobId}`,
        kind: 'waiting_capacity',
        severity: 'warning',
        title: `Run waiting · capacity (${e.nodeId})`,
        detail: `${e.reason || 'No eligible capacity'}${e.model ? ` · model ${e.model}` : ''}`,
        runId: e.runId || null,
        resetAt: e.nextReset ?? null,
      })
    } else if (e.state === 'recovering') {
      out.push({ id: `recover:${e.jobId}`, kind: 'recovering', severity: 'warning', title: `Recovering interrupted work (${e.nodeId})`, detail: RECOVERY_DISCLOSURE, runId: e.runId || null })
    }
  }
  return out
}

export function causeLabel(cause: PoolEventCause): string {
  switch (cause.kind) {
    case 'operator':
      return `by ${cause.actor || 'an operator'}`
    case 'outcome':
      return `outcome: ${OUTCOME_TEXT[cause.outcome] ?? cause.outcome}`
    case 'liveness':
      return 'liveness check'
    case 'placement':
      return 'placement'
    case 'system':
      return 'system'
    default:
      return cause.kind || 'unknown cause'
  }
}

export function eventLabel(kind: string): string {
  return kind.replace(/[._]/g, ' ')
}

export function formatBytes(n: number | null): string {
  return n === null ? '—' : `${(n / 1024 ** 3).toFixed(1)} GiB`
}

export function formatPercent(n: number | null): string {
  return n === null ? '—' : `${Math.round(n)}%`
}
