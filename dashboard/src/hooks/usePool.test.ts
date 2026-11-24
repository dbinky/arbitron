import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { T0, execStatus, health, iso, poolEvent, remote, reply, row, runner, source } from '../api/runners.fixtures'
import { POOL_EVENT_LIMIT } from './poolState'
import {
  FakeWebSocket,
  POOL_URLS,
  dropSystemSockets,
  emitSystem,
  flush,
  installPool,
  openSystemSockets,
  wsPoolEvent,
  type PoolWorld,
} from './poolSocket.fixtures'
import { POOL_EVENT_DEBOUNCE_MS, POOL_POLL_MS, POOL_REFRESH_MS } from './useLiveSnapshot'
import { executionsOf, usePool } from './usePool'

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

const SRC = 'mac-a/anthropic_subscription/-'
const macA = (over = {}) => remote('mac-a', { sources: [source({ id: SRC })], ...over })
const world = (over: Partial<PoolWorld> = {}): PoolWorld => ({
  runners: [runner(), macA()],
  events: [],
  running: [],
  waiting: [],
  ...over,
})
type Pool = ReturnType<typeof usePool>
const runnerOf = (r: Pool, id = 'mac-a') => r.state?.runners.find((x) => x.id === id)

/** Mount, finish the initial read, open the socket and finish the connect refresh. */
async function live(w: PoolWorld) {
  const f = installPool(w)
  const hook = renderHook(() => usePool())
  await flush()
  openSystemSockets()
  await flush()
  return { f, ...hook }
}

describe('usePool — happy', () => {
  it('requests the event log with the pool limit', () => {
    expect(POOL_URLS.events).toBe(`/api/v1/runners/events?limit=${POOL_EVENT_LIMIT}`)
  })

  it('loads a local authoritative pool: runners, log, running and waiting jobs', async () => {
    installPool(
      world({
        events: [poolEvent({ id: 'e1', runnerId: 'mac-a' })],
        running: [row('run-1', [execStatus({ jobId: 'j1', runId: 'run-1', runnerId: 'mac-a', sourceId: SRC, state: 'running', homeRunnerId: 'mac-a' })])],
        waiting: [row('run-2', [execStatus({ runId: '', jobId: 'j2', state: 'waiting_capacity', reason: 'all anthropic subscriptions exhausted', sessionPinRunnerId: 'mac-a' })], { state: 'waiting' })],
      }),
    )
    const { result } = renderHook(() => usePool())
    expect(result.current.loading).toBe(true)
    await flush()
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toBeNull()
    expect(result.current.state?.runners.map((r) => r.id)).toEqual(['builtin', 'mac-a'])
    expect(runnerOf(result.current)?.sources[0].id).toBe(SRC)
    expect(result.current.state?.events.map((e) => e.id)).toEqual(['e1'])
    expect(result.current.state?.executions).toEqual([
      expect.objectContaining({ jobId: 'j1', runId: 'run-1', runnerId: 'mac-a', sourceId: SRC, homeRunnerId: 'mac-a' }),
      expect.objectContaining({ jobId: 'j2', runId: 'run-2', state: 'waiting_capacity', sessionPinRunnerId: 'mac-a' }),
    ])
    expect(result.current.stale).toBe(true) // socket not open yet
    openSystemSockets()
    await flush()
    expect(result.current.connected).toBe(true)
    expect(result.current.stale).toBe(false)
  })
})

