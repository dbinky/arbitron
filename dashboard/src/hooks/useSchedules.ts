import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type { Schedule } from '../api/types'

/** What useSchedules exposes to a presentational view. */
export interface UseSchedules {
  /** The workflow's schedules, or an empty list until the first load resolves. */
  schedules: Schedule[]
  loading: boolean
  error: string | null
}

/**
 * useSchedules owns the read-only fetch lifecycle for one workflow's schedules:
 * the scoped list fetch, the loading/error state, and the cancel-flag that drops
 * a stale resolution when the workflow prop changes mid-flight. Extracting it out
 * of SchedulesPanel keeps the panel purely presentational and makes the fetch
 * lifecycle unit-testable apart from the markup. It deliberately keeps the manual
 * useEffect fetch (NOT TanStack Query/SWR — that dashboard-wide swap is a separate
 * ticket); this is the single call site to migrate when that lands.
 *
 * @param workflowName - the workflow whose schedules to load. A change re-fetches.
 */
export function useSchedules(workflowName: string): UseSchedules {
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    api.schedules
      .list(workflowName)
      .then((data) => {
        if (!cancelled) setSchedules(data)
      })
      .catch(() => {
        if (!cancelled) setError('Failed to load schedules')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [workflowName])

  return { schedules, loading, error }
}
