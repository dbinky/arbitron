import { renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { T0, detail, execStatus, installFetch, iso, poolEvent, reply } from '../api/runners.fixtures'
import type { RunnerDetail } from '../api/types'
import { FakeWebSocket, POOL_URLS, emitSystem, flush, openSystemSockets, wsPoolEvent } from './poolSocket.fixtures'
import { POOL_EVENT_DEBOUNCE_MS } from './useLiveSnapshot'
import { applyRunnerEvent, useRunner } from './useRunner'

const A_URL = '/api/v1/runners/mac-a'
const B_URL = '/api/v1/runners/mac-b'
const SRC = 'mac-a/anthropic_subscription/-'

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(T0)
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const macA = (over: Partial<RunnerDetail> = {}) =>
  detail({
    assignments: [{ jobId: 'j1', runId: 'run-1', nodeId: 'review', state: 'running', sourceId: SRC, hidden: false }, { jobId: 'j2', runId: null, nodeId: 'x', state: 'running', sourceId: SRC, hidden: true }],
    homes: [{ runId: 'run-1', hidden: false, generation: 3, dirty: true }],
    sessions: [{ runId: 'run-5', nodeId: 'chat', hidden: false, generation: 2 }],
    ...over,
  })

function serve(routes: Record<string, unknown> = {}) {
  return installFetch({
    [A_URL]: macA(),
    [POOL_URLS.events]: [poolEvent({ id: 'e1', runnerId: 'mac-a', at: iso(1_000) }), poolEvent({ id: 'e2', runnerId: 'builtin', at: iso(2_000) })],
    ...routes,
  })
}

describe('useRunner', () => {
  it('loads a local authoritative pool and scoped runner detail', async () => {
    serve()
    const { result } = renderHook(() => useRunner('mac-a'))
    await flush()
    const s = result.current.state!
    expect(s.detail.id).toBe('mac-a')
    expect(s.detail.homes).toEqual([{ runId: 'run-1', hidden: false, generation: 3, dirty: true }])
    expect(s.detail.sessions).toHaveLength(1)
    expect(s.pool.runners.map((r) => r.id)).toEqual(['mac-a'])
    expect(s.pool.events.map((e) => e.id)).toEqual(['e1'])
    expect(s.pool.executions).toEqual([expect.objectContaining({ jobId: 'j1', runId: 'run-1', nodeId: 'review', runnerId: 'mac-a', sourceId: SRC })])
  })

  it('invalidates on this runner\'s events and renders the changed detail, ignoring other runners', async () => {
    const f = serve()
    const { result } = renderHook(() => useRunner('mac-a'))
    await flush()
    openSystemSockets()
    await flush()
    const reads = f.count('GET', A_URL)
    emitSystem(wsPoolEvent(poolEvent({ id: 'e4', kind: 'runner.cordoned', runnerId: 'builtin', at: iso(6_000) })))
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', A_URL)).toBe(reads)
    f.routes[A_URL] = macA({ cordoned: true, sessions: [] })
    emitSystem(wsPoolEvent(poolEvent({ id: 'e3', kind: 'runner.cordoned', runnerId: 'mac-a', at: iso(5_000) })))
    expect(result.current.state?.detail.cordoned).toBe(false) // never patched from the event
    expect(result.current.state?.pool.events.map((e) => e.id)).toEqual(['e3', 'e1'])
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', A_URL)).toBe(reads + 1)
    expect(result.current.state?.detail.cordoned).toBe(true)
    expect(result.current.state?.detail.sessions).toEqual([])
  })

  it('ignores old subject response/error after route switch', async () => {
    let failA: (e: Error) => void = () => {}
    const f = serve({
      [A_URL]: () => new Promise((_, reject) => (failA = reject)),
      [B_URL]: detail({ ...macA(), id: 'mac-b', name: 'mac-b' }),
    })
    const { result, rerender } = renderHook(({ id }) => useRunner(id), { initialProps: { id: 'mac-a' as string | undefined } })
    await flush()
    rerender({ id: 'mac-b' })
    await flush()
    expect(result.current.state?.detail.id).toBe('mac-b')
    failA(Object.assign(new Error('API 404: runner not found'), { status: 404 }))
    await flush()
    expect(result.current.error).toBeNull()
    expect(result.current.loading).toBe(false)
    expect(result.current.state?.detail.id).toBe('mac-b')
    expect(f.count('GET', B_URL)).toBe(1)
  })

  it('makes no request and reports no stale data without an id', async () => {
    const f = serve()
    const { result } = renderHook(() => useRunner(undefined))
    await flush()
    expect(f.calls).toHaveLength(0)
    expect(result.current.loading).toBe(false)
    expect(result.current.stale).toBe(false)
    expect(result.current.state).toBeNull()
  })

  it('surfaces a missing runner as an error', async () => {
    serve({ [A_URL]: reply(404, { error: 'runner not found' }) })
    const { result } = renderHook(() => useRunner('mac-a'))
    await flush()
    expect(result.current.state).toBeNull()
    expect(result.current.error).toBe('API 404: runner not found')
  })
})

describe('applyRunnerEvent', () => {
  const s = () => ({ detail: macA(), pool: { runners: [macA()], events: [], executions: [] } })
  it('scopes invalidation to this runner, its sources and its visible runs', () => {
    const st = s()
    const pool = (over = {}) => ({ kind: 'pool' as const, event: poolEvent({ id: 'p', ...over }) })
    expect(applyRunnerEvent(st, pool({ runnerId: 'mac-b' }), 'mac-a').refetch).toBe(false)
    expect(applyRunnerEvent(st, pool({ runnerId: 'mac-b', runId: 'run-1', kind: 'job.requeued' }), 'mac-a').refetch).toBe(true)
    expect(applyRunnerEvent(st, pool({ runnerId: '', sourceId: SRC }), 'mac-a').refetch).toBe(true)
    expect(applyRunnerEvent(st, pool({ runnerId: '', sourceId: 'mac-b/anthropic_subscription/-' }), 'mac-a').refetch).toBe(false)
    expect(applyRunnerEvent(st, { kind: 'execution', at: iso(), status: execStatus({ runId: 'run-9', runnerId: 'mac-b' }) }, 'mac-a').refetch).toBe(false)
    expect(applyRunnerEvent(st, { kind: 'execution', at: iso(), status: execStatus({ runId: 'run-9', runnerId: 'mac-a' }) }, 'mac-a').refetch).toBe(true)
    expect(applyRunnerEvent(st, { kind: 'node_done', instanceId: 'run-1', nodeId: 'review' }, 'mac-a').refetch).toBe(true)
    const other = applyRunnerEvent(st, pool({ runnerId: 'mac-b', runId: 'run-1' }), 'mac-a')
    expect(other.state).toBe(st) // other runners' events never join this log
    expect(applyRunnerEvent(st, { kind: 'ignored' }, 'mac-a')).toEqual({ state: st, refetch: false })
  })
})
