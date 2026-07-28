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