describe('usePool — success', () => {
  it('invalidates then renders changed server health and assignments', async () => {
    const w = world({ running: [row('run-1', [execStatus({ jobId: 'j1', runnerId: 'builtin' })])] })
    const { f, result } = await live(w)
    const reads = f.count('GET', POOL_URLS.runners)
    w.runners = [runner(), macA({ cordoned: true, sources: [source({ id: SRC, health: health({ capacity: 'exhausted', resetAt: iso(600_000) }) })] })]
    w.running = [row('run-1', [execStatus({ jobId: 'j1', runnerId: 'mac-a', sourceId: SRC })])]
    // The event deliberately contradicts the server: only the REST producer may feed the view.
    emitSystem(wsPoolEvent(poolEvent({ id: 'e2', kind: 'source.health_changed', runnerId: 'mac-a', sourceId: SRC, at: iso(5_000), detail: { capacity: 'available' } })))
    expect(runnerOf(result.current)?.cordoned).toBe(false)
    expect(result.current.state?.events[0].id).toBe('e2')
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 1)
    expect(runnerOf(result.current)?.cordoned).toBe(true)
    expect(runnerOf(result.current)?.sources[0].health).toMatchObject({ capacity: 'exhausted', resetAt: iso(600_000) })
    expect(result.current.state?.executions).toEqual([expect.objectContaining({ jobId: 'j1', runnerId: 'mac-a', sourceId: SRC })])
  })

  it('coalesces a burst of invalidations into one REST read', async () => {
    const { f } = await live(world())
    const reads = f.count('GET', POOL_URLS.runners)
    emitSystem(wsPoolEvent(poolEvent({ id: 'u1', kind: 'runner.updated', runnerId: 'mac-a' })))
    emitSystem(wsPoolEvent(poolEvent({ id: 'u2', kind: 'runner.updated', runnerId: 'mac-a' })))
    emitSystem({ type: 'execution.status', timestamp: iso(), instanceId: 'run-1', data: execStatus({ jobId: 'j1' }) })
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 1)
  })
})

describe('usePool — failure', () => {
  it('keeps exhausted source exhausted after an out-of-order recovered event', async () => {
    const exhausted = macA({ sources: [source({ id: SRC, health: health({ capacity: 'exhausted', resetAt: iso(600_000) }) })] })
    const { result } = await live(world({ runners: [runner(), exhausted] }))
    emitSystem(wsPoolEvent(poolEvent({ id: 'late', kind: 'source.recovered', runnerId: 'mac-a', sourceId: SRC, at: iso(9_000_000) })))
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(runnerOf(result.current)?.sources[0].health.capacity).toBe('exhausted')
  })

  it('never resurrects an old node visit when delayed execution.status follows completion', async () => {
    const w = world({ running: [row('run-1', [execStatus({ jobId: 'visit-1', nodeId: 'review' })])] })
    const { result } = await live(w)
    w.running = [row('run-1', [execStatus({ jobId: 'visit-2', nodeId: 'review' })])]
    emitSystem({ type: 'node.completed', timestamp: iso(1_000), instanceId: 'run-1', data: { nodeId: 'review' } })
    emitSystem({ type: 'execution.status', timestamp: iso(500), instanceId: 'run-1', data: execStatus({ jobId: 'visit-1', nodeId: 'review', state: 'running' }) })
    expect(result.current.state?.executions.map((e) => e.jobId)).toEqual(['visit-1'])
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(result.current.state?.executions.map((e) => e.jobId)).toEqual(['visit-2'])
  })
})

describe('usePool — error', () => {
  it('shows stale data on REST failure while socket stays connected and clears stale only after successful retry', async () => {
    const w = world()
    const { result } = await live(w)
    expect(result.current.stale).toBe(false)
    w.runners = reply(500, { error: 'registry unavailable' })
    emitSystem(wsPoolEvent(poolEvent({ id: 'x', kind: 'runner.updated', runnerId: 'mac-a' })))
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(result.current.connected).toBe(true)
    expect(result.current.error).toBe('API 500: registry unavailable')
    expect(result.current.stale).toBe(true)
    expect(result.current.state?.runners.map((r) => r.id)).toEqual(['builtin', 'mac-a'])
    w.runners = [runner(), macA({ cordoned: true })]
    await act(async () => {
      await result.current.refetch()
    })
    expect(result.current.error).toBeNull()
    expect(result.current.stale).toBe(false)
    expect(runnerOf(result.current)?.cordoned).toBe(true)
  })

  it('disconnects, polls, reconnects and surfaces retry errors', async () => {
    const w = world()
    const { f, result } = await live(w)
    const reads = f.count('GET', POOL_URLS.runners)
    dropSystemSockets()
    expect(result.current.stale).toBe(true)
    await flush(POOL_POLL_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 1)
    w.runners = reply(503, { error: 'down' })
    await flush(POOL_POLL_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 2)
    expect(result.current.error).toBe('API 503: down')
    expect(result.current.state?.runners).toHaveLength(2)
    w.runners = [runner()]
    openSystemSockets() // the 3s auto-reconnect created a new socket during the polls
    expect(result.current.stale).toBe(true) // connected alone does not prove freshness
    await flush()
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 3)
    expect(result.current.error).toBeNull()
    expect(result.current.stale).toBe(false)
    expect(result.current.state?.runners).toHaveLength(1)
  })

  it('while connected refreshes on the slower telemetry cadence only', async () => {
    const { f } = await live(world())
    const reads = f.count('GET', POOL_URLS.runners)
    await flush(POOL_REFRESH_MS - 1)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads)
    await flush(1)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 1)
  })
})

