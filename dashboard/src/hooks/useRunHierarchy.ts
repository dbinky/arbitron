import { useCallback, useRef, useState } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import { api } from '../api/client'

interface Level {
  rows: EnrichedInstanceSummary[]
  loading: boolean
  error: string | null
}

const EMPTY: EnrichedInstanceSummary[] = []

/**
 * CHILD_PAGE_LIMIT is the explicit page size for one expansion.
 *
 * It is stated rather than inherited: without it the request took the server's
 * unspoken default, so a row with more children than that rendered a truncated
 * list INDISTINGUISHABLE from a complete one. With an explicit limit the caller
 * knows what a full page means, and HierarchyRows can say so.
 */
export const CHILD_PAGE_LIMIT = 100

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
  /**
   * Drop every cached level, PRESERVING which rows are expanded, and refetch
   * the open ones. Used on a full refetch.
   */
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
 *
 * EXPANSION STATE AND FETCHED DATA ARE SEPARATE CONCERNS. Which rows are open
 * is USER INTENT; the rows inside them are a cache. Only the cache goes stale
 * on a refetch, so only the cache is dropped — see `invalidate`.
 */
export function useRunHierarchy(): RunHierarchy {
  const [expanded, setExpandedState] = useState<Record<string, boolean>>({})
  const [levels, setLevelsState] = useState<Record<string, Level>>({})

  // Mirrors of the state above, updated synchronously (inside the same
  // functional setState call) rather than on the next render. `toggle` reads
  // these instead of the closed-over `expanded`/`levels` so that two toggles
  // fired back to back — a real double-click on the row's expander — each see
  // the OTHER's effect immediately, rather than both reading the same
  // pre-update snapshot and both slipping past the "already expanded"/
  // "already fetching" guards to fire a duplicate request.
  const expandedRef = useRef(expanded)
  const levelsRef = useRef(levels)

  const setExpanded = useCallback((updater: (prev: Record<string, boolean>) => Record<string, boolean>) => {
    setExpandedState((prev) => (expandedRef.current = updater(prev)))
  }, [])
  const setLevels = useCallback((updater: (prev: Record<string, Level>) => Record<string, Level>) => {
    setLevelsState((prev) => (levelsRef.current = updater(prev)))
  }, [])

  const isExpanded = useCallback((id: string) => expanded[id] === true, [expanded])
  const childrenOf = useCallback((id: string) => levels[id]?.rows ?? EMPTY, [levels])
  const isLoading = useCallback((id: string) => levels[id]?.loading === true, [levels])
  const errorOf = useCallback((id: string) => levels[id]?.error ?? null, [levels])

  const fetchLevel = useCallback(
    async (id: string) => {
      setLevels((prev) => ({ ...prev, [id]: { rows: prev[id]?.rows ?? EMPTY, loading: true, error: null } }))
      try {
        // includeDismissed: true — a dismissed run is hidden from the top-level
        // QUEUE, which is a personal inbox; it is not hidden from a run TREE,
        // which is a structural view of what a run spawned. Omitting them here
        // also made the expansion contradict the row's own childCount, which is
        // computed server-side without any dismissal filter: the badge said 3
        // and the expansion showed 1, with nothing to explain the gap.
        const rows = await api.instances.list({ parent: id, limit: CHILD_PAGE_LIMIT, includeDismissed: true })
        setLevels((prev) => ({ ...prev, [id]: { rows, loading: false, error: null } }))
      } catch (err) {
        const msg = err instanceof Error ? err.message : 'Failed to load child runs'
        setLevels((prev) => ({ ...prev, [id]: { rows: EMPTY, loading: false, error: msg } }))
      }
    },
    [setLevels],
  )

  const toggle = useCallback(
    async (id: string) => {
      if (expandedRef.current[id]) {
        setExpanded((prev) => ({ ...prev, [id]: false }))
        return
      }
      setExpanded((prev) => ({ ...prev, [id]: true }))

      // Already fetched successfully: re-expanding uses the cache. A level that
      // ERRORED is not cached, so re-expanding retries.
      if (levelsRef.current[id] && levelsRef.current[id].error === null) return

      await fetchLevel(id)
    },
    [setExpanded, fetchLevel],
  )

  const invalidate = useCallback(() => {
    // Expansion is USER INTENT and survives. Collapsing everything on any
    // lifecycle event anywhere in the system — which is what dropping the
    // expanded map did — made the tree unusable precisely when it mattered:
    // watching a spawning run means watching a stream of lifecycle events, so
    // the tree slammed shut every few seconds, mid-inspection.
    const open = Object.keys(expandedRef.current).filter((id) => expandedRef.current[id])

    // The fetched rows ARE stale, so they go — but only for the open levels,
    // which are then refetched. A collapsed level has no cache worth keeping
    // and will fetch on its next expand anyway.
    setLevels(() => ({}))
    for (const id of open) {
      void fetchLevel(id)
    }
  }, [setLevels, fetchLevel])

  return { isExpanded, childrenOf, isLoading, errorOf, toggle, invalidate }
}
