import { useCallback, useRef, useState } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import { api, type ListInstancesParams } from '../api/client'
import { buildHierarchyMarkers, type HierarchyMarker } from '../graph/liveSummary'

interface Level {
  rows: EnrichedInstanceSummary[]
  /** A full (re)fetch of every loaded page is in flight. */
  loading: boolean
  /** A load-more page is in flight. */
  loadingMore: boolean
  error: string | null
  /** The last fetched page was full, so more children may exist beyond it. */
  hasMore: boolean
  /** Server pages consumed, independent of the number of de-duplicated rows. */
  pages: number
  /** Child id -> marker, from the parent's authorized aggregate. */
  markers: Record<string, HierarchyMarker>
}

const EMPTY: EnrichedInstanceSummary[] = []
const NO_MARKERS: Record<string, HierarchyMarker> = {}
const NEW_LEVEL: Level = { rows: EMPTY, loading: false, loadingMore: false, error: null, hasMore: false, pages: 0, markers: NO_MARKERS }

/**
 * CHILD_PAGE_LIMIT is the explicit page size for one expansion.
 *
 * It is stated rather than inherited: without it the request took the server's
 * unspoken default, so a row with more children than that rendered a truncated
 * list INDISTINGUISHABLE from a complete one. With an explicit limit the caller
 * knows what a full page means, offers load-more after a full page, and
 * HierarchyRows can say how many are shown.
 */
export const CHILD_PAGE_LIMIT = 100

export interface RunHierarchy {
  /** Whether the given row is currently expanded. */
  isExpanded: (id: string) => boolean
  /** The fetched children of the given row (empty until fetched). */
  childrenOf: (id: string) => EnrichedInstanceSummary[]
  /** Whether the given row's level is being (re)fetched. */
  isLoading: (id: string) => boolean
  /** The last expansion, refresh or load-more error for the given row, or null. */
  errorOf: (id: string) => string | null
  /** Expand (fetching once) or collapse the given row. */
  toggle: (id: string) => Promise<void>
  /**
   * Refresh: keep every OPEN level's rows on screen while refetching all of its
   * loaded pages (stale-while-revalidate), drop collapsed levels, and PRESERVE
   * which rows are expanded. Used on every top-level refetch (lifecycle/group
   * events, polling, reconnect).
   */
  invalidate: () => void
  /**
   * Merge `fields` into every cached row whose id matches, at any depth. Used
   * to live-patch an expanded child from a node event without a refetch; a
   * row that is not cached is ignored (it fetches fresh on expand).
   */
  patchRow: (id: string, fields: Partial<EnrichedInstanceSummary>) => void
  /** Whether more children may exist beyond the loaded pages (the last page was full). */
  hasMore: (id: string) => boolean
  /** Whether a load-more page for the given row is in flight. */
  isLoadingMore: (id: string) => boolean
  /** Fetch the next CHILD_PAGE_LIMIT children of an expanded row and append them. */
  loadMore: (id: string) => Promise<void>
  /** Branch/group/task/active markers of the given row's children, keyed by child id. */
  markersOf: (id: string) => Record<string, HierarchyMarker>
}

function pageParams(parent: string, offset: number): ListInstancesParams {
  // includeDismissed: true — a dismissed run is hidden from the top-level
  // QUEUE, which is a personal inbox; it is not hidden from a run TREE, which
  // is a structural view of what a run spawned. Omitting them also made the
  // expansion contradict the row's own childCount, which is computed
  // server-side without any dismissal filter.
  const p: ListInstancesParams = { parent, limit: CHILD_PAGE_LIMIT, includeDismissed: true }
  if (offset > 0) p.offset = offset
  return p
}

/** Concatenate pages, keeping the first occurrence of each id (rows shift between page requests). */
function mergeRows(...pages: EnrichedInstanceSummary[][]): EnrichedInstanceSummary[] {
  const seen = new Set<string>()
  const out: EnrichedInstanceSummary[] = []
  for (const page of pages) {
    for (const r of page) {
      if (seen.has(r.id)) continue
      seen.add(r.id)
      out.push(r)
    }
  }
  return out
}

/** Statuses meaning the aggregate is authoritatively not readable (same set useGraphState stops on). */
const UNREADABLE_STATUSES = new Set([401, 403, 404, 501])

