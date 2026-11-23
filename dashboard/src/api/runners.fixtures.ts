import { vi, type Mock } from 'vitest'
import type {
  EnrichedInstanceSummary,
  ExecutionStatus,
  PoolEvent,
  RunnerDetail,
  RunnerKey,
  RunnerSource,
  RunnerTelemetry,
  RunnerView,
  SourceHealth,
} from './types'

/** Fixed test epoch; tests call vi.setSystemTime(T0) when they need "now". */
export const T0 = Date.parse('2026-11-20T10:00:00Z')
export const NIL_UUID = '00000000-0000-0000-0000-000000000000'
export const iso = (offsetMs = 0): string => new Date(T0 + offsetMs).toISOString()

export function health(over: Partial<SourceHealth> = {}): SourceHealth {
  return {
    auth: 'ok',
    capacity: 'available',
    resetAt: null,
    headroom: 0.8,
    congested: false,
    congestedUntil: null,
    lastProbeAt: iso(-30_000),
    lastOutcome: '',
    ...over,
  }
}

export function source(over: Partial<RunnerSource> = {}): RunnerSource {
  return {
    id: 'builtin/anthropic_subscription/-',
    kind: 'anthropic_subscription',
    credentialRef: '',
    families: ['anthropic'],
    backends: ['claude-code'],
    unavailableReason: '',
    models: [],
    health: health(),
    ...over,
  }
}

export function telemetry(over: Partial<RunnerTelemetry> = {}): RunnerTelemetry {
  return {
    sampledAt: iso(-5_000),
    cpuPercent: 42,
    memUsedBytes: 8 * 1024 ** 3,
    memPressure: 'normal',
    load1Pct: 30,
    diskFreeBytes: 100 * 1024 ** 3,
    thermal: 'nominal',
    uptimeSeconds: 7_200,
    cores: 10,
    ramBytes: 32 * 1024 ** 3,
    ...over,
  }
}

/** The built-in runner as a healthy one-machine pool reports it. */
export function runner(over: Partial<RunnerView> = {}): RunnerView {
  return {
    id: 'builtin',
    name: 'builtin',
    builtIn: true,
    stepLimit: 8,
    labels: {},
    cordoned: false,
    draining: false,
    compat: { state: 'compatible', reason: '' },
    liveness: 'online',
    lastContact: iso(-1_000),
    os: 'darwin',
    runnerVersion: '1.0.0',
    harnessVersions: { 'claude-code': '2.1.0' },
    slotsUsed: 0,
    queued: 0,
    homedWorkspaces: 0,
    sources: [source()],
    telemetry: telemetry(),
    drain: null,
    ...over,
  }
}

export function remote(id: string, over: Partial<RunnerView> = {}): RunnerView {
  return runner({
    id,
    name: id,
    builtIn: false,
    sources: [source({ id: `${id}/anthropic_subscription/-` })],
    ...over,
  })
}

export function detail(over: Partial<RunnerDetail> = {}): RunnerDetail {
  return {
    ...remote('mac-a'),
    assignments: [],
    recentTelemetry: [],
    sessions: [],
    homes: [],
    ...over,
  }
}

export function poolEvent(over: Partial<PoolEvent> = {}): PoolEvent {
  return {
    id: 'ev-1',
    at: iso(),
    kind: 'runner.updated',
    runnerId: 'builtin',
    sourceId: '',
    runId: null,
    cause: { kind: 'operator', actor: 'admin', outcome: '' },
    detail: {},
    ...over,
  }
}

export function execStatus(over: Partial<ExecutionStatus> = {}): ExecutionStatus {
  return {
    runId: 'run-1',
    nodeId: 'review',
    operationId: 'node',
    jobId: 'job-1',
    state: 'running',
    runnerId: 'builtin',
    group: 'anthropic_subscription:anthropic',
    model: 'claude-opus-4-1',
    ...over,
  }
}

export function key(over: Partial<RunnerKey> = {}): RunnerKey {
  return {
    id: 'key-1',
    name: 'mac-mini',
    runnerId: 'mac-mini',
    prefix: 'ark_AbCd',
    claimed: false,
    revoked: false,
    createdAt: iso(-86_400_000),
    claimedAt: null,
    lastUsedAt: null,
    revokedAt: null,
    ...over,
  }
}

export function row(
  id: string,
  execution?: ExecutionStatus[],
  over: Partial<EnrichedInstanceSummary> = {},
): EnrichedInstanceSummary {
  return {
    id,
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'running',
    priority: 'normal',
    createdAt: iso(-60_000),
    updatedAt: iso(-1_000),
    runName: `run — ${id}`,
    retryCount: 0,
    ...(execution ? { execution } : {}),
    ...over,
  }
}

export interface Reply {
  readonly __reply: true
  status: number
  body?: unknown
}
/** An explicit HTTP reply; a plain route value is served as a 200 JSON body. */
export const reply = (status: number, body?: unknown): Reply => ({ __reply: true, status, body })
function isReply(v: unknown): v is Reply {
  return typeof v === 'object' && v !== null && (v as { __reply?: unknown }).__reply === true
}

function response(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: `HTTP ${status}`,
    json: async () => {
      if (body === undefined) throw new SyntaxError('Unexpected end of JSON input')
      return body
    },
  }
}

export interface FetchCall {
  url: string
  method: string
  body: string | undefined
}

/**
 * Stubs global fetch with an exact-URL router. Keys are "<METHOD> <url>" or a
 * bare "<url>" (GET). A route value may be a body, a reply(), or a function of
 * the RequestInit returning either (sync or async). Unknown URLs are 404s, so a
 * caller that builds the wrong URL never gets a lucky answer.
 */
export function installFetch(routes: Record<string, unknown>) {
  const calls: FetchCall[] = []
  const fn: Mock = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ url, method, body: typeof init?.body === 'string' ? init.body : undefined })
    const route = routes[`${method} ${url}`] ?? (method === 'GET' ? routes[url] : undefined)
    if (route === undefined) return response(404, { error: `no route for ${method} ${url}` })
    const value = typeof route === 'function' ? await (route as (i?: RequestInit) => unknown)(init) : route
    return isReply(value) ? response(value.status, value.body) : response(200, value)
  })
  vi.stubGlobal('fetch', fn)
  return {
    fn,
    calls,
    routes,
    count: (method: string, url: string) => calls.filter((c) => c.method === method && c.url === url).length,
  }
}
