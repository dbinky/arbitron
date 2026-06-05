import { useState, useEffect, useCallback, useRef } from 'react'
import type { DashboardStats, Me, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import DepartmentSelector from '../components/DepartmentSelector'
import { formatPercent, formatDuration, throughputTotal } from './dashboardFormat'
import styles from './DashboardView.module.css'

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

export default function DashboardView() {
  const [me, setMe] = useState<Me | null>(null)
  // null ⇒ "all my departments" union default; [dept] ⇒ focused.
  const [selection, setSelection] = useState<string[] | null>(null)
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

  if (error) {
    return <div className={styles.container}><div className={styles.error}>{error}</div></div>
  }
  if (loading || !stats) {
    return <div className={styles.container}><div className={styles.loading}>Loading dashboard...</div></div>
  }

  const { queue, outcomes, throughput, perWorkflow } = stats
  const maxRuns = Math.max(1, ...throughput.map((b) => b.runs))

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <span className={styles.title}>Dashboard</span>
        {me && (
          <DepartmentSelector departments={me.departments} selection={selection} onChange={setSelection} />
        )}
      </div>

      <div className={styles.sectionTitle}>Queue Health</div>
      <div className={styles.grid}>
        <Tile label="Running" value={queue.running} />
        <Tile label="Queued / Backlog" value={queue.queued} />
        <Tile label="Waiting / Paused" value={queue.waiting + queue.paused} />
        <Tile label="Failures Needing Attention" value={queue.failedNeedingAttention} emphasis danger />
      </div>

      <div className={styles.sectionTitle}>Throughput ({throughputTotal(throughput)} runs)</div>
      <div className={styles.throughput}>
        {throughput.map((b) => (
          <div
            key={b.bucket}
            className={styles.bar}
            style={{ height: `${(b.runs / maxRuns) * 100}%` }}
            title={`${b.bucket}: ${b.runs}`}
          />
        ))}
      </div>

      <div className={styles.sectionTitle}>Outcomes</div>
      <div className={styles.grid}>
        <Tile label="Total Runs" value={outcomes.totalRuns} />
        <Tile label="Success Rate" valueText={formatPercent(outcomes.successRate)} success />
        <Tile label="Failure Rate" valueText={formatPercent(outcomes.failureRate)} danger />
        <Tile
          label="Avg / p95 Duration"
          valueText={`${formatDuration(outcomes.avgDurationSeconds)} / ${formatDuration(outcomes.p95DurationSeconds)}`}
        />
      </div>

      <div className={styles.sectionTitle}>Per-Workflow</div>
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Workflow</th>
            <th>Runs</th>
            <th>Failure Rate</th>
            <th>Avg Duration</th>
          </tr>
        </thead>
        <tbody>
          {perWorkflow.map((wf) => (
            <tr key={wf.workflowName}>
              <td>{wf.workflowName}</td>
              <td>{wf.runs}</td>
              <td>{formatPercent(wf.failureRate)}</td>
              <td>{formatDuration(wf.avgDurationSeconds)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

interface TileProps {
  label: string
  value?: number
  valueText?: string
  success?: boolean
  danger?: boolean
  emphasis?: boolean
}

function Tile({ label, value, valueText, success, danger, emphasis }: TileProps) {
  const valueClass = [
    styles.statValue,
    success ? styles.statValueSuccess : '',
    danger ? styles.statValueDanger : '',
  ].join(' ').trim()
  return (
    <div className={`${styles.stat} ${emphasis ? styles.statEmphasis : ''}`}>
      <div className={styles.statLabel}>{label}</div>
      <div className={valueClass}>{valueText ?? value}</div>
    </div>
  )
}
