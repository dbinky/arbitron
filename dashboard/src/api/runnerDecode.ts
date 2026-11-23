import type {
  AssignmentView,
  DrainBlocker,
  DrainProgress,
  ExecutionState,
  ExecutionStatus,
  HomeView,
  NodeExecution,
  OutcomeClass,
  PoolEvent,
  RunnerCompatState,
  RunnerDetail,
  RunnerKey,
  RunnerKeyCreateResponse,
  RunnerLiveness,
  RunnerSource,
  RunnerTelemetry,
  RunnerView,
  SessionPinView,
  SourceAuthState,
  SourceCapacityState,
  SourceHealth,
  WebSocketEvent,
} from './types'

/**
 * Tolerant decoders for the Remote Runners DTOs. Rules: Go nil collections
 * become empty; absent timestamps/metrics become null ("unavailable");
 * unrecognized enum values fail CLOSED (offline, unknown health, incompatible)
 * so nothing is shown healthy that the server did not say is healthy; rows
 * without their identity are dropped; unknown event kinds are ignored.
 */

type Obj = Record<string, unknown>

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}
function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}
function strOrNull(v: unknown): string | null {
  return typeof v === 'string' && v !== '' ? v : null
}
function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}
function numOrNull(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}
function strList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}
function strMap(v: unknown): Record<string, string> {
  const out: Record<string, string> = {}
  if (!isObj(v)) return out
  for (const [k, val] of Object.entries(v)) if (typeof val === 'string') out[k] = val
  return out
}
function oneOf<T extends string>(v: unknown, allowed: readonly T[], fallback: T): T {
  return typeof v === 'string' && (allowed as readonly string[]).includes(v) ? (v as T) : fallback
}

/** A list endpoint may answer a bare array or `{ <key>: [...] }`. */
export function listOf(raw: unknown, key: string): unknown[] {
  if (Array.isArray(raw)) return raw
  if (isObj(raw) && Array.isArray(raw[key])) return raw[key] as unknown[]
  return []
}

export function decodeList<T>(raw: unknown, key: string, decode: (v: unknown) => T | null): T[] {
  const out: T[] = []
  for (const item of listOf(raw, key)) {
    const d = decode(item)
    if (d !== null) out.push(d)
  }
  return out
}

const LIVENESS: readonly RunnerLiveness[] = ['online', 'suspect', 'lost', 'offline']
const COMPAT: readonly RunnerCompatState[] = ['compatible', 'incompatible', 'chat_only']
const AUTH: readonly SourceAuthState[] = ['unknown', 'ok', 'logged_out']
const CAPACITY: readonly SourceCapacityState[] = ['available', 'exhausted', 'probation', 'unknown']
const OUTCOMES: readonly OutcomeClass[] = ['', 'exhausted', 'logged_out', 'transient', 'runner_lost', 'other', 'fenced']
const EXEC_STATES: readonly ExecutionState[] = ['queued_slot', 'waiting_capacity', 'running', 'moving_workspace', 'recovering']

export function decodeSourceHealth(raw: unknown): SourceHealth {
  const h = isObj(raw) ? raw : {}
  return {
    auth: oneOf(h.auth, AUTH, 'unknown'),
    capacity: oneOf(h.capacity, CAPACITY, 'unknown'),
    resetAt: strOrNull(h.resetAt),
    headroom: numOrNull(h.headroom),
    congested: h.congested === true,
    congestedUntil: strOrNull(h.congestedUntil),
    lastProbeAt: strOrNull(h.lastProbeAt),
    lastOutcome: oneOf(h.lastOutcome, OUTCOMES, ''),
  }
}

function decodeSource(raw: unknown): RunnerSource | null {
  if (!isObj(raw) || !str(raw.id)) return null
  return {
    id: str(raw.id),
    kind: str(raw.kind),
    credentialRef: str(raw.credentialRef),
    unavailableReason: str(raw.unavailableReason),
    families: strList(raw.families),
    backends: strList(raw.backends),
    models: strList(raw.models),
    health: decodeSourceHealth(raw.health),
  }
}

export function decodeTelemetry(raw: unknown): RunnerTelemetry | null {
  if (!isObj(raw)) return null
  return {
    sampledAt: strOrNull(raw.sampledAt),
    cpuPercent: numOrNull(raw.cpuPercent),
    memUsedBytes: numOrNull(raw.memUsedBytes),
    memPressure: strOrNull(raw.memPressure),
    load1Pct: numOrNull(raw.load1Pct),
    diskFreeBytes: numOrNull(raw.diskFreeBytes),
    thermal: strOrNull(raw.thermal),
    uptimeSeconds: numOrNull(raw.uptimeSeconds),
    cores: numOrNull(raw.cores),
    ramBytes: numOrNull(raw.ramBytes),
  }
}

