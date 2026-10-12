import { renderHook, act, waitFor } from '@testing-library/react'
import { it, expect, vi, beforeEach } from 'vitest'
import type { EnrichedInstanceSummary } from '../api/types'

const listMock = vi.fn()
vi.mock('../api/client', () => ({ api: { instances: { list: (...a: unknown[]) => listMock(...a) } } }))

import { CHILD_PAGE_LIMIT, useRunHierarchy } from './useRunHierarchy'

function row(id: string, overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id,
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'running',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: `run — ${id}`,
    retryCount: 0,
    ...overrides,
  }
}

beforeEach(() => {
  listMock.mockReset()
})

// ---------- happy path ----------

it('fetches a row\'s children on first expand', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => {
    await result.current.toggle('p1')
  })

  expect(listMock).toHaveBeenCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(result.current.isExpanded('p1')).toBe(true)
  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['c1'])
})

// ---------- success ----------

it('collapsing hides the children without refetching, and re-expanding uses the cache', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.isExpanded('p1')).toBe(false)

  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.isExpanded('p1')).toBe(true)
  expect(listMock).toHaveBeenCalledTimes(1)
})

it('recursive expansion is recursive fetching, one level per request', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) => {
    if (parent === 'p1') return Promise.resolve([row('c1', { parentInstanceId: 'p1', childCount: 1 })])
    if (parent === 'c1') return Promise.resolve([row('g1', { parentInstanceId: 'c1' })])
    return Promise.resolve([])
  })
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.toggle('c1') })

  expect(listMock).toHaveBeenNthCalledWith(1, { parent: 'p1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(listMock).toHaveBeenNthCalledWith(2, { parent: 'c1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(result.current.childrenOf('c1').map((r) => r.id)).toEqual(['g1'])
  expect(result.current.isExpanded('p1')).toBe(true)
  expect(result.current.isExpanded('c1')).toBe(true)
})

it('two sibling rows expand independently', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) =>
    Promise.resolve([row(`${parent}-child`, { parentInstanceId: parent })]),
  )
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('a') })
  await act(async () => { await result.current.toggle('b') })

  expect(result.current.childrenOf('a').map((r) => r.id)).toEqual(['a-child'])
  expect(result.current.childrenOf('b').map((r) => r.id)).toEqual(['b-child'])
})

it('reports a loading state while a level is in flight', async () => {
  let resolveFn: (v: EnrichedInstanceSummary[]) => void = () => {}
  listMock.mockReturnValue(new Promise<EnrichedInstanceSummary[]>((r) => { resolveFn = r }))
  const { result } = renderHook(() => useRunHierarchy())

  act(() => { void result.current.toggle('p1') })
  await waitFor(() => expect(result.current.isLoading('p1')).toBe(true))

  await act(async () => { resolveFn([]) })
  await waitFor(() => expect(result.current.isLoading('p1')).toBe(false))
})

it('two toggles fired before the first fetch settles issue only one request', async () => {
  let resolveFn: (v: EnrichedInstanceSummary[]) => void = () => {}
  listMock.mockReturnValue(new Promise<EnrichedInstanceSummary[]>((r) => { resolveFn = r }))
  const { result } = renderHook(() => useRunHierarchy())

  // Simulates a real double-click on the row's expander: both handlers fire
  // before React has re-rendered with the first call's `expanded`/`levels`
  // update, so a stale-closure read of either would let both calls slip past
  // the "already expanded" and "already fetching" guards.
  act(() => {
    void result.current.toggle('p1')
    void result.current.toggle('p1')
  })
  await act(async () => { resolveFn([]) })

  expect(listMock).toHaveBeenCalledTimes(1)
  // The second toggle lands after the first has already flipped `expanded`,
  // so — like clicking a checkbox twice — it collapses the row again rather
  // than starting a second fetch.
  expect(result.current.isExpanded('p1')).toBe(false)
})

// ---------- failure ----------

it('confines an expansion failure to its own row', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) =>
    parent === 'bad' ? Promise.reject(new Error('API 500: boom')) : Promise.resolve([row(`${parent}-child`, { parentInstanceId: parent })]),
  )
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('bad') })
  await act(async () => { await result.current.toggle('good') })

  expect(result.current.errorOf('bad')).toContain('boom')
  expect(result.current.childrenOf('bad')).toEqual([])
  expect(result.current.errorOf('good')).toBeNull()
  expect(result.current.childrenOf('good').map((r) => r.id)).toEqual(['good-child'])
})

