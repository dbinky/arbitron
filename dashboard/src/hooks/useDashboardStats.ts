import { useCallback, useEffect, useRef, useState } from 'react'
import type { DashboardStats, Me, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from './useSystemWebSocket'

/** Fixed observation window for v1 (design default). */
const WINDOW = '24h'
const DEBOUNCE_MS = 300
/**
 * Trailing-edge throttle ceiling: under a continuous event stream the plain
 * debounce would keep resetting and never fire, so the dashboard would either
 * never refresh or (without this) refetch on every event. MAX_WAIT_MS guarantees
 * at most one refetch per this interval while events keep arriving (PR #21).
 */
const MAX_WAIT_MS = 2000

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

  // Live refresh on relevant websocket events: a trailing-edge throttle. A burst
  // of events coalesces into one refetch after DEBOUNCE_MS of quiet, but under a
  // sustained stream the throttle still fires at most once per MAX_WAIT_MS so the
  // dashboard stays current without refetching on every event.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const maxWaitRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const clearTimers = useCallback(() => {
    if (debounceRef.current) { clearTimeout(debounceRef.current); debounceRef.current = null }
    if (maxWaitRef.current) { clearTimeout(maxWaitRef.current); maxWaitRef.current = null }
  }, [])
  const runFetch = useCallback(() => {
    clearTimers()
    void fetchStats()
  }, [clearTimers, fetchStats])

  useSystemWebSocket(
    useCallback(
      (event: WebSocketEvent) => {
        if (!STATE_CHANGE_EVENTS.has(event.type)) return
        // Trailing debounce: restart the quiet-period timer on each event.
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(runFetch, DEBOUNCE_MS)
        // Max-wait ceiling: ensure a refetch fires even if events never stop.
        if (!maxWaitRef.current) {
          maxWaitRef.current = setTimeout(runFetch, MAX_WAIT_MS)
        }
      },
      [runFetch],
    ),
  )
  useEffect(() => clearTimers, [clearTimers])

  return { me, stats, loading, error }
}