/** True when at least one machine metric is known. */
export function telemetryAvailable(t: RunnerTelemetry | null): t is RunnerTelemetry {
  if (!t) return false
  return [t.cpuPercent, t.memUsedBytes, t.memPressure, t.load1Pct, t.diskFreeBytes, t.thermal, t.uptimeSeconds, t.cores, t.ramBytes].some(
    (v) => v !== null,
  )
}

/** Freshness is independent of socket status; 5s future skew tolerance. */
export function telemetryFresh(t: RunnerTelemetry | null, now: number, maxAgeMs = 30_000): t is RunnerTelemetry {
  if (!telemetryAvailable(t) || !t.sampledAt) return false
  const sampled = Date.parse(t.sampledAt)
  return Number.isFinite(sampled) && sampled > 0 && sampled <= now + 5_000 && now - sampled <= maxAgeMs
}

function decodeBlocker(raw: unknown): DrainBlocker | null {
  if (!isObj(raw)) return null
  return { kind: str(raw.kind), runId: strOrNull(raw.runId), group: str(raw.group), reason: str(raw.reason) }
}

function decodeDrain(raw: unknown): DrainProgress | null {
  if (!isObj(raw)) return null
  return {
    activeSteps: num(raw.activeSteps),
    homedWorkspaces: num(raw.homedWorkspaces),
    sessions: num(raw.sessions),
    blocked: decodeList(raw.blocked, 'blocked', decodeBlocker),
    complete: raw.complete === true,
  }
}

function decodeCompat(v: unknown): RunnerView['compat'] {
  if (!isObj(v)) return { state: 'compatible', reason: '' }
  const state = oneOf(v.state, COMPAT, 'incompatible')
  const unrecognized = state === 'incompatible' && v.state !== 'incompatible'
  const reason = str(v.reason) || (unrecognized ? `unrecognized compatibility state ${JSON.stringify(v.state ?? null)}` : '')
  return { state, reason }
}

export function decodeRunnerView(raw: unknown): RunnerView | null {
  if (!isObj(raw) || !str(raw.id)) return null
  const id = str(raw.id)
  return {
    id,
    name: str(raw.name) || id,
    builtIn: raw.builtIn === true,
    stepLimit: num(raw.stepLimit),
    labels: strMap(raw.labels),
    cordoned: raw.cordoned === true,
    draining: raw.draining === true,
    compat: decodeCompat(raw.compat),
    liveness: oneOf(raw.liveness, LIVENESS, 'offline'),
    lastContact: strOrNull(raw.lastContact),
    os: str(raw.os),
    runnerVersion: str(raw.runnerVersion),
    harnessVersions: strMap(raw.harnessVersions),
    slotsUsed: num(raw.slotsUsed),
    queued: num(raw.queued),
    homedWorkspaces: num(raw.homedWorkspaces),
    sources: decodeList(raw.sources, 'sources', decodeSource),
    telemetry: decodeTelemetry(raw.telemetry),
    drain: decodeDrain(raw.drain),
  }
}

function decodeAssignment(raw: unknown): AssignmentView | null {
  if (!isObj(raw) || !str(raw.jobId)) return null
  const hidden = raw.hidden === true
  return {
    jobId: str(raw.jobId),
    runId: hidden ? null : strOrNull(raw.runId),
    nodeId: str(raw.nodeId),
    state: str(raw.state),
    sourceId: str(raw.sourceId),
    hidden,
  }
}

function decodeSessionPin(raw: unknown): SessionPinView | null {
  if (!isObj(raw)) return null
  const hidden = raw.hidden === true
  return { runId: hidden ? null : strOrNull(raw.runId), nodeId: str(raw.nodeId), hidden, generation: num(raw.generation) }
}

function decodeHome(raw: unknown): HomeView | null {
  if (!isObj(raw)) return null
  const hidden = raw.hidden === true
  return { runId: hidden ? null : strOrNull(raw.runId), hidden, generation: num(raw.generation), dirty: raw.dirty === true }
}

export function decodeRunnerDetail(raw: unknown): RunnerDetail | null {
  const view = decodeRunnerView(raw)
  if (!view || !isObj(raw)) return null
  return {
    ...view,
    assignments: decodeList(raw.assignments, 'assignments', decodeAssignment),
    recentTelemetry: decodeList(raw.recentTelemetry, 'recentTelemetry', decodeTelemetry),
    sessions: decodeList(raw.sessions, 'sessions', decodeSessionPin),
    homes: decodeList(raw.homes, 'homes', decodeHome),
  }
}

export function decodePoolEvent(raw: unknown): PoolEvent | null {
  if (!isObj(raw)) return null
  const id = str(raw.id)
  const kind = str(raw.kind)
  const at = str(raw.at)
  if (!id || !kind || !at) return null
  const cause = isObj(raw.cause) ? raw.cause : {}
  return {
    id,
    at,
    kind,
    runnerId: str(raw.runnerId),
    sourceId: str(raw.sourceId),
    runId: strOrNull(raw.runId),
    cause: { kind: str(cause.kind), actor: str(cause.actor), outcome: oneOf(cause.outcome, OUTCOMES, '') },
    detail: isObj(raw.detail) ? raw.detail : {},
  }
}

