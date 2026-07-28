# Phase 5 — Task 5: dashboard hierarchy data layer

**Goal:** Give the dashboard the client-side pieces of the hierarchy: the `parent` list parameter, the `parentInstanceId` / `childCount` row fields, and a hook that owns per-row expansion state and lazy child fetching.

**Architecture:** `useRunHierarchy` owns expansion state and a per-parent cache of fetched children, keyed by instance id, so expansion recurses to arbitrary depth without any depth-aware code. Each expansion is ONE request for ONE level — the alternative, a recursive query returning the whole tree flat, breaks pagination (a child can land on a different page than its parent, so the tree renders with holes whose cause is invisible). A failed expansion is confined to its own row.

**Tech Stack:** React 19, TypeScript, Vitest + `@testing-library/react`, Vite.

---

### Task 5: types, client parameter, and the hierarchy hook

**Files:**
- Modify: `dashboard/src/api/types.ts` (`EnrichedInstanceSummary`)
- Modify: `dashboard/src/api/client.ts:19-27` (`ListInstancesParams`), `:84-94` (`list`)
- Create: `dashboard/src/hooks/useRunHierarchy.ts`
- Test:   `dashboard/src/hooks/useRunHierarchy.test.ts`

---

- [x] **Step 1: Write the failing test**

Create `dashboard/src/hooks/useRunHierarchy.test.ts`:

```ts
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
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

// ---------- failure ----------

it('confines an expansion failure to its own row', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) =>
    parent === 'bad' ? Promise.reject(new Error('API 500: boom')) : Promise.resolve([row('ok-child', { parentInstanceId: parent })]),
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
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work/dashboard
npx vitest run src/hooks/useRunHierarchy.test.ts
```

Expected output:

```
Error: Failed to load url ./useRunHierarchy (resolved id: .../src/hooks/useRunHierarchy)
 FAIL  src/hooks/useRunHierarchy.test.ts
Test Files  1 failed (1)
```

- [x] **Step 3: Add the row fields and the list parameter**

In `dashboard/src/api/types.ts`, add to `EnrichedInstanceSummary`:

```ts
  /** Failed node id, for the detail panel's lazy last-error load. Absent for non-failed rows. */
  failedNodeId?: string
  /** The spawning parent run. Absent on a root run — presence means "this row nests". */
  parentInstanceId?: string
  /** Number of spawned children the CALLER may see. Absent/0 renders no expander. */
  childCount?: number
```

In `dashboard/src/api/client.ts`, add to `ListInstancesParams`:

```ts
export interface ListInstancesParams {
  state?: InstanceState
  workflowName?: string
  since?: string
  limit?: number
  offset?: number
  includeDismissed?: boolean
  /**
   * Narrow by position in the spawn hierarchy: omit for all runs, 'roots' for
   * top-level runs only, or an instance UUID for that run's children. The
   * server treats this as a NARROWING that composes with — and can never
   * replace — the visibility scope it derives from the request identity.
   */
  parent?: string
}
```

and in `list`, beside the other params:

```ts
      if (params?.includeDismissed) q.set('includeDismissed', 'true')
      if (params?.parent) q.set('parent', params.parent)
```

- [x] **Step 4: Implement the hook**

Create `dashboard/src/hooks/useRunHierarchy.ts`:

```ts
import { useCallback, useState } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import { api } from '../api/client'

interface Level {
  rows: EnrichedInstanceSummary[]
  loading: boolean
  error: string | null
}

const EMPTY: EnrichedInstanceSummary[] = []

export interface RunHierarchy {
  /** Whether the given row is currently expanded. */
  isExpanded: (id: string) => boolean
  /** The fetched children of the given row (empty until fetched). */
  childrenOf: (id: string) => EnrichedInstanceSummary[]
  /** Whether the given row's level is in flight. */
  isLoading: (id: string) => boolean
  /** The last expansion error for the given row, or null. */
  errorOf: (id: string) => string | null
  /** Expand (fetching once) or collapse the given row. */
  toggle: (id: string) => Promise<void>
  /** Drop every cached level and collapse everything (used on a full refetch). */
  invalidate: () => void
}

/**
 * useRunHierarchy owns the queue list's expansion state and its per-row lazy
 * child fetching.
 *
 * Expansion is RECURSIVE FETCHING: each expand is ONE request for ONE level,
 * keyed by instance id, so the tree opens to arbitrary depth with no
 * depth-aware code. The alternative — a recursive query returning the whole
 * tree flat, assembled client-side — was rejected because it breaks
 * pagination: a child can land on a different page than its parent, so the tree
 * renders with holes whose cause is invisible to the user.
 *
 * Every level is fetched through the SAME authorized list endpoint the top
 * level uses, so each depth inherits the server's visibility scoping for free.
 *
 * An expansion failure is confined to its own row: siblings and the rest of the
 * page are unaffected, and collapsing then re-expanding retries.
 */
export function useRunHierarchy(): RunHierarchy {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [levels, setLevels] = useState<Record<string, Level>>({})

  const isExpanded = useCallback((id: string) => expanded[id] === true, [expanded])
  const childrenOf = useCallback((id: string) => levels[id]?.rows ?? EMPTY, [levels])
  const isLoading = useCallback((id: string) => levels[id]?.loading === true, [levels])
  const errorOf = useCallback((id: string) => levels[id]?.error ?? null, [levels])

  const toggle = useCallback(
    async (id: string) => {
      if (expanded[id]) {
        setExpanded((prev) => ({ ...prev, [id]: false }))
        return
      }
      setExpanded((prev) => ({ ...prev, [id]: true }))

      // Already fetched successfully: re-expanding uses the cache. A level that
      // ERRORED is not cached, so re-expanding retries.
      if (levels[id] && levels[id].error === null) return

      setLevels((prev) => ({ ...prev, [id]: { rows: prev[id]?.rows ?? EMPTY, loading: true, error: null } }))
      try {
        const rows = await api.instances.list({ parent: id })
        setLevels((prev) => ({ ...prev, [id]: { rows, loading: false, error: null } }))
      } catch (err) {
        const msg = err instanceof Error ? err.message : 'Failed to load child runs'
        setLevels((prev) => ({ ...prev, [id]: { rows: EMPTY, loading: false, error: msg } }))
      }
    },
    [expanded, levels],
  )

  const invalidate = useCallback(() => {
    setExpanded({})
    setLevels({})
  }, [])

  return { isExpanded, childrenOf, isLoading, errorOf, toggle, invalidate }
}
```

- [x] **Step 5: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work/dashboard
npx tsc --noEmit && npx vitest run src/hooks/useRunHierarchy.test.ts
```

Expected output:

```
 ✓ src/hooks/useRunHierarchy.test.ts (10)
Test Files  1 passed (1)
     Tests  10 passed (10)
```

- [x] **Step 6: Confirm the dashboard suite still passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work/dashboard
npx tsc --noEmit && npx vitest run
```

Expected: `Test Files  N passed`, no failures.

- [x] **Step 7: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add dashboard/src/api/types.ts dashboard/src/api/client.ts dashboard/src/hooks/useRunHierarchy.ts dashboard/src/hooks/useRunHierarchy.test.ts
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(dashboard): run-hierarchy data layer with lazy per-row child fetching"
```
