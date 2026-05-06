import { useEffect, useState, useCallback } from 'react'
import type { InstanceSummary } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import QueueSection from '../components/QueueSection'

/** Returns an ISO date string for N days ago. */
function daysAgo(n: number): string {
  const d = new Date()
  d.setDate(d.getDate() - n)
  return d.toISOString()
}

const HISTORY_DAYS = 5

export default function QueueView() {
  const [active, setActive] = useState<InstanceSummary[]>([])
  const [failed, setFailed] = useState<InstanceSummary[]>([])
  const [completed, setCompleted] = useState<InstanceSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchAll = useCallback(async () => {
    try {
      const since = daysAgo(HISTORY_DAYS)

      const [running, waiting, queued, paused, failedRes, completedRes] =
        await Promise.all([
          api.instances.list({ state: 'running' }),
          api.instances.list({ state: 'waiting' }),
          api.instances.list({ state: 'queued' }),
          api.instances.list({ state: 'paused' }),
          api.instances.list({ state: 'failed', since }),
          api.instances.list({ state: 'completed', since }),
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
  }, [])

  useEffect(() => {
    void fetchAll()
  }, [fetchAll])

  useSystemWebSocket(
    useCallback(() => {
      void fetchAll()
    }, [fetchAll]),
  )

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
      />
    </div>
  )
}