export function decodeRunnerKey(raw: unknown): RunnerKey | null {
  if (!isObj(raw) || !str(raw.id) || !str(raw.name)) return null
  return {
    id: str(raw.id),
    name: str(raw.name),
    runnerId: str(raw.runnerId),
    prefix: str(raw.prefix),
    claimed: raw.claimed === true,
    revoked: raw.revoked === true,
    createdAt: str(raw.createdAt),
    claimedAt: strOrNull(raw.claimedAt),
    lastUsedAt: strOrNull(raw.lastUsedAt),
    revokedAt: strOrNull(raw.revokedAt),
  }
}

/**
 * The one-time create response, P2 T3 `{ key: runnerKeyView, plaintext, setup }`.
 * Returns null (never a partial) when either the metadata or the plaintext is missing.
 */
export function decodeKeyCreate(raw: unknown): RunnerKeyCreateResponse | null {
  if (!isObj(raw)) return null
  const plaintext = str(raw.plaintext)
  const key = isObj(raw.key) ? decodeRunnerKey(raw.key) : null
  return plaintext && key ? { key, plaintext, setup: str(raw.setup) } : null
}

export function decodeExecutionStatus(raw: unknown): ExecutionStatus | null {
  if (!isObj(raw)) return null
  const runId = str(raw.runId)
  const jobId = str(raw.jobId)
  if (!runId || !jobId || typeof raw.state !== 'string' || !(EXEC_STATES as readonly string[]).includes(raw.state)) return null
  const out: ExecutionStatus = {
    runId,
    nodeId: str(raw.nodeId),
    operationId: str(raw.operationId),
    jobId,
    state: raw.state as ExecutionState,
  }
  const optional = ['runnerId', 'sourceId', 'group', 'model', 'reason', 'nextReset', 'homeRunnerId', 'sessionPinRunnerId'] as const
  for (const field of optional) {
    const v = raw[field]
    if (typeof v === 'string' && v !== '') out[field] = v
  }
  if (isObj(raw.progress)) out.progress = { bytesDone: num(raw.progress.bytesDone), bytesTotal: num(raw.progress.bytesTotal) }
  return out
}

export type LiveEvent =
  | { kind: 'pool'; event: PoolEvent }
  | { kind: 'execution'; status: ExecutionStatus; at: string }
  | { kind: 'node_done'; instanceId: string; nodeId: string }
  | { kind: 'ignored' }

/** Retain trace data, but never expose a session assignment with an invalid identity. */
export function decodeNodeExecutionSession(raw: NodeExecution): NodeExecution {
  const id = raw.sessionInvocationId
  const valid = typeof id === 'string'
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id)
    && id !== '00000000-0000-0000-0000-000000000000'
  if (valid) return raw
  const out = { ...raw }
  delete out.sessionInvocationId
  delete out.sessionPinRunnerId
  delete out.sessionGeneration
  delete out.sessionMoves
  return out
}

const IGNORED: LiveEvent = { kind: 'ignored' }
const POOL_PREFIXES = ['runner.', 'source.', 'job.', 'pool.']
const RUN_SCOPED_POOL_KINDS = new Set(['workspace.moved', 'session.moved'])
const NODE_DONE_KINDS = new Set(['node.completed', 'node.failed', 'node.skipped'])

/** Event kinds recorded in the pool event log (contracts §9). */
export function isPoolEventKind(type: string): boolean {
  if (RUN_SCOPED_POOL_KINDS.has(type)) return true
  return POOL_PREFIXES.some((p) => type.startsWith(p) && type.length > p.length)
}

/** Narrows a system-stream frame to what the runner views consume; everything else is ignored. */
export function decodeLiveEvent(ev: WebSocketEvent): LiveEvent {
  if (!ev || typeof ev.type !== 'string') return IGNORED
  if (ev.type === 'execution.status') {
    const status = isObj(ev.data) ? decodeExecutionStatus({ runId: ev.instanceId, ...ev.data }) : null
    return status ? { kind: 'execution', status, at: str(ev.timestamp) } : IGNORED
  }
  if (NODE_DONE_KINDS.has(ev.type)) {
    const nodeId = isObj(ev.data) ? str(ev.data.nodeId) : ''
    return ev.instanceId && nodeId ? { kind: 'node_done', instanceId: ev.instanceId, nodeId } : IGNORED
  }
  if (!isPoolEventKind(ev.type)) return IGNORED
  // Every pool kind, including workspace.moved (P3 T6) and session.moved (P4 T8),
  // is published by runner.EventRecorder with data = PoolEventView (P1 T7).
  const decoded = decodePoolEvent(ev.data)
  if (!decoded) return IGNORED
  return { kind: 'pool', event: decoded.kind === ev.type ? decoded : { ...decoded, kind: ev.type } }
}
