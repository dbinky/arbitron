import { useSchedules } from '../hooks/useSchedules'
import { relativeTime } from '../utils/time'
import styles from './SchedulesPanel.module.css'

interface SchedulesPanelProps {
  workflowName: string
}

/** Formats a schedule's last-fired timestamp as a human-readable relative time
 * (e.g. "3h ago"), or "never" when it has not fired yet. */
function formatLastFired(lastFiredAt?: string | null): string {
  if (!lastFiredAt) return 'never'
  return relativeTime(lastFiredAt)
}

/**
 * READ-ONLY list of a workflow's schedules (cron, timezone, creator, enabled
 * state, last-fired). It fetches its own data via useSchedules and degrades to an
 * empty/error state without breaking the surrounding workflow view. It
 * deliberately renders NO create/edit/enable/disable/delete/run-now control —
 * schedule management stays on REST + MCP.
 */
export default function SchedulesPanel({ workflowName }: SchedulesPanelProps) {
  const { schedules, loading, error } = useSchedules(workflowName)

  if (loading) {
    return <div className={styles.empty}>Loading schedules…</div>
  }
  if (error) {
    return <div className={styles.error}>{error}</div>
  }
  if (schedules.length === 0) {
    return <div className={styles.empty}>No schedules for this workflow.</div>
  }

  return (
    <div className={styles.container}>
      <h3 className={styles.heading}>Schedules</h3>
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Cron</th>
            <th>Timezone</th>
            <th>Creator ID</th>
            <th>State</th>
            <th>Last fired</th>
          </tr>
        </thead>
        <tbody>
          {schedules.map((s) => (
            <tr key={s.id}>
              <td>{s.cronExpr}</td>
              <td>{s.timezone}</td>
              <td>{s.creatorId}</td>
              <td className={s.enabled ? styles.enabled : styles.disabled}>
                {s.enabled ? 'enabled' : 'disabled'}
              </td>
              <td>{formatLastFired(s.lastFiredAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