describe('usePool — edge', () => {
  it('serializes overlapping loads and coalesces an event-during-load into a fresh read', async () => {
    const w = world()
    const { f, result } = await live(w)
    let release: () => void = () => {}
    f.routes[POOL_URLS.runners] = () =>
      new Promise((resolve) => {
        release = () => resolve([runner(), macA({ liveness: 'online' })])
      })
    let first: Promise<void> = Promise.resolve()
    act(() => {
      first = result.current.refetch()
    })
    const reads = f.count('GET', POOL_URLS.runners)
    // Server changes, then the event arrives while the read is in flight.
    w.runners = [runner(), macA({ liveness: 'suspect' })]
    emitSystem(wsPoolEvent(poolEvent({ id: 'e7', kind: 'runner.suspect', runnerId: 'mac-a', at: iso(20_000) })))
    act(() => {
      void result.current.refetch() // a manual retry joins the active load
    })
    await flush(POOL_EVENT_DEBOUNCE_MS * 3)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads) // still one active load
    f.routes[POOL_URLS.runners] = () => w.runners
    await act(async () => {
      release()
      await first
    })
    expect(runnerOf(result.current)?.liveness).toBe('online') // result of the racing read, not a replayed patch
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 1) // exactly one dirty follow-up
    expect(runnerOf(result.current)?.liveness).toBe('suspect')
  })

  it('ignores unknown and malformed events: no state change, no reload', async () => {
    const { f, result } = await live(world())
    const reads = f.count('GET', POOL_URLS.runners)
    const before = result.current.state
    emitSystem({ type: 'teleport.started', timestamp: iso(), data: { id: 'x', at: iso(), kind: 'teleport.started' } })
    emitSystem({ type: 'runner.updated', timestamp: iso(), data: 'garbage' })
    emitSystem({ type: 'interactive.token', timestamp: iso(), data: { token: 'hi' }, instanceId: 'run-1' })
    expect(result.current.state).toBe(before)
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads)
  })

  it('stops all work on unmount', async () => {
    const { f, unmount } = await live(world())
    const reads = f.count('GET', POOL_URLS.runners)
    unmount()
    await flush(POOL_REFRESH_MS * 2)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads)
  })
})

describe('executionsOf', () => {
  it('flattens visible rows, fills the run id, drops malformed entries and dedupes by job id', () => {
    const rows = [
      row('a', [execStatus({ jobId: 'j1', runId: '' })]),
      row('b'),
      row('c', [{ jobId: 'j3', state: 'exploded' } as never, execStatus({ jobId: 'j4', runId: 'c' })]),
      row('c', [execStatus({ jobId: 'j4', runId: 'c', state: 'waiting_capacity' })], { state: 'waiting' }),
    ]
    expect(executionsOf(rows).map((s) => [s.runId, s.jobId, s.state])).toEqual([
      ['a', 'j1', 'running'],
      ['c', 'j4', 'running'],
    ])
  })
})