/**
 * Markers of `parentId`'s children from the parent's AUTHORIZED aggregate —
 * the same DTO the run views use. They are an overlay on the authoritative
 * list: an unreadable aggregate never fails the level or invents a state.
 * A hidden/absent parent (401/403/404/501) leaves the rows unannotated; a
 * transient failure (5xx, network) keeps `previous` — the last authorized
 * markers — so a refresh blip does not make them vanish (stale-while-
 * revalidate, like the level's rows).
 */
async function loadMarkers(
  parentId: string,
  previous: Record<string, HierarchyMarker>,
): Promise<Record<string, HierarchyMarker>> {
  try {
    const gs = await api.instances.graphState(parentId)
    return buildHierarchyMarkers(gs, parentId)
  } catch (err) {
    const status = err && typeof err === 'object' && 'status' in err ? err.status : undefined
    return typeof status === 'number' && UNREADABLE_STATUSES.has(status) ? NO_MARKERS : previous
  }
}

const errorText = (err: unknown, fallback: string) => (err instanceof Error ? err.message : fallback)

/**
 * useRunHierarchy owns the queue list's expansion state and its per-row lazy
 * child fetching.
 *
 * Expansion is RECURSIVE FETCHING: each expand is ONE request per page for ONE
 * level, keyed by instance id, so the tree opens to arbitrary depth with no
 * depth-aware code. Every level is fetched through the SAME authorized list
 * endpoint the top level uses, so each depth inherits the server's visibility
 * scoping for free; its markers come from the parent's authorized aggregate.
 *
 * A level is paged: the first page is CHILD_PAGE_LIMIT rows, `loadMore`
 * appends the next by offset, so every fixed task of a large group can be
 * inspected. A refresh refetches every loaded page and keeps the old rows on
 * screen until the new ones land; responses are applied in REQUEST order per
 * level, so a slow older response can never overwrite a newer one.
 *
 * An expansion failure is confined to its own row: siblings and the rest of the
 * page are unaffected, a failed refresh keeps the last rows, and collapsing
 * then re-expanding retries.
 *
 * EXPANSION STATE AND FETCHED DATA ARE SEPARATE CONCERNS. Which rows are open
 * is USER INTENT; the rows inside them are a cache. Only the cache goes stale
 * on a refetch, so only the cache is refreshed — see `invalidate`.
 */
