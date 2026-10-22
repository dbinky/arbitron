import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ExecutionContext, GroupGraph } from '../api/types'
import { fixtureView } from '../graph/arranged/testFixtures'
import v2 from '../graph/arranged/groupGeometry.v2.json'
import type { WorkflowDefinition } from '../graph/types'
import type { Geometry, LayoutView, RetryAck } from '../graph/arranged/types'
import { POLL_INTERVAL_MS, MAX_BACKOFF_MS } from './useLayoutStatus'
import { useInstanceLayoutTiles } from './useInstanceLayoutTiles'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(r => { resolve = r })
  return { promise, resolve }
}
function snapshot(revision: string) {
  const f = structuredClone(v2[2]) as unknown as { definition: WorkflowDefinition; geometry: Geometry; groups: GroupGraph }
  f.definition.presentation = { revision } as WorkflowDefinition['presentation']
  return f
}
function context(id: string, revision = id): ExecutionContext {
  const f = snapshot(revision)
  return { instanceId: id, kind: 'workflow', state: 'completed', blocking: false, redacted: false, nodes: [], graph: f.definition as unknown as Record<string, unknown>, layout: f.groups }
}
const view = (id: string, status: LayoutView['displayStatus'] = 'pending', generation = 1) => {
  const f = snapshot(id)
  return fixtureView({ savedRevision: id, geometry: f.geometry, groups: f.groups, displayStatus: status, jobGeneration: generation, resultGeneration: status === 'complete' ? generation : 0 })
}
type Reply = LayoutView | RetryAck | Promise<LayoutView | RetryAck>
let replies: Map<string, Reply[]>
let calls: { id: string; signal?: AbortSignal | null; method: string; body?: string | null }[]
const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
  const id = /instances\/([^/]+)/.exec(url)![1], method = init?.method ?? 'GET'
  calls.push({ id, signal: init?.signal, method, body: init?.body as string })
  const key = method === 'POST' ? `${id}:retry` : id, steps = replies.get(key)!
  if (!steps?.length) throw new Error(`unscripted ${key}`)
  const next = steps.length > 1 ? steps.shift()! : steps[0]
  return { ok: true, json: async () => await next } as Response
})
async function tick(ms = 0) { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
beforeEach(() => { vi.useFakeTimers(); replies = new Map(); calls = []; vi.stubGlobal('fetch', fetcher) })
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })

