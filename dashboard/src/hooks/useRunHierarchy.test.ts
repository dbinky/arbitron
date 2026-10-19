import { renderHook, act, waitFor } from '@testing-library/react'
import { it, expect, vi, beforeEach } from 'vitest'
import type { EnrichedInstanceSummary } from '../api/types'

const listMock = vi.fn()
const graphStateMock = vi.fn()
vi.mock('../api/client', () => ({
  api: {
    instances: {
      list: (...a: unknown[]) => listMock(...a),
      graphState: (...a: unknown[]) => graphStateMock(...a),
    },
  },
}))

import { CHILD_PAGE_LIMIT, useRunHierarchy } from './useRunHierarchy'
import { fanoutAggregate, fanoutContexts } from '../graph/liveSummary.fixtures'

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
  graphStateMock.mockReset()
  graphStateMock.mockRejectedValue(new Error('API 404: instance not found'))
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

// ---------- pagination (P4-T10) ----------

const many = (parent: string, n: number, from = 0) =>
  Array.from({ length: n }, (_, i) => row(`${parent}-c${from + i}`, { parentInstanceId: parent }))

// Fake keyed by (parent, limit, offset): a page is cut exactly where the server
// would cut it (adapter/sqlite/instance_repo.go:216 LIMIT ? OFFSET ?).
function pagedList(all: Record<string, EnrichedInstanceSummary[]>) {
  return ({ parent, limit, offset }: { parent: string; limit: number; offset?: number }) => {
    const rows = all[parent]
    if (!rows) return Promise.reject(new Error('API 404: instance not found'))
    const from = offset ?? 0
    return Promise.resolve(rows.slice(from, from + limit))
  }
}

it('a full first page reports more children, and loadMore appends the next page by offset', async () => {
  listMock.mockImplementation(pagedList({ p1: many('p1', 150) }))
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.childrenOf('p1')).toHaveLength(CHILD_PAGE_LIMIT)
  expect(result.current.hasMore('p1')).toBe(true)

  await act(async () => { await result.current.loadMore('p1') })

  expect(listMock).toHaveBeenLastCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(result.current.childrenOf('p1')).toHaveLength(150)
  expect(result.current.childrenOf('p1')[100].id).toBe('p1-c100')
  expect(result.current.hasMore('p1')).toBe(false)
  expect(result.current.isLoadingMore('p1')).toBe(false)
})

it('a short page reports no more children, and loadMore is then a no-op (also for an unfetched row)', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.hasMore('p1')).toBe(false)

  await act(async () => { await result.current.loadMore('p1') })
  await act(async () => { await result.current.loadMore('never-expanded') })
  expect(listMock).toHaveBeenCalledTimes(1)
})

// Children list newest-first, so a child created between two page requests
// pushes the first page's last row onto the second page.
it('loadMore de-duplicates a row that shifted across the page boundary', async () => {
  const first = many('p1', CHILD_PAGE_LIMIT)
  listMock
    .mockResolvedValueOnce(first)
    .mockResolvedValueOnce([first[CHILD_PAGE_LIMIT - 1], row('p1-older', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.loadMore('p1') })

  const ids = result.current.childrenOf('p1').map((r) => r.id)
  expect(ids).toHaveLength(CHILD_PAGE_LIMIT + 1)
  expect(new Set(ids).size).toBe(ids.length)
  expect(ids[ids.length - 1]).toBe('p1-older')
  expect(result.current.hasMore('p1')).toBe(false)
})

// Edge: >100 tasks + refresh — a refresh must never shrink a level the user
// paged through, or the 101st task disappears on the next lifecycle event.
it('a refresh after load-more refetches EVERY loaded page', async () => {
  listMock.mockImplementation(pagedList({ p1: many('p1', 150) }))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.loadMore('p1') })
  listMock.mockClear()

  await act(async () => { result.current.invalidate() })
  await waitFor(() => expect(result.current.isLoading('p1')).toBe(false))

  expect(listMock).toHaveBeenCalledTimes(2)
  expect(listMock).toHaveBeenCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(listMock).toHaveBeenCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(result.current.childrenOf('p1')).toHaveLength(150)
})

