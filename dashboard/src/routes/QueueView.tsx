import { useEffect, useState, useCallback, useRef } from 'react'
import type { InstanceSummary, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import { useRefetchOnReconnect } from '../hooks/useRefetchOnReconnect'
import QueueSection from '../components/QueueSection'

/** Returns an ISO date string for N days ago. */
function daysAgo(n: number): string {
  const d = new Date()
  d.setDate(d.getDate() - n)
  return d.toISOString()
}

const HISTORY_DAYS = 5
const DEBOUNCE_MS = 300

/** Event types that indicate a state change worth refetching for. */
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

export default function QueueView() {
  const [active, setActive] = useState<InstanceSummary[]>([])
  const [failed, setFailed] = useState<InstanceSummary[]>([])
  const [completed, setCompleted] = useState<InstanceSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showDismissed, setShowDismissed] = useState(false)

  const fetchAll = useCallback(async () => {
    try {
      const since = daysAgo(HISTORY_DAYS)

      // Seam: `showDismissed` drives the FETCH (includeDismissed), per-row
      // `inst.dismissed` from the server drives the RENDER (Restore vs Dismiss,
      // dimming). The server reconciles the two — do NOT re-thread showDismissed
      // down into the rows or it reintroduces the toggle/refetch flicker.
      const [running, waiting, queued, paused, failedRes, completedRes] =
        await Promise.all([
          api.instances.list({ state: 'running' }),
          api.instances.list({ state: 'waiting' }),
          api.instances.list({ state: 'queued' }),
          api.instances.list({ state: 'paused' }),
          api.instances.list({ state: 'failed', since, includeDismissed: showDismissed }),
          api.instances.list({ state: 'completed', since, includeDismissed: showDismissed }),
        ])

      setActive([...running, ...waiting, ...queued, ...paused])
      setFailed(failedRes)
      setCompleted(completedRes)
      setError(null)
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Unknown error'
      setError(msg)
    } finally {
      setLoading(false)
    }
  }, [showDismissed])

  const handleDismiss = useCallback(async (id: string) => {
    try {
      await api.instances.dismiss(id)
      await fetchAll()
    } catch (err) {
      // Route failures through the shared error surface so the unhappy path
      // isn't silent (a swallowed catch reads as "broken" to the user).
      setError(err instanceof Error ? err.message : 'Failed to dismiss instance')
    }
  }, [fetchAll])

  const handleRestore = useCallback(async (id: string) => {
    try {
      await api.instances.restore(id)
      await fetchAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to restore instance')
    }
  }, [fetchAll])

  const handleDismissAll = useCallback(async (state: 'failed' | 'completed') => {
    try {
      // Scope to the same window the Failed/Completed sections show (daysAgo(5)),
      // so [Dismiss All] dismisses exactly "every item currently listed".
      await api.instances.dismissAll(state, daysAgo(HISTORY_DAYS))
      await fetchAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to dismiss instances')
    }
  }, [fetchAll])

  useEffect(() => {
    void fetchAll()
  }, [fetchAll])

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const { connected } = useSystemWebSocket(
    useCallback(
      (event: WebSocketEvent) => {
        if (!STATE_CHANGE_EVENTS.has(event.type)) return
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => {
          void fetchAll()
        }, DEBOUNCE_MS)
      },
      [fetchAll],
    ),
  )

  // The system WebSocket auto-reconnects after a drop, but events emitted
  // while it was down are not replayed. Resync on reconnect so the queue
  // doesn't sit stale until the next event arrives.
  useRefetchOnReconnect(connected, fetchAll)

  // Clean up debounce timer on unmount.
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [])

  if (loading) {
    return (
      <div style={{ padding: 32, color: 'var(--text-muted)', textAlign: 'center' }}>
        Loading instances...
      </div>
    )
  }

  if (error) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <div style={{ color: 'var(--state-failed)', marginBottom: 12 }}>
          Failed to load instances: {error}
        </div>
        <button
          onClick={() => { setLoading(true); void fetchAll() }}
          style={{
            padding: '6px 16px',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
            color: 'var(--text-primary)',
            cursor: 'pointer',
          }}
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div style={{ padding: '16px 0' }}>
      <QueueSection
        title="Active"
        instances={active}
        titleColor="var(--state-running)"
        countBg="rgba(52, 152, 219, 0.2)"
        countColor="var(--state-running)"
        timeMode="elapsed"
      />
      <QueueSection
        title="Failed"
        instances={failed}
        titleColor="var(--state-failed)"
        countBg="rgba(231, 76, 60, 0.2)"
        countColor="var(--state-failed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="relative"
        onDismissAll={() => handleDismissAll('failed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />
      <QueueSection
        title="Completed"
        instances={completed}
        titleColor="var(--state-completed)"
        countBg="rgba(46, 204, 113, 0.2)"
        countColor="var(--state-completed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="relative"
        defaultCollapsed
        onDismissAll={() => handleDismissAll('completed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />

      <div style={{ textAlign: 'center', padding: '16px 0' }}>
        <button
          type="button"
          onClick={() => setShowDismissed((s) => !s)}
          style={{
            padding: '6px 16px',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
            color: 'var(--text-primary)',
            cursor: 'pointer',
          }}
        >
          {showDismissed ? 'Hide Dismissed' : 'Show Dismissed'}
        </button>
      </div>
    </div>
  )
}
