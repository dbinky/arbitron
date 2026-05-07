import { useState, useEffect, useCallback } from 'react'
import type { WorkflowStats } from '../api/types'
import { api } from '../api/client'
import styles from './UsageStats.module.css'

type RangePreset = 'today' | 'yesterday' | '7d' | '30d'

const rangeLabels: Record<RangePreset, string> = {
  today: 'Today',
  yesterday: 'Yesterday',
  '7d': 'Last 7 days',
  '30d': 'Last 30 days',
}

function computeRange(preset: RangePreset): { from: string; to: string } {
  const now = new Date()
  const to = now.toISOString()

  switch (preset) {
    case 'today': {
      const start = new Date(now)
      start.setHours(0, 0, 0, 0)
      return { from: start.toISOString(), to }
    }
    case 'yesterday': {
      const end = new Date(now)
      end.setHours(0, 0, 0, 0)
      const start = new Date(end)
      start.setDate(start.getDate() - 1)
      return { from: start.toISOString(), to: end.toISOString() }
    }
    case '7d': {
      const start = new Date(now)
      start.setDate(start.getDate() - 7)
      return { from: start.toISOString(), to }
    }
    case '30d': {
      const start = new Date(now)
      start.setDate(start.getDate() - 30)
      return { from: start.toISOString(), to }
    }
  }
}

function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds.toFixed(1)}s`
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}m ${secs.toFixed(0)}s`
}

interface UsageStatsProps {
  workflowName: string
}

export default function UsageStats({ workflowName }: UsageStatsProps) {
  const [range, setRange] = useState<RangePreset>('30d')
  const [stats, setStats] = useState<WorkflowStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchStats = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const { from, to } = computeRange(range)
      const data = await api.workflows.getStats(workflowName, from, to)
      setStats(data)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load stats')
    } finally {
      setLoading(false)
    }
  }, [workflowName, range])

  useEffect(() => {
    void fetchStats()
  }, [fetchStats])

  const successRate =
    stats && stats.totalRuns > 0
      ? ((stats.successCount / stats.totalRuns) * 100).toFixed(1)
      : '0.0'
  const failureRate =
    stats && stats.totalRuns > 0
      ? ((stats.failureCount / stats.totalRuns) * 100).toFixed(1)
      : '0.0'

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <span className={styles.title}>Usage Stats</span>
        <select
          className={styles.rangeSelect}
          value={range}
          onChange={(e) => setRange(e.target.value as RangePreset)}
        >
          {(Object.keys(rangeLabels) as RangePreset[]).map((key) => (
            <option key={key} value={key}>
              {rangeLabels[key]}
            </option>
          ))}
        </select>
      </div>

      {loading && <div className={styles.loading}>Loading stats...</div>}
      {error && <div className={styles.error}>{error}</div>}

      {!loading && !error && stats && (
        <div className={styles.grid}>
          <div className={styles.stat}>
            <div className={styles.statLabel}>Total Runs</div>
            <div className={styles.statValue}>{stats.totalRuns}</div>
          </div>
          <div className={styles.stat}>
            <div className={styles.statLabel}>Avg Duration</div>
            <div className={styles.statValue}>
              {formatDuration(stats.avgDurationSeconds)}
            </div>
          </div>
          <div className={styles.stat}>
            <div className={styles.statLabel}>Success Rate</div>
            <div className={`${styles.statValue} ${styles.statValueSuccess}`}>
              {successRate}%
            </div>
          </div>
          <div className={styles.stat}>
            <div className={styles.statLabel}>Failure Rate</div>
            <div className={`${styles.statValue} ${styles.statValueDanger}`}>
              {failureRate}%
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