// Failure: a failed page keeps what is already shown.
it('a failed load-more keeps the loaded rows and reports the error on the row', async () => {
  listMock.mockResolvedValueOnce(many('p1', CHILD_PAGE_LIMIT)).mockRejectedValueOnce(new Error('API 500: page boom'))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  await act(async () => { await result.current.loadMore('p1') })

  expect(result.current.errorOf('p1')).toContain('page boom')
  expect(result.current.childrenOf('p1')).toHaveLength(CHILD_PAGE_LIMIT)
  expect(result.current.hasMore('p1')).toBe(true)
  expect(result.current.isLoadingMore('p1')).toBe(false)
})

it('keeps the server offset and loaded page count when pages overlap', async () => {
  const first = many('p1', CHILD_PAGE_LIMIT)
  const second = [first[99], ...many('p1', 99, 100)]
  listMock.mockResolvedValueOnce(first).mockResolvedValueOnce(second).mockResolvedValueOnce([])
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.loadMore('p1') })
  expect(result.current.childrenOf('p1')).toHaveLength(199)
  await act(async () => { await result.current.loadMore('p1') })
  expect(listMock).toHaveBeenLastCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: 200, includeDismissed: true })

  listMock.mockClear().mockResolvedValue([])
  await act(async () => { result.current.invalidate() })
  expect(listMock).toHaveBeenCalledTimes(3)
  expect(listMock).toHaveBeenCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: 200, includeDismissed: true })
})

it('two load-more calls in the same event issue only one page request', async () => {
  listMock.mockResolvedValueOnce(many('p1', CHILD_PAGE_LIMIT))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })
  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValue(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  act(() => {
    void result.current.loadMore('p1')
    void result.current.loadMore('p1')
  })
  expect(listMock).toHaveBeenCalledTimes(2)
  await act(async () => { resolve([]) })
})

// ---------- stale cache (P4-T10) ----------

it('keeps the cached rows visible while a refresh is in flight, then replaces them', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1', state: 'running' })])
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  act(() => { result.current.invalidate() })

  await waitFor(() => expect(result.current.isLoading('p1')).toBe(true))
  expect(result.current.childrenOf('p1').map((r) => r.state)).toEqual(['running'])

  await act(async () => { resolve([row('c1', { parentInstanceId: 'p1', state: 'completed' })]) })
  await waitFor(() => expect(result.current.childrenOf('p1')[0].state).toBe('completed'))
})

it('an older refresh response never overwrites a newer one', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  let resolveOld!: (v: EnrichedInstanceSummary[]) => void
  let resolveNew!: (v: EnrichedInstanceSummary[]) => void
  listMock
    .mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolveOld = r }))
    .mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolveNew = r }))
  act(() => { result.current.invalidate() })
  act(() => { result.current.invalidate() })

  await act(async () => { resolveNew([row('c1', { parentInstanceId: 'p1', state: 'completed' })]) })
  await act(async () => { resolveOld([row('c1', { parentInstanceId: 'p1', state: 'running' })]) })

  expect(result.current.childrenOf('p1')[0].state).toBe('completed')
  expect(result.current.isLoading('p1')).toBe(false)
})

it('a failed refresh keeps the last rows and reports the error, leaving the row expanded', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1' })]).mockRejectedValueOnce(new Error('API 500: boom'))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  await act(async () => { result.current.invalidate() })

  await waitFor(() => expect(result.current.errorOf('p1')).toContain('boom'))
  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['c1'])
  expect(result.current.isExpanded('p1')).toBe(true)
})

it('a collapsed level invalidated during a fetch cannot be resurrected by its old response', async () => {
  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  const { result } = renderHook(() => useRunHierarchy())
  act(() => { void result.current.toggle('p1') })
  await act(async () => { await result.current.toggle('p1') })
  act(() => { result.current.invalidate() })
  await act(async () => { resolve([row('stale', { parentInstanceId: 'p1' })]) })
  expect(result.current.childrenOf('p1')).toEqual([])

  listMock.mockResolvedValueOnce([row('fresh', { parentInstanceId: 'p1' })])
  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['fresh'])
  expect(listMock).toHaveBeenCalledTimes(2)
})

// ---------- markers (P4-T10) ----------