export function useRunHierarchy(): RunHierarchy {
  const [expanded, setExpandedState] = useState<Record<string, boolean>>({})
  const [levels, setLevelsState] = useState<Record<string, Level>>({})

  // Mirrors updated synchronously before scheduling React state.
  // `toggle`/`loadMore` read these instead of the closed-over values so
  // two calls fired back to back — a real double-click — each see the OTHER's
  // effect rather than both slipping past the guards.
  const expandedRef = useRef(expanded)
  const levelsRef = useRef(levels)
  // Per-level request sequence: only the newest full fetch (and a load-more
  // issued after it) may write the level.
  const seqRef = useRef<Record<string, number>>({})

  const setExpanded = useCallback((updater: (prev: Record<string, boolean>) => Record<string, boolean>) => {
    expandedRef.current = updater(expandedRef.current)
    setExpandedState(expandedRef.current)
  }, [])
  const setLevels = useCallback((updater: (prev: Record<string, Level>) => Record<string, Level>) => {
    levelsRef.current = updater(levelsRef.current)
    setLevelsState(levelsRef.current)
  }, [])

  const isExpanded = useCallback((id: string) => expanded[id] === true, [expanded])
  const childrenOf = useCallback((id: string) => levels[id]?.rows ?? EMPTY, [levels])
  const isLoading = useCallback((id: string) => levels[id]?.loading === true, [levels])
  const errorOf = useCallback((id: string) => levels[id]?.error ?? null, [levels])
  const hasMore = useCallback((id: string) => levels[id]?.hasMore === true, [levels])
  const isLoadingMore = useCallback((id: string) => levels[id]?.loadingMore === true, [levels])
  const markersOf = useCallback((id: string) => levels[id]?.markers ?? NO_MARKERS, [levels])

  const fetchLevel = useCallback(
    async (id: string) => {
      const seq = (seqRef.current[id] ?? 0) + 1
      seqRef.current[id] = seq
      // Refetch every page already shown, so a refresh never shrinks a level
      // the user paged through.
      const pages = Math.max(1, levelsRef.current[id]?.pages ?? 0)
      const previousMarkers = levelsRef.current[id]?.markers ?? NO_MARKERS
      setLevels((prev) => ({ ...prev, [id]: { ...(prev[id] ?? NEW_LEVEL), loading: true, loadingMore: false, error: null } }))
      try {
        const [pageRows, markers] = await Promise.all([
          Promise.all(Array.from({ length: pages }, (_, i) => api.instances.list(pageParams(id, i * CHILD_PAGE_LIMIT)))),
          loadMarkers(id, previousMarkers),
        ])
        if (seqRef.current[id] !== seq) return // a newer fetch owns this level
        const last = pageRows[pageRows.length - 1]
        setLevels((prev) => ({
          ...prev,
          [id]: {
            rows: mergeRows(...pageRows),
            loading: false,
            loadingMore: false,
            error: null,
            hasMore: last.length >= CHILD_PAGE_LIMIT,
            pages,
            markers,
          },
        }))
      } catch (err) {
        if (seqRef.current[id] !== seq) return
        const msg = errorText(err, 'Failed to load child runs')
        // The last good rows stay: a failed refresh must not blank a level.
        setLevels((prev) => ({ ...prev, [id]: { ...(prev[id] ?? NEW_LEVEL), loading: false, loadingMore: false, error: msg } }))
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
      // ERRORED is retried.
      if (levelsRef.current[id] && levelsRef.current[id].error === null) return

      await fetchLevel(id)
    },
    [setExpanded, fetchLevel],
  )

  const loadMore = useCallback(
    async (id: string) => {
      const level = levelsRef.current[id]
      if (!level || level.loading || level.loadingMore || !level.hasMore) return
      const seq = seqRef.current[id] ?? 0
      const offset = level.pages * CHILD_PAGE_LIMIT
      setLevels((prev) => (prev[id] ? { ...prev, [id]: { ...prev[id], loadingMore: true, error: null } } : prev))
      try {
        const page = await api.instances.list(pageParams(id, offset))
        if (seqRef.current[id] !== seq) return // a refresh replaced this level meanwhile
        setLevels((prev) => {
          const cur = prev[id]
          if (!cur) return prev
          return {
            ...prev,
            [id]: { ...cur, rows: mergeRows(cur.rows, page), loadingMore: false, hasMore: page.length >= CHILD_PAGE_LIMIT, pages: cur.pages + 1 },
          }
        })
      } catch (err) {
        if (seqRef.current[id] !== seq) return
        const msg = errorText(err, 'Failed to load more child runs')
        setLevels((prev) => (prev[id] ? { ...prev, [id]: { ...prev[id], loadingMore: false, error: msg } } : prev))
      }
    },
    [setLevels],
  )

  const invalidate = useCallback(() => {
    // Expansion is USER INTENT and survives: collapsing everything on any
    // lifecycle event made the tree unusable precisely while watching a
    // spawning run. The open levels' rows stay on screen (stale-while-
    // revalidate) and are refetched; collapsed levels are dropped and fetch
    // fresh on their next expand.
    const open = Object.keys(expandedRef.current).filter((id) => expandedRef.current[id])
    // Dropping a collapsed level also invalidates its in-flight responses.
    for (const id of Object.keys(seqRef.current)) {
      if (!expandedRef.current[id]) seqRef.current[id]++
    }
    setLevels((prev) => {
      const next: Record<string, Level> = {}
      for (const id of open) if (prev[id]) next[id] = prev[id]
      return next
    })
    for (const id of open) {
      void fetchLevel(id)
    }
  }, [setLevels, fetchLevel])

  const patchRow = useCallback(
    (id: string, fields: Partial<EnrichedInstanceSummary>) => {
      setLevels((prev) => {
        let changed = false
        const next: Record<string, Level> = {}
        for (const [parent, level] of Object.entries(prev)) {
          if (!level.rows.some((r) => r.id === id)) {
            next[parent] = level
            continue
          }
          changed = true
          next[parent] = { ...level, rows: level.rows.map((r) => (r.id === id ? { ...r, ...fields } : r)) }
        }
        // No cached row matched: keep the same object so nothing re-renders.
        return changed ? next : prev
      })
    },
    [setLevels],
  )

  return { isExpanded, childrenOf, isLoading, errorOf, toggle, invalidate, patchRow, hasMore, isLoadingMore, loadMore, markersOf }
}
