import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import type { GraphState, WebSocketEvent } from '../api/types'
import {
  GRAPH_STATE_EVENT_DEBOUNCE_MS,
  GRAPH_STATE_POLL_MS,
  isGraphStateRefreshEvent,
  useGraphState,
} from './useGraphState'

// Only browser primitives are faked: the API client, instance socket,
// reconnect timer and reconnect resync hooks all run for real.
class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []
  readonly url: string
  readyState = FakeWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((msg: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  private closeListeners: Array<() => void> = []

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }
  addEventListener(type: string, fn: () => void) {
    if (type === 'close') this.closeListeners.push(fn)
  }
  send() {}
  open() {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.()
  }
  emit(event: WebSocketEvent) {
    this.onmessage?.({ data: JSON.stringify(event) })
  }
  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
    for (const fn of this.closeListeners) fn()
  }
}

function latestSocket(): FakeWebSocket {
  const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
  if (!ws) throw new Error('no WebSocket was opened')
  return ws
}
function liveSockets(): FakeWebSocket[] {
  return FakeWebSocket.instances.filter((w) => w.readyState !== FakeWebSocket.CLOSED)
}

interface Deferred {
  resolve: (body: unknown) => void
  reject: (status: number, message: string) => void
}

// Unknown URLs fail; a known URL repeats its last reply after its queue drains.
let replies: Record<string, Array<unknown | 'defer'>>
let lastReply: Record<string, unknown>
let deferreds: Deferred[]
const fetchMock = vi.fn()
function graphStateCalls(id: string): number {
  return fetchMock.mock.calls.filter(([url]) => url === `/api/v1/instances/${id}/graph-state`).length
}
function state(over: Partial<GraphState> = {}): GraphState {
  return {
    rootInstanceId: 'root-1', version: 'v1', active: true,
    contexts: [{ instanceId: 'root-1', kind: 'root', state: 'running', blocking: true, redacted: false, nodes: [{ instanceId: 'root-1', nodeId: 'a', state: 'running' }] }],
    groups: [], warnings: [], ...over,
  }
}
const done = (version: string): GraphState => state({
  version, active: false,
  contexts: [{ instanceId: 'root-1', kind: 'root', state: 'completed', blocking: true, redacted: false, nodes: [{ instanceId: 'root-1', nodeId: 'a', state: 'completed' }] }],
})
const ev = (type: string): WebSocketEvent => ({ type, timestamp: '2026-10-16T00:00:00Z', data: {}, sourceInstanceId: 'branch-1' })
async function advance(ms: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}
const flush = () => advance(0)
function serve(id: string, ...bodies: Array<unknown | 'defer'>) {
  replies[`/api/v1/instances/${id}/graph-state`] = [...bodies]
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeWebSocket.instances = []
  replies = {}
  lastReply = {}
  deferreds = []
  fetchMock.mockReset()
  fetchMock.mockImplementation((url: string) => {
    const queue = replies[url]
    if (!queue && !(url in lastReply)) {
      return Promise.resolve({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({ message: 'not found' }) })
    }
    const next = queue && queue.length > 0 ? queue.shift() : lastReply[url]
    if (next === 'defer') {
      return new Promise((resolve) => {
        deferreds.push({
          resolve: (body) => resolve({ ok: true, json: async () => body }),
          reject: (status, message) => resolve({ ok: false, status, statusText: 'err', json: async () => ({ message }) }),
        })
      })
    }
    lastReply[url] = next
    if (next instanceof Error) {
      return Promise.resolve({ ok: false, status: 500, statusText: 'Internal Server Error', json: async () => ({ message: next.message }) })
    }
    return Promise.resolve({ ok: true, json: async () => next })
  })
  vi.stubGlobal('fetch', fetchMock)
  vi.stubGlobal('WebSocket', FakeWebSocket)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('isGraphStateRefreshEvent', () => {
  it('accepts emitted node.*, instance.*, group.*, interactive.ended and subtree.inactive events', () => {
    for (const t of ['node.started', 'node.completed', 'node.failed', 'instance.completed', 'instance.paused', 'group.launched', 'group.task_updated', 'group.decided', 'group.resolved', 'interactive.ended', 'subtree.inactive']) {
      expect(isGraphStateRefreshEvent(t)).toBe(true)
    }
  })
  it('rejects chat traffic and malformed types', () => {
    for (const t of ['interactive.token', 'interactive.message', 'interactive.history', 'interactive.error', 'node', 'instance', 'group', '', 'nodes.started']) {
      expect(isGraphStateRefreshEvent(t)).toBe(false)
    }
  })
})

describe('useGraphState — initial load and subtree subscription', () => {
  it('fetches the aggregate for the instance and opens a SUBTREE WebSocket', async () => {
    serve('root-1', state())
    const { result } = renderHook(() => useGraphState('root-1'))
    expect(result.current.loading).toBe(true)
    await flush()
    expect(graphStateCalls('root-1')).toBe(1)
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toBeNull()
    expect(result.current.state?.version).toBe('v1')
    expect(latestSocket().url).toMatch(/\/api\/v1\/instances\/root-1\/ws\?subtree=1$/)
  })
  it('reports connected once the subtree socket opens', async () => {
    serve('root-1', state())
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    expect(result.current.connected).toBe(false)
    act(() => latestSocket().open())
    expect(result.current.connected).toBe(true)
  })
})

describe('useGraphState — aggregate data provenance', () => {
  it('retains descendant identity, redaction and frozen results while independent work remains active', async () => {
    const childNode = { instanceId: 'branch-1', nodeId: 'a1', state: 'running' }
    const panel = {
      groupRunId: 'gr-1', instanceId: 'root-1', nodeId: 'g', form: 'branches',
      completionRule: 'first_success', unfinishedBehavior: 'finish_independently',
      gatherMode: 'results', state: 'done',
      tasks: [{
        key: 'a', kind: 'branch', origin: 'a1', childInstanceId: 'branch-1',
        liveStatus: 'running', frozenStatus: 'unfinished', blocking: false,
        continuingIndependently: true, cancelRequested: false, redacted: false,
      }],
      progress: { total: 1, completed: 0, failed: 0, cancelled: 0, active: 1, notStarted: 0 },
      waitingReasons: null,
      finalOutput: { outcome: 'success', byKey: { a: { status: 'unfinished' } } },
    }
    const wire = {
      ...done('independent-running'), active: true,
      contexts: [
        ...done('root-done').contexts,
        { instanceId: 'branch-1', parentInstanceId: 'root-1', groupRunId: 'gr-1', taskKey: 'a', kind: 'branch', state: 'running', blocking: false, redacted: false, nodes: [childNode] },
        { instanceId: '00000000-0000-0000-0000-000000000000', kind: 'workflow', state: 'unknown', blocking: false, redacted: true, nodes: null },
      ],
      groups: [panel],
    }
    serve('root-1', wire, {
      ...wire, version: 'independent-completed', active: false,
      contexts: [wire.contexts[0], { ...wire.contexts[1], state: 'completed', nodes: [{ ...childNode, state: 'completed' }] }, wire.contexts[2]],
      groups: [{ ...panel, tasks: [{ ...panel.tasks[0], liveStatus: 'completed', continuingIndependently: false }], progress: { ...panel.progress, active: 0, completed: 1 } }],
    })
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    expect(result.current.state?.contexts[0].state).toBe('completed')
    expect(result.current.state?.contexts[1].nodes).toEqual([childNode])
    expect(result.current.state?.contexts[2]).toMatchObject({ redacted: true, nodes: [] })
    expect(result.current.state?.groups[0].waitingReasons).toEqual([])
    expect(result.current.connected).toBe(true)
    expect(liveSockets()).toHaveLength(1)

    act(() => latestSocket().emit(ev('instance.completed')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)

    expect(result.current.state?.contexts[1].nodes[0]).toEqual({ ...childNode, state: 'completed' })
    expect(result.current.state?.groups[0].tasks[0]).toMatchObject({ childInstanceId: 'branch-1', liveStatus: 'completed', frozenStatus: 'unfinished' })
    expect(result.current.state?.groups[0].finalOutput).toEqual(panel.finalOutput)
    expect(result.current.state?.active).toBe(false)
    expect(liveSockets()).toHaveLength(0)
  })
})

describe('useGraphState — event-driven refresh', () => {
  it('coalesces a burst of descendant events into ONE debounced refetch', async () => {
    serve('root-1', state(), state({ version: 'v2' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    act(() => {
      latestSocket().emit(ev('node.started'))
      latestSocket().emit(ev('group.task_updated'))
      latestSocket().emit(ev('instance.completed'))
    })
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS - 1)
    expect(graphStateCalls('root-1')).toBe(1)
    await advance(1)
    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state?.version).toBe('v2')
  })
  it('refetches on interactive.ended but ignores streaming chat tokens', async () => {
    serve('root-1', state(), state({ version: 'v2' }))
    renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    act(() => latestSocket().emit(ev('interactive.token')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS * 2)
    expect(graphStateCalls('root-1')).toBe(1)
    act(() => latestSocket().emit(ev('interactive.ended')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)
    expect(graphStateCalls('root-1')).toBe(2)
  })
  it('keeps the same state object when the refetched version is unchanged', async () => {
    serve('root-1', state(), state())
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    const first = result.current.state
    act(() => latestSocket().open())
    act(() => latestSocket().emit(ev('node.completed')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)
    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state).toBe(first)
  })
})

describe('useGraphState — bounded polling while active', () => {
  it('recovers a dropped final event by polling every GRAPH_STATE_POLL_MS while active', async () => {
    serve('root-1', state(), done('v-final'))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    expect(result.current.state?.active).toBe(true)
    await advance(GRAPH_STATE_POLL_MS - 1)
    expect(graphStateCalls('root-1')).toBe(1)
    await advance(1)
    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state?.version).toBe('v-final')
    expect(result.current.state?.contexts[0].state).toBe('completed')
  })
  it('closes the subtree socket and reduces polling once the subtree is inactive', async () => {
    serve('root-1', state(), done('v-final'))
    renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    await advance(GRAPH_STATE_POLL_MS)
    expect(graphStateCalls('root-1')).toBe(2)
    await advance(GRAPH_STATE_POLL_MS * 4)
    expect(graphStateCalls('root-1')).toBe(2)
    expect(liveSockets()).toHaveLength(0)
    await advance(10_000)
    expect(liveSockets()).toHaveLength(0)
  })
  it('does not poll an inactive aggregate at the active-work frequency', async () => {
    serve('root-1', done('v-done'))
    renderHook(() => useGraphState('root-1'))
    await flush()
    await advance(GRAPH_STATE_POLL_MS)
    expect(graphStateCalls('root-1')).toBe(1)
    expect(liveSockets()).toHaveLength(0)
  })
  it('discovers revived work without a socket event or remount', async () => {
    serve('root-1', done('before-retry'), state({ version: 'retried' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    expect(result.current.state?.active).toBe(false)
    expect(liveSockets()).toHaveLength(0)
    await advance(30_000)
    expect(result.current.state?.version).toBe('retried')
    expect(result.current.state?.contexts[0].nodes[0].state).toBe('running')
    expect(liveSockets()).toHaveLength(1)
  })
  it('skips a poll tick while a previous request is still in flight', async () => {
    serve('root-1', state(), 'defer')
    renderHook(() => useGraphState('root-1'))
    await flush()
    await advance(GRAPH_STATE_POLL_MS)
    expect(graphStateCalls('root-1')).toBe(2)
    await advance(GRAPH_STATE_POLL_MS)
    expect(graphStateCalls('root-1')).toBe(2)
  })
})

describe('useGraphState — automatic failure recovery', () => {
  it.each([401, 403, 404, 501])('stops polling and reconnecting after HTTP %s', async (status) => {
    serve('root-1', 'defer')
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().close()) // a reconnect is already scheduled
    await act(async () => { deferreds[0].reject(status, 'unavailable') })
    await advance(60_000)
    expect(result.current.error).toBe(`API ${status}: unavailable`)
    expect(liveSockets()).toHaveLength(0)
    expect(FakeWebSocket.instances).toHaveLength(1)
    expect(graphStateCalls('root-1')).toBe(1)
  })

  it('clears previously authorized data when a refresh loses permission', async () => {
    serve('root-1', state(), 'defer')
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    await advance(GRAPH_STATE_POLL_MS)
    await act(async () => { deferreds[0].reject(404, 'not found') })
    expect(result.current.state).toBeNull()
    expect(liveSockets()).toHaveLength(0)
  })

  it('retries a transient initial failure without a new event', async () => {
    serve('root-1', new Error('temporarily unavailable'), state({ version: 'recovered' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    expect(result.current.error).toContain('temporarily unavailable')
    await advance(GRAPH_STATE_POLL_MS)
    expect(result.current.state?.version).toBe('recovered')
    expect(result.current.error).toBeNull()
  })

  it('suspends socket attempts while the initial read keeps failing, then subscribes once it succeeds', async () => {
    serve('root-1', new Error('root storage unavailable'))
    const { result } = renderHook(() => useGraphState('root-1'))
    // The server rejects the handshake for the same root read (HTTP 500).
    act(() => latestSocket().close())
    await flush()
    await advance(3 * GRAPH_STATE_POLL_MS)
    expect(result.current.state).toBeNull()
    expect(result.current.error).toContain('root storage unavailable')
    expect(graphStateCalls('root-1')).toBe(4) // polling still retries the read
    expect(FakeWebSocket.instances).toHaveLength(1) // no 3s reconnect loop
    expect(liveSockets()).toHaveLength(0)

    serve('root-1', state({ version: 'recovered' }))
    await advance(GRAPH_STATE_POLL_MS)
    expect(result.current.state?.version).toBe('recovered')
    expect(result.current.error).toBeNull()
    expect(FakeWebSocket.instances).toHaveLength(2)
    expect(liveSockets()).toHaveLength(1)
    expect(latestSocket().url).toContain('?subtree=1')
  })

  it.each(['initial', 'poll'] as const)('recovers a stalled %s request and ignores its eventual stale response', async (phase) => {
    serve('root-1', ...(phase === 'poll' ? [state()] : []), 'defer', state({ version: 'recovered' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    if (phase === 'poll') await advance(GRAPH_STATE_POLL_MS)
    const [, init] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1]
    // The fake intentionally ignores abort, exercising a response that never
    // settles even after transport cancellation.
    await advance(15_000)
    expect(result.current.loading).toBe(false)
    // Initial-load expiry can coincide with a poll tick that recovers at once.
    if (phase === 'poll') expect(result.current.error).toMatch(/timed out/i)
    expect(init?.signal.aborted).toBe(true)
    await advance(GRAPH_STATE_POLL_MS)
    expect(result.current.state?.version).toBe('recovered')
    expect(result.current.error).toBeNull()
    await act(async () => { deferreds[0].resolve(done('too-late')) })
    expect(result.current.state?.version).toBe('recovered')
  })

  it('aborts the old subject request and cancels its deadline when disabled', async () => {
    serve('root-1', 'defer')
    const { result, rerender } = renderHook(({ enabled }) => useGraphState('root-1', { enabled }), { initialProps: { enabled: true } })
    await flush()
    const [, init] = fetchMock.mock.calls[0]
    rerender({ enabled: false })
    await flush()
    expect(init?.signal.aborted).toBe(true)
    await advance(60_000)
    expect(graphStateCalls('root-1')).toBe(1)
    expect(result.current.error).toBeNull()
    expect(result.current.loading).toBe(false)
  })
})

describe('useGraphState — reconnect resync', () => {
  it('reconnects a dropped subtree socket while active and refetches on reconnect', async () => {
    serve('root-1', state(), state({ version: 'v2' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    const first = latestSocket()
    act(() => first.open())
    act(() => first.close())
    expect(result.current.connected).toBe(false)
    await advance(3000)
    const second = latestSocket()
    expect(second).not.toBe(first)
    expect(second.url).toContain('?subtree=1')
    expect(graphStateCalls('root-1')).toBe(1)
    act(() => second.open())
    await flush()
    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state?.version).toBe('v2')
  })
})

describe('useGraphState — stale and failed responses', () => {
  it('reports loading until the newest manual refetch settles', async () => {
    serve('root-1', state(), 'defer')
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => { void result.current.refetch() })
    expect(result.current.loading).toBe(true)
    await act(async () => { deferreds[0].resolve(state({ version: 'v2' })) })
    expect(result.current.loading).toBe(false)
    expect(result.current.state?.version).toBe('v2')
  })

  it('ignores an out-of-order (older) response that resolves after a newer one', async () => {
    serve('root-1', 'defer', 'defer')
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    act(() => latestSocket().emit(ev('group.decided')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)
    expect(deferreds).toHaveLength(2)
    await act(async () => {
      deferreds[1].resolve(state({ version: 'v-new' }))
      await vi.advanceTimersByTimeAsync(0)
    })
    await act(async () => {
      deferreds[0].resolve(state({ version: 'v-old' }))
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.state?.version).toBe('v-new')
    expect(result.current.loading).toBe(false)
  })
  it('ignores an older failure after a newer successful response', async () => {
    serve('root-1', 'defer', state({ version: 'recovered' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    await act(async () => { await result.current.refetch() })
    await act(async () => { deferreds[0].reject(500, 'stale failure') })
    expect(result.current.state?.version).toBe('recovered')
    expect(result.current.error).toBeNull()
    expect(result.current.loading).toBe(false)
  })
  it('surfaces an error on the first load and clears it on the next successful refetch', async () => {
    serve('root-1', new Error('boom'), state())
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    expect(result.current.error).toBe('API 500: boom')
    expect(result.current.state).toBeNull()
    expect(result.current.loading).toBe(false)
    await act(async () => { await result.current.refetch() })
    expect(result.current.error).toBeNull()
    expect(result.current.state?.version).toBe('v1')
  })
  it('keeps the last good state when a later refetch fails', async () => {
    serve('root-1', state(), new Error('flaky'))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    await advance(GRAPH_STATE_POLL_MS)
    expect(result.current.error).toBe('API 500: flaky')
    expect(result.current.state?.version).toBe('v1')
  })
})

describe('useGraphState — disabled and switching', () => {
  it('does nothing without an instance id', async () => {
    const { result } = renderHook(() => useGraphState(undefined))
    await flush()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(result.current.loading).toBe(false)
    expect(result.current.state).toBeNull()
  })
  it('does nothing while enabled is false', async () => {
    serve('root-1', state())
    renderHook(() => useGraphState('root-1', { enabled: false }))
    await flush()
    await advance(GRAPH_STATE_POLL_MS * 2)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(FakeWebSocket.instances).toHaveLength(0)
  })
  it('drops the previous aggregate and ignores its late response when the instance changes', async () => {
    serve('root-1', 'defer')
    serve('root-2', state({ rootInstanceId: 'root-2', version: 'r2' }))
    const { result, rerender } = renderHook(({ id }: { id: string }) => useGraphState(id), { initialProps: { id: 'root-1' } })
    await flush()
    rerender({ id: 'root-2' })
    await flush()
    await act(async () => {
      deferreds[0].resolve(state({ version: 'late-root-1' }))
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.state?.rootInstanceId).toBe('root-2')
    expect(result.current.state?.version).toBe('r2')
    expect(latestSocket().url).toContain('/instances/root-2/ws?subtree=1')
  })
  it('continues polling the new subject even if an old request never settles', async () => {
    serve('root-1', 'defer')
    serve('root-2', state({ rootInstanceId: 'root-2', version: 'r2' }), {
      ...done('r2-done'), rootInstanceId: 'root-2',
    })
    const { result, rerender } = renderHook(({ id }: { id: string }) => useGraphState(id), { initialProps: { id: 'root-1' } })
    await flush()
    rerender({ id: 'root-2' })
    await flush()
    await advance(GRAPH_STATE_POLL_MS)
    expect(graphStateCalls('root-2')).toBe(2)
    expect(result.current.state?.version).toBe('r2-done')
    expect(liveSockets()).toHaveLength(0)
  })
  it('cancels a previous subject event debounce when switching instances', async () => {
    serve('root-1', state())
    serve('root-2', state({ rootInstanceId: 'root-2', version: 'r2' }))
    const { rerender } = renderHook(({ id }: { id: string }) => useGraphState(id), { initialProps: { id: 'root-1' } })
    await flush()
    act(() => latestSocket().emit(ev('node.completed')))
    rerender({ id: 'root-2' })
    await flush()
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)
    expect(graphStateCalls('root-2')).toBe(1)
  })
  it('stops polling and cancels a pending debounce on unmount', async () => {
    serve('root-1', state())
    const { unmount } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    act(() => latestSocket().emit(ev('node.started')))
    unmount()
    await advance(GRAPH_STATE_POLL_MS * 2)
    expect(graphStateCalls('root-1')).toBe(1)
    expect(liveSockets()).toHaveLength(0)
  })
})
