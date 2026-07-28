import { renderHook, act, waitFor } from '@testing-library/react'
import { it, expect, vi, beforeEach } from 'vitest'
import type { EnrichedInstanceSummary } from '../api/types'

const listMock = vi.fn()
vi.mock('../api/client', () => ({ api: { instances: { list: (...a: unknown[]) => listMock(...a) } } }))

import { useRunHierarchy } from './useRunHierarchy'

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

  expect(listMock).toHaveBeenCalledWith({ parent: 'p1' })
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

  expect(listMock).toHaveBeenNthCalledWith(1, { parent: 'p1' })
  expect(listMock).toHaveBeenNthCalledWith(2, { parent: 'c1' })
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

it('invalidate drops the cache so the next expand refetches', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  act(() => { result.current.invalidate() })

  expect(result.current.isExpanded('p1')).toBe(false)
  await act(async () => { await result.current.toggle('p1') })
  expect(listMock).toHaveBeenCalledTimes(2)
})
