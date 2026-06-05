import { useCallback, useEffect, useRef, useState } from 'react'
import type { DashboardStats, Me, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from './useSystemWebSocket'

/** Fixed observation window for v1 (design default). */
const WINDOW = '24h'
const DEBOUNCE_MS = 300

/** Instance lifecycle events that warrant a stats refetch. */
const STATE_CHANGE_EVENTS = new Set([
  'instance.created',
  'instance.started',
  'instance.completed',
  'instance.failed',
  'instance.cancelled',
  'instance.paused',
  'instance.resumed',
  'instance.retrying',
  'instance.recovered',
])

/** What useDashboardStats exposes to a presentational view. */
export interface UseDashboardStats {
  /** The caller identity (department list + role), loaded once on mount. */
  me: Me | null
  /** The latest stats envelope, or null until the first load resolves. */
  stats: DashboardStats | null
  loading: boolean
  error: string | null
}

/**
 * useDashboardStats owns the dashboard's data orchestration: the one-shot /me
 * identity fetch, the selection-driven stats fetch, the debounced live refetch on
 * relevant websocket events, and the loading/error state. Extracting it out of
 * DashboardView (PR #21) makes this logic — where the admin zero-state bug lived —
 * unit-testable without standing up a websocket, and keeps the view purely
 * presentational.
 *
 * @param selection - null ⇒ "all my departments" union default; [dept] ⇒ focused.
 *   A change re-fetches stats for the new scope.
 */
export function useDashboardStats(selection: string[] | null): UseDashboardStats {
  const [me, setMe] = useState<Me | null>(null)
  const [stats, setStats] = useState<DashboardStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Load /me once on mount for the department list + role.
  useEffect(() => {
    let cancelled = false
    api
      .me()
      .then((m) => { if (!cancelled) setMe(m) })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load identity') })
    return () => { cancelled = true }
  }, [])

  const fetchStats = useCallback(async () => {
    try {
      const data = await api.dashboard.stats(selection, WINDOW)
      setStats(data)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load dashboard stats')
    } finally {
      setLoading(false)
    }
  }, [selection])

  // Fetch stats once /me is known and whenever the selection changes.
  useEffect(() => {
    if (!me) return
    void fetchStats()
  }, [me, fetchStats])

  // Debounced live refresh on relevant websocket events.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  useSystemWebSocket(
    useCallback(
      (event: WebSocketEvent) => {
        if (!STATE_CHANGE_EVENTS.has(event.type)) return
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => { void fetchStats() }, DEBOUNCE_MS)
      },
      [fetchStats],
    ),
  )
  useEffect(() => () => { if (debounceRef.current) clearTimeout(debounceRef.current) }, [])

  return { me, stats, loading, error }
}