it('a retry after a failed expansion refetches', async () => {
  listMock
    .mockRejectedValueOnce(new Error('API 500: boom'))
    .mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.errorOf('p1')).not.toBeNull()

  await act(async () => { await result.current.toggle('p1') }) // collapse
  await act(async () => { await result.current.toggle('p1') }) // expand again
  expect(listMock).toHaveBeenCalledTimes(2)
  expect(result.current.errorOf('p1')).toBeNull()
  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['c1'])
})

// ---------- edge cases ----------

it('an unexpanded row reports no children, no loading, and no error', () => {
  const { result } = renderHook(() => useRunHierarchy())
  expect(result.current.isExpanded('nope')).toBe(false)
  expect(result.current.childrenOf('nope')).toEqual([])
  expect(result.current.isLoading('nope')).toBe(false)
  expect(result.current.errorOf('nope')).toBeNull()
  expect(listMock).not.toHaveBeenCalled()
})

it('a parent whose children all disappeared expands to an empty level, not an error', async () => {
  listMock.mockResolvedValue([])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.isExpanded('p1')).toBe(true)
  expect(result.current.childrenOf('p1')).toEqual([])
  expect(result.current.errorOf('p1')).toBeNull()
})

// Expansion is USER INTENT; the fetched rows are a cache. Only the cache goes
// stale on a refetch, so only the cache is dropped. Collapsing everything on
// any lifecycle event anywhere in the system made the tree unusable exactly
// when it mattered: watching a spawning run IS a stream of lifecycle events, so
// the tree slammed shut every few seconds, mid-inspection.
it('invalidate keeps rows expanded and refetches their levels', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { result.current.invalidate() })

  expect(result.current.isExpanded('p1')).toBe(true)
  expect(listMock).toHaveBeenCalledTimes(2)
  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['c1'])
})

// Refetching only the OPEN levels: a collapsed level has no cache worth keeping
// and fetches on its next expand anyway.
it('invalidate refetches only the expanded levels', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) =>
    Promise.resolve([row(`${parent}-child`, { parentInstanceId: parent })]),
  )
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('a') })
  await act(async () => { await result.current.toggle('b') })
  await act(async () => { await result.current.toggle('b') }) // collapse b again
  listMock.mockClear()

  await act(async () => { result.current.invalidate() })

  expect(listMock).toHaveBeenCalledTimes(1)
  expect(listMock).toHaveBeenCalledWith({ parent: 'a', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
})

// A dismissed run is hidden from the top-level QUEUE — a personal inbox — but
// not from a run TREE, which is a structural view of what a run spawned.
// Omitting them also made the expansion contradict the row's own childCount,
// which the server computes with no dismissal filter: the badge said 3 and the
// expansion showed 1, with nothing to explain the gap.
it('fetches child levels with includeDismissed so the tree matches the child count', async () => {
  listMock.mockResolvedValue([])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })

  expect(listMock).toHaveBeenCalledWith(
    expect.objectContaining({ includeDismissed: true }),
  )
})

// An explicit page size: without it the request took the server's unspoken
// default, so a row with more children than that rendered a truncated list
// indistinguishable from a complete one.
it('fetches child levels with an explicit page limit', async () => {
  listMock.mockResolvedValue([])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })

  expect(listMock).toHaveBeenCalledWith(
    expect.objectContaining({ limit: CHILD_PAGE_LIMIT }),
  )
})

// ---------- patchRow ----------

it('patchRow merges fields into a cached row at any depth and leaves siblings alone', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) =>
    Promise.resolve(parent === 'p1' ? [row('c1'), row('c2')] : [row('g1', { model: 'old' })]),
  )
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.toggle('c1') })

  act(() => { result.current.patchRow('g1', { model: 'new', gitRef: 'feat/x' }) })

  const g1 = result.current.childrenOf('c1')[0]
  expect(g1.model).toBe('new')
  expect(g1.gitRef).toBe('feat/x')
  expect(result.current.childrenOf('p1').map((r) => r.model)).toEqual([undefined, undefined])
})

it('patchRow for an uncached id is a no-op that keeps the same levels', async () => {
  listMock.mockResolvedValue([row('c1')])
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })
  const before = result.current.childrenOf

  act(() => { result.current.patchRow('nope', { model: 'x' }) })

  expect(result.current.childrenOf).toBe(before)
})