it("builds branch/task/active markers for each child from the parent's authorized graph state", async () => {
  listMock.mockResolvedValue([row('br-a', { parentInstanceId: 'root' }), row('br-b', { parentInstanceId: 'root' })])
  graphStateMock.mockImplementation((id: string) =>
    id === 'root' ? Promise.resolve(fanoutAggregate()) : Promise.reject(new Error('API 404: instance not found')),
  )
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('root') })

  expect(graphStateMock).toHaveBeenCalledWith('root')
  expect(result.current.markersOf('root')['br-a']).toEqual({
    kind: 'branch',
    groupNodeId: 'g',
    groupLabel: 'Fan out',
    taskKey: 'a',
    blocking: true,
    independent: false,
    activeSummary: 'running: A2',
  })
  expect(result.current.markersOf('root')['br-b'].activeSummary).toBe('running: B1')
})

// Error: markers are an overlay on the authoritative list. An unreadable
// aggregate (403/404 hidden parent, 5xx) leaves the rows listed and
// unannotated — it neither fails the level nor invents a state.
it('an unreadable graph state leaves the level listed with no markers and no error', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  graphStateMock.mockRejectedValue(new Error('API 403: forbidden'))
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })

  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['c1'])
  expect(result.current.markersOf('p1')).toEqual({})
  expect(result.current.errorOf('p1')).toBeNull()
})

// Failure: a transient aggregate failure on REFRESH must not make the markers
// vanish — the level keeps its last authorized markers (stale-while-revalidate,
// like its rows). Losing permission (401/403/404) is authoritative and clears them.
it('a transient graph-state failure on refresh keeps the last markers; a permission loss clears them', async () => {
  listMock.mockResolvedValue([row('br-a', { parentInstanceId: 'root' }), row('br-b', { parentInstanceId: 'root' })])
  graphStateMock
    .mockResolvedValueOnce(fanoutAggregate())
    .mockRejectedValueOnce(Object.assign(new Error('API 500: aggregate down'), { status: 500 }))
    .mockRejectedValueOnce(new TypeError('Failed to fetch'))
    .mockRejectedValueOnce(Object.assign(new Error('API 403: forbidden'), { status: 403 }))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('root') })
  expect(result.current.markersOf('root')['br-b'].activeSummary).toBe('running: B1')

  await act(async () => { result.current.invalidate() })
  await waitFor(() => expect(graphStateMock).toHaveBeenCalledTimes(2))
  await waitFor(() => expect(result.current.isLoading('root')).toBe(false))
  expect(result.current.markersOf('root')['br-b'].activeSummary).toBe('running: B1')
  expect(result.current.markersOf('root')['br-a'].taskKey).toBe('a')
  expect(result.current.errorOf('root')).toBeNull()

  await act(async () => { result.current.invalidate() })
  await waitFor(() => expect(graphStateMock).toHaveBeenCalledTimes(3))
  await waitFor(() => expect(result.current.isLoading('root')).toBe(false))
  expect(result.current.markersOf('root')['br-b'].activeSummary).toBe('running: B1')

  await act(async () => { result.current.invalidate() })
  await waitFor(() => expect(graphStateMock).toHaveBeenCalledTimes(4))
  await waitFor(() => expect(result.current.markersOf('root')).toEqual({}))
  expect(result.current.childrenOf('root').map((r) => r.id)).toEqual(['br-a', 'br-b'])
})

it('markers refresh with their level: a branch that finished loses its active summary', async () => {
  listMock.mockResolvedValue([row('br-a', { parentInstanceId: 'root' }), row('br-b', { parentInstanceId: 'root' })])
  graphStateMock
    .mockResolvedValueOnce(fanoutAggregate())
    .mockResolvedValueOnce(fanoutAggregate({ version: 'v2', contexts: fanoutContexts('completed') }))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('root') })
  expect(result.current.markersOf('root')['br-b'].activeSummary).toBe('running: B1')

  await act(async () => { result.current.invalidate() })

  await waitFor(() => expect(result.current.markersOf('root')['br-b'].activeSummary).toBeNull())
  expect(graphStateMock).toHaveBeenCalledTimes(2)
  expect(result.current.markersOf('root')['br-a'].activeSummary).toBe('running: A2')
})
