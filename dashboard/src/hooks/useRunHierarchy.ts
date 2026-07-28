import { useCallback, useRef, useState } from 'react'
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

      setLevels((prev) => ({ ...prev, [id]: { rows: prev[id]?.rows ?? EMPTY, loading: true, error: null } }))
      try {
        const rows = await api.instances.list({ parent: id })
        setLevels((prev) => ({ ...prev, [id]: { rows, loading: false, error: null } }))
      } catch (err) {
        const msg = err instanceof Error ? err.message : 'Failed to load child runs'
        setLevels((prev) => ({ ...prev, [id]: { rows: EMPTY, loading: false, error: msg } }))
      }
    },
    [setExpanded, setLevels],
  )

  const invalidate = useCallback(() => {
    setExpanded(() => ({}))
    setLevels(() => ({}))
  }, [setExpanded, setLevels])

  return { isExpanded, childrenOf, isLoading, errorOf, toggle, invalidate }
}
