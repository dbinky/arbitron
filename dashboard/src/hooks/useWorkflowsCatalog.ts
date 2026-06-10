import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type { WorkflowSummary, Me } from '../api/types'

/** What useWorkflowsCatalog exposes to the catalog view. */
export interface UseWorkflowsCatalog {
  /** The full (server-scoped) workflow list, empty until the first load resolves. */
  workflows: WorkflowSummary[]
  /** The caller identity (department list + auth mode), loaded once on mount. */
  me: Me | null
  loading: boolean
  error: string | null
}

/**
 * useWorkflowsCatalog owns the catalog's read-only data orchestration: the paired
 * one-shot /workflows + /me fetch, the loading/error state, and the cancel-flag
 * that drops a resolution arriving after unmount. Extracting it out of
 * WorkflowsCatalog (matching the useSchedules / useDashboardStats convention)
 * keeps the view presentational and the fetch lifecycle unit-testable apart from
 * the markup. It deliberately keeps the manual useEffect fetch (not TanStack
 * Query/SWR — that dashboard-wide swap is a separate ticket).
 */
export function useWorkflowsCatalog(): UseWorkflowsCatalog {
  const [workflows, setWorkflows] = useState<WorkflowSummary[]>([])
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    Promise.all([api.workflows.list(), api.me()])
      .then(([wf, meResp]) => {
        if (!cancelled) {
          setWorkflows(wf)
          setMe(meResp)
        }
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load workflows')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  return { workflows, me, loading, error }
}