describe('independent saved-snapshot child layout scheduler', () => {
  it('polls two completed/idle children independently, joins exact revisions and never touches root', async () => {
    replies.set('a', [view('a'), view('a', 'complete', 2)])
    replies.set('b', [view('b', 'reviewing', 3), view('b', 'complete', 3)])
    const contexts = [context('a'), context('b')], original = JSON.stringify(contexts)
    const { result } = renderHook(() => useInstanceLayoutTiles(contexts))
    await tick(); expect(result.current.views.get('a')?.displayStatus).toBe('pending')
    expect(result.current.views.get('b')?.jobGeneration).toBe(3)
    await tick(POLL_INTERVAL_MS)
    expect([...result.current.views].map(([id, v]) => [id, v.displayStatus, v.resultGeneration])).toEqual([['a', 'complete', 2], ['b', 'complete', 3]])
    const terminal = calls.length; await tick(POLL_INTERVAL_MS * 10); expect(calls).toHaveLength(terminal)
    expect(calls.every(c => c.id === 'a' || c.id === 'b')).toBe(true)
    expect(JSON.stringify(contexts)).toBe(original)
  })
  it('bounds actual GETs to four and starts queued work as a request settles', async () => {
    const pending = Array.from({ length: 7 }, () => deferred<LayoutView>())
    pending.forEach((p, i) => replies.set(String(i), [p.promise]))
    const { result } = renderHook(() => useInstanceLayoutTiles(pending.map((_, i) => context(String(i)))))
    await tick(); expect(calls.map(c => c.id)).toEqual(['0', '1', '2', '3'])
    await act(async () => pending[1].resolve(view('1', 'complete'))); await tick()
    expect(calls.map(c => c.id)).toEqual(['0', '1', '2', '3', '4'])
    expect(result.current.views.get('1')?.displayStatus).toBe('complete')
  })
  it.each(['transport', 'body'] as const)('releases stalled %s GETs at the deadline, preserves local geometry and fences late replies', async stage => {
    const late = Array.from({ length: 4 }, () => deferred<LayoutView>())
    const response = (body: LayoutView | Promise<LayoutView>) => ({ ok: true, json: async () => await body }) as Response
    const stalled = new Set<string>()
    let stall = false
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const id = /instances\/([^/]+)/.exec(url)![1]
      calls.push({ id, signal: init?.signal, method: init?.method ?? 'GET' })
      if (stall && Number(id) < 4 && !stalled.has(id)) {
        stalled.add(id)
        return stage === 'transport' ? late[Number(id)].promise.then(response) : response(late[Number(id)].promise)
      }
      return response(view(id, 'complete', stall ? 3 : 1))
    })
    const contexts = Array.from({ length: 5 }, (_, i) => context(String(i)))
    const { result } = renderHook(() => useInstanceLayoutTiles(contexts))
    await tick()
    const previous = [...result.current.views.values()]
    stall = true
    act(() => { for (const c of contexts) result.current.refresh(c.instanceId) })
    await tick()
    const blocked = calls.slice(5)
    expect(blocked.map(c => c.id)).toEqual(['0', '1', '2', '3'])
    await tick(15_000)
    expect(blocked.every(c => c.signal?.aborted)).toBe(true)
    expect(calls.slice(5).map(c => c.id)).toEqual(['0', '1', '2', '3', '4'])
    expect(result.current.views.get('4')?.jobGeneration).toBe(3)
    for (let i = 0; i < 4; i++) {
      expect(result.current.errors.get(String(i))).toMatch(/timed out/i)
      expect(result.current.views.get(String(i))).toMatchObject({ available: false, unreviewed: true, provenance: 'retained_geometry', geometry: previous[i].geometry })
      expect(result.current.views.get(String(i))?.evidence).toBeUndefined()
    }
    await tick(POLL_INTERVAL_MS)
    expect(result.current.errors.size).toBe(0)
    expect([...result.current.views.values()].every(v => v.jobGeneration === 3)).toBe(true)
    await act(async () => { late.forEach((p, i) => p.resolve(view(String(i), 'failed', 2))) })
    expect([...result.current.views.values()].every(v => v.jobGeneration === 3 && v.displayStatus === 'complete')).toBe(true)
    const count = calls.length
    await tick(30_000)
    expect(calls).toHaveLength(count)
  })
  it('fences A→B→A late GETs and retry acknowledgements by context epoch', async () => {
    const old = deferred<LayoutView>(), ack = deferred<RetryAck>()
    replies.set('a', [view('a', 'needs_attention'), old.promise, view('a', 'complete', 5)])
    replies.set('a:retry', [ack.promise]); replies.set('b', [view('b', 'complete')])
    const { result, rerender } = renderHook(({ contexts }) => useInstanceLayoutTiles(contexts), { initialProps: { contexts: [context('a')] } })
    await tick(); let retry!: Promise<void>
    act(() => { retry = result.current.retry('a'); result.current.refresh('a') }); await tick()
    rerender({ contexts: [context('b')] }); await tick()
    rerender({ contexts: [context('a')] }); await tick()
    expect(result.current.views.get('a')?.jobGeneration).toBe(5)
    await act(async () => { old.resolve(view('a', 'failed', 1)); ack.resolve({ savedRevision: 'a', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }); await retry })
    expect(result.current.views.get('a')?.jobGeneration).toBe(5)
    expect(result.current.retryErrors.size).toBe(0); expect(result.current.retrying.size).toBe(0)
  })
  it('retains only same-revision geometry on in-band outage and backs off up to the cap', async () => {
    const unavailable = { ...view('a'), available: false, geometry: undefined, contract: undefined, jobGeneration: 0, availabilityError: 'store offline' }
    replies.set('a', [view('a'), unavailable])
    const { result } = renderHook(() => useInstanceLayoutTiles([context('a')]))
    await tick(); const geometry = result.current.views.get('a')!.geometry
    await tick(POLL_INTERVAL_MS)
    expect(result.current.views.get('a')?.geometry).toEqual(geometry)
    expect(result.current.views.get('a')?.unreviewed).toBe(true); expect(result.current.views.get('a')?.evidence).toBeUndefined()
    expect(result.current.errors.get('a')).toBe('store offline')
    await tick(POLL_INTERVAL_MS); const count = calls.length
    await tick(POLL_INTERVAL_MS); expect(calls).toHaveLength(count)
    await tick(POLL_INTERVAL_MS); expect(calls).toHaveLength(count + 1)
    await tick(MAX_BACKOFF_MS * 4); expect(calls.length).toBeLessThan(15)
  })
  it('installs a retry generation floor before refetch; stale terminal reads keep polling', async () => {
    replies.set('a', [view('a', 'needs_attention'), view('a', 'complete'), view('a', 'complete', 2)])
    replies.set('b', [view('b', 'complete', 7)])
    replies.set('a:retry', [{ savedRevision: 'a', jobGeneration: 2, displayStatus: 'pending', state: 'pending' }])
    const { result } = renderHook(() => useInstanceLayoutTiles([context('a'), context('b')]))
    await tick(); const b = result.current.views.get('b')
    await act(async () => { await result.current.retry('a') })
    expect(calls.find(c => c.method === 'POST')).toMatchObject({ id: 'a', body: JSON.stringify({ expectedRevision: 'a' }) })
    expect(result.current.views.get('a')).toMatchObject({ jobGeneration: 2, displayStatus: 'pending' })
    expect(result.current.views.get('b')).toBe(b)
    await tick(POLL_INTERVAL_MS)
    expect(result.current.views.get('a')).toMatchObject({ jobGeneration: 2, displayStatus: 'complete' })
  })
  it('aborts removed and unmounted GETs through the request helper and immediately releases queue capacity', async () => {
    const slow = Array.from({ length: 6 }, () => deferred<LayoutView>())
    slow.forEach((p, i) => replies.set(String(i), [p.promise]))
    const contexts = slow.map((_, i) => context(String(i)))
    const { result, rerender, unmount } = renderHook(({ contexts }) => useInstanceLayoutTiles(contexts), { initialProps: { contexts } })
    await tick()
    const removed = calls.find(c => c.id === '0')!
    expect(removed.signal).toBeInstanceOf(AbortSignal)
    expect(removed.signal!.aborted).toBe(false)
    rerender({ contexts: contexts.slice(1) }); await tick()
    expect(removed.signal!.aborted).toBe(true)
    expect(calls.map(c => c.id)).toEqual(['0', '1', '2', '3', '4'])
    await act(async () => slow[0].resolve(view('0', 'complete')))
    expect(result.current.views.has('0')).toBe(false)
    expect(result.current.errors.has('0')).toBe(false)
    unmount()
    expect(calls.every(c => c.signal!.aborted)).toBe(true)
    const count = calls.length; await tick(POLL_INTERVAL_MS * 20); expect(calls).toHaveLength(count)
  })
  it('never regresses a newer generation while a retry acknowledgement is pending and isolates retry errors', async () => {
    const ack = deferred<RetryAck>()
    replies.set('a', [view('a', 'needs_attention'), view('a', 'complete', 4), deferred<LayoutView>().promise])
    replies.set('b', [view('b', 'needs_attention')]); replies.set('a:retry', [ack.promise])
    replies.set('b:retry', [{ savedRevision: 'wrong', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }])
    const { result } = renderHook(() => useInstanceLayoutTiles([context('a'), context('b')]))
    await tick(); let retry!: Promise<void>
    act(() => { retry = result.current.retry('a') }); await tick()
    expect([...result.current.retrying]).toEqual(['a'])
    act(() => result.current.refresh('a')); await tick()
    await act(async () => { ack.resolve({ savedRevision: 'a', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }); await retry })
    expect(result.current.views.get('a')?.jobGeneration).toBe(4)
    await act(async () => { await result.current.retry('b') })
    expect(result.current.retryErrors.get('b')).toMatch(/acknowledgement/)
    expect(result.current.retryErrors.has('a')).toBe(false)
  })
  it('discards incompatible retained geometry on an in-band outage', async () => {
    const invalid = view('a'); invalid.geometry = { ...invalid.geometry!, format: 'arbitron.layout/v1' }
    replies.set('a', [invalid, { ...view('a'), geometry: undefined, available: false }])
    const { result } = renderHook(() => useInstanceLayoutTiles([context('a')]))
    await tick(); await tick(POLL_INTERVAL_MS)
    expect(result.current.views.get('a')?.geometry).toBeUndefined()
  })
  it('rejects mismatched snapshot reads and supports explicit legacy revisions', async () => {
    replies.set('a', [view('other', 'complete'), view('a', 'complete')])
    const legacy = context('legacy'); delete (legacy.graph!.presentation as { revision?: string }).revision
    replies.set('legacy', [view('', 'legacy')])
    const { result } = renderHook(() => useInstanceLayoutTiles([context('a'), legacy]))
    await tick(); expect(result.current.views.has('a')).toBe(false)
    expect(result.current.errors.get('a')).toMatch(/revision/)
    expect(result.current.views.get('legacy')?.displayStatus).toBe('legacy')
    await tick(POLL_INTERVAL_MS); expect(result.current.views.get('a')?.savedRevision).toBe('a')
  })
})
